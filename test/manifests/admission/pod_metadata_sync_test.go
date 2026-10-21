package admission

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const podSyncAnnotation = "example.com/host-metadata"

func TestPodMetadataSyncMutationBoundaries(t *testing.T) {
	const (
		otherValue = "other"
		trueValue  = "true"
	)
	tests := []struct {
		name    string
		before  func(*corev1.Pod)
		change  func(*corev1.Pod)
		request func(map[string]any)
		callers string
		allowed bool
	}{
		{name: "annotation-add", allowed: true},
		{name: "annotation-change", before: func(p *corev1.Pod) { p.Annotations[podSyncAnnotation] = "old" }, allowed: true},
		{name: "annotation-remove", before: func(p *corev1.Pod) { p.Annotations[podSyncAnnotation] = "old" },
			change: func(p *corev1.Pod) { delete(p.Annotations, podSyncAnnotation) }, allowed: true},
		{name: "disabled", callers: "{}"},
		{name: "wrong-user", request: func(r map[string]any) { r["userInfo"] = map[string]any{"username": "other"} }},
		{name: "username-prefix", request: func(r map[string]any) {
			r["userInfo"] = map[string]any{"username": "syncer-extra"}
		}},
		{name: "wrong-namespace", request: func(r map[string]any) { r["namespace"] = otherValue }},
		{name: "namespace-specific-keys", callers: `{"syncer":{
			"other":["example.com/host-metadata"],"tenant":["example.com/other"]}}`},
		{name: "empty-keys", callers: `{"syncer":{"tenant":[]}}`},
		{name: "wildcard-key-is-literal", callers: `{"syncer":{"tenant":["*"]}}`},
		{name: "wildcard-user-is-literal", callers: `{"*":{"tenant":["example.com/host-metadata"]}}`},
		{name: "wildcard-namespace-is-literal", callers: `{"syncer":{"*":["example.com/host-metadata"]}}`},
		{name: "create", request: func(r map[string]any) { r["operation"] = "CREATE" }},
		{name: "delete", request: func(r map[string]any) { r["operation"] = "DELETE" }},
		{name: "status-subresource", request: func(r map[string]any) { r["subResource"] = "status" }},
		{name: "absent-subresource", request: func(r map[string]any) { delete(r, "subResource") }, allowed: true},
		{name: "different-kind", request: func(r map[string]any) {
			r["kind"] = map[string]any{"group": "", "kind": "Secret"}
		}},
		{name: "image-change", change: func(p *corev1.Pod) { p.Spec.Containers[0].Image = "example.com/replaced:test" }},
		{name: "service-account-change", change: func(p *corev1.Pod) { p.Spec.ServiceAccountName = otherValue }},
		{name: "toleration-add", change: func(p *corev1.Pod) { p.Spec.Tolerations = []corev1.Toleration{{Key: "other"}} }},
		{name: "status-change", change: func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }},
		{name: "label-add", change: func(p *corev1.Pod) { p.Labels["example.com/team"] = otherValue }},
		{name: "routing-label-change", change: func(p *corev1.Pod) { p.Labels["openbao-active"] = trueValue }},
		{name: "managed-label-remove", change: func(p *corev1.Pod) { delete(p.Labels, "app.kubernetes.io/managed-by") }},
		{name: "missing-managed-label", before: func(p *corev1.Pod) { delete(p.Labels, "app.kubernetes.io/managed-by") }},
		{name: "missing-cluster-label", before: func(p *corev1.Pod) { delete(p.Labels, "openbao.org/cluster") }},
		{name: "empty-cluster-label", before: func(p *corev1.Pod) { p.Labels["openbao.org/cluster"] = "" }},
		{name: "different-component", before: func(p *corev1.Pod) { p.Labels["openbao.org/component"] = "unknown" }},
		{name: "server-with-job-owner", before: func(p *corev1.Pod) {
			p.Labels["openbao.org/component"] = "openbaocluster"
			p.OwnerReferences[0].APIVersion, p.OwnerReferences[0].Kind = "batch/v1", "Job"
		}},
		{name: "helper-with-statefulset-owner", before: func(p *corev1.Pod) {
			p.Labels["openbao.org/component"] = "backup"
			p.OwnerReferences[0].APIVersion, p.OwnerReferences[0].Kind = "apps/v1", "StatefulSet"
		}},
		{name: "native-controller-pod", allowed: true},
		{name: "existing-owner-provenance", before: func(p *corev1.Pod) {
			p.Annotations["openbao.org/owner-uid"] = "cluster-uid"
		}, allowed: true},
		{name: "empty-provenance", before: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = "" }},
		{name: "provenance-change", change: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = otherValue }},
		{name: "provenance-remove", before: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = "cluster-uid" },
			change: func(p *corev1.Pod) { delete(p.Annotations, "openbao.org/owner-uid") }},
		{name: "owner-change", change: func(p *corev1.Pod) { p.OwnerReferences[0].UID = otherValue }},
		{name: "owner-remove", change: func(p *corev1.Pod) { p.OwnerReferences = nil }},
		{name: "missing-owner", before: func(p *corev1.Pod) { p.OwnerReferences = nil }},
		{name: "foreign-owner-kind", before: func(p *corev1.Pod) { p.OwnerReferences[0].Kind = "ReplicaSet" }},
		{name: "foreign-owner-api", before: func(p *corev1.Pod) { p.OwnerReferences[0].APIVersion = "example.com/v1" }},
		{name: "empty-owner-name", before: func(p *corev1.Pod) { p.OwnerReferences[0].Name = "" }},
		{name: "noncontroller-owner", before: func(p *corev1.Pod) { p.OwnerReferences[0].Controller = ptr.To(false) }},
		{name: "empty-owner-uid", before: func(p *corev1.Pod) { p.OwnerReferences[0].UID = "" }},
		{name: "finalizer-add", change: func(p *corev1.Pod) { p.Finalizers = append(p.Finalizers, "example.com/other") }},
		{name: "finalizer-remove", change: func(p *corev1.Pod) { p.Finalizers = nil }},
		{name: "unconfigured-annotation-add", change: func(p *corev1.Pod) {
			p.Annotations["example.com/other"] = otherValue
		}},
		{name: "unconfigured-annotation-remove", change: func(p *corev1.Pod) {
			delete(p.Annotations, "example.com/preserve")
		}},
		{name: "maintenance-enable", change: func(p *corev1.Pod) { p.Annotations["openbao.org/maintenance"] = trueValue }},
		{name: "deletion-time-change", change: func(p *corev1.Pod) {
			p.DeletionTimestamp = &metav1.Time{Time: time.Unix(1, 0)}
		}},
		{name: "deletion-grace-change", change: func(p *corev1.Pod) { p.DeletionGracePeriodSeconds = ptr.To(int64(0)) }},
		{name: "generate-name-change", change: func(p *corev1.Pod) { p.GenerateName = "other-" }},
		// Include the default annotation to isolate each reserved-key guard.
		{name: "configured-openbao-key",
			callers: `{"syncer":{"tenant":["example.com/host-metadata","openbao.org/maintenance"]}}`,
			change:  func(p *corev1.Pod) { p.Annotations["openbao.org/maintenance"] = trueValue }},
		{name: "configured-kubernetes-key",
			callers: `{"syncer":{"tenant":["example.com/host-metadata","kubernetes.io/config.mirror"]}}`,
			change:  func(p *corev1.Pod) { p.Annotations["kubernetes.io/config.mirror"] = otherValue }},
		{name: "configured-kubernetes-subdomain", callers: `{"syncer":{"tenant":[
			"example.com/host-metadata","seccomp.security.alpha.kubernetes.io/pod"]}}`,
			change: func(p *corev1.Pod) { p.Annotations["seccomp.security.alpha.kubernetes.io/pod"] = "unconfined" }},
		{name: "configured-k8s-key", callers: `{"syncer":{"tenant":["example.com/host-metadata","k8s.io/test"]}}`,
			change: func(p *corev1.Pod) { p.Annotations["k8s.io/test"] = otherValue }},
		{name: "mixed-permitted-and-reserved-keys",
			callers: `{"syncer":{"tenant":["example.com/host-metadata","kubernetes.io/test"]}}`},
		{name: "configured-vcluster-key",
			callers: `{"syncer":{"tenant":["example.com/host-metadata","vcluster.loft.sh/uid"]}}`,
			change:  func(p *corev1.Pod) { p.Annotations["vcluster.loft.sh/uid"] = otherValue }},
	}
	policy := readClusterPolicySpec(t, "../../../config/policy/openbao-lock-managed-resource-mutations.yaml")
	for _, component := range []string{
		"openbaocluster", "backup", "restore", "upgrade", "upgrade-snapshot", "validation-hook",
	} {
		t.Run(component, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					before := podMetadataSyncFixture()
					before.Labels["openbao.org/component"] = component
					if component != "openbaocluster" {
						before.OwnerReferences[0].APIVersion, before.OwnerReferences[0].Kind = "batch/v1", "Job"
					}
					if test.before != nil {
						test.before(before)
					}
					after := before.DeepCopy()
					after.Annotations[podSyncAnnotation] = "host"
					if test.change != nil {
						test.change(after)
					}
					request := map[string]any{"operation": "UPDATE", "subResource": "", "namespace": "tenant",
						"kind": map[string]any{"group": "", "kind": "Pod"}, "userInfo": map[string]any{"username": "syncer"}}
					if test.request != nil {
						test.request(request)
					}
					callers := test.callers
					if callers == "" {
						callers = `{"syncer":{"tenant":["example.com/host-metadata"]}}`
					}
					env, err := cel.NewEnv(ext.TwoVarComprehensions(), cel.Variable("object", cel.DynType),
						cel.Variable("oldObject", cel.DynType), cel.Variable("request", cel.DynType),
						cel.Variable("variables", cel.DynType))
					require.NoError(t, err)
					variables := map[string]any{}
					activation := map[string]any{"object": podMetadataSyncMap(t, after), "oldObject": podMetadataSyncMap(t, before),
						"request": request, "variables": variables}
					for _, variable := range policy.Variables {
						if !slices.Contains([]string{"pod_metadata_sync_callers", "is_pod_request", "has_old_owner_uid_annotation",
							"pod_metadata_sync_annotation_keys", "pod_new_annotations", "pod_old_annotations",
							"is_pod_metadata_sync_update"}, variable.Name) {
							continue
						}
						expression := variable.Expression
						if variable.Name == "pod_metadata_sync_callers" {
							expression = callers
						}
						ast, issues := env.Compile(expression)
						require.NoError(t, issues.Err(), variable.Name)
						program, err := env.Program(ast)
						require.NoError(t, err)
						value, _, err := program.Eval(activation)
						require.NoError(t, err, variable.Name)
						variables[variable.Name] = value.Value()
					}
					require.Equal(t, test.allowed, variables["is_pod_metadata_sync_update"])
				})
			}
		})
	}
}

func podMetadataSyncFixture() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "example-0", Namespace: "tenant",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "openbao-operator", "openbao.org/cluster": "example",
			"openbao.org/component": "openbaocluster"},
		Annotations: map[string]string{"example.com/preserve": "value"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "example",
			UID: "statefulset-uid", Controller: ptr.To(true)}}, Finalizers: []string{"example.com/hold"}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "openbao", Image: "example.com/openbao:test"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending}}
}

func podMetadataSyncMap(t *testing.T, pod *corev1.Pod) map[string]any {
	t.Helper()
	data, err := json.Marshal(pod)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}
