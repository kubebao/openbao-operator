package primitives

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestImportCreateOnlySeparatesIdentityFromStatusWrite(t *testing.T) {
	source, request := sourceFixture(t)
	creates, patches := 0, 0
	c := sourceClient(t, source, nil, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			creates++
			object.SetUID("created-destination")
			return c.Create(ctx, object, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, name string, object client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption,
		) error {
			patches++
			require.Equal(t, "status", name)
			return c.SubResource(name).Patch(ctx, object, patch, opts...)
		},
	})
	payload, err := BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	request.ExpectedImportDigest = payload.Digest
	request.CreateOnly = true
	createPayload, err := BuildImport(t.Context(), c, request)
	require.NoError(t, err)
	require.Equal(t, payload.Digest, createPayload.Digest, "write phases must share the reviewed payload digest")
	created, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	require.Equal(t, types.UID("created-destination"), created.Destination.UID)
	require.NotEmpty(t, created.Destination.ResourceVersion)
	require.False(t, created.StatusImported)
	require.Equal(t, 1, creates)
	require.Zero(t, patches)
	observed := payload.Object.DeepCopy()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(observed), observed))
	require.Empty(t, observed.Object["status"])
	require.Equal(t, payload.Object.Object["spec"], observed.Object["spec"])
	require.Equal(t, payload.Object.GetAnnotations(), observed.GetAnnotations())

	// A repeated create-only request remains an observation. A separately
	// authorized request with the recorded UID imports the status once.
	request.ExpectedDestinationUID = created.Destination.UID
	repeated, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	require.Equal(t, created, repeated)
	require.Equal(t, 1, creates)
	require.Zero(t, patches)
	request.CreateOnly = false
	completed, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	require.True(t, completed.StatusImported)
	require.Equal(t, created.Destination.UID, completed.Destination.UID)
	require.Equal(t, 1, creates)
	require.Equal(t, 1, patches)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(observed), observed))
	require.Equal(t, payload.Status, observed.Object["status"])
	request.CreateOnly = true
	alreadyComplete, err := Import(t.Context(), c, request)
	require.NoError(t, err)
	require.True(t, alreadyComplete.StatusImported)
	require.Equal(t, completed, alreadyComplete)
	require.Equal(t, 1, patches)
}

func TestImportCreateOnlyStillRejectsDestinationDrift(t *testing.T) {
	for name, edit := range map[string]func(*unstructured.Unstructured){
		"replacement UID": func(object *unstructured.Unstructured) { object.SetUID("replacement") },
		"missing markers": func(object *unstructured.Unstructured) { object.SetAnnotations(nil) },
		"different migration": func(object *unstructured.Unstructured) {
			annotations := object.GetAnnotations()
			annotations[ImportMigrationAnnotation] = "different-migration"
			object.SetAnnotations(annotations)
		},
		"different digest": func(object *unstructured.Unstructured) {
			annotations := object.GetAnnotations()
			annotations[ImportHashAnnotation] = strings.Repeat("0", 64)
			object.SetAnnotations(annotations)
		},
		"different spec": func(object *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(object.Object, int64(2), "spec", "replicas")
		},
		"different status": func(object *unstructured.Unstructured) {
			object.Object["status"] = map[string]any{"initialized": false}
		},
		"malformed status": func(object *unstructured.Unstructured) { object.Object["status"] = "invalid-status-value" },
	} {
		t.Run(name, func(t *testing.T) {
			source, request := sourceFixture(t)
			request.CreateOnly = true
			request.ExpectedDestinationUID = "reviewed-destination"
			payload, err := BuildImport(t.Context(), sourceClient(t, source, nil, interceptor.Funcs{}), request)
			require.NoError(t, err)
			request.ExpectedImportDigest = payload.Digest
			destination := payload.Object.DeepCopy()
			destination.SetUID(request.ExpectedDestinationUID)
			destination.SetResourceVersion("9")
			edit(destination)
			c := sourceClient(t, source, []client.Object{destination}, interceptor.Funcs{
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					t.Fatal("create-only must not replace a drifted destination")
					return nil
				},
				SubResourcePatch: func(context.Context, client.Client, string, client.Object,
					client.Patch, ...client.SubResourcePatchOption,
				) error {
					t.Fatal("create-only must not overwrite a drifted destination status")
					return nil
				},
			})
			_, err = Import(t.Context(), c, request)
			require.Error(t, err)
		})
	}
}

func TestImportCreateOnlyStillRequiresReviewedDigest(t *testing.T) {
	source, request := sourceFixture(t)
	request.CreateOnly = true
	request.ExpectedImportDigest = strings.Repeat("0", 64)
	c := sourceClient(t, source, nil, interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			t.Fatal("create-only must not write without the matching reviewed digest")
			return nil
		},
	})
	_, err := Import(t.Context(), c, request)
	require.ErrorContains(t, err, "digest differs")
}
