//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	"github.com/kubebao/openbao-operator/test/utils"
)

// === Shared Helpers ===

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyz0123456789")

func randString(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func patchSingleTenantMode(namespace string) error {
	cmd := exec.Command("kubectl", "set", "env", "deployment/openbao-operator-controller",
		fmt.Sprintf("WATCH_NAMESPACE=%s", namespace),
		"-n", operatorNamespace,
	)
	if output, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("failed to set WATCH_NAMESPACE: %w\nOutput: %s", err, output)
	}
	return nil
}

// restoreMultiTenantMode removes the WATCH_NAMESPACE env var to restore multi-tenant mode.
func restoreMultiTenantMode() error {
	cmd := exec.Command("kubectl", "set", "env", "deployment/openbao-operator-controller",
		"WATCH_NAMESPACE-",
		"-n", operatorNamespace,
	)
	output, err := utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to restore multi-tenant mode: %w\nOutput: %s", err, output)
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "Removed WATCH_NAMESPACE from controller deployment\nOutput: %s\n", output)
	return nil
}

func waitForControllerRolloutAndLeader(watchNamespace string) {
	By("waiting for controller deployment rollout to complete")
	cmd := exec.Command("kubectl", "rollout", "status", "deployment/openbao-operator-controller",
		"-n", operatorNamespace,
		"--timeout=2m",
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to wait for controller rollout\nOutput: %s", output))

	By("waiting for controller pod to be Ready and configured with the expected WATCH_NAMESPACE")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get",
			"pods",
			"-l", "app.kubernetes.io/name=openbao-operator,app.kubernetes.io/component=controller",
			"-o", "go-template={{ range .items }}{{ if not .metadata.deletionTimestamp }}{{ .metadata.name }}{{ \"\\n\" }}{{ end }}{{ end }}",
			"-n", operatorNamespace,
		)
		podOutput, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		pods := utils.GetNonEmptyLines(podOutput)
		g.Expect(pods).To(HaveLen(1), "expected 1 controller pod running")
		controllerPodName := pods[0]

		cmd = exec.Command("kubectl", "get", "pod", controllerPodName, "-n", operatorNamespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}",
		)
		ready, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(ready).To(Equal("True"), "controller pod not Ready")

		cmd = exec.Command("kubectl", "get", "pod", controllerPodName, "-n", operatorNamespace,
			"-o", "jsonpath={.spec.containers[?(@.name==\"manager\")].env[?(@.name==\"WATCH_NAMESPACE\")].value}",
		)
		watch, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(watch).To(Equal(watchNamespace), "WATCH_NAMESPACE not set on controller pod")

		cmd = exec.Command("kubectl", "get", "lease", "openbao-controller-leader.openbao.org",
			"-n", operatorNamespace,
			"-o", "jsonpath={.spec.holderIdentity}",
		)
		holder, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(holder).To(ContainSubstring(controllerPodName), "controller pod is not leader yet")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}

var _ = Describe("Tenant Isolation", Label("security", "tenant", "tenancy"), Ordered, func() {
	ctx := context.Background()

	// --- Multi-Tenant: Self-Service ---
	Context("Multi-Tenant: Self-Service (Confused Deputy Prevention)", Label("critical"), func() {
		var (
			cfg        *rest.Config
			scheme     *runtime.Scheme
			admin      client.Client
			attackerNS string
			victimNS   string
		)

		BeforeAll(func() {
			var err error
			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			scheme = runtime.NewScheme()
			Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
			Expect(openbaov1alpha1.AddToScheme(scheme)).To(Succeed())

			admin, err = client.New(cfg, client.Options{Scheme: scheme})
			Expect(err).NotTo(HaveOccurred())

			// Setup Namespaces
			attackerNS = "e2e-attacker-" + randString(5)
			victimNS = "e2e-victim-" + randString(5)

			ns1 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: attackerNS}}
			Expect(admin.Create(ctx, ns1)).To(Succeed())

			ns2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: victimNS}}
			Expect(admin.Create(ctx, ns2)).To(Succeed())

			// Create ServiceAccount for Attacker
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "attacker",
					Namespace: attackerNS,
				},
			}
			Expect(admin.Create(ctx, sa)).To(Succeed())

			// Grant 'edit' role to Attacker in AttackerNS
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "attacker-edit",
					Namespace: attackerNS,
				},
				Subjects: []rbacv1.Subject{
					{Kind: "ServiceAccount", Name: "attacker", Namespace: attackerNS},
				},
				RoleRef: rbacv1.RoleRef{
					Kind: "ClusterRole",
					Name: "edit",
				},
			}
			Expect(admin.Create(ctx, rb)).To(Succeed())
		})

		AfterAll(func() {
			if admin != nil {
				_ = admin.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: attackerNS}})
				_ = admin.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: victimNS}})
			}
		})

		It("blocks cross-namespace provisioning attempts", Label(
			"case:tenant-isolation-blocks-cross-namespace-provisioning-attempts-15ee6ba1",
		), func() {
			// Create client impersonating the attacker
			userCfg := *cfg
			userCfg.Impersonate = rest.ImpersonationConfig{
				UserName: fmt.Sprintf("system:serviceaccount:%s:attacker", attackerNS),
			}
			attackerClient, err := client.New(&userCfg, client.Options{Scheme: scheme})
			Expect(err).NotTo(HaveOccurred())

			// Attempt to create Tenant targeting VictimNS
			tenant := &openbaov1alpha1.OpenBaoTenant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "malicious-tenant",
					Namespace: attackerNS,
				},
				Spec: openbaov1alpha1.OpenBaoTenantSpec{
					TargetNamespace: victimNS,
				},
			}

			// Creation should be BLOCKED by ValidatingAdmissionPolicy (defense in depth)
			err = attackerClient.Create(ctx, tenant)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("self-service mode"))
		})

		It("allows self-service provisioning within own namespace", Label(
			"case:tenant-isolation-allows-self-service-provisioning-within-own-67dc337d",
		), func() {
			// Create client impersonating the attacker
			userCfg := *cfg
			userCfg.Impersonate = rest.ImpersonationConfig{
				UserName: fmt.Sprintf("system:serviceaccount:%s:attacker", attackerNS),
			}
			attackerClient, err := client.New(&userCfg, client.Options{Scheme: scheme})
			Expect(err).NotTo(HaveOccurred())

			// Attempt to create Tenant targeting Own Namespace
			tenant := &openbaov1alpha1.OpenBaoTenant{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "legit-tenant",
					Namespace: attackerNS,
				},
				Spec: openbaov1alpha1.OpenBaoTenantSpec{
					TargetNamespace: attackerNS, // Match!
				},
			}

			Expect(attackerClient.Create(ctx, tenant)).To(Succeed())

			// Controller should Provision it
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoTenant{}
				err := admin.Get(ctx, types.NamespacedName{Name: tenant.Name, Namespace: attackerNS}, updated)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(updated.Status.Provisioned).To(BeTrue(), "Status.Provisioned should be true")
			}, 30*time.Second, 1*time.Second).Should(Succeed())
		})
	})

	// --- Single-Tenant Mode ---
	Context("Single-Tenant Mode", Label("single-tenant"), func() {
		var (
			cfg    *rest.Config
			scheme *runtime.Scheme
			c      client.Client
		)

		const (
			singleTenantNS  = "e2e-single-tenant"
			clusterName     = "single-tenant-cluster"
			clusterRoleName = "openbao-operator-single-tenant"
		)

		BeforeAll(func() {
			var err error
			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			scheme = runtime.NewScheme()
			Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
			Expect(openbaov1alpha1.AddToScheme(scheme)).To(Succeed())

			c, err = client.New(cfg, client.Options{Scheme: scheme})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			// Resource preservation must not leak controller-wide configuration to later suites.
			By("restoring operator to multi-tenant mode")
			Expect(restoreMultiTenantMode()).To(Succeed())
			waitForControllerRolloutAndLeader("")

			if os.Getenv("E2E_SKIP_CLEANUP") == e2eStringTrue {
				By("E2E_SKIP_CLEANUP=true: keeping single-tenant namespace for debugging")
				return
			}

			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			// Clean up the single-tenant namespace
			By("cleaning up single-tenant test namespace")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: singleTenantNS}}
			if err := c.Delete(cleanupCtx, ns); err != nil && !apierrors.IsNotFound(err) {
				_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: failed to delete namespace %q: %v\n", singleTenantNS, err)
			}
		})

		It("sets up single-tenant mode environment", Label(
			"case:tenant-isolation-sets-up-single-tenant-mode-environment-4b87e620",
		), func() {
			By("creating the single-tenant namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: singleTenantNS,
					Labels: map[string]string{
						"pod-security.kubernetes.io/enforce": "restricted",
					},
				},
			}
			err := c.Create(ctx, ns)
			if apierrors.IsAlreadyExists(err) {
				_, _ = fmt.Fprintf(GinkgoWriter, "Namespace %q already exists\n", singleTenantNS)
			} else {
				Expect(err).NotTo(HaveOccurred())
			}

			By("applying the single-tenant ClusterRole")
			cmd := exec.Command("kubectl", "apply", "-f", "config/rbac/single_tenant_clusterrole.yaml")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply single-tenant ClusterRole")

			By("creating RoleBinding in target namespace")
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "openbao-operator-single-tenant",
					Namespace: singleTenantNS,
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     clusterRoleName,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "openbao-operator-controller",
						Namespace: operatorNamespace,
					},
				},
			}
			err = c.Create(ctx, rb)
			if apierrors.IsAlreadyExists(err) {
				_, _ = fmt.Fprintf(GinkgoWriter, "RoleBinding already exists\n")
			} else {
				Expect(err).NotTo(HaveOccurred())
			}

			By("patching controller deployment with WATCH_NAMESPACE")
			Expect(patchSingleTenantMode(singleTenantNS)).To(Succeed())

			By("waiting for controller to restart with new configuration")
			waitForControllerRolloutAndLeader(singleTenantNS)

			_, _ = fmt.Fprintf(GinkgoWriter, "Controller restarted in single-tenant mode for namespace %q\n", singleTenantNS)
		})

		It("creates an OpenBaoCluster without OpenBaoTenant", Label(
			"case:tenant-isolation-creates-an-openbaocluster-without-openbaotenant-ffc0817d",
		), func() {
			By("creating OpenBaoCluster directly (no tenant required)")
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: singleTenantNS,
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
						Enabled:  true,
						Requests: framework.DefaultAdminSelfInitRequests(),
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

			err := c.Create(ctx, cluster)
			Expect(err).NotTo(HaveOccurred())
			_, _ = fmt.Fprintf(GinkgoWriter, "Created OpenBaoCluster %q in single-tenant namespace %q\n", clusterName, singleTenantNS)
		})

		It("reconciles the cluster to Available via event-driven reconciliation", Label(
			"case:tenant-isolation-reconciles-the-cluster-to-available-via-ef2fac83",
		), func() {
			By("waiting for StatefulSet to be created")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: singleTenantNS}, sts)
				if err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet not found yet: %v\n", err)
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sts.Spec.Replicas).NotTo(BeNil())
				_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet %q exists with replicas=%d\n", clusterName, *sts.Spec.Replicas)
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting for StatefulSet pods to be Ready")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: singleTenantNS}, sts)).To(Succeed())
				_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet status: replicas=%d ready=%d\n",
					sts.Status.Replicas, sts.Status.ReadyReplicas)
				g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))
			}, framework.DefaultLongWaitTimeout, 3*time.Second).Should(Succeed())

			By("waiting for cluster to become Available")
			Eventually(func(g Gomega) {
				cluster := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: singleTenantNS}, cluster)).To(Succeed())

				available := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				if available == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Available condition not set yet\n")
					g.Expect(available).NotTo(BeNil())
					return
				}

				_, _ = fmt.Fprintf(GinkgoWriter, "Available condition: status=%s reason=%s\n",
					available.Status, available.Reason)
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			_, _ = fmt.Fprintf(GinkgoWriter, "OpenBaoCluster %q is Available in single-tenant mode!\n", clusterName)
		})

		It("reacts to StatefulSet deletion via event-driven reconciliation", Label(
			"case:tenant-isolation-reacts-to-statefulset-deletion-via-event-0a45a642",
		), func() {
			By("getting the current StatefulSet UID")
			sts := &appsv1.StatefulSet{}
			Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: singleTenantNS}, sts)).To(Succeed())
			oldUID := sts.UID
			_, _ = fmt.Fprintf(GinkgoWriter, "Current StatefulSet UID: %s\n", oldUID)

			By("deleting the StatefulSet to test event-driven reconciliation")
			// Deleting managed resources directly is blocked by admission policies in E2E.
			// Impersonate the controller ServiceAccount to simulate the deletion event.
			cmd := exec.Command("kubectl", "delete", "statefulset", clusterName,
				"-n", singleTenantNS,
				"--wait=false",
				"--as=system:serviceaccount:openbao-operator-system:openbao-operator-controller",
			)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to delete StatefulSet via impersonation\nOutput: %s", output))

			By("waiting for StatefulSet to be recreated (event-driven, should be fast)")
			// In single-tenant mode with Owns() watches, the deletion event should trigger
			// immediate reconciliation and recreation - much faster than polling-based
			startTime := time.Now()
			Eventually(func(g Gomega) {
				newSts := &appsv1.StatefulSet{}
				err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: singleTenantNS}, newSts)
				if err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Waiting for StatefulSet recreation (elapsed: %v)\n", time.Since(startTime))
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(newSts.UID).NotTo(Equal(oldUID), "StatefulSet should have a new UID")
			}, 30*time.Second, 1*time.Second).Should(Succeed())

			recreationTime := time.Since(startTime)
			_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet recreated in %v (event-driven reconciliation)\n", recreationTime)

			// In single-tenant mode, recreation should be nearly instant (< 10s)
			// In multi-tenant mode with polling, it could take up to the requeue interval
			Expect(recreationTime).To(BeNumerically("<", 20*time.Second),
				"Event-driven reconciliation should recreate StatefulSet quickly")
		})
	})
})
