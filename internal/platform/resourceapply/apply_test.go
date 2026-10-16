package resourceapply

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestApplyOwnedSetsOwnerReference(t *testing.T) {
	scheme := newTestScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithReturnManagedFields().Build()

	owner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "12345"}}
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}

	if err := ApplyOwned(context.Background(), k8sClient, scheme, owner, obj); err != nil {
		t.Fatalf("ApplyOwned() error = %v", err)
	}

	stored := &corev1.ConfigMap{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(stored.OwnerReferences) != 1 || stored.OwnerReferences[0].Name != owner.Name {
		t.Fatalf("expected controller owner reference for %q, got %#v", owner.Name, stored.OwnerReferences)
	}
	if got := stored.Annotations[constants.AnnotationOpenBaoOwnerUID]; got != string(owner.UID) {
		t.Fatalf("owner UID annotation = %q, want %q", got, owner.UID)
	}
}

func TestApplyOwnedResolvesLiveOwnerUID(t *testing.T) {
	scheme := newTestScheme(t)
	liveOwner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: types.UID("live-owner-uid")}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(liveOwner).WithReturnManagedFields().Build()

	staleOwner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}}
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}

	if err := ApplyOwned(context.Background(), k8sClient, scheme, staleOwner, obj); err != nil {
		t.Fatalf("ApplyOwned() error = %v", err)
	}

	stored := &corev1.ConfigMap{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(stored.OwnerReferences) != 1 || stored.OwnerReferences[0].UID != liveOwner.UID {
		t.Fatalf("expected controller owner reference UID %q, got %#v", liveOwner.UID, stored.OwnerReferences)
	}
}

func TestApplyObservedDoesNotSetOwnerReference(t *testing.T) {
	scheme := newTestScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithReturnManagedFields().Build()

	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}

	if _, err := ApplyObserved(context.Background(), k8sClient, obj, ""); err != nil {
		t.Fatalf("ApplyObserved() error = %v", err)
	}

	stored := &corev1.ConfigMap{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(stored.OwnerReferences) != 0 {
		t.Fatalf("expected no owner references, got %#v", stored.OwnerReferences)
	}
}

func TestApplyRetainedSetsOwnerUIDAnnotation(t *testing.T) {
	scheme := newTestScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithReturnManagedFields().Build()

	owner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: types.UID("owner-uid")}}
	obj := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-test-0", Namespace: "default"}}

	if err := ApplyRetained(context.Background(), k8sClient, owner, obj); err != nil {
		t.Fatalf("ApplyRetained() error = %v", err)
	}

	stored := &corev1.PersistentVolumeClaim{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(obj), stored); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := stored.Annotations[constants.AnnotationOpenBaoOwnerUID]; got != string(owner.UID) {
		t.Fatalf("owner UID annotation = %q, want %q", got, owner.UID)
	}
	if len(stored.OwnerReferences) != 0 {
		t.Fatalf("expected no owner references for retained resource, got %#v", stored.OwnerReferences)
	}
}

func TestApplyRetainedRejectsUnownedExistingResource(t *testing.T) {
	scheme := newTestScheme(t)
	existing := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-test-0", Namespace: "default"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(existing).WithReturnManagedFields().Build()

	owner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: types.UID("owner-uid")}}
	obj := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-test-0", Namespace: "default"}}

	err := ApplyRetained(context.Background(), k8sClient, owner, obj)
	if err == nil {
		t.Fatal("ApplyRetained() expected error")
	}
	if !strings.Contains(err.Error(), "requires OpenBaoCluster owner proof") {
		t.Fatalf("ApplyRetained() error = %q, want owner proof error", err.Error())
	}
}

func TestApplyOwnedRejectsUnownedExistingResource(t *testing.T) {
	scheme := newTestScheme(t)
	existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(existing).WithReturnManagedFields().Build()

	owner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: types.UID("owner-uid")}}
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}

	err := ApplyOwned(context.Background(), k8sClient, scheme, owner, obj)
	if err == nil {
		t.Fatal("ApplyOwned() expected error")
	}
	if !strings.Contains(err.Error(), "requires OpenBaoCluster owner proof") {
		t.Fatalf("ApplyOwned() error = %q, want owner proof error", err.Error())
	}
}

func TestApplyOwnedAllowsExistingOwnedResource(t *testing.T) {
	scheme := newTestScheme(t)
	controller := true
	owner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: types.UID("owner-uid")}}
	existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "cfg",
		Namespace: "default",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: openbaov1alpha1.GroupVersion.String(),
			Kind:       "OpenBaoCluster",
			Name:       owner.Name,
			UID:        owner.UID,
			Controller: &controller,
		}},
	}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(owner, existing).WithReturnManagedFields().Build()

	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}
	if err := ApplyOwned(context.Background(), k8sClient, scheme, owner, obj); err != nil {
		t.Fatalf("ApplyOwned() error = %v", err)
	}
}

func TestPrepareOwnedRequiresScheme(t *testing.T) {
	t.Parallel()

	owner := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "12345"}}
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}

	err := PrepareOwned(obj, owner, nil)
	if err == nil {
		t.Fatal("PrepareOwned() expected error")
	}
	if !strings.Contains(err.Error(), "scheme is required") {
		t.Fatalf("PrepareOwned() error = %q, want scheme requirement", err.Error())
	}
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(openbaov1alpha1): %v", err)
	}
	return scheme
}
