package helpers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RustFSConfig defines how to run RustFS (S3-compatible storage) for backup testing.
type RustFSConfig struct {
	Namespace   string
	Name        string
	Image       string
	AccessKey   string
	SecretKey   string
	Replicas    int32
	StorageSize string
	Buckets     []string // Buckets to create after RustFS is ready
}

// DefaultRustFSConfig returns a default RustFS configuration.
func DefaultRustFSConfig() RustFSConfig {
	return RustFSConfig{
		Name:        "rustfs",
		Image:       envOrDefault(EnvRustFSImage, DefaultRustFSImage),
		AccessKey:   "rustfsadmin",
		SecretKey:   "rustfsadmin",
		Replicas:    1, // Use 1 replica for e2e tests (distributed mode requires more setup)
		StorageSize: "10Gi",
	}
}

// EnsureRustFS creates (or reuses) a RustFS Deployment + Service for S3-compatible storage.
// The service is reachable at http://<name>.<namespace>.svc:9000.
// If Buckets are specified in cfg, they will be created after RustFS is ready.
// restCfg is required if buckets need to be created.
//
//nolint:gocyclo // End-to-end provisioning must be explicit to simplify troubleshooting in CI.
func EnsureRustFS(ctx context.Context, c client.Client, restCfg *rest.Config, cfg RustFSConfig) error {
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
	if cfg.AccessKey == "" {
		return fmt.Errorf("access key is required")
	}
	if cfg.SecretKey == "" {
		return fmt.Errorf("secret key is required")
	}
	if cfg.Replicas <= 0 {
		cfg.Replicas = 1
	}
	if cfg.StorageSize == "" {
		cfg.StorageSize = "10Gi"
	}

	// Create credentials Secret
	credentialsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-credentials",
			Namespace: cfg.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"access_key": []byte(cfg.AccessKey),
			"secret_key": []byte(cfg.SecretKey),
		},
	}
	err := c.Create(ctx, credentialsSecret)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create RustFS credentials Secret %s/%s: %w", cfg.Namespace, credentialsSecret.Name, err)
	}

	// Create ConfigMap with RustFS configuration
	// RustFS can be configured via environment variables or config file
	// For simplicity, we use environment variables
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-config",
			Namespace: cfg.Namespace,
		},
		Data: map[string]string{
			"RUSTFS_ADDRESS":         "[::]:9000",
			"RUSTFS_CONSOLE_ADDRESS": "[::]:9001",
			"RUSTFS_LOG_LEVEL":       "info",
		},
	}
	err = c.Create(ctx, configMap)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create RustFS ConfigMap %s/%s: %w", cfg.Namespace, configMap.Name, err)
	}

	// Create Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-svc",
			Namespace: cfg.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": cfg.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "api",
					Port:       9000,
					TargetPort: intstr.FromInt32(9000),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "console",
					Port:       9001,
					TargetPort: intstr.FromInt32(9001),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
	err = c.Create(ctx, svc)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create RustFS Service %s/%s: %w", cfg.Namespace, svc.Name, err)
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
			Replicas: ptr.To(cfg.Replicas),
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
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(false),
						RunAsUser:    ptr.To(int64(0)),
						RunAsGroup:   ptr.To(int64(0)),
						FSGroup:      ptr.To(int64(0)),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "rustfs",
							Image:           cfg.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 9000, Name: "api"},
								{ContainerPort: 9001, Name: "console"},
							},
							Env: []corev1.EnvVar{
								{
									Name: "RUSTFS_ACCESS_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: credentialsSecret.Name,
											},
											Key: "access_key",
										},
									},
								},
								{
									Name: "RUSTFS_SECRET_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: credentialsSecret.Name,
											},
											Key: "secret_key",
										},
									},
								},
								{
									Name:  "RUSTFS_ADDRESS",
									Value: "[::]:9000",
								},
								{
									Name:  "RUSTFS_CONSOLE_ADDRESS",
									Value: "[::]:9001",
								},
								{
									Name:  "RUSTFS_LOG_LEVEL",
									Value: "info",
								},
								{
									Name:  "RUSTFS_CONSOLE_ENABLE",
									Value: "true",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
								{
									Name:      "logs",
									MountPath: "/logs",
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(9000),
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       10,
								TimeoutSeconds:      5,
								FailureThreshold:    3,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(9000),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
								TimeoutSeconds:      3,
								FailureThreshold:    3,
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								RunAsNonRoot: ptr.To(false),
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
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "logs",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	err = c.Create(ctx, deployment)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create RustFS Deployment %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	// Wait for Deployment to be ready
	deploymentReadyDeadline := time.NewTimer(5 * time.Minute)
	defer deploymentReadyDeadline.Stop()
	deploymentReadyTicker := time.NewTicker(2 * time.Second)
	defer deploymentReadyTicker.Stop()

	for {
		current := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}, current); err != nil {
			return fmt.Errorf("failed to get RustFS Deployment %s/%s: %w", cfg.Namespace, cfg.Name, err)
		}

		if current.Status.ReadyReplicas >= cfg.Replicas && current.Status.ReadyReplicas == current.Status.Replicas {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"context canceled while waiting for RustFS Deployment %s/%s to be ready: %w",
				cfg.Namespace,
				cfg.Name,
				ctx.Err(),
			)
		case <-deploymentReadyDeadline.C:
			return fmt.Errorf(
				"timed out waiting for RustFS Deployment %s/%s to be ready (ready=%d/%d, replicas=%d)\n%s",
				cfg.Namespace,
				cfg.Name,
				current.Status.ReadyReplicas,
				cfg.Replicas,
				current.Status.Replicas,
				formatDeploymentPodDiagnostics(ctx, c, cfg.Namespace, cfg.Name),
			)
		case <-deploymentReadyTicker.C:
		}
	}

	// Wait for RustFS to be ready to accept connections
	rustfsAddr := fmt.Sprintf("http://%s-svc.%s.svc:9000", cfg.Name, cfg.Namespace)

	var lastErr error
	readinessTimer := time.NewTimer(2 * time.Minute)
	defer readinessTimer.Stop()
	readinessTicker := time.NewTicker(2 * time.Second)
	defer readinessTicker.Stop()

	for {
		if err := checkRustFSReadiness(ctx, c, cfg.Namespace, cfg.Name); err != nil {
			lastErr = err
		} else {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"context canceled while waiting for RustFS %s/%s to be ready: %w",
				cfg.Namespace,
				cfg.Name,
				ctx.Err(),
			)
		case <-readinessTimer.C:
			return fmt.Errorf("timed out waiting for RustFS %s/%s to be ready: %w", cfg.Namespace, cfg.Name, lastErr)
		case <-readinessTicker.C:
		}
	}

	// Create buckets if specified
	if len(cfg.Buckets) > 0 {
		if restCfg == nil {
			return fmt.Errorf("rest config is required to create buckets")
		}
		if err := createRustFSBuckets(ctx, restCfg, c, cfg, rustfsAddr); err != nil {
			return fmt.Errorf("failed to create RustFS buckets: %w", err)
		}
	}

	return nil
}

// checkRustFSReadiness checks if RustFS is ready by checking the health endpoint.
func checkRustFSReadiness(ctx context.Context, c client.Client, namespace, name string) error {
	// Get a pod from the deployment
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{"app": name}); err != nil {
		return fmt.Errorf("failed to list RustFS pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no RustFS pods found")
	}

	// Check if at least one pod is ready
	for i := range pods.Items {
		pod := pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					return nil
				}
			}
		}
	}

	return fmt.Errorf("no ready RustFS pods found")
}

// createRustFSBuckets creates the specified buckets in RustFS using AWS CLI.
func createRustFSBuckets(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	cfg RustFSConfig,
	endpoint string,
) error {
	if len(cfg.Buckets) == 0 {
		return nil
	}

	// Create a pod with AWS CLI to create buckets
	// Use amazon/aws-cli image which is commonly available
	awsCliImage := "amazon/aws-cli:latest"

	// Build the command to create all buckets
	bucketCommands := make([]string, 0, len(cfg.Buckets))
	for _, bucket := range cfg.Buckets {
		if bucket == "" {
			continue
		}
		// Create bucket if it doesn't exist
		// Use --endpoint-url for S3-compatible storage
		bucketCommands = append(bucketCommands, fmt.Sprintf(
			"aws --endpoint-url=%s s3 mb s3://%s || aws --endpoint-url=%s s3 ls s3://%s || exit 1",
			endpoint, bucket, endpoint, bucket,
		))
	}

	if len(bucketCommands) == 0 {
		return nil
	}

	command := strings.Join(bucketCommands, " && ")

	suffix, err := randomHexSuffix(4)
	if err != nil {
		return fmt.Errorf("failed to generate bucket creation pod suffix: %w", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			// Use a unique name to avoid cross-test/node races when e2e runs in parallel.
			Name:      fmt.Sprintf("%s-create-buckets-%s", cfg.Name, suffix),
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"openbao.org/component": "rustfs-bucket-create",
				"openbao.org/rustfs":    cfg.Name,
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
			Containers: []corev1.Container{
				{
					Name:  "aws-cli",
					Image: awsCliImage,
					Env: []corev1.EnvVar{
						{
							Name:  "AWS_ACCESS_KEY_ID",
							Value: cfg.AccessKey,
						},
						{
							Name:  "AWS_SECRET_ACCESS_KEY",
							Value: cfg.SecretKey,
						},
						{
							Name:  "AWS_DEFAULT_REGION",
							Value: "us-east-1", // Required by AWS CLI, but not used by RustFS
						},
					},
					Command: []string{"/bin/sh", "-ec"},
					Args:    []string{command},
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

	result, err := RunPodUntilCompletion(ctx, restCfg, c, pod, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to run bucket creation pod: %w", err)
	}

	if result.Phase != corev1.PodSucceeded {
		return fmt.Errorf("bucket creation pod failed, phase=%s, logs:\n%s", result.Phase, result.Logs)
	}

	_ = DeletePodBestEffort(ctx, c, cfg.Namespace, pod.Name)
	return nil
}

func randomHexSuffix(bytes int) (string, error) {
	if bytes <= 0 {
		return "", fmt.Errorf("bytes must be positive")
	}

	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// CleanupRustFS best-effort deletes the RustFS resources created by EnsureRustFS.
// It is safe to call even if resources were partially created or already removed.
func CleanupRustFS(ctx context.Context, c client.Client, cfg RustFSConfig) {
	// Order: deployment -> service -> secrets/configmap
	_ = c.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cfg.Name, Namespace: cfg.Namespace}})
	_ = c.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: cfg.Name + "-svc", Namespace: cfg.Namespace}})
	_ = c.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-credentials",
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
