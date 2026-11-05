package primitives

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type discoveryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f discoveryRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDiscoveryReaderNegotiatesOnlyMetadata(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "metadata returned"
		if reject {
			name = "unsupported metadata has no fallback"
		}
		t.Run(name, func(t *testing.T) {
			var listCalls, getCalls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if request.Header.Get("Authorization") != "Bearer fixture-token" ||
					request.Header.Get("X-Existing-Transport") != "preserved" {
					http.Error(writer, "configured authentication or transport wrapper lost", http.StatusUnauthorized)
					return
				}
				var response any
				switch request.URL.Path {
				case "/api":
					response = metav1.APIVersions{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIVersions"},
						Versions: []string{"v1"}}
				case "/apis":
					response = metav1.APIGroupList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIGroupList"}}
				case "/api/v1":
					response = metav1.APIResourceList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"},
						GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "secrets", Kind: "Secret",
							Namespaced: true, Verbs: metav1.Verbs{"get", "list"}}}}
				case "/api/v1/namespaces/tenant/secrets", "/api/v1/namespaces/tenant/secrets/fixture":
					kind := "PartialObjectMetadata"
					if request.URL.Path == "/api/v1/namespaces/tenant/secrets" {
						kind += "List"
						listCalls.Add(1)
					} else {
						getCalls.Add(1)
					}
					if request.Header.Get("Accept") != "application/json;as="+kind+";g=meta.k8s.io;v=v1" {
						http.Error(writer, "full resource fallback is forbidden", http.StatusBadRequest)
						return
					}
					if reject {
						writer.WriteHeader(http.StatusNotAcceptable)
						response = metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
							Status: "Failure", Code: http.StatusNotAcceptable, Reason: metav1.StatusReasonNotAcceptable}
					} else {
						object := discoveryTestMetadata("meta.k8s.io/v1", "PartialObjectMetadata", "fixture")
						response = object
						if kind == "PartialObjectMetadataList" {
							response = metav1.PartialObjectMetadataList{
								TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: kind},
								Items:    []metav1.PartialObjectMetadata{object}}
						}
					}
				default:
					http.NotFound(writer, request)
					return
				}
				if err := json.NewEncoder(writer).Encode(response); err != nil {
					t.Errorf("write metadata fixture: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			config := &rest.Config{Host: server.URL, BearerToken: "fixture-token",
				TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{
					Type: "CERTIFICATE", Bytes: server.Certificate().Raw})},
				WrapTransport: func(next http.RoundTripper) http.RoundTripper {
					return discoveryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
						request = request.Clone(request.Context())
						request.Header.Set("X-Existing-Transport", "preserved")
						return next.RoundTrip(request)
					})
				}}
			reader, err := NewDiscoveryReader(config)
			require.NoError(t, err)
			list := &metav1.PartialObjectMetadataList{}
			list.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "SecretList"})
			err = reader.List(t.Context(), list, client.InNamespace("tenant"))
			if reject {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, list.Items, 1)
			}
			object := &metav1.PartialObjectMetadata{}
			object.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
			err = reader.Get(t.Context(), client.ObjectKey{Namespace: "tenant", Name: "fixture"}, object)
			if reject {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "fixture-uid", string(object.UID))
			}
			require.EqualValues(t, 1, listCalls.Load())
			require.EqualValues(t, 1, getCalls.Load())
		})
	}
	_, err := NewDiscoveryReader(nil)
	require.Error(t, err)
}
