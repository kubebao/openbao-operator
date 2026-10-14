package adminops

import (
	"crypto/sha256"
	"fmt"
	"testing"

	configbuilder "github.com/kubebao/openbao-operator/internal/adapter/config"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"

	"github.com/go-logr/logr"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

func TestPolicyGateBeforeNewOperations(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Spec.ReconcilePolicies = true
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}}
	plan := reconcilerPlan{upgradeReconcilers: []subReconciler{fakeSubReconciler{}}}
	reconcilers := plan.orderedFor(cluster)
	require.Len(t, reconcilers, 1)
	_, err := reconcilers[0].Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err, "incomplete enrollment must preserve existing operation behavior")
	cluster.Status.Workload = &openbaov1alpha1.WorkloadControllerStatus{
		PolicyReconciliation: &openbaov1alpha1.PolicyReconciliationStatus{Revisions: map[string]string{}},
	}
	for _, policy := range configbuilder.OperatorPolicies(cluster) {
		cluster.Status.Workload.PolicyReconciliation.Revisions[policy.Name] = fmt.Sprintf("%x", sha256.Sum256([]byte(policy.Policy)))
	}
	_, err = reconcilers[0].Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	cluster.Status.Workload.PolicyReconciliation.Revisions[portauth.PolicyNameUpgrade] = ""
	cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{Operation: openbaov1alpha1.ClusterOperationUpgrade}
	_, err = reconcilers[0].Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err, "running operations must observe completion and release their lock")
	plan.upgradeReconcilers = []subReconciler{
		fakeMutatingSubReconciler{mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.OperationLock = nil }},
		fakeSubReconciler{},
	}
	reconcilers = plan.orderedFor(cluster)
	_, err = reconcilers[0].Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	_, err = reconcilers[1].Reconcile(t.Context(), logr.Discard(), cluster)
	require.Error(t, err, "a new operation cannot start after the previous operation releases its lock")
}

func TestUnapprovedUpgradeDoesNotBlockBackup(t *testing.T) {
	for _, blocked := range []string{portauth.PolicyNameUpgrade, portauth.PolicyNameBackup} {
		t.Run(blocked, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{}
			cluster.Spec.ReconcilePolicies = true
			cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{}
			cluster.Status.Workload = &openbaov1alpha1.WorkloadControllerStatus{
				PolicyReconciliation: &openbaov1alpha1.PolicyReconciliationStatus{Revisions: map[string]string{}},
			}
			for _, policy := range configbuilder.OperatorPolicies(cluster) {
				if policy.Name != blocked {
					cluster.Status.Workload.PolicyReconciliation.Revisions[policy.Name] = fmt.Sprintf("%x", sha256.Sum256([]byte(policy.Policy)))
				}
			}
			cluster.Status.Workload.PolicyReconciliation.Revisions[blocked] = ""
			var calls []string
			app := applicationForTest(reconcilerPlan{
				upgradeReconcilers: []subReconciler{recordingSubReconciler{name: portauth.PolicyNameUpgrade, calls: &calls}},
				backupReconciler:   recordingSubReconciler{name: portauth.PolicyNameBackup, calls: &calls},
			}, nil, nil)
			result, err := app.Reconcile(t.Context(), logr.Discard(), cluster.DeepCopy(), cluster, nil)
			require.NoError(t, err)
			require.Len(t, calls, 1)
			require.NotContains(t, calls, blocked)
			require.Positive(t, result.RequeueAfter)
			require.Equal(t, "PoliciesNotReady", cluster.Status.AdminOps.LastError.Reason)
		})
	}
}

func TestActiveRetentionObservesWithoutOpenBaoPolicyOrOperationLock(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Spec.ReconcilePolicies = true
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{}
	cluster.Status.Workload = &openbaov1alpha1.WorkloadControllerStatus{
		PolicyReconciliation: &openbaov1alpha1.PolicyReconciliationStatus{Revisions: map[string]string{portauth.PolicyNameBackup: "stale"}},
	}
	cluster.Status.Backup = &openbaov1alpha1.BackupStatus{Retention: &openbaov1alpha1.BackupRetentionStatus{
		Active: &openbaov1alpha1.BackupRetentionRun{Name: "retention"},
	}}
	var calls []string
	plan := reconcilerPlan{backupReconciler: recordingSubReconciler{name: "retention", calls: &calls}}
	_, err := plan.orderedFor(cluster)[0].Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, []string{"retention"}, calls)
	cluster.Status.Backup.Retention.Active = nil
	_, err = plan.orderedFor(cluster)[0].Reconcile(t.Context(), logr.Discard(), cluster)
	require.Error(t, err, "ordinary backup remains gated by its stale policy")
}
