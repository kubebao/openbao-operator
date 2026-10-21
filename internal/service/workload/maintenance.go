package workload

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

const maintenanceAnnotationEnabledValue = "true"

func (m *Manager) reconcileMaintenanceAnnotationsForPods(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec) error {
	if cluster == nil {
		return nil
	}

	enabled := cluster.Spec.Maintenance != nil && cluster.Spec.Maintenance.Enabled

	var pods corev1.PodList
	if err := m.client.List(ctx, &pods,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(resourceidentity.PodSelectorLabelsForPoolWithRevision(cluster, spec.Pool, spec.Revision)),
	); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}

		original := pod.DeepCopy()
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}

		if enabled {
			pod.Annotations[constants.AnnotationMaintenance] = maintenanceAnnotationEnabledValue
		} else {
			delete(pod.Annotations, constants.AnnotationMaintenance)
		}

		if reflect.DeepEqual(original.Annotations, pod.Annotations) {
			continue
		}

		if err := m.client.Patch(ctx, pod, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("failed to patch pod %s/%s maintenance annotation: %w", pod.Namespace, pod.Name, err)
		}
		logger.V(1).Info("Patched pod maintenance annotation", "pod", pod.Name, "enabled", enabled)
	}

	return nil
}

// GuardAzureWorkloadIdentityStatefulSet holds an existing StatefulSet whose Azure workload
// identity token projection disagrees with the cluster spec. Its recreation deletes the
// StatefulSet in maintenance mode, which admission allows only once the maintenance
// annotation is on it, so the annotation is kept current on the StatefulSet and its Pods
// while the rest of the workload is held.
func (m *Manager) GuardAzureWorkloadIdentityStatefulSet(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, existing *appsv1.StatefulSet, spec StatefulSetSpec) error {
	holdErr := CheckAzureWorkloadIdentityStatefulSet(cluster, existing)
	if holdErr == nil {
		return nil
	}
	if err := m.reconcileMaintenanceAnnotationForStatefulSet(ctx, cluster, existing); err != nil {
		return err
	}
	if err := m.reconcileMaintenanceAnnotationsForPods(ctx, logger, cluster, spec); err != nil {
		return fmt.Errorf("failed to reconcile maintenance annotations for OpenBaoCluster %s/%s pods: %w", cluster.Namespace, cluster.Name, err)
	}
	return holdErr
}

func (m *Manager) reconcileMaintenanceAnnotationForStatefulSet(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, existing *appsv1.StatefulSet) error {
	enabled := cluster.Spec.Maintenance != nil && cluster.Spec.Maintenance.Enabled
	value, present := existing.Annotations[constants.AnnotationMaintenance]
	if (enabled && value == maintenanceAnnotationEnabledValue) || (!enabled && !present) {
		return nil
	}
	if err := resourceownership.RequireOwnerProof("change workload StatefulSet maintenance annotation", existing, cluster); err != nil {
		return err
	}

	original := existing.DeepCopy()
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	if enabled {
		existing.Annotations[constants.AnnotationMaintenance] = maintenanceAnnotationEnabledValue
	} else {
		delete(existing.Annotations, constants.AnnotationMaintenance)
	}
	if err := m.client.Patch(ctx, existing, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to patch StatefulSet %s/%s maintenance annotation: %w", existing.Namespace, existing.Name, err)
	}
	return nil
}
