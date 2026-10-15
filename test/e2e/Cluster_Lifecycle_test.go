//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	platformconstants "github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Cluster Lifecycle", Label("lifecycle", "cluster"), Ordered, func() {
	ctx := context.Background()

	Context("Tenant + Cluster lifecycle (Self-Init)", Label("critical", "tenant"), func() {
		var (
			f   *framework.Framework
			c   client.Client
			cfg *rest.Config
		)

		const (
			clusterName = "smoke-cluster"
		)

		BeforeAll(func() {
			var err error
			f, err = framework.NewSetup(ctx, "smoke", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			c = f.Client

			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if f == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			_ = f.Cleanup(cleanupCtx)
		})

		It("provisions tenant RBAC via OpenBaoTenant", Label(
			"case:cluster-lifecycle-provisions-tenant-rbac-via-openbaotenant-1798196a",
		), func() {
			By("verifying OpenBaoTenant is provisioned")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoTenant{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: f.TenantName, Namespace: operatorNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Provisioned).To(BeTrue())
				g.Expect(updated.Status.LastError).To(BeEmpty())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		It("creates an OpenBaoCluster and converges to Available", Label(
			"case:cluster-lifecycle-creates-an-openbaocluster-and-converges-to-00cd448b",
		), func() {
			By(fmt.Sprintf("creating OpenBaoCluster %q in namespace %q", clusterName, f.Namespace))
			auditFileStorage := &openbaov1alpha1.AuditFileStorageConfig{
				Mode: openbaov1alpha1.AuditFileStorageModeManagedPVC,
				Size: "1Gi",
			}
			if sc := strings.TrimSpace(os.Getenv("E2E_STORAGE_CLASS")); sc != "" {
				auditFileStorage.StorageClassName = &sc
			}

			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: f.Namespace,
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
					ReconcilePolicies: true,
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
						Requests: append(
							framework.DefaultAdminSelfInitRequests(),
							e2ehelpers.CreateE2ERequests(f.Namespace)...,
						),
					},
					TLS: openbaov1alpha1.TLSConfig{
						Enabled:        true,
						Mode:           openbaov1alpha1.TLSModeOperatorManaged,
						RotationPeriod: "720h",
					},
					Storage: openbaov1alpha1.StorageConfig{
						Size: "1Gi",
					},
					Maintenance: &openbaov1alpha1.MaintenanceConfig{
						Enabled: true,
					},
					Network: &openbaov1alpha1.NetworkConfig{
						APIServerCIDR:        apiServerCIDR,
						APIServerEndpointIPs: apiServerEndpointIPs,
						IngressRules: []networkingv1.NetworkPolicyIngressRule{
							{
								From: []networkingv1.NetworkPolicyPeer{
									{
										PodSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"role": "test-verifier",
											},
										},
									},
								},
								Ports: []networkingv1.NetworkPolicyPort{
									{
										Protocol: ptr.To(corev1.ProtocolTCP),
										Port:     ptr.To(intstr.FromInt(8200)),
									},
								},
							},
						},
					},
					AuditFileStorage: auditFileStorage,
					Audit: []openbaov1alpha1.AuditDevice{
						{
							Type:        "file",
							Path:        "file",
							Description: "E2E file audit log",
							FileOptions: &openbaov1alpha1.FileAuditOptions{
								FilePath: "/openbao/audit/audit.jsonl",
							},
						},
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			if sc := os.Getenv("E2E_STORAGE_CLASS"); strings.TrimSpace(sc) != "" {
				sc = strings.TrimSpace(sc)
				cluster.Spec.Storage.StorageClassName = &sc
			}

			Expect(c.Create(ctx, cluster)).To(Succeed())

			By("waiting for OpenBaoCluster to be observed by the API server")
			Eventually(func() error {
				return c.Get(ctx, types.NamespacedName{
					Name:      clusterName,
					Namespace: f.Namespace,
				}, &openbaov1alpha1.OpenBaoCluster{})
			}, 30*time.Second, 1*time.Second).Should(Succeed())

			By("waiting for StatefulSet to be created")
			_, err := f.WaitForStatefulSetReady(
				ctx,
				clusterName,
				1,
				framework.DefaultWaitTimeout,
				framework.DefaultPollInterval,
			)
			Expect(err).NotTo(HaveOccurred())

			By("triggering a reconcile and waiting for Available condition")
			Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())
			f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

			By("verifying the approved policy bundle was reconciled")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(cluster), updated)).To(Succeed())
				g.Expect(updated.Status.Workload).NotTo(BeNil())
				g.Expect(updated.Status.Workload.PolicyRevision).NotTo(BeEmpty())
				g.Expect(updated.Status.Workload.LastError).To(BeNil())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("verifying reconcile metrics are emitted for the cluster")
			metricsOutput, metricErr := framework.WaitForControllerMetricSubstrings(
				operatorNamespace,
				2*time.Minute,
				"openbao_reconcile_duration_seconds_count{",
				fmt.Sprintf(`namespace="%s"`, f.Namespace),
				fmt.Sprintf(`name="%s"`, clusterName),
				`controller="openbaocluster-status"`,
			)
			Expect(metricErr).NotTo(HaveOccurred(), "Last metrics output:\n%s", metricsOutput)

			By("verifying Raft Autopilot is configured")
			// (Simplified verification for smoke test)
			cm := &corev1.ConfigMap{}
			Expect(c.Get(ctx, types.NamespacedName{Name: clusterName + "-config", Namespace: f.Namespace}, cm)).To(Succeed())
		})

		It("writes file audit records to managed audit storage", Label(
			"case:cluster-lifecycle-writes-file-audit-records-to-managed-fd0d4c2f",
			"audit",
		), func() {
			auditPVCName := clusterName + "-audit"
			verifierLabels := map[string]string{"role": "test-verifier"}

			By("waiting for audit file storage readiness")
			f.WaitForConditionReason(
				clusterName,
				openbaov1alpha1.ConditionAuditFileStorageReady,
				metav1.ConditionTrue,
				"AuditFileStorageReady",
			)

			By("verifying the managed audit PVC is Bound and RWX")
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: auditPVCName, Namespace: f.Namespace}, pvc)).To(Succeed())
				g.Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany))
				g.Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting for SelfInit to create the JWT role used by the audit request")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.SelfInitialized).To(BeTrue())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("triggering an audited OpenBao API request")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, clusterName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(e2ehelpers.WriteSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					openBaoImage,
					baoAddr,
					"default",
					"e2e-test",
					"secret/audit-file-storage-smoke",
					verifierLabels,
					map[string]string{"value": "audit-file-storage"},
				)).To(Succeed())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

			By("reading audit records through a collector-style read-only PVC mount")
			Eventually(func(g Gomega) {
				logs, err := readAuditRecordsFromPVC(ctx, cfg, c, f.Namespace, openBaoImage, auditPVCName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(logs).To(ContainSubstring("total_lines="))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())
		})

		It("expands storage by increasing spec.storage.size (if supported)", Label(
			"case:cluster-lifecycle-expands-storage-by-increasing-spec-storage-212f934d",
		), func() {
			pvcName := fmt.Sprintf("data-%s-0", clusterName)
			podName := fmt.Sprintf("%s-0", clusterName)

			By("waiting for the data PVC to exist")
			pvc := &corev1.PersistentVolumeClaim{}
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: f.Namespace}, pvc)).To(Succeed())
				g.Expect(pvc.Spec.Resources.Requests).NotTo(BeNil())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			scName := ""
			if pvc.Spec.StorageClassName != nil {
				scName = *pvc.Spec.StorageClassName
			} else {
				var scList storagev1.StorageClassList
				Expect(c.List(ctx, &scList)).To(Succeed())
				for i := range scList.Items {
					sc := &scList.Items[i]
					if sc.Annotations != nil && sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
						scName = sc.Name
						break
					}
				}
			}

			if scName == "" {
				Skip("no StorageClass found for data PVC; cannot validate expansion support")
			}

			sc := &storagev1.StorageClass{}
			Expect(c.Get(ctx, types.NamespacedName{Name: scName}, sc)).To(Succeed())
			if sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
				Skip(fmt.Sprintf("StorageClass %q does not support volume expansion (allowVolumeExpansion=false)", scName))
			}

			By("capturing the current pod UID (to detect potential restarts)")
			pod := &corev1.Pod{}
			Expect(c.Get(ctx, types.NamespacedName{Name: podName, Namespace: f.Namespace}, pod)).To(Succeed())
			oldUID := pod.UID

			By("updating OpenBaoCluster spec.storage.size from 1Gi to 2Gi")
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cluster := &openbaov1alpha1.OpenBaoCluster{}
				if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster); err != nil {
					return err
				}
				cluster.Spec.Storage.Size = "2Gi"
				if cluster.Spec.Maintenance == nil {
					cluster.Spec.Maintenance = &openbaov1alpha1.MaintenanceConfig{Enabled: true}
				} else {
					cluster.Spec.Maintenance.Enabled = true
				}
				return c.Update(ctx, cluster)
			})).To(Succeed())

			By("waiting for the PVC storage request to be updated by the operator")
			sawFSResizePending := false
			Eventually(func(g Gomega) {
				updated := &corev1.PersistentVolumeClaim{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("2Gi")))
				for _, cond := range updated.Status.Conditions {
					if cond.Type == corev1.PersistentVolumeClaimFileSystemResizePending && cond.Status == corev1.ConditionTrue {
						sawFSResizePending = true
					}
				}
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("ensuring the cluster remains Available")
			f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

			if sawFSResizePending {
				By("waiting for the pod to restart OR the filesystem resize to complete")
				Eventually(func(g Gomega) bool {
					// Check if pod restarted
					updatedPod := &corev1.Pod{}
					if err := c.Get(ctx, types.NamespacedName{Name: podName, Namespace: f.Namespace}, updatedPod); err == nil {
						if updatedPod.UID != oldUID {
							return true
						}
					}

					// Check if PVC capacity is updated (online resize)
					updatedPVC := &corev1.PersistentVolumeClaim{}
					if err := c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: f.Namespace}, updatedPVC); err == nil {
						qty := updatedPVC.Status.Capacity[corev1.ResourceStorage]
						if qty.Cmp(resource.MustParse("2Gi")) >= 0 {
							return true
						}
					}
					return false
				}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(BeTrue())
			}
		})
	})

	Context("Development Profile: Manual Init (Self-Init Disabled)", Label("profile-development"), func() {
		var (
			f *framework.Framework
			c client.Client
		)

		const (
			clusterName = "basic-cluster"
		)

		BeforeAll(func() {
			var err error
			f, err = framework.NewSetup(ctx, "basic", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			c = f.Client
		})

		AfterAll(func() {
			if f == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			_ = f.Cleanup(cleanupCtx)
		})

		It("creates a cluster with self-init disabled and produces expected Secrets", Label(
			"case:cluster-lifecycle-creates-a-cluster-with-self-init-19ac290f",
		), func() {
			By(fmt.Sprintf("creating OpenBaoCluster %q", clusterName))
			cluster, err := f.CreateDevelopmentCluster(ctx, framework.DevelopmentClusterConfig{
				Name:                 clusterName,
				Replicas:             3,
				Version:              openBaoVersion,
				Image:                openBaoImage,
				ConfigInitImg:        configInitImage,
				APIServerCIDR:        apiServerCIDR,
				APIServerEndpointIPs: apiServerEndpointIPs,
			})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = c.Delete(ctx, cluster)
			})

			By("waiting for Secrets to be created")
			Eventually(func() error {
				return c.Get(ctx, types.NamespacedName{Name: clusterName + "-tls-ca", Namespace: f.Namespace}, &corev1.Secret{})
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed(), "TLS CA Secret missing")

			Eventually(func() error {
				return c.Get(ctx, types.NamespacedName{Name: clusterName + "-tls-server", Namespace: f.Namespace}, &corev1.Secret{})
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed(), "TLS Server Secret missing")

			Eventually(func() error {
				return c.Get(ctx, types.NamespacedName{Name: clusterName + "-unseal-key", Namespace: f.Namespace}, &corev1.Secret{})
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed(), "Unseal Key Secret missing")

			By("waiting for root token Secret (self-init disabled)")
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, types.NamespacedName{
					Name:      clusterName + "-root-token",
					Namespace: f.Namespace,
				}, &corev1.Secret{})).To(Succeed())
			}, 10*time.Minute, 3*time.Second).Should(Succeed(), "Root Token Secret missing")
		})
	})

	Context("Development Profile: Scaling with Autopilot Reconciliation", Label(
		"profile-development",
		"scaling",
		"autopilot",
		"smoke",
	), func() {
		var (
			f   *framework.Framework
			c   client.Client
			cfg *rest.Config
		)

		const (
			clusterName = "scaling-cluster"
		)

		BeforeAll(func() {
			var err error
			f, err = framework.NewSetup(ctx, "scaling", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			c = f.Client

			// Get rest config for helper functions
			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if f == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			_ = f.Cleanup(cleanupCtx)
		})

		verifyClusterServesJWTAuthenticatedKV := func(secretPath, expectedValue string) {
			verifierLabels := map[string]string{"role": "test-verifier"}

			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, clusterName)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(e2ehelpers.WriteSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					openBaoImage,
					baoAddr,
					"default",
					"e2e-test",
					secretPath,
					verifierLabels,
					map[string]string{"foo": expectedValue},
				)).To(Succeed())

				value, err := e2ehelpers.ReadSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					openBaoImage,
					baoAddr,
					"default",
					"e2e-test",
					secretPath,
					verifierLabels,
					"foo",
				)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(value).To(Equal(expectedValue))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())
		}

		It("creates a cluster with 1 replica and verifies autopilot min_quorum=1", Label(
			"case:cluster-lifecycle-creates-a-cluster-with-1-replica-9792a98c",
		), func() {
			By(fmt.Sprintf("creating OpenBaoCluster %q with 1 replica", clusterName))
			requests := append(
				append([]openbaov1alpha1.SelfInitRequest{}, e2ehelpers.CreateAutopilotVerificationRequests(f.Namespace)...),
				e2ehelpers.CreateE2ERequests(f.Namespace)...,
			)
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: f.Namespace,
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
						Requests: requests,
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
						IngressRules: []networkingv1.NetworkPolicyIngressRule{
							{
								From: []networkingv1.NetworkPolicyPeer{
									{
										PodSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"role": "test-verifier",
											},
										},
									},
								},
								Ports: []networkingv1.NetworkPolicyPort{
									{
										Protocol: &[]corev1.Protocol{corev1.ProtocolTCP}[0],
										Port:     &[]intstr.IntOrString{intstr.FromInt(8200)}[0],
									},
								},
							},
						},
					},
					// Establish a stable ClusterIP Service for verification (DNS is more reliable than Headless in Kind)
					Service: &openbaov1alpha1.ServiceConfig{
						Type: "ClusterIP",
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}

			Expect(c.Create(ctx, cluster)).To(Succeed())

			By("waiting for StatefulSet to be ready with 1 replica")
			_, err := f.WaitForStatefulSetReady(
				ctx,
				clusterName,
				1,
				framework.DefaultLongWaitTimeout,
				framework.DefaultPollInterval,
			)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for Available condition")
			f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

			By("waiting for SelfInit to complete (SelfInitialized status)")
			// Wait for SelfInit requests to complete so JWT role is created
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{
					Name:      clusterName,
					Namespace: f.Namespace,
				}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue(), "cluster should be initialized")
				g.Expect(updated.Status.SelfInitialized).To(BeTrue(), "SelfInit requests should be completed")
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("ensuring public service exists for autopilot verification")
			svc := &corev1.Service{}
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, types.NamespacedName{
					Name:      clusterName + "-public",
					Namespace: f.Namespace,
				}, svc)).To(Succeed(), "public service should exist")
				g.Expect(svc.Spec.ClusterIP).NotTo(BeEmpty())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting a bit for autopilot config to be reconciled after initialization")
			// Give the operator time to reconcile autopilot config after cluster initialization
			time.Sleep(5 * time.Second)

			By("verifying Raft Autopilot min_quorum=1 (Development profile with 1 replica)")
			// Use Eventually to retry verification in case autopilot config hasn't been set yet
			Eventually(func() error {
				return e2ehelpers.VerifyRaftAutopilotMinQuorumViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					openBaoImage,
					"https://"+net.JoinHostPort(svc.Spec.ClusterIP, "8200"),
					"default",
					map[string]string{"role": "test-verifier"},
					1, // Expected min_quorum for Development profile with 1 replica
				)
			}, 2*time.Minute, 5*time.Second).Should(
				Succeed(),
				"Autopilot min_quorum should be 1 for Development profile with 1 replica",
			)
			_, _ = fmt.Fprintf(GinkgoWriter, "✓ Raft Autopilot min_quorum=1 verified\n")
		})

		It("scales up to 3 replicas and verifies autopilot min_quorum=3", Label(
			"case:cluster-lifecycle-scales-up-to-3-replicas-and-9bd63a38",
		), func() {

			By("updating cluster to 3 replicas")
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cluster := &openbaov1alpha1.OpenBaoCluster{}
				if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster); err != nil {
					return err
				}
				cluster.Spec.Replicas = 3
				return c.Update(ctx, cluster)
			})).To(Succeed())

			By("waiting for StatefulSet to scale to 3 replicas")
			_, err := f.WaitForStatefulSetReady(
				ctx,
				clusterName,
				3,
				framework.DefaultLongWaitTimeout,
				framework.DefaultPollInterval,
			)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for all pods to be ready")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
				g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(3)))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("getting public service for autopilot verification")
			svc := &corev1.Service{}
			Expect(c.Get(ctx, types.NamespacedName{
				Name:      clusterName + "-public",
				Namespace: f.Namespace,
			}, svc)).To(Succeed())

			By("triggering reconcile after scale up so autopilot settings are refreshed promptly")
			Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())

			By("verifying Raft Autopilot min_quorum=3 (Development profile with 3 replicas)")
			// Wait a bit for autopilot config to be reconciled after scaling
			Eventually(func() error {
				return e2ehelpers.VerifyRaftAutopilotMinQuorumViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					openBaoImage,
					"https://"+net.JoinHostPort(svc.Spec.ClusterIP, "8200"),
					"default",
					map[string]string{"role": "test-verifier"},
					3, // Expected min_quorum for Development profile with 3 replicas
				)
			}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(
				Succeed(),
				"Autopilot min_quorum should be updated to 3 after scaling",
			)
			_, _ = fmt.Fprintf(GinkgoWriter, "✓ Raft Autopilot min_quorum=3 verified after scale up\n")
		})

		It("scales down to 1 replica, remains responsive, and verifies autopilot min_quorum=1", Label(
			"case:cluster-lifecycle-scales-down-to-1-replica-remains-f03c312f",
		), func() {

			By("updating cluster to 1 replica")
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cluster := &openbaov1alpha1.OpenBaoCluster{}
				if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster); err != nil {
					return err
				}
				cluster.Spec.Replicas = 1
				return c.Update(ctx, cluster)
			})).To(Succeed())

			By("waiting for StatefulSet to scale down to 1 replica")
			_, err := f.WaitForStatefulSetReady(
				ctx,
				clusterName,
				1,
				framework.DefaultLongWaitTimeout,
				framework.DefaultPollInterval,
			)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the remaining pod to be ready")
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
				g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting for Available condition after scale down")
			f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

			By("getting public service for autopilot verification")
			svc := &corev1.Service{}
			Expect(c.Get(ctx, types.NamespacedName{
				Name:      clusterName + "-public",
				Namespace: f.Namespace,
			}, svc)).To(Succeed())

			By("triggering reconcile after scale down so autopilot settings are refreshed promptly")
			Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())

			By("verifying Raft Autopilot min_quorum=1 (Development profile with 1 replica)")
			// Wait a bit for autopilot config to be reconciled after scaling
			Eventually(func() error {
				return e2ehelpers.VerifyRaftAutopilotMinQuorumViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					openBaoImage,
					"https://"+net.JoinHostPort(svc.Spec.ClusterIP, "8200"),
					"default",
					map[string]string{"role": "test-verifier"},
					1, // Expected min_quorum for Development profile with 1 replica
				)
			}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(
				Succeed(),
				"Autopilot min_quorum should be updated to 1 after scale down",
			)
			_, _ = fmt.Fprintf(GinkgoWriter, "✓ Raft Autopilot min_quorum=1 verified after scale down\n")

			By("verifying the remaining cluster still serves JWT-authenticated KV traffic")
			verifyClusterServesJWTAuthenticatedKV(
				fmt.Sprintf("secret/%s-scaledown-smoke", clusterName),
				"still-responsive-after-3-to-1",
			)
		})
	})
})

func readAuditRecordsFromPVC(
	ctx context.Context,
	cfg *rest.Config,
	c client.Client,
	namespace string,
	image string,
	claimName string,
) (string, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("audit-reader-%d", time.Now().UnixNano()),
			Namespace: namespace,
			Labels: map[string]string{
				"role": "test-verifier",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			AutomountServiceAccountToken:  ptr.To(false),
			TerminationGracePeriodSeconds: ptr.To(int64(0)),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true),
				RunAsUser:    ptr.To(platformconstants.UserOpenBao),
				RunAsGroup:   ptr.To(platformconstants.GroupOpenBao),
				FSGroup:      ptr.To(platformconstants.GroupOpenBao),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "audit-storage",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: claimName,
							ReadOnly:  true,
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:    "reader",
					Image:   image,
					Command: []string{"/bin/sh", "-ec"},
					Args: []string{`
total=0
found=0
for file in /audit/*/audit.jsonl; do
  [ -f "$file" ] || continue
  found=1
  lines=$(wc -l < "$file" | tr -d ' ')
  echo "$file lines=$lines"
  total=$((total + lines))
done
echo "total_lines=$total"
[ "$found" -eq 1 ]
[ "$total" -gt 0 ]
`},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "audit-storage",
							MountPath: "/audit",
							ReadOnly:  true,
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot:           ptr.To(true),
						ReadOnlyRootFilesystem: ptr.To(true),
						Privileged:             ptr.To(false),
						RunAsUser:              ptr.To(platformconstants.UserOpenBao),
						RunAsGroup:             ptr.To(platformconstants.GroupOpenBao),
					},
				},
			},
		},
	}

	result, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, c, pod, time.Minute)
	_ = e2ehelpers.DeletePodBestEffort(ctx, c, namespace, pod.Name)
	if err != nil {
		return "", err
	}
	if result.Phase != corev1.PodSucceeded {
		return result.Logs, fmt.Errorf("audit reader pod failed, logs:\n%s", result.Logs)
	}
	return result.Logs, nil
}
