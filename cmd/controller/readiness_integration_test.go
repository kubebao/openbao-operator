//go:build integration

package controller

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/adapter/raft"
	appopenbaocluster "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/platform/testutil/managerprobe"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	initmanager "github.com/kubebao/openbao-operator/internal/service/init"
)

func TestControllerReadinessCacheAndWatchContract(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")
	config := managerprobe.Environment(t)
	for _, singleTenant := range []bool{false, true} {
		t.Run(map[bool]string{false: "multi-tenant", true: "single-tenant"}[singleTenant], func(t *testing.T) {
			watchNamespace := ""
			resource := "openbaoclusters"
			if singleTenant {
				watchNamespace = "default"
				resource = "secrets"
			}
			gate := managerprobe.NewListGate(resource)
			t.Cleanup(gate.Release)
			managerConfig := rest.CopyConfig(config)
			managerConfig.WrapTransport = gate.Wrap
			address := managerprobe.ProbeAddress(t)
			options := newManagerOptions(scheme, metricsserver.Options{BindAddress: "0"}, address, false, watchNamespace)
			warmup := true
			options.Controller.EnableWarmup = &warmup
			options.Controller.SkipNameValidation = &warmup
			var recording *managerprobe.RecordingCache
			options.NewCache = func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
				var err error
				recording, err = managerprobe.NewRecordingCache(config, opts)
				return recording, err
			}
			mgr, err := ctrl.NewManager(managerConfig, options)
			require.NoError(t, err)
			observed := &managerprobe.WarmupObserver{Manager: mgr}
			require.NoError(t, setupControllers(t.Context(), observed, readinessControllerRuntime(t, mgr, singleTenant)))
			require.NoError(t, addManagerHealthChecks(t.Context(), mgr, singleTenant))
			managerprobe.Start(t, mgr)
			select {
			case <-gate.Observed:
			case <-time.After(10 * time.Second):
				t.Fatal("cache did not request initial population")
			}
			require.Eventually(t, func() bool {
				return managerprobe.Status(address, "/healthz") == http.StatusOK
			}, 5*time.Second, 20*time.Millisecond)
			require.Equal(t, http.StatusInternalServerError, managerprobe.Status(address, "/readyz"))
			gate.Release()
			select {
			case <-mgr.Elected():
			case <-time.After(10 * time.Second):
				t.Fatal("controller warmup did not finish")
			}
			observed.Wait(t)
			recording.AssertMatchesWatches(t)
			require.Eventually(t, func() bool {
				return managerprobe.Status(address, "/readyz") == http.StatusOK
			}, 5*time.Second, 20*time.Millisecond)
		})
	}
}

func TestControllerStandbyReadinessAdmissionLifecycle(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	config := managerprobe.Environment(t)
	address := managerprobe.ProbeAddress(t)
	options := newManagerOptions(scheme, metricsserver.Options{BindAddress: "0"}, address, true, "")
	options.LeaderElectionNamespace = "default"
	skipNames := true
	options.Controller.SkipNameValidation = &skipNames
	kubeClient, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	managerprobe.HoldLeadership(t, kubeClient, options.LeaderElectionID)
	mgr, err := ctrl.NewManager(config, options)
	require.NoError(t, err)
	require.NoError(t, setupControllers(t.Context(), mgr, readinessControllerRuntime(t, mgr, false)))
	require.NoError(t, addManagerHealthChecks(t.Context(), mgr, false))
	managerprobe.Start(t, mgr)
	managerprobe.AdmissionLifecycle(t, mgr, address)
}

func readinessControllerRuntime(t *testing.T, mgr ctrl.Manager, singleTenant bool) controllerProcessRuntime {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	require.NoError(t, err)
	clientManager := openbao.NewClientManager(portopenbao.ClientConfig{})
	raftManager := raft.NewManager(clientset, raftClientFactoryProvider{clientManager: clientManager}, nil)
	initialization, err := initmanager.NewManager(mgr.GetConfig(), clientset, clientManager, raftManager)
	require.NoError(t, err)
	return controllerProcessRuntime{
		operatorNamespace: "default", singleTenantMode: singleTenant,
		openBaoRuntime: appopenbaocluster.RuntimeOpenBaoConfig{Raft: raftManager, InitManager: initialization},
	}
}
