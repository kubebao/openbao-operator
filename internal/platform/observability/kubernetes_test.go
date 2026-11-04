package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestKubernetesRequestLabels(t *testing.T) {
	for _, tt := range []struct {
		name, method, path, contentType, verb, resource, subresource string
	}{
		{"read", "GET", "/api/v1/namespaces/private/configmaps/secret-name", "", "get", "configmaps", "none"},
		{"apply", "PATCH", "/api/v1/namespaces/private/configmaps/name", "application/apply-patch+yaml", "apply", "configmaps", "none"},
		{"apply CBOR", "PATCH", "/api/v1/namespaces/private/configmaps/name", "application/apply-patch+cbor", "apply", "configmaps", "none"},
		{"repair", "PATCH", "/api/v1/namespaces/private/configmaps/name", "application/merge-patch+json", "patch", "configmaps", "none"},
		{"status", "PATCH", "/apis/kubebao.org/v1alpha1/namespaces/private/openbaoclusters/name/status", "", "patch", "openbaoclusters", "status"},
		{"list", "GET", "/apis/apps/v1/namespaces/private/statefulsets", "", "list", "statefulsets", "none"},
		{"watch", "GET", "/api/v1/secrets?watch=true", "", "watch", "secrets", "none"},
		{"endpoint slices", "GET", "/apis/discovery.k8s.io/v1/namespaces/private/endpointslices", "", "list", "endpointslices", "none"},
		{"backend TLS policy", "GET", "/apis/gateway.networking.k8s.io/v1/namespaces/private/backendtlspolicies/name", "", "get", "backendtlspolicies", "none"},
		{"cluster list", "GET", "/api/v1/namespaces", "", "list", "namespaces", "none"},
		{"namespace finalize", "PUT", "/api/v1/namespaces/private/finalize", "", "update", "namespaces", "finalize"},
		{"token", "POST", "/api/v1/namespaces/private/serviceaccounts/name/token", "", "create", "serviceaccounts", "token"},
		{"delete", "DELETE", "/api/v1/namespaces/private/pods/name", "", "delete", "pods", "none"},
		{"unknown resource", "GET", "/apis/private.example/v1/namespaces/private/user-resource/name/user-subresource", "", "get", "other", "other"},
		{"discovery", "GET", "/apis/private.example/v1", "", "other", "other", "none"},
		{"nonresource", "GET", "/healthz", "", "other", "other", "none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Content-Type", tt.contentType)
			verb, resource, subresource := kubernetesRequestLabels(req)
			require.Equal(t, []string{tt.verb, tt.resource, tt.subresource}, []string{verb, resource, subresource})
		})
	}
}

func TestKubernetesTransportRecordsResult(t *testing.T) {
	failure := errors.New("transport failure")
	for _, tt := range []struct {
		name   string
		status int
		err    error
		result string
	}{
		{"success", 200, nil, "success"}, {"created", 201, nil, "success"},
		{"conflict", 409, nil, "conflict"}, {"missing", 404, nil, "not_found"},
		{"forbidden", 403, nil, "forbidden"}, {"unavailable", 503, nil, "error"},
		{"transport", 0, failure, "error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metric := kubeClientRequestsTotal.WithLabelValues("get", "secrets", "none", tt.result)
			before := testutil.ToFloat64(metric)
			req := httptest.NewRequest("GET", "/api/v1/namespaces/private/secrets/private", nil)
			var response *http.Response
			if tt.status != 0 {
				response = &http.Response{StatusCode: tt.status, Body: http.NoBody}
			}
			rt := WrapKubernetesTransport(kubeRoundTripFunc(func(got *http.Request) (*http.Response, error) {
				require.Same(t, req, got)
				return response, tt.err
			}))
			got, err := rt.RoundTrip(req)
			if got != nil {
				defer func() { require.NoError(t, got.Body.Close()) }()
			}
			require.Same(t, response, got)
			require.ErrorIs(t, err, tt.err)
			require.Equal(t, before+1, testutil.ToFloat64(metric))
		})
	}
}

type kubeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kubeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
