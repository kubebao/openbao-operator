//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
)

var _ = Describe("Security: Anti-Tamper Policy", Label("security", "tamper", "cluster", "slow"), Ordered, func() {
	ctx := context.Background()

	var (
		cfg    *rest.Config
		scheme *runtime.Scheme
		admin  client.Client
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
	})

	Context("managed-resource mutation guardrails", func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			cluster         *openbaov1alpha1.OpenBaoCluster
		)

		BeforeAll(func() {
			var err error

			tenantFW, err = framework.New(ctx, admin, "tenant-tamper", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace

			cluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tamper-cluster",
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
			Expect(admin.Create(ctx, cluster)).To(Succeed())

			Eventually(func() error {
				return admin.Get(ctx, types.NamespacedName{Name: cluster.Name + "-config", Namespace: tenantNamespace}, &corev1.ConfigMap{})
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterAll(func() {
			if tenantFW == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			_ = tenantFW.Cleanup(cleanupCtx)
		})

		It("prevents deletion of the managed ConfigMap", Label(
			"case:anti-tamper-configmap-delete-blocked",
			"covers:anti-tamper",
			"covers:configmap-protection",
		), func() {
			cm := &corev1.ConfigMap{}
			err := admin.Get(ctx, types.NamespacedName{Name: cluster.Name + "-config", Namespace: tenantNamespace}, cm)
			Expect(err).NotTo(HaveOccurred())

			err = admin.Delete(ctx, cm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Direct modification of OpenBao-managed resources is prohibited"))
			Expect(err.Error()).To(ContainSubstring("ValidatingAdmissionPolicy"))

			Eventually(func() error {
				return admin.Get(ctx, types.NamespacedName{Name: cluster.Name + "-config", Namespace: tenantNamespace}, &corev1.ConfigMap{})
			}, 5*time.Second, 1*time.Second).Should(Succeed())
		})

		It("prevents deletion of the managed StatefulSet", Label(
			"case:anti-tamper-statefulset-delete-blocked",
			"covers:anti-tamper",
			"covers:statefulset-protection",
		), func() {
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return admin.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: tenantNamespace}, sts)
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			err := admin.Delete(ctx, sts)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Direct modification of OpenBao-managed resources is prohibited"))
			Expect(err.Error()).To(ContainSubstring("ValidatingAdmissionPolicy"))

			Eventually(func() error {
				return admin.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: tenantNamespace}, &appsv1.StatefulSet{})
			}, 5*time.Second, 1*time.Second).Should(Succeed())
		})
	})
})
