package openbaocluster

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/statusops"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// ReconcilePrerequisiteConditions evaluates and applies the status conditions for
// prerequisites that the operator can observe directly.
func (a *Applications) ReconcilePrerequisiteConditions(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	applyAPIServerNetworkStatus(ctx, a.config.StatusIntegration, cluster)
	applyTLSStatus(ctx, a.config.StatusDependencies, cluster)
	applyACMEIntegrationStatus(ctx, a.config.StatusIntegration, cluster)
	applyACMECacheStatus(ctx, a.config.StatusDependencies, cluster)
	applyAuditFileStorageStatus(ctx, a.config.StatusDependencies, cluster)
	applyGatewayIntegrationStatus(ctx, a.config.StatusIntegration, cluster)
	applyIngressIntegrationStatus(ctx, a.config.StatusIntegration, cluster)
	applyBackupConfigurationStatus(ctx, a.config.StatusDependencies, cluster)
	applyCloudUnsealIdentityStatus(ctx, a.config.StatusDependencies, cluster)
}

func applyAPIServerNetworkStatus(
	ctx context.Context,
	deps StatusIntegrationDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	result := EvaluateAPIServerNetwork(ctx, deps, cluster)
	statusops.ApplyAPIServerNetworkReadyCondition(cluster, statusops.ConditionResult{
		Status:  result.Status,
		Reason:  result.Reason,
		Message: result.Message,
	})
}

func applyTLSStatus(
	ctx context.Context,
	deps StatusDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	statusops.ApplyTLSReadyCondition(cluster, statusops.EvaluateTLSReadiness(ctx, deps.Reader, cluster))
}

func applyACMEIntegrationStatus(
	ctx context.Context,
	deps StatusIntegrationDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	if !portopenbao.UsesACMEMode(cluster) {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionACMEIntegrationReady)
		return
	}

	result := EvaluateACMEIntegration(ctx, deps, cluster)
	statusops.ApplyACMEIntegrationReadyCondition(cluster, statusops.ConditionResult{
		Status:  result.Status,
		Reason:  result.Reason,
		Message: result.Message,
	})
}

func applyACMECacheStatus(
	ctx context.Context,
	deps StatusDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	result, applicable := statusops.EvaluateACMECacheReadiness(ctx, deps.Reader, cluster)
	if !applicable {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionACMECacheReady)
		return
	}
	statusops.ApplyACMECacheReadyCondition(cluster, result)
}

func applyAuditFileStorageStatus(
	ctx context.Context,
	deps StatusDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	result, applicable := statusops.EvaluateAuditFileStorageReadiness(ctx, deps.Reader, cluster)
	if !applicable {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionAuditFileStorageReady)
		return
	}
	statusops.ApplyAuditFileStorageReadyCondition(cluster, result)
}

func applyGatewayIntegrationStatus(
	ctx context.Context,
	deps StatusIntegrationDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	if cluster.Spec.Gateway == nil || !cluster.Spec.Gateway.Enabled {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionGatewayIntegrationReady)
		return
	}

	result := EvaluateGatewayIntegration(ctx, deps, cluster)
	statusops.ApplyGatewayIntegrationReadyCondition(cluster, statusops.ConditionResult{
		Status:  result.Status,
		Reason:  result.Reason,
		Message: result.Message,
	})
}

func applyIngressIntegrationStatus(
	ctx context.Context,
	deps StatusIntegrationDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	if cluster.Spec.Ingress == nil || !cluster.Spec.Ingress.Enabled {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionIngressIntegrationReady)
		return
	}

	result := EvaluateIngressIntegration(ctx, deps, cluster)
	statusops.ApplyIngressIntegrationReadyCondition(cluster, statusops.ConditionResult{
		Status:  result.Status,
		Reason:  result.Reason,
		Message: result.Message,
	})
}

func applyBackupConfigurationStatus(
	ctx context.Context,
	deps StatusDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	if cluster.Spec.Backup == nil {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionBackupConfigurationReady)
		return
	}

	result, err := EvaluateBackupConfiguration(ctx, deps.Reader, cluster)
	if err != nil {
		statusops.ApplyBackupConfigurationReadyCondition(cluster, statusops.ConditionResult{
			Status:  metav1.ConditionUnknown,
			Reason:  constants.ReasonUnknown,
			Message: fmt.Sprintf("Failed to evaluate backup Job prerequisites: %v", err),
		})
		return
	}

	statusops.ApplyBackupConfigurationReadyCondition(cluster, statusops.ConditionResult{
		Status:  result.Status,
		Reason:  result.Reason,
		Message: result.Message,
	})
}

func applyCloudUnsealIdentityStatus(
	ctx context.Context,
	deps StatusDependencies,
	cluster *openbaov1alpha1.OpenBaoCluster,
) {
	result, applicable, err := EvaluateCloudUnsealIdentity(ctx, deps.Reader, cluster)
	if !applicable {
		removePrerequisiteCondition(cluster, openbaov1alpha1.ConditionCloudUnsealIdentityReady)
		return
	}

	if err != nil {
		statusops.ApplyCloudUnsealIdentityReadyCondition(cluster, statusops.ConditionResult{
			Status:  metav1.ConditionUnknown,
			Reason:  constants.ReasonUnknown,
			Message: fmt.Sprintf("Failed to evaluate cloud KMS unseal identity prerequisites: %v", err),
		})
		return
	}

	recreate, recreateRequired, err := statusops.EvaluateAzureWorkloadIdentityStatefulSets(ctx, deps.Reader, cluster)
	if err != nil {
		statusops.ApplyCloudUnsealIdentityReadyCondition(cluster, statusops.ConditionResult{
			Status:  metav1.ConditionUnknown,
			Reason:  constants.ReasonUnknown,
			Message: fmt.Sprintf("Failed to inspect OpenBao StatefulSets for the Azure workload identity token projection: %v", err),
		})
		return
	}
	if recreateRequired {
		statusops.ApplyCloudUnsealIdentityReadyCondition(cluster, recreate)
		return
	}

	statusops.ApplyCloudUnsealIdentityReadyCondition(cluster, statusops.ConditionResult{
		Status:  result.Status,
		Reason:  result.Reason,
		Message: result.Message,
	})
}

func removePrerequisiteCondition(cluster *openbaov1alpha1.OpenBaoCluster, conditionType openbaov1alpha1.ConditionType) {
	meta.RemoveStatusCondition(&cluster.Status.Conditions, string(conditionType))
}
