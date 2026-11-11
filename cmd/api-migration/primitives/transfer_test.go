package primitives

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestTransferOwnershipPreservesCurrentDataOnReverse(t *testing.T) {
	request, resource, objects := transferFixture()
	c := transferClient(t, objects, interceptor.Funcs{})
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(resource), resource))
	require.Equal(t, request.Resource.UID, resource.UID)
	require.Equal(t, request.Destination.UID, resource.OwnerReferences[0].UID)
	require.Equal(t, "preserved", resource.Annotations["example.org/user"])
	require.True(t, *resource.OwnerReferences[0].BlockOwnerDeletion)
	// Application writes after handover must survive rollback; only metadata moves.
	resource.Data["value"] = "after-handover"
	require.NoError(t, c.Update(t.Context(), resource))
	request.Source, request.Destination = request.Destination, request.Source
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(resource), resource))
	require.Equal(t, "after-handover", resource.Data["value"])
	require.Equal(t, string(request.Destination.UID), resource.Annotations[constants.AnnotationOpenBaoOwnerUID])
}

func TestTransferOwnershipInspectsLostResponse(t *testing.T) {
	request, resource, objects := transferFixture()
	patches := 0
	c := transferClient(t, objects, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object,
			patch client.Patch, opts ...client.PatchOption,
		) error {
			patches++
			if err := c.Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			return errors.New("response lost after server commit")
		},
	})
	require.ErrorContains(t, TransferOwnership(t.Context(), c, request), "requires inspection")
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.Equal(t, 1, patches, "retry must inspect and must not repeat the write")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(resource), resource))
	require.Equal(t, request.Destination.UID, resource.OwnerReferences[0].UID)
}

func TestTransferOwnershipReversesAcceptedWriteWithoutForwardReceipt(t *testing.T) {
	request, resource, objects := transferFixture()
	patches := 0
	c := transferClient(t, objects, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object,
			patch client.Patch, opts ...client.PatchOption,
		) error {
			patches++
			if err := c.Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			if patches == 1 {
				return errors.New("forward response lost after server commit")
			}
			return nil
		},
	})
	require.ErrorContains(t, TransferOwnership(t.Context(), c, request), "requires inspection")
	// Reverse from the original nomination without replaying or completing the
	// forward operation. Current payload changes remain outside the transfer.
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(resource), resource))
	resource.Data["value"] = "after-interruption"
	require.NoError(t, c.Update(t.Context(), resource))
	request.Source, request.Destination = request.Destination, request.Source
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.Equal(t, 2, patches, "reverse retry observes the accepted ownership without another mutation")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(resource), resource))
	require.Equal(t, request.Destination.UID, resource.OwnerReferences[0].UID)
	require.Equal(t, string(request.Destination.UID), resource.Annotations[constants.AnnotationOpenBaoOwnerUID])
	require.Equal(t, "after-interruption", resource.Data["value"])
}

func TestTransferOwnershipRejectsUnprovenIdentity(t *testing.T) {
	const replacementUID = "replacement"
	tests := []struct {
		name string
		edit func(*TransferRequest, *corev1.ConfigMap)
	}{
		{"resource replaced", func(r *TransferRequest, _ *corev1.ConfigMap) { r.Resource.UID = replacementUID }},
		{"namespace replaced", func(r *TransferRequest, _ *corev1.ConfigMap) { r.NamespaceUID = replacementUID }},
		{"source replaced", func(r *TransferRequest, _ *corev1.ConfigMap) { r.Source.UID = replacementUID }},
		{"destination replaced", func(r *TransferRequest, _ *corev1.ConfigMap) { r.Destination.UID = replacementUID }},
		{"annotation alone", func(_ *TransferRequest, o *corev1.ConfigMap) { o.OwnerReferences = nil }},
		{"missing annotation", func(_ *TransferRequest, o *corev1.ConfigMap) {
			delete(o.Annotations, constants.AnnotationOpenBaoOwnerUID)
		}},
		{"foreign annotation", func(_ *TransferRequest, o *corev1.ConfigMap) {
			o.Annotations[constants.AnnotationOpenBaoOwnerUID] = "foreign"
		}},
		{"foreign controller", func(_ *TransferRequest, o *corev1.ConfigMap) { o.OwnerReferences[0].UID = "foreign" }},
		{"foreign controller kind", func(_ *TransferRequest, o *corev1.ConfigMap) {
			o.OwnerReferences[0].Kind = "OtherKind"
		}},
		{"extra OpenBao reference", func(_ *TransferRequest, o *corev1.ConfigMap) {
			o.OwnerReferences = append(o.OwnerReferences, metav1.OwnerReference{
				APIVersion: PlanSourceAPIVersion, Kind: "OpenBaoCluster", Name: "other", UID: "other"})
		}},
		{"incomplete destination proof", func(r *TransferRequest, o *corev1.ConfigMap) {
			o.OwnerReferences[0].UID, o.OwnerReferences[0].APIVersion = r.Destination.UID, r.Destination.APIVersion
			delete(o.Annotations, constants.AnnotationOpenBaoOwnerUID)
		}},
		{"retained config map", func(r *TransferRequest, _ *corev1.ConfigMap) { r.Retained = true }},
		{"unsupported job", func(r *TransferRequest, _ *corev1.ConfigMap) {
			r.Resource.Kind, r.Resource.APIVersion = "Job", testBatchAPIVersion
		}},
		{"cross namespace", func(r *TransferRequest, _ *corev1.ConfigMap) { r.Destination.Namespace = "other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, resource, objects := transferFixture()
			tt.edit(&request, resource)
			c := transferClient(t, objects, interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					t.Fatal("unproven ownership must not be mutated")
					return nil
				},
			})
			require.Error(t, TransferOwnership(t.Context(), c, request))
		})
	}
}

func TestTransferRetainedProofIsExplicit(t *testing.T) {
	request, resource, objects := transferFixture()
	secret := &corev1.Secret{ObjectMeta: *resource.ObjectMeta.DeepCopy(),
		Data: map[string][]byte{"value": []byte("preserved")}}
	secret.OwnerReferences = nil
	objects[len(objects)-1] = secret
	request.Resource.Kind = "Secret"
	c := transferClient(t, objects, interceptor.Funcs{})
	require.Error(t, TransferOwnership(t.Context(), c, request))
	request.Retained = true
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(secret), secret))
	require.Empty(t, secret.OwnerReferences, "retained resources must not acquire GC ownership")
	require.Equal(t, []byte("preserved"), secret.Data["value"])
}

func transferFixture() (TransferRequest, *corev1.ConfigMap, []client.Object) {
	r := TransferRequest{
		NamespaceUID: "namespace-uid",
		Source: TransferIdentity{APIVersion: PlanSourceAPIVersion, Kind: "OpenBaoCluster",
			Namespace: "test", Name: "bao", UID: "old"},
		Destination: TransferIdentity{APIVersion: PlanDestinationAPIVersion, Kind: "OpenBaoCluster",
			Namespace: "test", Name: "bao", UID: "new"},
		Resource: TransferIdentity{APIVersion: "v1", Kind: "ConfigMap",
			Namespace: "test", Name: "config", UID: "resource"},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test", UID: r.NamespaceUID}}
	objects := []client.Object{ns}
	for _, id := range []TransferIdentity{r.Source, r.Destination} {
		owner := &unstructured.Unstructured{}
		owner.SetAPIVersion(id.APIVersion)
		owner.SetKind(id.Kind)
		owner.SetNamespace(id.Namespace)
		owner.SetName(id.Name)
		owner.SetUID(id.UID)
		objects = append(objects, owner)
	}
	resource := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "config", UID: r.Resource.UID,
		Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: string(r.Source.UID),
			"example.org/user": "preserved"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: r.Source.APIVersion, Kind: r.Source.Kind,
			Name: r.Source.Name, UID: r.Source.UID,
			Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}}}, Data: map[string]string{"value": "before"}}
	return r, resource, append(objects, resource)
}

func transferClient(t *testing.T, objects []client.Object, funcs interceptor.Funcs) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(funcs).Build()
}
