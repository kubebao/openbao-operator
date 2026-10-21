package openbaocluster

import (
	"context"
	"errors"
	"testing"
	"time"

	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"

	"github.com/go-logr/logr"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

type autopilotRuntimeStub struct {
	reconcileFunc func(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error
}

func TestServiceIPFamilyChangeWaitsForConfigurationCorrection(t *testing.T) {
	result, handled := workloadResultForError(errors.New("primary IP family change"),
		&openbaov1alpha1.ControllerErrorStatus{Reason: constants.ReasonServiceIPFamilyChangeNotSupported},
		DefaultWorkloadResultPolicy())
	require.True(t, handled)
	require.Zero(t, result.RequeueAfter)
}

// Deleting the held StatefulSet is not a spec change, and without ownership watches nothing
// else would wake the reconcile that recreates it.
func TestAzureWorkloadIdentityRecreateRequeuesForUserAction(t *testing.T) {
	policy := DefaultWorkloadResultPolicy()
	result, handled := workloadResultForError(errors.New("recreate required"),
		&openbaov1alpha1.ControllerErrorStatus{Reason: constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired},
		policy)
	require.True(t, handled)
	require.Equal(t, policy.RequeueShort, result.RequeueAfter)
}

func (s autopilotRuntimeStub) ReconcileAutopilotConfig(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if s.reconcileFunc != nil {
		return s.reconcileFunc(ctx, logger, cluster)
	}
	return nil
}

type readerStub struct {
	getFunc func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
}

func (s readerStub) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if s.getFunc != nil {
		return s.getFunc(ctx, key, obj, opts...)
	}
	return nil
}

func (s readerStub) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return nil
}

func TestAutopilotConfigReconciler_Reconcile_UsesStatefulSetReplicasDuringScaleDown(t *testing.T) {
	scheme := runtime.NewScheme()
	assert.NoError(t, clientgoscheme.AddToScheme(scheme))
	assert.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "openbaocluster-demo",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}
	currentReplicas := int32(2)
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &currentReplicas,
		},
	}

	k8sReader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(statefulSet).
		Build()

	var gotReplicas int32
	reconciler := &autopilotConfigReconciler{
		autopilotRuntime: autopilotRuntimeStub{
			reconcileFunc: func(_ context.Context, _ logr.Logger, gotCluster *openbaov1alpha1.OpenBaoCluster) error {
				gotReplicas = gotCluster.Spec.Replicas
				return nil
			},
		},
		statefulSetReader: k8sReader,
		requeueShort:      5 * time.Second,
	}

	result, err := reconciler.Reconcile(context.Background(), logr.Discard(), cluster)
	assert.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
	assert.Equal(t, int32(2), gotReplicas)
	assert.Equal(t, int32(1), cluster.Spec.Replicas)
}

func TestAutopilotConfigReconciler_Reconcile_UsesRevisionedStatefulSetDuringScaleDown(t *testing.T) {
	scheme := runtime.NewScheme()
	assert.NoError(t, clientgoscheme.AddToScheme(scheme))
	assert.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "openbaocluster-demo",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				BlueRevision: "blue-rev",
			},
		},
	}
	currentReplicas := int32(2)
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-blue-rev",
			Namespace: cluster.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &currentReplicas,
		},
	}

	k8sReader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(statefulSet).
		Build()

	var gotReplicas int32
	reconciler := &autopilotConfigReconciler{
		autopilotRuntime: autopilotRuntimeStub{
			reconcileFunc: func(_ context.Context, _ logr.Logger, gotCluster *openbaov1alpha1.OpenBaoCluster) error {
				gotReplicas = gotCluster.Spec.Replicas
				return nil
			},
		},
		statefulSetReader: k8sReader,
		requeueShort:      5 * time.Second,
	}

	result, err := reconciler.Reconcile(context.Background(), logr.Discard(), cluster)
	assert.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
	assert.Equal(t, int32(2), gotReplicas)
	assert.Equal(t, int32(1), cluster.Spec.Replicas)
}

func TestAutopilotConfigReconciler_Reconcile_RequeuesWhenStatefulSetReadFails(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "openbaocluster-demo",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
		},
	}

	runtimeCalled := false
	reconciler := &autopilotConfigReconciler{
		autopilotRuntime: autopilotRuntimeStub{
			reconcileFunc: func(_ context.Context, _ logr.Logger, _ *openbaov1alpha1.OpenBaoCluster) error {
				runtimeCalled = true
				return nil
			},
		},
		statefulSetReader: readerStub{
			getFunc: func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
				return errors.New("boom")
			},
		},
		requeueShort: 5 * time.Second,
	}

	result, err := reconciler.Reconcile(context.Background(), logr.Discard(), cluster)
	assert.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter)
	assert.False(t, runtimeCalled)
}

func TestPolicyFailureStillRunsAutopilot(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "policy-failure", Namespace: "test"}}
	cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).
		WithStatusSubresource(cluster).WithReturnManagedFields().Build()
	attempts, autopilotCalls := 0, 0
	recorder := events.NewFakeRecorder(10)
	manager := &configuration.PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		attempts++
		return nil, portopenbao.NewAPIError("login", 403, nil)
	}}
	app := NewApplications(ApplicationsConfig{
		Client: c, PolicyReconciler: &policyConfigReconciler{manager: manager, recorder: recorder}, WorkloadPolicy: DefaultWorkloadResultPolicy(),
		WorkloadReconcilers: []SubReconciler{
			&autopilotConfigReconciler{statefulSetReader: readerStub{}, autopilotRuntime: autopilotRuntimeStub{
				reconcileFunc: func(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) error {
					autopilotCalls++
					return nil
				},
			}},
		},
	})
	for range 2 {
		result, err := app.ReconcileWorkload(t.Context(), logr.Discard(), cluster.DeepCopy(), cluster, nil)
		require.NoError(t, err)
		require.Greater(t, result.RequeueAfter, 4*time.Minute)
		require.Equal(t, "PolicyReconciliationFailed", cluster.Status.Workload.PolicyReconciliation.LastError.Reason)
		require.Nil(t, cluster.Status.Workload.LastError)
	}
	require.Len(t, recorder.Events, 1, "one warning on the failure transition, not on cooldown polls")
	require.Equal(t, 2, autopilotCalls)
	require.Equal(t, 1, attempts, "neither the second stage nor an unrelated reconcile bypasses cooldown")
}

type workloadStepFunc func(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (recon.Result, error)

func (f workloadStepFunc) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	return f(ctx, logger, cluster)
}

func TestPolicyCheckFollowsWorkloadAttempt(t *testing.T) {
	const outcomeDisabled = "disabled"
	const outcomeRequeue = "requeue"
	for _, outcome := range []string{"complete", outcomeRequeue, "error", outcomeDisabled} {
		t.Run(outcome, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "ordering", Namespace: "test"}}
			cluster.Spec.ReconcilePolicies = outcome != outcomeDisabled
			cluster.Status.Initialized = true
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).WithStatusSubresource(cluster).WithReturnManagedFields().Build()
			original := cluster.DeepCopy()
			cluster.Status.Workload = &openbaov1alpha1.WorkloadControllerStatus{LastError: &openbaov1alpha1.ControllerErrorStatus{Reason: "Previous"}}
			require.NoError(t, PatchWorkloadOwnedFields(t.Context(), c, logr.Discard(), original, cluster, "test-setup"))
			var calls []string
			manager := &configuration.PolicyManager{ClientFor: func(context.Context, *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
				require.Equal(t, []string{"infrastructure"}, calls, "infrastructure runs before an unavailable policy endpoint can delay it")
				calls = append(calls, "policy")
				return nil, portopenbao.NewAPIError("unavailable", 503, nil)
			}}
			app := NewApplications(ApplicationsConfig{Client: c, PolicyReconciler: manager, WorkloadPolicy: DefaultWorkloadResultPolicy(),
				WorkloadReconcilers: []SubReconciler{workloadStepFunc(func(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
					calls = append(calls, "infrastructure")
					switch outcome {
					case outcomeRequeue, outcomeDisabled:
						return recon.Result{RequeueAfter: time.Second}, nil
					case "error":
						return recon.Result{}, errors.New("infrastructure failed")
					default:
						return recon.Result{}, nil
					}
				})},
			})
			_, err := app.ReconcileWorkload(t.Context(), logr.Discard(), cluster.DeepCopy(), cluster, nil)
			if outcome == "error" {
				require.ErrorContains(t, err, "infrastructure failed")
			} else {
				require.NoError(t, err)
			}
			if outcome == outcomeDisabled {
				require.Equal(t, []string{"infrastructure"}, calls)
			} else {
				require.Equal(t, []string{"infrastructure", "policy"}, calls)
			}
			if outcome == outcomeRequeue || outcome == outcomeDisabled {
				require.Equal(t, "Previous", cluster.Status.Workload.LastError.Reason, "preserve pre-feature error handling")
			}
			persisted := &openbaov1alpha1.OpenBaoCluster{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), persisted))
			if outcome != outcomeDisabled {
				require.Equal(t, "PolicyReconciliationFailed", persisted.Status.Workload.PolicyReconciliation.LastError.Reason)
				require.NotNil(t, persisted.Status.Workload.PolicyReconciliation.RetryAfter)
			}
			if outcome == outcomeRequeue || outcome == outcomeDisabled {
				require.Equal(t, "Previous", persisted.Status.Workload.LastError.Reason)
			}
		})
	}
}
