package deletionops

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestCleanupRespectsDeletionPolicyForPVCs(t *testing.T) {
	tests := []struct {
		name           string
		policy         openbaov1alpha1.DeletionPolicy
		expectPVCExist bool
	}{
		{
			name:           "retain keeps PVCs",
			policy:         openbaov1alpha1.DeletionPolicyRetain,
			expectPVCExist: true,
		},
		{
			name:           "deletepvcs deletes PVCs",
			policy:         openbaov1alpha1.DeletionPolicyDeletePVCs,
			expectPVCExist: false,
		},
		{
			name:           "deleteall deletes PVCs",
			policy:         openbaov1alpha1.DeletionPolicyDeleteAll,
			expectPVCExist: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newCleanupTestCluster("cleanup-policy")
			pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-cleanup-policy-0")
			kubeClient := newCleanupTestClient(t, cluster, pvc)

			err := Cleanup(context.Background(), logr.Discard(), kubeClient, cluster, tt.policy)
			if err != nil {
				t.Fatalf("Cleanup() error = %v", err)
			}

			err = kubeClient.Get(
				context.Background(),
				types.NamespacedName{Namespace: cluster.Namespace, Name: pvc.Name},
				&corev1.PersistentVolumeClaim{},
			)

			if tt.expectPVCExist {
				if err != nil {
					t.Fatalf("expected PVC to exist, got error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected PVC to be deleted")
			}
			if !apierrors.IsNotFound(err) {
				t.Fatalf("expected not found error for PVC, got: %v", err)
			}
		})
	}
}

func TestDeletePVCsDeletesAllPVCs(t *testing.T) {
	cluster := newCleanupTestCluster("cleanup-delete")
	pvc1 := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-cleanup-delete-0")
	pvc2 := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-cleanup-delete-1")
	kubeClient := newCleanupTestClient(t, cluster, pvc1, pvc2)

	err := Cleanup(context.Background(), logr.Discard(), kubeClient, cluster, openbaov1alpha1.DeletionPolicyDeletePVCs)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	for _, pvcName := range []string{pvc1.Name, pvc2.Name} {
		err = kubeClient.Get(
			context.Background(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: pvcName},
			&corev1.PersistentVolumeClaim{},
		)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("expected PVC %s to be deleted, got error: %v", pvcName, err)
		}
	}
}

func TestDeletePVCsPreservesExistingACMESharedCachePVC(t *testing.T) {
	cluster := newCleanupTestCluster("cleanup-acme-cache")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		DirectoryURL: "https://example.invalid/acme",
		Domains:      []string{"example.com"},
		SharedCache: &openbaov1alpha1.ACMESharedCacheConfig{
			Mode:              openbaov1alpha1.ACMESharedCacheModeExistingPVC,
			ExistingClaimName: "shared-acme-cache",
		},
	}

	dataPVC := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-cleanup-acme-cache-0")
	cachePVC := newCleanupTestPVC(cluster.Namespace, cluster.Name, "shared-acme-cache")
	kubeClient := newCleanupTestClient(t, cluster, dataPVC, cachePVC)

	err := Cleanup(context.Background(), logr.Discard(), kubeClient, cluster, openbaov1alpha1.DeletionPolicyDeletePVCs)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	err = kubeClient.Get(
		context.Background(),
		types.NamespacedName{Namespace: cluster.Namespace, Name: dataPVC.Name},
		&corev1.PersistentVolumeClaim{},
	)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected data PVC to be deleted, got error: %v", err)
	}

	err = kubeClient.Get(
		context.Background(),
		types.NamespacedName{Namespace: cluster.Namespace, Name: cachePVC.Name},
		&corev1.PersistentVolumeClaim{},
	)
	if err != nil {
		t.Fatalf("expected existing ACME shared cache PVC to be preserved, got error: %v", err)
	}
}

func TestDeletePVCsSkipsLabelMatchedPVCWithoutOwnerProof(t *testing.T) {
	cluster := newCleanupTestCluster("cleanup-unowned")
	pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-cleanup-unowned-0")
	delete(pvc.Annotations, constants.AnnotationOpenBaoOwnerUID)
	kubeClient := newCleanupTestClient(t, cluster, pvc)

	err := Cleanup(context.Background(), logr.Discard(), kubeClient, cluster, openbaov1alpha1.DeletionPolicyDeletePVCs)
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	err = kubeClient.Get(
		context.Background(),
		types.NamespacedName{Namespace: cluster.Namespace, Name: pvc.Name},
		&corev1.PersistentVolumeClaim{},
	)
	if err != nil {
		t.Fatalf("expected unproven PVC to be preserved, got error: %v", err)
	}
}

func TestDeletePVCsPreservesForeignFinalizers(t *testing.T) {
	cluster := newCleanupTestCluster("cleanup-finalizers")
	pvc := newCleanupTestPVC(cluster.Namespace, cluster.Name, "data-cleanup-finalizers-0")
	pvc.Finalizers = []string{
		"kubernetes.io/pvc-protection",
		"external-provisioner.volume.kubernetes.io/finalizer",
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error = %v", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(openbao) error = %v", err)
	}
	patchCalls := 0
	deleteCalls := 0
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, pvc).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patchCalls++
				return c.Patch(ctx, obj, patch, opts...)
			},
			Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
				deleteCalls++
				if got := obj.GetFinalizers(); len(got) != 2 || got[0] != pvc.Finalizers[0] || got[1] != pvc.Finalizers[1] {
					t.Fatalf("Delete() finalizers = %v, want %v", got, pvc.Finalizers)
				}
				return nil
			},
		}).
		Build()

	if err := Cleanup(context.Background(), logr.Discard(), kubeClient, cluster, openbaov1alpha1.DeletionPolicyDeletePVCs); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if patchCalls != 0 {
		t.Fatalf("Patch() calls = %d, want 0", patchCalls)
	}
	if deleteCalls != 1 {
		t.Fatalf("Delete() calls = %d, want 1", deleteCalls)
	}
}

func newCleanupTestClient(t *testing.T, objs ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error = %v", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(openbao) error = %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()
}

func newCleanupTestCluster(name string) *openbaov1alpha1.OpenBaoCluster {
	stamp := metav1.Now()
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			UID:               types.UID(name + "-uid"),
			DeletionTimestamp: &stamp,
			Finalizers:        []string{openbaov1alpha1.OpenBaoClusterFinalizer},
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.4",
			Image:    "openbao/openbao:2.4.4",
			Replicas: 3,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled: true,
			},
			Storage: openbaov1alpha1.StorageConfig{
				Size: "10Gi",
			},
		},
	}
}

func newCleanupTestPVC(namespace, clusterName, name string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				constants.LabelOpenBaoCluster: clusterName,
			},
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: clusterName + "-uid",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
}
