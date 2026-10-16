//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const strategySwitchUpgradeRole = "strategy-switch-upgrade"

const strategySwitchUpgradePolicy = `path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }
path "sys/storage/raft/join" { capabilities = ["update"] }
path "sys/storage/raft/configuration" { capabilities = ["read", "update"] }
path "sys/storage/raft/remove-peer" { capabilities = ["update"] }
path "sys/storage/raft/promote" { capabilities = ["update"] }
path "sys/storage/raft/demote" { capabilities = ["update"] }`

var _ = Describe("Upgrade Strategy Switching", Label("upgrade", "rolling", "bluegreen", "slow"), Ordered, func() {
	ctx := context.Background()

	var (
		tenantNamespace string
		tenantFW        *framework.Framework
		cluster         *openbaov1alpha1.OpenBaoCluster
		admin           client.Client
	)

	BeforeAll(func() {
		var err error
		tenantFW, err = framework.NewSetup(ctx, "tenant-strategy-switch", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		tenantNamespace = tenantFW.Namespace
		admin = tenantFW.Client

		selfInitRequests := append([]openbaov1alpha1.SelfInitRequest{}, e2ehelpers.CreateE2ERequests(tenantNamespace)...)
		selfInitRequests = append(selfInitRequests, e2ehelpers.CreateJWTPolicyRoleRequests(
			tenantNamespace,
			"strategy-switch-cluster-upgrade-serviceaccount",
			strategySwitchUpgradeRole,
			strategySwitchUpgradeRole,
			strategySwitchUpgradePolicy,
		)...)

		cluster = &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "strategy-switch-cluster",
				Namespace: tenantNamespace,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile:  openbaov1alpha1.ProfileDevelopment,
				Version:  defaultBlueGreenUpgradeFromVersion,
				Image:    fmt.Sprintf("openbao/openbao:%s", defaultBlueGreenUpgradeFromVersion),
				Replicas: 3,
				InitContainer: &openbaov1alpha1.InitContainerConfig{
					Enabled: true,
					Image:   configInitImage,
				},
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: true,
					OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
						Enabled: true,
					},
					Requests: selfInitRequests,
				},
				TLS: openbaov1alpha1.TLSConfig{
					Enabled:        true,
					Mode:           openbaov1alpha1.TLSModeOperatorManaged,
					RotationPeriod: "720h",
				},
				Storage: openbaov1alpha1.StorageConfig{Size: "1Gi"},
				Network: &openbaov1alpha1.NetworkConfig{
					APIServerCIDR:        apiServerCIDR,
					APIServerEndpointIPs: apiServerEndpointIPs,
					APIServerEgress:      framework.APIServerEgressForNetworkProfile(),
				},
				Upgrade: &openbaov1alpha1.UpgradeConfig{
					Strategy:    openbaov1alpha1.UpdateStrategyRollingUpdate,
					Image:       upgradeExecutorImage,
					JWTAuthRole: strategySwitchUpgradeRole,
					BlueGreen: &openbaov1alpha1.BlueGreenConfig{
						AutoPromote: true,
						Verification: &openbaov1alpha1.VerificationConfig{
							MinSyncDuration: "15s",
						},
					},
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		Expect(admin.Create(ctx, cluster)).To(Succeed())

		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
			g.Expect(updated.Status.Initialized).To(BeTrue())
			g.Expect(updated.Status.CurrentVersion).To(Equal(defaultBlueGreenUpgradeFromVersion))
			g.Expect(updated.Status.AcceptedUpgradeStrategy).To(Equal(openbaov1alpha1.UpdateStrategyRollingUpdate))
			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})

	AfterAll(func() {
		if tenantFW != nil {
			cleanupUpgradeFixture(ctx, tenantFW)
		}
	})

	It("switches both directions at idle and preserves the active workload", Label(
		"case:idle-upgrade-strategy-switch",
		"covers:upgrade-strategy-switch",
		"covers:stable-workload-identity",
	), func() {
		key := types.NamespacedName{Name: cluster.Name, Namespace: tenantNamespace}

		By("recording the initial rolling StatefulSet identity")
		initialSTS := &appsv1.StatefulSet{}
		Expect(admin.Get(ctx, key, initialSTS)).To(Succeed())
		initialUID := initialSTS.UID

		By("switching only the idle strategy from RollingUpdate to BlueGreen")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Upgrade.Strategy = openbaov1alpha1.UpdateStrategyBlueGreen
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			g.Expect(updated.Status.AcceptedUpgradeStrategy).To(Equal(openbaov1alpha1.UpdateStrategyBlueGreen))
			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			g.Expect(updated.Status.BlueGreen.BlueRevision).To(BeEmpty())
			g.Expect(updated.Status.BlueGreen.BlueControllerRevision).NotTo(BeEmpty())

			stable := &appsv1.StatefulSet{}
			g.Expect(admin.Get(ctx, key, stable)).To(Succeed())
			g.Expect(stable.UID).To(Equal(initialUID))
			g.Expect(stable.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("performing a blue-green upgrade from 2.4.4 to 2.5.5")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Version = defaultBlueGreenUpgradeToVersion
			updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", defaultBlueGreenUpgradeToVersion)
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		var promotedStatefulSetName string
		var promotedStatefulSetUID types.UID
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			g.Expect(updated.Status.CurrentVersion).To(Equal(defaultBlueGreenUpgradeToVersion))
			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			g.Expect(updated.Status.BlueGreen.BlueRevision).NotTo(BeEmpty())

			promotedStatefulSetName = fmt.Sprintf("%s-%s", cluster.Name, updated.Status.BlueGreen.BlueRevision)
			stable := &appsv1.StatefulSet{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: promotedStatefulSetName, Namespace: tenantNamespace}, stable)).To(Succeed())
			g.Expect(stable.Status.ReadyReplicas).To(Equal(cluster.Spec.Replicas))
			promotedStatefulSetUID = stable.UID
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("switching only the idle strategy from BlueGreen to RollingUpdate")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Upgrade.Strategy = openbaov1alpha1.UpdateStrategyRollingUpdate
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			g.Expect(updated.Status.AcceptedUpgradeStrategy).To(Equal(openbaov1alpha1.UpdateStrategyRollingUpdate))

			stable := &appsv1.StatefulSet{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: promotedStatefulSetName, Namespace: tenantNamespace}, stable)).To(Succeed())
			g.Expect(stable.UID).To(Equal(promotedStatefulSetUID))
			g.Expect(stable.Spec.UpdateStrategy.Type).To(Equal(appsv1.RollingUpdateStatefulSetStrategyType))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("performing a rolling upgrade from 2.5.5 to 2.6.4 against the same StatefulSet")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Version = defaultUpgradeToVersion
			updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", defaultUpgradeToVersion)
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, key, updated)).To(Succeed())
			g.Expect(updated.Status.CurrentVersion).To(Equal(defaultUpgradeToVersion))
			g.Expect(updated.Status.Upgrade).To(BeNil())

			stable := &appsv1.StatefulSet{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: promotedStatefulSetName, Namespace: tenantNamespace}, stable)).To(Succeed())
			g.Expect(stable.UID).To(Equal(promotedStatefulSetUID))

			pods := &corev1.PodList{}
			g.Expect(admin.List(ctx, pods,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster:      cluster.Name,
					constants.LabelOpenBaoWorkloadPool: constants.LabelValueOpenBaoWorkloadPoolVoter,
				},
			)).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(int(cluster.Spec.Replicas)))
			for _, pod := range pods.Items {
				e2ehelpers.ExpectOpenBaoPodVersion(g, pod, defaultUpgradeToVersion)
			}
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})
})
