//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
)

type policyClient struct {
	values      map[string]string
	readDenied  bool
	writeDenied bool
	writes      int
}

func (c *policyClient) ReadACLPolicy(_ context.Context, name string) (*string, error) {
	if c.readDenied {
		return nil, fmt.Errorf("read denied")
	}
	value, exists := c.values[name]
	if !exists {
		return nil, nil
	}
	return &value, nil
}

func (c *policyClient) WriteACLPolicy(_ context.Context, name, policy string) error {
	if c.writeDenied {
		return fmt.Errorf("approval denied")
	}
	c.values[name] = policy
	c.writes++
	return nil
}

type policyRepairObserver struct {
	observe func(*openbaov1alpha1.OpenBaoCluster)
}

func (r policyRepairObserver) Reconcile(_ context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	r.observe(cluster)
	return recon.Result{RequeueAfter: time.Second}, nil
}

func TestPolicyReconciliationStatus(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "policy-reconciliation")
	waitForOpenBaoClusterAdmissionPolicies(t, cluster.Namespace)
	cluster.Spec.ReconcilePolicies = true
	require.NoError(t, k8sClient.Create(ctx, cluster))
	cluster.Status.Initialized = true
	require.NoError(t, k8sClient.Status().Update(ctx, cluster))
	store := &policyClient{values: map[string]string{}}
	manager := &configuration.PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		return store, nil
	}}
	observations := 0
	applications := openbaocluster.NewApplications(openbaocluster.ApplicationsConfig{
		Client:           k8sClient,
		PolicyReconciler: manager,
		WorkloadPolicy:   openbaocluster.DefaultWorkloadResultPolicy(),
		WorkloadReconcilers: []openbaocluster.SubReconciler{policyRepairObserver{observe: func(c *openbaov1alpha1.OpenBaoCluster) {
			observations++
			if observations == 1 {
				require.NoError(t, configuration.RequirePolicyReady(c, portauth.PolicyNameOperator), "unobserved policies do not block infrastructure")
			}
		}}},
	})
	for _, step := range []struct {
		readDenied   bool
		writeDenied  bool
		deletePolicy bool
		writes       int
	}{
		{readDenied: true}, // Enabling before enrollment preserves existing operations.
		{writes: 3},
		{}, // A matching revision still reads live policies without rewriting them.
		{readDenied: true},
		{},
		{deletePolicy: true, writeDenied: true},
		{writes: 1},
	} {
		if cluster.Status.Workload != nil && cluster.Status.Workload.PolicyReconciliation != nil {
			cluster.Status.Workload.PolicyReconciliation.RetryAfter = nil
			cluster.Status.Workload.PolicyReconciliation.LastVerified = nil
		}
		store.readDenied, store.writeDenied = step.readDenied, step.writeDenied
		store.writes = 0
		if step.deletePolicy {
			delete(store.values, portauth.PolicyNameOperator)
		}
		hadVerified := cluster.Status.Workload != nil && cluster.Status.Workload.PolicyRevision != ""
		result, err := applications.ReconcileWorkload(ctx, logr.Discard(), cluster.DeepCopy(), cluster, nil)
		require.NoError(t, err)
		require.Equal(t, step.writes, store.writes)
		require.Nil(t, cluster.Status.Workload.LastError)
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
		if step.readDenied || step.writeDenied {
			require.Positive(t, result.RequeueAfter)
			if hadVerified {
				require.NotEmpty(t, cluster.Status.Workload.PolicyRevision, "preserve the last complete observation")
			}
			require.Equal(t, "PolicyReconciliationFailed", cluster.Status.Workload.PolicyReconciliation.LastError.Reason)
			if step.deletePolicy {
				require.Error(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameOperator))
				require.Contains(t, cluster.Status.Workload.PolicyReconciliation.Revisions, portauth.PolicyNameOperator)
				require.Empty(t, cluster.Status.Workload.PolicyReconciliation.Revisions[portauth.PolicyNameOperator])
			} else {
				require.NoError(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameOperator))
			}
		} else {
			require.Nil(t, cluster.Status.Workload.LastError)
			require.NoError(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameOperator))
		}
	}
	require.Equal(t, 7, observations, "infrastructure repair still runs when OpenBao policy reads or writes fail")
}

func TestPolicyReconciliationWithoutSelfInit(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "manual-policy-enrollment")
	waitForOpenBaoClusterAdmissionPolicies(t, cluster.Namespace)
	cluster.Spec.SelfInit = nil
	cluster.Spec.ReconcilePolicies = true
	require.NoError(t, k8sClient.Create(ctx, cluster))
}

func TestPolicyRepairAfterDeclaredIdleStrategyChange(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "policy-bundle-change")
	waitForOpenBaoClusterAdmissionPolicies(t, cluster.Namespace)
	cluster.Spec.ReconcilePolicies = true
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true},
		Requests: []openbaov1alpha1.SelfInitRequest{{
			Name: "audit", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "sys/audit/stdout",
			AuditDevice: &openbaov1alpha1.SelfInitAuditDevice{Type: "file",
				FileOptions: &openbaov1alpha1.FileAuditOptions{FilePath: "stdout"}},
		}},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	cluster.Status.Initialized = true
	cluster.Status.Phase = openbaov1alpha1.ClusterPhaseRunning
	cluster.Status.CurrentVersion = cluster.Spec.Version
	cluster.Status.ReadyReplicas = cluster.Spec.Replicas
	cluster.Status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyRollingUpdate
	cluster.Status.Conditions = []metav1.Condition{{Type: "Available", Status: metav1.ConditionTrue,
		Reason: "Ready", Message: "Fixture workload is ready", LastTransitionTime: metav1.Now()}}
	require.NoError(t, k8sClient.Status().Update(ctx, cluster))
	store := &policyClient{values: map[string]string{}}
	reconcile := func() {
		t.Helper()
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
		manager := &configuration.PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
			return store, nil
		}}
		_, err := manager.Reconcile(ctx, logr.Discard(), cluster)
		require.NoError(t, err)
		require.NoError(t, k8sClient.Status().Update(ctx, cluster))
	}
	reconcile()
	initialUID, initialRevision := cluster.UID, cluster.Status.Workload.PolicyRevision
	verified := cluster.Status.Workload.PolicyReconciliation.LastVerified.DeepCopy()
	delete(store.values, portauth.PolicyNameOperator)
	store.writes = 0
	reconcile()
	require.Zero(t, store.writes, "a fresh manager must honor the persisted verification cache")
	before := cluster.DeepCopy()
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{Strategy: openbaov1alpha1.UpdateStrategyBlueGreen}
	require.NoError(t, k8sClient.Patch(ctx, cluster, client.MergeFrom(before)))
	require.Equal(t, verified, cluster.Status.Workload.PolicyReconciliation.LastVerified)
	reconcile()
	require.Equal(t, initialUID, cluster.UID)
	require.NotEqual(t, initialRevision, cluster.Status.Workload.PolicyRevision)
	require.NotEmpty(t, store.values[portauth.PolicyNameOperator], "repair uses the declared bundle change, not a backdated status")
	require.NoError(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameOperator))
}
