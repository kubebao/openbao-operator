package resourceapply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func TestOwnershipCheckRecordsObservedIdentity(t *testing.T) {
	for _, exists := range []bool{false, true} {
		name := "absent"
		if exists {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default", UID: "owner-uid"}}
			builder := fake.NewClientBuilder().WithScheme(newTestScheme(t))
			if exists {
				builder.WithObjects(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: "cfg", Namespace: "default", UID: "observed-uid", ResourceVersion: "9",
					Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: string(owner.UID)},
				}})
			}
			desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: "cfg", Namespace: "default", UID: "stale-uid", ResourceVersion: "1",
			}}
			require.NoError(t, EnsureOwnedResourceManageable(t.Context(), builder.Build(), owner, desired))
			if exists {
				require.EqualValues(t, "observed-uid", desired.UID)
				require.Equal(t, "9", desired.ResourceVersion)
			} else {
				require.Empty(t, desired.UID)
				require.Empty(t, desired.ResourceVersion)
			}
		})
	}
}

func TestApplyObservedUsesCreateFieldOwner(t *testing.T) {
	for _, fieldOwner := range []string{"", "custom-owner"} {
		t.Run("fieldOwner="+fieldOwner, func(t *testing.T) {
			var manager string
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					manager = (&client.CreateOptions{}).ApplyOptions(opts).FieldManager
					return c.Create(ctx, obj, opts...)
				},
			}).Build()
			obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}
			_, err := ApplyObserved(t.Context(), c, obj, fieldOwner)
			require.NoError(t, err)
			if fieldOwner == "" {
				require.Equal(t, constants.FieldOwnerOpenBaoOperator, manager)
			} else {
				require.Equal(t, fieldOwner, manager)
			}
		})
	}
}

func TestApplyObservedRejectsIncompleteUpdateIdentity(t *testing.T) {
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default", UID: "observed-uid"}}
	_, err := ApplyObserved(t.Context(), nil, obj, "")
	require.ErrorContains(t, err, "without its observed resource version")
}

func TestPromoteCreateOwnership(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupted=%t", interrupted), func(t *testing.T) {
			obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: "cfg", Namespace: "default", UID: "observed-uid", ResourceVersion: "9",
				ManagedFields: []metav1.ManagedFieldsEntry{
					{Manager: "operator", Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "v1", FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:data":{"f:created":{}}}`)},
					{Manager: "operator", Operation: metav1.ManagedFieldsOperationApply, APIVersion: "v1", FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:data":{"f:applied":{}}}`)},
					{Manager: "other", Operation: metav1.ManagedFieldsOperationApply, APIVersion: "v1", FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:data":{"f:external":{}}}`)},
				},
			}, Data: map[string]string{"desired": "value"}}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, _ client.WithWatch, response client.Object, patch client.Patch, _ ...client.PatchOption) error {
					data, err := patch.Data(response)
					require.NoError(t, err)
					var written corev1.ConfigMap
					require.NoError(t, json.Unmarshal(data, &written))
					require.Equal(t, obj.UID, written.UID)
					require.Equal(t, "9", written.ResourceVersion)
					require.Nil(t, written.Data, "ownership migration must patch only metadata")
					require.Len(t, written.ManagedFields, 2)
					require.Equal(t, obj.ManagedFields[2], written.ManagedFields[0])
					promoted := written.ManagedFields[1]
					require.Equal(t, metav1.ManagedFieldsOperationApply, promoted.Operation)
					require.JSONEq(t, `{"f:data":{"f:created":{},"f:applied":{}}}`, promoted.FieldsV1.GetRawString())
					if interrupted {
						return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, obj.Name, errors.New("concurrent update"))
					}
					response.SetResourceVersion("10")
					return nil
				},
			}).Build()
			err := promoteCreateOwnership(t.Context(), c, obj, "operator")
			if interrupted {
				require.True(t, apierrors.IsConflict(err))
				require.True(t, operatorerrors.IsTransient(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, "10", obj.ResourceVersion)
			}
			require.Empty(t, obj.ManagedFields)
			require.Equal(t, map[string]string{"desired": "value"}, obj.Data)
		})
	}
}
