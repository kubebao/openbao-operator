//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

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
)

var _ = Describe("Manager Resilience", Label("manager", "cluster"), Serial, Ordered, func() {
	ctx := context.Background()

	var (
		f                          *framework.Framework
		c                          client.Client
		originalControllerReplicas int32
	)

	countDataPVCs := func(clusterName string) int {
		pvcs := &corev1.PersistentVolumeClaimList{}
		Expect(c.List(ctx, pvcs, client.InNamespace(f.Namespace))).To(Succeed())

		prefix := fmt.Sprintf("data-%s-", clusterName)
		count := 0
		for i := range pvcs.Items {
			if strings.HasPrefix(pvcs.Items[i].Name, prefix) {
				count++
			}
		}
		return count
	}

	countClusterStatefulSets := func(clusterName string) int {
		stsList := &appsv1.StatefulSetList{}
		Expect(c.List(ctx, stsList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.LabelOpenBaoCluster: clusterName,
			},
		)).To(Succeed())
		return len(stsList.Items)
	}

	BeforeAll(func() {
		var err error
		f, err = framework.NewSetup(ctx, "manager-resilience", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		c = f.Client

		deploy, err := getControllerDeployment(ctx, c, operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(deploy.Spec.Replicas).NotTo(BeNil())
		originalControllerReplicas = *deploy.Spec.Replicas
	})

	AfterEach(func() {
		Expect(scaleControllerDeployment(ctx, c, operatorNamespace, originalControllerReplicas)).To(Succeed())
		_, err := waitForReadyControllerPods(
			ctx,
			c,
			operatorNamespace,
			int(originalControllerReplicas),
			2*time.Minute,
		)
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

	It("recovers idempotently when the controller restarts during initial and scale reconciliation", Label(
		"e2e-anchor",
		"case:manager-restart-idempotent-reconcile",
		"covers:controller-restart",
		"covers:idempotent-reconcile",
		"covers:scale-reconcile",
	), func() {
		const clusterName = "manager-resilience-cluster"

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
		Expect(c.Create(ctx, cluster)).To(Succeed())

		By("restarting the controller while the initial reconcile is still in progress")
		Expect(restartControllerDeployment(ctx, c, operatorNamespace)).To(Succeed())

		By("verifying the cluster still converges to a single ready StatefulSet")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(updated.Status.CurrentVersion).To(Equal(openBaoVersion))
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())
		_, err := f.WaitForStatefulSetReady(
			ctx,
			clusterName,
			1,
			framework.DefaultLongWaitTimeout,
			framework.DefaultPollInterval,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(countClusterStatefulSets(clusterName)).To(Equal(1))
		Expect(countDataPVCs(clusterName)).To(Equal(1))

		sts, err := f.GetStatefulSet(ctx, clusterName)
		Expect(err).NotTo(HaveOccurred())
		originalUID := sts.UID

		By("scaling the cluster and restarting the controller during the reconcile")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Replicas = 2
			g.Expect(c.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		Expect(restartControllerDeployment(ctx, c, operatorNamespace)).To(Succeed())

		By("verifying the scale reconcile finishes without duplicating managed resources")
		_, err = f.WaitForStatefulSetReady(
			ctx,
			clusterName,
			2,
			framework.DefaultLongWaitTimeout,
			framework.DefaultPollInterval,
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			current := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, current)).To(Succeed())
			g.Expect(current.UID).To(
				Equal(originalUID),
				"controller restart should resume the same StatefulSet instead of recreating it",
			)
			g.Expect(current.Status.ReadyReplicas).To(Equal(int32(2)))
			g.Expect(countClusterStatefulSets(clusterName)).To(Equal(1))
			g.Expect(countDataPVCs(clusterName)).To(Equal(2))
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())

		By("reconfirming the cluster returns to an available steady state")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})

	It("fails over leader election and continues reconciling with a second controller replica", Label(
		"e2e-anchor",
		"case:manager-leader-failover",
		"covers:leader-election",
		"covers:controller-failover",
		"covers:post-failover-reconcile",
	), func() {
		const clusterName = "manager-failover-cluster"

		cluster, err := f.CreateDevelopmentCluster(ctx, framework.DevelopmentClusterConfig{
			Name:                 clusterName,
			Replicas:             1,
			Version:              openBaoVersion,
			Image:                openBaoImage,
			ConfigInitImg:        configInitImage,
			APIServerCIDR:        apiServerCIDR,
			APIServerEndpointIPs: apiServerEndpointIPs,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = c.Delete(ctx, cluster) })

		_, err = f.WaitForStatefulSetReady(ctx, clusterName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("scaling the controller deployment to two replicas")
		Expect(scaleControllerDeployment(ctx, c, operatorNamespace, 2)).To(Succeed())
		controllerPods, err := waitForReadyControllerPods(
			ctx,
			c,
			operatorNamespace,
			2,
			2*time.Minute,
		)
		Expect(err).NotTo(HaveOccurred())

		By("capturing the current controller lease holder")
		holderBefore, err := controllerLeaderHolderIdentity(ctx, c, operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		var leaderPodName string
		for i := range controllerPods {
			if strings.Contains(holderBefore, controllerPods[i].Name) {
				leaderPodName = controllerPods[i].Name
				break
			}
		}
		Expect(leaderPodName).NotTo(BeEmpty(), "lease holder should match one of the ready controller pods")

		By("deleting the current leader pod to force failover")
		Expect(deleteControllerPod(ctx, c, operatorNamespace, leaderPodName)).To(Succeed())

		By("waiting for a different controller pod to acquire leadership")
		Eventually(func(g Gomega) {
			holderAfter, err := controllerLeaderHolderIdentity(ctx, c, operatorNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(holderAfter).NotTo(Equal(holderBefore))
			g.Expect(holderAfter).NotTo(ContainSubstring(leaderPodName))

			currentPods, err := waitForReadyControllerPods(
				ctx,
				c,
				operatorNamespace,
				2,
				30*time.Second,
			)
			g.Expect(err).NotTo(HaveOccurred())
			matchesReadyPod := false
			for i := range currentPods {
				if strings.Contains(holderAfter, currentPods[i].Name) {
					matchesReadyPod = true
					break
				}
			}
			g.Expect(matchesReadyPod).To(BeTrue(), "new lease holder should correspond to a ready controller pod")
		}, 3*time.Minute, framework.DefaultPollInterval).Should(Succeed())

		By("updating the cluster after failover and verifying reconciliation still works")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Replicas = 2
			g.Expect(c.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		_, err = f.WaitForStatefulSetReady(
			ctx,
			clusterName,
			2,
			framework.DefaultLongWaitTimeout,
			framework.DefaultPollInterval,
		)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		Expect(countClusterStatefulSets(clusterName)).To(Equal(1))
		Expect(countDataPVCs(clusterName)).To(Equal(2))
	})

	It("reconciles an existing cluster after the controller is scaled down and back up", Label(
		"case:manager-outage-adopts-existing-cluster",
		"covers:controller-outage",
		"covers:existing-cluster-adoption",
		"covers:post-outage-reconcile",
	), func() {
		const clusterName = "manager-adoption-cluster"

		cluster, err := f.CreateDevelopmentCluster(ctx, framework.DevelopmentClusterConfig{
			Name:                 clusterName,
			Replicas:             1,
			Version:              openBaoVersion,
			Image:                openBaoImage,
			ConfigInitImg:        configInitImage,
			APIServerCIDR:        apiServerCIDR,
			APIServerEndpointIPs: apiServerEndpointIPs,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = c.Delete(ctx, cluster) })

		sts, err := f.WaitForStatefulSetReady(ctx, clusterName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		originalUID := sts.UID

		By("scaling the controller deployment to zero replicas")
		Expect(scaleControllerDeployment(ctx, c, operatorNamespace, 0)).To(Succeed())

		By("changing cluster desired state while the controller is offline")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Replicas = 2
			g.Expect(c.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		Consistently(func() int32 {
			current := &appsv1.StatefulSet{}
			Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, current)).To(Succeed())
			return current.Status.ReadyReplicas
		}, 20*time.Second, framework.DefaultPollInterval).Should(Equal(int32(1)))

		By("scaling the controller deployment back to one replica")
		Expect(scaleControllerDeployment(ctx, c, operatorNamespace, 1)).To(Succeed())
		_, err = waitForReadyControllerPods(
			ctx,
			c,
			operatorNamespace,
			1,
			2*time.Minute,
		)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the existing cluster is adopted and reconciled to the new desired state")
		current, err := f.WaitForStatefulSetReady(ctx, clusterName, 2, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.UID).To(Equal(originalUID), "controller outage should not recreate the StatefulSet")
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
	})
})
