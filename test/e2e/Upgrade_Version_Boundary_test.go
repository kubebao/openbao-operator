//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const boundarySourceVersion = "2.5.5"

var _ = Describe("Upgrade protocol boundary", Label("upgrade", "rolling"), func() {
	It("rejects a pre-2.6 crossing in both strategies without replacing voters", Label("case:upgrade-protocol-boundary", "covers:upgrade-version-compatibility", "e2e-anchor"), func() {
		ctx := context.Background()
		fw, err := framework.NewSetup(ctx, "tenant-upgrade-boundary", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		defer cleanupUpgradeFixture(ctx, fw)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "boundary", Namespace: fw.Namespace},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				ControllerJWTMode: openbaov1alpha1.ControllerJWTModeTarget, Profile: openbaov1alpha1.ProfileDevelopment,
				Version: boundarySourceVersion, Image: "openbao/openbao:" + boundarySourceVersion, Replicas: 3,
				InitContainer:  &openbaov1alpha1.InitContainerConfig{Enabled: true, Image: configInitImage},
				SelfInit:       &openbaov1alpha1.SelfInitConfig{Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}, Requests: e2ehelpers.CreateE2ERequests(fw.Namespace)},
				TLS:            openbaov1alpha1.TLSConfig{Enabled: true, Mode: openbaov1alpha1.TLSModeOperatorManaged, RotationPeriod: "720h"},
				Storage:        openbaov1alpha1.StorageConfig{Size: "1Gi"},
				Network:        &openbaov1alpha1.NetworkConfig{APIServerCIDR: apiServerCIDR, APIServerEndpointIPs: apiServerEndpointIPs, APIServerEgress: framework.APIServerEgressForNetworkProfile()},
				Upgrade:        &openbaov1alpha1.UpgradeConfig{Strategy: openbaov1alpha1.UpdateStrategyRollingUpdate},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		Expect(fw.Client.Create(ctx, cluster)).To(Succeed())
		key := client.ObjectKeyFromObject(cluster)
		Eventually(func(g Gomega) {
			g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
			g.Expect(cluster.Status.Initialized).To(BeTrue())
			g.Expect(cluster.Status.CurrentVersion).To(Equal(boundarySourceVersion))
			g.Expect(cluster.Status.Upgrade).To(BeNil())
			sts := &appsv1.StatefulSet{}
			g.Expect(fw.Client.Get(ctx, key, sts)).To(Succeed())
			g.Expect(sts.Status.ObservedGeneration).To(Equal(sts.Generation))
			g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(3)))
			g.Expect(sts.Status.CurrentRevision).To(Equal(sts.Status.UpdateRevision))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		for _, strategy := range []openbaov1alpha1.UpdateStrategyType{openbaov1alpha1.UpdateStrategyRollingUpdate, openbaov1alpha1.UpdateStrategyBlueGreen} {
			By(fmt.Sprintf("accepting idle strategy %s before the version request", strategy))
			Eventually(func(g Gomega) {
				g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
				before := cluster.DeepCopy()
				cluster.Spec.Version, cluster.Spec.Image = boundarySourceVersion, "openbao/openbao:"+boundarySourceVersion
				cluster.Spec.Upgrade.Strategy = strategy
				g.Expect(fw.Client.Patch(ctx, cluster, client.MergeFrom(before))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
				g.Expect(cluster.Status.AcceptedUpgradeStrategy).To(Equal(strategy))
				sts := &appsv1.StatefulSet{}
				g.Expect(fw.Client.Get(ctx, key, sts)).To(Succeed())
				g.Expect(sts.Status.ObservedGeneration).To(Equal(sts.Generation))
				g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(3)))
				if strategy == openbaov1alpha1.UpdateStrategyBlueGreen {
					g.Expect(sts.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
				} else {
					g.Expect(sts.Spec.UpdateStrategy.Type).To(Equal(appsv1.RollingUpdateStatefulSetStrategyType))
				}
				g.Expect(cluster.Status.Workload).NotTo(BeNil())
				g.Expect(cluster.Status.Workload.LastError).To(BeNil())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			baseline := &appsv1.StatefulSet{}
			Expect(fw.Client.Get(ctx, key, baseline)).To(Succeed())
			pods := &corev1.PodList{}
			Expect(fw.Client.List(ctx, pods, client.InNamespace(fw.Namespace), client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name})).To(Succeed())
			Expect(pods.Items).To(HaveLen(3))
			uids := map[string]types.UID{}
			for _, pod := range pods.Items {
				uids[pod.Name] = pod.UID
			}
			before := cluster.DeepCopy()
			cluster.Spec.Version, cluster.Spec.Image = "2.6.5", "openbao/openbao:2.6.5"
			Expect(fw.Client.Patch(ctx, cluster, client.MergeFrom(before))).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
				g.Expect(cluster.Status.Workload).NotTo(BeNil())
				g.Expect(cluster.Status.Workload.LastError).NotTo(BeNil())
				g.Expect(cluster.Status.Workload.LastError.Reason).To(Equal(constants.ReasonUpgradeVersionIncompatible))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Consistently(func(g Gomega) {
				stored := &appsv1.StatefulSet{}
				g.Expect(fw.Client.Get(ctx, key, stored)).To(Succeed())
				g.Expect(stored.UID).To(Equal(baseline.UID))
				g.Expect(stored.Spec).To(Equal(baseline.Spec))
				g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
				g.Expect(cluster.Status.CurrentVersion).To(Equal(boundarySourceVersion))
				g.Expect(cluster.Status.Upgrade).To(BeNil())
				g.Expect(cluster.Status.OperationLock).To(BeNil())
				for name, uid := range uids {
					pod := &corev1.Pod{}
					g.Expect(fw.Client.Get(ctx, client.ObjectKey{Namespace: fw.Namespace, Name: name}, pod)).To(Succeed())
					g.Expect(pod.UID).To(Equal(uid))
					g.Expect(pod.DeletionTimestamp).To(BeNil())
				}
				jobs := &batchv1.JobList{}
				g.Expect(fw.Client.List(ctx, jobs, client.InNamespace(fw.Namespace))).To(Succeed())
				g.Expect(jobs.Items).To(BeEmpty())
			}, 20*time.Second, framework.DefaultPollInterval).Should(Succeed())
			By("clearing only the rejected version request before any strategy change")
			Eventually(func(g Gomega) {
				g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
				before := cluster.DeepCopy()
				cluster.Spec.Version, cluster.Spec.Image = boundarySourceVersion, "openbao/openbao:"+boundarySourceVersion
				g.Expect(fw.Client.Patch(ctx, cluster, client.MergeFrom(before))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(fw.Client.Get(ctx, key, cluster)).To(Succeed())
				g.Expect(cluster.Status.Workload).NotTo(BeNil())
				g.Expect(cluster.Status.Workload.LastError).To(BeNil())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		}
	})
})
