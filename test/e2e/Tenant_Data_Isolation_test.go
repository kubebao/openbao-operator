//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Tenant Data Isolation", Label("security", "tenant", "tenancy"), Ordered, func() {
	ctx := context.Background()

	var (
		cfg     *rest.Config
		scheme  *runtime.Scheme
		admin   client.Client
		tenantA *framework.Framework
		tenantB *framework.Framework
	)

	newTenantCluster := func(namespace, name string) *openbaov1alpha1.OpenBaoCluster {
		return &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile:  openbaov1alpha1.ProfileDevelopment,
				Version:  openBaoVersion,
				Image:    openBaoImage,
				Replicas: 1,
				InitContainer: &openbaov1alpha1.InitContainerConfig{
					Enabled: true,
					Image:   configInitImage,
				},
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: true,
					OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
						Enabled: true,
					},
					Requests: e2ehelpers.CreateE2ERequests(namespace),
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
					APIServerCIDR:        apiServerCIDR,
					APIServerEndpointIPs: apiServerEndpointIPs,
					APIServerEgress:      framework.APIServerEgressForNetworkProfile(),
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
	}

	waitForAvailable := func(namespace, clusterName string) {
		Eventually(func(g Gomega) {
			cluster := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster)).To(Succeed())
			available := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())
	}

	newStatusProbePod := func(namespace, name, baoAddr string, labels map[string]string, expectReachable bool) *corev1.Pod {
		command := `
set -eu
export BAO_ADDR=` + baoAddr + `
export BAO_SKIP_VERIFY=true
export BAO_CLIENT_TIMEOUT=5s
if bao status >/tmp/status.txt 2>&1; then
  cat /tmp/status.txt
  ` + map[bool]string{true: "exit 0", false: "echo unexpected-success >&2; exit 1"}[expectReachable] + `
fi
cat /tmp/status.txt
` + map[bool]string{true: "exit 1", false: "exit 0"}[expectReachable] + `
`

		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    labels,
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: ptr.To(true),
					RunAsUser:    ptr.To(int64(100)),
					RunAsGroup:   ptr.To(int64(1000)),
					FSGroup:      ptr.To(int64(1000)),
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				},
				Containers: []corev1.Container{
					{
						Name:    "bao",
						Image:   openBaoImage,
						Command: []string{"/bin/sh", "-ec"},
						Args:    []string{command},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
							RunAsNonRoot: ptr.To(true),
						},
					},
				},
			},
		}
	}

	BeforeAll(func() {
		var err error

		cfg, err = ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		scheme = runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(openbaov1alpha1.AddToScheme(scheme)).To(Succeed())

		admin, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		tenantA, err = framework.New(ctx, admin, "tenant-data-a", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		tenantB, err = framework.New(ctx, admin, "tenant-data-b", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		if tenantA != nil {
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			_ = tenantA.Cleanup(cleanupCtx)
			cancel()
		}
		if tenantB != nil {
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			_ = tenantB.Cleanup(cleanupCtx)
			cancel()
		}
	})

	It("isolates tenant data plane access across namespaces", Label(
		"case:tenant-data-plane-isolation",
		"covers:tenant-isolation",
		"covers:data-plane-isolation",
		"covers:network-isolation",
	), func() {
		const (
			clusterAName = "tenant-a-cluster"
			clusterBName = "tenant-b-cluster"
		)

		clusterA := newTenantCluster(tenantA.Namespace, clusterAName)
		clusterB := newTenantCluster(tenantB.Namespace, clusterBName)
		Expect(admin.Create(ctx, clusterA)).To(Succeed())
		Expect(admin.Create(ctx, clusterB)).To(Succeed())
		DeferCleanup(func() { _ = admin.Delete(ctx, clusterA) })
		DeferCleanup(func() { _ = admin.Delete(ctx, clusterB) })

		_, err := tenantA.WaitForStatefulSetReady(ctx, clusterAName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		_, err = tenantB.WaitForStatefulSetReady(ctx, clusterBName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		waitForAvailable(tenantA.Namespace, clusterAName)
		waitForAvailable(tenantB.Namespace, clusterBName)

		labelsA := map[string]string{
			constants.LabelOpenBaoCluster:   clusterAName,
			constants.LabelOpenBaoComponent: "backup",
		}
		labelsB := map[string]string{
			constants.LabelOpenBaoCluster:   clusterBName,
			constants.LabelOpenBaoComponent: "backup",
		}

		By("writing tenant-specific secrets through each tenant's own data plane")
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantA.Namespace, clusterAName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(e2ehelpers.WriteSecretViaJWT(
				ctx,
				cfg,
				admin,
				tenantA.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"e2e-test",
				"secret/tenant-a",
				labelsA,
				map[string]string{"value": "tenant-a"},
			)).To(Succeed())
		}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantB.Namespace, clusterBName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(e2ehelpers.WriteSecretViaJWT(
				ctx,
				cfg,
				admin,
				tenantB.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"e2e-test",
				"secret/tenant-b",
				labelsB,
				map[string]string{"value": "tenant-b"},
			)).To(Succeed())
		}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(Succeed())

		By("verifying each tenant can still reach and read its own cluster")
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantA.Namespace, clusterAName)
			g.Expect(err).NotTo(HaveOccurred())
			value, err := e2ehelpers.ReadSecretViaJWT(
				ctx,
				cfg,
				admin,
				tenantA.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"e2e-test",
				"secret/tenant-a",
				labelsA,
				"value",
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(value).To(Equal("tenant-a"))
		}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantB.Namespace, clusterBName)
			g.Expect(err).NotTo(HaveOccurred())
			value, err := e2ehelpers.ReadSecretViaJWT(
				ctx,
				cfg,
				admin,
				tenantB.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"e2e-test",
				"secret/tenant-b",
				labelsB,
				"value",
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(value).To(Equal("tenant-b"))
		}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(Succeed())

		By("verifying a labeled pod in tenant A still cannot reach tenant B's data plane")
		tenantBAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantB.Namespace, clusterBName)
		Expect(err).NotTo(HaveOccurred())
		crossTenantProbe := newStatusProbePod(tenantA.Namespace, "cross-tenant-probe", tenantBAddr, labelsB, false)
		result, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, admin, crossTenantProbe, 45*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Phase).To(Equal(corev1.PodSucceeded), "expected cross-tenant reachability probe to fail, logs:\n%s", result.Logs)
		_ = e2ehelpers.DeletePodBestEffort(ctx, admin, crossTenantProbe.Namespace, crossTenantProbe.Name)

		By("verifying the same labeled access path works inside tenant B")
		sameTenantProbe := newStatusProbePod(tenantB.Namespace, "same-tenant-probe", tenantBAddr, labelsB, true)
		result, err = e2ehelpers.RunPodUntilCompletion(ctx, cfg, admin, sameTenantProbe, 45*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Phase).To(Equal(corev1.PodSucceeded), "expected same-tenant reachability probe to succeed, logs:\n%s", result.Logs)
		_ = e2ehelpers.DeletePodBestEffort(ctx, admin, sameTenantProbe.Namespace, sameTenantProbe.Name)
	})
})
