package rolling

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	openbaoapi "github.com/kubebao/openbao-operator/internal/platform/testutil/openbao"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
)

func TestPatchStatusSSA_PreservesSiblingAdminOpsFieldsFromLatestObject(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)

	stored := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upgrade-cluster",
			Namespace: "ns1",
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				FromVersion:      "2.4.3",
				TargetVersion:    "2.4.4",
				CurrentPartition: 1,
			},
			UpgradeRequests: &openbaov1alpha1.UpgradeRequestStatus{
				LastHandledRetry: "retry-1",
			},
			Backup: &openbaov1alpha1.BackupStatus{
				LastFailureReason: "persist-me",
			},
		},
	}
	cluster := stored.DeepCopy()
	cluster.Status.Backup = nil
	cluster.Status.UpgradeRequests = &openbaov1alpha1.UpgradeRequestStatus{LastHandledRetry: "stale"}
	cluster.Status.Upgrade.CurrentPartition = 2

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(stored.DeepCopy()).
		Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), nil, nil).
		WithReader(c).
		WithAdminOpsStatusMutator(testAdminOpsMutator(c))

	if err := mgr.patchStatusSSA(context.Background(), cluster); err != nil {
		t.Fatalf("patchStatusSSA() error = %v", err)
	}

	updated := &openbaov1alpha1.OpenBaoCluster{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stored), updated); err != nil {
		t.Fatalf("Get() cluster error = %v", err)
	}
	if updated.Status.Backup == nil || updated.Status.Backup.LastFailureReason != "persist-me" {
		t.Fatalf("persisted backup = %#v, want sibling adminops field preserved", updated.Status.Backup)
	}
	if cluster.Status.Backup == nil || cluster.Status.Backup.LastFailureReason != "persist-me" {
		t.Fatalf("in-memory backup = %#v, want refreshed sibling adminops field", cluster.Status.Backup)
	}
	if updated.Status.Upgrade == nil || updated.Status.Upgrade.CurrentPartition != 2 {
		t.Fatalf("persisted upgrade = %#v, want updated rolling status", updated.Status.Upgrade)
	}
	if updated.Status.UpgradeRequests == nil || updated.Status.UpgradeRequests.LastHandledRetry != "retry-1" {
		t.Fatalf("persisted requests = %#v, want latest acknowledgement preserved", updated.Status.UpgradeRequests)
	}
}

func TestWaitForFinalizationConverged_WaitsForStatefulSetConvergence(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upgrade-cluster",
			Namespace: "ns1",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
		},
	}

	partition := int32(0)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"},
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: &partition,
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas:   3,
			UpdatedReplicas: 2,
			CurrentRevision: "rev-old",
			UpdateRevision:  "rev-new",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).Build()
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return &openbaoapi.MockClusterActions{}, nil
	}, nil)

	converged, err := mgr.waitForFinalizationConverged(context.Background(), testr.New(t), cluster)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if converged {
		t.Fatalf("expected convergence check to wait when StatefulSet has not converged")
	}
}

func TestWaitForFinalizationConverged_SucceedsWhenStatefulSetPodsAndHealthConverged(t *testing.T) {
	m, cluster, _, observer := rollingRaftFixture(t, 0)
	converged, err := m.waitForFinalizationConverged(t.Context(), logr.Discard(), cluster)
	if err != nil || converged {
		t.Fatalf("first observation: converged=%v err=%v", converged, err)
	}
	for _, name := range []string{"bao-1", "bao-2"} {
		peer := observer.state.Servers[name]
		peer.LastContact = "2ms"
		observer.state.Servers[name] = peer
	}
	converged, err = m.waitForFinalizationConverged(t.Context(), logr.Discard(), cluster)
	if err != nil || !converged {
		t.Fatalf("fresh caught-up observation: converged=%v err=%v", converged, err)
	}
}

func TestWaitForFinalizationConverged_RepairsStalePartitionWhenStatusIsAlreadyComplete(t *testing.T) {
	m, cluster, sts, _ := rollingRaftFixture(t, 0)
	partition := int32(3)
	sts.Spec.UpdateStrategy.RollingUpdate.Partition = &partition
	if err := m.client.Update(t.Context(), sts); err != nil {
		t.Fatal(err)
	}
	converged, err := m.waitForFinalizationConverged(t.Context(), logr.Discard(), cluster)
	if err != nil || converged {
		t.Fatalf("partition repair: converged=%v err=%v", converged, err)
	}
	if err := m.client.Get(t.Context(), client.ObjectKeyFromObject(sts), sts); err != nil {
		t.Fatal(err)
	}
	if *sts.Spec.UpdateStrategy.RollingUpdate.Partition != 0 {
		t.Fatalf("partition=%d", *sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	}
}

func TestPatchFinalizedUpgradeStatus_ClearsUpgradeWithoutTouchingCurrentVersion(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upgrade-cluster",
			Namespace: "ns1",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version: "2.4.4",
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			CurrentVersion: "2.4.3",
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				FromVersion:      "2.4.3",
				TargetVersion:    "2.4.4",
				CurrentPartition: 0,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(withoutUpgradeStatus(cluster)).
		Build()
	seedUpgradeStatus(t, c, cluster)
	mgr := newManagerWithClientFactory(c, scheme, backup.NewUpgradeStrategyRuntime(c, scheme), nil, nil).
		WithReader(c).
		WithAdminOpsStatusMutator(testAdminOpsMutator(c))

	if err := mgr.patchFinalizedUpgradeStatus(context.Background(), cluster); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cluster.Status.Upgrade != nil {
		t.Fatalf("expected upgrade status to be cleared after finalization")
	}
	if cluster.Status.CurrentVersion != "2.4.3" {
		t.Fatalf("expected CurrentVersion to remain unchanged, got %q", cluster.Status.CurrentVersion)
	}
}
