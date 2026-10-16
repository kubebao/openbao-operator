package resourceapply

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func TestApplyResponseFallbackErrors(t *testing.T) {
	failure := apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "cfg", errors.New("conflict"))
	for _, retained := range []bool{false, true} {
		for _, stage := range []string{"apply", "read", "repair", "replacement"} {
			t.Run(fmt.Sprintf("retained=%t/%s", retained, stage), func(t *testing.T) {
				scheme := newTestScheme(t)
				owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default", UID: "owner-uid"}}
				obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"}}
				var gets, applies, patches int
				c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
					Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
						gets++
						if gets == 1 {
							obj.SetUID("object-uid")
							obj.SetResourceVersion("1")
							obj.SetAnnotations(map[string]string{"openbao.org/owner-uid": string(owner.UID)})
							return nil
						}
						if stage == "replacement" {
							obj.SetUID("replacement-uid")
							obj.SetResourceVersion("2")
							return nil
						}
						if stage == "read" {
							return failure
						}
						obj.SetUID("object-uid")
						obj.SetResourceVersion("2")
						return nil
					},
					Apply: func(_ context.Context, _ client.WithWatch, config runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
						applies++
						if stage == "apply" {
							return failure
						}
						// Simulate a client that omits ownership metadata in its response.
						response := config.(client.Object)
						response.SetAnnotations(nil)
						response.SetOwnerReferences(nil)
						return nil
					},
					Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
						patches++
						return failure
					},
				}).Build()
				var err error
				if retained {
					err = ApplyRetained(t.Context(), c, owner, obj)
				} else {
					err = ApplyOwned(t.Context(), c, scheme, owner, obj)
				}
				if stage == "replacement" {
					require.ErrorContains(t, err, "was replaced after the write")
				} else {
					require.ErrorIs(t, err, failure)
				}
				require.True(t, operatorerrors.IsTransient(err))
				require.Equal(t, 1, applies)
				if stage == "apply" {
					require.Equal(t, 1, gets)
				} else {
					require.Equal(t, 2, gets)
				}
				if stage == "repair" {
					require.Equal(t, 1, patches)
				} else {
					require.Zero(t, patches)
				}
			})
		}
	}
}
