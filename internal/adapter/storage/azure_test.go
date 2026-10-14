package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAzureContainer_Validation(t *testing.T) {
	tests := []struct {
		name        string
		config      AzureClientConfig
		wantErr     bool
		errContains string
	}{
		{
			name:        "Empty Config",
			config:      AzureClientConfig{},
			wantErr:     true,
			errContains: "container is required",
		},
		{
			name: "Missing Account and Connection String",
			config: AzureClientConfig{
				Container: "test-container",
			},
			wantErr:     true,
			errContains: "storage account or connection string is required",
		},
		{
			name: "Managed Identity Missing Storage Account",
			config: AzureClientConfig{
				Container:          "test-container",
				UseManagedIdentity: true,
			},
			wantErr:     true,
			errContains: "storage account is required when using managed identity",
		},
		{
			name: "Valid Connection String",
			config: AzureClientConfig{
				Container:        "test-container",
				ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=U29tZUtleQ==;EndpointSuffix=core.windows.net",
				EnsureExists:     false, // Prevent network call
			},
			wantErr: false,
		},
		{
			name: "Valid Shared Key",
			config: AzureClientConfig{
				Container:      "test-container",
				StorageAccount: "testaccount",
				AccountKey:     "U29tZUtleQ==", // Base64 encoded "SomeKey"
				EnsureExists:   false,          // Prevent network call
			},
			wantErr: false,
		},
		{
			name: "Valid Managed Identity With Client ID",
			config: AzureClientConfig{
				Container:               "test-container",
				StorageAccount:          "testaccount",
				UseManagedIdentity:      true,
				ManagedIdentityClientID: "client-id",
				EnsureExists:            false, // Prevent network call
			},
			wantErr: false,
		},
		{
			name: "Valid Emulator Config (Azurite)",
			config: AzureClientConfig{
				Container:          "test-container",
				StorageAccount:     "devstoreaccount1",
				AccountKey:         "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==", // Azurite default key
				Endpoint:           "http://127.0.0.1:10000/devstoreaccount1",
				InsecureSkipVerify: true,
				EnsureExists:       false,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := OpenAzureContainer(ctx, tt.config)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, store)
			} else {
				// We expect success for client creation validation
				// Note: Real network failures might still occur if EnsureExists was true
				require.NoError(t, err)
				require.NotNil(t, store)
				_ = store.Close()
			}
		})
	}
}

func TestBuildAzureHTTPClient(t *testing.T) {
	tests := []struct {
		name    string
		config  AzureClientConfig
		wantErr bool
	}{
		{
			name:   "Default Config",
			config: AzureClientConfig{},
		},
		{
			name: "Insecure Skip Verify",
			config: AzureClientConfig{
				InsecureSkipVerify: true,
			},
		},
		{
			name: "Invalid CA Cert",
			config: AzureClientConfig{
				CACert: []byte("invalid-cert"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := buildAzureHTTPClient(tt.config)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				require.NotNil(t, client)
			}
		})
	}
}
