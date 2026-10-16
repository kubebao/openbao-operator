package networking

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func newHTTPRouteNoKindMatchError() error {
	return &meta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"},
		SearchedVersions: []string{"v1"},
	}
}

func newGatewayOptionalResourceOptions(degradeOnCRDMissing bool, applyErr error) optionalResourceOptions {
	return optionalResourceOptions{
		kind:                "HTTPRoute",
		apiVersion:          "gateway.networking.k8s.io/v1",
		enabled:             true,
		name:                types.NamespacedName{Namespace: "default", Name: "x"},
		logger:              logr.Discard(),
		degradeOnCRDMissing: degradeOnCRDMissing,
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
			}, true, nil
		},
		get:    func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error { return nil },
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error { return nil },
		apply:  func(_ context.Context, _ client.Object) error { return applyErr },
	}
}

func TestReconcileOptionalResource_Disabled_NoopsWhenNotFound(t *testing.T) {
	t.Parallel()

	var getCalls, deleteCalls, applyCalls int

	opts := optionalResourceOptions{
		kind:              "ConfigMap",
		apiVersion:        "v1",
		enabled:           false,
		name:              types.NamespacedName{Namespace: "default", Name: "x"},
		logger:            logr.Discard(),
		deleteDisabledMsg: "disabled; deleting",
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			t.Fatalf("buildDesired should not be called")
			return nil, false, nil
		},
		get: func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			getCalls++
			return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "configmaps"}, "x")
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return nil
		},
		apply: func(_ context.Context, _ client.Object) error {
			applyCalls++
			return nil
		},
	}

	if err := reconcileOptionalResource(context.Background(), opts); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if getCalls != 1 || deleteCalls != 0 || applyCalls != 0 {
		t.Fatalf("unexpected calls: get=%d delete=%d apply=%d", getCalls, deleteCalls, applyCalls)
	}
}

func TestReconcileOptionalResource_Disabled_DeletesWhenPresent(t *testing.T) {
	t.Parallel()

	var getCalls, deleteCalls, applyCalls int

	opts := optionalResourceOptions{
		kind:              "ConfigMap",
		apiVersion:        "v1",
		enabled:           false,
		name:              types.NamespacedName{Namespace: "default", Name: "x"},
		logger:            logr.Discard(),
		deleteDisabledMsg: "disabled; deleting",
		logKey:            "configmap",
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			t.Fatalf("buildDesired should not be called")
			return nil, false, nil
		},
		get: func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			getCalls++
			return nil
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return nil
		},
		apply: func(_ context.Context, _ client.Object) error {
			applyCalls++
			return nil
		},
	}

	if err := reconcileOptionalResource(context.Background(), opts); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if getCalls != 1 || deleteCalls != 1 || applyCalls != 0 {
		t.Fatalf("unexpected calls: get=%d delete=%d apply=%d", getCalls, deleteCalls, applyCalls)
	}
}

func TestReconcileOptionalResource_DisabledRejectsUnownedExistingResource(t *testing.T) {
	t.Parallel()

	var deleteCalls int
	owner := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default", UID: types.UID("cluster-uid")},
	}

	opts := optionalResourceOptions{
		kind:              "ConfigMap",
		apiVersion:        "v1",
		enabled:           false,
		name:              types.NamespacedName{Namespace: "default", Name: "cluster-config"},
		owner:             owner,
		logger:            logr.Discard(),
		deleteDisabledMsg: "disabled; deleting",
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			t.Fatalf("buildDesired should not be called")
			return nil, false, nil
		},
		get: func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
			obj.SetName("cluster-config")
			obj.SetNamespace("default")
			return nil
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return nil
		},
		apply: func(_ context.Context, _ client.Object) error { return nil },
	}

	err := reconcileOptionalResource(context.Background(), opts)
	if err == nil {
		t.Fatal("expected owner proof error, got nil")
	}
	if !strings.Contains(err.Error(), "requires OpenBaoCluster owner proof") {
		t.Fatalf("error = %q, want owner proof error", err.Error())
	}
	if deleteCalls != 0 {
		t.Fatalf("deleteCalls = %d, want 0", deleteCalls)
	}
}

func TestReconcileOptionalResource_Enabled_InvalidConfig_DeletesWhenPresent(t *testing.T) {
	t.Parallel()

	var getCalls, deleteCalls, applyCalls int

	opts := optionalResourceOptions{
		kind:             "ConfigMap",
		apiVersion:       "v1",
		enabled:          true,
		name:             types.NamespacedName{Namespace: "default", Name: "x"},
		logger:           logr.Discard(),
		deleteInvalidMsg: "invalid; deleting",
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			return nil, false, nil
		},
		get: func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			getCalls++
			return nil
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return nil
		},
		apply: func(_ context.Context, _ client.Object) error {
			applyCalls++
			return nil
		},
	}

	if err := reconcileOptionalResource(context.Background(), opts); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if getCalls != 1 || deleteCalls != 1 || applyCalls != 0 {
		t.Fatalf("unexpected calls: get=%d delete=%d apply=%d", getCalls, deleteCalls, applyCalls)
	}
}

func TestReconcileOptionalResource_Enabled_ValidConfig_Applies(t *testing.T) {
	t.Parallel()

	var getCalls, deleteCalls, applyCalls int

	opts := optionalResourceOptions{
		kind:       "ConfigMap",
		apiVersion: "v1",
		enabled:    true,
		name:       types.NamespacedName{Namespace: "default", Name: "x"},
		logger:     logr.Discard(),
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "x",
					Namespace: "default",
				},
			}, true, nil
		},
		get: func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			getCalls++
			return nil
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return nil
		},
		apply: func(_ context.Context, obj client.Object) error {
			applyCalls++
			got := obj.GetObjectKind().GroupVersionKind()
			want := schema.FromAPIVersionAndKind("v1", "ConfigMap")
			if got != want {
				t.Fatalf("unexpected GVK: got=%v want=%v", got, want)
			}
			return nil
		},
	}

	if err := reconcileOptionalResource(context.Background(), opts); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if getCalls != 0 || deleteCalls != 0 || applyCalls != 1 {
		t.Fatalf("unexpected calls: get=%d delete=%d apply=%d", getCalls, deleteCalls, applyCalls)
	}
}

func TestReconcileOptionalResource_Enabled_BuildError_Propagates(t *testing.T) {
	t.Parallel()

	buildErr := errors.New("boom")

	var getCalls, deleteCalls, applyCalls int

	opts := optionalResourceOptions{
		kind:       "ConfigMap",
		apiVersion: "v1",
		enabled:    true,
		name:       types.NamespacedName{Namespace: "default", Name: "x"},
		logger:     logr.Discard(),
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			return nil, false, buildErr
		},
		get: func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			getCalls++
			return nil
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return nil
		},
		apply: func(_ context.Context, _ client.Object) error {
			applyCalls++
			return nil
		},
	}

	err := reconcileOptionalResource(context.Background(), opts)
	if !errors.Is(err, buildErr) {
		t.Fatalf("expected %v, got %v", buildErr, err)
	}
	if getCalls != 0 || deleteCalls != 0 || applyCalls != 0 {
		t.Fatalf("unexpected calls: get=%d delete=%d apply=%d", getCalls, deleteCalls, applyCalls)
	}
}

func TestReconcileOptionalResource_CRDMissingOnApply_DegradesWhenConfigured(t *testing.T) {
	t.Parallel()

	crdMissingErr := newHTTPRouteNoKindMatchError()
	err := reconcileOptionalResource(context.Background(), newGatewayOptionalResourceOptions(true, crdMissingErr))
	if !errors.Is(err, ErrGatewayAPIMissing) {
		t.Fatalf("expected ErrGatewayAPIMissing, got %T: %v", err, err)
	}
}

func TestReconcileOptionalResource_CRDMissingOnApply_CanBeIgnored(t *testing.T) {
	t.Parallel()

	opts := newGatewayOptionalResourceOptions(false, newHTTPRouteNoKindMatchError())
	opts.ignoreCRDMissing = true

	if err := reconcileOptionalResource(context.Background(), opts); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestReconcileOptionalResource_CRDMissingOnApply_DoesNotDegradeWhenDisabled(t *testing.T) {
	t.Parallel()

	crdMissingErr := newHTTPRouteNoKindMatchError()

	opts := optionalResourceOptions{
		kind:       "HTTPRoute",
		apiVersion: "gateway.networking.k8s.io/v1",
		enabled:    true,
		name:       types.NamespacedName{Namespace: "default", Name: "x"},
		logger:     logr.Discard(),
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
			}, true, nil
		},
		get:    func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error { return nil },
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error { return nil },
		apply:  func(_ context.Context, _ client.Object) error { return crdMissingErr },
	}

	err := reconcileOptionalResource(context.Background(), opts)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if errors.Is(err, ErrGatewayAPIMissing) {
		t.Fatalf("did not expect ErrGatewayAPIMissing, got %T: %v", err, err)
	}
}

func TestReconcileOptionalResource_CRDMissingOnDelete_IsIgnored(t *testing.T) {
	t.Parallel()

	crdMissingErr := errors.New("could not find the requested resource")

	var getCalls, deleteCalls int

	opts := optionalResourceOptions{
		kind:              "HTTPRoute",
		apiVersion:        "gateway.networking.k8s.io/v1",
		enabled:           false,
		name:              types.NamespacedName{Namespace: "default", Name: "x"},
		logger:            logr.Discard(),
		deleteDisabledMsg: "disabled; deleting",
		newEmpty: func() client.Object {
			return &corev1.ConfigMap{}
		},
		buildDesired: func() (client.Object, bool, error) {
			t.Fatalf("buildDesired should not be called")
			return nil, false, nil
		},
		get: func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			getCalls++
			return nil
		},
		delete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			deleteCalls++
			return crdMissingErr
		},
		apply: func(_ context.Context, _ client.Object) error { return nil },
	}

	if err := reconcileOptionalResource(context.Background(), opts); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if getCalls != 1 || deleteCalls != 1 {
		t.Fatalf("unexpected calls: get=%d delete=%d", getCalls, deleteCalls)
	}
}

func TestReconcileOptionalResource_NotFoundOnApply_DegradesWhenConfigured(t *testing.T) {
	t.Parallel()

	notFoundErr := apierrors.NewNotFound(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "x")
	err := reconcileOptionalResource(context.Background(), newGatewayOptionalResourceOptions(true, notFoundErr))
	if !errors.Is(err, ErrGatewayAPIMissing) {
		t.Fatalf("expected ErrGatewayAPIMissing, got %T: %v", err, err)
	}
}
