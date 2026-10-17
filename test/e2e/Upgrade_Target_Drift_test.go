//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Upgrade Strategies: Blue/Green Drift", Label("upgrade", "bluegreen", "slow"), Ordered, func() {
	ctx := context.Background()

	var (
		tenantNamespace string
		tenantFW        *framework.Framework
		driftCluster    *openbaov1alpha1.OpenBaoCluster
		initialVersion  string
		initialImage    string
		targetVersion   string
		targetImage     string
		driftImage      string
		admin           client.Client
	)

	BeforeAll(func() {
		var err error

		tenantFW, err = framework.NewSetup(ctx, "tenant-bluegreen-drift", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		tenantNamespace = tenantFW.Namespace
		admin = tenantFW.Client

		initialVersion = blueGreenUpgradeFromVersion()
		targetVersion = blueGreenUpgradeToVersion()
		initialImage = fmt.Sprintf("openbao/openbao:%s", initialVersion)
		targetImage = fmt.Sprintf("openbao/openbao:%s", targetVersion)
		driftImage = fmt.Sprintf("ghcr.io/openbao/openbao:%s", targetVersion)

		if initialVersion == targetVersion {
			Skip(fmt.Sprintf("Blue/green drift test skipped: versions identical (%s)", initialVersion))
		}

		driftCluster = &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bluegreen-drift-cluster",
				Namespace: tenantNamespace,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile:  openbaov1alpha1.ProfileDevelopment,
				Version:  initialVersion,
				Image:    initialImage,
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
				Upgrade: &openbaov1alpha1.UpgradeConfig{
					Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
					Image:    upgradeExecutorImage,
					BlueGreen: &openbaov1alpha1.BlueGreenConfig{
						AutoPromote: true,
						Verification: &openbaov1alpha1.VerificationConfig{
							// Keep both targets before promotion while checking abort cleanup.
							MinSyncDuration: "5m",
						},
					},
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		Expect(admin.Create(ctx, driftCluster)).To(Succeed())

		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: driftCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
			g.Expect(updated.Status.Initialized).To(BeTrue())
			g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))

			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))

			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		Eventually(func(g Gomega) {
			stsList := &appsv1.StatefulSetList{}
			g.Expect(admin.List(ctx, stsList,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster: driftCluster.Name,
				},
			)).To(Succeed())
			g.Expect(stsList.Items).NotTo(BeEmpty())

			var totalReady int32
			for i := range stsList.Items {
				totalReady += stsList.Items[i].Status.ReadyReplicas
			}
			g.Expect(totalReady).To(Equal(driftCluster.Spec.Replicas))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})

	AfterAll(func() {
		if tenantFW != nil {
			cleanupUpgradeFixture(ctx, tenantFW)
		}
	})

	It("abandons an outdated green revision without rolling back", Label(
		"case:bluegreen-target-drift-restart",
		"e2e-anchor",
		"covers:bluegreen-drift",
		"covers:target-revision-drift",
		"covers:stale-green-cleanup",
	), func() {
		clusterKey := types.NamespacedName{Name: driftCluster.Name, Namespace: tenantNamespace}
		var staleGreenRevision string
		staleGreenClaims := make(map[string]types.UID)

		By("recording Blue ownership and data before the aborted upgrade")
		beforeUpgrade := &openbaov1alpha1.OpenBaoCluster{}
		Expect(admin.Get(ctx, clusterKey, beforeUpgrade)).To(Succeed())
		blueName := upgrade.StableVoterStatefulSetName(beforeUpgrade)
		originalBlue := &appsv1.StatefulSet{}
		Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: blueName}, originalBlue)).To(Succeed())
		Expect(resourceownership.HasManagedControllerOwnerProof(originalBlue, beforeUpgrade,
			openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))).To(BeTrue())
		blueClaims := make(map[string]types.UID)
		for ordinal := int32(0); ordinal < beforeUpgrade.Spec.Replicas; ordinal++ {
			name := fmt.Sprintf("data-%s-%d", blueName, ordinal)
			claim := &corev1.PersistentVolumeClaim{}
			Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
			blueClaims[name] = claim.UID
		}

		By("starting a blue/green upgrade")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Version = targetVersion
			updated.Spec.Image = targetImage
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		Expect(tenantFW.TriggerReconcile(ctx, driftCluster.Name)).To(Succeed())

		By("waiting for the first green revision to enter an early phase")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
			g.Expect(updated.Status.BlueGreen.Phase).To(Or(
				Equal(openbaov1alpha1.PhaseDeployingGreen),
				Equal(openbaov1alpha1.PhaseJoiningMesh),
				Equal(openbaov1alpha1.PhaseSyncing),
			))
			revision := updated.Status.BlueGreen.GreenRevision
			green := &appsv1.StatefulSet{}
			g.Expect(admin.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", driftCluster.Name, revision),
				Namespace: tenantNamespace,
			}, green)).To(Succeed())
			g.Expect(resourceownership.HasManagedControllerOwnerProof(green, updated,
				openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))).To(BeTrue())
			g.Expect(green.Spec.Template.Labels[constants.LabelOpenBaoRevision]).To(Equal(revision))
			for ordinal := int32(0); ordinal < updated.Spec.Replicas; ordinal++ {
				name := fmt.Sprintf("data-%s-%d", green.Name, ordinal)
				claim := &corev1.PersistentVolumeClaim{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
				staleGreenClaims[name] = claim.UID
			}
			staleGreenRevision = revision
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())

		By("changing the desired target image while the first green revision is still in flight")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Image = driftImage
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		Expect(tenantFW.TriggerReconcile(ctx, driftCluster.Name)).To(Succeed())

		By("verifying the stale green workload is cleaned up")
		Eventually(func() bool {
			err := admin.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", driftCluster.Name, staleGreenRevision),
				Namespace: tenantNamespace,
			}, &appsv1.StatefulSet{})
			return apierrors.IsNotFound(err)
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(BeTrue())

		By("verifying abort preserves Blue and both revisions' recorded data claims")
		preservedBlue := &appsv1.StatefulSet{}
		Expect(admin.Get(ctx, client.ObjectKeyFromObject(originalBlue), preservedBlue)).To(Succeed())
		Expect(preservedBlue.UID).To(Equal(originalBlue.UID))
		Expect(preservedBlue.DeletionTimestamp).To(BeNil())
		for _, claims := range []map[string]types.UID{blueClaims, staleGreenClaims} {
			for name, uid := range claims {
				claim := &corev1.PersistentVolumeClaim{}
				Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
				Expect(claim.UID).To(Equal(uid))
				Expect(claim.DeletionTimestamp).To(BeNil())
			}
		}

		By("verifying the stale target is abandoned before any rollback")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(Equal(staleGreenRevision))
			g.Expect(updated.Status.BlueGreen.BlueRevision).NotTo(Equal(staleGreenRevision))
			g.Expect(updated.Status.BlueGreen.RollbackStartTime).To(BeNil(), "early-phase drift should abort and restart, not enter rollback")
			g.Expect(updated.Status.BreakGlass).To(BeNil())
			if updated.Status.BlueGreen.Phase == openbaov1alpha1.PhaseIdle {
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())
				g.Expect(updated.Status.OperationLock).To(BeNil())
			}
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("verifying no stale workload remains in the namespace")
		Eventually(func(g Gomega) {
			stsList := &appsv1.StatefulSetList{}
			g.Expect(admin.List(ctx, stsList,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster: driftCluster.Name,
				},
			)).To(Succeed())
			for _, sts := range stsList.Items {
				g.Expect(sts.Name).NotTo(Equal(fmt.Sprintf("%s-%s", driftCluster.Name, staleGreenRevision)))
			}
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})
})
