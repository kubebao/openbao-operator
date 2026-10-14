package backup

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestBuildBackupJob_NestedS3Role(t *testing.T) {
	for _, role := range []string{"nested-role", ""} {
		t.Run("role="+role, func(t *testing.T) {
			cluster := newTestClusterWithBackup("nested-role", "default")
			cluster.Spec.Backup.Target.RoleARN = "legacy-role"
			cluster.Spec.Backup.Target.S3 = &api.S3TargetConfig{RoleARN: ptr.To(role)}
			job, err := BuildJob(cluster, JobOptions{JobName: testBackupJobName, JobType: JobTypeScheduled})
			require.NoError(t, err)
			container := job.Spec.Template.Spec.Containers[0]
			env := map[string]string{}
			for _, value := range container.Env {
				env[value.Name] = value.Value
			}
			require.Equal(t, role, env[constants.EnvAWSRoleARN])
			var tokenVolume, tokenMount bool
			for _, volume := range job.Spec.Template.Spec.Volumes {
				if volume.Name == awsWebIdentityVolumeName {
					tokenVolume = true
					require.NotNil(t, volume.Projected)
					require.Equal(t, "sts.amazonaws.com", volume.Projected.Sources[0].ServiceAccountToken.Audience)
				}
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == awsWebIdentityVolumeName {
					tokenMount = true
					require.True(t, mount.ReadOnly)
				}
			}
			require.Equal(t, role != "", tokenVolume)
			require.Equal(t, role != "", tokenMount)
			if role == "" {
				require.NotContains(t, env, constants.EnvAWSWebIdentityTokenFile)
			} else {
				require.Equal(t, awsWebIdentityTokenFile, env[constants.EnvAWSWebIdentityTokenFile])
			}
		})
	}
}

func TestControllerStorageReads_UseEffectiveS3Role(t *testing.T) {
	for _, role := range []string{"nested-role", ""} {
		t.Run("role="+role, func(t *testing.T) {
			cluster := newTestClusterWithBackup("nested-role", "default")
			cluster.Spec.Backup.Target.RoleARN = "legacy-role"
			cluster.Spec.Backup.Target.S3 = &api.S3TargetConfig{RoleARN: ptr.To(role)}
			cluster.Spec.Backup.Target.CredentialsSecretRef = &corev1.LocalObjectReference{Name: "storage-creds"}
			require.Equal(t, role == "", shouldReadBackupSizeFromObjectStorage(cluster))
		})
	}
}
