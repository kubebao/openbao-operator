//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Cluster TLS Lifecycle", Label("tls", "cluster", "lifecycle"), Ordered, func() {
	ctx := context.Background()

	const (
		clusterName           = "tls-lifecycle"
		tlsCertHashAnnotation = "openbao.org/tls-cert-hash"
	)

	var (
		cfg    *rest.Config
		scheme *runtime.Scheme
		f      *framework.Framework
		c      client.Client
	)

	isPodReady := func(pod *corev1.Pod) bool {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady {
				return condition.Status == corev1.ConditionTrue
			}
		}
		return false
	}

	openBaoRestartCount := func(pod *corev1.Pod) int32 {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == constants.ContainerBao {
				return status.RestartCount
			}
		}
		return -1
	}

	parseLeafCertificate := func(secret *corev1.Secret) (*x509.Certificate, error) {
		certPEM := secret.Data["tls.crt"]
		if len(certPEM) == 0 {
			return nil, fmt.Errorf("tls.crt is empty")
		}

		block, _ := pem.Decode(certPEM)
		if block == nil {
			return nil, fmt.Errorf("failed to decode tls.crt PEM")
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse tls.crt: %w", err)
		}
		return cert, nil
	}

	BeforeAll(func() {
		var err error

		cfg, err = ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		scheme = runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(openbaov1alpha1.AddToScheme(scheme)).To(Succeed())

		f, err = framework.NewSetup(ctx, "tls-lifecycle", operatorNamespace)
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

	It("verifies operator-managed TLS with the cluster CA and regenerates the server Secret", Label(
		"case:tls-lifecycle-server-secret-regeneration",
		"covers:tls-lifecycle",
		"covers:tls-verification",
		"covers:secret-regeneration",
		"covers:cert-replacement",
		"covers:tls-hot-reload",
		"covers:pod-stability",
	), func() {
		requests := append([]openbaov1alpha1.SelfInitRequest{}, framework.DefaultAdminSelfInitRequests()...)
		requests = append(requests, e2ehelpers.CreateE2ERequests(f.Namespace)...)

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
					Enabled:  true,
					OIDC:     &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true},
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
					APIServerEgress:      framework.APIServerEgressForNetworkProfile(),
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		Expect(c.Create(ctx, cluster)).To(Succeed())

		clusterKey := types.NamespacedName{Name: clusterName, Namespace: f.Namespace}
		tlsCAKey := types.NamespacedName{Name: clusterName + "-tls-ca", Namespace: f.Namespace}
		tlsServerKey := types.NamespacedName{Name: clusterName + "-tls-server", Namespace: f.Namespace}
		podKey := types.NamespacedName{Name: clusterName + "-0", Namespace: f.Namespace}
		labels := map[string]string{
			constants.LabelOpenBaoCluster:   clusterName,
			constants.LabelOpenBaoComponent: "backup",
		}
		secretPath := "secret/tls-lifecycle"
		secretData := map[string]string{"foo": "bar"}

		By("waiting for the cluster to become Available with TLS ready")
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionTLSReady, metav1.ConditionTrue)

		By("waiting for the TLS Secrets to exist")
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, tlsCAKey, &corev1.Secret{})).To(Succeed())
			g.Expect(c.Get(ctx, tlsServerKey, &corev1.Secret{})).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("writing a secret through the JWT-authenticated test role")
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
				labels,
				secretData,
			)).To(Succeed())
		}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

		By("reading the secret over TLS with explicit CA validation")
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, clusterName)
			g.Expect(err).NotTo(HaveOccurred())

			val, err := e2ehelpers.ReadSecretViaJWTWithCA(
				ctx,
				cfg,
				c,
				f.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"e2e-test",
				secretPath,
				labels,
				"foo",
				tlsCAKey.Name,
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(val).To(Equal("bar"))
		}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

		By("recording the initial certificate and pod reload state")
		Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())
		initialServerSecret := &corev1.Secret{}
		Expect(c.Get(ctx, tlsServerKey, initialServerSecret)).To(Succeed())
		initialLeafCert, err := parseLeafCertificate(initialServerSecret)
		Expect(err).NotTo(HaveOccurred())

		initialPod := &corev1.Pod{}
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, podKey, initialPod)).To(Succeed())
			g.Expect(isPodReady(initialPod)).To(BeTrue())
			g.Expect(openBaoRestartCount(initialPod)).To(BeNumerically(">=", 0))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		initialPodUID := initialPod.UID
		initialRestartCount := openBaoRestartCount(initialPod)
		caSecret := &corev1.Secret{}
		Expect(c.Get(ctx, tlsCAKey, caSecret)).To(Succeed())
		roots := x509.NewCertPool()
		Expect(roots.AppendCertsFromPEM(caSecret.Data["ca.crt"])).To(BeTrue())
		forwardedAddr, stopForward, err := startTLSPodPortForward(f.Namespace, podKey.Name)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(stopForward)
		servedLeafSerial := func() (string, error) {
			dialer := &net.Dialer{Timeout: 3 * time.Second}
			conn, dialErr := tls.DialWithDialer(dialer, "tcp", forwardedAddr, &tls.Config{
				RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12,
			})
			if dialErr != nil {
				return "", dialErr
			}
			defer func() { _ = conn.Close() }()
			if len(conn.ConnectionState().PeerCertificates) == 0 {
				return "", fmt.Errorf("TLS listener did not serve a certificate")
			}
			return conn.ConnectionState().PeerCertificates[0].SerialNumber.String(), nil
		}
		By("verifying the pod serves the initial certificate")
		Eventually(servedLeafSerial, framework.DefaultWaitTimeout, framework.DefaultPollInterval).
			Should(Equal(initialLeafCert.SerialNumber.String()))

		nativeTLSReload, err := platformsemver.AtLeast(openBaoVersion, 2, 7, 0)
		Expect(err).NotTo(HaveOccurred())
		wrapperWatchFile := "-watch-file=" + constants.PathTLS + "/tls.crt"
		if nativeTLSReload {
			Expect(initialPod.Spec.Containers[0].Args).NotTo(ContainElement(wrapperWatchFile))
		} else {
			Expect(initialPod.Spec.Containers[0].Args).To(ContainElement(wrapperWatchFile))
		}
		initialCertHash := ""
		if initialPod.Annotations != nil {
			initialCertHash = initialPod.Annotations[tlsCertHashAnnotation]
		}

		serverSecret := &corev1.Secret{}
		Expect(c.Get(ctx, tlsServerKey, serverSecret)).To(Succeed())
		previousUID := serverSecret.UID

		controllerDeployment := &appsv1.Deployment{}
		Expect(c.Get(ctx, types.NamespacedName{
			Name:      "openbao-operator-controller",
			Namespace: operatorNamespace,
		}, controllerDeployment)).To(Succeed())
		controllerSA := controllerDeployment.Spec.Template.Spec.ServiceAccountName
		Expect(controllerSA).NotTo(BeEmpty())
		controllerUser := fmt.Sprintf("system:serviceaccount:%s:%s", operatorNamespace, controllerSA)
		controllerGroups := []string{
			"system:serviceaccounts",
			fmt.Sprintf("system:serviceaccounts:%s", operatorNamespace),
			"system:authenticated",
		}

		By("deleting the managed tls-server Secret as the operator controller")
		err = e2ehelpers.RunWithImpersonation(
			ctx,
			cfg,
			scheme,
			controllerUser,
			controllerGroups,
			func(ic client.Client) error {
				return ic.Delete(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tlsServerKey.Name,
						Namespace: tlsServerKey.Namespace,
					},
				})
			},
		)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			err := c.Get(ctx, tlsServerKey, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(
			BeTrue(),
			"expected tls-server Secret to be deleted before regeneration",
		)

		By("verifying the OpenBao pod stays ready without being recreated while the secret is reissued")
		Consistently(func(g Gomega) {
			pod := &corev1.Pod{}
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			g.Expect(pod.UID).To(Equal(initialPodUID))
			g.Expect(isPodReady(pod)).To(BeTrue())
			g.Expect(openBaoRestartCount(pod)).To(Equal(initialRestartCount))
		}, 15*time.Second, framework.DefaultPollInterval).Should(Succeed())

		By("triggering reconcile and waiting for the tls-server Secret to be reissued")
		Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())
		var updatedLeafCert *x509.Certificate
		Eventually(func(g Gomega) {
			updated := &corev1.Secret{}
			g.Expect(c.Get(ctx, tlsServerKey, updated)).To(Succeed())
			g.Expect(updated.UID).NotTo(Equal(previousUID))
			g.Expect(updated.Data["tls.crt"]).NotTo(BeEmpty())
			g.Expect(updated.Data["tls.key"]).NotTo(BeEmpty())
			g.Expect(updated.Data["ca.crt"]).NotTo(BeEmpty())

			parsed, parseErr := parseLeafCertificate(updated)
			g.Expect(parseErr).NotTo(HaveOccurred())
			g.Expect(parsed.SerialNumber.String()).NotTo(Equal(initialLeafCert.SerialNumber.String()))
			g.Expect(updated.Data["tls.crt"]).NotTo(Equal(initialServerSecret.Data["tls.crt"]))
			updatedLeafCert = parsed
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("verifying the pod stays ready without restarting")
		Eventually(func(g Gomega) {
			pod := &corev1.Pod{}
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			g.Expect(pod.UID).To(Equal(initialPodUID))
			g.Expect(isPodReady(pod)).To(BeTrue())
			g.Expect(openBaoRestartCount(pod)).To(Equal(initialRestartCount))
			if !nativeTLSReload {
				g.Expect(pod.Annotations).NotTo(BeNil())
				g.Expect(pod.Annotations[tlsCertHashAnnotation]).NotTo(BeEmpty())
				g.Expect(pod.Annotations[tlsCertHashAnnotation]).NotTo(Equal(initialCertHash))
			}
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("verifying the pod serves the rotated certificate")
		Eventually(servedLeafSerial, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).
			Should(Equal(updatedLeafCert.SerialNumber.String()))

		By("reconfirming cluster readiness and stability after server Secret regeneration")
		Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionTLSReady, metav1.ConditionTrue)
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		Expect(f.WaitForClusterPhase(
			ctx,
			clusterName,
			openbaov1alpha1.ClusterPhaseRunning,
			framework.DefaultLongWaitTimeout,
			framework.DefaultPollInterval,
		)).To(Succeed())

		Consistently(func(g Gomega) {
			cluster := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, clusterKey, cluster)).To(Succeed())
			tlsReady := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionTLSReady))
			available := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(tlsReady).NotTo(BeNil())
			g.Expect(tlsReady.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))

			pod := &corev1.Pod{}
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			g.Expect(pod.UID).To(Equal(initialPodUID))
			g.Expect(isPodReady(pod)).To(BeTrue())
			g.Expect(openBaoRestartCount(pod)).To(Equal(initialRestartCount))
		}, 15*time.Second, 5*time.Second).Should(Succeed())

		By("re-reading the secret over TLS with CA validation after regeneration")
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, clusterName)
			g.Expect(err).NotTo(HaveOccurred())

			val, err := e2ehelpers.ReadSecretViaJWTWithCA(
				ctx,
				cfg,
				c,
				f.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"e2e-test",
				secretPath,
				labels,
				"foo",
				tlsCAKey.Name,
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(val).To(Equal("bar"))
		}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

		Expect(updatedLeafCert).NotTo(BeNil())
		Expect(updatedLeafCert.SerialNumber.String()).NotTo(Equal(initialLeafCert.SerialNumber.String()))

		Expect(c.Get(ctx, clusterKey, &openbaov1alpha1.OpenBaoCluster{})).To(Succeed())
	})
})

func startTLSPodPortForward(namespace, podName string) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("allocate TLS port-forward port: %w", err)
	}
	addr := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return "", nil, fmt.Errorf("close TLS port reservation: %w", err)
	}

	cmd := exec.Command("kubectl", "-n", namespace, "port-forward", "pod/"+podName,
		fmt.Sprintf("%d:8200", port)) // #nosec G204 -- test harness port-forwards to an operator-owned Pod
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start TLS port-forward: %w", err)
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return addr, stop, nil
}
