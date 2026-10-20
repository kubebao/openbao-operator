//go:build integration
// +build integration

package openbaocluster_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	security "github.com/kubebao/openbao-operator/internal/adapter/security"
	appopenbaocluster "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/controller/openbaocluster"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
	portsecurity "github.com/kubebao/openbao-operator/internal/port/security"
)

func TestSetupWithManager_ReconcilesClusterInSingleTenantMode(t *testing.T) {
	ctx := context.Background()
	namespace := "single-tenant-test"
	liveClient := startOpenBaoClusterManager(t, namespace, true)

	cluster := newManagerTestCluster(namespace, "single-tenant-cluster")
	require.NoError(t, liveClient.Create(ctx, cluster))
	t.Cleanup(func() {
		_ = liveClient.Delete(context.Background(), cluster)
	})

	waitForClusterFinalizer(t, ctx, liveClient, cluster)
	waitForManagedConfigMap(t, ctx, liveClient, namespace, cluster.Name)
}

func TestSetupWithManager_ReconcilesClusterInMultiTenantMode(t *testing.T) {
	ctx := context.Background()
	namespace := "multi-tenant-test"
	liveClient := startOpenBaoClusterManager(t, namespace, false)

	cluster := newManagerTestCluster(namespace, "multi-tenant-cluster")
	require.NoError(t, liveClient.Create(ctx, cluster))
	t.Cleanup(func() {
		_ = liveClient.Delete(context.Background(), cluster)
	})

	waitForClusterFinalizer(t, ctx, liveClient, cluster)
	waitForManagedConfigMap(t, ctx, liveClient, namespace, cluster.Name)
}

func TestSetupWithManager_SingleTenantRecreatesDeletedConfigMap(t *testing.T) {
	ctx := context.Background()
	namespace := "single-tenant-watch-test"
	liveClient := startOpenBaoClusterManager(t, namespace, true)

	cluster := newManagerTestCluster(namespace, "watch-recreate-cluster")
	require.NoError(t, liveClient.Create(ctx, cluster))
	t.Cleanup(func() {
		_ = liveClient.Delete(context.Background(), cluster)
	})

	configMap := waitForManagedConfigMap(t, ctx, liveClient, namespace, cluster.Name)
	originalUID := configMap.UID

	require.NoError(t, liveClient.Delete(ctx, configMap))
	waitForObjectDeletion(t, ctx, liveClient, configMap)

	configMapKey := client.ObjectKey{Namespace: namespace, Name: cluster.Name + constants.SuffixConfigMap}
	require.Eventually(t, func() bool {
		current := &corev1.ConfigMap{}
		if err := liveClient.Get(ctx, configMapKey, current); err != nil {
			return false
		}
		return current.UID != originalUID
	}, 20*time.Second, 200*time.Millisecond, "expected single-tenant Owns() watch to recreate deleted ConfigMap")
}

func TestSetupWithManager_SingleTenantCompletesTerminalPodDeletion(t *testing.T) {
	originalStandard, originalShort := constants.RequeueStandard, constants.RequeueShort
	constants.RequeueStandard, constants.RequeueShort = 200*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() {
		constants.RequeueStandard, constants.RequeueShort = originalStandard, originalShort
	})

	for _, scenario := range []string{"terminal-status", "finalizer-removal"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			namespace := "cleanup-" + scenario
			c := startOpenBaoClusterManager(t, namespace, true)
			cluster := newManagerTestCluster(namespace, "cleanup")
			cluster.Spec.ReconcilePolicies = false
			require.NoError(t, c.Create(ctx, cluster))
			waitForClusterFinalizer(t, ctx, c, cluster)
			waitForManagedConfigMap(t, ctx, c, namespace, cluster.Name)

			sts := &appsv1.StatefulSet{}
			require.Eventually(t, func() bool {
				return c.Get(ctx, client.ObjectKeyFromObject(cluster), sts) == nil
			}, 10*time.Second, 100*time.Millisecond)
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: sts.Name + "-0", Namespace: namespace, Labels: sts.Spec.Template.Labels,
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet")),
					},
				},
				Spec: corev1.PodSpec{NodeName: "test-node", Containers: []corev1.Container{
					{Name: "openbao", Image: "example.com/openbao:test"},
				}},
			}
			if scenario == "finalizer-removal" {
				pod.Finalizers = []string{"test.openbao.org/hold"}
			}
			require.NoError(t, c.Create(ctx, pod))
			pod.Status.Phase = corev1.PodRunning
			require.NoError(t, c.Status().Update(ctx, pod))
			require.NoError(t, c.Delete(ctx, pod, client.GracePeriodSeconds(600)))
			key, uid := client.ObjectKeyFromObject(pod), pod.UID
			assertRetained := func() {
				t.Helper()
				require.Never(t, func() bool {
					current := &corev1.Pod{}
					return apierrors.IsNotFound(c.Get(ctx, key, current))
				}, time.Second, 50*time.Millisecond)
				require.NoError(t, c.Get(ctx, key, pod))
				require.Equal(t, uid, pod.UID)
				require.NotNil(t, pod.DeletionTimestamp)
				require.NotNil(t, pod.DeletionGracePeriodSeconds)
				require.EqualValues(t, 600, *pod.DeletionGracePeriodSeconds)
			}
			// Drain initial child events and retries while deletion is ineligible.
			// The only later writes affect the Pod, which has no workload watch.
			assertRetained()
			pod.Status.Phase = corev1.PodFailed
			require.NoError(t, c.Status().Update(ctx, pod))
			if scenario == "finalizer-removal" {
				assertRetained()
				require.Equal(t, []string{"test.openbao.org/hold"}, pod.Finalizers)
				pod.Finalizers = nil
				require.NoError(t, c.Update(ctx, pod))
			}
			require.Eventually(t, func() bool {
				return apierrors.IsNotFound(c.Get(ctx, key, &corev1.Pod{}))
			}, 5*time.Second, 100*time.Millisecond, "periodic workload refresh must complete Pod deletion")
		})
	}
}

func TestSetupWithManager_MultiTenantRecreatesDeletedChildrenOnPoll(t *testing.T) {
	ctx := context.Background()
	namespace := "multi-tenant-poll-test"
	originalRequeueStandard := constants.RequeueStandard
	constants.RequeueStandard = 250 * time.Millisecond
	t.Cleanup(func() {
		constants.RequeueStandard = originalRequeueStandard
	})
	liveClient := startOpenBaoClusterManager(t, namespace, false)

	cluster := newManagerTestCluster(namespace, "poll-recreate-cluster")
	require.NoError(t, liveClient.Create(ctx, cluster))
	t.Cleanup(func() {
		_ = liveClient.Delete(context.Background(), cluster)
	})

	configMap := waitForManagedConfigMap(t, ctx, liveClient, namespace, cluster.Name)
	originalUID := configMap.UID

	require.NoError(t, liveClient.Delete(ctx, configMap))
	waitForObjectDeletion(t, ctx, liveClient, configMap)

	configMapKey := client.ObjectKey{Namespace: namespace, Name: cluster.Name + constants.SuffixConfigMap}
	require.Eventually(t, func() bool {
		current := &corev1.ConfigMap{}
		if err := liveClient.Get(ctx, configMapKey, current); err != nil {
			return false
		}
		return current.UID != originalUID
	}, 20*time.Second, 200*time.Millisecond, "expected multi-tenant polling to recreate deleted ConfigMap")

	statefulSetKey := client.ObjectKey{Namespace: namespace, Name: cluster.Name}
	statefulSet := &appsv1.StatefulSet{}
	require.Eventually(t, func() bool {
		return liveClient.Get(ctx, statefulSetKey, statefulSet) == nil
	}, 20*time.Second, 200*time.Millisecond, "expected manager-driven reconcile to create the managed StatefulSet")
	originalStatefulSetUID := statefulSet.UID

	require.NoError(t, liveClient.Delete(ctx, statefulSet))
	waitForObjectDeletion(t, ctx, liveClient, statefulSet)
	require.Eventually(t, func() bool {
		current := &appsv1.StatefulSet{}
		if err := liveClient.Get(ctx, statefulSetKey, current); err != nil {
			return false
		}
		return current.UID != originalStatefulSetUID
	}, 20*time.Second, 200*time.Millisecond, "expected multi-tenant polling to recreate deleted StatefulSet")
}

func TestSetupWithManager_RemovesReconcileMetricsAfterDeletion(t *testing.T) {
	ctx := t.Context()
	const namespace = "metric-cleanup-test"
	controllers := []string{"openbaocluster-workload", "openbaocluster-adminops", "openbaocluster-status"}
	liveClient := startOpenBaoClusterManager(t, namespace, true)
	cluster := newManagerTestCluster(namespace, "metric-cleanup-cluster")
	cluster.Spec.Paused = true
	require.NoError(t, liveClient.Create(ctx, cluster))
	key := client.ObjectKeyFromObject(cluster)
	waitForClusterFinalizer(t, ctx, liveClient, cluster)
	for _, controller := range controllers {
		require.Eventually(t, func() bool {
			return reconcileMetricCounts(t, key, controller)["duration"] > 0
		}, 10*time.Second, 100*time.Millisecond, "expected initial reconcile for %s", controller)
		observability.NewReconcileMetrics(namespace, cluster.Name, controller).IncrementError("BeforeDeletion")
	}

	require.NoError(t, liveClient.Delete(ctx, cluster))
	waitForObjectDeletion(t, ctx, liveClient, cluster)
	for _, controller := range controllers {
		require.Eventually(t, func() bool {
			return len(reconcileMetricCounts(t, key, controller)) == 0
		}, 10*time.Second, 100*time.Millisecond, "expected deletion watch to clear %s", controller)
	}

	recreated := newManagerTestCluster(namespace, cluster.Name)
	recreated.Spec.Paused = true
	require.NoError(t, liveClient.Create(ctx, recreated))
	require.NotEqual(t, cluster.UID, recreated.UID)
	t.Cleanup(func() { _ = liveClient.Delete(context.Background(), recreated) })
	waitForClusterFinalizer(t, ctx, liveClient, recreated)
	for _, controller := range controllers {
		require.Eventually(t, func() bool {
			return reconcileMetricCounts(t, key, controller)["duration"] > 0
		}, 10*time.Second, 100*time.Millisecond, "expected metrics for recreated owner in %s", controller)
		require.NotContains(t, reconcileMetricCounts(t, key, controller), "BeforeDeletion")
	}
}

func reconcileMetricCounts(t *testing.T, key client.ObjectKey, controller string) map[string]float64 {
	t.Helper()
	families, err := controllermetrics.Registry.Gather()
	require.NoError(t, err)
	counts := make(map[string]float64)
	for _, family := range families {
		if family.GetName() != "openbao_reconcile_duration_seconds" && family.GetName() != "openbao_reconcile_errors_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string)
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["namespace"] != key.Namespace || labels["name"] != key.Name || labels["controller"] != controller {
				continue
			}
			if family.GetName() == "openbao_reconcile_duration_seconds" {
				counts["duration"] = float64(metric.GetHistogram().GetSampleCount())
			} else {
				counts[labels["reason"]] = metric.GetCounter().GetValue()
			}
		}
	}
	return counts
}

func startOpenBaoClusterManager(t *testing.T, namespace string, singleTenant bool) client.Client {
	t.Helper()
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")

	scheme := newIntegrationScheme(t)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
			filepath.Join("..", "..", "..", "test", "manifests", "gateway-api", "v1.5.1", "crds"),
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

	testNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}
	require.NoError(t, liveClient.Create(context.Background(), testNamespace))
	t.Cleanup(func() {
		_ = liveClient.Delete(context.Background(), testNamespace)
	})
	if !singleTenant {
		require.NoError(t, ensureProvisionedTenantNamespace(context.Background(), liveClient, namespace))
	}

	mgrOptions := ctrl.Options{
		Scheme:                 scheme,
		Logger:                 testr.New(t),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	}
	skipNameValidation := true
	mgrOptions.Controller.SkipNameValidation = &skipNameValidation
	if singleTenant {
		mgrOptions.Cache = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {},
			},
		}
	} else {
		// Match cmd/controller.newManagerOptions: multi-tenant child reads bypass
		// the shared cache. Cached reads after Apply can otherwise return stale
		// NotFound errors and accumulate failure backoff during initial setup.
		mgrOptions.Client.Cache = &client.CacheOptions{
			DisableFor: []client.Object{
				&corev1.Secret{},
				&batchv1.Job{},
				&appsv1.StatefulSet{},
				&corev1.Service{},
				&corev1.ConfigMap{},
				&corev1.Namespace{},
				&policyv1.PodDisruptionBudget{},
				&networkingv1.Ingress{},
				&networkingv1.NetworkPolicy{},
				&rbacv1.Role{},
				&rbacv1.RoleBinding{},
				&corev1.ServiceAccount{},
				&corev1.Pod{},
				&corev1.PersistentVolumeClaim{},
				&discoveryv1.EndpointSlice{},
				&gatewayv1.HTTPRoute{},
				&gatewayv1.TLSRoute{},
				&gatewayv1.BackendTLSPolicy{},
			},
		}
	}

	mgr, err := ctrl.NewManager(cfg, mgrOptions)
	require.NoError(t, err)

	imageVerifier := security.NewImageVerifier(logr.Discard(), mgr.GetClient(), nil)
	reconciler := &openbaocluster.OpenBaoClusterReconciler{
		Client: mgr.GetClient(),
		ControllerRuntime: openbaocluster.ControllerRuntime{
			APIReader:        mgr.GetAPIReader(),
			Recorder:         mgr.GetEventRecorder("openbaocluster-test"),
			SingleTenantMode: singleTenant,
		},
		Applications: appopenbaocluster.NewRuntimeApplications(appopenbaocluster.RuntimeApplicationsConfig{
			Kubernetes: appopenbaocluster.RuntimeKubernetesConfig{
				Client:            mgr.GetClient(),
				APIReader:         mgr.GetAPIReader(),
				Scheme:            mgr.GetScheme(),
				RestConfig:        cfg,
				OperatorNamespace: "openbao-operator-system",
				Recorder:          mgr.GetEventRecorder("openbaocluster-test"),
			},
			ImageVerification: appopenbaocluster.RuntimeImageVerificationConfig{
				ImageVerifier:         imageVerifier,
				OperatorImageVerifier: imageVerifier,
				Infra: appopenbaocluster.InfraImageVerificationRuntime{
					OperatorImageVerifier:              imageVerifier,
					VerifyOperatorImage:                portsecurity.VerifyOperatorImageForCluster,
					IsMainImageVerificationEnabled:     portsecurity.IsMainImageVerificationEnabled,
					IsOperatorImageVerificationEnabled: portsecurity.IsOperatorImageVerificationEnabled,
				},
			},
		}),
	}
	require.NoError(t, reconciler.SetupWithManager(mgr))

	managerCtx, cancel := context.WithCancel(context.Background())
	managerErr := make(chan error, 1)
	go func() {
		managerErr <- mgr.Start(managerCtx)
	}()
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

func ensureProvisionedTenantNamespace(ctx context.Context, c client.Client, namespace string) error {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.TenantRoleBindingName,
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     constants.TenantRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "openbao-operator-controller",
				Namespace: "openbao-operator-system",
			},
		},
	}
	err := c.Create(ctx, roleBinding)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func newIntegrationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, gatewayv1.Install(scheme))
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

func newManagerTestCluster(namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  "2.4.4",
			Image:    "openbao/openbao:2.4.4",
			Replicas: 1,
			Storage: openbaov1alpha1.StorageConfig{
				Size: "10Gi",
			},
			TLS: openbaov1alpha1.TLSConfig{
				Enabled:        true,
				RotationPeriod: "720h",
			},
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			},
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Image: "openbao/openbao-init:latest",
			},
		},
	}
}

func waitForClusterFinalizer(t *testing.T, ctx context.Context, c client.Client, cluster *openbaov1alpha1.OpenBaoCluster) {
	t.Helper()

	clusterKey := client.ObjectKeyFromObject(cluster)
	require.Eventually(t, func() bool {
		current := &openbaov1alpha1.OpenBaoCluster{}
		if err := c.Get(ctx, clusterKey, current); err != nil {
			return false
		}
		return slices.Contains(current.Finalizers, openbaov1alpha1.OpenBaoClusterFinalizer)
	}, 20*time.Second, 200*time.Millisecond, "expected manager-driven reconcile to add the finalizer")
}

func waitForManagedConfigMap(t *testing.T, ctx context.Context, c client.Client, namespace, clusterName string) *corev1.ConfigMap {
	t.Helper()

	configMapKey := client.ObjectKey{
		Namespace: namespace,
		Name:      clusterName + constants.SuffixConfigMap,
	}
	current := &corev1.ConfigMap{}
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, configMapKey, current); err != nil {
			return false
		}
		_, hasConfig := current.Data["config.hcl"]
		return hasConfig
	}, 20*time.Second, 200*time.Millisecond, "expected manager-driven reconcile to create the managed ConfigMap")

	result := current.DeepCopy()
	require.NoError(t, c.Get(ctx, configMapKey, result))
	return result
}

func waitForObjectDeletion(t *testing.T, ctx context.Context, c client.Client, obj client.Object) {
	t.Helper()

	key := client.ObjectKeyFromObject(obj)
	deletedUID := obj.GetUID()
	require.NotEmpty(t, deletedUID, "expected the deleted object's UID")
	require.Eventually(t, func() bool {
		current := obj.DeepCopyObject().(client.Object)
		err := c.Get(ctx, key, current)
		// A watch can recreate the child before the first read observes NotFound.
		return apierrors.IsNotFound(err) || (err == nil && current.GetUID() != deletedUID)
	}, 10*time.Second, 200*time.Millisecond, "expected object %s/%s with UID %s to be deleted", key.Namespace, key.Name, deletedUID)
}
