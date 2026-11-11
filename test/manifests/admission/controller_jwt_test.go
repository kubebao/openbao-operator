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
				cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType),
			)
			require.NoError(t, err)
			for _, tt := range []struct {
				name, operation, profile, mode, oldMode string
				oidc, allowed, migrationImport          bool
			}{
				{name: "migration-import-omitted", profile: "Development", migrationImport: true, allowed: true},
				{name: "migration-import-shared", profile: "Hardened", mode: "Shared", oidc: true,
					migrationImport: true, allowed: true},
				{name: "migration-import-target", profile: "Hardened", mode: "Target", oidc: true,
					migrationImport: true, allowed: true},
				{name: "development-omitted", profile: "Development", allowed: false},
				{name: "development-shared", profile: "Development", mode: "Shared", allowed: false},
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
							"request":   map[string]any{"operation": operation},
							"variables": map[string]any{"is_authorized_migration_import": tt.migrationImport},
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

func TestMigrationSourceMarkerIsReserved(t *testing.T) {
	const marker = "migration.kubebao.org/source-uid"
	for _, path := range []string{
		"../../../config/policy/openbao-validate-openbaocluster.yaml",
		"../../../charts/openbao-operator/templates/admission/validate-openbaocluster.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			policy := readClusterPolicySpec(t, path)
			env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
				cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
			require.NoError(t, err)
			eval := func(expression string, activation map[string]any) any {
				ast, issues := env.Compile(expression)
				require.NoError(t, issues.Err())
				program, err := env.Program(ast)
				require.NoError(t, err)
				value, _, err := program.Eval(activation)
				require.NoError(t, err)
				return value.Value()
			}
			meta := func(uid string) map[string]any {
				if uid == "" {
					return map[string]any{"metadata": map[string]any{}}
				}
				return map[string]any{"metadata": map[string]any{"annotations": map[string]any{marker: uid}}}
			}
			for _, tt := range []struct {
				name, operation, uid, oldUID string
				authorized, allowed          bool
			}{
				{name: "authorized import", operation: "CREATE", uid: "source", authorized: true, allowed: true},
				{name: "unauthorized import", operation: "CREATE", uid: "source"},
				{name: "ordinary create", operation: "CREATE", allowed: true},
				{name: "unchanged marker", operation: "UPDATE", uid: "source", oldUID: "source", allowed: true},
				{name: "added marker", operation: "UPDATE", uid: "source"},
				{name: "changed marker", operation: "UPDATE", uid: "other", oldUID: "source", authorized: true},
				// A Target controller's audience keeps the marked identity.
				{name: "removed marker", operation: "UPDATE", oldUID: "source", authorized: true},
				{name: "ordinary update", operation: "UPDATE", allowed: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					var oldObject any
					if tt.operation == "UPDATE" {
						oldObject = meta(tt.oldUID)
					}
					activation := map[string]any{"object": meta(tt.uid), "oldObject": oldObject,
						"request": map[string]any{"operation": tt.operation}}
					variables := map[string]any{"migration_authorized": tt.authorized}
					activation["variables"] = variables
					for _, variable := range policy.Variables {
						switch variable.Name {
						case "migration_source_uid", "previous_migration_source_uid", "is_authorized_migration_import":
							variables[variable.Name] = eval(variable.Expression, activation)
						}
					}
					allowed := true
					for _, rule := range policy.Validations {
						if strings.Contains(rule.Message, marker) {
							allowed = allowed && eval(rule.Expression, activation) == true
						}
					}
					require.Equal(t, tt.allowed, allowed)
				})
			}
		})
	}
}
