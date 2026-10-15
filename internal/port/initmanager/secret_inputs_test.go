package initmanager

import (
	"fmt"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestSecretInputs(t *testing.T) {
	tests := []struct {
		name      string
		edit      func(*openbaov1alpha1.SelfInitRequest)
		wantError string
	}{
		{name: "raw string input"},
		{name: "input without literal data", edit: func(r *openbaov1alpha1.SelfInitRequest) { r.Data = nil }},
		{name: "nested names do not overlap", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.Data.Raw = []byte(`{"nested":{"password":"not-the-input"}}`)
		}},
		{name: "duplicate literal", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.Data.Raw = []byte(`{"password":null}`)
		}, wantError: "occurs in both data and dataFrom"},
		{name: "malformed payload", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.Data.Raw = []byte(`{"password":"sentinel-sensitive-parse-error`)
		}, wantError: "data must be a JSON object"},
		{name: "scalar payload", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.Data.Raw = []byte(`"sentinel-sensitive-parse-error"`)
		}, wantError: "data must be a JSON object"},
		{name: "null payload", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.Data.Raw = []byte(`null`)
		}, wantError: "data must be a JSON object"},
		{name: "structured field", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.AuthMethod = &openbaov1alpha1.SelfInitAuthMethod{Type: "userpass"}
		}, wantError: "structured request fields"},
		{name: "structured path", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.Path = "/sys/auth/userpass"
		}, wantError: "raw request paths"},
		{name: "missing key", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.DataFrom["password"] = openbaov1alpha1.SelfInitDataSource{}
		}, wantError: "valid Secret name and key"},
		{name: "invalid field", edit: func(r *openbaov1alpha1.SelfInitRequest) {
			r.DataFrom["../password"] = r.DataFrom["password"]
		}, wantError: "valid Secret name and key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := openbaov1alpha1.SelfInitRequest{
				Name: "admin-user", Path: "auth/userpass/users/admin",
				Data: &apiextensionsv1.JSON{Raw: []byte(`{"token_policies":["admin"]}`)},
				DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{
					"password": {SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap-login", Key: "password"}},
				},
			}
			if tt.edit != nil {
				tt.edit(&request)
			}
			inputs, err := SecretInputs([]openbaov1alpha1.SelfInitRequest{request})
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				require.NotContains(t, err.Error(), "sentinel-sensitive-parse-error")
				return
			}
			require.NoError(t, err)
			require.Len(t, inputs, 1)
			require.Equal(t, "bootstrap-login", inputs[0].SecretName)
			require.Equal(t, "password", inputs[0].SecretKey)
			require.Len(t, inputs[0].FileName, 64)
			require.Equal(t, constants.PathSelfInitInputs+"/"+inputs[0].FileName, inputs[0].Path())
			again, err := SecretInputs([]openbaov1alpha1.SelfInitRequest{request})
			require.NoError(t, err)
			require.Equal(t, inputs, again)
		})
	}
}

func TestSecretInputsStableOrder(t *testing.T) {
	source := openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap", Key: "credential"},
	}
	inputs, err := SecretInputs([]openbaov1alpha1.SelfInitRequest{
		{Name: "first", Path: "auth/example/config", DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{"z": source, "a": source}},
		{Name: "second", Path: "auth/example/config", DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{"a": source}},
	})
	require.NoError(t, err)
	require.Len(t, inputs, 3)
	require.Equal(t, "a", inputs[0].FieldName)
	require.Equal(t, "z", inputs[1].FieldName)
	require.NotEqual(t, inputs[0].Path(), inputs[2].Path())
	_, err = SecretInputs([]openbaov1alpha1.SelfInitRequest{
		{Name: "duplicate", Path: "auth/example/config", DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{"a": source}},
		{Name: "duplicate", Path: "auth/example/config", DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{"b": source}},
	})
	require.ErrorContains(t, err, "unique source-bearing request names")
}

func TestSecretInputSourceLimit(t *testing.T) {
	requests := make([]openbaov1alpha1.SelfInitRequest, 3)
	for i := range requests {
		requests[i] = openbaov1alpha1.SelfInitRequest{
			Name: fmt.Sprintf("request-%d", i), Path: "auth/example/config",
			DataFrom: make(map[string]openbaov1alpha1.SelfInitDataSource),
		}
		for field := range 64 {
			requests[i].DataFrom[fmt.Sprintf("field_%d", field)] = openbaov1alpha1.SelfInitDataSource{
				SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{
					Name: fmt.Sprintf("bootstrap-%d", i%2), Key: fmt.Sprintf("key-%d", field),
				},
			}
		}
	}
	inputs, err := SecretInputs(requests)
	require.NoError(t, err)
	require.Len(t, inputs, 192)
	requests[2].DataFrom["field_0"] = openbaov1alpha1.SelfInitDataSource{
		SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "third-source", Key: "credential"},
	}
	_, err = SecretInputs(requests)
	require.ErrorContains(t, err, "at most two distinct Secrets")
}
