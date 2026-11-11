package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

func retirementFixture(t *testing.T, kind, name string, finalizers ...string) (*Procedure, *unstructured.Unstructured) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	for _, resource := range []string{clusterKind, restoreKind, tenantKind} {
		gvk := schema.FromAPIVersionAndKind(primitives.PlanSourceAPIVersion, resource)
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(resource+"List"), &unstructured.UnstructuredList{})
	}
	source := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
	source.SetAPIVersion(primitives.PlanSourceAPIVersion)
	source.SetKind(kind)
	source.SetNamespace("openbao")
	source.SetName(name)
	source.SetUID(types.UID(name + "-uid"))
	source.SetFinalizers(finalizers)
	objects := []client.Object{source, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openbao", UID: "namespace"}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	p := &Procedure{kube: &Kube{client: c}, log: io.Discard,
		plan: &Plan{Phase: phaseCommitting, Config: Config{OperatorNamespace: "openbao"},
			Progress: Progress{Command: "commit", Completed: []string{"verify"}}}}
	if kind == clusterKind {
		p.plan.Clusters = []ClusterPlan{{Namespace: source.GetNamespace(), Name: name, SourceUID: source.GetUID(),
			SpecDigest: digestJSON(source.Object["spec"])}}
	} else {
		record := RecordPlan{Kind: kind, Namespace: source.GetNamespace(), Name: name, SourceUID: source.GetUID(),
			SpecDigest: digestJSON(source.Object["spec"])}
		if kind == restoreKind {
			p.plan.Restores = []RecordPlan{record}
		} else {
			p.plan.Tenants = []RecordPlan{record}
		}
	}
	return p, source
}

func TestRollbackRejectsDeletingSourcesBeforeChangingState(t *testing.T) {
	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		t.Run(kind, func(t *testing.T) {
			p, source := retirementFixture(t, kind, "retiring", metav1.FinalizerOrphanDependents)
			now := metav1.Now()
			source.SetDeletionTimestamp(&now)
			// A fake client retains a deleting object with an outstanding finalizer.
			p.kube.client = fake.NewClientBuilder().WithScheme(p.kube.client.Scheme()).WithObjects(source).Build()
			require.ErrorContains(t, p.rollbackPossible(t.Context()), "being deleted")
			require.ErrorContains(t, p.Rollback(t.Context()), "being deleted")
			require.Equal(t, phaseCommitting, p.plan.Phase)
			require.Equal(t, Progress{Command: "commit", Completed: []string{"verify"}}, p.plan.Progress,
				"an irreversible commit must remain resumable")
		})
	}
}

func TestSourcePreflightRejectsForeignFinalizers(t *testing.T) {
	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		t.Run(kind, func(t *testing.T) {
			p, source := retirementFixture(t, kind, "blocked", "example.org/protection")
			if kind == clusterKind {
				_, err := p.planCluster(t.Context(), source, "operator-namespace")
				require.ErrorContains(t, err, "unknown finalizer")
				_, err = p.sourceIntact(t.Context(), p.plan.Clusters[0])
				require.ErrorContains(t, err, "unknown finalizer")
			} else {
				records := append(p.plan.Restores, p.plan.Tenants...)
				require.ErrorContains(t, p.inspectRecord(t.Context(), records[0]), "unknown finalizer")
			}
		})
	}
}

func TestRetirementChecksAllSourcesBeforeDeletingAny(t *testing.T) {
	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		t.Run(kind, func(t *testing.T) {
			p, blocked := retirementFixture(t, kind, "later", "example.org/protection")
			first := blocked.DeepCopy()
			first.SetKind(clusterKind)
			first.SetName("first")
			first.SetUID("first-uid")
			first.SetResourceVersion("")
			first.SetFinalizers([]string{clusterFinalizer})
			require.NoError(t, p.kube.client.Create(t.Context(), first))
			p.plan.Clusters = append([]ClusterPlan{{Namespace: first.GetNamespace(), Name: first.GetName(),
				SourceUID: first.GetUID(), SpecDigest: digestJSON(first.Object["spec"])}}, p.plan.Clusters...)
			_, err := p.retireSource(t.Context())
			require.ErrorContains(t, err, "unknown finalizer")
			fresh, err := p.kube.getObject(t.Context(), first.GetAPIVersion(), first.GetKind(), first.GetNamespace(), first.GetName())
			require.NoError(t, err)
			require.NotNil(t, fresh)
			require.Nil(t, fresh.GetDeletionTimestamp())
			require.Equal(t, first.GetFinalizers(), fresh.GetFinalizers())
			require.NoError(t, p.rollbackPossible(t.Context()), "all sources must remain available for rollback")
		})
	}
}
