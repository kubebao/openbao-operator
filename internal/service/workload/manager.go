package workload

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

// Manager reconciles workload-owned resources for an OpenBaoCluster.
type Manager struct {
	client   client.Client
	reader   client.Reader
	scheme   *runtime.Scheme
	platform string
}

func NewManager(c client.Client, scheme *runtime.Scheme, platform string) *Manager {
	return &Manager{client: c, reader: c, scheme: scheme, platform: platform}
}

func (m *Manager) WithReader(reader client.Reader) *Manager {
	if reader != nil {
		m.reader = reader
	}
	return m
}

func (m *Manager) EnsureBlueGreenStatus(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) {
	EnsureBlueGreenStatus(ctx, logger, m.reader, cluster)
}

func (m *Manager) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, configContent string, spec StatefulSetSpec) error {
	if spec.SkipReconciliation {
		logger.Info("Skipping StatefulSet reconciliation per spec", "reason", "skip flag set")
		return nil
	}

	if spec.Pool == "" {
		spec.Pool = constants.LabelValueOpenBaoWorkloadPoolVoter
	}

	if spec.Pool == constants.LabelValueOpenBaoWorkloadPoolVoter && cluster != nil && spec.Replicas != cluster.Spec.Replicas {
		logger.V(1).Info(
			"Applying staged StatefulSet replica count",
			"statefulset", statefulSetNameForSpec(cluster, spec),
			"clusterDesiredReplicas", cluster.Spec.Replicas,
			"appliedStatefulSetReplicas", spec.Replicas,
		)
	}

	if err := m.EnsureStatefulSet(ctx, logger, cluster, configContent, spec); err != nil {
		return err
	}

	if err := m.ensurePodDisruptionBudget(ctx, logger, cluster, spec); err != nil {
		return err
	}

	return nil
}

func (m *Manager) ScaleDownStatefulSetIfExists(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec) error {
	return m.ScaleStatefulSetIfExists(ctx, logger, cluster, spec, 0)
}

func (m *Manager) DeleteStatefulSetIfExists(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec) error {
	name := statefulSetNameForSpec(cluster, spec)
	return m.deleteOwnedObjectIfExists(ctx, logger, cluster, name, &appsv1.StatefulSet{}, "StatefulSet", "statefulset", spec.Pool)
}

func (m *Manager) ScaleStatefulSetIfExists(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec, replicas int32) error {
	name := statefulSetNameForSpec(cluster, spec)
	if name == "" {
		return nil
	}

	statefulSet := &appsv1.StatefulSet{}
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, statefulSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get StatefulSet %s/%s for staged scale down: %w", cluster.Namespace, name, err)
	}

	if statefulSet.Spec.Replicas != nil && *statefulSet.Spec.Replicas == replicas {
		return nil
	}
	if err := resourceownership.RequireOwnerProof("scale workload StatefulSet", statefulSet, cluster); err != nil {
		return err
	}

	updated := statefulSet.DeepCopy()
	updated.Spec.Replicas = int32Ptr(replicas)
	if err := m.client.Patch(ctx, updated, client.MergeFrom(statefulSet)); err != nil {
		return fmt.Errorf("failed to scale StatefulSet %s/%s to %d replicas: %w", cluster.Namespace, name, replicas, err)
	}

	logger.Info("Scaled StatefulSet", "statefulset", name, "pool", spec.Pool, "replicas", replicas)
	return nil
}

func (m *Manager) DeleteConfigMapIfExists(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec) error {
	cmName := configMapNameForSpec(cluster, spec)
	return m.deleteOwnedObjectIfExists(ctx, logger, cluster, cmName, &corev1.ConfigMap{}, "ConfigMap", "configmap", spec.Pool)
}

func (m *Manager) deleteOwnedObjectIfExists(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, name string, obj client.Object, kind, logKey, pool string) error {
	if name == "" {
		return nil
	}

	if err := m.client.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get %s %s/%s for deletion: %w", kind, cluster.Namespace, name, err)
	}
	if err := resourceownership.RequireOwnerProof("delete workload "+kind, obj, cluster); err != nil {
		return err
	}

	uid := obj.GetUID()
	if err := m.client.Delete(ctx, obj, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete %s %s/%s: %w", kind, cluster.Namespace, name, err)
	}

	logger.Info("Deleted "+kind, logKey, name, "pool", pool)
	return nil
}

func (m *Manager) ensureConfigMapWithName(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, cmName string, configContent string) error {
	if cmName == "" {
		return fmt.Errorf("config ConfigMap name is required")
	}

	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Data: map[string]string{configFileName: configContent},
	}
	if err := resourceapply.ApplyOwned(ctx, m.client, m.scheme, cluster, configMap); err != nil {
		return fmt.Errorf("failed to ensure config ConfigMap %s/%s: %w", cluster.Namespace, cmName, err)
	}

	return nil
}

func (m *Manager) applyResource(ctx context.Context, obj client.Object, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if sts, ok := obj.(*appsv1.StatefulSet); ok &&
		portworkload.EffectiveStrategy(cluster) != openbaov1alpha1.UpdateStrategyBlueGreen {
		resolvedCluster, err := resourceapply.ResolveOwnerIdentity(ctx, m.client, cluster)
		if err != nil {
			return err
		}
		if err := resourceapply.EnsureOwnedResourceManageable(ctx, m.client, resolvedCluster, obj); err != nil {
			return err
		}
		if err := resourceapply.PrepareOwned(obj, resolvedCluster, m.scheme); err != nil {
			return err
		}
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sts)
		if err != nil {
			return fmt.Errorf("failed to convert StatefulSet to unstructured: %w", err)
		}
		if spec, ok := u["spec"].(map[string]any); ok {
			delete(spec, "updateStrategy")
		}
		unstructuredObj := &metav1unstructured.Unstructured{Object: u}
		gvk := sts.GetObjectKind().GroupVersionKind()
		if gvk.Empty() {
			gvk, err = m.client.GroupVersionKindFor(sts)
			if err != nil {
				return fmt.Errorf("failed to resolve GVK for StatefulSet: %w", err)
			}
		}
		unstructuredObj.SetGroupVersionKind(gvk)
		response, err := resourceapply.ApplyObserved(ctx, m.client, unstructuredObj, "")
		if err != nil {
			return err
		}
		return resourceapply.EnsureOwnedResourceProofStamped(ctx, m.client, m.scheme, resolvedCluster, response)
	}

	return resourceapply.ApplyOwned(ctx, m.client, m.scheme, cluster, obj)
}
