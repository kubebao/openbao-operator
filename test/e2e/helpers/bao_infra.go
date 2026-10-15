package helpers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InfraBaoConfig defines how to run the in-cluster "infra-bao" instance used
// as a mock external dependency (e.g., Transit auto-unseal).
// Infra-bao always runs in production mode with TLS enabled (never dev mode).
type InfraBaoConfig struct {
	Namespace string
	Name      string
	Image     string
}

const infraBaoCAMountPath = "/etc/bao/infra-ca"

// EnsureInfraBao creates (or reuses) a production-mode OpenBao pod + service with TLS.
// The service is reachable at https://<name>.<namespace>.svc:8200.
// Infra-bao always runs in production mode with TLS (never dev mode).
func EnsureInfraBao(ctx context.Context, restCfg *rest.Config, c client.Client, cfg InfraBaoConfig) error {
	if c == nil {
		return fmt.Errorf("kubernetes client is required")
	}
	if restCfg == nil {
		return fmt.Errorf("rest config is required")
	}
	if cfg.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if cfg.Name == "" {
		return fmt.Errorf("name is required")
	}
	if cfg.Image == "" {
		return fmt.Errorf("image is required")
	}

	// Always generate TLS certificates and configure production mode (never dev mode)
	var tlsCertSecret *corev1.Secret
	var tlsCASecret *corev1.Secret
	var configMap *corev1.ConfigMap
	var unsealKeySecret *corev1.Secret
	{
		// Generate CA certificate
		caCertPEM, caKeyPEM, err := generateInfraBaoCA(cfg.Name)
		if err != nil {
			return fmt.Errorf("failed to generate CA for infra-bao: %w", err)
		}

		// Generate server certificate
		serverCertPEM, serverKeyPEM, err := generateInfraBaoServerCert(cfg.Namespace, cfg.Name, caCertPEM, caKeyPEM)
		if err != nil {
			return fmt.Errorf("failed to generate server certificate for infra-bao: %w", err)
		}

		// Create CA secret
		tlsCASecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfg.Name + "-tls-ca",
				Namespace: cfg.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt": caCertPEM,
				"ca.key": caKeyPEM,
			},
		}
		err = c.Create(ctx, tlsCASecret)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create infra-bao CA Secret %s/%s: %w", cfg.Namespace, tlsCASecret.Name, err)
		}

		// Create server certificate secret
		tlsCertSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfg.Name + "-tls-server",
				Namespace: cfg.Namespace,
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"tls.crt": serverCertPEM,
				"tls.key": serverKeyPEM,
				"ca.crt":  caCertPEM,
			},
		}
		err = c.Create(ctx, tlsCertSecret)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create infra-bao TLS server Secret %s/%s: %w", cfg.Namespace, tlsCertSecret.Name, err)
		}

		// Generate static unseal key for infra-bao
		staticKey, keyErr := generateUnsealKey()
		if keyErr != nil {
			return fmt.Errorf("failed to generate static unseal key: %w", keyErr)
		}

		// Create static unseal key secret
		unsealKeySecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfg.Name + "-unseal-key",
				Namespace: cfg.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"key": staticKey,
			},
		}
		err = c.Create(ctx, unsealKeySecret)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create infra-bao unseal key Secret %s/%s: %w", cfg.Namespace, unsealKeySecret.Name, err)
		}

		// Create ConfigMap with OpenBao config
		// Always use production mode (never dev mode) with static seal for auto-initialization
		// Static seal allows OpenBao to auto-initialize and auto-unseal without manual intervention
		configContent := `ui = true

storage "file" {
  path = "/bao/data"
}

seal "static" {
  current_key = "file:///etc/bao/unseal/key"
  current_key_id = "operator-generated-v1"
}

listener "tcp" {
  address     = "[::]:8200"
  tls_cert_file = "/etc/bao/tls/tls.crt"
  tls_key_file  = "/etc/bao/tls/tls.key"
  tls_client_ca_file = "/etc/bao/tls/ca.crt"
}
`

		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfg.Name + "-config",
				Namespace: cfg.Namespace,
			},
			Data: map[string]string{
				"config.hcl": configContent,
			},
		}
		err = c.Create(ctx, configMap)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create infra-bao ConfigMap %s/%s: %w", cfg.Namespace, configMap.Name, err)
		}
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": cfg.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:     "http",
					Port:     8200,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}
	err := c.Create(ctx, svc)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create infra-bao Service %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	// Always use production mode with TLS (never dev mode)
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyAlways,
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
				Name:  "openbao",
				Image: cfg.Image,
				Ports: []corev1.ContainerPort{
					{ContainerPort: 8200, Name: "http"},
				},
				Command: []string{"bao", "server", "-config=/etc/bao/config/config.hcl"},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "config",
						MountPath: "/etc/bao/config",
						ReadOnly:  true,
					},
					{
						Name:      "tls",
						MountPath: "/etc/bao/tls",
						ReadOnly:  true,
					},
					{
						Name:      "unseal",
						MountPath: "/etc/bao/unseal",
						ReadOnly:  true,
					},
					{
						Name:      "data",
						MountPath: "/bao/data",
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
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: configMap.Name,
						},
					},
				},
			},
			{
				Name: "tls",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: tlsCertSecret.Name,
					},
				},
			},
			{
				Name: "unseal",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  unsealKeySecret.Name,
						DefaultMode: ptr.To(int32(0440)), // Match operator's secretFileMode
					},
				},
			},
			{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app": cfg.Name,
			},
		},
		Spec: podSpec,
	}

	err = c.Create(ctx, pod)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create infra-bao Pod %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	// Wait for pod to be running
	podRunningDeadline := time.NewTimer(2 * time.Minute)
	defer podRunningDeadline.Stop()
	podRunningTicker := time.NewTicker(1 * time.Second)
	defer podRunningTicker.Stop()

	for {
		current := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}, current); err != nil {
			return fmt.Errorf("failed to get infra-bao Pod %s/%s: %w", cfg.Namespace, cfg.Name, err)
		}

		if current.Status.Phase == corev1.PodRunning {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"context canceled while waiting for infra-bao Pod %s/%s to be running: %w",
				cfg.Namespace,
				cfg.Name,
				ctx.Err(),
			)
		case <-podRunningDeadline.C:
			return fmt.Errorf(
				"timed out waiting for infra-bao Pod %s/%s to be running (last phase: %s)",
				cfg.Namespace,
				cfg.Name,
				current.Status.Phase,
			)
		case <-podRunningTicker.C:
		}
	}

	// Initialize infra-bao if not already initialized
	// This ensures infra-bao is ready for use in tests (e.g., Transit auto-unseal, ACME CA)
	if err := initializeInfraBao(ctx, restCfg, c, cfg); err != nil {
		return fmt.Errorf("failed to initialize infra-bao %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	return nil
}

func infraBaoRootTokenSecretName(name string) string {
	return name + "-root-token"
}

func infraBaoTLSCASecretName(name string) string {
	return name + "-tls-ca"
}

func readSecretData(ctx context.Context, c client.Client, namespace, name, key string) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err != nil {
		return nil, err
	}

	value, ok := secret.Data[key]
	if !ok || len(strings.TrimSpace(string(value))) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing non-empty key %q", namespace, name, key)
	}
	return value, nil
}

// ReadInfraBaoRootToken returns the initialized infra-bao root token captured by EnsureInfraBao.
func ReadInfraBaoRootToken(ctx context.Context, c client.Client, namespace, name string) (string, error) {
	data, err := readSecretData(ctx, c, namespace, infraBaoRootTokenSecretName(name), "token")
	if err != nil {
		return "", fmt.Errorf("failed to read infra-bao root token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ReadInfraBaoTLSCACert returns the infra-bao TLS CA bundle used to trust the helper service.
func ReadInfraBaoTLSCACert(ctx context.Context, c client.Client, namespace, name string) ([]byte, error) {
	data, err := readSecretData(ctx, c, namespace, infraBaoTLSCASecretName(name), "ca.crt")
	if err != nil {
		return nil, fmt.Errorf("failed to read infra-bao CA bundle: %w", err)
	}
	return data, nil
}

func buildInfraBaoSealCredentialsData(rootToken string, tlsCACert, pkiCACert []byte) map[string][]byte {
	data := map[string][]byte{
		"token":  []byte(strings.TrimSpace(rootToken)),
		"ca.crt": tlsCACert,
	}
	if len(pkiCACert) > 0 {
		data["pki-ca.crt"] = pkiCACert
	}
	return data
}

// EnsureInfraBaoSealCredentialsSecret upserts a Secret matching the transit/private-ACME
// seal credential contract used by the E2E suites.
func EnsureInfraBaoSealCredentialsSecret(
	ctx context.Context,
	c client.Client,
	namespace string,
	secretName string,
	rootToken string,
	tlsCACert []byte,
	pkiCACert []byte,
) error {
	if c == nil {
		return fmt.Errorf("kubernetes client is required")
	}
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if secretName == "" {
		return fmt.Errorf("secret name is required")
	}
	if strings.TrimSpace(rootToken) == "" {
		return fmt.Errorf("root token is required")
	}
	if len(tlsCACert) == 0 {
		return fmt.Errorf("tls CA certificate is required")
	}

	desiredData := buildInfraBaoSealCredentialsData(rootToken, tlsCACert, pkiCACert)
	key := types.NamespacedName{Name: secretName, Namespace: namespace}
	existing := &corev1.Secret{}
	err := c.Get(ctx, key, existing)
	switch {
	case apierrors.IsNotFound(err):
		return c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: desiredData,
		})
	case err != nil:
		return err
	default:
		original := existing.DeepCopy()
		existing.Type = corev1.SecretTypeOpaque
		existing.Data = desiredData
		return c.Patch(ctx, existing, client.MergeFrom(original))
	}
}

func newInfraBaoClientPod(
	namespace string,
	podName string,
	image string,
	infraBaoName string,
	infraBaoAddress string,
	token string,
	script string,
) *corev1.Pod {
	env := []corev1.EnvVar{
		{Name: "BAO_ADDR", Value: infraBaoAddress},
		{Name: "BAO_CACERT", Value: infraBaoCAMountPath + "/ca.crt"},
	}
	if strings.TrimSpace(token) != "" {
		env = append(env, corev1.EnvVar{Name: "BAO_TOKEN", Value: strings.TrimSpace(token)})
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
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
					Name:    "bao",
					Image:   image,
					Env:     env,
					Command: []string{"/bin/sh", "-ec"},
					Args:    []string{script},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "infra-ca",
							MountPath: infraBaoCAMountPath,
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
					Name: "infra-ca",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: infraBaoTLSCASecretName(infraBaoName),
							Items: []corev1.KeyToPath{
								{
									Key:  "ca.crt",
									Path: "ca.crt",
								},
							},
						},
					},
				},
			},
		},
	}
}

// CleanupInfraBao best-effort deletes the infra-bao resources created by EnsureInfraBao.
// It is safe to call even if resources were partially created or already removed.
func CleanupInfraBao(ctx context.Context, c client.Client, cfg InfraBaoConfig) {
	// Order: pod -> service -> secrets/configmap
	_ = c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: cfg.Name, Namespace: cfg.Namespace}})
	_ = c.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: cfg.Name, Namespace: cfg.Namespace}})
	_ = c.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-tls-server",
			Namespace: cfg.Namespace,
		},
	})
	_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cfg.Name + "-tls-ca", Namespace: cfg.Namespace}})
	_ = c.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-unseal-key",
			Namespace: cfg.Namespace,
		},
	})
	_ = c.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      infraBaoRootTokenSecretName(cfg.Name),
			Namespace: cfg.Namespace,
		},
	})
	_ = c.Delete(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-config",
			Namespace: cfg.Namespace,
		},
	})
}

// checkInfraBaoReadinessLocal checks readiness from inside the pod via kubectl exec.
// We accept exit code 0 (unsealed) or 2 (sealed/uninitialized) as "responsive".
// initializeInfraBao initializes infra-bao if it's not already initialized.
// It uses the static auto-unseal configuration, so no secret_shares or secret_threshold
// are needed. The root token is stored in a Secret for later use.
func initializeInfraBao(ctx context.Context, restCfg *rest.Config, c client.Client, cfg InfraBaoConfig) error {
	secretName := infraBaoRootTokenSecretName(cfg.Name)
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: cfg.Namespace}, &corev1.Secret{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check existing root token Secret %s/%s: %w", cfg.Namespace, secretName, err)
	}

	infraAddr := fmt.Sprintf("https://%s.%s.svc:8200", cfg.Name, cfg.Namespace)
	pod := newInfraBaoClientPod(cfg.Namespace, cfg.Name+"-operator-init", cfg.Image, cfg.Name, infraAddr, "", `
set -u

wait_for_api() {
  i=0
  while [ "$i" -lt 60 ]; do
    rc=0
    bao status >/dev/null 2>&1 || rc=$?
    # Exit code 0: unsealed/initialized; exit code 2: sealed/uninitialized but responsive.
    if [ "$rc" -eq 0 ] || [ "$rc" -eq 2 ]; then
      return 0
    fi
    i=$((i+1))
    sleep 2
  done
  echo "timed out waiting for infra-bao API to respond" >&2
  bao status >&2 || true
  return 1
}

wait_for_api || exit 1

# For static seal, we don't need to pass secret_shares or secret_threshold.
bao operator init -format=json
`)

	result, err := RunPodUntilCompletion(ctx, restCfg, c, pod, 2*time.Minute)
	if err != nil {
		// If initialization already happened (e.g., pod restarted), ensure the Secret exists.
		if getErr := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: cfg.Namespace},
			&corev1.Secret{}); getErr == nil {
			_ = DeletePodBestEffort(ctx, c, cfg.Namespace, pod.Name)
			return nil
		}
		return fmt.Errorf("infra-bao init pod failed: %w", err)
	}
	if result.Phase != corev1.PodSucceeded {
		return fmt.Errorf("infra-bao init pod phase=%s logs:\n%s", result.Phase, result.Logs)
	}

	// Parse JSON output to extract root token.
	var initResult struct {
		RootToken string `json:"root_token"`
	}
	if err := json.Unmarshal([]byte(result.Logs), &initResult); err != nil {
		return fmt.Errorf("failed to parse infra-bao init output as JSON (logs:\n%s): %w", result.Logs, err)
	}
	if strings.TrimSpace(initResult.RootToken) == "" {
		return fmt.Errorf("infra-bao init output missing root_token (logs:\n%s)", result.Logs)
	}

	rootTokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cfg.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token": []byte(strings.TrimSpace(initResult.RootToken)),
		},
	}

	if err := c.Create(ctx, rootTokenSecret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create root token Secret %s/%s: %w", cfg.Namespace, secretName, err)
	}
	_ = DeletePodBestEffort(ctx, c, cfg.Namespace, pod.Name)
	return nil
}

// ConfigureInfraBaoTransit enables the transit secrets engine and ensures the
// given key exists. It runs a short-lived client pod (bao CLI) and returns its logs.
func ConfigureInfraBaoTransit(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	namespace string,
	infraBaoName string,
	clientImage string,
	infraBaoAddress string,
	keyName string,
) (*PodResult, error) {
	if infraBaoAddress == "" {
		return nil, fmt.Errorf("infra-bao address is required")
	}
	if infraBaoName == "" {
		return nil, fmt.Errorf("infra-bao name is required")
	}
	if keyName == "" {
		return nil, fmt.Errorf("key name is required")
	}

	tokenToUse, err := ReadInfraBaoRootToken(ctx, c, namespace, infraBaoName)
	if err != nil {
		return nil, err
	}

	pod := newInfraBaoClientPod(
		namespace,
		"infra-bao-configure-transit",
		clientImage,
		infraBaoName,
		infraBaoAddress,
		tokenToUse,
		`
wait_for_unsealed() {
  # Ensure the server is reachable and unsealed. OpenBao CLI returns exit code 2
  # when sealed/uninitialized; treat that as "not ready yet" and keep polling.
  i=0
  while [ "$i" -lt 60 ]; do
    if bao status >/dev/null 2>&1; then
      return 0
    fi
    i=$((i+1))
    sleep 2
  done
  echo "timed out waiting for infra-bao to be unsealed; last status:" >&2
  bao status >&2 || true
  return 1
}

wait_for_unsealed

# Enable transit engine; tolerate "already enabled" without relying on grep.
if ! out="$(bao secrets enable transit 2>&1)"; then
  case "$out" in
    *"path is already in use"*|*"existing mount at"*|*"already in use"*)
      ;;
    *)
      echo "$out" >&2
      exit 1
      ;;
  esac
fi

# Ensure the transit key exists.
if ! bao read -format=json transit/keys/`+keyName+` >/dev/null 2>&1; then
  bao write -f transit/keys/`+keyName+` type=aes256-gcm96 >/dev/null
fi

echo "ok"
`,
	)

	result, err := RunPodUntilCompletion(ctx, restCfg, c, pod, 2*time.Minute)
	if err != nil {
		return nil, err
	}

	_ = DeletePodBestEffort(ctx, c, namespace, pod.Name)
	return result, nil
}

// ConfigureInfraBaoPKIACME enables the PKI secrets engine and ACME support on infra-bao.
// It is safe to call multiple times.
func ConfigureInfraBaoPKIACME(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	namespace string,
	infraBaoName string,
	clientImage string,
	infraBaoAddress string,
	clusterPath string,
) (*PodResult, error) {
	if infraBaoAddress == "" {
		return nil, fmt.Errorf("infra-bao address is required")
	}
	if infraBaoName == "" {
		return nil, fmt.Errorf("infra-bao name is required")
	}
	if clusterPath == "" {
		return nil, fmt.Errorf("cluster path is required")
	}

	tokenToUse, err := ReadInfraBaoRootToken(ctx, c, namespace, infraBaoName)
	if err != nil {
		return nil, err
	}

	pod := newInfraBaoClientPod(
		namespace,
		"infra-bao-configure-pki-acme",
		clientImage,
		infraBaoName,
		infraBaoAddress,
		tokenToUse,
		`
wait_for_unsealed() {
  i=0
  while [ "$i" -lt 60 ]; do
    if bao status >/dev/null 2>&1; then
      return 0
    fi
    i=$((i+1))
    sleep 2
  done
  echo "timed out waiting for infra-bao to be unsealed; last status:" >&2
  bao status >&2 || true
  return 1
}

wait_for_unsealed

# Enable PKI engine; tolerate "already enabled" without relying on grep.
if ! out="$(bao secrets enable pki 2>&1)"; then
  case "$out" in
    *"path is already in use"*|*"existing mount at"*|*"already in use"*)
      ;;
    *)
      echo "$out" >&2
      exit 1
      ;;
  esac
fi

bao secrets tune \
  -allowed-response-headers=Location \
  -allowed-response-headers=Replay-Nonce \
  -allowed-response-headers=Link \
  pki/ >/dev/null

if ! bao read -format=json pki/cert/ca >/dev/null 2>&1; then
  bao write -format=json pki/root/generate/internal \
    common_name="E2E ACME Root CA" ttl=87600h >/dev/null
fi

bao write pki/config/cluster path="`+clusterPath+`" >/dev/null
bao write pki/config/acme enabled=true >/dev/null
echo "ok"
`,
	)

	result, err := RunPodUntilCompletion(ctx, restCfg, c, pod, 2*time.Minute)
	if err != nil {
		return nil, err
	}

	_ = DeletePodBestEffort(ctx, c, namespace, pod.Name)
	return result, nil
}

// FetchInfraBaoPKICA fetches the PKI CA certificate from infra-bao.
// This is the CA that signs ACME certificates, which is different from the TLS CA.
func FetchInfraBaoPKICA(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	namespace string,
	infraBaoName string,
	clientImage string,
	infraBaoAddress string,
) ([]byte, error) {
	if infraBaoAddress == "" {
		return nil, fmt.Errorf("infra-bao address is required")
	}
	if infraBaoName == "" {
		return nil, fmt.Errorf("infra-bao name is required")
	}

	tokenToUse, err := ReadInfraBaoRootToken(ctx, c, namespace, infraBaoName)
	if err != nil {
		return nil, err
	}

	pod := newInfraBaoClientPod(
		namespace,
		"infra-bao-fetch-pki-ca",
		clientImage,
		infraBaoName,
		infraBaoAddress,
		tokenToUse,
		`bao read -format=json pki/cert/ca`,
	)

	result, err := RunPodUntilCompletion(ctx, restCfg, c, pod, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to run pod to fetch PKI CA: %w", err)
	}
	if result.Phase != corev1.PodSucceeded {
		return nil, fmt.Errorf("pod to fetch PKI CA failed, phase=%s, logs:\n%s", result.Phase, result.Logs)
	}

	// Parse JSON output to extract the CA certificate
	var pkiResponse struct {
		Data struct {
			Certificate string `json:"certificate"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Logs), &pkiResponse); err != nil {
		return nil, fmt.Errorf("failed to parse PKI CA JSON response: %w, logs:\n%s", err, result.Logs)
	}

	pkiCA := strings.TrimSpace(pkiResponse.Data.Certificate)
	if pkiCA == "" {
		return nil, fmt.Errorf("PKI CA certificate is empty in response")
	}

	_ = DeletePodBestEffort(ctx, c, namespace, pod.Name)
	return []byte(pkiCA), nil
}

// generateInfraBaoCA generates a self-signed CA certificate for infra-bao.
func generateInfraBaoCA(name string) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate CA private key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s Infra-Bao Root CA (e2e)", name),
			Organization: []string{"OpenBao Operator E2E"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// generateInfraBaoServerCert generates a server certificate for infra-bao signed by the given CA.
func generateInfraBaoServerCert(
	namespace string,
	name string,
	caCertPEM []byte,
	caKeyPEM []byte,
) ([]byte, []byte, error) {
	// Parse CA
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, nil, fmt.Errorf("failed to decode CA private key PEM")
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA private key: %w", err)
	}

	// Generate server key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate server private key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	dnsNames := []string{
		"localhost",
		fmt.Sprintf("%s.%s.svc", name, namespace),
		fmt.Sprintf("*.%s.%s.svc", name, namespace),
	}

	ipAddresses := []net.IP{net.ParseIP("127.0.0.1")}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s.%s.svc", name, namespace),
			Organization: []string{"OpenBao Operator E2E"},
		},
		NotBefore:   now.Add(-1 * time.Hour),
		NotAfter:    now.AddDate(0, 0, 365),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create server certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// generateUnsealKey generates a 32-byte random key for static unseal (same as operator uses).
// The raw bytes are written directly to the Secret so that the mounted
// file contains a 32-byte key compatible with OpenBao's static seal.
// OpenBao supports raw, base64, or hex encoding and will auto-detect the format.
func generateUnsealKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate unseal key: %w", err)
	}
	return key, nil
}
