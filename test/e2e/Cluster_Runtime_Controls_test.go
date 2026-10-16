//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Cluster Runtime Controls", Label("lifecycle", "cluster", "runtime"), Ordered, func() {
	ctx := context.Background()

	var (
		f   *framework.Framework
		c   client.Client
		cfg *rest.Config
	)

	newDevelopmentCluster := func(name string) *openbaov1alpha1.OpenBaoCluster {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
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

		return cluster
	}

	waitForClusterAvailable := func(clusterName string) {
		Eventually(func(g Gomega) {
			cluster := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, cluster)).To(Succeed())
			available := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())
	}

	waitForClusterPod := func(clusterName string) *corev1.Pod {
		var readyPod *corev1.Pod
		Eventually(func(g Gomega) {
			pods := &corev1.PodList{}
			g.Expect(c.List(ctx, pods,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster: clusterName,
				},
			)).To(Succeed())

			found := false
			for i := range pods.Items {
				pod := &pods.Items[i]
				if pod.DeletionTimestamp != nil || !isPodReady(pod) {
					continue
				}
				copy := pod.DeepCopy()
				readyPod = copy
				found = true
				break
			}
			g.Expect(found).To(BeTrue())
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		return readyPod
	}

	parseServerCertificate := func(secret *corev1.Secret) *x509.Certificate {
		certPEM := secret.Data["tls.crt"]
		Expect(certPEM).NotTo(BeEmpty())

		block, _ := pem.Decode(certPEM)
		Expect(block).NotTo(BeNil())

		cert, err := x509.ParseCertificate(block.Bytes)
		Expect(err).NotTo(HaveOccurred())
		return cert
	}

	pluginChecksumForClusterArchitecture := func() string {
		nodes := &corev1.NodeList{}
		Expect(c.List(ctx, nodes)).To(Succeed())
		Expect(nodes.Items).NotTo(BeEmpty())

		arch := ""
		for i := range nodes.Items {
			node := &nodes.Items[i]
			nodeArch := node.Labels["kubernetes.io/arch"]
			if nodeArch == "" {
				nodeArch = node.Status.NodeInfo.Architecture
			}
			if nodeArch == "" {
				continue
			}
			if arch == "" {
				arch = nodeArch
				continue
			}
			if arch != nodeArch {
				Skip(fmt.Sprintf("OCI plugin checksum fixture is single-architecture; cluster has both %q and %q nodes", arch, nodeArch))
			}
		}

		switch arch {
		case "amd64":
			return "c8d23e6d31be2a59d0c269bb7243158c4c61c5073f7ba50ce6f1a0050e023e2d"
		case "arm64":
			return "b98cb1cbfd0f567d7b614efb0621aaba10c4deda865f5e5b3d155609ada2482e"
		default:
			Skip(fmt.Sprintf("OCI plugin checksum fixture is not defined for node architecture %q", arch))
			return ""
		}
	}

	BeforeAll(func() {
		var err error
		cfg, err = ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		f, err = framework.NewSetup(ctx, "cluster-runtime", operatorNamespace)
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

	It("rolls the OpenBao pod when runtime.restartAt changes", Label(
		"case:cluster-restart-at-rolls-pod",
		"covers:restart-at",
		"covers:pod-rollout",
	), func() {
		const clusterName = "restart-at-cluster"

		cluster := newDevelopmentCluster(clusterName)
		Expect(c.Create(ctx, cluster)).To(Succeed())
		DeferCleanup(func() { _ = c.Delete(ctx, cluster) })

		_, err := f.WaitForStatefulSetReady(ctx, clusterName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		waitForClusterAvailable(clusterName)
		initialPod := waitForClusterPod(clusterName)

		restartAt := time.Now().UTC().Format(time.RFC3339Nano)

		By("setting spec.runtime.restartAt to trigger a rolling restart")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Runtime = &openbaov1alpha1.RuntimeConfig{RestartAt: restartAt}
			g.Expect(c.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("waiting for the StatefulSet pod template to carry the restart annotation")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
			g.Expect(sts.Spec.Template.Annotations).To(HaveKeyWithValue(constants.AnnotationRestartAt, restartAt))
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("waiting for the original pod to be replaced")
		Eventually(func(g Gomega) {
			currentPod := waitForClusterPod(clusterName)
			g.Expect(currentPod.UID).NotTo(Equal(initialPod.UID))
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())

		waitForClusterAvailable(clusterName)
	})

	It("creates ingress resources and includes the ingress host in the server certificate", Label(
		"case:cluster-ingress-host-san",
		"covers:ingress",
		"covers:external-service",
		"covers:tls-san",
	), func() {
		const clusterName = "ingress-cluster"

		host := fmt.Sprintf("%s.%s.e2e.example.invalid", clusterName, f.Namespace)
		cluster := newDevelopmentCluster(clusterName)
		cluster.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled: true,
			Host:    host,
			Path:    "/",
		}
		cluster.Spec.Network.TrustedIngressPeers = []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "ingress-system",
					},
				},
			},
		}
		Expect(c.Create(ctx, cluster)).To(Succeed())
		DeferCleanup(func() { _ = c.Delete(ctx, cluster) })

		_, err := f.WaitForStatefulSetReady(ctx, clusterName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		waitForClusterAvailable(clusterName)

		By("verifying the ingress and public service are created for external access")
		Eventually(func(g Gomega) {
			service := &corev1.Service{}
			g.Expect(c.Get(ctx, types.NamespacedName{
				Name:      clusterName + "-public",
				Namespace: f.Namespace,
			}, service)).To(Succeed())

			ingress := &networkingv1.Ingress{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, ingress)).To(Succeed())
			g.Expect(ingress.Spec.Rules).To(HaveLen(1))
			g.Expect(ingress.Spec.Rules[0].Host).To(Equal(host))
			g.Expect(ingress.Spec.TLS).To(HaveLen(1))
			g.Expect(ingress.Spec.TLS[0].SecretName).To(Equal(clusterName + "-tls-server"))
			g.Expect(ingress.Spec.Rules[0].HTTP).NotTo(BeNil())
			g.Expect(ingress.Spec.Rules[0].HTTP.Paths).NotTo(BeEmpty())
			g.Expect(ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service).NotTo(BeNil())
			g.Expect(ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name).To(Equal(clusterName + "-public"))
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("verifying the operator-managed server certificate includes the ingress host")
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			g.Expect(c.Get(ctx, types.NamespacedName{
				Name:      clusterName + "-tls-server",
				Namespace: f.Namespace,
			}, secret)).To(Succeed())
			cert := parseServerCertificate(secret)
			g.Expect(cert.DNSNames).To(ContainElement(host))
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})

	It("registers an OCI plugin with a writable plugin directory", Label(
		"case:cluster-oci-plugin-install",
		"covers:plugin-auto-download",
		"covers:plugin-directory",
	), func() {
		const (
			clusterName      = "plugin-cluster"
			pluginPolicyName = "plugin-admin"
			pluginRoleName   = "plugin-admin"
		)

		ok, err := platformsemver.AtLeast(openBaoVersion, 2, 5, 0)
		Expect(err).NotTo(HaveOccurred())
		if !ok {
			Skip(fmt.Sprintf("declarative OCI plugin auto-download requires OpenBao >= 2.5.0; got %s", openBaoVersion))
		}

		pluginChecksum := pluginChecksumForClusterArchitecture()
		port443 := intstr.FromInt(443)
		tcp := corev1.ProtocolTCP

		cluster := newDevelopmentCluster(clusterName)
		cluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
			Plugin: &openbaov1alpha1.PluginConfig{
				AutoDownload: ptr.To(true),
				AutoRegister: ptr.To(true),
			},
		}
		cluster.Spec.Network.EgressRules = append(cluster.Spec.Network.EgressRules, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					IPBlock: &networkingv1.IPBlock{
						CIDR: "0.0.0.0/0",
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: &tcp,
					Port:     &port443,
				},
			},
		})
		cluster.Spec.Plugins = []openbaov1alpha1.Plugin{
			{
				Type:       "secret",
				Name:       "aws",
				Image:      "ghcr.io/openbao/openbao-plugin-secrets-aws",
				Version:    "v0.0.1",
				BinaryName: "openbao-plugin-secrets-aws",
				SHA256Sum:  pluginChecksum,
			},
		}
		cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
			Enabled: true,
			OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
				Enabled: true,
			},
			Requests: append(
				framework.DefaultAdminSelfInitRequests(),
				e2ehelpers.CreateJWTPolicyRoleRequests(
					f.Namespace,
					"default",
					pluginPolicyName,
					pluginRoleName,
					`path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}`,
				)...,
			),
		}

		Expect(c.Create(ctx, cluster)).To(Succeed())
		DeferCleanup(func() { _ = c.Delete(ctx, cluster) })

		By("waiting for the OpenBao pod to become ready")
		sts, err := f.WaitForStatefulSetReady(ctx, clusterName, 1, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the StatefulSet mounts a writable plugin directory")
		pluginVolumeFound := false
		for _, volume := range sts.Spec.Template.Spec.Volumes {
			if volume.Name == constants.VolumePlugins {
				pluginVolumeFound = true
				Expect(volume.EmptyDir).NotTo(BeNil())
				break
			}
		}
		Expect(pluginVolumeFound).To(BeTrue())

		pluginMountFound := false
		for _, container := range sts.Spec.Template.Spec.Containers {
			if container.Name != constants.ContainerBao {
				continue
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == constants.VolumePlugins {
					pluginMountFound = true
					Expect(mount.MountPath).To(Equal(constants.PathPlugins))
					Expect(mount.ReadOnly).To(BeFalse())
					break
				}
			}
		}
		Expect(pluginMountFound).To(BeTrue())

		waitForClusterAvailable(clusterName)

		By("verifying OpenBao registered the declarative OCI plugin")
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, clusterName)
			g.Expect(err).NotTo(HaveOccurred())
			_, err = e2ehelpers.RunCommandViaJWT(
				ctx,
				cfg,
				c,
				f.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				pluginRoleName,
				map[string]string{
					constants.LabelOpenBaoCluster:   clusterName,
					constants.LabelOpenBaoComponent: constants.ComponentBackup,
				},
				fmt.Sprintf(`
bao plugin list secret | grep -E '^aws[[:space:]]+v0\.0\.1'
bao plugin info -version=v0.0.1 secret aws | grep %q
`, pluginChecksum),
			)
			g.Expect(err).NotTo(HaveOccurred())
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	})

})
