package restore

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestBuildRestoreJob_NestedS3Role(t *testing.T) {
	for _, role := range []string{"nested-role", ""} {
		t.Run("role="+role, func(t *testing.T) {
			request := &api.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "nested-role", Namespace: "default"},
				Spec: api.OpenBaoRestoreSpec{Cluster: "target", Image: "example.com/restore:v1",
					Source: api.RestoreSource{Key: "snapshot", Target: api.BackupTarget{
						Endpoint: "https://s3.example.com", Bucket: "snapshots", RoleARN: "legacy-role",
						S3: &api.S3TargetConfig{RoleARN: ptr.To(role)},
					}},
				},
			}
			cluster := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default"}}
			request.Status.Execution = newRestoreExecutionStatus(request)
			job, err := (&Manager{Platform: constants.PlatformKubernetes}).buildRestoreJob(request, cluster, "")
			require.NoError(t, err)
			container := job.Spec.Template.Spec.Containers[0]
			env := map[string]string{}
			for _, value := range container.Env {
				env[value.Name] = value.Value
			}
			require.Equal(t, role, env[constants.EnvAWSRoleARN])
			var tokenVolume, tokenMount bool
			for _, volume := range job.Spec.Template.Spec.Volumes {
				if volume.Name == restoreAWSIdentityVolume {
					tokenVolume = true
					require.NotNil(t, volume.Projected)
					require.Equal(t, restoreAWSIdentityAudience, volume.Projected.Sources[0].ServiceAccountToken.Audience)
				}
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == restoreAWSIdentityVolume {
					tokenMount = true
					require.True(t, mount.ReadOnly)
				}
			}
			require.Equal(t, role != "", tokenVolume)
			require.Equal(t, role != "", tokenMount)
			if role == "" {
				require.NotContains(t, env, constants.EnvAWSWebIdentityTokenFile)
			} else {
				require.Equal(t, restoreAWSIdentityTokenFile, env[constants.EnvAWSWebIdentityTokenFile])
			}
		})
	}
}
