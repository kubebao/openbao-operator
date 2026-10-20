package openbaocluster

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type runtimeInitManagerStub struct{}

func (runtimeInitManagerStub) Reconcile(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
) (recon.Result, error) {
	return recon.Result{}, nil
}

type runtimeRaftStub struct{}

func (*runtimeRaftStub) ReconcileAutopilotConfig(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
) error {
	return nil
}

func (*runtimeRaftStub) PrepareScaleDown(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
	string,
	int32,
	int32,
) error {
	return nil
}

func (*runtimeRaftStub) PrepareReadReplicaScaleDown(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
	string,
	int32,
	int32,
) error {
	return nil
}

func (*runtimeRaftStub) ReadRaftConfiguration(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
) (*portopenbao.RaftConfigurationResponse, error) {
	return nil, nil
}

func (*runtimeRaftStub) ReadRaftAutopilotState(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
) (*portopenbao.RaftAutopilotStateResponse, error) {
	return nil, nil
}

func TestNewRuntimeApplicationsBuildsApplicationBoundary(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	applications := NewRuntimeApplications(RuntimeApplicationsConfig{
		Kubernetes: RuntimeKubernetesConfig{
			Client:    k8sClient,
			APIReader: k8sClient,
			Scheme:    scheme,
		},
	})

	require.NotNil(t, applications)
	require.NotNil(t, applications.config.AdminOpsApplication)
	assert.False(t, applications.InitializationConfigured())
}

func TestNewRuntimeApplicationsWiresExplicitRaftRuntime(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	raftRuntime := &runtimeRaftStub{}
	applications := NewRuntimeApplications(RuntimeApplicationsConfig{
		Kubernetes: RuntimeKubernetesConfig{
			Client:    k8sClient,
			APIReader: k8sClient,
			Scheme:    scheme,
		},
		OpenBao: RuntimeOpenBaoConfig{
			InitManager: runtimeInitManagerStub{},
			Raft:        raftRuntime,
		},
	})

	require.Len(t, applications.config.WorkloadReconcilers, 7)
	_, cleanupFirst := applications.config.WorkloadReconcilers[0].(*terminalPodCleanupReconciler)
	require.True(t, cleanupFirst)
	infra, ok := applications.config.WorkloadReconcilers[2].(*infraReconciler)
	require.True(t, ok)
	assert.Same(t, raftRuntime, infra.deps.ScaleDown.Runtime)
	assert.Same(t, raftRuntime, infra.deps.ScaleDown.ReadReplicaRuntime)
	autopilot, ok := applications.config.WorkloadReconcilers[6].(*autopilotConfigReconciler)
	require.True(t, ok)
	assert.Same(t, raftRuntime, autopilot.autopilotRuntime)
	assert.Same(t, raftRuntime, applications.config.StatusDependencies.MembershipRuntime)
	assert.True(t, applications.InitializationConfigured())
}

func TestApplicationsRequireConfiguredBoundary(t *testing.T) {
	ctx := context.Background()
	logger := logr.Discard()
	cluster := &openbaov1alpha1.OpenBaoCluster{}

	var applications *Applications
	_, err := applications.ReconcileWorkload(ctx, logger, cluster.DeepCopy(), cluster, nil)
	assert.EqualError(t, err, "workload application client is required")
	_, err = applications.ReconcileAdminOps(ctx, logger, cluster.DeepCopy(), cluster, nil)
	assert.EqualError(t, err, "admin operations application is required")
	_, err = applications.GatherStatusState(ctx, logger, cluster)
	assert.EqualError(t, err, "status application is required")
	assert.EqualError(t, applications.HandleDeletion(ctx, logger, cluster), "deletion application is required")
	assert.False(t, applications.InitializationConfigured())
}

func TestApplicationsReportsConfiguredInitialization(t *testing.T) {
	applications := NewApplications(ApplicationsConfig{InitializationConfigured: true})
	assert.True(t, applications.InitializationConfigured())
}

func TestRestoreHoldStopsManagement(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	app := NewApplications(ApplicationsConfig{
		Client:              fake.NewClientBuilder().WithScheme(scheme).Build(),
		AdminOpsApplication: &AdminOpsApplication{},
	})
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Annotations = map[string]string{constants.AnnotationRestoreHold: "operation"}
	// Unconfigured collaborators would fail if either management plane ran.
	_, err := app.ReconcileWorkload(t.Context(), logr.Discard(), cluster.DeepCopy(), cluster, nil)
	require.NoError(t, err)
	_, err = app.ReconcileAdminOps(t.Context(), logr.Discard(), cluster.DeepCopy(), cluster, nil)
	require.NoError(t, err)
	require.ErrorContains(t, app.HandleDeletion(t.Context(), logr.Discard(), cluster), "held")
}

func TestTerminalPodCleanupRequiresUncachedReader(t *testing.T) {
	r := NewTerminalPodCleanupReconciler(nil, nil)
	result, err := r.Reconcile(t.Context(), logr.Discard(), &openbaov1alpha1.OpenBaoCluster{})
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
}

func TestTerminalPodCleanupReadErrorsDoNotBlockWorkload(t *testing.T) {
	for _, readErr := range []error{
		apierrors.NewServiceUnavailable("Pod API unavailable"),
		apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil),
	} {
		t.Run(readErr.Error(), func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant", UID: "cluster-uid"},
				Spec:       openbaov1alpha1.OpenBaoClusterSpec{Profile: openbaov1alpha1.ProfileDevelopment},
			}
			reads := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster.DeepCopy()).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
						reads++
						return readErr
					},
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						t.Fatal("cleanup must not delete after an incomplete authority read")
						return nil
					},
				}).Build()
			continued := false
			_, err := RunWorkloadReconcilers(t.Context(), c, logr.Discard(), cluster.DeepCopy(), cluster,
				[]SubReconciler{NewTerminalPodCleanupReconciler(c, c), workloadStepFunc(
					func(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
						continued = true
						return recon.Result{}, nil
					})}, nil, nil, DefaultWorkloadResultPolicy())
			require.NoError(t, err)
			require.Equal(t, 1, reads)
			require.True(t, continued, "cleanup failure must not block later workload steps")
			require.Nil(t, cluster.Status.Workload.LastError)
		})
	}
}
