package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAzureContainer_ServiceEndpointRequests(t *testing.T) {
	// Keep token acquisition local for both Azure token-credential branches.
	// This tests Blob request addressing, not Azure federation.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-identity-header", r.Header.Get("X-IDENTITY-HEADER"))
		assert.Equal(t, "https://storage.azure.com", r.URL.Query().Get("resource"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"test-storage-token","expires_on":"%d","token_type":"Bearer"}`,
			time.Now().Add(time.Hour).Unix())
	}))
	t.Cleanup(tokenServer.Close)
	t.Setenv("IDENTITY_ENDPOINT", tokenServer.URL)
	t.Setenv("IDENTITY_HEADER", "test-identity-header")
	t.Setenv("IDENTITY_SERVER_THUMBPRINT", "")
	t.Setenv("AZURE_TOKEN_CREDENTIALS", "ManagedIdentityCredential")
	t.Setenv("AZURE_CLIENT_ID", "test-client-id")

	authMethods := []struct {
		name             string
		config           AzureClientConfig
		connectionString bool
	}{
		{name: "shared key", config: AzureClientConfig{AccountKey: "U29tZUtleQ=="}},
		{name: "managed identity", config: AzureClientConfig{
			UseManagedIdentity: true, ManagedIdentityClientID: "test-client-id",
		}},
		{name: "default credential", config: AzureClientConfig{UseManagedIdentity: true}},
		{name: "connection string", connectionString: true},
	}
	endpoints := []struct {
		name string
		path string
	}{
		{name: "Azure service URL"},
		{name: "custom service path", path: "/blob/service"},
		{name: "Azurite account path", path: "/devstoreaccount1"},
		{name: "trailing slash", path: "/blob/service/"},
	}

	for _, auth := range authMethods {
		for _, endpoint := range endpoints {
			t.Run(auth.name+"/"+endpoint.name, func(t *testing.T) {
				testAzureEndpointOperations(t, auth.config, auth.connectionString, endpoint.path)
			})
		}
	}
}

func testAzureEndpointOperations(t *testing.T, cfg AzureClientConfig, connectionString bool, servicePath string) {
	t.Helper()
	const (
		containerName = "snapshots"
		key           = "clusters/backup.snap"
		payload       = "snapshot-data"
		lastModified  = "Wed, 14 Oct 2026 10:00:00 GMT"
	)
	type requestRecord struct {
		method      string
		path        string
		isContainer bool
	}
	var (
		mu       sync.Mutex
		requests []requestRecord
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isContainer := r.URL.Query().Get("restype") == "container"
		mu.Lock()
		requests = append(requests, requestRecord{r.Method, r.URL.Path, isContainer})
		mu.Unlock()
		if cfg.UseManagedIdentity {
			assert.Equal(t, "Bearer test-storage-token", r.Header.Get("Authorization"))
		} else {
			assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "SharedKey testaccount:"))
		}
		w.Header().Set("Last-Modified", lastModified)
		w.Header().Set("x-ms-creation-time", lastModified)
		w.Header().Set("ETag", `"test-etag"`)
		switch {
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && isContainer:
			assert.Equal(t, "list", r.URL.Query().Get("comp"))
			assert.Equal(t, "clusters/", r.URL.Query().Get("prefix"))
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(w, `<EnumerationResults><Blobs><Blob><Name>%s</Name><Properties>`+
				`<Last-Modified>%s</Last-Modified><Content-Length>%d</Content-Length>`+
				`</Properties></Blob></Blobs><NextMarker></NextMarker></EnumerationResults>`,
				key, lastModified, len(payload))
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, payload)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "unexpected operation", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	cfg.StorageAccount = "testaccount"
	cfg.Container = containerName
	cfg.Endpoint = server.URL + servicePath
	cfg.InsecureSkipVerify = true
	cfg.EnsureExists = true
	if connectionString {
		cfg.ConnectionString = "DefaultEndpointsProtocol=https;AccountName=testaccount;AccountKey=U29tZUtleQ==;" +
			"BlobEndpoint=" + cfg.Endpoint
		cfg.Endpoint = "https://ignored.example.invalid/wrong-path"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := OpenAzureContainer(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })

	require.NoError(t, store.Upload(ctx, key, strings.NewReader(payload)))
	reader, err := store.Download(ctx, key)
	require.NoError(t, err)
	body, readErr := io.ReadAll(reader)
	require.NoError(t, reader.Close())
	require.NoError(t, readErr)
	assert.Equal(t, payload, string(body))
	info, err := store.Head(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, int64(len(payload)), info.Size)
	objects, err := store.List(ctx, "clusters/")
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, key, objects[0].Key)
	require.NoError(t, store.Delete(ctx, key))

	containerPath := strings.TrimSuffix(servicePath, "/") + "/" + containerName
	mu.Lock()
	defer mu.Unlock()
	for _, request := range requests {
		wantPath := containerPath + "/" + key
		if request.isContainer {
			wantPath = containerPath
		}
		assert.Equal(t, wantPath, request.path, "%s request", request.method)
	}
}

func TestOpenAzureContainer_DefaultServiceURL(t *testing.T) {
	for _, cfg := range []AzureClientConfig{
		{AccountKey: "U29tZUtleQ=="},
		{UseManagedIdentity: true, ManagedIdentityClientID: "test-client-id"},
		{UseManagedIdentity: true},
	} {
		cfg.StorageAccount = "testaccount"
		cfg.Container = "snapshots"
		store, err := OpenAzureContainer(context.Background(), cfg)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, store.Close()) })
		bucket, ok := store.(*Bucket)
		require.True(t, ok)
		var client *container.Client
		require.True(t, bucket.bucket.As(&client))
		assert.Equal(t, "https://testaccount.blob.core.windows.net/snapshots", client.URL())
	}
}
