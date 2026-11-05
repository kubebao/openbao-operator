package openbaocluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	app "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	adminopsapp "github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminops"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func TestAPIMigrationHoldSuppressesAdminOperations(t *testing.T) {
	for _, upgradeRequested := range []bool{false, true} {
		t.Run(map[bool]string{false: "due-backup-retention-and-restore-test", true: "upgrade-request"}[upgradeRequested], func(t *testing.T) {
			scheme := newOpenBaoClusterTestScheme(t)
			cluster := migrationHoldCluster()
			past := metav1.NewTime(time.Now().Add(-time.Hour))
			cluster.Spec.Backup = &api.BackupSchedule{
				Schedule: "* * * * *", Retention: &api.BackupRetention{MaxCount: 1},
				RestoreTest: &api.RestoreTest{Schedule: "* * * * *", EverySuccessfulBackups: 1, Namespace: "restore-test"},
			}
			cluster.Status.Backup = &api.BackupStatus{
				LastBackupTime: &past, LastAttemptTime: &past, NextScheduledBackup: &past,
				SuccessfulBackups: 2, RestoreTest: &api.RestoreTestStatus{LastScheduledAt: &past},
			}
			if upgradeRequested {
				cluster.Spec.Version = "2.6.5"
				cluster.Spec.Upgrade = &api.UpgradeConfig{Requests: &api.UpgradeRequestConfig{Retry: "retry-now"}}
			}
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&api.OpenBaoCluster{}).WithObjects(cluster).Build()
			checkpoint := errors.New("admin application reached status checkpoint")
			patches := 0
			admin := adminopsapp.NewApplication(adminopsapp.Dependencies{Client: c, APIReader: c, Scheme: scheme},
				func(context.Context, logr.Logger, *api.OpenBaoCluster, *api.OpenBaoCluster, upgrade.RequestAcknowledgements, string) error {
					patches++
					return checkpoint
				}, nil)
			parent := &OpenBaoClusterReconciler{
				Client: c, ControllerRuntime: ControllerRuntime{
					APIReader: c, SingleTenantMode: true, APIMigrationHold: true,
				},
				Applications: app.NewApplications(app.ApplicationsConfig{Client: c, AdminOpsApplication: admin}),
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)}
			before := &api.OpenBaoCluster{}
			require.NoError(t, c.Get(t.Context(), request.NamespacedName, before))
			r := &openBaoClusterAdminOpsReconciler{parent: parent}
			for range 2 {
				result, err := r.Reconcile(t.Context(), request)
				require.NoError(t, err)
				require.Equal(t, ctrl.Result{}, result)
			}
			require.Zero(t, patches, "hold must prevent entering operation/status applications")
			after := &api.OpenBaoCluster{}
			require.NoError(t, c.Get(t.Context(), request.NamespacedName, after))
			require.Equal(t, before, after, "hold must not reserve operations, acknowledge requests, or change history")

			// A new process normally supplies this value. Direct invocation here
			// proves the ordinary application path remains reachable when false.
			parent.APIMigrationHold = false
			_, err := r.Reconcile(t.Context(), request)
			require.ErrorIs(t, err, checkpoint)
			require.Positive(t, patches)
		})
	}
}

func TestAPIMigrationHoldPreservesWorkloadPolicyAndStatusPaths(t *testing.T) {
	scheme := newOpenBaoClusterTestScheme(t)
	cluster := migrationHoldCluster()
	cluster.Spec.ReconcilePolicies = true
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&api.OpenBaoCluster{}).WithObjects(cluster).Build()
	workload, policy := &migrationHoldStep{}, &migrationHoldStep{}
	parent := &OpenBaoClusterReconciler{
		Client: c, ControllerRuntime: ControllerRuntime{
			APIReader: c, SingleTenantMode: true, APIMigrationHold: true,
		},
		Applications: app.NewApplications(app.ApplicationsConfig{
			Client: c, WorkloadReconcilers: []app.SubReconciler{workload}, PolicyReconciler: policy,
			WorkloadPolicy: app.DefaultWorkloadResultPolicy(),
		}),
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)}
	_, err := (&openBaoClusterWorkloadReconciler{parent: parent}).Reconcile(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, 1, workload.calls)
	require.Equal(t, 1, policy.calls, "authentication/policy repair remains reachable")
	parent.Applications = newStatusTestApplications(c, scheme)
	_, err = (&openBaoClusterStatusReconciler{parent: parent}).Reconcile(t.Context(), request)
	require.NoError(t, err)
	current := &api.OpenBaoCluster{}
	require.NoError(t, c.Get(t.Context(), request.NamespacedName, current))
	require.Equal(t, cluster.Generation, current.Status.ObservedGeneration)
	require.NotEmpty(t, current.Status.Conditions)
}

type migrationHoldStep struct{ calls int }

func (s *migrationHoldStep) Reconcile(context.Context, logr.Logger, *api.OpenBaoCluster) (recon.Result, error) {
	s.calls++
	return recon.Result{}, nil
}

func migrationHoldCluster() *api.OpenBaoCluster {
	return &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "held", Namespace: "tenant", Generation: 2,
			Finalizers: []string{api.OpenBaoClusterFinalizer}},
		Spec: api.OpenBaoClusterSpec{Profile: api.ProfileDevelopment, Replicas: 1, Version: "2.6.4"},
		Status: api.OpenBaoClusterStatus{Initialized: true, CurrentVersion: "2.6.4", ReadyReplicas: 1,
			AcceptedUpgradeStrategy: api.UpdateStrategyRollingUpdate},
	}
}
