package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	syaml "sigs.k8s.io/yaml"
)

func TestHandoffRetriesConflictsWithFreshGuards(t *testing.T) {
	for _, open := range []bool{true, false} {
		for _, mode := range []string{"status", "operator-resumed", "policy-replaced", "foreign-principal", "persistent", "forbidden"} {
			if !open && mode == "operator-resumed" {
				continue
			}
			t.Run(fmt.Sprintf("open=%t/%s", open, mode), func(t *testing.T) {
				scheme := runtime.NewScheme()
				require.NoError(t, admissionv1.AddToScheme(scheme))
				require.NoError(t, appsv1.AddToScheme(scheme))
				require.NoError(t, corev1.AddToScheme(scheme))
				policy := releasedLockPolicy(t)
				if !open {
					policy = openedLockPolicy(t)
				}
				policy.UID = "original-policy"
				deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "openbao-operator-system", UID: "controller"},
					Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(0)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "controller"}}}}
				base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&policy).
					WithObjects(&policy, deployment).Build()
				attempts := 0
				wrapped := interceptor.NewClient(base, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch,
					object client.Object, patch client.Patch, options ...client.PatchOption) error {
					attempts++
					if mode == "forbidden" {
						return apierrors.NewForbidden(schema.GroupResource{Resource: "validatingadmissionpolicies"}, policy.Name, fmt.Errorf("denied"))
					}
					if attempts == 1 || mode == "persistent" {
						current := &admissionv1.ValidatingAdmissionPolicy{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(&policy), current))
						switch mode {
						case "operator-resumed":
							deployment.Spec.Replicas = ptr.To(int32(1))
							require.NoError(t, c.Update(ctx, deployment))
						case "policy-replaced":
							require.NoError(t, c.Delete(ctx, current))
							current.UID, current.ResourceVersion = "replacement-policy", ""
							require.NoError(t, c.Create(ctx, current))
						case "foreign-principal":
							var err error
							current.Spec, err = openHandoff(releasedLockPolicy(t).Spec, "system:serviceaccount:other:migrator")
							require.NoError(t, err)
							require.NoError(t, c.Update(ctx, current))
						default:
							current.Status.ObservedGeneration++
							require.NoError(t, c.Status().Update(ctx, current))
						}
						return apierrors.NewConflict(schema.GroupResource{Resource: "validatingadmissionpolicies"}, policy.Name, fmt.Errorf("changed"))
					}
					if err := c.Patch(ctx, object, patch, options...); err != nil {
						return err
					}
					current := &admissionv1.ValidatingAdmissionPolicy{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(&policy), current))
					current.Status.ObservedGeneration = current.Generation
					return c.Status().Update(ctx, current)
				}})
				p := &Procedure{kube: &Kube{client: wrapped, timeout: time.Second}, plan: &Plan{Config: Config{
					OperatorNamespace: deployment.Namespace, Migrator: testMigrator}, Deployments: []DeploymentPlan{{Name: deployment.Name, UID: deployment.UID}}}}
				_, err := p.setHandoff(t.Context(), open)
				switch mode {
				case "status":
					require.NoError(t, err)
					require.Equal(t, 2, attempts)
					current := &admissionv1.ValidatingAdmissionPolicy{}
					require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(&policy), current))
					actual, principal := handoffPrincipal(current.Spec)
					require.Equal(t, open, actual)
					if open {
						require.Equal(t, testMigrator, principal)
					}
				case "persistent":
					require.True(t, apierrors.IsConflict(err), "%v", err)
					require.Equal(t, 5, attempts)
				case "forbidden":
					require.True(t, apierrors.IsForbidden(err), "%v", err)
					require.Equal(t, 1, attempts)
				default:
					require.Error(t, err)
					message := map[string]string{"operator-resumed": "not scaled to zero", "policy-replaced": "was replaced", "foreign-principal": "another principal"}[mode]
					require.ErrorContains(t, err, message)
					require.Equal(t, 1, attempts, "a changed guard must prevent a second write")
				}
			})
		}
	}
}

const (
	testMigrator     = "system:serviceaccount:openbao:migrator"
	testSourceAPI    = "openbao.org/v1alpha1"
	testDestAPI      = "kubebao.org/v1alpha1"
	testSourceUID    = types.UID("source-uid")
	testDestUID      = types.UID("destination-uid")
	testClusterName  = "bao"
	testOwnerUIDKey  = "openbao.org/owner-uid"
	testUnrelatedKey = "example.com/team"
)

// releasedLockPolicy is the lock policy every installation method renders.
func releasedLockPolicy(t *testing.T) admissionv1.ValidatingAdmissionPolicy {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", "openbao-lock-managed-resource-mutations.yaml"))
	require.NoError(t, err)
	var policy admissionv1.ValidatingAdmissionPolicy
	require.NoError(t, syaml.Unmarshal(data, &policy))
	return policy
}

func openedLockPolicy(t *testing.T) admissionv1.ValidatingAdmissionPolicy {
	t.Helper()
	policy := releasedLockPolicy(t)
	spec, err := openHandoff(policy.Spec, testMigrator)
	require.NoError(t, err)
	policy.Spec = spec
	return policy
}

func TestHandoffOpensAndClosesExactly(t *testing.T) {
	released := releasedLockPolicy(t)
	opened := openedLockPolicy(t)
	require.Len(t, opened.Spec.Validations, len(released.Spec.Validations))
	extended := 0
	for i, validation := range opened.Spec.Validations {
		original := released.Spec.Validations[i]
		if validation.Expression == original.Expression {
			continue
		}
		extended++
		require.Equal(t, handoffOpening+original.Expression+handoffClosing, validation.Expression,
			"validation %q only gains the handoff alternative", original.Message)
		require.Equal(t, original.Message, validation.Message)
	}
	require.Equal(t, 2, extended, "only the owner-uid and managed-mutation validations are extended")
	open, principal := handoffPrincipal(opened.Spec)
	require.True(t, open)
	require.Equal(t, testMigrator, principal)

	again, err := openHandoff(opened.Spec, testMigrator)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(opened.Spec, again), "opening twice changes nothing")
	_, err = openHandoff(opened.Spec, "system:serviceaccount:openbao:other")
	require.ErrorContains(t, err, "another principal")

	closed, err := closeHandoff(opened.Spec)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(released.Spec, closed), "closing restores the released policy exactly")
	open, _ = handoffPrincipal(closed)
	require.False(t, open)
}

func TestHandoffPrincipalIsALiteral(t *testing.T) {
	spec, err := openHandoff(releasedLockPolicy(t).Spec, `system:serviceaccount:openbao:mig"rator`)
	require.NoError(t, err)
	require.Equal(t, `"system:serviceaccount:openbao:mig\"rator"`, spec.Variables[len(releasedLockPolicy(t).Spec.Variables)].Expression,
		"the principal is a quoted CEL string, never an expression")
}

type handoffCase struct {
	name      string
	operation string
	kind      metav1.GroupVersionKind
	old       runtime.Object
	new       runtime.Object
	request   func(map[string]any)
	allowed   bool
}

func TestAPIMigrationHandoffBoundaries(t *testing.T) {
	policy := openedLockPolicy(t)
	configMap := metav1.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	secret := metav1.GroupVersionKind{Version: "v1", Kind: "Secret"}
	statefulSet := metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}
	pod := metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}
	job := metav1.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	backupJob := func(api string, uid types.UID, image string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: managedMeta(api, uid), Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "backup", Image: image}}}}}}
	}

	// A restore executor Job: provenance and controller reference name its record.
	restoreJob := func(api string, uid types.UID, change func(*batchv1.Job)) *batchv1.Job {
		job := backupJob("", uid, "backup:1")
		if api != "" {
			job.OwnerReferences = []metav1.OwnerReference{{APIVersion: api, Kind: "OpenBaoRestore", Name: "restore-1",
				UID: uid, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}}
		}
		if change != nil {
			change(job)
		}
		return job
	}

	owned := func(api string, uid types.UID) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: managedMeta(api, uid), Data: map[string]string{"config": "value"}}
	}
	retained := func(uid types.UID) *corev1.Secret {
		meta := managedMeta("", uid)
		return &corev1.Secret{ObjectMeta: meta, Data: map[string][]byte{"key": []byte("secret")}}
	}
	set := func(api string, uid, template types.UID) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: managedMeta(api, uid), Spec: appsv1.StatefulSetSpec{
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{
				Name: "data", Annotations: map[string]string{testOwnerUIDKey: string(template)}}}},
		}}
	}
	mutate := func(object *corev1.ConfigMap, change func(*corev1.ConfigMap)) *corev1.ConfigMap {
		object = object.DeepCopy()
		change(object)
		return object
	}

	cases := []handoffCase{
		{name: "forward controller swap", operation: "UPDATE", kind: configMap,
			old: owned(testSourceAPI, testSourceUID), new: owned(testDestAPI, testDestUID), allowed: true},
		{name: "reverse controller swap", operation: "UPDATE", kind: configMap,
			old: owned(testDestAPI, testDestUID), new: owned(testSourceAPI, testSourceUID), allowed: true},
		{name: "retained provenance swap", operation: "UPDATE", kind: secret,
			old: retained(testSourceUID), new: retained(testDestUID), allowed: true},
		{name: "create recreated StatefulSet", operation: "CREATE", kind: statefulSet,
			new: set(testDestAPI, testDestUID, testDestUID), allowed: true},
		{name: "delete StatefulSet for recreation", operation: "DELETE", kind: statefulSet,
			old: set(testDestAPI, testDestUID, testSourceUID), allowed: true},
		{name: "finished backup Job swap", operation: "UPDATE", kind: job,
			old: backupJob(testSourceAPI, testSourceUID, "backup:1"), new: backupJob(testDestAPI, testDestUID, "backup:1"),
			allowed: true},
		{name: "backup Job spec change", operation: "UPDATE", kind: job,
			old: backupJob(testSourceAPI, testSourceUID, "backup:1"), new: backupJob(testDestAPI, testDestUID, "backup:2")},
		{name: "delete backup Job", operation: "DELETE", kind: job, old: backupJob(testDestAPI, testDestUID, "backup:1")},
		{name: "restore Job swap", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, nil), new: restoreJob(testDestAPI, testDestUID, nil), allowed: true},
		{name: "reverse restore Job swap", operation: "UPDATE", kind: job,
			old: restoreJob(testDestAPI, testDestUID, nil), new: restoreJob(testSourceAPI, testSourceUID, nil), allowed: true},
		{name: "restore Job to another record", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, nil),
			new: restoreJob(testDestAPI, testDestUID, func(j *batchv1.Job) { j.OwnerReferences[0].Name = "restore-2" })},
		{name: "restore Job to a Cluster", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, nil),
			new: restoreJob(testDestAPI, testDestUID, func(j *batchv1.Job) { j.OwnerReferences[0].Kind = "OpenBaoCluster" })},
		{name: "restore Job reference and annotation disagree", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, nil),
			new: restoreJob(testDestAPI, testDestUID, func(j *batchv1.Job) { j.Annotations[testOwnerUIDKey] = "forged" })},
		{name: "restore Job without provenance", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, func(j *batchv1.Job) { delete(j.Annotations, testOwnerUIDKey) }),
			new: restoreJob(testDestAPI, testDestUID, func(j *batchv1.Job) { delete(j.Annotations, testOwnerUIDKey) })},
		{name: "restore Job same group retarget", operation: "UPDATE", kind: job,
			old: restoreJob(testDestAPI, testSourceUID, nil), new: restoreJob(testDestAPI, testDestUID, nil)},
		{name: "restore Job spec change", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, nil),
			new: restoreJob(testDestAPI, testDestUID, func(j *batchv1.Job) { j.Spec.Template.Spec.Containers[0].Image = "x" })},
		{name: "restore Job loses blockOwnerDeletion", operation: "UPDATE", kind: job,
			old: restoreJob(testSourceAPI, testSourceUID, nil),
			new: restoreJob(testDestAPI, testDestUID, func(j *batchv1.Job) { j.OwnerReferences[0].BlockOwnerDeletion = nil })},
		{name: "ConfigMap with a restore controller", operation: "UPDATE", kind: configMap,
			old: mutate(owned(testSourceAPI, testSourceUID), func(c *corev1.ConfigMap) { c.OwnerReferences[0].Kind = "OpenBaoRestore" }),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.OwnerReferences[0].Kind = "OpenBaoRestore" })},
		{name: "Cluster Job moved to a restore record", operation: "UPDATE", kind: job,
			old: backupJob(testSourceAPI, testSourceUID, "backup:1"), new: restoreJob(testDestAPI, testDestUID, nil)},

		{name: "other principal", operation: "UPDATE", kind: configMap,
			old: owned(testSourceAPI, testSourceUID), new: owned(testDestAPI, testDestUID),
			request: func(r map[string]any) {
				r["userInfo"] = map[string]any{"username": "system:serviceaccount:openbao:other"}
			}},
		{name: "status subresource", operation: "UPDATE", kind: configMap,
			old: owned(testSourceAPI, testSourceUID), new: owned(testDestAPI, testDestUID),
			request: func(r map[string]any) { r["subResource"] = "status" }},
		{name: "payload change", operation: "UPDATE", kind: configMap, old: owned(testSourceAPI, testSourceUID),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.Data["config"] = "changed" })},
		{name: "secret payload change", operation: "UPDATE", kind: secret, old: retained(testSourceUID),
			new: func() *corev1.Secret { s := retained(testDestUID); s.Data["key"] = []byte("changed"); return s }()},
		{name: "label change", operation: "UPDATE", kind: configMap, old: owned(testSourceAPI, testSourceUID),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.Labels[testUnrelatedKey] = "x" })},
		{name: "unrelated annotation change", operation: "UPDATE", kind: configMap, old: owned(testSourceAPI, testSourceUID),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.Annotations[testUnrelatedKey] = "x" })},
		{name: "finalizer change", operation: "UPDATE", kind: configMap, old: owned(testSourceAPI, testSourceUID),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.Finalizers = []string{"example.com/hold"} })},
		{name: "different cluster name", operation: "UPDATE", kind: configMap, old: owned(testSourceAPI, testSourceUID),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.OwnerReferences[0].Name = "other" })},
		{name: "same group retarget", operation: "UPDATE", kind: configMap,
			old: owned(testDestAPI, testSourceUID), new: owned(testDestAPI, testDestUID)},
		{name: "reference and annotation disagree", operation: "UPDATE", kind: configMap, old: owned(testSourceAPI, testSourceUID),
			new: mutate(owned(testDestAPI, testDestUID), func(c *corev1.ConfigMap) { c.Annotations[testOwnerUIDKey] = "forged" })},
		{name: "unchanged owner", operation: "UPDATE", kind: configMap,
			old: owned(testSourceAPI, testSourceUID), new: owned(testSourceAPI, testSourceUID)},
		{name: "retained gains controller", operation: "UPDATE", kind: secret, old: retained(testSourceUID),
			new: func() *corev1.Secret {
				s := retained(testDestUID)
				s.OwnerReferences = owned(testDestAPI, testDestUID).OwnerReferences
				return s
			}()},
		{name: "unsupported kind", operation: "UPDATE", kind: pod,
			old: &corev1.Pod{ObjectMeta: managedMeta(testSourceAPI, testSourceUID)},
			new: &corev1.Pod{ObjectMeta: managedMeta(testDestAPI, testDestUID)}},
		{name: "create with stale claim template", operation: "CREATE", kind: statefulSet,
			new: set(testDestAPI, testDestUID, testSourceUID)},
		{name: "create other kind", operation: "CREATE", kind: configMap, new: owned(testDestAPI, testDestUID)},
		{name: "delete other kind", operation: "DELETE", kind: configMap, old: owned(testDestAPI, testDestUID)},
		{name: "delete unowned StatefulSet", operation: "DELETE", kind: statefulSet,
			old: &appsv1.StatefulSet{ObjectMeta: managedMeta("", testSourceUID)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateHandoff(t, policy, tc); got != tc.allowed {
				t.Fatalf("handoff allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func managedMeta(api string, uid types.UID) metav1.ObjectMeta {
	meta := metav1.ObjectMeta{Name: "bao-config", Namespace: "tenant",
		Labels:      map[string]string{"app.kubernetes.io/managed-by": "openbao-operator", "openbao.org/cluster": testClusterName},
		Annotations: map[string]string{testOwnerUIDKey: string(uid), "example.com/kept": "value"}}
	if api != "" {
		meta.OwnerReferences = []metav1.OwnerReference{{APIVersion: api, Kind: "OpenBaoCluster", Name: testClusterName,
			UID: uid, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}}
	}
	return meta
}

func TestRetainedUpgradeServiceAccountHandoff(t *testing.T) {
	policy := openedLockPolicy(t)
	meta := managedMeta("", testSourceUID)
	meta.Name = testClusterName + "-upgrade-serviceaccount"
	meta.Labels["openbao.org/service-account-role"] = "upgrade"
	before := &corev1.ServiceAccount{ObjectMeta: meta, AutomountServiceAccountToken: ptr.To(false),
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}}
	after := before.DeepCopy()
	after.Annotations[testOwnerUIDKey] = string(testDestUID)
	tc := handoffCase{operation: "UPDATE", kind: metav1.GroupVersionKind{Version: "v1", Kind: "ServiceAccount"}, old: before, new: after}
	require.True(t, evaluateHandoff(t, policy, tc))
	tc.old, tc.new = after, before
	require.True(t, evaluateHandoff(t, policy, tc))
	for _, field := range []string{"imagePullSecrets", "automount", "secrets", "name", "role"} {
		t.Run(field, func(t *testing.T) {
			old, changed := before.DeepCopy(), after.DeepCopy()
			switch field {
			case "imagePullSecrets":
				changed.ImagePullSecrets[0].Name = "different-registry"
			case "automount":
				changed.AutomountServiceAccountToken = ptr.To(true)
			case "secrets":
				changed.Secrets = []corev1.ObjectReference{{Name: "unexpected-secret"}}
			case "name":
				old.Name, changed.Name = "unrelated", "unrelated"
			case "role":
				old.Labels["openbao.org/service-account-role"], changed.Labels["openbao.org/service-account-role"] = "wrong-role", "wrong-role"
			}
			tc.old, tc.new = old, changed
			require.False(t, evaluateHandoff(t, policy, tc))
		})
	}
}

func evaluateHandoff(t *testing.T, policy admissionv1.ValidatingAdmissionPolicy, tc handoffCase) bool {
	t.Helper()
	toMap := func(object runtime.Object) any {
		if object == nil {
			return nil
		}
		value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	request := map[string]any{
		"operation": tc.operation, "subResource": "", "namespace": "tenant",
		"kind":     map[string]any{"group": tc.kind.Group, "version": tc.kind.Version, "kind": tc.kind.Kind},
		"userInfo": map[string]any{"username": testMigrator},
	}
	if tc.request != nil {
		tc.request(request)
	}
	env, err := cel.NewEnv(ext.TwoVarComprehensions(),
		cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	variables := map[string]any{}
	activation := map[string]any{"object": toMap(tc.new), "oldObject": toMap(tc.old), "request": request,
		"variables": variables}
	for _, variable := range policy.Spec.Variables {
		if !strings.HasPrefix(variable.Name, "api_migration_") {
			continue
		}
		ast, issues := env.Compile(variable.Expression)
		if issues.Err() != nil {
			t.Fatalf("compile %s: %v", variable.Name, issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		value, _, err := program.Eval(activation)
		if err != nil {
			// The API server treats an evaluation error in this term as a denial.
			variables[variable.Name] = false
			continue
		}
		variables[variable.Name] = value.Value()
	}
	allowed, ok := variables["api_migration_handoff"].(bool)
	if !ok {
		t.Fatalf("api_migration_handoff did not evaluate to a boolean: %T", variables["api_migration_handoff"])
	}
	return allowed
}
