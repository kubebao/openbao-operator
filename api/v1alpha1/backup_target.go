package v1alpha1

// EffectiveS3Region returns the configured S3 region before defaulting.
func (t BackupTarget) EffectiveS3Region() string {
	if t.S3 != nil && t.S3.Region != nil {
		return *t.S3.Region
	}
	return t.Region
}

// EffectiveS3UsePathStyle returns the configured S3 addressing mode.
func (t BackupTarget) EffectiveS3UsePathStyle() bool {
	if t.S3 != nil && t.S3.UsePathStyle != nil {
		return *t.S3.UsePathStyle
	}
	return t.UsePathStyle
}

// EffectiveS3RoleARN returns the configured S3 role, including an empty override.
func (t BackupTarget) EffectiveS3RoleARN() string {
	if t.S3 != nil && t.S3.RoleARN != nil {
		return *t.S3.RoleARN
	}
	return t.RoleARN
}
