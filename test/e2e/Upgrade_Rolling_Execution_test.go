//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Rolling execution identity", Label("upgrade", "upgrades", "rolling", "cluster", "slow"), func() {
	It("completes consecutive upgrades while prior step-down Jobs remain", Label(
		"case:rolling-consecutive-execution-identity", "covers:rolling-execution-identity", "e2e-anchor",
	), func() {
		ctx := context.Background()
		f, err := framework.NewSetup(ctx, "tenant-rolling-executions", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { cleanupUpgradeFixture(ctx, f) })
		cluster := &api.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "rolling-executions", Namespace: f.Namespace},
			Spec: api.OpenBaoClusterSpec{
				Profile: api.ProfileDevelopment, Version: defaultBlueGreenUpgradeFromVersion,
				Image: fmt.Sprintf("openbao/openbao:%s", defaultBlueGreenUpgradeFromVersion), Replicas: 3,
				InitContainer: &api.InitContainerConfig{Enabled: true, Image: configInitImage},
				SelfInit: &api.SelfInitConfig{
					Enabled: true, OIDC: &api.SelfInitOIDCConfig{Enabled: true},
					Requests: e2ehelpers.CreateE2ERequests(f.Namespace),
				},
				TLS:     api.TLSConfig{Enabled: true, Mode: api.TLSModeOperatorManaged, RotationPeriod: "720h"},
				Storage: api.StorageConfig{Size: "1Gi"},
				Network: &api.NetworkConfig{
					APIServerCIDR: apiServerCIDR, APIServerEndpointIPs: apiServerEndpointIPs,
					APIServerEgress: framework.APIServerEgressForNetworkProfile(),
				},
				Upgrade: &api.UpgradeConfig{Image: upgradeExecutorImage}, DeletionPolicy: api.DeletionPolicyDeleteAll,
			},
		}
		Expect(f.Client.Create(ctx, cluster)).To(Succeed())
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpRollingUpgradeDiagnostics(ctx, f.Client, f.Namespace, cluster.Name)
			}
		})
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
			g.Expect(cluster.Status.Initialized).To(BeTrue())
			g.Expect(cluster.Status.CurrentVersion).To(Equal(defaultBlueGreenUpgradeFromVersion))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		_, err = f.WaitForStatefulSetReady(ctx, cluster.Name, 3, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())

		priorExecution := ""
		history := map[types.NamespacedName]types.UID{}
		for _, target := range []string{defaultUpgradeFromVersion, defaultUpgradeToVersion} {
			By("Requesting rolling target " + target)
			Eventually(func(g Gomega) {
				g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
				original := cluster.DeepCopy()
				cluster.Spec.Version = target
				cluster.Spec.Image = fmt.Sprintf("openbao/openbao:%s", target)
				g.Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			execution := ""
			Eventually(func(g Gomega) {
				g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
				g.Expect(cluster.Status.Upgrade).NotTo(BeNil())
				g.Expect(cluster.Status.Upgrade.ExecutionID).NotTo(BeEmpty())
				execution = cluster.Status.Upgrade.ExecutionID
				g.Expect(execution).NotTo(Equal(priorExecution))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
				if cluster.Status.Upgrade != nil {
					g.Expect(cluster.Status.Upgrade.ExecutionID).To(Equal(execution))
					g.Expect(cluster.Status.Upgrade.Failure).To(BeNil())
				}
				g.Expect(cluster.Status.CurrentVersion).To(Equal(target))
				g.Expect(cluster.Status.Upgrade).To(BeNil())
			}, 15*time.Minute, framework.DefaultPollInterval).Should(Succeed())
			for key, uid := range history {
				job := &batchv1.Job{}
				Expect(f.Client.Get(ctx, key, job)).To(Succeed())
				Expect(job.UID).To(Equal(uid), "prior attempt evidence must remain unchanged")
			}
			if priorExecution == "" {
				By("Retaining successful prior-attempt Jobs for every ordinal before the next upgrade")
				fixture := cluster.DeepCopy()
				fixture.Status.Upgrade = &api.UpgradeProgress{ExecutionID: execution}
				for ordinal := int32(0); ordinal < cluster.Spec.Replicas; ordinal++ {
					job := newStaleRollingStepDownJob(fixture, fmt.Sprintf("%s-%d", cluster.Name, ordinal), cluster.Spec.Image)
					key := client.ObjectKeyFromObject(job)
					if err := f.Client.Get(ctx, key, &batchv1.Job{}); client.IgnoreNotFound(err) != nil {
						Expect(err).NotTo(HaveOccurred())
					} else if err != nil {
						Expect(createRollingStepDownFixtureJob(ctx, f.Client, job)).To(Succeed())
					}
					Eventually(func(g Gomega) {
						g.Expect(f.Client.Get(ctx, key, job)).To(Succeed())
						g.Expect(validateManagedLifecycleJobOwnerProof(job, cluster)).To(Succeed())
						g.Expect(job.Status.Succeeded).To(Equal(int32(1)))
					}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
					history[key] = job.UID
				}
			}
			priorExecution = execution
		}
	})
})
