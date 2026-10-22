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
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "default"},
		{name: "custom-fullname", args: []string{"--set", "fullnameOverride=security-control"}},
		{name: "single-tenant", args: []string{"--set", "tenancy.mode=single", "--set", "tenancy.targetNamespace=tenant"}},
		{name: "external-pod-security", args: []string{"--set", "tenancy.namespacePodSecurityLabels.mode=external", "--set", "fullnameOverride=external-security"}},
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
				content := strings.Replace(string(data), "namespace: openbao-operator-system", "namespace: custom-operator-system\nnamePrefix: team-", 1)
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
			for _, key := range []string{"POD_NAMESPACE", "OPERATOR_NAMESPACE", "OPERATOR_NAME_PREFIX", "OPERATOR_SERVICE_ACCOUNT_NAME", "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE"} {
				t.Setenv(key, "")
			}
			t.Setenv("POD_NAMESPACE", deployment.Namespace)
			for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
				switch env.Name {
				case "OPERATOR_NAME_PREFIX", "OPERATOR_SERVICE_ACCOUNT_NAME", "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE":
					t.Setenv(env.Name, env.Value)
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
