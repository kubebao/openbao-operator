package blobstore

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestResolveDestination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          Destination
		azureContainer string
		wantProvider   string
		wantBucket     string
		wantRegion     string
	}{
		{
			name: "default S3 provider and region", input: Destination{Bucket: "backups"},
			wantProvider: constants.StorageProviderS3, wantBucket: "backups", wantRegion: constants.DefaultS3Region,
		},
		{
			name: "configured S3 region", input: Destination{Provider: "s3", Bucket: "backups", Region: "eu-west-1"},
			azureContainer: "ignored", wantProvider: constants.StorageProviderS3,
			wantBucket: "backups", wantRegion: "eu-west-1",
		},
		{
			name: "GCS bucket", input: Destination{Provider: "gcs", Bucket: "backups"},
			azureContainer: "ignored", wantProvider: constants.StorageProviderGCS, wantBucket: "backups",
		},
		{
			name: "Azure bucket fallback", input: Destination{Provider: "azure", Bucket: "backups"},
			wantProvider: constants.StorageProviderAzure, wantBucket: "backups",
		},
		{
			name: "Azure container alias overrides bucket", input: Destination{Provider: "azure", Bucket: "legacy-bucket"},
			azureContainer: "selected-container", wantProvider: constants.StorageProviderAzure,
			wantBucket: "selected-container",
		},
		{
			name: "unsupported provider remains for caller validation", input: Destination{Provider: "other", Bucket: "backups"},
			azureContainer: "ignored", wantProvider: "other", wantBucket: "backups",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.input.Endpoint = "https://storage.example.test/account/path/"
			got := ResolveDestination(tt.input, tt.azureContainer)
			assert.Equal(t, tt.wantProvider, got.Provider)
			assert.Equal(t, tt.wantBucket, got.Bucket)
			assert.Equal(t, tt.wantRegion, got.Region)
			assert.Equal(t, tt.input.Endpoint, got.Endpoint, "resolution must not append or remove URL paths")
			assert.Equal(t, got, ResolveDestination(got, tt.azureContainer), "resolution must be idempotent")
		})
	}
}
