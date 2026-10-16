//go:build integration
// +build integration

package openbaorestore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	appopenbaorestore "github.com/kubebao/openbao-operator/internal/app/openbaorestore"
	openbaorestorecontroller "github.com/kubebao/openbao-operator/internal/controller/openbaorestore"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

func TestOpenBaoRestore_SetupWithManager_InitializesRestoreStatusFromPending(t *testing.T) {
	setAdmissionReady(t)

	ctx := context.Background()
	liveClient := startOpenBaoRestoreManager(t)

	const namespace = "restore-test"
	createNamespace(t, ctx, liveClient, namespace)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-validation",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "test-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "https://objectstore.example.com",
					Bucket:   "backups",
				},
				Key: "snapshots/backup.snap",
			},
		},
	}
	require.NoError(t, liveClient.Create(ctx, restore))

	restoreKey := types.NamespacedName{Namespace: restore.Namespace, Name: restore.Name}
	require.Eventually(t, func() bool {
		current := &openbaov1alpha1.OpenBaoRestore{}
		if err := liveClient.Get(ctx, restoreKey, current); err != nil {
			return false
		}
		return current.Status.Phase != "" &&
			current.Status.Phase != openbaov1alpha1.RestorePhasePending &&
			current.Status.StartTime != nil &&
			current.Status.SnapshotKey == "snapshots/backup.snap" &&
			slices.Contains(current.Finalizers, openbaov1alpha1.OpenBaoRestoreFinalizer)
	}, 20*time.Second, 200*time.Millisecond, "expected manager-driven reconcile to initialize restore status and leave pending phase")

	// Restore a persisted, already-released Unknown request. The manager must
	// publish its state without replaying an earlier transition event.
	require.Eventually(t, func() bool {
		return liveClient.Get(ctx, restoreKey, restore) == nil && restore.Status.Phase == openbaov1alpha1.RestorePhaseFailed
	}, 10*time.Second, 100*time.Millisecond)
	before := restore.DeepCopy()
	restore.Status.Phase = openbaov1alpha1.RestorePhaseUnknown
	restore.Status.AdministratorDisposition = openbaov1alpha1.RestoreAdministratorResume
	require.NoError(t, liveClient.Status().Patch(ctx, restore, client.MergeFrom(before)))
	require.Eventually(t, func() bool {
		families, err := controllermetrics.Registry.Gather()
		if err != nil {
			return false
		}
		for _, family := range families {
			if family.GetName() != "openbao_restore_state" {
				continue
			}
			for _, metric := range family.Metric {
				labels := map[string]string{}
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["namespace"] == namespace && labels["name"] == restore.Spec.Cluster {
					return metric.GetGauge().GetValue() == 6
				}
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "expected persisted Resume to replace running/failed telemetry")
}

func setAdmissionReady(t *testing.T) {
	t.Helper()

	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	admission.SetAdmissionDependenciesReady(true)
	t.Cleanup(func() {
		admission.SetAdmissionDependenciesReady(false)
	})
}

func startOpenBaoRestoreManager(t *testing.T) client.Client {
	t.Helper()
	return startOpenBaoRestoreManagerWith(t, nil, nil)
}

func startOpenBaoRestoreManagerWith(t *testing.T, seed func(client.Client), delegate appopenbaorestore.RestoreReconciler) client.Client {
	t.Helper()

	scheme := newIntegrationScheme(t)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}
	if assetsDir := getFirstFoundEnvTestBinaryDir(); assetsDir != "" {
		testEnv.BinaryAssetsDirectory = assetsDir
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, testEnv.Stop())
	})

	liveClient, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	if seed != nil {
		seed(liveClient)
	}

	mgrOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	}
	skipNameValidation := true
	mgrOptions.Controller.SkipNameValidation = &skipNameValidation

	mgr, err := ctrl.NewManager(cfg, mgrOptions)
	require.NoError(t, err)

	reconciler := &openbaorestorecontroller.OpenBaoRestoreReconciler{
		Client: mgr.GetClient(),
		RestoreReconciler: appopenbaorestore.NewRestoreReconciler(appopenbaorestore.RestoreDependencies{
			Client:    mgr.GetClient(),
			APIReader: mgr.GetAPIReader(),
			Scheme:    mgr.GetScheme(),
			Recorder:  mgr.GetEventRecorder("openbaorestore"),
		}),
	}
	if delegate != nil {
		reconciler.RestoreReconciler = delegate
	}
	require.NoError(t, reconciler.SetupWithManager(t.Context(), mgr))

	managerCtx, cancel := context.WithCancel(context.Background())
	managerErr := make(chan error, 1)
	go func() {
		managerErr <- mgr.Start(managerCtx)
	}()
	require.True(t, mgr.GetCache().WaitForCacheSync(managerCtx), "expected manager cache to sync before creating test resources")
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-managerErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("manager stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("manager did not stop within timeout")
		}
	})

	return liveClient
}

func newIntegrationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	return scheme
}

func getFirstFoundEnvTestBinaryDir() string {
	if assetsDir := os.Getenv("KUBEBUILDER_ASSETS"); assetsDir != "" {
		absoluteAssetsDir, err := filepath.Abs(assetsDir)
		if err != nil {
			return ""
		}
		return absoluteAssetsDir
	}

	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			assetsDir, err := filepath.Abs(filepath.Join(basePath, entry.Name()))
			if err != nil {
				return ""
			}
			return assetsDir
		}
	}
	return ""
}

func createNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()

	require.NoError(t, c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}))
}

// Keep persisted states unchanged so this test observes metric watches rather
// than depending on restore execution or transition side effects.
type preserveRestoreState struct{}

func (preserveRestoreState) Reconcile(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoRestore) (recon.Result, error) {
	return recon.Result{}, nil
}

func TestOpenBaoRestore_StateMetricsWatchLifecycle(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")
	const namespace = "restore-metrics-watch"
	var held, completed *openbaov1alpha1.OpenBaoRestore
	liveClient := startOpenBaoRestoreManagerWith(t, func(c client.Client) {
		createNamespace(t, t.Context(), c, namespace)
		create := func(name, cluster string, phase openbaov1alpha1.RestorePhase) *openbaov1alpha1.OpenBaoRestore {
			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					Cluster: cluster,
					Source: openbaov1alpha1.RestoreSource{
						Target: openbaov1alpha1.BackupTarget{Endpoint: "https://objectstore.example.com", Bucket: "backups"},
						Key:    "snapshot.snap",
					},
				},
			}
			require.NoError(t, c.Create(t.Context(), restore))
			restore.Status.Phase = phase
			require.NoError(t, c.Status().Update(t.Context(), restore))
			return restore
		}
		held = create("a-held", "target", openbaov1alpha1.RestorePhaseUnknown)
		completed = create("z-success", "target", openbaov1alpha1.RestorePhaseCompleted)
		create("unrelated", "other", openbaov1alpha1.RestorePhaseCompleted)
	}, preserveRestoreState{})
	// Existing requests must initialize the metric when the manager starts.
	waitForRestoreState(t, namespace, "target", 4, true)
	waitForRestoreState(t, namespace, "other", 2, true)
	require.NoError(t, liveClient.Delete(t.Context(), held))
	waitForRestoreState(t, namespace, "target", 2, true)

	completed.Status.Phase = openbaov1alpha1.RestorePhaseUnknown
	require.NoError(t, liveClient.Status().Update(t.Context(), completed))
	waitForRestoreState(t, namespace, "target", 4, true)
	completed.Status.AdministratorDisposition = openbaov1alpha1.RestoreAdministratorAbandon
	require.NoError(t, liveClient.Status().Update(t.Context(), completed))
	waitForRestoreState(t, namespace, "target", 7, true)

	require.NoError(t, liveClient.Delete(t.Context(), completed))
	waitForRestoreState(t, namespace, "target", 0, false)
	waitForRestoreState(t, namespace, "other", 2, true)
}

func waitForRestoreState(t *testing.T, namespace, cluster string, want float64, exists bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		families, err := controllermetrics.Registry.Gather()
		if err != nil {
			return false
		}
		for _, family := range families {
			if family.GetName() != "openbao_restore_state" {
				continue
			}
			for _, metric := range family.GetMetric() {
				labels := make(map[string]string)
				for _, label := range metric.GetLabel() {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["namespace"] == namespace && labels["name"] == cluster {
					return exists && metric.GetGauge().GetValue() == want
				}
			}
		}
		return !exists
	}, 10*time.Second, 50*time.Millisecond)
}
