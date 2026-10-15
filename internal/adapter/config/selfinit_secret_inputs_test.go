package config

import (
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/initmanager"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestRenderSelfInitSecretInputs(t *testing.T) {
	const minimumVersion = "2.6.0"
	cluster := newMinimalCluster("bao", "tenant")
	cluster.Spec.Version = minimumVersion
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{{
		Name: "human-oidc-config", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "auth/human/config",
		Data: &apiextensionsv1.JSON{Raw: []byte(`{"oidc_client_id":"application-id","default_role":"platform-admin"}`)},
		DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{
			"oidc_client_secret": {SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "human-oidc", Key: "client-secret"}},
		},
	}}}
	rendered, err := RenderSelfInitHCL(cluster, nil)
	require.NoError(t, err)
	compareGolden(t, "render_self_init_secret_inputs", rendered)
	inputs, err := initmanager.SecretInputs(cluster.Spec.SelfInit.Requests)
	require.NoError(t, err)
	require.Contains(t, string(rendered), inputs[0].Path())
	require.Contains(t, string(rendered), `eval_source = "file"`)
	require.Contains(t, string(rendered), `eval_type   = "string"`)
	require.NotContains(t, string(rendered), "client-secret")
	require.NotContains(t, string(rendered), "human-oidc\"")
	for _, tt := range []struct {
		name string
		data *apiextensionsv1.JSON
	}{
		{name: "empty data", data: &apiextensionsv1.JSON{Raw: []byte(`{}`)}},
		{name: "omitted data"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cluster.DeepCopy()
			candidate.Spec.SelfInit.Requests[0].Data = tt.data
			rendered, err := RenderSelfInitHCL(candidate, nil)
			require.NoError(t, err)
			require.Contains(t, string(rendered), inputs[0].Path())
			require.Contains(t, string(rendered), `eval_source = "file"`)
		})
	}

	cluster.Spec.Version = "2.5.5"
	_, err = RenderSelfInitHCL(cluster, nil)
	require.ErrorContains(t, err, "dataFrom requires OpenBao >= 2.6.0")
	cluster.Spec.Version = minimumVersion
	cluster.Spec.SelfInit.Requests[0].Data.Raw = []byte(`{"oidc_client_secret":"sentinel-sensitive-value"}`)
	_, err = RenderSelfInitHCL(cluster, nil)
	require.ErrorContains(t, err, "occurs in both data and dataFrom")
	require.NotContains(t, err.Error(), "sentinel-sensitive-value")
}
