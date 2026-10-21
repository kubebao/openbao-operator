package admission

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
)

func TestRecoverySecretGarbageCollectionGuard(t *testing.T) {
	for _, path := range []string{"../../../config/policy/openbao-lock-managed-resource-mutations.yaml",
		"../../../charts/openbao-operator/templates/admission/lock-managed-resources.yaml"} {
		policy := readClusterPolicySpec(t, path)
		for _, scenario := range []struct {
			name, secret, actor string
			terminating, allow  bool
		}{
			{name: "unseal", secret: "example-unseal-key"},
			{name: "root-token", secret: "example-root-token"},
			{name: "name-prefix", secret: "example-unseal-key-extra", allow: true},
			{name: "other-cluster", secret: "other-unseal-key", allow: true},
			{name: "ordinary-tls", secret: "example-tls-server", allow: true},
			{name: "controller", secret: "example-unseal-key", actor: "controller", allow: true},
			{name: "provisioner", secret: "example-unseal-key", actor: "provisioner", allow: true},
			{name: "authorized-maintenance", secret: "example-unseal-key", actor: "maintenance", allow: true},
			{name: "namespace-teardown", secret: "example-unseal-key", terminating: true, allow: true},
		} {
			t.Run(path+"/"+scenario.name, func(t *testing.T) {
				env, err := cel.NewEnv(ext.TwoVarComprehensions(), cel.Variable("object", cel.DynType),
					cel.Variable("oldObject", cel.DynType), cel.Variable("request", cel.DynType),
					cel.Variable("variables", cel.DynType), cel.Variable("namespaceObject", cel.DynType))
				require.NoError(t, err)
				variables := map[string]any{
					"is_operator_controller":  scenario.actor == "controller",
					"is_operator_provisioner": scenario.actor == "provisioner",
					"maintenance_authorized":  scenario.actor == "maintenance"}
				namespaceMetadata := map[string]any{}
				if scenario.terminating {
					namespaceMetadata["deletionTimestamp"] = "2026-10-21T00:00:00Z"
				}
				// No owner-UID annotation: legacy recovery Secrets need the same protection.
				activation := map[string]any{"object": nil, "oldObject": map[string]any{"metadata": map[string]any{
					"name": scenario.secret, "labels": map[string]any{
						"app.kubernetes.io/managed-by": "openbao-operator", "openbao.org/cluster": "example"}}},
					"request":   map[string]any{"operation": "DELETE", "kind": map[string]any{"group": "", "kind": "Secret"}},
					"variables": variables, "namespaceObject": map[string]any{"metadata": namespaceMetadata}}
				evaluate := func(expression string) any {
					ast, issues := env.Compile(expression)
					require.NoError(t, issues.Err())
					program, err := env.Program(ast)
					require.NoError(t, err)
					value, _, err := program.Eval(activation)
					require.NoError(t, err)
					return value.Value()
				}
				for _, variable := range policy.Variables {
					if slices.Contains([]string{"is_openbao_managed_by_operator", "has_openbao_specific_label",
						"is_managed", "is_recovery_secret_delete"}, variable.Name) {
						variables[variable.Name] = evaluate(variable.Expression)
					}
				}
				checked := false
				for _, validation := range policy.Validations {
					if strings.HasPrefix(validation.Message, "Generated recovery Secrets") {
						require.Equal(t, scenario.allow, evaluate(validation.Expression))
						checked = true
					}
				}
				require.True(t, checked, "missing recovery Secret GC guard")
			})
		}
	}
}
