package bluegreen

import (
	"context"
	"maps"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

func newManagedCleanupStatefulSet(cluster *openbaov1alpha1.OpenBaoCluster, revision string) *appsv1.StatefulSet {
	name := cluster.Name
	labels := resourceidentity.Labels(cluster)
	if revision != "" {
		name += "-" + revision
		labels[constants.LabelOpenBaoRevision] = revision
	}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: cluster.Namespace, UID: types.UID(name + "-uid"),
			Labels: labels, Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: cluster.Name, Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(labels)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(labels)},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: constants.ContainerBao, Image: cluster.Spec.Image}}},
			},
		},
	}
}

func runStatefulSetCleanup(t *testing.T, m *Manager, cluster *openbaov1alpha1.OpenBaoCluster, blue bool) error {
	t.Helper()
	if blue {
		_, _, err := m.ensureBlueStatefulSetDeleted(t.Context(), logr.Discard(), cluster)
		return err
	}
	return m.cleanupGreenStatefulSet(t.Context(), logr.Discard(), cluster)
}

func TestStatefulSetCleanupRequiresCurrentOwnerAndRevision(t *testing.T) {
	t.Parallel()
	for _, blue := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			alter  func(*openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet)
			reject bool
		}{
			{name: "current managed owner"},
			{name: "missing cluster UID", reject: true, alter: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.UID = ""
			}},
			{name: "missing controller owner", reject: true, alter: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.OwnerReferences = nil
			}},
			{name: "foreign controller", reject: true, alter: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.OwnerReferences[0].Name = "other-cluster"
			}},
			{name: "stale cluster UID", reject: true, alter: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.OwnerReferences[0].UID = "old-cluster-uid"
			}},
			{name: "missing provenance", reject: true, alter: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.Annotations = nil
			}},
			{name: "stale provenance", reject: true, alter: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.Annotations[constants.AnnotationOpenBaoOwnerUID] = "old-cluster-uid"
			}},
			{name: "wrong revision", reject: true, alter: func(_ *openbaov1alpha1.OpenBaoCluster, s *appsv1.StatefulSet) {
				s.Spec.Template.Labels[constants.LabelOpenBaoRevision] = "another-revision"
			}},
			{name: "same Blue and Green revision", reject: true, alter: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.BlueGreen.BlueRevision = c.Status.BlueGreen.GreenRevision
			}},
			{name: "equivalent Blue and Green revision", reject: true, alter: func(c *openbaov1alpha1.OpenBaoCluster, _ *appsv1.StatefulSet) {
				c.Status.BlueGreen.BlueRevision = " " + c.Status.BlueGreen.GreenRevision + " "
			}},
		} {
			name := "abort/"
			if blue {
				name = "forward/"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				cluster := newPhaseMachineCluster()
				cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseCleanup
				revision := cluster.Status.BlueGreen.GreenRevision
				if blue {
					revision = cluster.Status.BlueGreen.BlueRevision
				}
				sts := newManagedCleanupStatefulSet(cluster, revision)
				if tc.alter != nil {
					tc.alter(cluster, sts)
				}
				c := fake.NewClientBuilder().WithScheme(newBlueGreenTestScheme(t)).WithObjects(sts).Build()
				m := &Manager{client: c, reader: c}
				err := runStatefulSetCleanup(t, m, cluster, blue)
				require.Equal(t, tc.reject, err != nil, "cleanup error: %v", err)
				getErr := c.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{})
				require.Equal(t, !tc.reject, apierrors.IsNotFound(getErr))
			})
		}
	}
}

func TestBlueCleanupAcceptsOnlyExpectedLegacyWorkload(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		recorded         string
		observed         string
		templateRevision string
		reject           bool
	}{
		{name: "recorded controller revision", recorded: "controller-1", observed: "controller-1"},
		{name: "older status without controller revision", observed: "controller-1"},
		{name: "changed controller revision", recorded: "controller-1", observed: "controller-2", reject: true},
		{name: "missing observed controller revision", recorded: "controller-1", reject: true},
		{name: "revisioned workload cannot use legacy fallback", templateRevision: "new-generation", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newPhaseMachineCluster()
			cluster.Status.BlueGreen.BlueRevision = ""
			cluster.Status.BlueGreen.BlueControllerRevision = tc.recorded
			sts := newManagedCleanupStatefulSet(cluster, "")
			sts.Status.CurrentRevision = tc.observed
			sts.Spec.Template.Labels[constants.LabelOpenBaoRevision] = tc.templateRevision
			c := fake.NewClientBuilder().WithScheme(newBlueGreenTestScheme(t)).WithObjects(sts).Build()
			err := runStatefulSetCleanup(t, &Manager{client: c}, cluster, true)
			require.Equal(t, tc.reject, err != nil, "cleanup error: %v", err)
			getErr := c.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{})
			require.Equal(t, !tc.reject, apierrors.IsNotFound(getErr))
		})
	}
}

func TestStatefulSetCleanupPreservesReadPool(t *testing.T) {
	t.Parallel()
	const readRevision = "read"
	for _, blue := range []bool{false, true} {
		cluster := newPhaseMachineCluster()
		if blue {
			cluster.Status.BlueGreen.BlueRevision = readRevision
		} else {
			cluster.Status.BlueGreen.GreenRevision = readRevision
		}
		sts := newManagedCleanupStatefulSet(cluster, readRevision)
		c := fake.NewClientBuilder().WithScheme(newBlueGreenTestScheme(t)).WithObjects(sts).Build()
		err := runStatefulSetCleanup(t, &Manager{client: c}, cluster, blue)
		require.ErrorContains(t, err, "read-replica workload")
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{}))
	}
}

func TestStatefulSetCleanupRepeatsAfterLostStatusWithoutDeletingReplacement(t *testing.T) {
	t.Parallel()
	for _, blue := range []bool{false, true} {
		cluster := newPhaseMachineCluster()
		revision := cluster.Status.BlueGreen.GreenRevision
		if blue {
			revision = cluster.Status.BlueGreen.BlueRevision
		}
		sts := newManagedCleanupStatefulSet(cluster, revision)
		sts.Finalizers = []string{"test.openbao.org/held"}
		pod := newRevisionPod(cluster, revision, sts.Name+"-0")
		claim := rollbackDataClaim(cluster, "data-"+sts.Name+"-0")
		c := fake.NewClientBuilder().WithScheme(newBlueGreenTestScheme(t)).WithObjects(sts, pod, claim).Build()
		deletes := 0
		writer := interceptor.NewClient(c, interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				_, statefulSet := obj.(*appsv1.StatefulSet)
				require.True(t, statefulSet, "cleanup cannot directly delete Pods or data claims")
				deletes++
				return c.Delete(ctx, obj, opts...)
			},
		})
		m := &Manager{client: writer, reader: c}
		// Keep the old phase and revisions, as when deletion succeeds but the
		// following cluster status update is lost during a controller restart.
		for range 2 {
			require.NoError(t, runStatefulSetCleanup(t, m, cluster, blue))
		}
		require.Equal(t, 1, deletes, "a terminating StatefulSet needs no repeated delete")
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), sts))
		require.NotNil(t, sts.DeletionTimestamp)
		sts.Finalizers = nil
		require.NoError(t, c.Update(t.Context(), sts))
		require.NoError(t, runStatefulSetCleanup(t, m, cluster, blue), "already absent is idempotent")
		replacement := newManagedCleanupStatefulSet(cluster, revision)
		replacement.UID, replacement.OwnerReferences, replacement.Annotations = "replacement-uid", nil, nil
		require.NoError(t, c.Create(t.Context(), replacement))
		require.Error(t, runStatefulSetCleanup(t, m, cluster, blue))
		require.Equal(t, 1, deletes)
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(replacement), &appsv1.StatefulSet{}))
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pod), &corev1.Pod{}))
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{}))
	}
}

func TestStatefulSetCleanupUsesObservedDeletePreconditions(t *testing.T) {
	t.Parallel()
	for _, blue := range []bool{false, true} {
		cluster := newPhaseMachineCluster()
		revision := cluster.Status.BlueGreen.GreenRevision
		if blue {
			revision = cluster.Status.BlueGreen.BlueRevision
		}
		sts := newManagedCleanupStatefulSet(cluster, revision)
		c := fake.NewClientBuilder().WithScheme(newBlueGreenTestScheme(t)).WithObjects(sts).Build()
		writer := interceptor.NewClient(c, interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				t.Fatal("cleanup must use its live reader")
				return nil
			},
			Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
				options := (&client.DeleteOptions{}).ApplyOptions(opts)
				require.NotNil(t, options.Preconditions)
				require.NotNil(t, options.Preconditions.UID)
				require.Equal(t, object.GetUID(), *options.Preconditions.UID)
				require.NotNil(t, options.Preconditions.ResourceVersion)
				require.Equal(t, object.GetResourceVersion(), *options.Preconditions.ResourceVersion)
				changed := object.DeepCopyObject().(client.Object)
				changed.SetAnnotations(map[string]string{"changed": "after-read"})
				require.NoError(t, c.Update(ctx, changed))
				return c.Delete(ctx, object, opts...)
			},
		})
		err := runStatefulSetCleanup(t, &Manager{client: writer, reader: c}, cluster, blue)
		require.True(t, apierrors.IsConflict(err), "changed object must survive: %v", err)
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{}))
	}
}
