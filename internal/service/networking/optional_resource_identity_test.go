package networking

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestOptionalResourceDeletionPinsObservedUID(t *testing.T) {
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default", UID: "owner-uid"}}
	deleted := false
	err := reconcileOptionalResource(t.Context(), optionalResourceOptions{
		kind: "ConfigMap", apiVersion: "v1", owner: owner, logger: logr.Discard(),
		name:     types.NamespacedName{Namespace: "default", Name: "optional"},
		newEmpty: func() client.Object { return &corev1.ConfigMap{} },
		get: func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
			obj.SetUID("observed-uid")
			obj.SetAnnotations(map[string]string{constants.AnnotationOpenBaoOwnerUID: string(owner.UID)})
			return nil
		},
		delete: func(_ context.Context, _ client.Object, opts ...client.DeleteOption) error {
			options := (&client.DeleteOptions{}).ApplyOptions(opts)
			require.NotNil(t, options.Preconditions)
			require.NotNil(t, options.Preconditions.UID)
			require.EqualValues(t, "observed-uid", *options.Preconditions.UID)
			deleted = true
			return nil
		},
		apply: func(context.Context, client.Object) error { return nil },
	})
	require.NoError(t, err)
	require.True(t, deleted)
}
