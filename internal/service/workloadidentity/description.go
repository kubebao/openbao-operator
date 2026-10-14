package workloadidentity

import (
	"fmt"
	"strings"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/hardenedcontract"
)

type StorageIdentityMode string

const (
	StorageIdentityModeSecret           StorageIdentityMode = "secret"
	StorageIdentityModeWorkloadIdentity StorageIdentityMode = "workloadIdentity"
	StorageIdentityModeAmbient          StorageIdentityMode = "ambient"
)

type StorageIdentityDescription struct {
	Mode        StorageIdentityMode
	Reason      string
	Message     string
	FailureHint string
}

// DescribeStorageIdentity classifies how backup or restore Jobs are expected
// to authenticate to object storage.
func DescribeStorageIdentity(target openbaov1alpha1.BackupTarget, serviceAccountName string) StorageIdentityDescription {
	provider := normalizeStorageProvider(target.Provider)
	sa := strings.TrimSpace(serviceAccountName)
	refName := ""
	if target.CredentialsSecretRef != nil {
		refName = strings.TrimSpace(target.CredentialsSecretRef.Name)
	}

	if refName != "" {
		return StorageIdentityDescription{
			Mode:        StorageIdentityModeSecret,
			Reason:      readyReason,
			Message:     secretBackedStorageMessage(provider, refName, target, sa),
			FailureHint: workloadIdentityFailureHint(target, sa),
		}
	}

	if hardenedcontract.HasExplicitStorageIdentity(target) {
		return StorageIdentityDescription{
			Mode:        StorageIdentityModeWorkloadIdentity,
			Reason:      constants.ReasonWorkloadIdentityConfigured,
			Message:     explicitStorageIdentityMessage(provider, target, sa),
			FailureHint: workloadIdentityFailureHint(target, sa),
		}
	}

	return StorageIdentityDescription{
		Mode:        StorageIdentityModeAmbient,
		Reason:      constants.ReasonAmbientIdentityAssumed,
		Message:     ambientStorageIdentityMessage(provider, sa),
		FailureHint: workloadIdentityFailureHint(target, sa),
	}
}

// ConditionSummary returns a durable status-oriented description of how backup
// or restore storage access is expected to work for the generated Job
// ServiceAccount and pod template.
func ConditionSummary(target openbaov1alpha1.BackupTarget, serviceAccountName string) string {
	return DescribeStorageIdentity(target, serviceAccountName).Message
}

// IdentityConfigurationEventMessage returns a human-readable event message for
// backup or restore workloads when the target relies on workload identity,
// projected web identity, or the provider default chain.
func IdentityConfigurationEventMessage(target openbaov1alpha1.BackupTarget, serviceAccountName string) (string, bool) {
	description := DescribeStorageIdentity(target, serviceAccountName)
	if description.Mode == StorageIdentityModeSecret && description.FailureHint == "" {
		return "", false
	}
	return description.Message, description.Message != ""
}

// FailureHint returns a short follow-up hint that can be appended to Job
// failure messages when cloud identity is part of the storage auth path.
func FailureHint(target openbaov1alpha1.BackupTarget, serviceAccountName string) string {
	return DescribeStorageIdentity(target, serviceAccountName).FailureHint
}

func normalizeStorageProvider(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case constants.StorageProviderGCS:
		return constants.StorageProviderGCS
	case constants.StorageProviderAzure:
		return constants.StorageProviderAzure
	default:
		return constants.StorageProviderS3
	}
}

func secretBackedStorageMessage(provider, secretName string, target openbaov1alpha1.BackupTarget, serviceAccountName string) string {
	base := fmt.Sprintf("Storage credentials Secret %q is configured.", secretName)
	extra := explicitIdentityDetails(provider, target, serviceAccountName)
	if extra == "" {
		return base
	}
	return base + " " + extra
}

func explicitStorageIdentityMessage(provider string, target openbaov1alpha1.BackupTarget, serviceAccountName string) string {
	fragments := make([]string, 0, 3)
	saSuffix := generatedServiceAccountSuffix(serviceAccountName)

	if provider == constants.StorageProviderS3 && strings.TrimSpace(target.EffectiveS3RoleARN()) != "" {
		fragments = append(fragments, fmt.Sprintf(
			"S3 storage auth uses roleArn %q with a projected ServiceAccount token%s",
			strings.TrimSpace(target.EffectiveS3RoleARN()),
			saSuffix,
		))
	}

	if extra := explicitIdentityDetails(provider, target, serviceAccountName); extra != "" {
		fragments = append(fragments, extra)
	}

	if len(fragments) == 0 {
		if saSuffix == "" {
			return "Explicit workload identity metadata is configured for storage access."
		}
		return "Explicit workload identity metadata is configured for storage access" + saSuffix + "."
	}

	return strings.Join(fragments, ". ") + "."
}

func ambientStorageIdentityMessage(provider, serviceAccountName string) string {
	saSuffix := generatedServiceAccountSuffix(serviceAccountName)
	switch provider {
	case constants.StorageProviderGCS:
		return "No storage credentials Secret is configured. GCS storage access will rely on Application Default Credentials" + saSuffix + "."
	case constants.StorageProviderAzure:
		return "No storage credentials Secret is configured. Azure storage access will rely on Managed Identity, Azure Workload Identity, or another Azure default credential chain" + saSuffix + "."
	default:
		return "No storage credentials Secret is configured. S3 storage access will rely on the AWS SDK default credential chain or another S3-compatible ambient identity path" + saSuffix + "."
	}
}

func explicitIdentityDetails(provider string, target openbaov1alpha1.BackupTarget, serviceAccountName string) string {
	if target.WorkloadIdentity == nil {
		return ""
	}

	hasAnnotations := len(target.WorkloadIdentity.ServiceAccountAnnotations) > 0
	hasPodLabels := len(target.WorkloadIdentity.PodLabels) > 0
	if !hasAnnotations && !hasPodLabels {
		return ""
	}

	saSuffix := generatedServiceAccountSuffix(serviceAccountName)
	switch provider {
	case constants.StorageProviderGCS:
		if hasAnnotations {
			return "GCS workload identity metadata is configured via ServiceAccount annotations" + saSuffix
		}
		return "GCS storage auth relies on ambient credentials because no ServiceAccount annotations are configured" + saSuffix
	case constants.StorageProviderAzure:
		switch {
		case hasAnnotations && hasPodLabels:
			return "Azure workload identity metadata is configured on both the generated ServiceAccount annotations and Job pod labels" + saSuffix
		case hasAnnotations:
			return "Azure workload identity metadata is configured on the generated ServiceAccount annotations" + saSuffix + ", but Azure Workload Identity usually also requires Job pod labels"
		case hasPodLabels:
			return "Azure workload identity metadata is configured on Job pod labels" + saSuffix + ", but Azure Workload Identity usually also requires generated ServiceAccount annotations"
		}
	default:
		switch {
		case hasAnnotations && hasPodLabels:
			return "Workload identity metadata is configured on both the generated ServiceAccount annotations and Job pod labels" + saSuffix
		case hasAnnotations:
			return "Workload identity metadata is configured on generated ServiceAccount annotations" + saSuffix
		case hasPodLabels:
			return "Workload identity metadata is configured on Job pod labels" + saSuffix
		}
	}

	return ""
}

func workloadIdentityFailureHint(target openbaov1alpha1.BackupTarget, serviceAccountName string) string {
	if !hardenedcontract.HasExplicitStorageIdentity(target) && (target.CredentialsSecretRef != nil && strings.TrimSpace(target.CredentialsSecretRef.Name) != "") {
		return ""
	}

	if sa := strings.TrimSpace(serviceAccountName); sa != "" {
		return fmt.Sprintf(
			"This Job uses generated ServiceAccount %q for storage identity. If storage authentication failed, verify the ServiceAccount annotations, Job pod labels, projected ServiceAccount token, and any cloud identity binding for that ServiceAccount.",
			sa,
		)
	}

	return "This Job relies on workload identity or the provider default credential chain for storage access. If storage authentication failed, verify the cloud identity binding, Job pod metadata, and projected ServiceAccount token."
}

func generatedServiceAccountSuffix(serviceAccountName string) string {
	sa := strings.TrimSpace(serviceAccountName)
	if sa == "" {
		return ""
	}
	return fmt.Sprintf(" on generated ServiceAccount %q", sa)
}
