package admission

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
)

const (
	restoreStatusField  = "status"
	preservedFieldValue = "preserve"
)

func TestRestoreForegroundFinalizerMutationBoundaries(t *testing.T) {
	for _, path := range []string{
		"../../../config/policy/openbao-validate-openbaorestore.yaml",
		"../../../charts/openbao-operator/templates/admission/validate-openbaorestore.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			policy := readClusterPolicySpec(t, path)
			expression := ""
			for _, variable := range policy.Variables {
				if variable.Name == "is_foreground_finalizer_cleanup" {
					expression = variable.Expression
				}
			}
			require.NotEmpty(t, expression)
			env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
				cel.Variable("request", cel.DynType))
			require.NoError(t, err)
			ast, issues := env.Compile(expression)
			require.NoError(t, issues.Err())
			program, err := env.Program(ast)
			require.NoError(t, err)
			for _, test := range []struct {
				name    string
				change  func(before, after, request map[string]any)
				allowed bool
			}{
				{name: "sole-foreground-removal", allowed: true},
				{name: "absent-subresource", change: func(_, _, request map[string]any) {
					delete(request, "subResource")
				}, allowed: true},
				{name: "empty-finalizers", change: func(_, after, _ map[string]any) {
					restoreMetadata(after)["finalizers"] = []any{}
				}, allowed: true},
				{name: "managed-fields-change", change: func(_, after, _ map[string]any) {
					restoreMetadata(after)["managedFields"] = []any{map[string]any{"manager": "garbage-collector"}}
				}, allowed: true},
				{name: "absent-optional-metadata", change: func(before, after, _ map[string]any) {
					for _, object := range []map[string]any{before, after} {
						delete(restoreMetadata(object), "annotations")
						delete(restoreMetadata(object), "labels")
					}
				}, allowed: true},
				{name: "empty-optional-metadata", change: func(before, after, _ map[string]any) {
					for _, object := range []map[string]any{before, after} {
						restoreMetadata(object)["annotations"] = map[string]any{}
						restoreMetadata(object)["labels"] = map[string]any{}
					}
				}, allowed: true},
				{name: "absent-status", change: func(before, after, _ map[string]any) {
					delete(before, restoreStatusField)
					delete(after, restoreStatusField)
				}, allowed: true},
				{name: "create", change: func(_, _, request map[string]any) {
					request["operation"] = string(admissionv1.Create)
				}},
				{name: "delete", change: func(_, _, request map[string]any) {
					request["operation"] = string(admissionv1.Delete)
				}},
				{name: "status-subresource", change: func(_, _, request map[string]any) {
					request["subResource"] = restoreStatusField
				}},
				{name: "not-deleting", change: func(before, after, _ map[string]any) {
					delete(restoreMetadata(before), "deletionTimestamp")
					delete(restoreMetadata(after), "deletionTimestamp")
				}},
				{name: "operator-finalizer-present", change: func(before, _, _ map[string]any) {
					restoreMetadata(before)["finalizers"] = []any{"openbao.org/openbaorestore-finalizer", "foregroundDeletion"}
				}},
				{name: "other-finalizer-present", change: func(before, _, _ map[string]any) {
					restoreMetadata(before)["finalizers"] = []any{"example.com/protect", "foregroundDeletion"}
				}},
				{name: "no-old-finalizer", change: func(before, _, _ map[string]any) {
					delete(restoreMetadata(before), "finalizers")
				}},
				{name: "unchanged-foreground-finalizer", change: func(_, after, _ map[string]any) {
					restoreMetadata(after)["finalizers"] = []any{"foregroundDeletion"}
				}},
				{name: "replacement-finalizer", change: func(_, after, _ map[string]any) {
					restoreMetadata(after)["finalizers"] = []any{"example.com/protect"}
				}},
				{name: "spec-change", change: func(_, after, _ map[string]any) {
					after["spec"] = map[string]any{"cluster": "other"}
				}},
				{name: "status-change", change: func(_, after, _ map[string]any) {
					after[restoreStatusField] = map[string]any{"phase": "Completed"}
				}},
				{name: "status-removal", change: func(_, after, _ map[string]any) {
					delete(after, restoreStatusField)
				}},
				{name: "top-level-add", change: func(_, after, _ map[string]any) { after["futureField"] = "changed" }},
				{name: "top-level-removal", change: func(before, _, _ map[string]any) {
					before["futureField"] = preservedFieldValue
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					before, after := restoreFinalizerFixture(), restoreFinalizerFixture()
					delete(restoreMetadata(after), "finalizers")
					request := map[string]any{"operation": "UPDATE", "subResource": ""}
					if test.change != nil {
						test.change(before, after, request)
					}
					value, _, err := program.Eval(map[string]any{"object": after, "oldObject": before, "request": request})
					require.NoError(t, err)
					require.Equal(t, test.allowed, value.Value())
				})
			}
			// Check every metadata field in both directions. This also binds
			// server-maintained identity/version fields and future metadata fields.
			for _, key := range []string{"name", "namespace", "uid", "resourceVersion", "generation", "creationTimestamp",
				"deletionTimestamp", "deletionGracePeriodSeconds", "generateName", "annotations", "labels",
				"ownerReferences", "futureField"} {
				for _, mutation := range []string{"add", "change", "remove"} {
					t.Run(key+"-"+mutation, func(t *testing.T) {
						before, after := restoreFinalizerFixture(), restoreFinalizerFixture()
						delete(restoreMetadata(after), "finalizers")
						restoreMetadata(before)[key], restoreMetadata(after)[key] = preservedFieldValue, preservedFieldValue
						switch mutation {
						case "add":
							delete(restoreMetadata(before), key)
						case "change":
							restoreMetadata(after)[key] = "changed"
						case "remove":
							delete(restoreMetadata(after), key)
						}
						value, _, err := program.Eval(map[string]any{"object": after, "oldObject": before,
							"request": map[string]any{"operation": "UPDATE", "subResource": ""}})
						require.NoError(t, err)
						require.Equal(t, false, value.Value())
					})
				}
			}
		})
	}
}

func restoreMetadata(object map[string]any) map[string]any {
	return object["metadata"].(map[string]any)
}

func restoreFinalizerFixture() map[string]any {
	return map[string]any{
		"apiVersion": "openbao.org/v1alpha1", "kind": "OpenBaoRestore",
		"metadata": map[string]any{"name": "cleanup", "namespace": "tenant", "uid": "restore-uid", "resourceVersion": "10",
			"deletionTimestamp": "2026-10-23T12:53:24Z", "finalizers": []any{"foregroundDeletion"},
			"annotations": map[string]any{"openbao.org/restore-acknowledge": "restore-uid/Abandon"},
			"labels":      map[string]any{"example.com/preserve": "original"}},
		"spec": map[string]any{"cluster": "target"}, restoreStatusField: map[string]any{"phase": "Failed"},
	}
}
