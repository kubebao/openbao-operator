//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	platformconstants "github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const acmeCacheStorageClassEnv = "E2E_ACME_CACHE_STORAGE_CLASS"

var _ = Describe("ACME TLS (OpenBao native ACME client)", Label("tls", "security", "slow"), Ordered, func() {
	ctx := context.Background()

	var (
		cfg    *rest.Config
		scheme *runtime.Scheme
		c      client.Client
		f      *framework.Framework
	)

	const (
		clusterName = "acme-cluster"

		infraBaoName            = "infra-bao"
		infraBaoKeyName         = "openbao-unseal"
		infraBaoTokenSecretName = "infra-bao-token-acme"
	)

	var (
		infraBaoRootToken string
		infraBaoTLSCACert []byte
		infraBaoPKICA     []byte
	)

	BeforeAll(func() {
		var err error

		requireHardenedSignedSuite()

		By("setting up test client and scheme")
		cfg, err = ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		scheme = runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(openbaov1alpha1.AddToScheme(scheme)).To(Succeed())

		c, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		f, err = framework.New(ctx, c, "acme", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "Created namespace %q\n", f.Namespace)

		By(fmt.Sprintf(
			"setting up infra-bao instance %q in namespace %q (production mode with TLS)",
			infraBaoName,
			f.Namespace,
		))
		infraCfg := e2ehelpers.InfraBaoConfig{
			Namespace: f.Namespace,
			Name:      infraBaoName,
			Image:     openBaoImage,
		}
		Expect(e2ehelpers.EnsureInfraBao(ctx, cfg, c, infraCfg)).To(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Infra-bao instance %q is running in production mode with TLS\n", infraBaoName)

		var errRead error
		infraBaoRootToken, errRead = e2ehelpers.ReadInfraBaoRootToken(ctx, c, f.Namespace, infraBaoName)
		Expect(errRead).NotTo(HaveOccurred())

		infraBaoTLSCACert, errRead = e2ehelpers.ReadInfraBaoTLSCACert(ctx, c, f.Namespace, infraBaoName)
		Expect(errRead).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "Fetched infra-bao CA bundle for %q\n", infraBaoName)

		By("configuring PKI secrets engine with ACME support on infra-bao")
		// Use HTTPS since infra-bao is configured with TLS for ACME tests
		infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
		clusterPath := infraAddr + "/v1/pki"

		result, err := e2ehelpers.ConfigureInfraBaoPKIACME(
			ctx,
			cfg,
			c,
			f.Namespace,
			infraBaoName,
			openBaoImage,
			infraAddr,
			clusterPath,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Phase).To(Equal(corev1.PodSucceeded), "infra-bao pki/acme setup failed, logs:\n%s", result.Logs)
		_, _ = fmt.Fprintf(GinkgoWriter, "PKI secrets engine configured with ACME support at path %q\n", clusterPath)

		By("configuring transit secrets engine on infra-bao")
		result, err = e2ehelpers.ConfigureInfraBaoTransit(
			ctx,
			cfg,
			c,
			f.Namespace,
			infraBaoName,
			openBaoImage,
			infraAddr,
			infraBaoKeyName,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Phase).To(Equal(corev1.PodSucceeded), "infra-bao transit setup failed, logs:\n%s", result.Logs)
		_, _ = fmt.Fprintf(GinkgoWriter, "Transit secrets engine configured with key %q\n", infraBaoKeyName)

		By("fetching PKI CA certificate from infra-bao for ACME certificate verification")
		infraBaoPKICA, err = e2ehelpers.FetchInfraBaoPKICA(ctx, cfg, c, f.Namespace, infraBaoName, openBaoImage, infraAddr)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraBaoPKICA).NotTo(BeEmpty(), "PKI CA certificate should not be empty")
		_, _ = fmt.Fprintf(GinkgoWriter, "Fetched PKI CA certificate from infra-bao (length: %d bytes)\n", len(infraBaoPKICA))
	})

	AfterAll(func() {
		if f == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		_ = f.Cleanup(cleanupCtx)
	})

	It("provisions tenant RBAC via OpenBaoTenant", Label(
		"case:cluster-tls-acme-provisions-tenant-rbac-via-openbaotenant-cbd56667",
	), func() {
		By("verifying OpenBaoTenant is provisioned")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoTenant{}
			g.Expect(c.Get(ctx, types.NamespacedName{
				Name:      f.TenantName,
				Namespace: operatorNamespace,
			}, updated)).To(Succeed())
			_, _ = fmt.Fprintf(
				GinkgoWriter,
				"OpenBaoTenant status: Provisioned=%v, LastError=%q\n",
				updated.Status.Provisioned,
				updated.Status.LastError,
			)
			g.Expect(updated.Status.Provisioned).To(BeTrue())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Tenant %q successfully provisioned\n", f.TenantName)
	})

	It("creates an ACME TLS cluster and becomes Ready (no TLS secrets mounted)", Label(
		"case:tls-acme-self-init-ready",
	), func() {
		var err error

		By("setting up ACME service and domain")
		// Use HTTPS for ACME directory URL as OpenBao requires HTTPS for non-internal CAs
		// Note: infra-bao is running in dev mode (HTTP), but for ACME tests we need HTTPS.
		// The directory URL must use HTTPS even if infra-bao itself uses HTTP internally.
		// In a real scenario, the ACME CA would be external (e.g., Let's Encrypt) and use HTTPS.
		infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
		acmeDirectoryURL := infraAddr + "/v1/pki/acme/directory"

		// The operator creates a dedicated Service at ports 80/443 to support ACME validation
		// while pods are not yet Ready (PublishNotReadyAddresses=true).
		acmeServiceName := clusterName + "-acme"
		acmeDomain := fmt.Sprintf("%s.%s.svc", acmeServiceName, f.Namespace)
		_, _ = fmt.Fprintf(GinkgoWriter, "ACME directory URL: %q, domain: %q\n", acmeDirectoryURL, acmeDomain)

		By("creating transit token secret for auto-unseal (include TLS CA for transit and PKI CA for ACME)")
		err = e2ehelpers.EnsureInfraBaoSealCredentialsSecret(
			ctx,
			c,
			f.Namespace,
			infraBaoTokenSecretName,
			infraBaoRootToken,
			infraBaoTLSCACert,
			infraBaoPKICA,
		)
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "Created transit token secret %q\n", infraBaoTokenSecretName)

		// Verify the token in the secret works with infra-bao before the cluster tries to use it
		By("verifying transit token secret can access infra-bao transit key")
		// Use HTTPS and skip TLS verification for test environment
		infraAddr = fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
		verifyTokenPod := newTransitEncryptVerifyPod(
			"verify-transit-token-acme",
			f.Namespace,
			openBaoImage,
			infraAddr,
			infraBaoRootToken,
			infraBaoKeyName,
		)
		verifyResult, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, c, verifyTokenPod, 30*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(verifyResult.Phase).To(Equal(corev1.PodSucceeded), "Token verification failed, logs:\n%s", verifyResult.Logs)
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified transit token can encrypt with transit key %q\n", infraBaoKeyName)
		_ = e2ehelpers.DeletePodBestEffort(ctx, c, f.Namespace, verifyTokenPod.Name)

		By("preparing ACME shared cache storage")
		acmeCacheClaimName := clusterName + "-acme-cache"
		acmeCacheStorageClassName, cleanupACMECacheStorage, err := ensureACMEE2ECacheStorage(
			ctx,
			cfg,
			c,
			openBaoImage,
			f.Namespace,
			acmeCacheClaimName,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cleanupACMECacheStorage(cleanupCtx)
		})
		_, _ = fmt.Fprintf(GinkgoWriter, "ACME cache StorageClass: %q\n", acmeCacheStorageClassName)

		By(fmt.Sprintf("creating OpenBaoCluster %q with ACME TLS mode", clusterName))
		acmeCacheStorageClass := ptr.To(acmeCacheStorageClassName)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: f.Namespace,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				// Hardened profile with 3 replicas (VAP requires >= 3 for Raft quorum HA)
				Profile:  openbaov1alpha1.ProfileHardened,
				Version:  openBaoVersion,
				Image:    openBaoImage,
				Replicas: 3, // Minimum for Hardened profile (VAP rule)
				InitContainer: &openbaov1alpha1.InitContainerConfig{
					Enabled: true,
					Image:   hardenedConfigInitImage,
				},
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: true,
					Requests: []openbaov1alpha1.SelfInitRequest{
						{
							Name:      "enable-userpass-auth",
							Operation: openbaov1alpha1.SelfInitOperationUpdate,
							Path:      "sys/auth/userpass",
							AuthMethod: &openbaov1alpha1.SelfInitAuthMethod{
								Type: "userpass",
								Config: map[string]string{
									"default_lease_ttl":  "0",
									"max_lease_ttl":      "0",
									"listing_visibility": "unauth",
								},
							},
						},
						{
							Name:      "create-admin-policy",
							Operation: openbaov1alpha1.SelfInitOperationUpdate,
							Path:      "sys/policies/acl/admin",
							Policy: &openbaov1alpha1.SelfInitPolicy{
								Policy: `path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}`,
							},
						},
					},
				},
				TLS: openbaov1alpha1.TLSConfig{
					Enabled: true,
					Mode:    openbaov1alpha1.TLSModeACME,
					ACME: &openbaov1alpha1.ACMEConfig{
						DirectoryURL: acmeDirectoryURL,
						Email:        "e2e@example.invalid",
						SharedCache: &openbaov1alpha1.ACMESharedCacheConfig{
							Mode:             openbaov1alpha1.ACMESharedCacheModeManagedPVC,
							Size:             "1Gi",
							StorageClassName: acmeCacheStorageClass,
						},
					},
				},
				Configuration: &openbaov1alpha1.OpenBaoConfiguration{
					ACMECARoot: "/etc/bao/seal-creds/ca.crt", // TLS CA for verifying ACME directory server (infra-bao)
				},
				Unseal: &openbaov1alpha1.UnsealConfig{
					Type: "transit",
					Transit: &openbaov1alpha1.TransitSealConfig{
						Address:   infraAddr,
						MountPath: "transit",
						KeyName:   infraBaoKeyName,
						Token:     "", // Token is provided via token_file in Secret
						TLSCACert: "/etc/bao/seal-creds/ca.crt",
						// Note: tls_skip_verify is not set because:
						// 1. Hardened profile validation policy disallows tls_skip_verify=true
						// 2. When using HTTP (not HTTPS), TLS verification is not applicable
					},
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: infraBaoTokenSecretName,
					},
				},
				Storage: openbaov1alpha1.StorageConfig{
					Size: "1Gi",
				},
				Network: &openbaov1alpha1.NetworkConfig{
					APIServerCIDR:        apiServerCIDR,
					APIServerEndpointIPs: apiServerEndpointIPs,
					TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
						{
							// Allow ingress from infra-bao for ACME validation.
							// The ACME server (infra-bao) needs to connect for both:
							// - HTTP-01 challenge: service port 80 -> pod port 8200
							// - TLS-ALPN-01 challenge: service port 443 -> pod port 8200
							// The test verifier pod also needs access so the suite can prove the issued
							// certificate is trusted over the Service hostname.
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": infraBaoName,
								},
							},
						},
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"role": "test-verifier",
								},
							},
						},
					},
					EgressRules: []networkingv1.NetworkPolicyEgressRule{
						{
							// Allow egress to infra-bao in the same namespace for ACME directory access
							To: []networkingv1.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": infraBaoName,
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: &[]corev1.Protocol{corev1.ProtocolTCP}[0],
									Port:     &[]intstr.IntOrString{intstr.FromInt(8200)}[0],
								},
							},
						},
					},
				},
				// Ensure the headless service exists (used for raft), and our additional
				// ACME validation service routes to the same pods.
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		desiredReplicas := cluster.Spec.Replicas
		Expect(c.Create(ctx, cluster)).To(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Created OpenBaoCluster %q with ACME TLS\n", clusterName)
		DeferCleanup(func() { _ = c.Delete(ctx, cluster) })

		By("waiting for OpenBaoCluster to be observed by the API server")
		Eventually(func() error {
			return c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, &openbaov1alpha1.OpenBaoCluster{})
		}, 30*time.Second, 1*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "OpenBaoCluster %q observed by API server\n", clusterName)

		By("verifying TLS secrets are NOT created (ACME mode)")
		Consistently(func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: clusterName + "-tls-server", Namespace: f.Namespace}, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, 30*time.Second, 2*time.Second).Should(BeTrue(), "tls-server Secret should not exist in ACME mode")
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified tls-server Secret does not exist (as expected for ACME mode)\n")

		Consistently(func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: clusterName + "-tls-ca", Namespace: f.Namespace}, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, 30*time.Second, 2*time.Second).Should(BeTrue(), "tls-ca Secret should not exist in ACME mode")
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified tls-ca Secret does not exist (as expected for ACME mode)\n")

		By("checking for prerequisite resources (ConfigMap)")
		Eventually(func(g Gomega) {
			// Check for ConfigMap (required before StatefulSet creation)
			cm := &corev1.ConfigMap{}
			cmName := types.NamespacedName{Name: clusterName + "-config", Namespace: f.Namespace}
			err := c.Get(ctx, cmName, cm)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "ConfigMap %q not found yet: %v\n", cmName.Name, err)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "ConfigMap %q exists\n", cmName.Name)
			}

			// Check cluster status for errors
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())

			// Log all conditions
			for _, cond := range updated.Status.Conditions {
				_, _ = fmt.Fprintf(GinkgoWriter, "Cluster condition: %s=%s reason=%s message=%q\n",
					cond.Type, cond.Status, cond.Reason, cond.Message)
			}

			// Check for degraded condition
			degraded := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionDegraded))
			if degraded != nil && degraded.Status == metav1.ConditionTrue {
				_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: Cluster is Degraded: %s\n", degraded.Message)
			}

			// Check TLSReady condition (should be True or not set for ACME mode)
			tlsReady := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionTLSReady))
			if tlsReady != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "TLSReady condition: status=%s reason=%s message=%q\n",
					tlsReady.Status, tlsReady.Reason, tlsReady.Message)
			}
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("waiting for StatefulSet to be created")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet %q not found yet: %v\n", clusterName, err)
				g.Expect(err).NotTo(HaveOccurred())
			}
			g.Expect(sts.Spec.Replicas).NotTo(BeNil())
			_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet %q exists with replicas=%d (ready=%d)\n",
				clusterName, *sts.Spec.Replicas, sts.Status.ReadyReplicas)
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet %q created successfully\n", clusterName)

		By("waiting for the first ACME pod to become Ready after self-init")
		Eventually(func(g Gomega) {
			pod := &corev1.Pod{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName + "-0", Namespace: f.Namespace}, pod)).To(Succeed())
			var restartCount int32
			for _, status := range pod.Status.ContainerStatuses {
				restartCount += status.RestartCount
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "Pod %q phase=%s ready=%t restarts=%d\n",
				pod.Name, pod.Status.Phase, isPodReady(pod), restartCount)
			logACMEPodContainerStates(pod)
			g.Expect(isPodReady(pod)).To(BeTrue())
		}, 5*time.Minute, 5*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Pod %q completed ACME self-init and became Ready\n", clusterName+"-0")

		By("waiting for StatefulSet pods to reach the desired replica count")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet status: spec=%d replicas=%d ready=%d updated=%d desired=%d\n",
				ptr.Deref(sts.Spec.Replicas, 0), sts.Status.Replicas, sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, desiredReplicas)
			g.Expect(sts.Status.Replicas).To(Equal(desiredReplicas))
			g.Expect(sts.Status.UpdatedReplicas).To(Equal(desiredReplicas))
			g.Expect(sts.Status.ReadyReplicas).To(Equal(desiredReplicas))
		}, 10*time.Minute, 5*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet %q pods are Ready\n", clusterName)

		By("waiting for the ACME shared cache PVC to become Bound")
		Eventually(func(g Gomega) {
			pvc := &corev1.PersistentVolumeClaim{}
			g.Expect(c.Get(ctx, types.NamespacedName{
				Name:      acmeCacheClaimName,
				Namespace: f.Namespace,
			}, pvc)).To(Succeed())
			_, _ = fmt.Fprintf(
				GinkgoWriter,
				"ACME cache PVC %q phase=%s accessModes=%v\n",
				pvc.Name,
				pvc.Status.Phase,
				pvc.Spec.AccessModes,
			)
			g.Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany))
			g.Expect(pvc.Status.Phase).To(Equal(corev1.ClaimBound))
		}, 5*time.Minute, 5*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Managed ACME shared cache PVC is Bound\n")

		By("verifying documented ACME readiness conditions")
		f.WaitForCondition(clusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		f.WaitForConditionReason(
			clusterName,
			openbaov1alpha1.ConditionACMEIntegrationReady,
			metav1.ConditionTrue,
			"ACMEIntegrationReady",
		)
		f.WaitForConditionReason(clusterName, openbaov1alpha1.ConditionACMECacheReady, metav1.ConditionTrue, "ACMECacheReady")
		f.WaitForConditionReason(
			clusterName,
			openbaov1alpha1.ConditionProductionReady,
			metav1.ConditionTrue,
			"ProductionReady",
		)

		By("validating that the config contains ACME parameters")
		// Validate that the config rendered by the operator contains the ACME parameters.
		// We run a short-lived pod that queries the OpenBao pod's config via the API server
		// using kubectl is not available here, so we inspect the config ConfigMap instead,
		// which must contain the tls_acme settings when TLS mode is ACME.
		Eventually(func(g Gomega) {
			cm := &corev1.ConfigMap{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName + "-config", Namespace: f.Namespace}, cm)).To(Succeed())
			cfgText := cm.Data["config.hcl"]
			_, _ = fmt.Fprintf(GinkgoWriter, "Checking config.hcl for ACME parameters...\n")
			g.Expect(cfgText).To(ContainSubstring("tls_acme_ca_directory"))
			g.Expect(cfgText).To(ContainSubstring(acmeDirectoryURL))
			g.Expect(cfgText).To(ContainSubstring("tls_acme_domains"))
			g.Expect(cfgText).To(ContainSubstring(acmeDomain))
			g.Expect(cfgText).To(ContainSubstring("tls_acme_cache_path"))
			g.Expect(cfgText).To(ContainSubstring("/bao/acme-cache/certmagic"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(
			GinkgoWriter,
			"ConfigMap contains ACME parameters "+
				"(tls_acme_ca_directory, tls_acme_domains, tls_acme_cache_path)\n",
		)

		By("verifying the ACME-issued certificate is trusted by the PKI CA")
		verifyACMECertPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "verify-acme-cert",
				Namespace: f.Namespace,
				Labels: map[string]string{
					"role": "test-verifier",
				},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: ptr.To(true),
					RunAsUser:    ptr.To(int64(100)),
					RunAsGroup:   ptr.To(int64(1000)),
					FSGroup:      ptr.To(int64(1000)),
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "acme-ca",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: infraBaoTokenSecretName,
								Items: []corev1.KeyToPath{
									{
										Key:  "pki-ca.crt",
										Path: "ca.crt",
									},
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "bao",
						Image: openBaoImage,
						Env: []corev1.EnvVar{
							{Name: "BAO_ADDR", Value: fmt.Sprintf("https://%s.%s.svc:443", acmeServiceName, f.Namespace)},
							{Name: "BAO_CACERT", Value: "/etc/acme-ca/ca.crt"},
							{Name: "BAO_CLIENT_TIMEOUT", Value: "5s"},
						},
						Command: []string{"/bin/sh", "-ec"},
						Args: []string{
							"rc=0; bao status >/tmp/status.txt 2>&1 || rc=$?; cat /tmp/status.txt; exit $rc",
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "acme-ca",
								MountPath: "/etc/acme-ca",
								ReadOnly:  true,
							},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
							RunAsNonRoot: ptr.To(true),
						},
					},
				},
			},
		}
		verifyACMECertResult, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, c, verifyACMECertPod, 45*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(verifyACMECertResult.Phase).To(
			Equal(corev1.PodSucceeded),
			"ACME certificate verification failed, logs:\n%s",
			verifyACMECertResult.Logs,
		)
		_ = e2ehelpers.DeletePodBestEffort(ctx, c, f.Namespace, verifyACMECertPod.Name)
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified ACME certificate trust via bao status against %q\n", acmeDomain)
	})
})

func ensureACMEE2ECacheStorage(
	ctx context.Context,
	cfg *rest.Config,
	c client.Client,
	image string,
	namespace string,
	claimName string,
) (string, func(context.Context), error) {
	if sc := strings.TrimSpace(os.Getenv(acmeCacheStorageClassEnv)); sc != "" {
		return sc, func(context.Context) {}, nil
	}
	if sc := strings.TrimSpace(os.Getenv("E2E_STORAGE_CLASS")); sc != "" {
		return sc, func(context.Context) {}, nil
	}

	storageClassName := namespace + "-acme-rwx"
	pvName := namespace + "-" + claimName
	hostPathType := corev1.HostPathDirectoryOrCreate
	hostPath := fmt.Sprintf("/tmp/openbao-operator-e2e/acme-cache/%s/%s", namespace, claimName)

	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
		Provisioner:       "kubernetes.io/no-provisioner",
		VolumeBindingMode: ptr.To(storagev1.VolumeBindingImmediate),
	}
	if err := c.Create(ctx, storageClass); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", nil, fmt.Errorf("create ACME cache StorageClass %s: %w", storageClassName, err)
	}
	if err := ensureACMECacheHostPathPermissions(ctx, cfg, c, image, namespace, hostPath); err != nil {
		return "", nil, err
	}

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			StorageClassName:              storageClassName,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Namespace:  namespace,
				Name:       claimName,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hostPath,
					Type: &hostPathType,
				},
			},
		},
	}
	if err := c.Create(ctx, pv); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", nil, fmt.Errorf("create ACME cache PersistentVolume %s: %w", pvName, err)
	}

	cleanup := func(cleanupCtx context.Context) {
		_ = c.Delete(cleanupCtx, &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: pvName},
		})
		_ = c.Delete(cleanupCtx, &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: storageClassName},
		})
	}
	return storageClassName, cleanup, nil
}

func ensureACMECacheHostPathPermissions(
	ctx context.Context,
	cfg *rest.Config,
	c client.Client,
	image string,
	namespace string,
	hostPath string,
) error {
	if cfg == nil {
		return errors.New("rest config is required")
	}
	if image == "" {
		return errors.New("image is required")
	}

	podName := acmeCachePermissionPodName(namespace)
	hostPathType := corev1.HostPathDirectoryOrCreate
	prepPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:  ptr.To(int64(0)),
				RunAsGroup: ptr.To(int64(0)),
			},
			Volumes: []corev1.Volume{
				{
					Name: "cache",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: hostPath,
							Type: &hostPathType,
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "permissions",
					Image: image,
					Command: []string{
						"/bin/sh",
						"-ec",
					},
					Args: []string{
						fmt.Sprintf(
							"mkdir -p /cache && chown -R %d:%d /cache && chmod -R u+rwX,g+rwX /cache",
							platformconstants.UserOpenBao,
							platformconstants.GroupOpenBao,
						),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "cache",
							MountPath: "/cache",
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false),
						RunAsUser:                ptr.To(int64(0)),
						RunAsGroup:               ptr.To(int64(0)),
					},
				},
			},
		},
	}

	result, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, c, prepPod, time.Minute)
	defer func() {
		_ = e2ehelpers.DeletePodBestEffort(context.Background(), c, prepPod.Namespace, prepPod.Name)
	}()
	if err != nil {
		return fmt.Errorf("prepare ACME cache hostPath permissions: %w", err)
	}
	if result.Phase != corev1.PodSucceeded {
		return fmt.Errorf("prepare ACME cache hostPath permissions failed, phase=%s logs:\n%s", result.Phase, result.Logs)
	}
	return nil
}

func acmeCachePermissionPodName(namespace string) string {
	name := namespace + "-acme-cache-perms"
	if len(name) <= 63 {
		return name
	}
	return strings.TrimRight(name[:63], "-")
}

func logACMEPodContainerStates(pod *corev1.Pod) {
	for _, status := range pod.Status.InitContainerStatuses {
		logACMEContainerState("init", status)
	}
	for _, status := range pod.Status.ContainerStatuses {
		logACMEContainerState("container", status)
	}
}

func logACMEContainerState(prefix string, status corev1.ContainerStatus) {
	_, _ = fmt.Fprintf(
		GinkgoWriter,
		"%s %q ready=%t restartCount=%d state=%s lastState=%s\n",
		prefix,
		status.Name,
		status.Ready,
		status.RestartCount,
		formatACMEContainerState(status.State),
		formatACMEContainerState(status.LastTerminationState),
	)
}

func formatACMEContainerState(state corev1.ContainerState) string {
	switch {
	case state.Waiting != nil:
		message := strings.TrimSpace(state.Waiting.Message)
		if message != "" {
			return fmt.Sprintf("Waiting(reason=%s,message=%q)", state.Waiting.Reason, message)
		}
		return fmt.Sprintf("Waiting(reason=%s)", state.Waiting.Reason)
	case state.Running != nil:
		return "Running"
	case state.Terminated != nil:
		message := strings.TrimSpace(state.Terminated.Message)
		if message != "" {
			return fmt.Sprintf(
				"Terminated(reason=%s,exitCode=%d,message=%q)",
				state.Terminated.Reason,
				state.Terminated.ExitCode,
				message,
			)
		}
		return fmt.Sprintf("Terminated(reason=%s,exitCode=%d)", state.Terminated.Reason, state.Terminated.ExitCode)
	default:
		return "Unknown"
	}
}
