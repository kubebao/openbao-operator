package workloadidentity

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestDescribeStorageIdentity_NestedS3Role(t *testing.T) {
	for _, role := range []string{"nested-role", ""} {
		t.Run("role="+role, func(t *testing.T) {
			target := api.BackupTarget{RoleARN: "legacy-role", S3: &api.S3TargetConfig{RoleARN: ptr.To(role)}}
			description := DescribeStorageIdentity(target, "snapshot-reader")
			if role == "" {
				require.Equal(t, StorageIdentityModeAmbient, description.Mode)
			} else {
				require.Equal(t, StorageIdentityModeWorkloadIdentity, description.Mode)
				require.Contains(t, description.Message, role)
			}
			require.NotContains(t, description.Message, "legacy-role")
		})
	}
}
