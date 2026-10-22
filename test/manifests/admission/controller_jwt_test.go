package admission

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
)

func TestControllerJWTCreationPolicy(t *testing.T) {
	for _, path := range []string{
		"../../../config/policy/openbao-validate-openbaocluster.yaml",
		"../../../charts/openbao-operator/templates/admission/validate-openbaocluster.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			policy := readClusterPolicySpec(t, path)
			env, err := cel.NewEnv(
				cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
				cel.Variable("request", cel.DynType),
			)
			require.NoError(t, err)
			for _, tt := range []struct {
				name, operation, profile, mode, oldMode string
				oidc, allowed                           bool
			}{
				{name: "development-omitted", profile: "Development", allowed: false},
				{name: "development-shared", profile: "Development", mode: "Shared", allowed: true},
				{name: "development-target", profile: "Development", mode: "Target", oidc: true, allowed: true},
				{name: "hardened-omitted", profile: "Hardened", oidc: true, allowed: false},
				{name: "hardened-shared", profile: "Hardened", mode: "Shared", oidc: true, allowed: false},
				{name: "hardened-target", profile: "Hardened", mode: "Target", oidc: true, allowed: true},
				{name: "target-without-oidc", profile: "Hardened", mode: "Target", allowed: false},
				{name: "legacy-development", operation: "UPDATE", profile: "Development", allowed: true},
				{name: "legacy-hardened", operation: "UPDATE", profile: "Hardened", allowed: true},
				{name: "legacy-explicit-shared", operation: "UPDATE", profile: "Hardened", mode: "Shared",
					oldMode: "Shared", allowed: true},
				{name: "migration", operation: "UPDATE", profile: "Hardened", mode: "Target", oidc: true,
					allowed: true},
				{name: "target-removal", operation: "UPDATE", profile: "Hardened", oldMode: "Target",
					oidc: true, allowed: false},
				{name: "target-downgrade", operation: "UPDATE", profile: "Hardened", mode: "Shared",
					oldMode: "Target", oidc: true, allowed: false},
			} {
				t.Run(tt.name, func(t *testing.T) {
					operation := tt.operation
					if operation == "" {
						operation = string(admissionv1.Create)
					}
					spec := map[string]any{"profile": tt.profile, "selfInit": map[string]any{
						"enabled": true, "oidc": map[string]any{"enabled": tt.oidc},
					}}
					if tt.mode != "" {
						spec["controllerJWTMode"] = tt.mode
					}
					var oldObject any
					if operation == "UPDATE" {
						oldSpec := map[string]any{"profile": tt.profile}
						if tt.oldMode != "" {
							oldSpec["controllerJWTMode"] = tt.oldMode
						}
						oldObject = map[string]any{"spec": oldSpec}
					}
					allowed := true
					for _, rule := range policy.Validations {
						if !strings.Contains(rule.Message, "spec.controllerJWTMode") {
							continue
						}
						ast, issues := env.Compile(rule.Expression)
						require.NoError(t, issues.Err())
						program, err := env.Program(ast)
						require.NoError(t, err)
						value, _, err := program.Eval(map[string]any{
							"object": map[string]any{"spec": spec}, "oldObject": oldObject,
							"request": map[string]any{"operation": operation},
						})
						require.NoError(t, err)
						allowed = allowed && value.Value() == true
					}
					require.Equal(t, tt.allowed, allowed)
				})
			}
		})
	}
}
