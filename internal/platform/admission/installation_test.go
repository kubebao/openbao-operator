package admission

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigsyaml "sigs.k8s.io/yaml"
)

func installerTool(t *testing.T, name string) string {
	t.Helper()
	if tool, err := exec.LookPath(name); err == nil {
		return tool
	}
	tool := filepath.Join("..", "..", "..", "bin", name)
	if _, err := os.Stat(tool); err == nil {
		return tool
	}
	t.Skipf("%s binary not available", name)
	return ""
}

func TestHelmInstallationsMatchReleaseContracts(t *testing.T) {
	pvcCallers := `admissionPolicies.pvcBindingSync.allowedCallers=[{"username":"syncer","namespaces":["tenant-a","tenant-b"]}]`
	podCallers := `admissionPolicies.podMetadataSync.allowedCallers=[{"username":"syncer","namespaces":["tenant-a"],"annotationKeys":["example.com/host-id"]},{"username":"syncer","namespaces":["tenant-b"],"annotationKeys":["example.com/zone"]}]`
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "default"},
		{name: "custom-fullname", args: []string{"--set", "fullnameOverride=security-control"}},
		{name: "single-tenant", args: []string{"--set", "tenancy.mode=single", "--set", "tenancy.targetNamespace=tenant"}},
		{name: "external-pod-security", args: []string{"--set", "tenancy.namespacePodSecurityLabels.mode=external", "--set", "fullnameOverride=external-security"}},
		{name: "pvc-sync", args: []string{"--set-json", pvcCallers}},
		{name: "pod-sync", args: []string{"--set-json", podCallers}},
		{name: "both-sync", args: []string{"--set-json", pvcCallers, "--set-json", podCallers}},
		{name: "literal-usernames", args: []string{"--set-json",
			`admissionPolicies.pvcBindingSync.allowedCallers=[{"username":"syncer$(POD_NAMESPACE)","namespaces":["tenant-a"]}]`,
		}},
		{name: "sync-custom-installation", args: []string{
			"--set-json", pvcCallers, "--set-json", podCallers,
			"--set", "fullnameOverride=sync-security", "--set", "tenancy.namespacePodSecurityLabels.mode=external",
			"--set", "tenancy.mode=single", "--set", "tenancy.targetNamespace=tenant-a",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"template", "sample", filepath.Join("..", "..", "..", "charts", "openbao-operator"), "--namespace", "custom-operator-system"}, tt.args...)
			rendered, err := exec.Command(installerTool(t, "helm"), args...).CombinedOutput()
			require.NoError(t, err, "%s", rendered)
			checkRenderedInstallation(t, rendered)
		})
	}
}

func TestKustomizeInstallationsMatchReleaseContracts(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	for _, variant := range []string{
		"default", "custom-identity", "custom-identity-customized",
		"single-tenant", "single-tenant-custom-identity", "single-tenant-custom-identity-customized",
		"pvc-binding-sync", "pvc-binding-sync-customized", "pod-metadata-sync", "pod-metadata-sync-customized",
	} {
		t.Run(variant, func(t *testing.T) {
			build := filepath.Join(root, "config", "default")
			if variant != "default" {
				build = filepath.Join(root, "config", "overlays", strings.TrimSuffix(variant, "-customized"))
			}
			if strings.HasSuffix(variant, "-customized") {
				directory := t.TempDir()
				require.NoError(t, os.CopyFS(filepath.Join(directory, "config"), os.DirFS(filepath.Join(root, "config"))))
				build = filepath.Join(directory, "config", "overlays", strings.TrimSuffix(variant, "-customized"))
				path := filepath.Join(build, "kustomization.yaml")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				content := string(data)
				switch {
				case strings.HasPrefix(variant, "pvc-binding-sync"):
					content = strings.Replace(content, `{"kubernetes-super-admin":["openbao-demo"]}`,
						`{ "syncer$(POD_NAMESPACE)": ["tenant-b"], "syncer": ["tenant-a", "tenant-b"] }`, 1)
				case strings.HasPrefix(variant, "pod-metadata-sync"):
					content = strings.Replace(content, `{"kubernetes-super-admin":{"openbao-demo":["example.com/observed-host-annotation"]}}`,
						`{ "syncer": { "tenant-b": ["example.com/zone"], "tenant-a": ["example.com/host-id"] } }`, 1)
				default:
					content = strings.Replace(content, "namespace: openbao-operator-system", "namespace: custom-operator-system\nnamePrefix: team-", 1)
				}
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			rendered, err := exec.Command(installerTool(t, "kustomize"), "build", "--load-restrictor", "LoadRestrictionsNone", build).Output()
			require.NoError(t, err)
			checkRenderedInstallation(t, rendered)
		})
	}
}

func checkRenderedInstallation(t *testing.T, rendered []byte) {
	t.Helper()
	var objects []client.Object
	var deployments []appsv1.Deployment
	configMaps := map[string]map[string]string{}
	decoder := yaml.NewYAMLToJSONDecoder(bytes.NewReader(rendered))
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		data, err := sigsyaml.Marshal(document)
		require.NoError(t, err)
		switch document["kind"] {
		case "ConfigMap":
			var object corev1.ConfigMap
			require.NoError(t, sigsyaml.UnmarshalStrict(data, &object))
			configMaps[object.Name] = object.Data
		case "ValidatingAdmissionPolicy":
			object := &admissionv1.ValidatingAdmissionPolicy{}
			require.NoError(t, sigsyaml.UnmarshalStrict(data, object))
			require.Empty(t, object.Namespace, "admission objects must remain cluster-scoped")
			objects = append(objects, object)
		case "ValidatingAdmissionPolicyBinding":
			object := &admissionv1.ValidatingAdmissionPolicyBinding{}
			require.NoError(t, sigsyaml.UnmarshalStrict(data, object))
			require.Empty(t, object.Namespace, "admission objects must remain cluster-scoped")
			objects = append(objects, object)
		case "Deployment":
			var object appsv1.Deployment
			require.NoError(t, sigsyaml.UnmarshalStrict(data, &object))
			deployments = append(deployments, object)
		}
	}
	require.Len(t, objects, 2*len(DefaultDependencies()))
	require.NotEmpty(t, deployments)
	for _, deployment := range deployments {
		t.Run(deployment.Name, func(t *testing.T) {
			for _, key := range []string{
				"POD_NAMESPACE", "OPERATOR_NAMESPACE", "OPERATOR_NAME_PREFIX", "OPERATOR_SERVICE_ACCOUNT_NAME",
				"OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE",
				"OPENBAO_ADMISSION_PVC_BINDING_SYNC_CALLERS", "OPENBAO_ADMISSION_POD_METADATA_SYNC_CALLERS",
			} {
				t.Setenv(key, "")
			}
			t.Setenv("POD_NAMESPACE", deployment.Namespace)
			for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
				switch env.Name {
				case "OPERATOR_NAME_PREFIX", "OPERATOR_SERVICE_ACCOUNT_NAME", "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE",
					"OPENBAO_ADMISSION_PVC_BINDING_SYNC_CALLERS", "OPENBAO_ADMISSION_POD_METADATA_SYNC_CALLERS":
					value := env.Value
					if env.ValueFrom != nil {
						ref := env.ValueFrom.ConfigMapKeyRef
						require.NotNil(t, ref)
						var found bool
						value, found = configMaps[ref.Name][ref.Key]
						require.True(t, found, "trusted setting must reference a rendered ConfigMap key")
					} else if env.Name == pvcBindingSyncCallersEnv || env.Name == podMetadataSyncCallersEnv {
						require.NotContains(t, value, "$", "inline JSON must escape environment expansion")
					}
					t.Setenv(env.Name, value)
				}
			}
			require.NotEmpty(t, os.Getenv("OPERATOR_SERVICE_ACCOUNT_NAME"))
			require.NotEmpty(t, os.Getenv("OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME"))
			status, err := CheckDependencies(context.Background(), newAdmissionClient(t, objects...), DefaultDependencies(), DefaultNamePrefixes())
			require.NoError(t, err)
			require.True(t, status.OverallReady, status.SummaryMessage())
		})
	}
}
