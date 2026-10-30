package helpers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AzuriteConfig defines how to run Azurite for Azure Blob Storage emulator testing.
type AzuriteConfig struct {
	Namespace string
	Name      string
	Image     string
	BlobPort  int32
}

// DefaultAzuriteConfig returns a default Azurite configuration.
func DefaultAzuriteConfig() AzuriteConfig {
	return AzuriteConfig{
		Name:     "azurite",
		Image:    envOrDefault(EnvAzuriteImage, DefaultAzuriteImage),
		BlobPort: 10000,
	}
}

// EnsureAzurite creates (or reuses) an Azurite Deployment + Service for Azure emulator testing.
// The service is reachable at http://<name>.<namespace>.svc:<blobPort>.
func EnsureAzurite(ctx context.Context, c client.Client, cfg AzuriteConfig) error {
	if c == nil {
		return fmt.Errorf("kubernetes client is required")
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
	if cfg.BlobPort <= 0 {
		cfg.BlobPort = 10000
	}

	// Ensure namespace exists
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: cfg.Namespace,
		},
	}
	if err := c.Create(ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create namespace %s: %w", cfg.Namespace, err)
	}

	// Create Service
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
					Name:       "blob",
					Port:       cfg.BlobPort,
					TargetPort: intstr.FromInt32(cfg.BlobPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
	if err := c.Create(ctx, svc); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Azurite Service %s/%s: %w", cfg.Namespace, svc.Name, err)
	}

	// Create Deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app": cfg.Name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": cfg.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": cfg.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            cfg.Name,
							Image:           cfg.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: cfg.BlobPort,
									Name:          "blob",
								},
							},
							Args: []string{
								"azurite",
								"--blobHost", "::",
								"--blobPort", fmt.Sprintf("%d", cfg.BlobPort),
								"--skipApiVersionCheck",
								"--disableProductStyleUrl", // Required for IP-style URLs with account name in path
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(cfg.BlobPort),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
								TimeoutSeconds:      3,
								FailureThreshold:    3,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(cfg.BlobPort),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
								TimeoutSeconds:      3,
								FailureThreshold:    3,
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								// Note: Azurite image runs as root, so we cannot set RunAsNonRoot
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	if err := c.Create(ctx, deployment); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Azurite Deployment %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	// Wait for Deployment to be ready
	return waitForDeploymentReady(ctx, c, cfg.Namespace, cfg.Name, "Azurite")
}

// CreateAzuriteContainer creates a blob container in the Azurite emulator by running a Pod inside the cluster.
// This is necessary because Azurite starts empty and OpenBao's backup job expects the container to exist.
// Uses the default devstoreaccount1 account that Azurite provides.
// The endpoint must be the complete Blob service URL, including /devstoreaccount1.
// This function uses Azure CLI in a Pod to handle authentication properly.
func CreateAzuriteContainer(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	namespace, endpoint, containerName, accountKey string,
) error {
	if restCfg == nil {
		return fmt.Errorf("rest config is required")
	}
	if c == nil {
		return fmt.Errorf("kubernetes client is required")
	}
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	if containerName == "" {
		return fmt.Errorf("container name is required")
	}
	if accountKey == "" {
		// Use default Azurite account key if not provided
		accountKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	}

	// Generate unique pod name
	suffix, err := randomHexSuffix(4)
	if err != nil {
		return fmt.Errorf("failed to generate pod suffix: %w", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("create-azurite-container-%s", suffix),
			Namespace: namespace,
			Labels: map[string]string{
				"openbao.org/component": "azurite-container-create",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true),
				RunAsUser:    ptr.To(int64(1000)),
				RunAsGroup:   ptr.To(int64(1000)),
				FSGroup:      ptr.To(int64(1000)),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "home-dir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "azure-cli",
					Image: "mcr.microsoft.com/azure-cli:latest",
					Env: []corev1.EnvVar{
						{
							Name:  "HOME",
							Value: "/tmp/home",
						},
						{
							Name:  "TMPDIR",
							Value: "/tmp/home",
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "home-dir",
							MountPath: "/tmp/home",
						},
					},
					Command: []string{"/bin/sh", "-ec"},
					Args: []string{fmt.Sprintf(`
set -e
# Build connection string for Azurite with IP-style URL format
# Format per Microsoft docs:
# DefaultEndpointsProtocol=http;AccountName=<account>;AccountKey=<key>;BlobEndpoint=<endpoint>
# For IP-style URLs, BlobEndpoint should be: http://<host>:<port>/<account-name>
CONN_STRING="DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=%s;BlobEndpoint=%s;"

# Check if container exists to make this operation idempotent
EXISTS=$(az storage container exists \
    --name "%s" \
    --connection-string "$CONN_STRING" \
    --output tsv | tr -d '\r' || echo "False")

if [ "$EXISTS" = "True" ]; then
  echo "Container '%s' already exists."
  exit 0
fi

# Create if not exists
echo "Creating container '%s'..."
az storage container create \
    --name "%s" \
    --connection-string "$CONN_STRING" \
    --fail-on-exist \
    --output none

echo "Successfully created container '%s'."
`, accountKey, endpoint, containerName, containerName, containerName, containerName, containerName)},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot: ptr.To(true),
						// Note: ReadOnlyRootFilesystem removed for e2e testing to allow Azure CLI to use /tmp
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
				},
			},
		},
	}

	result, err := RunPodUntilCompletion(ctx, restCfg, c, pod, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to run container creation pod: %w", err)
	}

	if result.Phase != corev1.PodSucceeded {
		return fmt.Errorf("container creation pod failed, phase=%s, logs:\n%s", result.Phase, result.Logs)
	}

	_ = DeletePodBestEffort(ctx, c, namespace, pod.Name)
	return nil
}
