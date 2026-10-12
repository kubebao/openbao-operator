package perf

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	perfRunIDLabel        = "perf.openbao.org/run-id"
	perfScenarioLabel     = "perf.openbao.org/scenario"
	perfNamespacePrefix   = "perf"
	reconcileTriggerKey   = "perf.openbao.org/reconcile-trigger"
	nativePollInterval    = 2 * time.Second
	operatorJWTAuthRole   = "e2e-test"
	operatorJWTAuthPolicy = "e2e-test"

	metricClusterAvailableSeconds     = "cluster_available_seconds"
	metricStatefulSetCreatedSeconds   = "statefulset_created_seconds"
	metricFirstPodReadySeconds        = "first_pod_ready_seconds"
	metricAllPodsReadySeconds         = "all_pods_ready_seconds"
	metricObservedKubernetesWrites    = "observed_kubernetes_writes"
	metricKubernetesWrites            = "kubernetes_writes"
	metricUpgradeTotalSeconds         = "upgrade_total_seconds"
	metricUpgradeSessionStartSeconds  = "upgrade_session_start_seconds"
	metricUpgradePodReadySeconds      = "upgrade_pod_ready_seconds"
	metricUpgradeAvailabilityFailures = "upgrade_availability_probe_failures"
	metricUpgradeKubernetesWrites     = "upgrade_kubernetes_writes"
	metricTenantChurnCompleteSeconds  = "tenant_churn_complete_seconds"
	metricTenantReadyP50Seconds       = "tenant_ready_p50_seconds"
	metricTenantReadyP95Seconds       = "tenant_ready_p95_seconds"
	metricTenantKubernetesWrites      = "tenant_kubernetes_writes"
	metricTenantCount                 = "tenant_count"
)

// Config contains the harness values required to execute native performance scenarios.
type Config struct {
	RunID                  string
	ArtifactDir            string
	ExistingClusterContext string
	Namespace              string
	NamespacePrefix        string
	OperatorNS             string
	OpenBaoVersion         string
	OpenBaoImage           string
	UpgradeFromVersion     string
	UpgradeFromImage       string
	UpgradeToVersion       string
	UpgradeToImage         string
	BackupExecutorImage    string
	UpgradeExecutorImage   string
	ConfigInitImage        string
	APIServerCIDR          string
	StorageClass           string
	TenantChurnCount       int
}

// Scenario identifies a native performance scenario.
type Scenario struct {
	Name string
}

// Phase is a timestamped scenario milestone emitted by native scenarios.
type Phase struct {
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
	Source string    `json:"source"`
}

// Result is the native scenario execution result consumed by the perfcheck harness.
type Result struct {
	Phases       []Phase
	Measurements map[string]float64
	Namespace    string
	Artifacts    map[string]string
	Cleanup      func(context.Context)
}

type nativeScenarioContext struct {
	opts              Config
	scenario          Scenario
	cluster           string
	runID             string
	namespace         string
	createdNS         bool
	createdNamespaces []string
	cfg               *rest.Config
	client            client.Client
	artifacts         map[string]string
}

type resourceWriteTracker struct {
	seen  map[string]string
	count int
}

func RunNativeScenario(
	ctx context.Context,
	opts Config,
	cluster string,
	scenario Scenario,
) (Result, error) {
	native, err := newNativeScenarioContext(opts, cluster, scenario)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Namespace: native.namespace,
		Cleanup:   native.cleanup,
	}
	if err := native.ensureNamespace(ctx); err != nil {
		return result, err
	}
	if err := native.ensureTenant(ctx); err != nil {
		return result, err
	}

	var runResult Result
	switch scenario.Name {
	case "lifecycle-convergence":
		runResult, err = native.runLifecycleConvergence(ctx)
	case "tenant-churn":
		runResult, err = native.runTenantChurn(ctx)
	case "backup":
		runResult, err = native.runBackup(ctx)
	case "restore":
		runResult, err = native.runRestore(ctx)
	case "rolling-upgrade":
		runResult, err = native.runRollingUpgrade(ctx)
	default:
		return result, fmt.Errorf("native scenario %q is not implemented", scenario.Name)
	}
	if runResult.Cleanup == nil {
		runResult.Cleanup = native.cleanup
	}
	if runResult.Namespace == "" {
		runResult.Namespace = native.namespace
	}
	return runResult, err
}

func newNativeScenarioContext(
	opts Config,
	cluster string,
	scenario Scenario,
) (*nativeScenarioContext, error) {
	cfg, c, err := nativeKubernetesClient(opts, cluster)
	if err != nil {
		return nil, err
	}
	runID := nativeRunID(opts, cluster, scenario.Name)
	namespace := strings.TrimSpace(opts.Namespace)
	createdNS := false
	if namespace == "" {
		prefix := strings.TrimSpace(opts.NamespacePrefix)
		if prefix == "" {
			prefix = perfNamespacePrefix
		}
		namespace = boundedDNSLabel(fmt.Sprintf("%s-%s", prefix, runID))
		createdNS = true
	}
	return &nativeScenarioContext{
		opts:      opts,
		scenario:  scenario,
		cluster:   cluster,
		runID:     runID,
		namespace: namespace,
		createdNS: createdNS,
		cfg:       cfg,
		client:    c,
	}, nil
}

func nativeKubernetesClient(opts Config, cluster string) (*rest.Config, client.Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.ExistingClusterContext == "" {
		loadingRules.ExplicitPath = nativeKubeconfigPath(opts, cluster)
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext(opts, cluster)}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load kubeconfig context %q: %w", kubeContext(opts, cluster), err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("add Kubernetes scheme: %w", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("add OpenBao scheme: %w", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("add apps scheme: %w", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("add batch scheme: %w", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("add networking scheme: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return cfg, c, nil
}

func kubeContext(opts Config, cluster string) string {
	if opts.ExistingClusterContext != "" {
		return opts.ExistingClusterContext
	}
	return fmt.Sprintf("kind-%s", cluster)
}

func nativeKubeconfigPath(opts Config, cluster string) string {
	return filepath.Join(opts.ArtifactDir, "kubeconfigs", cluster+".yaml")
}

func (n *nativeScenarioContext) ensureNamespace(ctx context.Context) error {
	created, err := n.ensureLabeledNamespace(ctx, n.namespace)
	if err != nil {
		return err
	}
	if created {
		n.createdNS = true
	}
	return nil
}

func (n *nativeScenarioContext) ensureLabeledNamespace(ctx context.Context, name string) (bool, error) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: n.namespaceLabels(),
		},
	}
	err := n.client.Create(ctx, ns)
	if err == nil {
		n.createdNamespaces = appendUniqueString(n.createdNamespaces, name)
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("create namespace %q: %w", name, err)
	}
	current := &corev1.Namespace{}
	if getErr := n.client.Get(ctx, types.NamespacedName{Name: name}, current); getErr != nil {
		return false, fmt.Errorf("get existing namespace %q: %w", name, getErr)
	}
	original := current.DeepCopy()
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range n.namespaceLabels() {
		current.Labels[key] = value
	}
	if patchErr := n.client.Patch(ctx, current, client.MergeFrom(original)); patchErr != nil {
		return false, fmt.Errorf("label namespace %q: %w", name, patchErr)
	}
	return false, nil
}

func (n *nativeScenarioContext) ensureTenant(ctx context.Context) error {
	tenantKey, err := n.createTenant(ctx, n.namespace, n.namespace)
	if err != nil {
		return err
	}
	return pollUntil(ctx, func() (bool, error) {
		provisioned, _, err := n.getTenantProvisioned(ctx, tenantKey)
		return provisioned, err
	})
}

func (n *nativeScenarioContext) createTenant(
	ctx context.Context,
	name string,
	targetNamespace string,
) (types.NamespacedName, error) {
	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: n.opts.OperatorNS,
			Labels:    n.resourceLabels(),
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: targetNamespace,
		},
	}
	if err := n.client.Create(ctx, tenant); err != nil && !apierrors.IsAlreadyExists(err) {
		return types.NamespacedName{}, fmt.Errorf(
			"create OpenBaoTenant %s/%s: %w",
			n.opts.OperatorNS,
			name,
			err,
		)
	}
	return types.NamespacedName{Namespace: n.opts.OperatorNS, Name: name}, nil
}

func (n *nativeScenarioContext) getTenantProvisioned(
	ctx context.Context,
	key types.NamespacedName,
) (bool, *openbaov1alpha1.OpenBaoTenant, error) {
	current := &openbaov1alpha1.OpenBaoTenant{}
	if err := n.client.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("get OpenBaoTenant %s/%s: %w", key.Namespace, key.Name, err)
	}
	if current.Status.LastError != "" {
		return false, current, fmt.Errorf(
			"OpenBaoTenant %s/%s reported LastError: %s",
			key.Namespace,
			key.Name,
			current.Status.LastError,
		)
	}
	return current.Status.Provisioned, current, nil
}

func (n *nativeScenarioContext) runLifecycleConvergence(ctx context.Context) (Result, error) {
	cluster := n.buildCluster(
		n.resourceName("perf-life"),
		n.opts.OpenBaoVersion,
		n.opts.OpenBaoImage,
		1,
	)
	tracker := newResourceWriteTracker()
	resourceCreatedAt := time.Now().UTC()
	if err := n.client.Create(ctx, cluster); err != nil {
		return Result{}, fmt.Errorf("create OpenBaoCluster: %w", err)
	}
	if !cluster.CreationTimestamp.IsZero() {
		resourceCreatedAt = cluster.CreationTimestamp.Time
	}

	phases := []Phase{{Name: "resource_created", At: resourceCreatedAt, Source: "harness"}}
	phaseTimes := map[string]time.Time{"resource_created": resourceCreatedAt}
	var firstPodReadyAt time.Time
	var allPodsReadyAt time.Time

	err := pollUntil(ctx, func() (bool, error) {
		if err := tracker.observe(ctx, n.client, n.namespace, cluster.Name); err != nil {
			return false, err
		}
		current := &openbaov1alpha1.OpenBaoCluster{}
		if err := n.client.Get(ctx, client.ObjectKeyFromObject(cluster), current); err != nil {
			return false, fmt.Errorf("get OpenBaoCluster: %w", err)
		}
		sts := &appsv1.StatefulSet{}
		if err := n.client.Get(ctx, client.ObjectKeyFromObject(cluster), sts); err == nil {
			recordPhaseOnce(&phases, phaseTimes, "statefulset_created", sts.CreationTimestamp.Time, "statefulset")
		} else if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("get StatefulSet: %w", err)
		}

		pods, err := n.clusterPods(ctx, cluster.Name)
		if err != nil {
			return false, err
		}
		if firstPodReadyAt.IsZero() {
			firstPodReadyAt = firstReadyPodTime(pods)
			if !firstPodReadyAt.IsZero() {
				recordPhaseOnce(&phases, phaseTimes, "first_pod_ready", firstPodReadyAt, "pod_condition")
			}
		}
		if allPodsReadyAt.IsZero() && len(pods) >= int(cluster.Spec.Replicas) {
			allPodsReadyAt = allPodsReadyTime(pods, int(cluster.Spec.Replicas))
			if !allPodsReadyAt.IsZero() {
				recordPhaseOnce(&phases, phaseTimes, "all_pods_ready", allPodsReadyAt, "pod_condition")
			}
		}
		available := meta.FindStatusCondition(current.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
		if available == nil || available.Status != metav1.ConditionTrue {
			return false, nil
		}
		availableAt := conditionTransitionTime(available)
		recordPhaseOnce(&phases, phaseTimes, "cluster_available", availableAt, "openbaocluster_status")
		return true, nil
	})
	if err != nil {
		measurements := lifecycleMeasurements(phaseTimes, tracker.count)
		return n.result(phases, measurements), err
	}

	measurements := lifecycleMeasurements(phaseTimes, tracker.count)
	return n.result(phases, measurements), nil
}

func (n *nativeScenarioContext) runRollingUpgrade(ctx context.Context) (Result, error) {
	if n.opts.UpgradeFromVersion == n.opts.UpgradeToVersion {
		return Result{}, fmt.Errorf(
			"upgrade source and target versions are both %q",
			n.opts.UpgradeFromVersion,
		)
	}
	cluster := n.buildRollingUpgradeCluster()
	tracker := newResourceWriteTracker()
	if err := n.client.Create(ctx, cluster); err != nil {
		return Result{}, fmt.Errorf("create OpenBaoCluster: %w", err)
	}
	if err := n.waitForAvailable(ctx, cluster.Name, int(cluster.Spec.Replicas), tracker); err != nil {
		return Result{}, err
	}

	requestedAt := time.Now().UTC()
	phases := []Phase{{Name: "upgrade_requested", At: requestedAt, Source: "harness"}}
	phaseTimes := map[string]time.Time{"upgrade_requested": requestedAt}
	probeLog, err := n.createAvailabilityProbeLog()
	if err != nil {
		return n.result(phases, nil), err
	}
	defer func() { _ = probeLog.Close() }()
	if err := n.patchUpgradeTarget(ctx, cluster.Name); err != nil {
		measurements := rollingUpgradeMeasurements(phaseTimes, requestedAt, nil, 0, tracker.count)
		return n.result(phases, measurements), err
	}

	var (
		availabilityFailures int
		seenUpgrade          bool
		podReadyTimes        []time.Time
		targetReadyPods      = map[string]struct{}{}
	)
	err = pollUntil(ctx, func() (bool, error) {
		if err := tracker.observe(ctx, n.client, n.namespace, cluster.Name); err != nil {
			return false, err
		}
		current := &openbaov1alpha1.OpenBaoCluster{}
		if err := n.client.Get(ctx, client.ObjectKeyFromObject(cluster), current); err != nil {
			return false, fmt.Errorf("get OpenBaoCluster: %w", err)
		}
		if current.Status.Upgrade != nil {
			seenUpgrade = true
			startedAt := time.Now().UTC()
			if current.Status.Upgrade.StartedAt != nil {
				startedAt = current.Status.Upgrade.StartedAt.Time
			}
			recordPhaseOnce(&phases, phaseTimes, "upgrade_session_started", startedAt, "openbaocluster_status")
		}
		probe := n.probeClusterAvailability(ctx, cluster.Name)
		if !probe.Available {
			availabilityFailures++
			n.observeFailedAvailabilityProbe(ctx, cluster.Name, &probe)
		}
		if err := json.NewEncoder(probeLog).Encode(probe); err != nil {
			return false, fmt.Errorf("write availability probe artifact: %w", err)
		}

		pods, err := n.clusterPods(ctx, cluster.Name)
		if err != nil {
			return false, err
		}
		for i := range pods {
			pod := &pods[i]
			if pod.Labels[portopenbao.LabelVersion] != n.opts.UpgradeToVersion {
				continue
			}
			if _, exists := targetReadyPods[pod.Name]; exists {
				continue
			}
			readyAt := podReadyTransitionTime(pod)
			if readyAt.IsZero() {
				continue
			}
			targetReadyPods[pod.Name] = struct{}{}
			podReadyTimes = append(podReadyTimes, readyAt)
			recordPhaseOnce(
				&phases,
				phaseTimes,
				fmt.Sprintf("pod_%s_ready", pod.Name),
				readyAt,
				"pod_condition",
			)
		}
		if !seenUpgrade || current.Status.Upgrade != nil {
			return false, nil
		}
		available := meta.FindStatusCondition(current.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
		if current.Status.CurrentVersion != n.opts.UpgradeToVersion ||
			available == nil ||
			available.Status != metav1.ConditionTrue {
			return false, nil
		}
		recordPhaseOnce(&phases, phaseTimes, "upgrade_completed", time.Now().UTC(), "openbaocluster_status")
		return true, nil
	})
	if err != nil {
		measurements := rollingUpgradeMeasurements(
			phaseTimes,
			requestedAt,
			podReadyTimes,
			availabilityFailures,
			tracker.count,
		)
		return n.result(phases, measurements), err
	}

	measurements := rollingUpgradeMeasurements(phaseTimes, requestedAt, podReadyTimes, availabilityFailures, tracker.count)
	return n.result(phases, measurements), nil
}

type tenantChurnTarget struct {
	namespace string
	key       types.NamespacedName
	index     int
}

func (n *nativeScenarioContext) runTenantChurn(ctx context.Context) (Result, error) {
	tracker := newResourceWriteTracker()
	startedAt := time.Now().UTC()
	phases := []Phase{{Name: "tenant_churn_started", At: startedAt, Source: "harness"}}
	phaseTimes := map[string]time.Time{"tenant_churn_started": startedAt}
	targets := make([]tenantChurnTarget, 0, n.opts.TenantChurnCount)

	for i := 0; i < n.opts.TenantChurnCount; i++ {
		namespace := n.tenantChurnNamespaceName(i)
		if _, err := n.ensureLabeledNamespace(ctx, namespace); err != nil {
			return n.result(phases, tenantChurnMeasurements(phaseTimes, startedAt, nil, tracker.count, len(targets))), err
		}
		key, err := n.createTenant(ctx, namespace, namespace)
		if err != nil {
			return n.result(phases, tenantChurnMeasurements(phaseTimes, startedAt, nil, tracker.count, len(targets))), err
		}
		targets = append(targets, tenantChurnTarget{namespace: namespace, key: key, index: i})
	}
	recordPhaseOnce(&phases, phaseTimes, "tenant_churn_created", time.Now().UTC(), "harness")

	readyTimes := make([]time.Time, 0, len(targets))
	readyByTenant := make(map[string]time.Time, len(targets))
	err := pollUntil(ctx, func() (bool, error) {
		ready := 0
		for _, target := range targets {
			provisioned, tenant, err := n.getTenantProvisioned(ctx, target.key)
			if err != nil {
				return false, err
			}
			if tenant != nil {
				tracker.track("OpenBaoTenant", tenant)
			}
			ns := &corev1.Namespace{}
			if err := n.client.Get(ctx, types.NamespacedName{Name: target.namespace}, ns); err == nil {
				tracker.track("Namespace", ns)
			} else if !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("get tenant namespace %q: %w", target.namespace, err)
			}
			if !provisioned {
				continue
			}
			ready++
			if _, exists := readyByTenant[target.namespace]; exists {
				continue
			}
			readyAt := tenantProvisionedTransitionTime(tenant)
			readyByTenant[target.namespace] = readyAt
			readyTimes = append(readyTimes, readyAt)
			recordPhaseOnce(
				&phases,
				phaseTimes,
				fmt.Sprintf("tenant_%02d_provisioned", target.index+1),
				readyAt,
				"openbaotenant_status",
			)
		}
		if ready == len(targets) {
			recordPhaseOnce(&phases, phaseTimes, "tenant_churn_complete", time.Now().UTC(), "harness")
			return true, nil
		}
		return false, nil
	})

	measurements := tenantChurnMeasurements(phaseTimes, startedAt, readyTimes, tracker.count, len(targets))
	return n.result(phases, measurements), err
}

func lifecycleMeasurements(phaseTimes map[string]time.Time, writes int) map[string]float64 {
	measurements := phaseMeasurements(phaseTimes, "resource_created", map[string]string{
		metricStatefulSetCreatedSeconds: "statefulset_created",
		metricFirstPodReadySeconds:      "first_pod_ready",
		metricAllPodsReadySeconds:       "all_pods_ready",
		metricClusterAvailableSeconds:   "cluster_available",
	})
	measurements[metricObservedKubernetesWrites] = float64(writes)
	measurements[metricKubernetesWrites] = float64(writes)
	return measurements
}

func rollingUpgradeMeasurements(
	phaseTimes map[string]time.Time,
	requestedAt time.Time,
	podReadyTimes []time.Time,
	availabilityFailures int,
	writes int,
) map[string]float64 {
	measurements := phaseMeasurements(phaseTimes, "upgrade_requested", map[string]string{
		metricUpgradeSessionStartSeconds: "upgrade_session_started",
		metricUpgradeTotalSeconds:        "upgrade_completed",
	})
	measurements[metricUpgradePodReadySeconds] = maxDurationSeconds(requestedAt, podReadyTimes)
	measurements[metricUpgradeAvailabilityFailures] = float64(availabilityFailures)
	measurements[metricObservedKubernetesWrites] = float64(writes)
	measurements[metricKubernetesWrites] = float64(writes)
	measurements[metricUpgradeKubernetesWrites] = float64(writes)
	return measurements
}

func tenantChurnMeasurements(
	phaseTimes map[string]time.Time,
	startedAt time.Time,
	readyTimes []time.Time,
	writes int,
	tenantCount int,
) map[string]float64 {
	measurements := phaseMeasurements(phaseTimes, "tenant_churn_started", map[string]string{
		metricTenantChurnCompleteSeconds: "tenant_churn_complete",
	})
	measurements[metricTenantReadyP50Seconds] = durationPercentileSeconds(startedAt, readyTimes, 0.50)
	measurements[metricTenantReadyP95Seconds] = durationPercentileSeconds(startedAt, readyTimes, 0.95)
	measurements[metricObservedKubernetesWrites] = float64(writes)
	measurements[metricKubernetesWrites] = float64(writes)
	measurements[metricTenantKubernetesWrites] = float64(writes)
	measurements[metricTenantCount] = float64(tenantCount)
	return measurements
}

func (n *nativeScenarioContext) result(
	phases []Phase,
	measurements map[string]float64,
) Result {
	return Result{
		Phases:       phases,
		Measurements: measurements,
		Namespace:    n.namespace,
		Artifacts:    n.artifacts,
		Cleanup:      n.cleanup,
	}
}

func (n *nativeScenarioContext) buildCluster(
	name string,
	version string,
	image string,
	replicas int32,
) *openbaov1alpha1.OpenBaoCluster {
	storage := openbaov1alpha1.StorageConfig{Size: "1Gi"}
	if storageClass := n.nativeStorageClass(); storageClass != "" {
		storage.StorageClassName = &storageClass
	}
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: n.namespace,
			Labels:    n.resourceLabels(),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  version,
			Image:    image,
			Replicas: replicas,
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Enabled: true,
				Image:   n.opts.ConfigInitImage,
			},
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
				Requests: nativeSelfInitRequests(n.namespace),
			},
			TLS: openbaov1alpha1.TLSConfig{
				Enabled:        true,
				Mode:           openbaov1alpha1.TLSModeOperatorManaged,
				RotationPeriod: "720h",
			},
			Storage: storage,
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR: n.opts.APIServerCIDR,
			},
			Observability:  n.workloadObservability(version),
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
		},
	}
}

func (n *nativeScenarioContext) buildRollingUpgradeCluster() *openbaov1alpha1.OpenBaoCluster {
	cluster := n.buildCluster(
		n.resourceName("perf-up"),
		n.opts.UpgradeFromVersion,
		n.opts.UpgradeFromImage,
		3,
	)
	cluster.Spec.Service = &openbaov1alpha1.ServiceConfig{}
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{Image: n.opts.UpgradeExecutorImage}
	return cluster
}

func (n *nativeScenarioContext) workloadObservability(version string) *openbaov1alpha1.ObservabilityConfig {
	if n.opts.ExistingClusterContext != "" {
		return nil
	}
	metrics := &openbaov1alpha1.MetricsConfig{
		Enabled:       true,
		ScrapeProfile: "Active",
	}
	if metricsOnlyListenerSupported(version) {
		metrics.MetricsOnlyListener = &openbaov1alpha1.MetricsOnlyListenerConfig{
			Enabled:                      boolPtr(true),
			UnauthenticatedMetricsAccess: boolPtr(true),
		}
	}
	return &openbaov1alpha1.ObservabilityConfig{
		Metrics: metrics,
	}
}

func metricsOnlyListenerSupported(version string) bool {
	ok, err := platformsemver.AtLeast(version, 2, 5, 0)
	return err == nil && ok
}

func boolPtr(value bool) *bool {
	return &value
}

func (n *nativeScenarioContext) nativeStorageClass() string {
	return strings.TrimSpace(n.opts.StorageClass)
}

func (n *nativeScenarioContext) resourceName(prefix string) string {
	return nativeResourceName(prefix, n.runID)
}

func (n *nativeScenarioContext) tenantChurnNamespaceName(index int) string {
	return n.resourceName(fmt.Sprintf("perf-tenant-%02d", index+1))
}

func nativeSelfInitRequests(namespace string) []openbaov1alpha1.SelfInitRequest {
	return []openbaov1alpha1.SelfInitRequest{
		{
			Name:      "enable-kv-secrets",
			Operation: openbaov1alpha1.SelfInitOperationUpdate,
			Path:      "sys/mounts/secret",
			SecretEngine: &openbaov1alpha1.SelfInitSecretEngine{
				Type:        "kv",
				Description: "KV v2 secret engine",
				Options:     map[string]string{"version": "2"},
			},
		},
		{
			Name:      "create-perf-policy",
			Operation: openbaov1alpha1.SelfInitOperationUpdate,
			Path:      "sys/policies/acl/" + operatorJWTAuthPolicy,
			Policy: &openbaov1alpha1.SelfInitPolicy{
				Policy: `path "secret/*" { capabilities = ["create", "read", "update", "delete", "list"] }
path "secret/data/*" { capabilities = ["create", "read", "update", "delete", "list"] }
path "secret/metadata/*" { capabilities = ["read", "list", "delete"] }`,
			},
		},
		{
			Name:      "create-perf-role",
			Operation: openbaov1alpha1.SelfInitOperationUpdate,
			Path:      "auth/jwt-operator/role/" + operatorJWTAuthRole,
			Data: nativeJSON(map[string]any{
				"role_type":       "jwt",
				"user_claim":      "sub",
				"bound_audiences": []string{"openbao-internal"},
				"bound_subject":   fmt.Sprintf("system:serviceaccount:%s:default", namespace),
				"token_policies":  []string{operatorJWTAuthPolicy},
				"ttl":             "1h",
			}),
		},
	}
}

func nativeJSON(value map[string]any) *apiextensionsv1.JSON {
	data, err := json.Marshal(value)
	if err != nil {
		return &apiextensionsv1.JSON{Raw: []byte("{}")}
	}
	return &apiextensionsv1.JSON{Raw: data}
}

func (n *nativeScenarioContext) waitForAvailable(
	ctx context.Context,
	clusterName string,
	expectedPods int,
	tracker *resourceWriteTracker,
) error {
	return pollUntil(ctx, func() (bool, error) {
		if err := tracker.observe(ctx, n.client, n.namespace, clusterName); err != nil {
			return false, err
		}
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		if err := n.client.Get(ctx, client.ObjectKey{Namespace: n.namespace, Name: clusterName}, cluster); err != nil {
			return false, fmt.Errorf("get OpenBaoCluster: %w", err)
		}
		pods, err := n.clusterPods(ctx, clusterName)
		if err != nil {
			return false, err
		}
		if allPodsReadyTime(pods, expectedPods).IsZero() {
			return false, nil
		}
		available := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
		return available != nil && available.Status == metav1.ConditionTrue, nil
	})
}

func (n *nativeScenarioContext) patchUpgradeTarget(ctx context.Context, clusterName string) error {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := n.client.Get(ctx, client.ObjectKey{Namespace: n.namespace, Name: clusterName}, cluster); err != nil {
		return fmt.Errorf("get OpenBaoCluster for upgrade patch: %w", err)
	}
	original := cluster.DeepCopy()
	cluster.Spec.Version = n.opts.UpgradeToVersion
	cluster.Spec.Image = n.opts.UpgradeToImage
	if cluster.Annotations == nil {
		cluster.Annotations = map[string]string{}
	}
	cluster.Annotations[reconcileTriggerKey] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := n.client.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch OpenBaoCluster upgrade target: %w", err)
	}
	return nil
}

func (n *nativeScenarioContext) clusterPods(ctx context.Context, clusterName string) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := n.client.List(ctx, pods,
		client.InNamespace(n.namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: clusterName},
	); err != nil {
		return nil, fmt.Errorf("list pods for cluster %s/%s: %w", n.namespace, clusterName, err)
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].Name < pods.Items[j].Name
	})
	return pods.Items, nil
}

func (n *nativeScenarioContext) cleanup(ctx context.Context) {
	tenantList := &openbaov1alpha1.OpenBaoTenantList{}
	if err := n.client.List(ctx, tenantList,
		client.InNamespace(n.opts.OperatorNS),
		client.MatchingLabels{perfRunIDLabel: n.runID},
	); err == nil {
		for i := range tenantList.Items {
			_ = n.client.Delete(ctx, &tenantList.Items[i])
		}
	}
	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{Name: n.namespace, Namespace: n.opts.OperatorNS},
	}
	_ = n.client.Delete(ctx, tenant)
	restoreList := &openbaov1alpha1.OpenBaoRestoreList{}
	if err := n.client.List(ctx, restoreList,
		client.InNamespace(n.namespace),
		client.MatchingLabels{perfRunIDLabel: n.runID},
	); err == nil {
		for i := range restoreList.Items {
			_ = n.client.Delete(ctx, &restoreList.Items[i])
		}
	}
	secretList := &corev1.SecretList{}
	if err := n.client.List(ctx, secretList,
		client.InNamespace(n.namespace),
		client.MatchingLabels{perfRunIDLabel: n.runID},
	); err == nil {
		for i := range secretList.Items {
			_ = n.client.Delete(ctx, &secretList.Items[i])
		}
	}
	networkPolicyList := &networkingv1.NetworkPolicyList{}
	if err := n.client.List(ctx, networkPolicyList,
		client.InNamespace(n.namespace),
		client.MatchingLabels{perfRunIDLabel: n.runID},
	); err == nil {
		for i := range networkPolicyList.Items {
			_ = n.client.Delete(ctx, &networkPolicyList.Items[i])
		}
	}
	deletedNamespaces := make(map[string]struct{}, len(n.createdNamespaces)+1)
	for _, namespace := range n.createdNamespaces {
		if _, exists := deletedNamespaces[namespace]; exists {
			continue
		}
		deletedNamespaces[namespace] = struct{}{}
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		_ = n.client.Delete(ctx, ns)
	}
	if n.createdNS {
		if _, exists := deletedNamespaces[n.namespace]; !exists {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n.namespace}}
			_ = n.client.Delete(ctx, ns)
		}
		return
	}
	clusterList := &openbaov1alpha1.OpenBaoClusterList{}
	if err := n.client.List(ctx, clusterList,
		client.InNamespace(n.namespace),
		client.MatchingLabels{perfRunIDLabel: n.runID},
	); err == nil {
		for i := range clusterList.Items {
			_ = n.client.Delete(ctx, &clusterList.Items[i])
		}
	}
}

func (n *nativeScenarioContext) namespaceLabels() map[string]string {
	labels := n.resourceLabels()
	labels["pod-security.kubernetes.io/enforce"] = "restricted"
	return labels
}

func (n *nativeScenarioContext) resourceLabels() map[string]string {
	return map[string]string{
		perfRunIDLabel:    n.runID,
		perfScenarioLabel: n.scenario.Name,
	}
}

func newResourceWriteTracker() *resourceWriteTracker {
	return &resourceWriteTracker{seen: map[string]string{}}
}

func (t *resourceWriteTracker) observe(
	ctx context.Context,
	c client.Client,
	namespace string,
	clusterName string,
) error {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterName}, cluster); err == nil {
		t.track("OpenBaoCluster", cluster)
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get OpenBaoCluster for write tracking: %w", err)
	}
	stsList := &appsv1.StatefulSetList{}
	if err := c.List(ctx, stsList,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: clusterName},
	); err != nil {
		return fmt.Errorf("list StatefulSets for write tracking: %w", err)
	}
	for i := range stsList.Items {
		t.track("StatefulSet", &stsList.Items[i])
	}
	podList := &corev1.PodList{}
	if err := c.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: clusterName},
	); err != nil {
		return fmt.Errorf("list Pods for write tracking: %w", err)
	}
	for i := range podList.Items {
		t.track("Pod", &podList.Items[i])
	}
	jobList := &batchv1.JobList{}
	if err := c.List(ctx, jobList,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: clusterName},
	); err != nil {
		return fmt.Errorf("list Jobs for write tracking: %w", err)
	}
	for i := range jobList.Items {
		t.track("Job", &jobList.Items[i])
	}
	return nil
}

func (t *resourceWriteTracker) track(kind string, obj client.Object) {
	key := fmt.Sprintf("%s/%s/%s", kind, obj.GetNamespace(), obj.GetName())
	rv := obj.GetResourceVersion()
	if old, ok := t.seen[key]; ok && old == rv {
		return
	}
	t.seen[key] = rv
	t.count++
}

func recordPhaseOnce(
	phases *[]Phase,
	phaseTimes map[string]time.Time,
	name string,
	at time.Time,
	source string,
) {
	if _, exists := phaseTimes[name]; exists {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	phaseTimes[name] = at
	*phases = append(*phases, Phase{Name: name, At: at, Source: source})
}

func phaseMeasurements(
	phaseTimes map[string]time.Time,
	startPhase string,
	targets map[string]string,
) map[string]float64 {
	measurements := make(map[string]float64, len(targets))
	start := phaseTimes[startPhase]
	if start.IsZero() {
		return measurements
	}
	for measurement, phase := range targets {
		if at := phaseTimes[phase]; !at.IsZero() {
			seconds := at.Sub(start).Seconds()
			if seconds < 0 {
				seconds = 0
			}
			measurements[measurement] = seconds
		}
	}
	return measurements
}

func conditionTransitionTime(condition *metav1.Condition) time.Time {
	if condition == nil || condition.LastTransitionTime.IsZero() {
		return time.Now().UTC()
	}
	return condition.LastTransitionTime.Time
}

func tenantProvisionedTransitionTime(tenant *openbaov1alpha1.OpenBaoTenant) time.Time {
	if tenant == nil {
		return time.Now().UTC()
	}
	condition := meta.FindStatusCondition(tenant.Status.Conditions, "Provisioned")
	if condition == nil || condition.Status != metav1.ConditionTrue {
		return time.Now().UTC()
	}
	return conditionTransitionTime(condition)
}

func firstReadyPodTime(pods []corev1.Pod) time.Time {
	var first time.Time
	for i := range pods {
		readyAt := podReadyTransitionTime(&pods[i])
		if readyAt.IsZero() {
			continue
		}
		if first.IsZero() || readyAt.Before(first) {
			first = readyAt
		}
	}
	return first
}

func allPodsReadyTime(pods []corev1.Pod, expected int) time.Time {
	if expected <= 0 || len(pods) < expected {
		return time.Time{}
	}
	var latest time.Time
	ready := 0
	for i := range pods {
		readyAt := podReadyTransitionTime(&pods[i])
		if readyAt.IsZero() {
			continue
		}
		ready++
		if latest.IsZero() || readyAt.After(latest) {
			latest = readyAt
		}
	}
	if ready < expected {
		return time.Time{}
	}
	return latest
}

func podReadyTransitionTime(pod *corev1.Pod) time.Time {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			if condition.LastTransitionTime.IsZero() {
				return time.Now().UTC()
			}
			return condition.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

func maxDurationSeconds(start time.Time, values []time.Time) float64 {
	var maxSeconds float64
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		seconds := value.Sub(start).Seconds()
		if seconds > maxSeconds {
			maxSeconds = seconds
		}
	}
	return maxSeconds
}

func durationPercentileSeconds(start time.Time, values []time.Time, quantile float64) float64 {
	durations := make([]float64, 0, len(values))
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		seconds := value.Sub(start).Seconds()
		if seconds < 0 {
			seconds = 0
		}
		durations = append(durations, seconds)
	}
	return percentileValue(durations, quantile)
}

func percentileValue(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if quantile < 0 {
		quantile = 0
	}
	if quantile > 1 {
		quantile = 1
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func pollUntil(ctx context.Context, check func() (bool, error)) error {
	ticker := time.NewTicker(nativePollInterval)
	defer ticker.Stop()
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for native scenario condition: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func nativeRunID(opts Config, cluster string, scenario string) string {
	if strings.TrimSpace(opts.RunID) != "" {
		return boundedDNSLabel(opts.RunID)
	}
	suffixBytes := make([]byte, 3)
	if _, err := rand.Read(suffixBytes); err != nil {
		return boundedDNSLabel(fmt.Sprintf("%s-%s", cluster, scenario))
	}
	return boundedDNSLabel(fmt.Sprintf("%s-%s-%s", cluster, scenario, hex.EncodeToString(suffixBytes)))
}

func boundedDNSLabel(value string) string {
	return boundedDNSLabelMax(value, 63)
}

func boundedDNSLabelMax(value string, maxLength int) string {
	if maxLength <= 0 || maxLength > 63 {
		maxLength = 63
	}
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	out := builder.String()
	out = strings.Trim(out, "-")
	if len(out) > maxLength {
		out = strings.Trim(out[:maxLength], "-")
	}
	if out == "" {
		return "perf"
	}
	return out
}

func nativeResourceName(prefix, runID string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(runID))
	return boundedDNSLabelMax(fmt.Sprintf("%s-%08x", prefix, hash.Sum32()), 40)
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
