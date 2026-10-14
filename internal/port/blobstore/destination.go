package blobstore

import "github.com/kubebao/openbao-operator/internal/platform/constants"

// Destination identifies the storage service and bucket or container to use.
// Credentials, workload identity, and client behavior are configured separately.
type Destination struct {
	Provider string
	Endpoint string
	Bucket   string
	Region   string
}

// EffectiveProvider applies the default provider without changing configured values.
// Callers retain responsibility for validating unsupported providers.
func EffectiveProvider(provider string) string {
	if provider == "" {
		return constants.StorageProviderS3
	}
	return provider
}

// ResolveDestination applies shared provider and region defaults, and the legacy
// Azure container override. The endpoint is returned unchanged.
func ResolveDestination(destination Destination, azureContainer string) Destination {
	destination.Provider = EffectiveProvider(destination.Provider)
	switch destination.Provider {
	case constants.StorageProviderS3:
		if destination.Region == "" {
			destination.Region = constants.DefaultS3Region
		}
	case constants.StorageProviderAzure:
		if azureContainer != "" {
			destination.Bucket = azureContainer
		}
	}
	return destination
}
