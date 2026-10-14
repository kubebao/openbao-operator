package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestBackupTargetEffectiveS3Options(t *testing.T) {
	for _, tc := range []struct {
		name   string
		s3     *S3TargetConfig
		region string
		path   bool
		role   string
	}{
		{name: "legacy", region: "eu-west-1", path: true, role: "legacy-role"},
		{name: "empty block", s3: &S3TargetConfig{}, region: "eu-west-1", path: true, role: "legacy-role"},
		{name: "nested", s3: &S3TargetConfig{
			Region: ptr.To("eu-central-1"), UsePathStyle: ptr.To(false), RoleARN: ptr.To("nested-role"),
		}, region: "eu-central-1", role: "nested-role"},
		{name: "partial block", s3: &S3TargetConfig{UsePathStyle: ptr.To(false)}, region: "eu-west-1", role: "legacy-role"},
		{name: "empty overrides", s3: &S3TargetConfig{Region: ptr.To(""), RoleARN: ptr.To("")}, path: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := BackupTarget{Region: "eu-west-1", UsePathStyle: true, RoleARN: "legacy-role", S3: tc.s3}
			require.Equal(t, tc.region, target.EffectiveS3Region())
			require.Equal(t, tc.path, target.EffectiveS3UsePathStyle())
			require.Equal(t, tc.role, target.EffectiveS3RoleARN())
			require.Equal(t, "eu-west-1", target.Region)
			require.True(t, target.UsePathStyle)
			require.Equal(t, "legacy-role", target.RoleARN)
		})
	}
}

func TestBackupTargetDeepCopyPreservesS3OptionPresence(t *testing.T) {
	target := &BackupTarget{S3: &S3TargetConfig{
		Region: ptr.To("eu-west-1"), UsePathStyle: ptr.To(false), RoleARN: ptr.To(""),
	}}
	copy := target.DeepCopy()
	require.Equal(t, target, copy)
	*copy.S3.Region, *copy.S3.UsePathStyle, *copy.S3.RoleARN = "changed", true, "changed"
	require.Equal(t, "eu-west-1", *target.S3.Region)
	require.False(t, *target.S3.UsePathStyle)
	require.Empty(t, *target.S3.RoleARN)
	copy = (&BackupTarget{S3: &S3TargetConfig{}}).DeepCopy()
	require.Nil(t, copy.S3.Region)
	require.Nil(t, copy.S3.UsePathStyle)
	require.Nil(t, copy.S3.RoleARN)
}
