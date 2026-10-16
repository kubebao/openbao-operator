//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
)

const (
	kmipClusterName       = "kmip-pykmip"
	kmipCredentialsName   = "kmip-pykmip-credentials"
	kmipServerName        = "kmip-pykmip-server"
	kmipServerTLSName     = "kmip-pykmip-server-tls"
	kmipServerPort        = 5696
	kmipKeyID             = "1"
	kmipClientCommonName  = "openbao-kmip-client"
	kmipTLS12Cipher       = "TLS_RSA_WITH_AES_128_CBC_SHA256"
	kmipCredentialCACert  = "ca.crt"
	kmipCredentialCert    = "client.crt"
	kmipCredentialKey     = "client.key"
	kmipServerCACert      = "ca.crt"
	kmipServerCertificate = "server.crt"
	kmipServerPrivateKey  = "server.key"
)

var _ = Describe("Cluster KMIP Unseal", Label("cluster", "lifecycle", "unseal", "kmip", "hsm"), Ordered, func() {
	ctx := context.Background()

	var (
		f *framework.Framework
		c client.Client
	)

	BeforeAll(func() {
		requireKMIPSuite()
		ensureKMIPServerImageLoaded()

		var err error
		f, err = framework.NewSetup(ctx, "kmip", operatorNamespace)
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

	It("initializes, restarts, and scales using a PyKMIP-backed KMIP seal", Label(
		"case:cluster-unseal-kmip-initializes-restarts-and-scales-using-a-d2fcee95",
	), func() {
		serverName := fmt.Sprintf("%s.%s.svc", kmipServerName, f.Namespace)
		endpoint := fmt.Sprintf("%s:%d", serverName, kmipServerPort)

		By("creating KMIP mTLS material")
		tlsBundle, err := newKMIPTLSBundle(kmipServerName, f.Namespace, kmipClientCommonName)
		Expect(err).NotTo(HaveOccurred())

		serverTLSSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kmipServerTLSName,
				Namespace: f.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				kmipServerCACert:      tlsBundle.caCert,
				kmipServerCertificate: tlsBundle.serverCert,
				kmipServerPrivateKey:  tlsBundle.serverKey,
			},
		}
		Expect(c.Create(ctx, serverTLSSecret)).To(Succeed())

		credentials := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kmipCredentialsName,
				Namespace: f.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				kmipCredentialCACert: tlsBundle.caCert,
				kmipCredentialCert:   tlsBundle.clientCert,
				kmipCredentialKey:    tlsBundle.clientKey,
			},
		}
		Expect(c.Create(ctx, credentials)).To(Succeed())

		By("starting the PyKMIP fixture server")
		Expect(c.Create(ctx, kmipFixtureService(f.Namespace, kmipServerName))).To(Succeed())
		Expect(c.Create(ctx, kmipFixtureDeployment(f.Namespace, kmipServerName, kmipServerTLSName, kmipServerImage(), kmipClientCommonName))).To(Succeed())
		waitForKMIPDeploymentReady(ctx, c, f.Namespace, kmipServerName)

		By("creating an OpenBaoCluster configured for KMIP unseal")
		tcp := corev1.ProtocolTCP
		kmipPort := intstr.FromInt(kmipServerPort)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kmipClusterName,
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
					EgressRules: []networkingv1.NetworkPolicyEgressRule{
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": kmipServerName,
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: &tcp,
									Port:     &kmipPort,
								},
							},
						},
					},
				},
				Unseal: &openbaov1alpha1.UnsealConfig{
					Type: openbao.SealTypeKMIP,
					KMIP: &openbaov1alpha1.KMIPSealConfig{
						Endpoint:     endpoint,
						KMSKeyID:     kmipKeyID,
						ClientCert:   "/etc/bao/seal-creds/" + kmipCredentialCert,
						ClientKey:    "/etc/bao/seal-creds/" + kmipCredentialKey,
						CACert:       "/etc/bao/seal-creds/" + kmipCredentialCACert,
						ServerName:   serverName,
						Timeout:      ptr.To(int32(20)),
						EncryptAlg:   "AES_GCM",
						TLS12Ciphers: kmipTLS12Cipher,
					},
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: kmipCredentialsName,
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

		By("waiting for the initial KMIP-sealed pod to become ready")
		_, err = f.WaitForStatefulSetReady(ctx, kmipClusterName, 1, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(kmipClusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("verifying the rendered OpenBao config contains the KMIP seal stanza")
		config := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Name: kmipClusterName + "-config", Namespace: f.Namespace}, config)).To(Succeed())
		Expect(config.Data).To(HaveKey("config.hcl"))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`seal "kmip"`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`endpoint`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(endpoint))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`kms_key_id`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(kmipKeyID))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`encrypt_alg`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`AES_GCM`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(`tls12_ciphers`))
		Expect(config.Data["config.hcl"]).To(ContainSubstring(kmipTLS12Cipher))

		By("verifying the StatefulSet projects KMIP credential files")
		statefulSet := &appsv1.StatefulSet{}
		Expect(c.Get(ctx, types.NamespacedName{Name: kmipClusterName, Namespace: f.Namespace}, statefulSet)).To(Succeed())
		Expect(hasVolumeNamed(statefulSet.Spec.Template.Spec.Volumes, "seal-creds")).To(BeTrue())
		Expect(hasVolumeMountNamed(statefulSet.Spec.Template.Spec.Containers[0].VolumeMounts, "seal-creds")).To(BeTrue())

		By("deleting the pod and validating it auto-unseals after restart")
		pod := &corev1.Pod{}
		Expect(c.Get(ctx, types.NamespacedName{Name: kmipClusterName + "-0", Namespace: f.Namespace}, pod)).To(Succeed())
		oldUID := pod.UID
		Expect(c.Delete(ctx, pod)).To(Succeed())

		Eventually(func(g Gomega) {
			restarted := &corev1.Pod{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: kmipClusterName + "-0", Namespace: f.Namespace}, restarted)).To(Succeed())
			g.Expect(restarted.UID).NotTo(Equal(oldUID))
			g.Expect(podReady(restarted)).To(BeTrue())
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		f.WaitForCondition(kmipClusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("scaling up to verify new pods can use the seeded KMIP key material")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &openbaov1alpha1.OpenBaoCluster{}
			if err := c.Get(ctx, types.NamespacedName{Name: kmipClusterName, Namespace: f.Namespace}, current); err != nil {
				return err
			}
			current.Spec.Replicas = 2
			return c.Update(ctx, current)
		})).To(Succeed())
		_, err = f.WaitForStatefulSetReady(ctx, kmipClusterName, 2, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(kmipClusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)

		By("scaling back down cleanly")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &openbaov1alpha1.OpenBaoCluster{}
			if err := c.Get(ctx, types.NamespacedName{Name: kmipClusterName, Namespace: f.Namespace}, current); err != nil {
				return err
			}
			current.Spec.Replicas = 1
			return c.Update(ctx, current)
		})).To(Succeed())
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, types.NamespacedName{Name: kmipClusterName, Namespace: f.Namespace}, sts)).To(Succeed())
			g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		f.WaitForCondition(kmipClusterName, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
	})
})

type kmipTLSBundle struct {
	caCert     []byte
	serverCert []byte
	serverKey  []byte
	clientCert []byte
	clientKey  []byte
}

func newKMIPTLSBundle(serviceName, namespace, clientCommonName string) (kmipTLSBundle, error) {
	caCert, caKey, err := newKMIPCA()
	if err != nil {
		return kmipTLSBundle{}, err
	}

	serverCert, serverKey, err := newKMIPCertificate(kmipCertificateRequest{
		commonName: fmt.Sprintf("%s.%s.svc", serviceName, namespace),
		dnsNames: []string{
			serviceName,
			fmt.Sprintf("%s.%s", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		},
		keyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		caCert:      caCert,
		caKey:       caKey,
	})
	if err != nil {
		return kmipTLSBundle{}, err
	}

	clientCert, clientKey, err := newKMIPCertificate(kmipCertificateRequest{
		commonName:  clientCommonName,
		keyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		caCert:      caCert,
		caKey:       caKey,
	})
	if err != nil {
		return kmipTLSBundle{}, err
	}

	return kmipTLSBundle{
		caCert:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}),
		serverCert: serverCert,
		serverKey:  serverKey,
		clientCert: clientCert,
		clientKey:  clientKey,
	}, nil
}

func newKMIPCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate KMIP CA key: %w", err)
	}

	serialNumber, err := randomKMIPSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "OpenBao Operator KMIP E2E CA",
			Organization: []string{"OpenBao Operator E2E"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create KMIP CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse KMIP CA certificate: %w", err)
	}
	return cert, privateKey, nil
}

type kmipCertificateRequest struct {
	commonName  string
	dnsNames    []string
	keyUsage    x509.KeyUsage
	extKeyUsage []x509.ExtKeyUsage
	caCert      *x509.Certificate
	caKey       *rsa.PrivateKey
}

func newKMIPCertificate(req kmipCertificateRequest) ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate KMIP certificate key: %w", err)
	}

	serialNumber, err := randomKMIPSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   req.commonName,
			Organization: []string{"OpenBao Operator E2E"},
		},
		NotBefore:   now.Add(-1 * time.Hour),
		NotAfter:    now.AddDate(0, 0, 30),
		KeyUsage:    req.keyUsage,
		ExtKeyUsage: req.extKeyUsage,
		DNSNames:    req.dnsNames,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, req.caCert, &privateKey.PublicKey, req.caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create KMIP certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM, nil
}

func randomKMIPSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate KMIP certificate serial: %w", err)
	}
	return serialNumber, nil
}

func kmipFixtureService(namespace, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": name,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app": name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "kmip",
					Protocol:   corev1.ProtocolTCP,
					Port:       kmipServerPort,
					TargetPort: intstr.FromInt(kmipServerPort),
				},
			},
		},
	}
}

func kmipFixtureDeployment(namespace, name, tlsSecretName, image, keyOwner string) *appsv1.Deployment {
	labels := map[string]string{
		"app": name,
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: ptr.To(int64(1000)),
					},
					Containers: []corev1.Container{
						{
							Name:            "pykmip",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Env: []corev1.EnvVar{
								{Name: "KMIP_KEY_ID", Value: kmipKeyID},
								{Name: "KMIP_KEY_OWNER", Value: keyOwner},
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "kmip",
									ContainerPort: kmipServerPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(kmipServerPort)},
								},
								InitialDelaySeconds: 2,
								PeriodSeconds:       2,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(kmipServerPort)},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "tls", MountPath: "/etc/pykmip/tls", ReadOnly: true},
								{Name: "data", MountPath: "/var/lib/pykmip"},
								{Name: "tmp", MountPath: "/tmp"},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								ReadOnlyRootFilesystem: ptr.To(true),
								RunAsNonRoot:           ptr.To(true),
								RunAsUser:              ptr.To(int64(1000)),
								RunAsGroup:             ptr.To(int64(1000)),
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("96Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "tls",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName:  tlsSecretName,
									DefaultMode: ptr.To[int32](0o440),
								},
							},
						},
						{
							Name:         "data",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
						{
							Name:         "tmp",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
				},
			},
		},
	}
}

func waitForKMIPDeploymentReady(ctx context.Context, c client.Client, namespace, name string) {
	Eventually(func(g Gomega) {
		deployment := &appsv1.Deployment{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, deployment)).To(Succeed())
		g.Expect(deployment.Status.ReadyReplicas).To(Equal(int32(1)))
	}, 5*time.Minute, 2*time.Second).Should(Succeed())
}

func hasVolumeNamed(volumes []corev1.Volume, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name {
			return true
		}
	}
	return false
}

func hasVolumeMountNamed(mounts []corev1.VolumeMount, name string) bool {
	for _, mount := range mounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}
