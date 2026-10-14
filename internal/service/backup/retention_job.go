package backup

import (
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// buildRetentionJob keeps the successful backup's image and storage identity,
// without its OpenBao credentials, trust bundle, or Kubernetes API access.
func buildRetentionJob(source *batchv1.Job, run *api.BackupRetentionRun) (*batchv1.Job, error) {
	if len(source.Spec.Template.Spec.Containers) != 1 || source.Spec.Template.Spec.Containers[0].Name != ComponentBackup {
		return nil, fmt.Errorf("successful backup Job has no supported executor template")
	}
	template := source.Spec.Template.DeepCopy()
	// The API server adds these labels to the source template. A new Job must
	// receive its own generated name and UID, including the legacy labels.
	for _, name := range []string{batchv1.ControllerUidLabel, batchv1.JobNameLabel, "controller-uid", "job-name"} {
		delete(template.Labels, name)
	}
	template.Labels[constants.LabelOpenBaoBackupType] = retentionType
	template.Labels[labelRetentionRun] = string(run.SourceJobUID)
	template.Spec.AutomountServiceAccountToken = ptr.To(false)
	template.Spec.InitContainers = nil
	template.Spec.EphemeralContainers = nil
	executor := &template.Spec.Containers[0]
	executor.Name = retentionComponent
	executor.EnvFrom = nil
	executor.Env = retentionStorageEnv(executor.Env)
	executor.Env = append(executor.Env,
		corev1.EnvVar{Name: "EXECUTOR_MODE", Value: retentionType},
		corev1.EnvVar{Name: EnvRetentionPrefix, Value: run.Prefix},
		corev1.EnvVar{Name: EnvRetentionKey, Value: run.Key},
		corev1.EnvVar{Name: EnvRetentionRestoreTestKey, Value: run.RestoreTestKey},
		corev1.EnvVar{Name: EnvRetentionMaxCount, Value: strconv.FormatInt(int64(run.Policy.MaxCount), 10)},
		corev1.EnvVar{Name: EnvRetentionMaxAge, Value: run.Policy.MaxAge},
		corev1.EnvVar{Name: EnvRetentionDeadline, Value: run.DeadlineAt.UTC().Format(time.RFC3339)},
		corev1.EnvVar{Name: EnvRetentionSourceJobUID, Value: string(run.SourceJobUID)},
	)
	var mounts []corev1.VolumeMount
	for _, mount := range executor.VolumeMounts {
		if retentionStorageVolume(mount.Name) {
			mounts = append(mounts, mount)
		}
	}
	executor.VolumeMounts = mounts
	var volumes []corev1.Volume
	for _, volume := range template.Spec.Volumes {
		if retentionStorageVolume(volume.Name) {
			volumes = append(volumes, volume)
		}
	}
	template.Spec.Volumes = volumes
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: source.Namespace,
			Labels:      template.Labels,
			Annotations: map[string]string{annotationBackupGeneration: fmt.Sprint(run.Generation)}},
		Spec: batchv1.JobSpec{Template: *template, BackoffLimit: ptr.To(int32(0)),
			ActiveDeadlineSeconds: ptr.To(retentionJobDeadlineSeconds)},
	}, nil
}

func retentionStorageEnv(source []corev1.EnvVar) []corev1.EnvVar {
	var env []corev1.EnvVar
	for _, value := range source {
		switch value.Name {
		case constants.EnvClusterNamespace, constants.EnvClusterName,
			constants.EnvBackupProvider, constants.EnvBackupEndpoint, constants.EnvBackupBucket,
			constants.EnvBackupPathPrefix, constants.EnvBackupRegion, constants.EnvBackupUsePathStyle,
			constants.EnvBackupInsecureSkipVerify, constants.EnvBackupGCSProject,
			constants.EnvBackupAzureStorageAccount, constants.EnvBackupAzureContainer,
			constants.EnvBackupCredentialsPath, constants.EnvAWSRoleARN, constants.EnvAWSWebIdentityTokenFile:
			env = append(env, value)
		}
	}
	return env
}

func retentionStorageVolume(name string) bool {
	return name == backupCredentialsVolumeName || name == awsWebIdentityVolumeName
}
