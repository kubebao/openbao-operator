package primitives

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func importOwnershipEntry(manager, subresource string) metav1.ManagedFieldsEntry {
	fields := `{"f:status":{"f:phase":{}}}`
	if subresource == "" {
		fields = `{"f:spec":{"f:replicas":{}}}`
	}
	return metav1.ManagedFieldsEntry{Manager: manager, Subresource: subresource,
		Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: PlanDestinationAPIVersion,
		FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(fields)}
}

func pendingStatusImport(t *testing.T) (*unstructured.Unstructured, ImportRequest, *ImportPayload,
	*unstructured.Unstructured,
) {
	t.Helper()
	source, request := sourceFixture(t)
	payload, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
	require.NoError(t, err)
	request.ExpectedImportDigest = payload.Digest
	request.ExpectedDestinationUID = "destination"
	destination := payload.Object.DeepCopy()
	destination.SetUID(request.ExpectedDestinationUID)
	destination.SetResourceVersion("42")
	destination.Object["status"] = payload.Status
	destination.SetManagedFields([]metav1.ManagedFieldsEntry{
		importOwnershipEntry("unrelated-spec-owner", ""), importOwnershipEntry(importStatusFieldManager, "status"),
	})
	return source, request, payload, destination
}

func TestImportStatusOwnershipRejectsUnexpectedWriters(t *testing.T) {
	for _, field := range []string{"manager", "operation", "apiVersion"} {
		t.Run(field, func(t *testing.T) {
			source, request, _, destination := pendingStatusImport(t)
			entries := destination.GetManagedFields()
			switch field {
			case "manager":
				entries[1].Manager = "another-writer"
			case "operation":
				entries[1].Operation = metav1.ManagedFieldsOperationApply
			case "apiVersion":
				entries[1].APIVersion = PlanSourceAPIVersion
			}
			destination.SetManagedFields(entries)
			c := sourceClient(t, source, []client.Object{destination}, interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					t.Fatal("unexpected ownership must not be cleared")
					return nil
				},
			})
			_, err := Import(t.Context(), c, request)
			require.ErrorContains(t, err, "unexpected status field ownership")
		})
	}
}

func TestImportResumesPendingStatusOwnershipWithoutRewritingStatus(t *testing.T) {
	source, request, payload, destination := pendingStatusImport(t)
	retained := destination.GetManagedFields()[:1]
	patches := 0
	c := sourceClient(t, source, []client.Object{destination}, interceptor.Funcs{
		SubResourcePatch: func(context.Context, client.Client, string, client.Object,
			client.Patch, ...client.SubResourcePatchOption,
		) error {
			t.Fatal("accepted status must not be written again")
			return nil
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch,
			opts ...client.PatchOption,
		) error {
			patches++
			data, err := patch.Data(obj)
			require.NoError(t, err)
			var operations []transferPatchOperation
			require.NoError(t, json.Unmarshal(data, &operations))
			require.Len(t, operations, 3)
			require.Equal(t, transferPatchOperation{Op: "test", Path: "/metadata/uid", Value: "destination"}, operations[0])
			require.Equal(t, "/metadata/resourceVersion", operations[1].Path)
			require.Equal(t, "test", operations[1].Op)
			require.Equal(t, "/metadata/managedFields", operations[2].Path)
			require.Equal(t, "replace", operations[2].Op)
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	require.True(t, ImportStatusPending(destination), "the accepted status still needs its ownership released")
	require.Equal(t, request.ExpectedDestinationUID, destination.GetUID())
	request.CreateOnly = true
	result, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	require.False(t, result.StatusImported)
	require.Zero(t, patches)
	request.CreateOnly = false
	result, err = Import(t.Context(), c, request)
	require.NoError(t, err)
	require.True(t, result.StatusImported)
	require.Equal(t, 1, patches)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(destination), destination))
	require.Equal(t, retained, destination.GetManagedFields())
	require.Equal(t, payload.Status, destination.Object["status"])
	require.False(t, ImportStatusPending(destination))
}

func TestImportRechecksAuthorityBeforeStatusOwnershipRelease(t *testing.T) {
	source, request, payload, destination := pendingStatusImport(t)
	c := sourceClient(t, source, []client.Object{destination}, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			t.Fatal("ownership release must not run after authority recheck fails")
			return nil
		},
	})
	checked := false
	result, err := importApplyPayload(t.Context(), c, request, payload, func(context.Context) error {
		checked = true
		return errors.New("source authority changed")
	})
	require.ErrorContains(t, err, "source authority changed")
	require.True(t, checked)
	require.False(t, result.StatusImported)
}
