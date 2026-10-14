package restore

import (
	"fmt"
	"maps"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/auth"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	"github.com/kubebao/openbao-operator/internal/adapter/storageenv"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/workloadidentity"
)

const (
	// Volume and mount names for restore jobs
	restoreTLSCAVolumeName      = "tls-ca"
	restoreTLSCAMountPath       = constants.PathTLS
	restoreCredentialsVolume    = "storage-credentials"
	restoreCredentialsMountPath = constants.PathBackupCredentials // Same path as backup for LoadExecutorConfig
	restoreJWTTokenVolumeName   = "jwt-token"
	restoreJWTTokenMountPath    = "/var/run/secrets/tokens" // #nosec G101 -- mount path not credential
	restoreTokenVolumeName      = "restore-token"
	restoreTokenMountPath       = "/etc/bao/restore/token" // #nosec G101 -- mount path not credential
	restoreAWSIdentityVolume    = "aws-iam-token"
	restoreAWSIdentityMountPath = "/var/run/secrets/aws"
	restoreAWSIdentityTokenFile = "/var/run/secrets/aws/token" // #nosec G101 -- mount path not credential
	restoreAWSIdentityAudience  = "sts.amazonaws.com"          // #nosec G101 -- audience constant, not a credential
)

func getRestoreExecutorImage(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster) (string, error) {
	if restore.Spec.Image != "" {
		return restore.Spec.Image, nil
	}
	if cluster.Spec.Backup != nil && cluster.Spec.Backup.Image != "" {
		return cluster.Spec.Backup.Image, nil
	}
	image, err := constants.DefaultBackupImage()
	if err != nil {
		return "", operatorerrors.WrapPermanentConfig(operatorerrors.WithReason(
			constants.ReasonHelperImageConfigurationInvalid,
			fmt.Errorf(
				"default restore executor image is unavailable; set spec.image on the OpenBaoRestore, set spec.backup.image on the OpenBaoCluster, or configure OPERATOR_VERSION in the operator Deployment: %w",
				err,
			),
		))
	}
	return image, nil
}

// buildRestoreJob creates a Kubernetes Job for executing the restore.
func (m *Manager) buildRestoreJob(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster, verifiedExecutorDigest string) (*batchv1.Job, error) {
	if restore.Status.Execution == nil || restore.Status.Execution.PreparedAt == nil ||
		restore.Status.Execution.PreparedAt.IsZero() {
		return nil, fmt.Errorf("restore staging requires a persisted preparation timestamp")
	}
	preparationDeadline := restore.Status.Execution.PreparedAt.Add(constants.DefaultRestorePreparationTimeout)
	jobName := restoreJobName(restore)
	labels := restoreLabels(cluster)
	security.AddManagedWorkloadSecurityLabels(labels, cluster)
	podTemplateLabels := maps.Clone(labels)
	workloadidentity.MergePodLabels(podTemplateLabels, restore.Spec.Source.Target)

	executorImage, err := getRestoreExecutorImage(restore, cluster)
	if err != nil {
		return nil, err
	}

	image := verifiedExecutorDigest
	if image == "" {
		image = executorImage
	}

	// Build environment variables
	envVars := buildRestoreEnvVars(restore, cluster, m.clientConfig)
	envVars = append(envVars, corev1.EnvVar{
		Name: constants.EnvRestorePreparationDeadline, Value: preparationDeadline.UTC().Format(time.RFC3339),
	})

	tlsTrust, err := portopenbao.ResolveClientTrustBundle(cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve restore TLS trust source: %w", err)
	}
	tlsServerName := portopenbao.ComputeTLSServerName(cluster)
	if tlsServerName != "" {
		envVars = append(envVars, corev1.EnvVar{Name: constants.EnvTLSServerName, Value: tlsServerName})
	}
	if !tlsTrust.UseSystemRoots {
		envVars = append(envVars, corev1.EnvVar{Name: constants.EnvTLSCAPath, Value: constants.PathTLSCACert})
	}

	// Build volumes and mounts
	volumes := buildRestoreVolumes(restore, cluster, tlsTrust)
	volumes = append(volumes, corev1.Volume{Name: "claim-api", VolumeSource: corev1.VolumeSource{
		Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
			{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr.To(int64(3600))}},
			{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
		}},
	}})
	volumeMounts := buildRestoreVolumeMounts(restore, cluster, tlsTrust)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "claim-api", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true})
	envVars = append(envVars, corev1.EnvVar{Name: "RESTORE_REQUEST_NAME", Value: restore.Name},
		corev1.EnvVar{Name: "RESTORE_REQUEST_UID", Value: string(restore.UID)})
	for _, field := range []struct{ name, path string }{{"POD_NAME", "metadata.name"}, {"POD_UID", "metadata.uid"}} {
		envVars = append(envVars, corev1.EnvVar{Name: field.name, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: field.path}}})
	}
	scratchLimit := resource.NewQuantity(constants.DefaultRestoreScratchLimitBytes, resource.BinarySI)
	volumes = append(volumes, corev1.Volume{
		Name:         "restore-scratch",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: scratchLimit}},
	})
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name: "restore-scratch", MountPath: constants.PathRestoreScratch,
	})
	ephemeralStorage := *resource.NewQuantity(constants.DefaultRestoreEphemeralStorageBytes, resource.BinarySI)

	// Build container
	container := corev1.Container{
		Name:  "restore",
		Image: image,
		Env:   envVars,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		// SECURITY: Resource limits prevent restore jobs from exhausting node resources
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("100m"),
				corev1.ResourceEphemeralStorage: ephemeralStorage,
				corev1.ResourceMemory:           resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("500m"),
				corev1.ResourceEphemeralStorage: ephemeralStorage,
				corev1.ResourceMemory:           resource.MustParse("512Mi"),
			},
		},
		VolumeMounts: volumeMounts,
	}

	// Keep the Job until the controller persists its terminal result and all
	// post-restore recovery. The controller removes it deliberately afterward.
	// A failed executor can have submitted its snapshot before losing the
	// response. Do not restart it to retry the entire destructive operation.
	// Job settings alone do not prevent duplicate Pods from being started.
	backoffLimit := int32(0)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: restore.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				restoreExecutionIDAnnotation: restoreExecutionOperationID(restore),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: ptr.To(int64((constants.DefaultRestorePreparationTimeout + 10*time.Minute).Seconds())),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podTemplateLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:           resourceidentity.RestoreServiceAccountName(cluster, restore),
					AutomountServiceAccountToken: ptr.To(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext: func() *corev1.PodSecurityContext {
						podSecurityContext := &corev1.PodSecurityContext{
							RunAsNonRoot: ptr.To(true),
							SeccompProfile: &corev1.SeccompProfile{
								Type: corev1.SeccompProfileTypeRuntimeDefault,
							},
						}

						// For OpenShift, we must NOT set RunAsUser, RunAsGroup, or FSGroup.
						// OpenShift assigns these dynamically via Security Context Constraints (SCC).
						// For standard Kubernetes (default), we pin them to ensure file ownership matches the image.
						if m.Platform != constants.PlatformOpenShift {
							podSecurityContext.RunAsUser = ptr.To(constants.UserBackup)
							podSecurityContext.RunAsGroup = ptr.To(constants.GroupBackup)
							podSecurityContext.FSGroup = ptr.To(constants.GroupBackup)
						}

						return podSecurityContext
					}(),
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}

	return job, nil
}

// buildRestoreEnvVars builds environment variables for the restore job.
func buildRestoreEnvVars(
	restore *openbaov1alpha1.OpenBaoRestore,
	cluster *openbaov1alpha1.OpenBaoCluster,
	clientConfigs ...portopenbao.ClientConfig,
) []corev1.EnvVar {
	clientConfig := portopenbao.ClientConfig{}
	if len(clientConfigs) > 0 {
		clientConfig = clientConfigs[0]
	}

	destination := storageenv.ResolveDestination(restore.Spec.Source.Target)

	envVars := []corev1.EnvVar{
		// Set executor mode to restore
		{
			Name:  "EXECUTOR_MODE",
			Value: "restore",
		},
		// Cluster info
		{
			Name:  constants.EnvClusterName,
			Value: cluster.Name,
		},
		{
			Name:  constants.EnvStatefulSetName,
			Value: restoreTargetStatefulSetName(cluster),
		},
		{
			Name:  constants.EnvClusterNamespace,
			Value: cluster.Namespace,
		},
		{
			Name:  constants.EnvClusterReplicas,
			Value: fmt.Sprintf("%d", cluster.Spec.Replicas),
		},
		// BACKUP_* env vars are required by LoadExecutorConfig for validation.
		// The executor will use RESTORE_* values when available.
		{
			Name:  constants.EnvBackupProvider,
			Value: destination.Provider,
		},
		{
			Name:  constants.EnvBackupEndpoint,
			Value: destination.Endpoint,
		},
		{
			Name:  constants.EnvBackupBucket,
			Value: destination.Bucket,
		},
		// Restore-specific overrides (used by runRestore function)
		{
			Name:  constants.EnvRestoreKey,
			Value: restore.Spec.Source.Key,
		},
		{
			Name:  constants.EnvRestoreBucket,
			Value: destination.Bucket,
		},
		{
			Name:  constants.EnvRestoreEndpoint,
			Value: destination.Endpoint,
		},
		{
			Name:  constants.EnvRestoreForce,
			Value: fmt.Sprintf("%t", restore.Spec.Force),
		},
	}

	envVars = storageenv.AppendProviderEnvVars(envVars, restore.Spec.Source.Target)
	envVars = storageenv.AppendRestoreProviderEnvVars(envVars, restore.Spec.Source.Target)
	if destination.Provider == constants.StorageProviderS3 && restore.Spec.Source.Target.EffectiveS3RoleARN() != "" {
		envVars = append(envVars,
			corev1.EnvVar{Name: constants.EnvAWSRoleARN, Value: restore.Spec.Source.Target.EffectiveS3RoleARN()},
			corev1.EnvVar{Name: constants.EnvAWSWebIdentityTokenFile, Value: restoreAWSIdentityTokenFile},
		)
	}

	// JWT auth configuration
	jwtRole := effectiveRestoreJWTRole(restore, cluster)
	envVars = storageenv.AppendAuthEnvVars(envVars, jwtRole, restoreUsesStaticTokenAuth(restore, cluster))
	if jwtRole != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  constants.EnvOpenBaoJWTAuthStrategy,
			Value: portopenbao.NormalizeJWTAuthStrategyOrDefault(clientConfig.JWTAuthStrategy),
		})
	}

	// Note: Credentials are mounted as a volume and read by the executor based on provider.
	// S3-specific env vars (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY) are not needed
	// as the executor reads credentials from the mounted secret.

	return envVars
}

func restoreTargetStatefulSetName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster == nil {
		return ""
	}
	if cluster.Status.BlueGreen != nil && cluster.Status.BlueGreen.BlueRevision != "" {
		return fmt.Sprintf("%s-%s", cluster.Name, cluster.Status.BlueGreen.BlueRevision)
	}
	return cluster.Name
}

// buildRestoreVolumes builds volumes for the restore job.
func buildRestoreVolumes(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster, tlsTrust portopenbao.TrustBundleSource) []corev1.Volume {
	var volumes []corev1.Volume

	// TLS CA volume (when a trust bundle Secret is required)
	if !tlsTrust.UseSystemRoots {
		volumes = append(volumes, corev1.Volume{
			Name: restoreTLSCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: tlsTrust.SecretName,
					Items: []corev1.KeyToPath{
						{
							Key:  tlsTrust.SecretKey,
							Path: "ca.crt",
						},
					},
				},
			},
		})
	}

	// JWT token volume (if using JWT auth)
	if effectiveRestoreJWTRole(restore, cluster) != "" {
		audience := auth.OpenBaoJWTAudience()
		expirationSeconds := int64(3600)
		volumes = append(volumes, corev1.Volume{
			Name: restoreJWTTokenVolumeName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          audience,
								ExpirationSeconds: &expirationSeconds,
								Path:              "openbao-token",
							},
						},
					},
				},
			},
		})
	}

	// Static token volume (if using token auth)
	if restoreUsesStaticTokenAuth(restore, cluster) {
		volumes = append(volumes, corev1.Volume{
			Name: restoreTokenVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  restore.Spec.TokenSecretRef.Name,
					DefaultMode: ptr.To(int32(0400)),
				},
			},
		})
	}

	// S3 credentials volume (if using credentials secret)
	if restore.Spec.Source.Target.CredentialsSecretRef != nil {
		defaultMode := int32(0400)
		volumes = append(volumes, corev1.Volume{
			Name: restoreCredentialsVolume,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  restore.Spec.Source.Target.CredentialsSecretRef.Name,
					DefaultMode: &defaultMode,
				},
			},
		})
	}
	if restore.Spec.Source.Target.EffectiveS3RoleARN() != "" {
		expirationSeconds := int64(3600)
		volumes = append(volumes, corev1.Volume{
			Name: restoreAWSIdentityVolume,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          restoreAWSIdentityAudience,
								ExpirationSeconds: &expirationSeconds,
								Path:              "token",
							},
						},
					},
				},
			},
		})
	}

	return volumes
}

// buildRestoreVolumeMounts builds volume mounts for the restore container.
func buildRestoreVolumeMounts(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster, tlsTrust portopenbao.TrustBundleSource) []corev1.VolumeMount {
	var mounts []corev1.VolumeMount

	// TLS CA mount
	if !tlsTrust.UseSystemRoots {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      restoreTLSCAVolumeName,
			MountPath: restoreTLSCAMountPath,
			ReadOnly:  true,
		})
	}

	// JWT token mount
	if effectiveRestoreJWTRole(restore, cluster) != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      restoreJWTTokenVolumeName,
			MountPath: restoreJWTTokenMountPath,
			ReadOnly:  true,
		})
	}

	// Static token mount
	if restoreUsesStaticTokenAuth(restore, cluster) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      restoreTokenVolumeName,
			MountPath: restoreTokenMountPath,
			ReadOnly:  true,
		})
	}

	// Storage credentials mount
	if restore.Spec.Source.Target.CredentialsSecretRef != nil {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      restoreCredentialsVolume,
			MountPath: restoreCredentialsMountPath,
			ReadOnly:  true,
		})
	}
	if restore.Spec.Source.Target.EffectiveS3RoleARN() != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      restoreAWSIdentityVolume,
			MountPath: restoreAWSIdentityMountPath,
			ReadOnly:  true,
		})
	}

	return mounts
}

func effectiveRestoreJWTRole(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster) string {
	return storageenv.EffectiveJWTRole(
		restore.Spec.JWTAuthRole,
		portauth.OperatorJWTBootstrapEnabled(cluster),
		portauth.RoleNameRestore,
	)
}

func restoreUsesStaticTokenAuth(restore *openbaov1alpha1.OpenBaoRestore, cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return restore.Spec.TokenSecretRef != nil && effectiveRestoreJWTRole(restore, cluster) == ""
}
