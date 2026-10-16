//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const (
	e2eGatewayControllerName = gatewayv1.GatewayController("openbao.org/e2e-gateway")
	gatewayPhasePollInterval = 250 * time.Millisecond
)

func expectGatewayUpgradeSourceReady(g Gomega, cluster *openbaov1alpha1.OpenBaoCluster, version string) {
	g.Expect(cluster.Status.Initialized).To(BeTrue())
	g.Expect(cluster.Status.CurrentVersion).To(Equal(version), "the source version must be established before requesting an upgrade")
	g.Expect(cluster.Status.ReadyReplicas).To(Equal(cluster.Spec.Replicas))
	g.Expect(cluster.Status.BlueGreen).NotTo(BeNil())
	g.Expect(cluster.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
	g.Expect(cluster.Status.BlueGreen.BlueRevision).NotTo(BeEmpty())
	g.Expect(cluster.Status.BlueGreen.GreenRevision).To(BeEmpty())
	available := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
	g.Expect(available).NotTo(BeNil())
	g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
}

func ensureGatewayClassReady(
	ctx context.Context,
	c client.Client,
	name string,
	features ...gatewayv1.FeatureName,
) {
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: e2eGatewayControllerName,
		},
	}
	Expect(c.Create(ctx, gatewayClass)).To(Succeed())
	DeferCleanup(func() {
		_ = c.Delete(context.Background(), &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})

	Eventually(func(g Gomega) {
		current := &gatewayv1.GatewayClass{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: name}, current)).To(Succeed())

		original := current.DeepCopy()
		current.Status.Conditions = []metav1.Condition{
			{
				Type:               string(gatewayv1.GatewayClassConditionStatusAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.GatewayClassReasonAccepted),
				ObservedGeneration: current.Generation,
				LastTransitionTime: metav1.Now(),
			},
			{
				Type:               string(gatewayv1.GatewayClassConditionStatusSupportedVersion),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.GatewayClassReasonSupportedVersion),
				ObservedGeneration: current.Generation,
				LastTransitionTime: metav1.Now(),
			},
		}
		current.Status.SupportedFeatures = make([]gatewayv1.SupportedFeature, 0, len(features))
		for _, feature := range features {
			current.Status.SupportedFeatures = append(current.Status.SupportedFeatures, gatewayv1.SupportedFeature{Name: feature})
		}

		g.Expect(c.Status().Patch(ctx, current, client.MergeFrom(original))).To(Succeed())
	}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
}

func markTLSRouteReady(
	ctx context.Context,
	c client.Client,
	namespace, name string,
) {
	Eventually(func(g Gomega) {
		route := &gatewayv1.TLSRoute{}
		g.Expect(c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, route)).To(Succeed())
		g.Expect(route.Spec.ParentRefs).To(HaveLen(1))

		original := route.DeepCopy()
		route.Status.Parents = []gatewayv1.RouteParentStatus{
			{
				ParentRef:      route.Spec.ParentRefs[0],
				ControllerName: e2eGatewayControllerName,
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonAccepted),
						ObservedGeneration: route.Generation,
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               string(gatewayv1.RouteConditionResolvedRefs),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonResolvedRefs),
						ObservedGeneration: route.Generation,
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}

		g.Expect(c.Status().Patch(ctx, route, client.MergeFrom(original))).To(Succeed())
	}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
}

func markGatewayProgrammed(
	ctx context.Context,
	c client.Client,
	namespace, name string,
) {
	Eventually(func(g Gomega) {
		gateway := &gatewayv1.Gateway{}
		g.Expect(c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, gateway)).To(Succeed())

		original := gateway.DeepCopy()
		gateway.Status.Conditions = []metav1.Condition{
			{
				Type:               string(gatewayv1.GatewayConditionProgrammed),
				Status:             metav1.ConditionTrue,
				Reason:             "Programmed",
				ObservedGeneration: gateway.Generation,
				LastTransitionTime: metav1.Now(),
			},
		}

		g.Expect(c.Status().Patch(ctx, gateway, client.MergeFrom(original))).To(Succeed())
	}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
}

// Gateway API CRDs and GatewayClasses are shared cluster resources.
var _ = Describe("Upgrade Strategies: Gateway API", Label("upgrade", "upgrades", "cluster", "slow"), Serial, func() {
	ctx := context.Background()

	// --- Gateway Integration ---
	Context("Gateway API", Label("gateway", "requires-gateway-api"), Ordered, func() {
		var (
			gatewayAPIFW      *framework.Framework
			gatewayAPICleanup func()
		)

		BeforeAll(func() {
			var err error
			gatewayAPIFW, err = framework.NewSetup(ctx, "tenant-gateway-api", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())

			gatewayAPICleanup, err = gatewayAPIFW.RequireGatewayAPI()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			if gatewayAPICleanup != nil {
				gatewayAPICleanup()
			}
			if gatewayAPIFW != nil {
				_ = gatewayAPIFW.Cleanup(ctx)
			}
		})

		Context("HTTPRoute Blue/Green Cutover", Label("bluegreen"), Ordered, func() {
			var (
				tenantNamespace  string
				tenantFW         *framework.Framework
				upgradeCluster   *openbaov1alpha1.OpenBaoCluster
				admin            client.Client
				gatewayClassName = "e2e-traefik-http"
			)

			BeforeAll(func() {
				var err error
				tenantFW, err = framework.NewSetup(ctx, "tenant-gateway", operatorNamespace)
				Expect(err).NotTo(HaveOccurred())
				tenantNamespace = tenantFW.Namespace
				admin = tenantFW.Client

				gw := &gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tenant-gateway",
						Namespace: tenantNamespace,
					},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
						Listeners: []gatewayv1.Listener{
							{
								Name:     "https",
								Port:     443,
								Protocol: gatewayv1.HTTPSProtocolType,
								Hostname: ptrTo(gatewayv1.Hostname("bao.example.local")),
							},
						},
					},
				}
				Expect(admin.Create(ctx, gw)).To(Succeed())
				ensureGatewayClassReady(ctx, admin, gatewayClassName, gatewayv1.FeatureName("HTTPRoute"))
				markGatewayProgrammed(ctx, admin, tenantNamespace, gw.Name)

				initialVersion := blueGreenUpgradeFromVersion()
				upgradeCluster = &openbaov1alpha1.OpenBaoCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-cluster",
						Namespace: tenantNamespace,
					},
					Spec: openbaov1alpha1.OpenBaoClusterSpec{
						Profile:  openbaov1alpha1.ProfileDevelopment,
						Version:  initialVersion,
						Image:    fmt.Sprintf("openbao/openbao:%s", initialVersion),
						Replicas: 3,
						Upgrade: &openbaov1alpha1.UpgradeConfig{
							Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
							Image:    upgradeExecutorImage,
							BlueGreen: &openbaov1alpha1.BlueGreenConfig{
								AutoPromote: true,
								Verification: &openbaov1alpha1.VerificationConfig{
									MinSyncDuration: "10s",
								},
							},
						},
						InitContainer: &openbaov1alpha1.InitContainerConfig{
							Enabled: true,
							Image:   configInitImage,
						},
						SelfInit: &openbaov1alpha1.SelfInitConfig{
							Enabled: true,
							OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
								Enabled: true,
							},
							Requests: e2ehelpers.CreateE2ERequests(tenantNamespace),
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
						Gateway: &openbaov1alpha1.GatewayConfig{
							Enabled: true,
							GatewayRef: openbaov1alpha1.GatewayReference{
								Name: "tenant-gateway",
							},
							Hostname: "bao.example.local",
						},
						DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
					},
				}
				Expect(admin.Create(ctx, upgradeCluster)).To(Succeed())

				Eventually(func(g Gomega) {
					updated := &openbaov1alpha1.OpenBaoCluster{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
					expectGatewayUpgradeSourceReady(g, updated, initialVersion)
				}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			})

			AfterAll(func() {
				if tenantFW != nil {
					_ = tenantFW.Cleanup(ctx)
				}
			})

			It("keeps HTTPRoute stable and switches external Service selector at cutover", Label(
				"e2e-anchor",
				"case:upgrade-gateway-httproute-cutover",
			), func() {
				targetVersion := blueGreenUpgradeToVersion()

				By("Capturing HTTPRoute before upgrade to verify stability")
				var httpRouteBeforeUpgrade *gatewayv1.HTTPRoute
				Eventually(func(g Gomega) {
					httpRoute := &gatewayv1.HTTPRoute{}
					g.Expect(admin.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-httproute", upgradeCluster.Name),
						Namespace: tenantNamespace,
					}, httpRoute)).To(Succeed())
					httpRouteBeforeUpgrade = httpRoute.DeepCopy()
				}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

				By("Triggering upgrade")
				Eventually(func(g Gomega) {
					updated := &openbaov1alpha1.OpenBaoCluster{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
					original := updated.DeepCopy()
					updated.Spec.Version = targetVersion
					updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)
					g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
				}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

				// Start immediately and poll briefly to observe the cutover phases.
				By("Verifying external Service remains on Blue while DemotingBlue is in progress")
				Eventually(func(g Gomega) {
					expectBlueGreenServiceSelectorDuringPhase(
						g,
						ctx,
						admin,
						tenantNamespace,
						upgradeCluster.Name,
						openbaov1alpha1.PhaseDemotingBlue,
						func(status *openbaov1alpha1.BlueGreenStatus) string {
							return status.BlueRevision
						},
						"GreenRevision should be set at cutover",
					)
				}, 20*time.Minute, gatewayPhasePollInterval).Should(Succeed())

				By("Waiting for Cleanup phase and verifying the external Service selector switches to Green")
				Eventually(func(g Gomega) {
					expectBlueGreenServiceSelectorDuringPhase(
						g,
						ctx,
						admin,
						tenantNamespace,
						upgradeCluster.Name,
						openbaov1alpha1.PhaseCleanup,
						func(status *openbaov1alpha1.BlueGreenStatus) string {
							return status.GreenRevision
						},
						"GreenRevision should be set during cleanup",
					)
				}, 20*time.Minute, gatewayPhasePollInterval).Should(Succeed())

				By("Waiting for Blue/Green upgrade to complete")
				Eventually(func(g Gomega) {
					updated := &openbaov1alpha1.OpenBaoCluster{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
					g.Expect(updated.Status.BlueGreen).NotTo(BeNil(), "BlueGreen status should be initialized")

					// Explicitly require PhaseIdle - this will retry until phase is Idle
					g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle), "upgrade should complete and return to Idle")
					g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
					g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())

					// Verify pods with the final Blue revision are healthy
					labelSelector := labels.SelectorFromSet(map[string]string{
						constants.LabelAppInstance:     upgradeCluster.Name,
						constants.LabelAppName:         constants.LabelValueAppNameOpenBao,
						constants.LabelOpenBaoRevision: updated.Status.BlueGreen.BlueRevision,
					})
					podList := &corev1.PodList{}
					g.Expect(admin.List(ctx, podList, client.InNamespace(tenantNamespace), client.MatchingLabelsSelector{Selector: labelSelector})).To(Succeed())
					g.Expect(podList.Items).To(HaveLen(3))

					for _, pod := range podList.Items {
						g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning))
					}
				}, 30*time.Minute, 30*time.Second).Should(Succeed())

				By("Verifying legacy blue/green Services do not exist")
				Eventually(func(g Gomega) {
					// Blue service should not exist
					blueSvc := &corev1.Service{}
					err := admin.Get(ctx, types.NamespacedName{
						Namespace: tenantNamespace,
						Name:      fmt.Sprintf("%s-public-blue", upgradeCluster.Name),
					}, blueSvc)
					g.Expect(err).To(HaveOccurred())
					g.Expect(client.IgnoreNotFound(err)).To(Succeed(), "blue service should be deleted")

					// Green service should not exist
					greenSvc := &corev1.Service{}
					err = admin.Get(ctx, types.NamespacedName{
						Namespace: tenantNamespace,
						Name:      fmt.Sprintf("%s-public-green", upgradeCluster.Name),
					}, greenSvc)
					g.Expect(err).To(HaveOccurred())
					g.Expect(client.IgnoreNotFound(err)).To(Succeed(), "green service should be deleted")

					// Main public service should still exist
					mainSvc := &corev1.Service{}
					g.Expect(admin.Get(ctx, types.NamespacedName{
						Namespace: tenantNamespace,
						Name:      fmt.Sprintf("%s-public", upgradeCluster.Name),
					}, mainSvc)).To(Succeed())
				}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

				By("Verifying HTTPRoute remains stable throughout upgrade")
				Eventually(func(g Gomega) {
					httpRouteAfterUpgrade := &gatewayv1.HTTPRoute{}
					g.Expect(admin.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-httproute", upgradeCluster.Name),
						Namespace: tenantNamespace,
					}, httpRouteAfterUpgrade)).To(Succeed())

					// Verify HTTPRoute spec remains unchanged
					g.Expect(httpRouteAfterUpgrade.Spec.ParentRefs).To(Equal(httpRouteBeforeUpgrade.Spec.ParentRefs),
						"HTTPRoute ParentRefs should remain unchanged")
					g.Expect(httpRouteAfterUpgrade.Spec.Hostnames).To(Equal(httpRouteBeforeUpgrade.Spec.Hostnames),
						"HTTPRoute Hostnames should remain unchanged")
					g.Expect(httpRouteAfterUpgrade.Spec.Rules).To(HaveLen(len(httpRouteBeforeUpgrade.Spec.Rules)),
						"HTTPRoute Rules count should remain unchanged")
					if len(httpRouteAfterUpgrade.Spec.Rules) > 0 && len(httpRouteBeforeUpgrade.Spec.Rules) > 0 {
						g.Expect(httpRouteAfterUpgrade.Spec.Rules[0].Matches).To(Equal(httpRouteBeforeUpgrade.Spec.Rules[0].Matches),
							"HTTPRoute Rules Matches should remain unchanged")
						// BackendRefs should point to the same Service (only Service selector changes, not the Service name)
						g.Expect(httpRouteAfterUpgrade.Spec.Rules[0].BackendRefs).To(
							HaveLen(len(httpRouteBeforeUpgrade.Spec.Rules[0].BackendRefs)),
							"HTTPRoute BackendRefs count should remain unchanged")
						if len(httpRouteAfterUpgrade.Spec.Rules[0].BackendRefs) > 0 && len(httpRouteBeforeUpgrade.Spec.Rules[0].BackendRefs) > 0 {
							g.Expect(httpRouteAfterUpgrade.Spec.Rules[0].BackendRefs[0].Name).To(Equal(httpRouteBeforeUpgrade.Spec.Rules[0].BackendRefs[0].Name),
								"HTTPRoute BackendRef Service name should remain unchanged")
							g.Expect(httpRouteAfterUpgrade.Spec.Rules[0].BackendRefs[0].Port).To(Equal(httpRouteBeforeUpgrade.Spec.Rules[0].BackendRefs[0].Port),
								"HTTPRoute BackendRef Port should remain unchanged")
						}
					}
				}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			})
		})

		Context("Gateway TLS Passthrough", Label("tls-passthrough"), Ordered, func() {
			var (
				tenantNamespace     string
				tenantFW            *framework.Framework
				admin               client.Client
				passthroughCluster  *openbaov1alpha1.OpenBaoCluster
				passthroughHostname = "bao-passthrough.example.local"
				passthroughListener = "tls-passthrough"
				passthroughGateway  = "tenant-gateway-passthrough"
				gatewayClassName    = "e2e-traefik-tls"
			)

			BeforeAll(func() {
				var err error

				tenantFW, err = framework.NewSetup(ctx, "tenant-gateway-tls", operatorNamespace)
				Expect(err).NotTo(HaveOccurred())
				tenantNamespace = tenantFW.Namespace
				admin = tenantFW.Client

				gw := &gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:      passthroughGateway,
						Namespace: tenantNamespace,
					},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
						Listeners: []gatewayv1.Listener{
							{
								Name:     gatewayv1.SectionName(passthroughListener),
								Port:     443,
								Protocol: gatewayv1.TLSProtocolType,
								Hostname: ptrTo(gatewayv1.Hostname(passthroughHostname)),
								TLS: &gatewayv1.ListenerTLSConfig{
									Mode: ptrTo(gatewayv1.TLSModePassthrough),
								},
							},
						},
					},
				}
				Expect(admin.Create(ctx, gw)).To(Succeed())
				ensureGatewayClassReady(ctx, admin, gatewayClassName, gatewayv1.FeatureName("TLSRoute"))
				markGatewayProgrammed(ctx, admin, tenantNamespace, gw.Name)

				passthroughCluster = &openbaov1alpha1.OpenBaoCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-passthrough-cluster",
						Namespace: tenantNamespace,
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
							Requests: e2ehelpers.CreateE2ERequests(tenantNamespace),
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
						Gateway: &openbaov1alpha1.GatewayConfig{
							Enabled:        true,
							TLSPassthrough: true,
							ListenerName:   passthroughListener,
							Hostname:       passthroughHostname,
							GatewayRef: openbaov1alpha1.GatewayReference{
								Name: passthroughGateway,
							},
						},
						DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
					},
				}
				Expect(admin.Create(ctx, passthroughCluster)).To(Succeed())
				markTLSRouteReady(
					ctx,
					admin,
					tenantNamespace,
					fmt.Sprintf("%s-tlsroute", passthroughCluster.Name),
				)

				Eventually(func(g Gomega) {
					updated := &openbaov1alpha1.OpenBaoCluster{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Name: passthroughCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
					g.Expect(updated.Status.Initialized).To(BeTrue())
					available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
					g.Expect(available).NotTo(BeNil())
					g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
				}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			})

			AfterAll(func() {
				if tenantFW != nil {
					_ = tenantFW.Cleanup(ctx)
				}
			})

			It("creates a TLSRoute and reports healthy passthrough integration", Label(
				"case:upgrade-strategies-creates-a-tlsroute-and-reports-healthy-79be45e7",
			), func() {
				By("waiting for passthrough Gateway integration status")
				Eventually(func(g Gomega) {
					updated := &openbaov1alpha1.OpenBaoCluster{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Name: passthroughCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

					cond := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionGatewayIntegrationReady))
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
					g.Expect(cond.Reason).To(Equal("GatewayIntegrationReady"))
				}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

				By("verifying a TLSRoute is created for passthrough access")
				Eventually(func(g Gomega) {
					tlsRoute := &gatewayv1.TLSRoute{}
					g.Expect(admin.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-tlsroute", passthroughCluster.Name),
						Namespace: tenantNamespace,
					}, tlsRoute)).To(Succeed())

					g.Expect(tlsRoute.Spec.ParentRefs).To(HaveLen(1))
					g.Expect(tlsRoute.Spec.ParentRefs[0].Name).To(Equal(gatewayv1.ObjectName(passthroughGateway)))
					g.Expect(tlsRoute.Spec.ParentRefs[0].SectionName).NotTo(BeNil())
					g.Expect(*tlsRoute.Spec.ParentRefs[0].SectionName).To(Equal(gatewayv1.SectionName(passthroughListener)))
					g.Expect(tlsRoute.Spec.Hostnames).To(Equal([]gatewayv1.Hostname{gatewayv1.Hostname(passthroughHostname)}))
					g.Expect(tlsRoute.Spec.Rules).To(HaveLen(1))
					g.Expect(tlsRoute.Spec.Rules[0].BackendRefs).To(HaveLen(1))
					g.Expect(tlsRoute.Spec.Rules[0].BackendRefs[0].Name).To(Equal(gatewayv1.ObjectName(fmt.Sprintf("%s-public", passthroughCluster.Name))))
					g.Expect(tlsRoute.Spec.Rules[0].BackendRefs[0].Port).NotTo(BeNil())
					g.Expect(*tlsRoute.Spec.Rules[0].BackendRefs[0].Port).To(Equal(gatewayv1.PortNumber(constants.PortAPI)))
				}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

				By("verifying BackendTLSPolicy is not created for passthrough mode")
				Consistently(func() bool {
					err := admin.Get(ctx, types.NamespacedName{
						Name:      fmt.Sprintf("%s-backend-tls", passthroughCluster.Name),
						Namespace: tenantNamespace,
					}, &gatewayv1.BackendTLSPolicy{})
					return apierrors.IsNotFound(err)
				}, 30*time.Second, framework.DefaultPollInterval).Should(BeTrue())
			})
		})
	})
})
