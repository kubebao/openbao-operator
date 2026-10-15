package configuration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	configbuilder "github.com/kubebao/openbao-operator/internal/adapter/config"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type policyStore struct {
	values  map[string]string
	deny    string
	readErr error
	reads   []string
	writes  []string
}

func (s *policyStore) ReadACLPolicy(_ context.Context, name string) (*string, error) {
	s.reads = append(s.reads, name)
	if s.readErr != nil {
		return nil, s.readErr
	}
	value, exists := s.values[name]
	if !exists {
		return nil, nil
	}
	return &value, nil
}

func (s *policyStore) WriteACLPolicy(_ context.Context, name, policy string) error {
	s.writes = append(s.writes, name)
	if name == s.deny {
		return portopenbao.NewAPIError("approval denied", http.StatusForbidden, nil)
	}
	s.values[name] = policy
	return nil
}

func TestPolicyReconciliation(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Spec.ReconcilePolicies = true // Manual enrollment does not require self-init.
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{}
	store := &policyStore{values: map[string]string{}}
	manager := &PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		return store, nil
	}}
	_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Empty(t, store.values)
	cluster.Status.Initialized = true
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Len(t, store.writes, 4)
	require.NotContains(t, store.values, portauth.PolicyNameApproval)
	complete := cluster.Status.Workload.PolicyRevision

	for _, tc := range []struct {
		name   string
		change func()
		writes []string
		err    string
	}{
		{name: "unchanged"},
		{name: "deleted", change: func() { delete(store.values, portauth.PolicyNameOperator) }, writes: []string{portauth.PolicyNameOperator}},
		{name: "drift", change: func() { store.values[portauth.PolicyNameRestore] += "\n" }, writes: []string{portauth.PolicyNameRestore}},
		{name: "transient read", change: func() { store.readErr = fmt.Errorf("connection reset") }, err: "connection reset"},
		{name: "read recovery", change: func() { store.readErr = nil }},
		{name: "partial repair", change: func() {
			delete(store.values, portauth.PolicyNameUpgrade)
			delete(store.values, portauth.PolicyNameBackup)
			store.deny = portauth.PolicyNameUpgrade
		}, writes: []string{portauth.PolicyNameUpgrade, portauth.PolicyNameBackup}, err: "approval denied"},
		{name: "write recovery", change: func() { store.deny = "" }, writes: []string{portauth.PolicyNameUpgrade}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster.Status.Workload.PolicyReconciliation.RetryAfter = nil
			cluster.Status.Workload.PolicyReconciliation.LastVerified = nil
			if tc.change != nil {
				tc.change()
			}
			store.reads, store.writes = nil, nil
			result, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.Equal(t, tc.writes, store.writes)
			require.Len(t, store.reads, 4, "one policy failure must not stop the rest")
			require.Equal(t, complete, cluster.Status.Workload.PolicyRevision, "retain last complete observation")
			require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameBackup))
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				require.Positive(t, result.RequeueAfter)
				store.reads, store.writes = nil, nil
				_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
				require.NoError(t, err, "cooldown is recorded in status")
				require.Empty(t, store.reads)
				if store.deny != "" {
					require.Equal(t, 5*time.Minute, result.RequeueAfter)
					require.Error(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
				} else {
					require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade), "read failures retain observations")
				}
				return
			}
			require.NoError(t, err)
			require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
		})
	}
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{Strategy: "BlueGreen"}
	require.Error(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
	require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameBackup), "an upgrade permission change leaves backup ready")
	cluster.Spec.ReconcilePolicies = false
	require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
}

func TestPolicyAuthenticationBackoff(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{}
			cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
			calls := 0
			manager := &PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
				calls++
				return nil, portopenbao.NewAPIError("login", code, nil)
			}}
			result, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.Error(t, err)
			want := 5 * time.Minute
			if code == http.StatusServiceUnavailable {
				want = 30 * time.Second
			}
			require.Equal(t, want, result.RequeueAfter)
			_, _ = manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.Equal(t, 1, calls)
			cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{}
			_, _ = manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.Equal(t, 2, calls, "new desired permissions bypass the old retry deadline")
		})
	}
}

func TestPolicyEnrollmentObservations(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{}
	store := &policyStore{values: map[string]string{}, readErr: portopenbao.NewAPIError("not enrolled", http.StatusForbidden, nil)}
	manager := &PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		return store, nil
	}}
	_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.Error(t, err)
	for _, name := range []string{portauth.PolicyNameBackup, portauth.PolicyNameUpgrade} {
		require.NoError(t, RequirePolicyReady(cluster, name), "an enrollment failure must not stop existing operations")
	}
	store.readErr, store.deny = nil, portauth.PolicyNameUpgrade
	cluster.Status.Workload.PolicyReconciliation.RetryAfter = nil
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.Error(t, err)
	require.Empty(t, cluster.Status.Workload.PolicyRevision, "the complete bundle has never succeeded")
	require.Error(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade), "a known missing policy blocks its operation")
	require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameBackup))
}

func TestPolicyVerificationInterval(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
	store := &policyStore{values: map[string]string{}}
	logins := 0
	manager := &PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		logins++
		return store, nil
	}}
	_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	verified := cluster.Status.Workload.PolicyReconciliation.LastVerified.DeepCopy()
	store.reads, store.writes = nil, nil
	for range 3 {
		result, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
		require.NoError(t, err)
		require.Greater(t, result.RequeueAfter, 4*time.Minute)
	}
	require.Equal(t, 1, logins)
	require.Empty(t, store.reads)
	require.Equal(t, verified, cluster.Status.Workload.PolicyReconciliation.LastVerified)
	delete(store.values, portauth.PolicyNameUpgrade)
	cluster.Status.Workload.PolicyReconciliation.LastVerified.Time = time.Now().Add(-6 * time.Minute)
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, []string{portauth.PolicyNameUpgrade}, store.writes)
	require.Len(t, store.reads, 3)
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{}
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, 3, logins, "desired changes bypass the successful verification interval")
}

func TestIdleStrategyChangeRepairsDeletedPolicyWithoutWaitingForVerificationInterval(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.UID = "stable-target"
	cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
	store := &policyStore{values: map[string]string{}}
	logins := 0
	manager := &PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		logins++
		return store, nil
	}}
	_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	initialRevision := cluster.Status.Workload.PolicyRevision
	verified := cluster.Status.Workload.PolicyReconciliation.LastVerified.DeepCopy()
	approval := configbuilder.OperatorPolicyApproval(cluster)
	delete(store.values, portauth.PolicyNameOperator)
	store.writes = nil
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Empty(t, store.writes)
	require.Equal(t, 1, logins, "unchanged desired state retains the verification cache")
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{Strategy: openbaov1alpha1.UpdateStrategyBlueGreen}
	require.Equal(t, approval, configbuilder.OperatorPolicyApproval(cluster), "both strategies are already approved")
	require.Equal(t, verified, cluster.Status.Workload.PolicyReconciliation.LastVerified, "do not backdate verification")
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, 2, logins, "the changed desired bundle requires fresh authenticated verification")
	require.Contains(t, store.writes, portauth.PolicyNameOperator)
	require.Contains(t, store.writes, portauth.PolicyNameUpgrade)
	require.NotEqual(t, initialRevision, cluster.Status.Workload.PolicyRevision)
	require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameOperator))
	require.Nil(t, cluster.Status.Workload.PolicyReconciliation.LastError)
}

func TestPolicyReconciliationDuringStrategyChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lock     bool
		accepted openbaov1alpha1.UpdateStrategyType
	}{
		{name: "active upgrade", lock: true, accepted: openbaov1alpha1.UpdateStrategyBlueGreen},
		{name: "lost operation lock", accepted: openbaov1alpha1.UpdateStrategyBlueGreen},
		{name: "inferred strategy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{}
			cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
			cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{Strategy: openbaov1alpha1.UpdateStrategyBlueGreen}
			cluster.Status.AcceptedUpgradeStrategy = tc.accepted
			cluster.Status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{Phase: openbaov1alpha1.PhaseCleanup}
			if tc.lock {
				cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{Operation: openbaov1alpha1.ClusterOperationUpgrade}
			}
			store := &policyStore{values: map[string]string{}}
			manager := &PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
				return store, nil
			}}
			_, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			activeRevision := cluster.Status.Workload.PolicyRevision
			approval := configbuilder.OperatorPolicyApproval(cluster)
			activePolicy := store.values[portauth.PolicyNameUpgrade]
			require.Contains(t, activePolicy, "sys/storage/raft/remove-peer")

			cluster.Spec.Upgrade.Strategy = openbaov1alpha1.UpdateStrategyRollingUpdate
			require.Equal(t, approval, configbuilder.OperatorPolicyApproval(cluster), "strategy changes need no new approval")
			store.writes = nil
			_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.Empty(t, store.writes, "retain the policy used by the running upgrade")
			require.Equal(t, activeRevision, cluster.Status.Workload.PolicyRevision)
			require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade), "allow operation lock recovery")

			delete(store.values, portauth.PolicyNameUpgrade)
			cluster.Status.Workload.PolicyReconciliation.LastVerified = nil
			_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.Equal(t, []string{portauth.PolicyNameUpgrade}, store.writes)
			require.Equal(t, activePolicy, store.values[portauth.PolicyNameUpgrade], "repair with the running strategy's contents")

			delete(store.values, portauth.PolicyNameUpgrade)
			cluster.Status.Workload.PolicyReconciliation.LastVerified = nil
			store.deny = portauth.PolicyNameUpgrade
			result, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.Error(t, err)
			require.Equal(t, 5*time.Minute, result.RequeueAfter)
			require.Error(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))

			cluster.Status.OperationLock = nil
			cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseIdle
			store.deny, store.writes = "", nil
			_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
			require.NoError(t, err)
			require.Equal(t, []string{portauth.PolicyNameUpgrade}, store.writes, "completion bypasses the old bundle's cooldown")
			require.NotContains(t, store.values[portauth.PolicyNameUpgrade], "sys/storage/raft/remove-peer")
			require.NotEqual(t, activeRevision, cluster.Status.Workload.PolicyRevision)
			require.NoError(t, RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
			require.Equal(t, openbaov1alpha1.UpdateStrategyRollingUpdate, cluster.Spec.Upgrade.Strategy)
		})
	}
}
