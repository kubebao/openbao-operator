package workloadidentity

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

type CloudUnsealIdentityMode string

const (
	CloudUnsealIdentityModeSecret   CloudUnsealIdentityMode = "secret"
	CloudUnsealIdentityModeExplicit CloudUnsealIdentityMode = "explicit"
	CloudUnsealIdentityModeAmbient  CloudUnsealIdentityMode = "ambient"
)

type CloudUnsealIdentityDescription struct {
	Mode       CloudUnsealIdentityMode
	Provider   string
	SecretName string
	Message    string
}

type CloudUnsealIdentityReadiness struct {
	Readiness
	Mode CloudUnsealIdentityMode
}

// DescribeCloudUnsealIdentity classifies how the main OpenBao Pods are
// expected to authenticate to supported cloud KMS unseal backends.
func DescribeCloudUnsealIdentity(cluster *openbaov1alpha1.OpenBaoCluster) (CloudUnsealIdentityDescription, bool) {
	if cluster == nil || cluster.Spec.Unseal == nil {
		return CloudUnsealIdentityDescription{}, false
	}

	serviceAccountName := mainWorkloadServiceAccountName(cluster)
	secretName := ""
	if cluster.Spec.Unseal.CredentialsSecretRef != nil {
		secretName = strings.TrimSpace(cluster.Spec.Unseal.CredentialsSecretRef.Name)
	}

	switch cluster.Spec.Unseal.Type {
	case "awskms":
		cfg := cluster.Spec.Unseal.AWSKMS
		if cfg == nil {
			return CloudUnsealIdentityDescription{}, false
		}
		if secretName != "" {
			return CloudUnsealIdentityDescription{
				Mode:       CloudUnsealIdentityModeSecret,
				Provider:   "AWS KMS",
				SecretName: secretName,
				Message: fmt.Sprintf(
					"AWS KMS unseal uses credentials Secret %s/%s for the main OpenBao Pods on ServiceAccount %q.",
					cluster.Namespace,
					secretName,
					serviceAccountName,
				),
			}, true
		}
		if awsInlineCredentialsConfigured(cfg) {
			return CloudUnsealIdentityDescription{
				Mode:     CloudUnsealIdentityModeExplicit,
				Provider: "AWS KMS",
				Message: fmt.Sprintf(
					"AWS KMS unseal uses inline credentials from spec.unseal.awskms on ServiceAccount %q. Prefer spec.unseal.credentialsSecretRef or IRSA for production.",
					serviceAccountName,
				),
			}, true
		}
		return CloudUnsealIdentityDescription{
			Mode:     CloudUnsealIdentityModeAmbient,
			Provider: "AWS KMS",
			Message:  awsAmbientUnsealMessage(cluster, serviceAccountName),
		}, true
	case "gcpckms":
		cfg := cluster.Spec.Unseal.GCPCloudKMS
		if cfg == nil {
			return CloudUnsealIdentityDescription{}, false
		}
		if secretName != "" {
			return CloudUnsealIdentityDescription{
				Mode:       CloudUnsealIdentityModeSecret,
				Provider:   "GCP Cloud KMS",
				SecretName: secretName,
				Message: fmt.Sprintf(
					"GCP Cloud KMS unseal uses credentials Secret %s/%s for the main OpenBao Pods on ServiceAccount %q.",
					cluster.Namespace,
					secretName,
					serviceAccountName,
				),
			}, true
		}
		if strings.TrimSpace(cfg.Credentials) != "" {
			return CloudUnsealIdentityDescription{
				Mode:     CloudUnsealIdentityModeExplicit,
				Provider: "GCP Cloud KMS",
				Message: fmt.Sprintf(
					"GCP Cloud KMS unseal uses explicit spec.unseal.gcpCloudKMS.credentials on ServiceAccount %q. Ensure the referenced credentials file is present in the OpenBao Pods; the operator only projects it automatically when you use spec.unseal.credentialsSecretRef.",
					serviceAccountName,
				),
			}, true
		}
		return CloudUnsealIdentityDescription{
			Mode:     CloudUnsealIdentityModeAmbient,
			Provider: "GCP Cloud KMS",
			Message:  gcpAmbientUnsealMessage(cluster, serviceAccountName),
		}, true
	case "azurekeyvault":
		cfg := cluster.Spec.Unseal.AzureKeyVault
		if cfg == nil {
			return CloudUnsealIdentityDescription{}, false
		}
		if cfg.WorkloadIdentity != nil {
			message := fmt.Sprintf(
				"Azure Key Vault unseal uses the workload identity of client ID %q: the operator projects a token for ServiceAccount %q with audience %q into the main OpenBao Pods. Entra ID must hold a federated credential for that identity that trusts the cluster's ServiceAccount issuer and this ServiceAccount.",
				cfg.WorkloadIdentity.ClientID,
				serviceAccountName,
				"api://AzureADTokenExchange",
			)
			if secretName != "" {
				message += fmt.Sprintf(" Credentials Secret %s/%s supplies only AZURE_ENVIRONMENT and AZURE_AD_RESOURCE.", cluster.Namespace, secretName)
			}
			return CloudUnsealIdentityDescription{
				Mode:       CloudUnsealIdentityModeExplicit,
				Provider:   "Azure Key Vault",
				SecretName: secretName,
				Message:    message,
			}, true
		}
		if secretName != "" {
			return CloudUnsealIdentityDescription{
				Mode:       CloudUnsealIdentityModeSecret,
				Provider:   "Azure Key Vault",
				SecretName: secretName,
				Message: fmt.Sprintf(
					"Azure Key Vault unseal uses credentials Secret %s/%s for the main OpenBao Pods on ServiceAccount %q.",
					cluster.Namespace,
					secretName,
					serviceAccountName,
				),
			}, true
		}
		if strings.TrimSpace(cfg.ClientSecret) != "" {
			return CloudUnsealIdentityDescription{
				Mode:     CloudUnsealIdentityModeExplicit,
				Provider: "Azure Key Vault",
				Message: fmt.Sprintf(
					"Azure Key Vault unseal uses inline client-secret authentication from spec.unseal.azureKeyVault on ServiceAccount %q. Prefer spec.unseal.credentialsSecretRef or managed identity for production.",
					serviceAccountName,
				),
			}, true
		}
		return CloudUnsealIdentityDescription{
			Mode:     CloudUnsealIdentityModeAmbient,
			Provider: "Azure Key Vault",
			Message:  azureAmbientUnsealMessage(cluster, serviceAccountName),
		}, true
	case "ocikms":
		cfg := cluster.Spec.Unseal.OCIKMS
		if cfg == nil {
			return CloudUnsealIdentityDescription{}, false
		}
		if secretName != "" && ociAPIKeyEnabled(cfg) {
			return CloudUnsealIdentityDescription{
				Mode:       CloudUnsealIdentityModeSecret,
				Provider:   "OCI KMS",
				SecretName: secretName,
				Message: fmt.Sprintf(
					"OCI KMS unseal uses credentials Secret %s/%s for the main OpenBao Pods on ServiceAccount %q. The Secret must contain OCI SDK config key %q, and key_file in that config must reference another file under %s.",
					cluster.Namespace,
					secretName,
					serviceAccountName,
					"config",
					"/etc/bao/seal-creds",
				),
			}, true
		}
		if ociAPIKeyEnabled(cfg) {
			return CloudUnsealIdentityDescription{
				Mode:     CloudUnsealIdentityModeExplicit,
				Provider: "OCI KMS",
				Message:  "OCI KMS unseal uses spec.unseal.ocikms.authTypeAPIKey=true without spec.unseal.credentialsSecretRef. Ensure OCI SDK config is already present in the OpenBao Pods and reachable through OCI_CONFIG_FILE or the OCI SDK default location.",
			}, true
		}
		return CloudUnsealIdentityDescription{
			Mode:     CloudUnsealIdentityModeAmbient,
			Provider: "OCI KMS",
			Message:  ociAmbientUnsealMessage(serviceAccountName),
		}, true
	default:
		return CloudUnsealIdentityDescription{}, false
	}
}

// EvaluateCloudUnsealIdentity resolves the operator-known cloud KMS unseal
// authentication contract for the main OpenBao Pods and validates referenced
// credentials Secrets when one is configured.
func EvaluateCloudUnsealIdentity(
	ctx context.Context,
	reader client.Reader,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (CloudUnsealIdentityReadiness, bool, error) {
	if cluster != nil &&
		cluster.Spec.Unseal != nil &&
		cluster.Spec.Unseal.Type == "ocikms" &&
		cluster.Spec.Unseal.CredentialsSecretRef != nil &&
		cluster.Spec.Unseal.OCIKMS != nil &&
		(cluster.Spec.Unseal.OCIKMS.AuthTypeAPIKey == nil || !*cluster.Spec.Unseal.OCIKMS.AuthTypeAPIKey) {
		return CloudUnsealIdentityReadiness{
			Readiness: Readiness{
				Status:  metav1.ConditionFalse,
				Reason:  constants.ReasonPrerequisitesMissing,
				Message: "OCI KMS credentialsSecretRef requires spec.unseal.ocikms.authTypeAPIKey=true because the operator only mounts OCI SDK config for API key authentication.",
			},
		}, true, nil
	}

	description, ok := DescribeCloudUnsealIdentity(cluster)
	if !ok {
		return CloudUnsealIdentityReadiness{}, false, nil
	}

	if description.SecretName != "" {
		if err := ensureSecretExists(ctx, reader, cluster.Namespace, description.SecretName); err != nil {
			if apierrors.IsNotFound(err) {
				return CloudUnsealIdentityReadiness{
					Readiness: Readiness{
						Status:  metav1.ConditionFalse,
						Reason:  constants.ReasonCredentialsSecretMissing,
						Message: fmt.Sprintf("%s unseal credentials Secret %s/%s was not found", description.Provider, cluster.Namespace, description.SecretName),
					},
					Mode: description.Mode,
				}, true, nil
			}
			return CloudUnsealIdentityReadiness{}, true, fmt.Errorf(
				"failed to read %s unseal credentials Secret %s/%s: %w",
				description.Provider,
				cluster.Namespace,
				description.SecretName,
				err,
			)
		}
	}

	reason := readyReason
	if description.Mode == CloudUnsealIdentityModeAmbient {
		reason = cloudUnsealIdentityReason(cluster)
	}

	return CloudUnsealIdentityReadiness{
		Readiness: Readiness{
			Status:  metav1.ConditionTrue,
			Reason:  reason,
			Message: description.Message,
		},
		Mode: description.Mode,
	}, true, nil
}

func CrossSurfaceIdentityHint(
	cluster *openbaov1alpha1.OpenBaoCluster,
	operation Operation,
	target openbaov1alpha1.BackupTarget,
	jobServiceAccountName string,
) string {
	unsealIdentity, ok := DescribeCloudUnsealIdentity(cluster)
	if !ok {
		return ""
	}

	jobSA := strings.TrimSpace(jobServiceAccountName)
	mainSA := mainWorkloadServiceAccountName(cluster)
	if jobSA == "" || mainSA == "" || jobSA == mainSA {
		return ""
	}

	storageIdentity := DescribeStorageIdentity(target, jobServiceAccountName)
	if storageIdentity.Mode == StorageIdentityModeSecret {
		return ""
	}

	title := operationTitle(operation)
	if storageIdentity.Mode == StorageIdentityModeAmbient {
		return fmt.Sprintf(
			"The main OpenBao Pods use separate %s unseal identity on ServiceAccount %q. %s Jobs use generated ServiceAccount %q and do not inherit that identity automatically; configure target.workloadIdentity or target.credentialsSecretRef if storage access needs its own cloud identity binding.",
			unsealIdentity.Provider,
			mainSA,
			title,
			jobSA,
		)
	}

	return fmt.Sprintf(
		"The main OpenBao Pods use separate %s unseal identity on ServiceAccount %q. %s Jobs use their own storage identity configuration on generated ServiceAccount %q.",
		unsealIdentity.Provider,
		mainSA,
		title,
		jobSA,
	)
}

func mainWorkloadServiceAccountName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster != nil && cluster.Spec.ServiceAccount != nil && strings.TrimSpace(cluster.Spec.ServiceAccount.Name) != "" {
		return strings.TrimSpace(cluster.Spec.ServiceAccount.Name)
	}
	if cluster == nil {
		return ""
	}
	return cluster.Name + constants.SuffixServiceAccount
}

func awsInlineCredentialsConfigured(cfg *openbaov1alpha1.AWSKMSSealConfig) bool {
	if cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.AccessKey) != "" ||
		strings.TrimSpace(cfg.SecretKey) != "" ||
		strings.TrimSpace(cfg.SessionToken) != ""
}

func ociAPIKeyEnabled(cfg *openbaov1alpha1.OCIKMSSealConfig) bool {
	if cfg == nil {
		return false
	}
	return cfg.AuthTypeAPIKey != nil && *cfg.AuthTypeAPIKey
}

func awsAmbientUnsealMessage(cluster *openbaov1alpha1.OpenBaoCluster, serviceAccountName string) string {
	message := fmt.Sprintf(
		"AWS KMS unseal omits spec.unseal.credentialsSecretRef and inline access keys. OpenBao Pods will rely on the standard AWS credential chain on ServiceAccount %q.",
		serviceAccountName,
	)
	if hasServiceAccountAnnotations(cluster) {
		return message + " ServiceAccount annotations are configured on spec.serviceAccount.annotations."
	}
	return message + " If you intend to use IRSA, configure spec.serviceAccount.annotations; otherwise the Pods must rely on node IAM or another AWS credential source."
}

func gcpAmbientUnsealMessage(cluster *openbaov1alpha1.OpenBaoCluster, serviceAccountName string) string {
	message := fmt.Sprintf(
		"GCP Cloud KMS unseal omits spec.unseal.credentialsSecretRef and spec.unseal.gcpCloudKMS.credentials. OpenBao Pods will rely on Application Default Credentials on ServiceAccount %q.",
		serviceAccountName,
	)
	if hasServiceAccountAnnotations(cluster) {
		return message + " ServiceAccount annotations are configured on spec.serviceAccount.annotations."
	}
	return message + " If you intend to use GKE Workload Identity, configure spec.serviceAccount.annotations; otherwise the Pods must rely on another Application Default Credentials source."
}

func azureAmbientUnsealMessage(cluster *openbaov1alpha1.OpenBaoCluster, serviceAccountName string) string {
	message := fmt.Sprintf(
		"Azure Key Vault unseal omits spec.unseal.credentialsSecretRef and spec.unseal.azureKeyVault.clientSecret. OpenBao Pods will rely on Managed Identity or Azure Workload Identity on ServiceAccount %q.",
		serviceAccountName,
	)

	hasServiceAccount := hasServiceAccountAnnotations(cluster)
	hasPodLabels := hasPodIdentityLabels(cluster)
	switch {
	case hasServiceAccount && hasPodLabels:
		return message + " Azure workload identity metadata is configured on both spec.serviceAccount.annotations and spec.podMetadata.labels."
	case hasServiceAccount:
		return message + " spec.serviceAccount.annotations is configured, but Azure Workload Identity usually also requires spec.podMetadata.labels."
	case hasPodLabels:
		return message + " spec.podMetadata.labels is configured, but Azure Workload Identity usually also requires spec.serviceAccount.annotations."
	default:
		return message + " If you intend to use Azure Workload Identity, configure both spec.serviceAccount.annotations and spec.podMetadata.labels; otherwise the Pods must rely on node-managed identity."
	}
}

func ociAmbientUnsealMessage(serviceAccountName string) string {
	return fmt.Sprintf(
		"OCI KMS unseal uses the default OCI principal flow on ServiceAccount %q. Verify that the main OpenBao Pods have the intended OCI ambient identity, such as instance principal, before relying on this path.",
		serviceAccountName,
	)
}

func hasServiceAccountAnnotations(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil &&
		cluster.Spec.ServiceAccount != nil &&
		len(cluster.Spec.ServiceAccount.Annotations) > 0
}

func hasPodIdentityLabels(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil &&
		cluster.Spec.PodMetadata != nil &&
		len(cluster.Spec.PodMetadata.Labels) > 0
}

func cloudUnsealIdentityReason(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster == nil || cluster.Spec.Unseal == nil {
		return constants.ReasonAmbientIdentityAssumed
	}

	switch cluster.Spec.Unseal.Type {
	case "awskms", "gcpckms":
		if hasServiceAccountAnnotations(cluster) {
			return constants.ReasonWorkloadIdentityConfigured
		}
	case "azurekeyvault":
		if hasServiceAccountAnnotations(cluster) && hasPodIdentityLabels(cluster) {
			return constants.ReasonWorkloadIdentityConfigured
		}
	}

	return constants.ReasonAmbientIdentityAssumed
}

func operationTitle(operation Operation) string {
	switch operation {
	case OperationRestore:
		return "Restore"
	default:
		return "Backup"
	}
}
