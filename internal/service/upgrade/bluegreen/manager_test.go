package bluegreen

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	upgradecore "github.com/kubebao/openbao-operator/internal/service/upgrade/core"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

func TestManager_Reconcile_SkipsWhenNotBlueGreen(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version: "2.4.4",
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyRollingUpdate,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized:    true,
			CurrentVersion: "2.4.3",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		Build()
	infraMgr := workloadsvc.NewManager(c, scheme, "")
	mgr := NewManager(c, scheme, infraMgr, backup.NewUpgradeStrategyRuntime(c, scheme), portopenbao.ClientConfig{}, security.NewImageVerifier(logr.Discard(), c, nil), security.NewImageVerifier(logr.Discard(), c, nil), "")

	result, err := mgr.Reconcile(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Fatalf("expected no requeue")
	}
}

func TestEnsureIdleAndCleanupGreen_CleansStaleGreenAndReleasesLock(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.4",
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:         openbaov1alpha1.PhaseIdle,
				BlueRevision:  "blue",
				GreenRevision: "green",
			},
			OperationLock: &openbaov1alpha1.OperationLockStatus{
				Operation: openbaov1alpha1.ClusterOperationUpgrade,
				Holder:    upgradecore.UpgradeOperationLockHolder,
				Message:   "blue/green upgrade phase Idle",
			},
		},
	}

	greenStatefulSet := newManagedCleanupStatefulSet(cluster, cluster.Status.BlueGreen.GreenRevision)

	clientBuilder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster, greenStatefulSet)
	c := clientBuilder.Build()

	infraMgr := workloadsvc.NewManager(c, scheme, "")
	manager := NewManager(c, scheme, infraMgr, backup.NewUpgradeStrategyRuntime(c, scheme), portopenbao.ClientConfig{}, security.NewImageVerifier(logr.Discard(), c, nil), security.NewImageVerifier(logr.Discard(), c, nil), "")

	if err := manager.ensureIdleAndCleanupGreen(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("ensureIdleAndCleanupGreen: %v", err)
	}

	if cluster.Status.BlueGreen.GreenRevision != "" {
		t.Fatalf("expected green revision to be cleared, got %q", cluster.Status.BlueGreen.GreenRevision)
	}
	if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle {
		t.Fatalf("expected phase Idle, got %s", cluster.Status.BlueGreen.Phase)
	}
	if cluster.Status.OperationLock != nil {
		t.Fatalf("expected operation lock to be released")
	}

	staleGreenStatefulSet := &appsv1.StatefulSet{}
	err := c.Get(context.Background(), types.NamespacedName{Name: "test-green", Namespace: "default"}, staleGreenStatefulSet)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected stale Green StatefulSet to be deleted, got err=%v", err)
	}
}
