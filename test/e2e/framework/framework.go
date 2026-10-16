//go:build e2e
// +build e2e

package framework

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/test/utils"
)

const (
	// ReconcileTriggerAnnotationKey is used by tests to force a reconcile by mutating
	// an annotation on the OpenBaoCluster.
	ReconcileTriggerAnnotationKey = "e2e.openbao.org/reconcile-trigger"

	// RestrictedPodSecurityLabelKey enforces the Kubernetes restricted Pod Security Standard.
	RestrictedPodSecurityLabelKey = "pod-security.kubernetes.io/enforce"
	// RestrictedPodSecurityLabelValue is the value for RestrictedPodSecurityLabelKey.
	RestrictedPodSecurityLabelValue = "restricted"

	// DefaultPollInterval is the default polling interval for E2E waits.
	DefaultPollInterval = 2 * time.Second
	// DefaultWaitTimeout is the default timeout for common E2E waits.
	DefaultWaitTimeout = 2 * time.Minute
	// DefaultLongWaitTimeout is a longer timeout used for cluster convergence.
	DefaultLongWaitTimeout = 5 * time.Minute
)

// Framework encapsulates common E2E setup/teardown and convenience helpers.
type Framework struct {
	Client            client.Client
	Namespace         string
	OperatorNamespace string
	Ctx               context.Context
	TenantName        string
}

// DevelopmentClusterConfig defines the inputs for creating a basic Development profile cluster.
type DevelopmentClusterConfig struct {
	TLSMode              openbaov1alpha1.TLSMode
	Name                 string
	Replicas             int32
	Version              string
	Image                string
	ConfigInitImg        string
	APIServerCIDR        string
	APIServerEndpointIPs []string
	DeletionPolicy       openbaov1alpha1.DeletionPolicy
}

// DefaultAdminSelfInitRequests returns a standard set of SelfInit requests that:
// - enable JWT authentication
// - create an "admin" policy with full capabilities.
//
// This helper is used across multiple E2E tests to avoid duplicating the same
// SelfInit wiring in each test file.
func DefaultAdminSelfInitRequests() []openbaov1alpha1.SelfInitRequest {
	return []openbaov1alpha1.SelfInitRequest{
		{
			Name:      "enable-jwt-auth",
			Operation: openbaov1alpha1.SelfInitOperationUpdate,
			Path:      "sys/auth/jwt",
			AuthMethod: &openbaov1alpha1.SelfInitAuthMethod{
				Type: "jwt",
			},
		},
		{
			Name:      "create-admin-policy",
			Operation: openbaov1alpha1.SelfInitOperationUpdate,
			Path:      "sys/policies/acl/admin",
			Policy: &openbaov1alpha1.SelfInitPolicy{
				Policy: `path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}`,
			},
		},
	}
}

// EnsureRestrictedNamespace creates a namespace with a restricted Pod Security label.
// It ignores AlreadyExists errors.
func EnsureRestrictedNamespace(ctx context.Context, c client.Client, name string) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if c == nil {
		return fmt.Errorf("kubernetes client is required")
	}
	if name == "" {
		return fmt.Errorf("name is required")
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				RestrictedPodSecurityLabelKey: RestrictedPodSecurityLabelValue,
			},
		},
	}

	err := c.Create(ctx, ns)
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("failed to create namespace %q: %w", name, err)
}

// New creates a unique tenant namespace (with restricted Pod Security labels) and a matching OpenBaoTenant,
// then waits until the tenant is provisioned.
func New(ctx context.Context, c client.Client, baseName string, operatorNamespace string) (*Framework, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if c == nil {
		return nil, fmt.Errorf("kubernetes client is required")
	}
	if baseName == "" {
		return nil, fmt.Errorf("base name is required")
	}
	if operatorNamespace == "" {
		return nil, fmt.Errorf("operator namespace is required")
	}

	nsName, err := uniqueNamespaceName(baseName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate namespace name: %w", err)
	}

	f := &Framework{
		Client:            c,
		Namespace:         nsName,
		OperatorNamespace: operatorNamespace,
		Ctx:               ctx,
		TenantName:        nsName,
	}

	if err := f.createNamespace(ctx); err != nil {
		return nil, err
	}
	if err := f.createTenant(ctx); err != nil {
		return nil, err
	}
	if err := f.WaitForTenantProvisioned(ctx, DefaultWaitTimeout, DefaultPollInterval); err != nil {
		return nil, err
	}

	return f, nil
}

// NewSetup initializes a new Framework with a fresh client and scheme.
// It acts as a "Batteries Included" setup for E2E tests.
func NewSetup(ctx context.Context, baseName string, operatorNamespace string) (*Framework, error) {
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kube config: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add openbao scheme: %w", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		return nil, fmt.Errorf("failed to add gateway scheme: %w", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return New(ctx, c, baseName, operatorNamespace)
}

// RequireGatewayAPI ensures Gateway API CRDs are installed.
// It returns a cleanup function that should be called in AfterAll.
func (f *Framework) RequireGatewayAPI() (func(), error) {
	if err := f.InstallGatewayAPI(); err != nil {
		return nil, err
	}
	return func() {
		_ = f.UninstallGatewayAPI()
	}, nil
}

// InstallGatewayAPI installs the Gateway API CRDs.
func (f *Framework) InstallGatewayAPI() error {
	// Re-using the logic from utils, but making it a method of Framework for convenience
	manifestPath := "test/manifests/gateway-api/v1.5.1/crds"
	// In a real framework, we might want to check env vars or default paths here
	// For now, we assume the test/manifests path is correct relative to where tests run

	// Use server-side apply
	cmd := exec.Command("kubectl", "apply", "--server-side", "--field-manager=openbao-e2e", "-f", manifestPath)
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("failed to install Gateway API CRDs: %w", err)
	}

	// Wait for established
	cmd = exec.Command("kubectl", "wait", "--for", "condition=Established",
		"crd/gateways.gateway.networking.k8s.io",
		"crd/httproutes.gateway.networking.k8s.io",
		"crd/tlsroutes.gateway.networking.k8s.io",
		"--timeout", "5m")
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("failed to wait for Gateway API CRDs: %w", err)
	}
	return nil
}

// UninstallGatewayAPI removes the Gateway API CRDs.
func (f *Framework) UninstallGatewayAPI() error {
	manifestPath := "test/manifests/gateway-api/v1.5.1/crds"
	cmd := exec.Command("kubectl", "delete", "-f", manifestPath, "--ignore-not-found")
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("failed to uninstall Gateway API CRDs: %w", err)
	}
	return nil
}

// WaitForPhase waits for the cluster to reach the specified phase.
func (f *Framework) WaitForPhase(clusterName string, phase openbaov1alpha1.ClusterPhase) {
	gomega.Eventually(func(g gomega.Gomega) {
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		err := f.Client.Get(f.Ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(cluster.Status.Phase).To(gomega.Equal(phase))
	}, DefaultWaitTimeout, DefaultPollInterval).Should(gomega.Succeed(), "Cluster failed to reach phase %s", phase)
}

// WaitForCondition waits for the specified condition to have the expected status.
func (f *Framework) WaitForCondition(clusterName string, conditionType openbaov1alpha1.ConditionType, status metav1.ConditionStatus) {
	gomega.Eventually(func(g gomega.Gomega) {
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		err := f.Client.Get(f.Ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		cond := meta.FindStatusCondition(cluster.Status.Conditions, string(conditionType))
		g.Expect(cond).NotTo(gomega.BeNil())
		g.Expect(cond.Status).To(gomega.Equal(status))
	}, DefaultWaitTimeout, DefaultPollInterval).Should(gomega.Succeed(), "Cluster failed to reach condition %s=%s", conditionType, status)
}

// WaitForConditionReason waits for the specified condition to have the expected status and reason.
func (f *Framework) WaitForConditionReason(
	clusterName string,
	conditionType openbaov1alpha1.ConditionType,
	status metav1.ConditionStatus,
	reason string,
) {
	gomega.Eventually(func(g gomega.Gomega) {
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		err := f.Client.Get(f.Ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		cond := meta.FindStatusCondition(cluster.Status.Conditions, string(conditionType))
		g.Expect(cond).NotTo(gomega.BeNil())
		g.Expect(cond.Status).To(gomega.Equal(status))
		g.Expect(cond.Reason).To(gomega.Equal(reason))
	}, DefaultWaitTimeout, DefaultPollInterval).Should(
		gomega.Succeed(),
		"Cluster failed to reach condition %s=%s reason=%s",
		conditionType,
		status,
		reason,
	)
}

// Cleanup deletes the tenant namespace and OpenBaoTenant, ignoring NotFound.
func (f *Framework) Cleanup(ctx context.Context) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if f.Client == nil {
		return nil
	}
	if os.Getenv("E2E_SKIP_CLEANUP") == "true" {
		return nil
	}

	if err := f.deleteOpenBaoClusters(ctx); err != nil {
		return err
	}

	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.TenantName,
			Namespace: f.OperatorNamespace,
		},
	}
	if err := f.Client.Delete(ctx, tenant); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete OpenBaoTenant %s/%s: %w", f.OperatorNamespace, f.TenantName, err)
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.Namespace}}
	if err := f.Client.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace %q: %w", f.Namespace, err)
	}

	return nil
}

// CreateDevelopmentCluster creates a basic Development profile OpenBaoCluster in the framework namespace.
func (f *Framework) CreateDevelopmentCluster(ctx context.Context, cfg DevelopmentClusterConfig) (*openbaov1alpha1.OpenBaoCluster, error) {
	if f == nil || f.Client == nil {
		return nil, fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	if cfg.Replicas <= 0 {
		return nil, fmt.Errorf("replicas must be positive")
	}
	if cfg.Version == "" {
		return nil, fmt.Errorf("openbao version is required")
	}
	if cfg.Image == "" {
		return nil, fmt.Errorf("openbao image is required")
	}
	if cfg.ConfigInitImg == "" {
		return nil, fmt.Errorf("config-init image is required")
	}
	if cfg.APIServerCIDR == "" {
		return nil, fmt.Errorf("api server cidr is required")
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: f.Namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  cfg.Version,
			Image:    cfg.Image,
			Replicas: cfg.Replicas,
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Enabled: true,
				Image:   cfg.ConfigInitImg,
			},
			TLS: openbaov1alpha1.TLSConfig{
				Enabled:        true,
				Mode:           openbaov1alpha1.TLSModeOperatorManaged,
				RotationPeriod: "720h",
			},
			Storage: openbaov1alpha1.StorageConfig{
				Size: "1Gi",
			},
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR:        cfg.APIServerCIDR,
				APIServerEndpointIPs: cfg.APIServerEndpointIPs,
				APIServerEgress:      APIServerEgressForNetworkProfile(),
			},
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
		},
	}

	if cfg.DeletionPolicy != "" {
		cluster.Spec.DeletionPolicy = cfg.DeletionPolicy
	}
	if cfg.TLSMode != "" {
		cluster.Spec.TLS.Mode = cfg.TLSMode
	}

	if sc := strings.TrimSpace(os.Getenv("E2E_STORAGE_CLASS")); sc != "" {
		cluster.Spec.Storage.StorageClassName = &sc
	}

	if err := f.Client.Create(ctx, cluster); err != nil {
		return nil, fmt.Errorf("failed to create OpenBaoCluster %s/%s: %w", f.Namespace, cfg.Name, err)
	}

	return cluster, nil
}

// WaitForTenantProvisioned waits until the framework OpenBaoTenant reports Status.Provisioned=true.
func (f *Framework) WaitForTenantProvisioned(ctx context.Context, timeout time.Duration, pollInterval time.Duration) error {
	if f == nil || f.Client == nil {
		return fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if pollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		tenant := &openbaov1alpha1.OpenBaoTenant{}
		if err := f.Client.Get(ctx, types.NamespacedName{Name: f.TenantName, Namespace: f.OperatorNamespace}, tenant); err == nil {
			if tenant.Status.Provisioned {
				return nil
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get OpenBaoTenant %s/%s: %w", f.OperatorNamespace, f.TenantName, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled while waiting for OpenBaoTenant %s/%s to be provisioned: %w", f.OperatorNamespace, f.TenantName, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for OpenBaoTenant %s/%s to be provisioned", f.OperatorNamespace, f.TenantName)
		case <-ticker.C:
		}
	}
}

// WaitForClusterPhase waits until the given cluster reaches the expected status phase.
func (f *Framework) WaitForClusterPhase(ctx context.Context, clusterName string, expectedPhase openbaov1alpha1.ClusterPhase, timeout time.Duration, pollInterval time.Duration) error {
	if f == nil || f.Client == nil {
		return fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if clusterName == "" {
		return fmt.Errorf("cluster name is required")
	}
	if timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if pollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		if err := f.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster); err == nil {
			if cluster.Status.Phase == expectedPhase {
				return nil
			}
		} else {
			return fmt.Errorf("failed to get OpenBaoCluster %s/%s: %w", f.Namespace, clusterName, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled while waiting for OpenBaoCluster %s/%s to reach phase %q: %w", f.Namespace, clusterName, expectedPhase, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for OpenBaoCluster %s/%s to reach phase %q", f.Namespace, clusterName, expectedPhase)
		case <-ticker.C:
		}
	}
}

// SetMaintenanceEnabled enables or disables cluster maintenance mode through spec.maintenance.enabled.
func (f *Framework) SetMaintenanceEnabled(ctx context.Context, clusterName string, enabled bool) error {
	if f == nil || f.Client == nil {
		return fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if clusterName == "" {
		return fmt.Errorf("cluster name is required")
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := f.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster); err != nil {
		return fmt.Errorf("failed to get OpenBaoCluster %s/%s: %w", f.Namespace, clusterName, err)
	}

	original := cluster.DeepCopy()

	if enabled {
		if cluster.Spec.Maintenance == nil {
			cluster.Spec.Maintenance = &openbaov1alpha1.MaintenanceConfig{}
		}
		cluster.Spec.Maintenance.Enabled = true
	} else {
		if cluster.Spec.Maintenance == nil {
			return nil
		}
		cluster.Spec.Maintenance = nil
	}

	if err := f.Client.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to patch OpenBaoCluster %s/%s maintenance config: %w", f.Namespace, clusterName, err)
	}

	return nil
}

// TriggerReconcile forces a reconcile by patching a timestamp annotation on the OpenBaoCluster.
func (f *Framework) TriggerReconcile(ctx context.Context, clusterName string) error {
	if f == nil || f.Client == nil {
		return fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if clusterName == "" {
		return fmt.Errorf("cluster name is required")
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := f.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster); err != nil {
		return fmt.Errorf("failed to get OpenBaoCluster %s/%s: %w", f.Namespace, clusterName, err)
	}

	original := cluster.DeepCopy()
	annotations := cluster.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[ReconcileTriggerAnnotationKey] = time.Now().UTC().Format(time.RFC3339Nano)
	cluster.SetAnnotations(annotations)

	if err := f.Client.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to patch OpenBaoCluster %s/%s reconcile trigger: %w", f.Namespace, clusterName, err)
	}

	return nil
}

// GetStatefulSet fetches the cluster StatefulSet (same name as the cluster).
func (f *Framework) GetStatefulSet(ctx context.Context, clusterName string) (*appsv1.StatefulSet, error) {
	if f == nil || f.Client == nil {
		return nil, fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if clusterName == "" {
		return nil, fmt.Errorf("cluster name is required")
	}

	sts := &appsv1.StatefulSet{}
	if err := f.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts); err != nil {
		return nil, fmt.Errorf("failed to get StatefulSet %s/%s: %w", f.Namespace, clusterName, err)
	}
	return sts, nil
}

// DeleteStatefulSet deletes the cluster StatefulSet (same name as the cluster).
func (f *Framework) DeleteStatefulSet(ctx context.Context, clusterName string) error {
	if f == nil || f.Client == nil {
		return fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if clusterName == "" {
		return fmt.Errorf("cluster name is required")
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: f.Namespace,
		},
	}
	if err := f.Client.Delete(ctx, sts); err != nil {
		return fmt.Errorf("failed to delete StatefulSet %s/%s: %w", f.Namespace, clusterName, err)
	}
	return nil
}

// WaitForStatefulSetReady waits until the StatefulSet exists and has at least the expected ready replicas.
func (f *Framework) WaitForStatefulSetReady(ctx context.Context, clusterName string, expectedReadyReplicas int32, timeout time.Duration, pollInterval time.Duration) (*appsv1.StatefulSet, error) {
	if f == nil || f.Client == nil {
		return nil, fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if clusterName == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	if expectedReadyReplicas < 0 {
		return nil, fmt.Errorf("expected ready replicas must be non-negative")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("poll interval must be positive")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		sts := &appsv1.StatefulSet{}
		err := f.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)
		if err == nil {
			if sts.Status.ReadyReplicas >= expectedReadyReplicas {
				return sts, nil
			}
		} else if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get StatefulSet %s/%s: %w", f.Namespace, clusterName, err)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled while waiting for StatefulSet %s/%s to be ready: %w", f.Namespace, clusterName, ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for StatefulSet %s/%s to be ready", f.Namespace, clusterName)
		case <-ticker.C:
		}
	}
}

// WaitForStatefulSetRecreated waits until the StatefulSet exists and has a different UID than oldUID.
func (f *Framework) WaitForStatefulSetRecreated(ctx context.Context, clusterName string, oldUID types.UID, timeout time.Duration, pollInterval time.Duration) (*appsv1.StatefulSet, error) {
	if oldUID == "" {
		return nil, fmt.Errorf("old uid is required")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		current := &appsv1.StatefulSet{}
		if err := f.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, current); err == nil {
			if current.DeletionTimestamp == nil && current.UID != oldUID {
				return current, nil
			}
		} else if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get StatefulSet %s/%s: %w", f.Namespace, clusterName, err)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled while waiting for StatefulSet %s/%s to be recreated: %w", f.Namespace, clusterName, ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for StatefulSet %s/%s to be recreated", f.Namespace, clusterName)
		case <-ticker.C:
		}
	}
}

func (f *Framework) createNamespace(ctx context.Context) error {
	return EnsureRestrictedNamespace(ctx, f.Client, f.Namespace)
}

func (f *Framework) createTenant(ctx context.Context) error {
	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.TenantName,
			Namespace: f.OperatorNamespace,
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: f.Namespace,
		},
	}

	err := f.Client.Create(ctx, tenant)
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("failed to create OpenBaoTenant %s/%s: %w", f.OperatorNamespace, f.TenantName, err)
}

func uniqueNamespaceName(baseName string) (string, error) {
	suffixBytes := make([]byte, 4)
	if _, err := rand.Read(suffixBytes); err != nil {
		return "", fmt.Errorf("failed to generate random suffix: %w", err)
	}
	suffix := hex.EncodeToString(suffixBytes)

	// Keep namespace names short and DNS-label compliant.
	now := time.Now().UTC().Unix()
	return fmt.Sprintf("e2e-%s-%d-%s", baseName, now, suffix), nil
}

func (f *Framework) deleteOpenBaoClusters(ctx context.Context) error {
	if f == nil || f.Client == nil {
		return fmt.Errorf("framework client is required")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}

	var clusters openbaov1alpha1.OpenBaoClusterList
	if err := f.Client.List(ctx, &clusters, client.InNamespace(f.Namespace)); err != nil {
		return fmt.Errorf("failed to list OpenBaoClusters in namespace %q: %w", f.Namespace, err)
	}
	for i := range clusters.Items {
		cluster := clusters.Items[i]
		if err := f.Client.Delete(ctx, &cluster); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
	}

	if err := f.waitForOpenBaoClustersDeleted(ctx, DefaultWaitTimeout, DefaultPollInterval); err == nil {
		return nil
	}

	if err := f.removeOpenBaoClusterFinalizers(ctx); err != nil {
		return err
	}

	var clustersAfterFinalizerRemoval openbaov1alpha1.OpenBaoClusterList
	if err := f.Client.List(ctx, &clustersAfterFinalizerRemoval, client.InNamespace(f.Namespace)); err != nil {
		return fmt.Errorf("failed to list OpenBaoClusters in namespace %q after finalizer removal: %w", f.Namespace, err)
	}
	for i := range clustersAfterFinalizerRemoval.Items {
		cluster := clustersAfterFinalizerRemoval.Items[i]
		if err := f.Client.Delete(ctx, &cluster); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete OpenBaoCluster %s/%s after finalizer removal: %w", cluster.Namespace, cluster.Name, err)
		}
	}

	if err := f.waitForOpenBaoClustersDeleted(ctx, 45*time.Second, DefaultPollInterval); err != nil {
		return err
	}

	return nil
}

func (f *Framework) waitForOpenBaoClustersDeleted(ctx context.Context, timeout time.Duration, pollInterval time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if pollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		var clusters openbaov1alpha1.OpenBaoClusterList
		if err := f.Client.List(ctx, &clusters, client.InNamespace(f.Namespace)); err != nil {
			return fmt.Errorf("failed to list OpenBaoClusters in namespace %q: %w", f.Namespace, err)
		}
		if len(clusters.Items) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled while waiting for OpenBaoClusters in namespace %q to be deleted: %w", f.Namespace, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for OpenBaoClusters in namespace %q to be deleted (remaining=%d)", f.Namespace, len(clusters.Items))
		case <-ticker.C:
		}
	}
}

func (f *Framework) removeOpenBaoClusterFinalizers(ctx context.Context) error {
	var clusters openbaov1alpha1.OpenBaoClusterList
	if err := f.Client.List(ctx, &clusters, client.InNamespace(f.Namespace)); err != nil {
		return fmt.Errorf("failed to list OpenBaoClusters in namespace %q for finalizer removal: %w", f.Namespace, err)
	}
	for i := range clusters.Items {
		cluster := clusters.Items[i]
		if len(cluster.Finalizers) == 0 {
			continue
		}
		original := cluster.DeepCopy()
		cluster.Finalizers = nil
		if err := f.Client.Patch(ctx, &cluster, client.MergeFrom(original)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to remove finalizers from OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
	}
	return nil
}
