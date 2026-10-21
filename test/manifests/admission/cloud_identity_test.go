package admission

import (
	"os"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestCloudIdentityPolicyCoversAllPodTemplates(t *testing.T) {
	for _, path := range []string{
		"../../../config/policy/openbao-validate-openbaocluster.yaml",
		"../../../charts/openbao-operator/templates/admission/validate-openbaocluster.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			policy := readClusterPolicySpec(t, path)
			if got := policy.MatchConstraints.ResourceRules[0].Operations; len(got) != 2 ||
				got[0] != admissionv1.Create || got[1] != admissionv1.Update {
				t.Fatalf("cloud identity policy must cover CREATE and UPDATE, got %v", got)
			}

			for _, location := range []string{"main", "read-replicas"} {
				for _, metadata := range []struct {
					name  string
					field string
					key   string
				}{
					{name: "aws-eks", field: "annotations", key: "eks.amazonaws.com/role-arn"},
					{name: "aws-iam", field: "annotations", key: "iam.amazonaws.com/role"},
					{name: "gcp", field: "annotations", key: "iam.gke.io/gcp-service-account"},
					{name: "azure-annotation", field: "annotations", key: "azure.workload.identity/client-id"},
					{name: "azure-label", field: "labels", key: "azure.workload.identity/use"},
					{name: "azure-legacy-label", field: "labels", key: "aadpodidbinding"},
				} {
					t.Run(location+"/"+metadata.name, func(t *testing.T) {
						spec := cloudIdentityPodSpec(location, map[string]any{
							metadata.field: map[string]any{metadata.key: "identity"},
						})
						for _, authorized := range []bool{false, true} {
							got := evaluateCloudIdentityRule(t, policy, spec, authorized, false)
							if got != authorized {
								t.Errorf("authorized=%t: rule allowed=%t", authorized, got)
							}
						}
						if !evaluateCloudIdentityRule(t, policy, spec, false, true) {
							t.Error("controller reconciliation must retain its existing exemption")
						}
					})
				}
			}

			for name, spec := range map[string]map[string]any{
				"absent-read-replicas": {},
				"absent-template":      {"readReplicas": map[string]any{}},
				"absent-metadata":      {"readReplicas": map[string]any{"template": map[string]any{}}},
				"empty-metadata":       cloudIdentityPodSpec("read-replicas", map[string]any{}),
				"unrelated-metadata": cloudIdentityPodSpec("read-replicas", map[string]any{
					"annotations": map[string]any{"example.com/owner": "team"},
					"labels":      map[string]any{"example.com/team": "team"},
				}),
			} {
				t.Run(name, func(t *testing.T) {
					if !evaluateCloudIdentityRule(t, policy, spec, false, false) {
						t.Fatal("metadata without cloud identity must not require delegation")
					}
				})
			}
		})
	}
}

func cloudIdentityPodSpec(location string, metadata map[string]any) map[string]any {
	if location == "main" {
		return map[string]any{"podMetadata": metadata}
	}
	return map[string]any{"readReplicas": map[string]any{
		"template": map[string]any{"metadata": metadata},
	}}
}

func readClusterPolicySpec(t *testing.T, path string) admissionv1.ValidatingAdmissionPolicySpec {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	// The Helm template changes metadata and identity variables. Extract the
	// policy spec so both shipped variants can be checked without a Helm binary.
	_, spec, found := strings.Cut(string(data), "\nspec:\n")
	if !found {
		t.Fatal("policy spec is missing")
	}
	spec, _, _ = strings.Cut(spec, "\n---")
	// Caller-map Helm helpers are unrelated to the CEL rules evaluated here.
	// Substitute an empty map so the unrendered template remains valid YAML.
	for _, helper := range []string{"pvcBindingSyncCallers", "podMetadataSyncCallers"} {
		spec = strings.ReplaceAll(spec, `{{ include "openbao-operator.`+helper+`" . | quote }}`, `'{}'`)
	}
	var policy admissionv1.ValidatingAdmissionPolicy
	if err := yaml.Unmarshal([]byte("spec:\n"+spec), &policy); err != nil {
		t.Fatalf("decode policy spec: %v", err)
	}
	return policy.Spec
}

func evaluateCloudIdentityRule(
	t *testing.T,
	policy admissionv1.ValidatingAdmissionPolicySpec,
	spec map[string]any,
	authorized, controller bool,
) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("variables", cel.DynType))
	if err != nil {
		t.Fatalf("create CEL environment: %v", err)
	}
	variables := map[string]any{
		"has_backup_cloud_identity_metadata": false,
		"cloud_identities_authorized":        authorized,
		"is_operator_controller":             controller,
	}
	activation := map[string]any{"object": map[string]any{"spec": spec}, "variables": variables}
	eval := func(expression string) bool {
		t.Helper()
		ast, issues := env.Compile(expression)
		if issues.Err() != nil {
			t.Fatalf("compile cloud identity expression: %v", issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatalf("build cloud identity expression: %v", err)
		}
		value, _, err := program.Eval(activation)
		if err != nil {
			t.Fatalf("evaluate cloud identity expression: %v", err)
		}
		result, ok := value.Value().(bool)
		if !ok {
			t.Fatalf("cloud identity expression returned %T", value.Value())
		}
		return result
	}
	for _, variable := range policy.Variables {
		switch variable.Name {
		case "has_main_workload_cloud_identity_metadata", "has_read_replica_cloud_identity_metadata",
			"has_cloud_identity_metadata":
			variables[variable.Name] = eval(variable.Expression)
		}
	}
	for _, validation := range policy.Validations {
		if strings.Contains(validation.Message, "use cloud identities on this OpenBaoCluster") {
			return eval(validation.Expression)
		}
	}
	t.Fatal("cloud identity validation is missing")
	return false
}
