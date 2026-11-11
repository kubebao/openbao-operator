package primitives

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const testBatchAPIVersion = "batch/v1"

func sourceFixture(t *testing.T) (*unstructured.Unstructured, ImportRequest) {
	t.Helper()
	cluster := &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "bao", Namespace: "tenant", UID: "source-uid",
			ResourceVersion: "7", Generation: 2,
			Finalizers: []string{api.OpenBaoClusterFinalizer}, Labels: map[string]string{"example.org/team": "platform"},
			Annotations: map[string]string{"example.org/private": "DO-NOT-PRINT"}},
		Spec: api.OpenBaoClusterSpec{Profile: api.ProfileDevelopment, ControllerJWTMode: api.ControllerJWTModeTarget,
			Replicas: 1, Version: "2.6.4"},
		Status: api.OpenBaoClusterStatus{Initialized: true, SelfInitialized: true, Phase: api.ClusterPhaseRunning,
			ReadyReplicas:      1,
			ObservedGeneration: 2, CurrentVersion: "2.6.4", AcceptedUpgradeStrategy: api.UpdateStrategyRollingUpdate,
			Conditions: []metav1.Condition{{Type: string(api.ConditionAvailable), Status: metav1.ConditionTrue,
				ObservedGeneration: 2}}},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
	require.NoError(t, err)
	source := &unstructured.Unstructured{Object: object}
	source.SetAPIVersion(PlanSourceAPIVersion)
	source.SetKind(discoveryClusterKind)
	return source, ImportRequest{Source: identity(PlanSourceAPIVersion, discoveryClusterKind, source),
		NamespaceUID: "namespace-uid",
		MigrationID:  "migration-test", LabelKeys: []string{"example.org/team"}}
}

func sourceClient(t *testing.T, source *unstructured.Unstructured,
	extras []client.Object, funcs interceptor.Funcs,
) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	for _, version := range []string{PlanSourceAPIVersion, PlanDestinationAPIVersion} {
		for _, kind := range []string{discoveryClusterKind, "OpenBaoTenant", "OpenBaoRestore"} {
			scheme.AddKnownTypeWithName(schema.FromAPIVersionAndKind(version, kind), &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(schema.FromAPIVersionAndKind(version, kind+"List"), &unstructured.UnstructuredList{})
		}
	}
	destination := &unstructured.Unstructured{}
	destination.SetAPIVersion(PlanDestinationAPIVersion)
	destination.SetKind(discoveryClusterKind)
	objects := make([]client.Object, 0, 2+len(extras))
	objects = append(objects,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant", UID: "namespace-uid"}}, source)
	objects = append(objects, extras...)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(destination).
		WithReturnManagedFields().WithInterceptorFuncs(funcs).Build()
}

func TestSourceInspectionUsesExplicitOldGroup(t *testing.T) {
	source, request := sourceFixture(t)
	destination := source.DeepCopy()
	destination.SetAPIVersion(PlanDestinationAPIVersion)
	destination.SetUID("destination-uid")
	require.NoError(t, unstructured.SetNestedField(destination.Object, false, "status", "initialized"))
	c := sourceClient(t, source, []client.Object{destination}, interceptor.Funcs{})
	request.Source.UID = ""
	request.Source.ResourceVersion = ""
	observed, err := InspectSource(t.Context(), c, request)
	require.NoError(t, err)
	require.Equal(t, types.UID("source-uid"), observed.Source.UID)
	require.Equal(t, PlanSourceAPIVersion, observed.Source.APIVersion)
	data, err := json.Marshal(observed)
	require.NoError(t, err)
	require.NotContains(t, string(data), "DO-NOT-PRINT")
	request.Source.APIVersion = PlanDestinationAPIVersion
	_, err = InspectSource(t.Context(), c, request)
	require.Error(t, err)
}

func TestSourceScopeRestrictions(t *testing.T) {
	for name, edit := range map[string]func(*unstructured.Unstructured, *ImportRequest){
		"replicas not ready": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedField(s.Object, int64(3), "spec", "replicas")
		},
		"uninitialized": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedField(s.Object, false, "status", "initialized")
		},
		"stale generation": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedField(s.Object, int64(1), "status", "observedGeneration")
		},
		"unavailable": func(s *unstructured.Unstructured, _ *ImportRequest) {
			unstructured.RemoveNestedField(s.Object, "status", "conditions")
		},
		"operation": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedMap(s.Object, map[string]any{"operation": "Backup"}, "status", "operationLock")
		},
		"upgrade": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedMap(s.Object, map[string]any{}, "status", "upgrade")
		},
		"read replicas not ready": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedField(s.Object, int64(2), "spec", "readReplicas", "replicas")
			_ = unstructured.SetNestedMap(s.Object, map[string]any{"desiredReplicas": int64(2),
				"readyReplicas": int64(2), "registeredReplicas": int64(1)}, "status", "readReplicas")
		},
		"owner dependency": func(s *unstructured.Unstructured, _ *ImportRequest) {
			s.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: PlanSourceAPIVersion, Kind: "OpenBaoTenant",
				Name: "tenant", UID: "owner", Controller: ptr.To(true)}})
		},
		"backup trigger": func(s *unstructured.Unstructured, _ *ImportRequest) {
			s.SetAnnotations(map[string]string{constants.AnnotationTriggerBackup: "pending"})
		},
		"restore origin": func(s *unstructured.Unstructured, _ *ImportRequest) {
			s.SetAnnotations(map[string]string{constants.AnnotationRestoreOrigin: "restore-uid"})
		},
		"version drift": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedField(s.Object, "2.6.5", "spec", "version")
		},
		"pending retry": func(s *unstructured.Unstructured, _ *ImportRequest) {
			_ = unstructured.SetNestedMap(s.Object,
				map[string]any{"requests": map[string]any{"retry": "pending"}}, "spec", "upgrade")
		},
		"wrong namespace UID": func(_ *unstructured.Unstructured, r *ImportRequest) {
			r.NamespaceUID = discoveryTestReplacement
		},
		"wrong source UID": func(_ *unstructured.Unstructured, r *ImportRequest) {
			r.Source.UID = discoveryTestReplacement
		},
		"wrong source RV": func(_ *unstructured.Unstructured, r *ImportRequest) {
			r.Source.ResourceVersion = discoveryTestReplacement
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, request := sourceFixture(t)
			edit(source, &request)
			_, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
			require.Error(t, err)
		})
	}
}

// pausedSource is a Cluster paused before it ever initialized: its operator
// recorded the pause and created nothing else.
func pausedSource(t *testing.T) (*unstructured.Unstructured, ImportRequest) {
	t.Helper()
	source, request := sourceFixture(t)
	_ = unstructured.SetNestedField(source.Object, true, "spec", "paused")
	_ = unstructured.SetNestedMap(source.Object, map[string]any{"phase": "Initializing", "observedGeneration": int64(2),
		"conditions": []any{map[string]any{"type": string(api.ConditionAvailable), "status": "Unknown",
			"reason": PausedReason, "message": "Reconciliation is paused", "observedGeneration": int64(2),
			"lastTransitionTime": "2026-11-01T00:00:00Z"}}}, "status")
	return source, request
}

func TestSourceScopeAcceptsAnObservedPause(t *testing.T) {
	source, request := pausedSource(t)
	_, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
	require.NoError(t, err, "a Cluster paused before initialization")

	// Paused while running, with a version change its operator has not started.
	source, request = sourceFixture(t)
	_ = unstructured.SetNestedField(source.Object, true, "spec", "paused")
	_ = unstructured.SetNestedField(source.Object, "2.6.5", "spec", "version")
	_ = unstructured.SetNestedField(source.Object, int64(0), "status", "readyReplicas")
	_ = unstructured.SetNestedSlice(source.Object, []any{map[string]any{"type": string(api.ConditionAvailable),
		"status": "Unknown", "reason": PausedReason, "message": "Reconciliation is paused", "observedGeneration": int64(2),
		"lastTransitionTime": "2026-11-01T00:00:00Z"}}, "status", "conditions")
	_, err = BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
	require.NoError(t, err, "a Cluster paused while running")
}

func TestSourceScopeRefusesAPauseNotObservedOrWithWorkInFlight(t *testing.T) {
	for name, edit := range map[string]func(*unstructured.Unstructured){
		"pause not observed": func(s *unstructured.Unstructured) { s.SetGeneration(3) },
		"available condition from before the pause": func(s *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(s.Object, []any{map[string]any{"type": string(api.ConditionAvailable),
				"status": "True", "reason": "Ready", "message": "ready", "observedGeneration": int64(2),
				"lastTransitionTime": "2026-11-01T00:00:00Z"}}, "status", "conditions")
		},
		"operation": func(s *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(s.Object, map[string]any{"operation": "Backup"}, "status", "operationLock")
		},
		"upgrade": func(s *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(s.Object, map[string]any{}, "status", "upgrade")
		},
		"restore hold": func(s *unstructured.Unstructured) {
			s.SetAnnotations(map[string]string{constants.AnnotationRestoreHold: "restore-uid"})
		},
		"pending retry": func(s *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(s.Object,
				map[string]any{"requests": map[string]any{"retry": "pending"}}, "spec", "upgrade")
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, request := pausedSource(t)
			edit(source)
			_, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
			require.Error(t, err)
		})
	}
}

func TestSourceScopeAcceptsHardenedHighAvailability(t *testing.T) {
	source, request := sourceFixture(t)
	_ = unstructured.SetNestedField(source.Object, "Hardened", "spec", "profile")
	_ = unstructured.SetNestedField(source.Object, int64(3), "spec", "replicas")
	_ = unstructured.SetNestedField(source.Object, int64(3), "status", "readyReplicas")
	_, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
	require.NoError(t, err)
}

func TestSourceScopeAcceptsReadReplicasAndRestoreHistory(t *testing.T) {
	source, request := sourceFixture(t)
	_ = unstructured.SetNestedField(source.Object, int64(2), "spec", "readReplicas", "replicas")
	_ = unstructured.SetNestedMap(source.Object, map[string]any{"desiredReplicas": int64(2),
		"readyReplicas": int64(2), "registeredReplicas": int64(2)}, "status", "readReplicas")
	history := map[string]any{"name": "restore-2025", "uid": "restore-uid"}
	_ = unstructured.SetNestedMap(source.Object, history, "status", "restore")
	payload, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
	require.NoError(t, err)
	imported, _, _ := unstructured.NestedMap(payload.Status, "restore")
	require.Equal(t, history, imported, "restore history feeds the Pod template and must be imported unchanged")
}

// Tenant and restore records in either group are the caller's to inspect and
// import; a Cluster import does not refuse them.
func TestImportLeavesRelatedRecordsToTheCaller(t *testing.T) {
	for _, version := range []string{PlanSourceAPIVersion, PlanDestinationAPIVersion} {
		for _, kind := range []string{"OpenBaoTenant", "OpenBaoRestore"} {
			for _, namespace := range []string{"tenant", "operator"} {
				t.Run(version+"/"+kind+"/"+namespace, func(t *testing.T) {
					source, request := sourceFixture(t)
					request.RelatedNamespaces = []NamespaceIdentity{{Name: "operator", UID: "operator-uid"}}
					object := &unstructured.Unstructured{}
					object.SetAPIVersion(version)
					object.SetKind(kind)
					object.SetNamespace(namespace)
					object.SetName("related")
					object.SetUID("related-uid")
					extras := []client.Object{
						&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "operator-uid"}}, object,
					}
					_, err := BuildImport(t.Context(), sourceClient(t, source, extras, interceptor.Funcs{}), request)
					require.NoError(t, err)
				})
			}
		}
	}
	source, request := sourceFixture(t)
	request.RelatedNamespaces = []NamespaceIdentity{{Name: "operator", UID: "replaced"}}
	extras := []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator", UID: "operator-uid"}}}
	_, err := BuildImport(t.Context(), sourceClient(t, source, extras, interceptor.Funcs{}), request)
	require.ErrorContains(t, err, "related namespace was replaced")
}

func TestImportRefusesOnlyRunningOperatorJobs(t *testing.T) {
	job := func(name string, managed bool, condition batchv1.JobConditionType) *batchv1.Job {
		object := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant"}}
		if managed {
			object.Labels = map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator}
		}
		if condition != "" {
			object.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue}}
		}
		return object
	}
	for name, test := range map[string]struct {
		job     *batchv1.Job
		allowed bool
	}{
		"finished backup":      {job("bao-backup-1", true, batchv1.JobComplete), true},
		"failed backup":        {job("bao-backup-2", true, batchv1.JobFailed), true},
		"running backup":       {job("bao-backup-3", true, ""), false},
		"unrelated user Job":   {job("report", false, ""), true},
		"suspended or pending": {job("bao-retention", true, batchv1.JobSuspended), false},
	} {
		t.Run(name, func(t *testing.T) {
			source, request := sourceFixture(t)
			_, err := BuildImport(t.Context(), sourceClient(t, source, []client.Object{test.job}, interceptor.Funcs{}), request)
			if test.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "still running")
			}
		})
	}
}

func TestImportDigestAndLostCreateResponse(t *testing.T) {
	source, request := sourceFixture(t)
	creates, patches := 0, 0
	c := sourceClient(t, source, nil, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			creates++
			object.SetUID("destination-uid")
			if err := c.Create(ctx, object, opts...); err != nil {
				return err
			}
			return errors.New("lost create response")
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, name string, object client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption,
		) error {
			patches++
			return c.SubResource(name).Patch(ctx, object, patch, opts...)
		},
	})
	payload, err := BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "spec")
	require.NotContains(t, payload.Object.GetAnnotations(), "example.org/private")
	require.Empty(t, payload.Object.GetFinalizers())
	require.Equal(t, source.Object["spec"], payload.Object.Object["spec"])
	require.NotContains(t, payload.Status, "conditions")
	require.NotContains(t, payload.Status, "observedGeneration")
	request.ExpectedImportDigest = strings.Repeat("0", 64)
	_, err = Import(t.Context(), c, request)
	require.ErrorContains(t, err, "digest differs")
	require.Zero(t, creates)
	request.ExpectedImportDigest = payload.Digest
	result, err := Import(t.Context(), c, request)
	require.ErrorContains(t, err, "record identity before resume")
	require.Equal(t, types.UID("destination-uid"), result.Destination.UID)
	require.Zero(t, patches)
	request.ExpectedDestinationUID = result.Destination.UID
	result, err = Import(t.Context(), c, request)
	require.NoError(t, err)
	require.True(t, result.StatusImported)
	require.Equal(t, 1, creates)
	require.Equal(t, 1, patches)
	_, err = Import(t.Context(), c, request)
	require.NoError(t, err)
	require.Equal(t, 1, patches)
}

func TestImportLostStatusResponseRequiresInspection(t *testing.T) {
	source, request := sourceFixture(t)
	patches := 0
	c := sourceClient(t, source, nil, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			object.SetUID("destination-uid")
			return c.Create(ctx, object, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, name string, object client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption,
		) error {
			patches++
			if err := c.SubResource(name).Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			return errors.New("lost status response")
		},
	})
	payload, err := BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	request.ExpectedImportDigest = payload.Digest
	result, err := Import(t.Context(), c, request)
	require.ErrorContains(t, err, "requires inspection")
	require.Equal(t, types.UID("destination-uid"), result.Destination.UID)
	require.False(t, result.StatusImported)
	request.ExpectedDestinationUID = result.Destination.UID
	result, err = Import(t.Context(), c, request)
	require.NoError(t, err)
	require.True(t, result.StatusImported)
	require.Equal(t, 1, patches)
}

func TestImportRechecksSourceBeforeStatusWrite(t *testing.T) {
	source, request := sourceFixture(t)
	c := sourceClient(t, source, nil, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			object.SetUID("destination-uid")
			if err := c.Create(ctx, object, opts...); err != nil {
				return err
			}
			changed := source.DeepCopy()
			if err := c.Get(ctx, client.ObjectKeyFromObject(changed), changed); err != nil {
				return err
			}
			changed.SetAnnotations(map[string]string{"example.org/change": "concurrent"})
			return c.Update(ctx, changed)
		},
		SubResourcePatch: func(context.Context, client.Client, string, client.Object,
			client.Patch, ...client.SubResourcePatchOption,
		) error {
			t.Fatal("status must not be imported after source revision drift")
			return nil
		},
	})
	payload, err := BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	request.ExpectedImportDigest = payload.Digest
	result, err := Import(t.Context(), c, request)
	require.ErrorContains(t, err, "source identity or revision changed")
	require.Equal(t, types.UID("destination-uid"), result.Destination.UID)
}

func TestImportRefusesForeignOrMissingRecordedDestination(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := map[bool]string{false: "recorded destination missing", true: "foreign destination"}[foreign]
		t.Run(name, func(t *testing.T) {
			source, request := sourceFixture(t)
			var extras []client.Object
			if foreign {
				object := source.DeepCopy()
				object.SetAPIVersion(PlanDestinationAPIVersion)
				object.SetUID("foreign")
				extras = append(extras, object)
			}
			c := sourceClient(t, source, extras, interceptor.Funcs{
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					t.Fatal("must not replace a recorded destination")
					return nil
				},
			})
			payload, err := BuildImport(t.Context(), c, request)
			require.NoError(t, err)
			request.ExpectedImportDigest = payload.Digest
			if !foreign {
				request.ExpectedDestinationUID = "recorded"
			}
			_, err = Import(t.Context(), c, request)
			require.Error(t, err)
		})
	}
}
