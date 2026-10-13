//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	"github.com/kubebao/openbao-operator/test/utils"
)

var _ = Describe("Cluster PKCS#11 Unseal", Label("cluster", "lifecycle", "unseal", "pkcs11", "hsm"), Ordered, func() {
	ctx := context.Background()

	var (
		f *framework.Framework
		c client.Client
	)

	const (
		clusterName     = "pkcs11-softhsm"
		credentialsName = "pkcs11-softhsm-credentials"
		softHSMConfig   = "directories.tokendir = /bao/data/softhsm/tokens\nobjectstore.backend = file\nlog.level = ERROR\nslots.removable = false\n"
	)

	BeforeAll(func() {
		requireSoftHSMSuite()

		var err error
		f, err = framework.NewSetup(ctx, "pkcs11", operatorNamespace)
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

	It("initializes, restarts, and scales using a SoftHSM-backed PKCS#11 seal", Label(
		"case:cluster-unseal-pkcs11-initializes-restarts-and-scales-using-a-50e1c83b",
	), func() {
		By("creating the PKCS#11 credentials Secret")
		credentials := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      credentialsName,
				Namespace: f.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"BAO_HSM_PIN":   []byte("1234"),
				"softhsm2.conf": []byte(softHSMConfig),
			},
		}
		Expect(c.Create(ctx, credentials)).To(Succeed())

		By("configuring the pinned PKCS#11 plugin for the target OpenBao version")
		plugin := openbaov1alpha1.Plugin{Type: "kms", Name: "pkcs11", Command: "openbao-plugin-kms-pkcs11"}
		modern, err := platformsemver.AtLeast(openBaoVersion, 2, 7, 0)
		Expect(err).NotTo(HaveOccurred())
		if !modern {
			pluginChecksum, err := utils.Run(exec.Command("docker", "run", "--rm", "--entrypoint", "cat", openBaoImage,
				"/opt/openbao-softhsm/plugin.sha256"))
			Expect(err).NotTo(HaveOccurred())
			checksumFields := strings.Fields(pluginChecksum)
			Expect(checksumFields).NotTo(BeEmpty())
			plugin.Version, plugin.BinaryName, plugin.SHA256Sum = "v0.1.0", plugin.Command, checksumFields[0]
		}

		By("creating an OpenBaoCluster configured for PKCS#11 unseal")
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
				Plugins:  []openbaov1alpha1.Plugin{plugin},
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
				},
				Unseal: &openbaov1alpha1.UnsealConfig{
					Type: "pkcs11",
					PKCS11: &openbaov1alpha1.PKCS11SealConfig{
						Lib:        "/usr/lib/softhsm/libsofthsm2.so",
						TokenLabel: "OpenBao",
						KeyLabel:   "bao-root-key-rsa",
						Mechanism:  "RSA_PKCS_OAEP",
						// SoftHSM's RSA OAEP support is limited to SHA1.
						RSAOAEPHash: "sha1",
						Runtime: &openbaov1alpha1.PKCS11RuntimeConfig{
							LibraryPath: "/usr/lib/softhsm",
							FileEnv: []openbaov1alpha1.PKCS11RuntimeFileEnvVar{
								{Name: "SOFTHSM2_CONF", SecretKey: "softhsm2.conf"},
							},
						},
					},
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: credentialsName,
					},
				},
				Maintenance: &openbaov1alpha1.MaintenanceConfig{
					Enabled: true,
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		if sc := strings.TrimSpace(os.Getenv("E2E_STORAGE_CLASS")); sc != "" {
			cluster.Spec.Storage.StorageClassName = &sc
		}
		Expect(c.Create(ctx, cluster)).To(Succeed())

		By("waiting for the initial PKCS#11-sealed pod to become ready")
		_, err = f.WaitForStatefulSetReady(ctx, clusterName, 1, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("verifying the rendered OpenBao config contains the PKCS#11 seal stanza")
		config := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Name: clusterName + "-config", Namespace: f.Namespace}, config)).To(Succeed())
		Expect(config.Data).To(HaveKey("config.hcl"))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`seal "pkcs11"`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`plugin "kms" "pkcs11"`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`token_label`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`OpenBao`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`key_label`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`bao-root-key-rsa`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`mechanism`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`RSA_PKCS_OAEP`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`rsa_oaep_hash`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`sha1`))

		By("verifying the StatefulSet uses the PKCS#11 runtime env wiring")
		statefulSet := &appsv1.StatefulSet{}
		Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, statefulSet)).To(Succeed())
		Expect(statefulSet.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		Expect(containerEnvValue(statefulSet.Spec.Template.Spec.Containers[0], "SOFTHSM2_CONF")).To(Equal("/etc/bao/seal-creds/softhsm2.conf"))
		Expect(containerEnvValue(statefulSet.Spec.Template.Spec.Containers[0], "LD_LIBRARY_PATH")).To(Equal("/usr/lib/softhsm"))
		Expect(containerEnvValue(statefulSet.Spec.Template.Spec.Containers[0], "BAO_HSM_RSA_OAEP_HASH")).To(Equal("sha1"))

		By("deleting the pod and validating it auto-unseals after restart")
		pod := &corev1.Pod{}
		Expect(c.Get(ctx, types.NamespacedName{Name: clusterName + "-0", Namespace: f.Namespace}, pod)).To(Succeed())
		oldUID := pod.UID
		Expect(c.Delete(ctx, pod)).To(Succeed())

		Eventually(func(g Gomega) {
			restarted := &corev1.Pod{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName + "-0", Namespace: f.Namespace}, restarted)).To(Succeed())
			g.Expect(restarted.UID).NotTo(Equal(oldUID))
			g.Expect(podReady(restarted)).To(BeTrue())
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("scaling up to verify new pods can use the seeded PKCS#11 key material")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &openbaov1alpha1.OpenBaoCluster{}
			if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, current); err != nil {
				return err
			}
			current.Spec.Replicas = 2
			return c.Update(ctx, current)
		})).To(Succeed())
		_, err = f.WaitForStatefulSetReady(ctx, clusterName, 2, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("scaling back down cleanly")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &openbaov1alpha1.OpenBaoCluster{}
			if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, current); err != nil {
				return err
			}
			current.Spec.Replicas = 1
			return c.Update(ctx, current)
		})).To(Succeed())
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
			g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
	})
})

func podReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func containerEnvValue(container corev1.Container, name string) string {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
