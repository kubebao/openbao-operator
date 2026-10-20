package admission

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPVCBindingSyncCoversPVCSpecFields(t *testing.T) {
	policy := readClusterPolicySpec(t, "../../../config/policy/openbao-lock-managed-resource-mutations.yaml")
	var expression string
	for _, variable := range policy.Variables {
		if variable.Name == "pvc_binding_sync_preserves_spec" {
			expression = variable.Expression
		}
	}
	fields := map[string]bool{}
	for _, match := range regexp.MustCompile(`object\.spec\.([A-Za-z]+)`).FindAllStringSubmatch(expression, -1) {
		fields[match[1]] = true
	}
	specType := reflect.TypeFor[corev1.PersistentVolumeClaimSpec]()
	for i := 0; i < specType.NumField(); i++ {
		field := specType.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !fields[name] {
			t.Errorf("PVC spec field %q is not covered by the sync predicate; review upstream field additions", name)
		}
	}
	if len(fields) != specType.NumField() {
		t.Fatalf("sync predicate compares %d fields, PVC spec has %d", len(fields), specType.NumField())
	}
}

func TestPVCBindingSyncMutationBoundaries(t *testing.T) {
	const (
		boundVolume     = "pv-host"
		bindingComplete = "yes"
		otherValue      = "other"
		trueValue       = "true"
	)
	type testCase struct {
		name    string
		before  func(*corev1.PersistentVolumeClaim)
		change  func(*corev1.PersistentVolumeClaim)
		request func(map[string]any)
		callers string
		allowed bool
	}
	tests := slices.Grow([]testCase{
		{
			name: "initial-binding",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Spec.VolumeName = boundVolume
			},
			allowed: true,
		},
		{name: "binding-annotations", change: func(p *corev1.PersistentVolumeClaim) {
			p.Annotations["pv.kubernetes.io/bind-completed"] = bindingComplete
			p.Annotations["pv.kubernetes.io/bound-by-controller"] = bindingComplete
			p.Annotations["volume.beta.kubernetes.io/storage-provisioner"] = "example.csi.test"
		}, allowed: true},
		{
			name: "existing-binding",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Spec.VolumeName = boundVolume
			},
			allowed: true,
		},
		{name: "disabled", callers: "{}"},
		{name: "wrong-user", request: func(r map[string]any) { r["userInfo"] = map[string]any{"username": otherValue} }},
		{
			name:    "username-prefix",
			request: func(r map[string]any) { r["userInfo"] = map[string]any{"username": "syncer-extra"} },
		},
		{name: "wrong-namespace", request: func(r map[string]any) { r["namespace"] = otherValue }},
		{name: "empty-namespace-list", callers: `{"syncer":[]}`},
		{name: "wildcard-namespace-is-literal", callers: `{"syncer":["*"]}`},
		{name: "wildcard-user-is-literal", callers: `{"*":["tenant"]}`},
		{name: "create", request: func(r map[string]any) { r["operation"] = "CREATE" }},
		{name: "delete", request: func(r map[string]any) { r["operation"] = "DELETE" }},
		{name: "status-subresource", request: func(r map[string]any) { r["subResource"] = "status" }},
		{name: "absent-subresource", request: func(r map[string]any) { delete(r, "subResource") }, allowed: true},
		{name: "pod", request: func(r map[string]any) { r["kind"] = map[string]any{"group": "", "kind": "Pod"} }},
		{
			name: "missing-provenance",
			before: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, "openbao.org/owner-uid")
			},
		},
		{
			name: "empty-provenance",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["openbao.org/owner-uid"] = ""
			},
		},
		{
			name: "claim-provenance",
			before: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, "openbao.org/owner-uid")
			},
			change: func(p *corev1.PersistentVolumeClaim) { p.Annotations["openbao.org/owner-uid"] = "owner" }},
		{
			name: "replace-provenance",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["openbao.org/owner-uid"] = otherValue
			},
		},
		{
			name: "remove-provenance",
			change: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, "openbao.org/owner-uid")
			},
		},
		{name: "labels", change: func(p *corev1.PersistentVolumeClaim) { p.Labels["example.com/team"] = otherValue }},
		{
			name: "remove-managed-label",
			change: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Labels, "app.kubernetes.io/managed-by")
			},
		},
		{
			name: "missing-cluster-label",
			before: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Labels, "openbao.org/cluster")
			},
		},
		{
			name: "unmanaged",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Labels["app.kubernetes.io/managed-by"] = otherValue
			},
		},
		{name: "owner-reference", change: func(p *corev1.PersistentVolumeClaim) {
			p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: otherValue, UID: otherValue}}
		}},
		{
			name: "add-finalizer",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Finalizers = append(p.Finalizers, "example.com/keep")
			},
		},
		{name: "remove-finalizer", change: func(p *corev1.PersistentVolumeClaim) { p.Finalizers = nil }},
		{
			name: "unrelated-annotation",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["example.com/edit"] = trueValue
			},
		},
		{
			name: "protected-annotation",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["openbao.org/maintenance"] = trueValue
			},
		},
		{
			name: "invalid-binding-marker",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["pv.kubernetes.io/bind-completed"] = "no"
			},
		},
		{name: "empty-provisioner", change: func(p *corev1.PersistentVolumeClaim) {
			p.Annotations["volume.beta.kubernetes.io/storage-provisioner"] = ""
		}},
		{
			name: "replace-binding-marker",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["pv.kubernetes.io/bind-completed"] = bindingComplete
			},
			change: func(p *corev1.PersistentVolumeClaim) { p.Annotations["pv.kubernetes.io/bind-completed"] = "no" }},
		{
			name: "remove-binding-marker",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["pv.kubernetes.io/bind-completed"] = bindingComplete
			},
			change: func(p *corev1.PersistentVolumeClaim) { delete(p.Annotations, "pv.kubernetes.io/bind-completed") }},
		{name: "replace-volume", before: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-original" },
			change: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-other" }},
		{name: "clear-volume", before: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-original" },
			change: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "" }},
		{name: "access-modes", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
		}},
		{name: "resources", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Gi")
		}},
		{
			name: "storage-class",
			change: func(p *corev1.PersistentVolumeClaim) {
				value := otherValue
				p.Spec.StorageClassName = &value
			},
		},
		{name: "volume-mode", change: func(p *corev1.PersistentVolumeClaim) {
			value := corev1.PersistentVolumeBlock
			p.Spec.VolumeMode = &value
		}},
		{name: "selector", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"disk": otherValue}}
		}},
		{name: "data-source", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.DataSource = &corev1.TypedLocalObjectReference{Kind: "PersistentVolumeClaim", Name: otherValue}
		}},
		{name: "data-source-ref", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.DataSourceRef = &corev1.TypedObjectReference{Kind: "PersistentVolumeClaim", Name: otherValue}
		}},
		{
			name: "volume-attributes-class",
			change: func(p *corev1.PersistentVolumeClaim) {
				value := otherValue
				p.Spec.VolumeAttributesClassName = &value
			},
		},
		{name: "status", change: func(p *corev1.PersistentVolumeClaim) { p.Status.Phase = corev1.ClaimBound }},
		{name: "mixed-binding-and-label", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.VolumeName = boundVolume
			p.Labels["example.com/team"] = otherValue
		}},
	}, 16)
	for _, annotation := range []struct{ name, key string }{
		{"ga-provisioner", "volume.kubernetes.io/storage-provisioner"},
		{"selected-node", "volume.kubernetes.io/selected-node"},
	} {
		setOriginal := func(p *corev1.PersistentVolumeClaim) { p.Annotations[annotation.key] = "original" }
		tests = append(tests,
			testCase{name: annotation.name + "-addition", change: setOriginal, allowed: true},
			testCase{name: annotation.name + "-preserved", before: setOriginal, allowed: true},
			testCase{name: annotation.name + "-empty", change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations[annotation.key] = ""
			}},
			testCase{name: annotation.name + "-replacement", before: setOriginal, change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations[annotation.key] = otherValue
			}},
			testCase{name: annotation.name + "-removal", before: setOriginal, change: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, annotation.key)
			}},
			testCase{name: annotation.name + "-unrelated-annotation", change: func(p *corev1.PersistentVolumeClaim) {
				setOriginal(p)
				p.Annotations["example.com/edit"] = trueValue
			}},
			testCase{name: annotation.name + "-wrong-user", change: setOriginal, request: func(r map[string]any) {
				r["userInfo"] = map[string]any{"username": otherValue}
			}},
			testCase{name: annotation.name + "-wrong-namespace", change: setOriginal, request: func(r map[string]any) {
				r["namespace"] = otherValue
			}},
		)
	}
	policy := readClusterPolicySpec(t, "../../../config/policy/openbao-lock-managed-resource-mutations.yaml")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := pvcBindingSyncFixture()
			if test.before != nil {
				test.before(before)
			}
			after := before.DeepCopy()
			if test.change != nil {
				test.change(after)
			}
			request := map[string]any{
				"operation": "UPDATE", "subResource": "", "namespace": "tenant",
				"kind":     map[string]any{"group": "", "kind": "PersistentVolumeClaim"},
				"userInfo": map[string]any{"username": "syncer"},
			}
			if test.request != nil {
				test.request(request)
			}
			callers := test.callers
			if callers == "" {
				callers = `{"syncer":["tenant"]}`
			}
			env, err := cel.NewEnv(ext.TwoVarComprehensions(),
				cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
				cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
			if err != nil {
				t.Fatal(err)
			}
			variables := map[string]any{}
			activation := map[string]any{"object": pvcBindingSyncMap(t, after), "oldObject": pvcBindingSyncMap(t, before),
				"request": request, "variables": variables}
			for _, variable := range policy.Variables {
				if !slices.Contains([]string{"pvc_binding_sync_callers", "is_pvc_request", "has_current_owner_uid_annotation",
					"has_old_owner_uid_annotation", "pvc_new_annotations", "pvc_old_annotations", "is_pvc_binding_sync_caller",
					"pvc_binding_sync_preserves_spec", "is_pvc_binding_sync_update"}, variable.Name) {
					continue
				}
				expression := variable.Expression
				if variable.Name == "pvc_binding_sync_callers" {
					expression = callers
				}
				ast, issues := env.Compile(expression)
				if issues.Err() != nil {
					t.Fatalf("compile %s: %v", variable.Name, issues.Err())
				}
				program, err := env.Program(ast)
				if err != nil {
					t.Fatal(err)
				}
				value, _, err := program.Eval(activation)
				if err != nil {
					t.Fatalf("evaluate %s: %v", variable.Name, err)
				}
				variables[variable.Name] = value.Value()
			}
			if got := variables["is_pvc_binding_sync_update"]; got != test.allowed {
				t.Fatalf("sync allowed=%v, want %t", got, test.allowed)
			}
		})
	}
}

func pvcBindingSyncFixture() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-example-0", Namespace: "tenant",
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "openbao-operator", "openbao.org/cluster": "example"},
			Annotations: map[string]string{"openbao.org/owner-uid": "owner"},
			Finalizers:  []string{"kubernetes.io/pvc-protection"}},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
}

func pvcBindingSyncMap(t *testing.T, obj *corev1.PersistentVolumeClaim) map[string]any {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
