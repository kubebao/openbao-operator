package workload

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

// FinalizeDeletion keeps the cluster and StatefulSet ownership chain live until
// workload Pods are gone. False means deletion is still pending.
func (m *Manager) FinalizeDeletion(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (bool, error) {
	if cluster == nil || cluster.UID == "" || cluster.Name == "" || cluster.Namespace == "" {
		return false, fmt.Errorf("cluster identity is required for workload finalization")
	}
	if _, err := m.readFinalizingCluster(ctx, cluster); err != nil {
		return false, err
	}
	selector := client.MatchingLabels{
		constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster: cluster.Name,
	}
	var sets appsv1.StatefulSetList
	if err := m.reader.List(ctx, &sets, client.InNamespace(cluster.Namespace), selector); err != nil {
		return false, fmt.Errorf("list StatefulSets for cluster finalization: %w", err)
	}
	var pods corev1.PodList
	if err := m.reader.List(ctx, &pods, client.InNamespace(cluster.Namespace), selector,
		client.MatchingLabels{constants.LabelOpenBaoComponent: constants.ComponentOpenBaoCluster}); err != nil {
		return false, fmt.Errorf("list workload Pods for cluster finalization: %w", err)
	}
	if len(sets.Items) == 0 && len(pods.Items) == 0 {
		return true, nil
	}
	sort.Slice(sets.Items, func(i, j int) bool { return sets.Items[i].Name < sets.Items[j].Name })
	for i := range sets.Items {
		set := &sets.Items[i]
		stamped, err := m.backfillFinalizingStatefulSetProvenance(ctx, cluster, set)
		if err != nil || stamped {
			return false, err
		}
		if !set.DeletionTimestamp.IsZero() {
			continue
		}
		if err := m.deleteFinalizingStatefulSet(ctx, cluster, set); err != nil {
			return false, err
		}
		return false, nil
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !terminalPodDeletionEligible(pod) {
			continue
		}
		requested, err := m.completeTerminatedPodDeletion(ctx, logger, cluster, pod, finalizingPodDeletion)
		if err != nil || requested {
			return false, err
		}
	}
	// Foreground garbage collection removes each StatefulSet only after its
	// dependent Pods disappear. Never remove its finalizers here.
	return false, nil
}

func (m *Manager) readFinalizingCluster(ctx context.Context, expected *openbaov1alpha1.OpenBaoCluster) (*openbaov1alpha1.OpenBaoCluster, error) {
	current := &openbaov1alpha1.OpenBaoCluster{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(expected), current); err != nil {
		return nil, fmt.Errorf("read cluster authority for finalization: %w", err)
	}
	if current.UID != expected.UID || !podDeletionAuthorized(current, finalizingPodDeletion) {
		return nil, fmt.Errorf("cluster finalization requires the original deleting OpenBaoCluster and its finalizer")
	}
	return current, nil
}

func (m *Manager) deleteFinalizingStatefulSet(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, observed *appsv1.StatefulSet) error {
	set := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(observed), set); err != nil {
		return client.IgnoreNotFound(fmt.Errorf("read StatefulSet for finalization: %w", err))
	}
	current, err := m.readFinalizingCluster(ctx, cluster)
	if err != nil {
		return err
	}
	if set.UID != observed.UID || set.UID == "" || set.ResourceVersion == "" ||
		set.Labels[constants.LabelAppManagedBy] != constants.LabelValueAppManagedByOpenBaoOperator ||
		set.Labels[constants.LabelOpenBaoCluster] != current.Name ||
		!resourceownership.HasManagedControllerOwnerProof(set, current, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")) {
		return fmt.Errorf("StatefulSet %s/%s lacks current cluster ownership proof for finalization", set.Namespace, set.Name)
	}
	if !set.DeletionTimestamp.IsZero() {
		return nil
	}
	// Scaling to zero would activate whenScaled: Delete and destroy retained
	// PVCs. Delete the StatefulSet in the foreground instead.
	if current.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDeletePVCs &&
		current.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDeleteAll &&
		set.Spec.PersistentVolumeClaimRetentionPolicy != nil &&
		set.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted == appsv1.DeletePersistentVolumeClaimRetentionPolicyType {
		return fmt.Errorf("StatefulSet %s/%s would delete PVCs despite Retain policy", set.Namespace, set.Name)
	}
	if err := m.client.Delete(ctx, set, client.PropagationPolicy(metav1.DeletePropagationForeground),
		client.Preconditions{UID: &set.UID, ResourceVersion: &set.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete StatefulSet for finalization: %w", err)
	}
	return nil
}
