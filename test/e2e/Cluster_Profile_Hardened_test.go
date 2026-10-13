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
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Hardened profile (External TLS + Transit auto-unseal + SelfInit)", Label(
	"profile-hardened",
	"security",
	"cluster",
), Ordered, func() {
	ctx := context.Background()

	var (
		cfg    *rest.Config
		scheme *runtime.Scheme
		c      client.Client
		f      *framework.Framework
	)

	const (
		clusterName = "hardened-cluster"

		infraBaoName            = "infra-bao"
		infraBaoKeyName         = "openbao-unseal"
		infraBaoTokenSecretName = "infra-bao-token"
	)

	var infraBaoRootToken string

	waitForTenantProvisioned := func() {
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
			g.Expect(updated.Status.LastError).To(BeEmpty())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	}

	waitForNetworkPolicy := func(name types.NamespacedName, timeout, pollInterval time.Duration) error {
		deadline := time.Now().Add(timeout)
		var lastErr error

		for {
			np := &networkingv1.NetworkPolicy{}
			err := c.Get(ctx, name, np)
			if err == nil {
				return nil
			}
			if !apierrors.IsNotFound(err) {
				return err
			}
			lastErr = err

			if time.Now().After(deadline) {
				return fmt.Errorf("timed out waiting for NetworkPolicy %s/%s: %w", name.Namespace, name.Name, lastErr)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
		}
	}

	dumpNetworkPolicyDiagnostics := func(namespace, clusterName string) {
		_, _ = fmt.Fprintf(GinkgoWriter, "\n========== NetworkPolicy Diagnostics (%s/%s) ==========\n", namespace, clusterName)
		dumpKubectlOutput("get", "openbaocluster", clusterName, "-n", namespace, "-o", "yaml")
		dumpKubectlOutput("get", "networkpolicies", "-n", namespace, "-o", "wide")
		dumpKubectlOutput("get", "pods", "-n", namespace, "-l", fmt.Sprintf("%s=%s", constants.LabelOpenBaoCluster, clusterName), "-o", "wide")
		dumpKubectlOutput("get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
		dumpKubectlOutput("logs", "deployment/openbao-operator-controller", "-n", operatorNamespace, "--tail=400")
	}

	ensureTransitTokenSecret := func() {
		By("creating transit token secret with CA certificate for TLS verification")
		infraBaoCACert, err := e2ehelpers.ReadInfraBaoTLSCACert(ctx, c, f.Namespace, infraBaoName)
		Expect(err).NotTo(HaveOccurred())

		Expect(e2ehelpers.EnsureInfraBaoSealCredentialsSecret(
			ctx,
			c,
			f.Namespace,
			infraBaoTokenSecretName,
			infraBaoRootToken,
			infraBaoCACert,
			nil,
		)).To(Succeed())

		Eventually(func(g Gomega) {
			created := &corev1.Secret{}
			g.Expect(c.Get(ctx, types.NamespacedName{
				Name:      infraBaoTokenSecretName,
				Namespace: f.Namespace,
			}, created)).To(Succeed())
			g.Expect(strings.TrimSpace(string(created.Data["token"]))).To(Equal(strings.TrimSpace(infraBaoRootToken)))
			g.Expect(created.Data["ca.crt"]).To(Equal(infraBaoCACert))
		}, 10*time.Second, 1*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified transit token secret %q\n", infraBaoTokenSecretName)
	}

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

		f, err = framework.New(ctx, c, "hardened", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "Created namespace %q\n", f.Namespace)

		By(fmt.Sprintf("setting up infra-bao instance %q in namespace %q", infraBaoName, f.Namespace))
		infraCfg := e2ehelpers.InfraBaoConfig{
			Namespace: f.Namespace,
			Name:      infraBaoName,
			Image:     openBaoImage,
		}
		Expect(e2ehelpers.EnsureInfraBao(ctx, cfg, c, infraCfg)).To(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Infra-bao instance %q is running\n", infraBaoName)

		var errRead error
		infraBaoRootToken, errRead = e2ehelpers.ReadInfraBaoRootToken(ctx, c, f.Namespace, infraBaoName)
		Expect(errRead).NotTo(HaveOccurred())

		By("configuring transit secrets engine on infra-bao")
		// Infra-bao always runs with TLS in production mode
		infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
		result, err := e2ehelpers.ConfigureInfraBaoTransit(
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

		By("verifying root token can access transit key (test encryption)")
		// Pull the actual root token from infra-bao init secret
		verifyToken := strings.TrimSpace(infraBaoRootToken)
		verifySecret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      "infra-bao-root-token",
			Namespace: f.Namespace,
		}, verifySecret); err == nil {
			if data, ok := verifySecret.Data["token"]; ok && len(data) > 0 {
				verifyToken = string(data)
			}
		}

		verifyPod := newTransitEncryptVerifyPod(
			"verify-transit-token",
			f.Namespace,
			openBaoImage,
			infraAddr,
			verifyToken,
			infraBaoKeyName,
		)
		verifyResult, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, c, verifyPod, 30*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(verifyResult.Phase).To(
			Equal(corev1.PodSucceeded),
			"Root token verification failed, logs:\n%s",
			verifyResult.Logs,
		)
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified root token can encrypt with transit key %q\n", infraBaoKeyName)
		_ = e2ehelpers.DeletePodBestEffort(ctx, c, f.Namespace, verifyPod.Name)
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
		"case:cluster-profile-hardened-provisions-tenant-rbac-via-openbaotenant-90cecec2",
	), func() {
		By("verifying OpenBaoTenant is provisioned")
		waitForTenantProvisioned()
		_, _ = fmt.Fprintf(GinkgoWriter, "Tenant %q successfully provisioned\n", f.TenantName)
	})

	It("creates a Hardened cluster that self-initializes and stays unsealed across restarts", Label(
		"case:hardened-self-init-auto-unseal",
	), func() {
		By("creating external TLS secrets required for TLS mode External")
		Expect(e2ehelpers.EnsureExternalTLSSecrets(ctx, c, f.Namespace, clusterName, 1)).To(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Created external TLS secrets for cluster %q\n", clusterName)

		ensureTransitTokenSecret()

		By("verifying transit token secret can be read from file and access infra-bao transit key")
		// Infra-bao always runs with TLS in production mode
		infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
		verifyTokenPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "verify-transit-token-hardened",
				Namespace: f.Namespace,
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
				Containers: []corev1.Container{
					{
						Name:  "bao",
						Image: openBaoImage,
						Env: []corev1.EnvVar{
							{Name: "BAO_ADDR", Value: infraAddr},
							// Read token from file (same way hardened cluster does)
							{Name: "BAO_TOKEN", Value: ""},
							// Skip TLS verification for self-signed certificates in test environment
							{Name: "BAO_SKIP_VERIFY", Value: "true"},
						},
						Command: []string{"/bin/sh", "-ec"},
						Args: []string{
							// Read token from file and trim whitespace, then test encryption
							// Also debug: show the raw file contents and length
							`echo "DEBUG: Token file contents (hex): $(cat /etc/bao/seal-creds/token | od -An -tx1 | tr -d ' \n')"
							echo "DEBUG: Token file length: $(cat /etc/bao/seal-creds/token | wc -c)"
							TOKEN=$(cat /etc/bao/seal-creds/token | tr -d '\n\r' | xargs)
							if [ -z "$TOKEN" ]; then
								echo "ERROR: Token file is empty"
								exit 1
							fi
							echo "DEBUG: Trimmed token: $TOKEN"
							export BAO_TOKEN="$TOKEN"
							` + fmt.Sprintf(
								"bao write -format=json transit/encrypt/%s "+
									"plaintext=$(echo -n 'test' | base64) >/dev/null && echo 'ok'",
								infraBaoKeyName,
							),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "seal-creds",
								MountPath: "/etc/bao/seal-creds",
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
				Volumes: []corev1.Volume{
					{
						Name: "seal-creds",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: infraBaoTokenSecretName,
							},
						},
					},
				},
			},
		}
		verifyResult, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, c, verifyTokenPod, 30*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(verifyResult.Phase).To(
			Equal(corev1.PodSucceeded),
			"Token file verification failed, logs:\n%s",
			verifyResult.Logs,
		)
		_, _ = fmt.Fprintf(
			GinkgoWriter,
			"Verified transit token file can be read and used to encrypt with transit key %q\n",
			infraBaoKeyName,
		)
		_ = e2ehelpers.DeletePodBestEffort(ctx, c, f.Namespace, verifyTokenPod.Name)

		By(fmt.Sprintf("creating Hardened OpenBaoCluster %q with External TLS and Transit auto-unseal", clusterName))
		// Infra-bao always runs with TLS in production mode
		infraAddr = fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
		recoveryKeyRecipients := e2eRecoveryKeyRecipients()

		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: f.Namespace,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile:  openbaov1alpha1.ProfileHardened,
				Version:  openBaoVersion,
				Image:    openBaoImage,
				Replicas: 3,
				InitContainer: &openbaov1alpha1.InitContainerConfig{
					Enabled: true,
					Image:   hardenedConfigInitImage,
				},
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: true,
					OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
						Enabled: true,
					},
					Requests: append(
						e2ehelpers.CreateHardenedProfileRequests(f.Namespace),
						e2ehelpers.CreateJWTPolicyRoleRequests(
							f.Namespace,
							"default",
							"recovery-backup-read",
							"recovery-backup-read",
							`path "sys/rotate/recovery/backup" { capabilities = ["read"] }`,
						)...,
					),
				},
				RecoveryKeys: &openbaov1alpha1.RecoveryKeysConfig{
					Initial: &openbaov1alpha1.InitialRecoveryKeysConfig{
						Shares:     3,
						Threshold:  2,
						Recipients: recoveryKeyRecipients,
					},
				},
				TLS: openbaov1alpha1.TLSConfig{
					Enabled: true,
					Mode:    openbaov1alpha1.TLSModeExternal,
				},
				// Establish a stable ClusterIP Service for verification (DNS is more reliable than Headless in Kind)
				Service: &openbaov1alpha1.ServiceConfig{
					Type: "ClusterIP",
				},
				Unseal: &openbaov1alpha1.UnsealConfig{
					Type: "transit",
					Transit: &openbaov1alpha1.TransitSealConfig{
						Address:   infraAddr,
						MountPath: "transit",
						KeyName:   infraBaoKeyName,
						Token:     "", // Token is provided via VAULT_TOKEN environment variable
						TLSCACert: "/etc/bao/seal-creds/ca.crt",
						// Note: token is provided via VAULT_TOKEN environment variable
						// (set by the operator from CredentialsSecretRef) to avoid issues
						// with trailing newlines in mounted Secret files.
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
					EgressRules: []networkingv1.NetworkPolicyEgressRule{
						{
							// Allow egress to infra-bao in the same namespace for transit seal backend
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
					TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"role": "test-verifier",
								},
							},
						},
					},
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}

		Expect(c.Create(ctx, cluster)).To(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Created OpenBaoCluster %q\n", clusterName)

		By("waiting for OpenBaoCluster to be observed by the API server")
		Eventually(func() error {
			return c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, &openbaov1alpha1.OpenBaoCluster{})
		}, 30*time.Second, 1*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "OpenBaoCluster %q observed by API server\n", clusterName)

		By("verifying NetworkPolicy was created")
		npName := types.NamespacedName{Name: clusterName + "-network-policy", Namespace: f.Namespace}
		err = waitForNetworkPolicy(npName, framework.DefaultWaitTimeout, framework.DefaultPollInterval)
		if err != nil {
			dumpNetworkPolicyDiagnostics(f.Namespace, clusterName)
		}
		Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "NetworkPolicy created successfully\n")

		By("checking for prerequisite resources (ConfigMap and TLS Secrets)")
		Eventually(func(g Gomega) {
			// Check for ConfigMap
			cm := &corev1.ConfigMap{}
			cmName := types.NamespacedName{Name: clusterName + "-config", Namespace: f.Namespace}
			err := c.Get(ctx, cmName, cm)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "ConfigMap %q not found yet: %v\n", cmName.Name, err)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "ConfigMap %q exists\n", cmName.Name)
			}

			// Check for TLS Secrets (External mode requires both CA and server secrets)
			tlsCASecret := &corev1.Secret{}
			tlsCASecretName := types.NamespacedName{Name: clusterName + "-tls-ca", Namespace: f.Namespace}
			err = c.Get(ctx, tlsCASecretName, tlsCASecret)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "TLS CA Secret %q not found yet: %v\n", tlsCASecretName.Name, err)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "TLS CA Secret %q exists\n", tlsCASecretName.Name)
			}

			tlsServerSecret := &corev1.Secret{}
			tlsServerSecretName := types.NamespacedName{Name: clusterName + "-tls-server", Namespace: f.Namespace}
			err = c.Get(ctx, tlsServerSecretName, tlsServerSecret)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "TLS server Secret %q not found yet: %v\n", tlsServerSecretName.Name, err)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "TLS server Secret %q exists\n", tlsServerSecretName.Name)
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

			// Check TLSReady condition
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

		By("waiting for the StatefulSet pod to become Ready (proves auto-unseal worked)")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet status: replicas=%d ready=%d updated=%d\n",
				sts.Status.Replicas, sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas)
			g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))
		}, 8*time.Minute, 5*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet %q pod is Ready (auto-unseal successful)\n", clusterName)

		By("waiting for status.initialized=true (self-init, no operator init)")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, updated)).To(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "Cluster status: Initialized=%v SelfInitialized=%v ReadyReplicas=%d\n",
				updated.Status.Initialized, updated.Status.SelfInitialized, updated.Status.ReadyReplicas)

			// Check StatefulSet status for debugging
			sts := &appsv1.StatefulSet{}
			if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts); err == nil {
				var specReplicas int32
				if sts.Spec.Replicas != nil {
					specReplicas = *sts.Spec.Replicas
				}
				_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet status: spec.replicas=%d ready=%d current=%d updated=%d\n",
					specReplicas, sts.Status.ReadyReplicas, sts.Status.CurrentReplicas, sts.Status.UpdatedReplicas)
			}

			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			if available != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Available condition: status=%s reason=%s message=%q\n",
					available.Status, available.Reason, available.Message)
			}
			g.Expect(updated.Status.Initialized).To(BeTrue())
			g.Expect(updated.Status.SelfInitialized).To(BeTrue())

			available = meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, 8*time.Minute, 5*time.Second).Should(Succeed())

		// Trigger a reconcile to ensure status is updated promptly after all replicas are ready.
		// The controller now has requeue logic, but this ensures immediate status update in tests.
		By("triggering reconcile to ensure status is updated")
		Expect(f.TriggerReconcile(ctx, clusterName)).To(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Triggered reconcile for cluster %q\n", clusterName)
		_, _ = fmt.Fprintf(GinkgoWriter, "Cluster %q is initialized via self-init\n", clusterName)

		By("verifying the encrypted recovery-key backup exists for the declared recipients")
		Eventually(func(g Gomega) {
			baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, clusterName)
			g.Expect(err).NotTo(HaveOccurred())

			backupJSON, err := e2ehelpers.RunCommandViaJWT(
				ctx,
				cfg,
				c,
				f.Namespace,
				openBaoImage,
				baoAddr,
				"default",
				"recovery-backup-read",
				map[string]string{"role": "test-verifier"},
				"bao read -format=json sys/rotate/recovery/backup",
			)
			g.Expect(err).NotTo(HaveOccurred())
			for _, recipient := range recoveryKeyRecipients {
				g.Expect(strings.ToUpper(backupJSON)).To(ContainSubstring(strings.ToUpper(recipient.Fingerprint)))
			}
		}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

		By("verifying the documented hardened production readiness condition")
		f.WaitForConditionReason(
			clusterName,
			openbaov1alpha1.ConditionProductionReady,
			metav1.ConditionTrue,
			"ProductionReady",
		)

		By("asserting root token and static unseal secrets do NOT exist")
		Consistently(func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: clusterName + "-root-token", Namespace: f.Namespace}, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, 15*time.Second, 1*time.Second).Should(BeTrue())
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified root-token Secret does not exist (as expected for self-init)\n")

		Consistently(func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: clusterName + "-unseal-key", Namespace: f.Namespace}, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, 15*time.Second, 1*time.Second).Should(BeTrue())
		_, _ = fmt.Fprintf(GinkgoWriter, "Verified unseal-key Secret does not exist (using Transit auto-unseal)\n")

		// Note: Hardened profile now requires >= 3 replicas (VAP rule), so we cannot
		// test restart by scaling down. Instead, we delete pods directly to verify
		// that auto-unseal works after pod restart.
		//
		// ValidatingAdmissionPolicy blocks direct mutations of OpenBao-managed resources
		// unless maintenance mode is enabled on the object and the caller is authorized
		// for the custom maintenance verb on the owning OpenBaoCluster.
		maintenanceGroup := "e2e-hardened-maintainers"
		maintenanceRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "e2e-hardened-maintenance",
				Namespace: f.Namespace,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "delete"},
				},
				{
					APIGroups:     []string{"openbao.org"},
					Resources:     []string{"openbaoclusters"},
					ResourceNames: []string{clusterName},
					Verbs:         []string{"get", "maintenance"},
				},
				{
					APIGroups:     []string{"openbao.org"},
					Resources:     []string{"openbaoclusters/status"},
					ResourceNames: []string{clusterName},
					Verbs:         []string{"get"},
				},
			},
		}
		Expect(e2ehelpers.EnsureRoleBinding(ctx, c, maintenanceRole, []rbacv1.Subject{
			{
				Kind: "Group",
				Name: maintenanceGroup,
			},
		})).To(Succeed())
		Expect(f.SetMaintenanceEnabled(ctx, clusterName, true)).To(Succeed())

		By("deleting pods to verify auto-unseal works after restart (maintenance mode)")
		podList := &corev1.PodList{}
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, podList, client.InNamespace(f.Namespace), client.MatchingLabels{
				"app.kubernetes.io/instance":   clusterName,
				"app.kubernetes.io/name":       "openbao",
				"app.kubernetes.io/managed-by": "openbao-operator",
			})).To(Succeed())
			g.Expect(podList.Items).NotTo(BeEmpty(), "At least one pod should exist")
			for _, pod := range podList.Items {
				g.Expect(pod.Annotations).To(HaveKeyWithValue(constants.AnnotationMaintenance, "true"))
			}
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		// Delete all pods to trigger restart
		for _, pod := range podList.Items {
			_, _ = fmt.Fprintf(GinkgoWriter, "Deleting pod %q to test auto-unseal after restart\n", pod.Name)

			err := e2ehelpers.RunWithImpersonation(ctx, cfg, scheme,
				"e2e-hardened-maintainer",
				[]string{"system:authenticated", maintenanceGroup},
				func(ic client.Client) error {
					return ic.Delete(ctx, &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      pod.Name,
							Namespace: pod.Namespace,
						},
					})
				},
			)
			Expect(err).NotTo(HaveOccurred())
		}

		// Wait for pods to be recreated and become Ready
		clusterObj := &openbaov1alpha1.OpenBaoCluster{}
		Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, clusterObj)).To(Succeed())
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)).To(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "StatefulSet status after pod restart: replicas=%d ready=%d\n",
				sts.Status.Replicas, sts.Status.ReadyReplicas)
			g.Expect(sts.Status.ReadyReplicas).To(Equal(clusterObj.Spec.Replicas))
		}, 8*time.Minute, 5*time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "Pods restarted and became Ready (Transit auto-unseal working)\n")
	})

	Context("Hardened Rolling Upgrade", Label("upgrade", "rolling", "hardened"), func() {
		const hardenedUpgradeClusterName = "hardened-upgrade-cluster"

		var upgradeCluster *openbaov1alpha1.OpenBaoCluster

		AfterEach(func() {
			if !CurrentSpecReport().Failed() || upgradeCluster == nil {
				return
			}

			By("Collecting hardened rolling upgrade diagnostics")
			dumpRollingUpgradeDiagnostics(ctx, c, f.Namespace, upgradeCluster.Name)
		})

		It("performs a hardened rolling upgrade", Label(
			"case:hardened-rolling-upgrade",
		), func() {
			initialVersion := envOrDefault("E2E_UPGRADE_FROM_VERSION", defaultUpgradeFromVersion)
			targetVersion := envOrDefault("E2E_UPGRADE_TO_VERSION", defaultUpgradeToVersion)
			initialImage := fmt.Sprintf("openbao/openbao:%s", initialVersion)
			targetImage := fmt.Sprintf("openbao/openbao:%s", targetVersion)
			upgradeImage := hardenedSignedUpgradeExecutorImage()

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Hardened upgrade test skipped: versions identical (%s)", initialVersion))
			}

			By("verifying the tenant is provisioned for the hardened upgrade cluster")
			waitForTenantProvisioned()

			By("ensuring the transit credentials secret exists for hardened cluster unseal")
			ensureTransitTokenSecret()

			By("creating external TLS secrets for the hardened upgrade cluster")
			Expect(e2ehelpers.EnsureExternalTLSSecrets(ctx, c, f.Namespace, hardenedUpgradeClusterName, 3)).To(Succeed())

			By("creating a hardened cluster configured for rolling upgrades")
			infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
			tcpProto := corev1.ProtocolTCP
			port8200 := intstr.FromInt(8200)
			upgradeCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hardenedUpgradeClusterName,
					Namespace: f.Namespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Version:  initialVersion,
					Image:    initialImage,
					Replicas: 3,
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   hardenedConfigInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
						Requests: append(
							e2ehelpers.CreateHardenedProfileRequests(f.Namespace),
							e2ehelpers.CreateE2ERequests(f.Namespace)...,
						),
					},
					TLS: openbaov1alpha1.TLSConfig{
						Enabled: true,
						Mode:    openbaov1alpha1.TLSModeExternal,
					},
					Service: &openbaov1alpha1.ServiceConfig{
						Type: corev1.ServiceTypeClusterIP,
					},
					Unseal: &openbaov1alpha1.UnsealConfig{
						Type: "transit",
						Transit: &openbaov1alpha1.TransitSealConfig{
							Address:   infraAddr,
							MountPath: "transit",
							KeyName:   infraBaoKeyName,
							Token:     "",
							TLSCACert: "/etc/bao/seal-creds/ca.crt",
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
						EgressRules: []networkingv1.NetworkPolicyEgressRule{
							{
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
										Protocol: &tcpProto,
										Port:     &port8200,
									},
								},
							},
						},
						TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
							{
								PodSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"role": "test-verifier",
									},
								},
							},
						},
					},
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Image:    upgradeImage,
						Strategy: openbaov1alpha1.UpdateStrategyRollingUpdate,
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(c.Create(ctx, upgradeCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.SelfInitialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, 12*time.Minute, framework.DefaultPollInterval).Should(Succeed())
			_, err := f.WaitForStatefulSetReady(
				ctx,
				upgradeCluster.Name,
				3,
				12*time.Minute,
				framework.DefaultPollInterval,
			)
			Expect(err).NotTo(HaveOccurred())

			By("writing a secret before the hardened upgrade")
			secretPath := "secret/hardened-rolling-upgrade-test"
			secretData := map[string]string{"foo": "bar", "version": initialVersion}
			verifierLabels := map[string]string{"role": "test-verifier"}
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					initialImage,
					baoAddr,
					"default",
					"e2e-test",
					secretPath,
					verifierLabels,
					secretData,
				)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed(), "Failed to write pre-upgrade secret")

			By("triggering the hardened rolling upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = targetImage
				g.Expect(c.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Expect(f.TriggerReconcile(ctx, upgradeCluster.Name)).To(Succeed())

			By("verifying the hardened rolling upgrade starts")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).NotTo(BeNil())
				g.Expect(updated.Status.Upgrade.TargetVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Upgrade.FromVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting for the hardened rolling upgrade to complete")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Upgrade).To(BeNil())
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseRunning))

				pods := &corev1.PodList{}
				g.Expect(c.List(ctx, pods,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster:   upgradeCluster.Name,
						constants.LabelOpenBaoComponent: constants.ComponentOpenBaoCluster,
					},
				)).To(Succeed())
				g.Expect(pods.Items).NotTo(BeEmpty())
				for _, pod := range pods.Items {
					e2ehelpers.ExpectOpenBaoPodVersion(g, pod, targetVersion)
				}
			}, 30*time.Minute, 10*time.Second).Should(Succeed())

			By("verifying the test secret persists after the hardened upgrade")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				val, err := e2ehelpers.ReadSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					targetImage,
					baoAddr,
					"default",
					"e2e-test",
					secretPath,
					verifierLabels,
					"foo",
				)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(val).To(Equal("bar"))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())
		})
	})

	Context("Hardened Blue/Green Upgrade", Label("upgrade", "bluegreen", "hardened"), func() {
		const hardenedBlueGreenClusterName = "hardened-bluegreen-cluster"

		var upgradeCluster *openbaov1alpha1.OpenBaoCluster

		AfterEach(func() {
			if !CurrentSpecReport().Failed() || upgradeCluster == nil {
				return
			}

			By("Collecting hardened blue/green upgrade diagnostics")
			dumpBlueGreenUpgradeDiagnostics(f.Namespace, upgradeCluster.Name)
		})

		It("performs a hardened blue/green upgrade", Label(
			"case:hardened-bluegreen-upgrade",
		), func() {
			initialVersion := blueGreenUpgradeFromVersion()
			targetVersion := blueGreenUpgradeToVersion()
			initialImage := fmt.Sprintf("openbao/openbao:%s", initialVersion)
			targetImage := fmt.Sprintf("openbao/openbao:%s", targetVersion)
			upgradeImage := hardenedSignedUpgradeExecutorImage()

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Hardened blue/green upgrade test skipped: versions identical (%s)", initialVersion))
			}

			By("verifying the tenant is provisioned for the hardened blue/green cluster")
			waitForTenantProvisioned()

			By("ensuring the transit credentials secret exists for hardened blue/green unseal")
			ensureTransitTokenSecret()

			By("creating external TLS secrets for the hardened blue/green cluster")
			Expect(e2ehelpers.EnsureExternalTLSSecrets(ctx, c, f.Namespace, hardenedBlueGreenClusterName, 3)).To(Succeed())

			By("creating a hardened cluster configured for blue/green upgrades")
			infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", infraBaoName, f.Namespace)
			tcpProto := corev1.ProtocolTCP
			port8200 := intstr.FromInt(8200)
			upgradeCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hardenedBlueGreenClusterName,
					Namespace: f.Namespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileHardened,
					Version:  initialVersion,
					Image:    initialImage,
					Replicas: 3,
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   hardenedConfigInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
						Requests: append(
							e2ehelpers.CreateHardenedProfileRequests(f.Namespace),
							e2ehelpers.CreateE2ERequests(f.Namespace)...,
						),
					},
					TLS: openbaov1alpha1.TLSConfig{
						Enabled: true,
						Mode:    openbaov1alpha1.TLSModeExternal,
					},
					Service: &openbaov1alpha1.ServiceConfig{
						Type: corev1.ServiceTypeClusterIP,
					},
					Unseal: &openbaov1alpha1.UnsealConfig{
						Type: "transit",
						Transit: &openbaov1alpha1.TransitSealConfig{
							Address:   infraAddr,
							MountPath: "transit",
							KeyName:   infraBaoKeyName,
							Token:     "",
							TLSCACert: "/etc/bao/seal-creds/ca.crt",
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
						EgressRules: []networkingv1.NetworkPolicyEgressRule{
							{
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
										Protocol: &tcpProto,
										Port:     &port8200,
									},
								},
							},
						},
						TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
							{
								PodSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"role": "test-verifier",
									},
								},
							},
						},
					},
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Image:    upgradeImage,
						Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote: true,
							Verification: &openbaov1alpha1.VerificationConfig{
								MinSyncDuration: "10s",
							},
						},
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(c.Create(ctx, upgradeCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.SelfInitialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, 12*time.Minute, framework.DefaultPollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				stsList := &appsv1.StatefulSetList{}
				g.Expect(c.List(ctx, stsList,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster: upgradeCluster.Name,
					},
				)).To(Succeed())
				g.Expect(stsList.Items).NotTo(BeEmpty(), "expected at least one StatefulSet for hardened blue/green cluster")

				var totalReady int32
				for _, sts := range stsList.Items {
					totalReady += sts.Status.ReadyReplicas
				}
				g.Expect(totalReady).To(Equal(upgradeCluster.Spec.Replicas),
					"expected total ready replicas across StatefulSets to match desired cluster replicas")
			}, 12*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("writing a secret before the hardened blue/green upgrade")
			secretPath := "secret/hardened-bluegreen-upgrade-test"
			secretData := map[string]string{"foo": "bar", "version": initialVersion}
			verifierLabels := map[string]string{"role": "test-verifier"}
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					initialImage,
					baoAddr,
					"default",
					"e2e-test",
					secretPath,
					verifierLabels,
					secretData,
				)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed(), "Failed to write pre-upgrade secret")

			By("triggering the hardened blue/green upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = targetImage
				g.Expect(c.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Expect(f.TriggerReconcile(ctx, upgradeCluster.Name)).To(Succeed())

			By("verifying the hardened blue/green upgrade starts")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).NotTo(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting for the hardened blue/green upgrade to complete")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(c.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: f.Namespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseRunning))

				pods := &corev1.PodList{}
				g.Expect(c.List(ctx, pods,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster:   upgradeCluster.Name,
						constants.LabelOpenBaoComponent: constants.ComponentOpenBaoCluster,
					},
				)).To(Succeed())
				g.Expect(pods.Items).NotTo(BeEmpty())
				for _, pod := range pods.Items {
					e2ehelpers.ExpectOpenBaoPodVersion(g, pod, targetVersion)
				}
			}, 30*time.Minute, 10*time.Second).Should(Succeed())

			By("verifying the test secret persists after the hardened blue/green upgrade")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, c, f.Namespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				val, err := e2ehelpers.ReadSecretViaJWT(
					ctx,
					cfg,
					c,
					f.Namespace,
					targetImage,
					baoAddr,
					"default",
					"e2e-test",
					secretPath,
					verifierLabels,
					"foo",
				)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(val).To(Equal("bar"))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())
		})
	})

	It("verifies Raft Autopilot is configured with cleanup_dead_servers enabled", Label(
		"case:cluster-profile-hardened-verifies-raft-autopilot-is-configured-with-f66c15e2",
	), func() {
		By("ensuring public service exists before creating verification pod")
		// Wait for the public service to be created by the operator
		svc := &corev1.Service{}
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, types.NamespacedName{
				Name:      clusterName + "-public",
				Namespace: f.Namespace,
			}, svc)).To(Succeed(), "public service should exist")
			g.Expect(svc.Spec.ClusterIP).NotTo(BeEmpty())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("ensuring public service has ready endpoints")
		// The Service object can exist before kube-proxy has any ready endpoints. In that window,
		// in-cluster clients can see "connection refused" when connecting to the ClusterIP.
		slices := &discoveryv1.EndpointSliceList{}
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, slices,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{"kubernetes.io/service-name": clusterName + "-public"},
			)).To(Succeed(), "public EndpointSlices should be listable")

			ready := 0
			for _, s := range slices.Items {
				for _, ep := range s.Endpoints {
					if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
						continue
					}
					ready += len(ep.Addresses)
				}
			}
			g.Expect(ready).To(BeNumerically(">", 0), "public EndpointSlices should have at least one ready address")
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("allowing verification pod egress to OpenBao cluster (NetworkPolicy)")
		tcpProto := corev1.ProtocolTCP
		udpProto := corev1.ProtocolUDP
		port8200 := intstr.FromInt(8200)
		port53 := intstr.FromInt(53)
		verifierEgress := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName + "-verifier-egress",
				Namespace: f.Namespace,
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "test-verifier"},
				},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress: []networkingv1.NetworkPolicyEgressRule{
					{
						To: []networkingv1.NetworkPolicyPeer{
							{
								NamespaceSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
								},
							},
						},
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: &udpProto, Port: &port53},
							{Protocol: &tcpProto, Port: &port53},
						},
					},
					{
						To: []networkingv1.NetworkPolicyPeer{
							// Allow egress to the Service ClusterIP (some CNIs evaluate policy pre-DNAT).
							{
								IPBlock: &networkingv1.IPBlock{
									CIDR: svc.Spec.ClusterIP + "/32",
								},
							},
							{
								PodSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										constants.LabelOpenBaoCluster: clusterName,
									},
								},
							},
							// Allow egress using the app.kubernetes.io labels (used by several tests and
							// avoids depending on a single label for pod selection).
							{
								PodSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"app.kubernetes.io/instance":   clusterName,
										"app.kubernetes.io/name":       "openbao",
										"app.kubernetes.io/managed-by": "openbao-operator",
									},
								},
							},
						},
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: &tcpProto, Port: &port8200},
						},
					},
				},
			},
		}
		Expect(c.Create(ctx, verifierEgress)).To(Succeed())
		DeferCleanup(func() { _ = c.Delete(ctx, verifierEgress) })

		By("reading autopilot configuration via JWT authenticated request")
		// Self-init clusters have operator JWT auth configured, so we can verify autopilot config
		// The openbao-operator policy has read/update on sys/storage/raft/autopilot/configuration

		// Use VerifyRaftAutopilotViaJWT helper to perform verification
		// Use the ClusterIP for verification to avoid DNS flakes in Kind
		err := e2ehelpers.VerifyRaftAutopilotViaJWT(
			ctx,
			cfg,
			c,
			f.Namespace,
			openBaoImage,
			fmt.Sprintf("https://%s:8200", svc.Spec.ClusterIP),
			"default",
			map[string]string{"role": "test-verifier"},
		)
		Expect(err).NotTo(HaveOccurred(), "Autopilot verification failed")
		_, _ = fmt.Fprintf(GinkgoWriter, "✓ Raft Autopilot is configured with cleanup_dead_servers enabled\n")

		// Cleanup handled by helper (best effort)
	})
})

func e2eRecoveryKeyRecipients() []openbaov1alpha1.RecoveryKeyRecipient {
	return []openbaov1alpha1.RecoveryKeyRecipient{
		{
			Name:        "e2e-custodian-01",
			Fingerprint: "70D46A170DCA5C707AB145C1845034789595779C",
			PGPPublicKey: strings.Join([]string{
				"mQENBGo07dsBCAD0WK/TcCChXCx7zNF2wlpiBLOxwBhD/DXdH4BwOcE00jBUY60Shs/4UYt7D3hq",
				"Tr9+HXNc7+ZGopjFs1iSjEpOPIBsogUH+/SO3YLzOeO9QR0Si6GowFOfsvT5JBqnAxSEnQbkJLLw",
				"tPBA3HlDeI9UpNvC8L/nQ8x53vnnVrrzEK11rKb8UeMNWsUFCxkstvBnEzKbaadzZwweLnC+CwLk",
				"tPhV0ea7C4sWsSmwg4TUFT/Nh3qJYhd9IWjTTG7KKZmECKcoWcrlinUZn8ee41RszDiDno+rZXR8",
				"1oE5APYpp+l0mvKr6K8ihP7HNqm8h0H6IULKjV+nb7z1/kikT763ABEBAAG0M2UyZS1jdXN0b2Rp",
				"YW4tMDEgPGUyZS1jdXN0b2RpYW4tMDFAZXhhbXBsZS5pbnZhbGlkPokBcwQTAQgAXRYhBHDUahcN",
				"ylxwerFFwYRQNHiVlXecBQJqNO3bGxSAAAAAAAQADm1hbnUyLDIuNSsxLjEyLDAsMwIbDQUJAuPM",
				"1QULCQgHAgIiAgYVCgkICwIEFgIDAQIeBwIXgAAKCRCEUDR4lZV3nNQYCAC9Ng3kGMaE5MibH/ZB",
				"jmCoW60k34BHCHSf8VHuA/mYV3Cj7ESgbsRl+bRQ1WoweNeHFIh3Oki2TKnrUK1TQniiwevdpeJr",
				"zYrCt2LZ6TkJKjw+JpbZific0oU8u5NDpEFLYtU5TsJa1v4Qg8l9lKJxXB0Udy38T8zJQoSBbzmS",
				"7YAWa96ksf/cRa9uJpwBVIlkTglVOcEvzjneq6c7i9B7YQYj+ZS4KQRBlCQRphJv5fSUrkLCuWe/",
				"J7ExDQxM+95OPaZHP06MAH++OJVNMsG3HC84gORsZDi5Dp/LHF1i1oJbLJ0blUiwsAGzUnCBpTXe",
				"+iXQpaJ6MY1uFYwqzE7S",
			}, ""),
		},
		{
			Name:        "e2e-custodian-02",
			Fingerprint: "37459146710CB1E66BCB26649C1ECEFEF129ECA9",
			PGPPublicKey: strings.Join([]string{
				"mQENBGo07dwBCADCy66TXwRwRfx0MggNmaP2/E7Q/JnmjyhwvOSZdxAnYPjkBqfcNeXci0P4o0kP",
				"3SmqAZdTqTqyldsVz0ltFKbYlwcSw5GXJadeEg+WmN1GPBxfVgvVMBLXDsYK9kbCAyM+na3lUMmo",
				"xEuotprWj1LYnLOPgGvI3k3BPsAj5sF4zPct3d8i23K5s5/Lq4e4aQxn8mKdYbmge5L3BwT2yxit",
				"4Rkd6n8OvZ+DuahpVs63dYf4vnLMxORl0j7r0E/aaV41M7/zHCQKaQJXZgx4kMoVo8/a0HM76sbN",
				"uWvQcwmrF7xhra1+myE5KanSvXrm3DZ68Yaet68immBZIrhMZjKlABEBAAG0M2UyZS1jdXN0b2Rp",
				"YW4tMDIgPGUyZS1jdXN0b2RpYW4tMDJAZXhhbXBsZS5pbnZhbGlkPokBcwQTAQgAXRYhBDdFkUZx",
				"DLHma8smZJwezv7xKeypBQJqNO3cGxSAAAAAAAQADm1hbnUyLDIuNSsxLjEyLDAsMwIbDQUJAuPM",
				"1AULCQgHAgIiAgYVCgkICwIEFgIDAQIeBwIXgAAKCRCcHs7+8SnsqfwCB/9KNfwD8eaC2PS3FFaX",
				"M/K5egY2v3VcXYaXkhPkJrAn5t8MOr2fJMUE+YiZH8YEBdehPKWBJepB3wc1boKXut/oMs8EHQPZ",
				"34spLoBN4XIOSArcn3hUiolKRgCMJ+7VG98JjcXhLYOJCd32C1eQlJwjYgThItF0jWZuzmBWX0vd",
				"6IZ5Ra5jqic6QLHS3fw8DqyeVlakFDijZ6lwD1uHHX6mJD8lG/9Xlfsg6SzArAbZVCuRK3/ubOYX",
				"p8900v4yVD358k0pUmhz+gv1NHDCa3plbJDJmYLuSplam9pih4X6xhHir4KO5dIv/6h7pHVMIT5a",
				"xPTwd03nyQ4+vZrQhxSp",
			}, ""),
		},
		{
			Name:        "e2e-custodian-03",
			Fingerprint: "14484A33214ED7E5CAFBEF5761C1C1850CA4BF98",
			PGPPublicKey: strings.Join([]string{
				"mQENBGo07dwBCADU8pBxATOjRgFQJSN6MkK7hJRZp7WaihXV3njdfU3mKE8Rp9qyIWwhLaqLwCqS",
				"KI2J7IM3YHKJRyRsdpXwx7pq4ssITJ33jbs7v932OQcVABkZmfb5dT7WQtTamicV82Nn8zkbaKlt",
				"iH5eAQq5pigqErAzC3tclNIh+vsVw7gyXjvge5VW+FzTGsETJa2coSeY7XGlBD8JmdyDhjbqAYIy",
				"+u4xL9TMaHUUQB1ljxnOrOq5/5wiXi3N6fBto+9/5vfLPoecMpZ/SG8uXlB/JoVIsiWqXQ6Yi+H+",
				"3wZbo8Hc8akH23ruxmbKhLgdvl3f/18YVZ/RC6aJU8Ksdan218MbABEBAAG0M2UyZS1jdXN0b2Rp",
				"YW4tMDMgPGUyZS1jdXN0b2RpYW4tMDNAZXhhbXBsZS5pbnZhbGlkPokBcwQTAQgAXRYhBBRISjMh",
				"TtflyvvvV2HBwYUMpL+YBQJqNO3cGxSAAAAAAAQADm1hbnUyLDIuNSsxLjEyLDAsMwIbDQUJAuPM",
				"1AULCQgHAgIiAgYVCgkICwIEFgIDAQIeBwIXgAAKCRBhwcGFDKS/mBp7CACPD/l+zhpzZdW9NqjO",
				"16+ClCBIJ3+aVFQPMX+JjHrvPq/ugl5fLwdcDU1kGfD5oy/58Or2ko9ttAz5E7UA3qOy8WfzEKmv",
				"p+d8JNYIt5Iwyrk+ylKW8ZwRhxZYvXr2eiGiT8k3+yggu0WmHukT6UQgTbxETwoZAFvLDNqs8bzT",
				"r/s+ISTV2ywBuWDpAD9NVQ6t9oW3KmnU3R1gvR122pA7BKO/pNgLAeRDcKziam8oI38WIuLNXv0S",
				"EG5OtwClqVilwRTH4FlQGh1bONhkS3fTaDuTuKR2JvJIPki+y3NErylc4COKxzvONvurud2qGB5+",
				"VctBY9iDutCMgLrzYlsh",
			}, ""),
		},
	}
}
