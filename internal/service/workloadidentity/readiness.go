package workloadidentity

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/storageenv"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/hardenedcontract"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

type Operation string

const (
	OperationBackup  Operation = "backup"
	OperationRestore Operation = "restore"
)

const readyReason = "Ready"

type Readiness struct {
	Status      metav1.ConditionStatus
	Reason      string
	Message     string
	FailureHint string
}

type Input struct {
	Operation          Operation
	Cluster            *openbaov1alpha1.OpenBaoCluster
	Namespace          string
	ServiceAccountName string
	JWTAuthRole        string
	TokenSecretRef     *corev1.LocalObjectReference
	Target             openbaov1alpha1.BackupTarget
	RequireEgressRules bool
	EgressRules        []networkingv1.NetworkPolicyEgressRule
}

func EvaluateBackupReadiness(ctx context.Context, reader client.Reader, cluster *openbaov1alpha1.OpenBaoCluster) (Readiness, error) {
	backupCfg := cluster.Spec.Backup

	return EvaluateExecutionReadiness(ctx, reader, Input{
		Operation:          OperationBackup,
		Cluster:            cluster,
		Namespace:          cluster.Namespace,
		ServiceAccountName: cluster.Name + constants.SuffixBackupServiceAccount,
		JWTAuthRole:        storageenv.EffectiveJWTRole(backupCfg.JWTAuthRole, portauth.OperatorJWTBootstrapEnabled(cluster), portauth.RoleNameBackup),
		TokenSecretRef:     backupCfg.TokenSecretRef,
		Target:             backupCfg.Target,
		RequireEgressRules: cluster.Spec.Profile == openbaov1alpha1.ProfileHardened,
		EgressRules:        clusterEgressRules(cluster),
	})
}

func EvaluateRestoreReadiness(ctx context.Context, reader client.Reader, restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster) (Readiness, error) {
	return EvaluateExecutionReadiness(ctx, reader, Input{
		Operation:          OperationRestore,
		Cluster:            cluster,
		Namespace:          restore.Namespace,
		ServiceAccountName: resourceidentity.RestoreServiceAccountName(cluster, restore),
		JWTAuthRole:        storageenv.EffectiveJWTRole(restore.Spec.JWTAuthRole, portauth.OperatorJWTBootstrapEnabled(cluster), portauth.RoleNameRestore),
		TokenSecretRef:     restore.Spec.TokenSecretRef,
		Target:             restore.Spec.Source.Target,
		RequireEgressRules: cluster.Spec.Profile == openbaov1alpha1.ProfileHardened,
		EgressRules:        clusterEgressRules(cluster),
	})
}

func EvaluateExecutionReadiness(ctx context.Context, reader client.Reader, input Input) (Readiness, error) {
	title := input.operationTitle()

	if input.RequireEgressRules {
		if len(input.EgressRules) == 0 {
			return Readiness{
				Status: metav1.ConditionFalse,
				Reason: constants.ReasonNetworkEgressRulesRequired,
				Message: fmt.Sprintf(
					"%s Jobs require explicit spec.network.egressRules in Hardened profile so they can reach the object storage endpoint",
					title,
				),
			}, nil
		}
		if !hardenedcontract.EgressRulesExplicit(input.EgressRules) {
			return Readiness{
				Status:  metav1.ConditionFalse,
				Reason:  constants.ReasonSecurityViolation,
				Message: "Hardened profile requires spec.network.egressRules entries to be port-scoped and target explicit non-wildcard peers.",
			}, nil
		}
	}

	if input.Cluster != nil && input.Cluster.Spec.Profile == openbaov1alpha1.ProfileHardened {
		if violation := hardenedcontract.EvaluateStorageTarget(input.operationTitle(), input.Target); violation != nil {
			return Readiness{
				Status:  metav1.ConditionFalse,
				Reason:  violation.Reason,
				Message: violation.Message,
			}, nil
		}
	}

	hasJWTAuth := strings.TrimSpace(input.JWTAuthRole) != ""
	hasTokenSecret := input.TokenSecretRef != nil && strings.TrimSpace(input.TokenSecretRef.Name) != ""
	usesStaticTokenAuth := !hasJWTAuth && hasTokenSecret
	if !hasJWTAuth && !hasTokenSecret {
		return Readiness{
			Status:  metav1.ConditionFalse,
			Reason:  constants.ReasonAuthenticationRequired,
			Message: fmt.Sprintf("%s authentication is required: configure jwtAuthRole or tokenSecretRef", title),
		}, nil
	}

	if usesStaticTokenAuth {
		secret, err := getSecret(ctx, reader, input.Namespace, input.TokenSecretRef.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return Readiness{
					Status:  metav1.ConditionFalse,
					Reason:  constants.ReasonTokenSecretMissing,
					Message: fmt.Sprintf("%s token Secret %s/%s was not found", title, input.Namespace, input.TokenSecretRef.Name),
				}, nil
			}
			return Readiness{}, fmt.Errorf("failed to read %s token Secret %s/%s: %w", input.Operation, input.Namespace, input.TokenSecretRef.Name, err)
		}
		if input.Operation == OperationRestore {
			if err := validateRestoreTokenSecretIdentity(secret, input.Cluster); err != nil {
				return Readiness{
					Status:  metav1.ConditionFalse,
					Reason:  constants.ReasonTokenSecretInvalid,
					Message: err.Error(),
				}, nil
			}
		}
	}

	if ref := input.Target.CredentialsSecretRef; ref != nil && strings.TrimSpace(ref.Name) != "" {
		if err := ensureSecretExists(ctx, reader, input.Namespace, ref.Name); err != nil {
			if apierrors.IsNotFound(err) {
				return Readiness{
					Status:  metav1.ConditionFalse,
					Reason:  constants.ReasonCredentialsSecretMissing,
					Message: fmt.Sprintf("%s storage credentials Secret %s/%s was not found", title, input.Namespace, ref.Name),
				}, nil
			}
			return Readiness{}, fmt.Errorf("failed to read %s storage credentials Secret %s/%s: %w", input.Operation, input.Namespace, ref.Name, err)
		}
	}

	authSummary := buildAuthSummary(input)
	storageSummary, reason := buildStorageSummary(input)
	messageParts := []string{authSummary, storageSummary}
	if crossSurface := CrossSurfaceIdentityHint(input.Cluster, input.Operation, input.Target, input.ServiceAccountName); strings.TrimSpace(crossSurface) != "" {
		messageParts = append(messageParts, crossSurface)
	}
	message := strings.TrimSpace(strings.Join(messageParts, " "))

	return Readiness{
		Status:      metav1.ConditionTrue,
		Reason:      reason,
		Message:     message,
		FailureHint: FailureHint(input.Target, input.ServiceAccountName),
	}, nil
}

func clusterEgressRules(cluster *openbaov1alpha1.OpenBaoCluster) []networkingv1.NetworkPolicyEgressRule {
	if cluster == nil || cluster.Spec.Network == nil {
		return nil
	}
	return cluster.Spec.Network.EgressRules
}

func ensureSecretExists(ctx context.Context, reader client.Reader, namespace, name string) error {
	_, err := getSecret(ctx, reader, namespace, name)
	return err
}

func getSecret(ctx context.Context, reader client.Reader, namespace, name string) (*corev1.Secret, error) {
	if reader == nil {
		return nil, fmt.Errorf("secret reader is required")
	}

	secret := &corev1.Secret{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func validateRestoreTokenSecretIdentity(secret *corev1.Secret, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if secret == nil {
		return fmt.Errorf("restore token Secret is required")
	}
	if cluster == nil || strings.TrimSpace(cluster.Name) == "" {
		return fmt.Errorf("restore token Secret %s/%s cannot be validated because the target cluster identity is unknown", secret.Namespace, secret.Name)
	}

	clusterLabel := secret.Labels[constants.LabelOpenBaoCluster]
	if clusterLabel != cluster.Name {
		return fmt.Errorf(
			"restore token Secret %s/%s must be labeled %s=%q for target cluster %q",
			secret.Namespace,
			secret.Name,
			constants.LabelOpenBaoCluster,
			cluster.Name,
			cluster.Name,
		)
	}

	purposeLabel := secret.Labels[constants.LabelOpenBaoCredentialPurpose]
	if purposeLabel != constants.LabelValueCredentialPurposeRestoreToken {
		return fmt.Errorf(
			"restore token Secret %s/%s must be labeled %s=%q",
			secret.Namespace,
			secret.Name,
			constants.LabelOpenBaoCredentialPurpose,
			constants.LabelValueCredentialPurposeRestoreToken,
		)
	}

	return nil
}

func buildAuthSummary(input Input) string {
	title := input.operationTitle()
	if strings.TrimSpace(input.JWTAuthRole) != "" {
		if strings.TrimSpace(input.ServiceAccountName) != "" {
			return fmt.Sprintf("%s auth uses JWT role %q on ServiceAccount %q.", title, input.JWTAuthRole, input.ServiceAccountName)
		}
		return fmt.Sprintf("%s auth uses JWT role %q.", title, input.JWTAuthRole)
	}

	if input.TokenSecretRef != nil && strings.TrimSpace(input.TokenSecretRef.Name) != "" {
		return fmt.Sprintf("%s auth uses token Secret %s/%s.", title, input.Namespace, input.TokenSecretRef.Name)
	}

	return fmt.Sprintf("%s auth is not configured.", title)
}

func buildStorageSummary(input Input) (string, string) {
	description := DescribeStorageIdentity(input.Target, input.ServiceAccountName)
	return description.Message, description.Reason
}

func (i Input) operationTitle() string {
	switch i.Operation {
	case OperationRestore:
		return "Restore"
	default:
		return "Backup"
	}
}
