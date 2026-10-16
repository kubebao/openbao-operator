package resourceapply

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

func ApplyOwned(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, obj client.Object) error {
	resolvedOwner, err := ResolveOwnerIdentity(ctx, c, owner)
	if err != nil {
		return err
	}
	if err := resourceownership.RequireOwnerUID(resolvedOwner); err != nil {
		return err
	}
	if err := EnsureOwnedResourceManageable(ctx, c, resolvedOwner, obj); err != nil {
		return err
	}
	if err := PrepareOwned(obj, resolvedOwner, scheme); err != nil {
		return err
	}
	response, err := ApplyObserved(ctx, c, obj, "")
	if err != nil {
		return err
	}
	if hasAppliedOwnerProof(response, resolvedOwner) {
		return nil
	}
	return EnsureOwnedResourceProofStamped(ctx, c, scheme, resolvedOwner, response)
}

func ResolveOwnerIdentity(ctx context.Context, c client.Client, owner client.Object) (client.Object, error) {
	if owner == nil || owner.GetUID() != "" || c == nil {
		return owner, nil
	}
	liveOwner, ok := owner.DeepCopyObject().(client.Object)
	if !ok {
		return owner, nil
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(owner), liveOwner); err != nil {
		if apierrors.IsNotFound(err) {
			return owner, nil
		}
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return nil, operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to resolve owner identity %s/%s: %w", owner.GetNamespace(), owner.GetName(), err))
		}
		return nil, fmt.Errorf("failed to resolve owner identity %s/%s: %w", owner.GetNamespace(), owner.GetName(), err)
	}
	if liveOwner.GetUID() == "" {
		return owner, nil
	}
	return liveOwner, nil
}

// EnsureOwnedResourceManageable checks ownership and records the observed UID
// and resource version on obj. ApplyObserved uses that identity for the write.
// An absent object has both fields cleared and must be created without an upsert.
func EnsureOwnedResourceManageable(ctx context.Context, c client.Client, owner client.Object, obj client.Object) error {
	if c == nil {
		return fmt.Errorf("client is required")
	}
	if owner == nil {
		return fmt.Errorf("owner is required")
	}
	if obj == nil {
		return fmt.Errorf("object is required")
	}
	existing, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("object %T does not implement client.Object", obj)
	}
	prepareObjectForGet(existing, obj)
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		if apierrors.IsNotFound(err) {
			obj.SetUID("")
			obj.SetResourceVersion("")
			obj.SetManagedFields(nil)
			return nil
		}
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to check existing resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
		return fmt.Errorf("failed to check existing resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if err := resourceownership.RequireOwnerProof("manage", existing, owner); err != nil {
		return err
	}
	if existing.GetResourceVersion() == "" {
		return fmt.Errorf("observed resource %s/%s has no resource version", obj.GetNamespace(), obj.GetName())
	}
	obj.SetUID(existing.GetUID())
	obj.SetResourceVersion(existing.GetResourceVersion())
	obj.SetManagedFields(existing.GetManagedFields())
	return nil
}

func ApplyRetained(ctx context.Context, c client.Client, owner client.Object, obj client.Object) error {
	resolvedOwner, err := ResolveOwnerIdentity(ctx, c, owner)
	if err != nil {
		return err
	}
	if err := resourceownership.RequireOwnerUID(resolvedOwner); err != nil {
		return err
	}
	if err := resourceownership.SetOwnerUIDAnnotation(obj, resolvedOwner); err != nil {
		return err
	}
	if err := EnsureOwnedResourceManageable(ctx, c, resolvedOwner, obj); err != nil {
		return err
	}
	response, err := ApplyObserved(ctx, c, obj, "")
	if err != nil {
		return err
	}
	if hasAppliedOwnerProof(response, resolvedOwner) {
		return nil
	}
	return EnsureRetainedResourceProofStamped(ctx, c, resolvedOwner, response)
}

// Require server identity as well as owner proof before trusting the apply result.
// Clients that do not return a complete response retain the read-and-repair path.
func hasAppliedOwnerProof(response client.Object, owner client.Object) bool {
	return response.GetUID() != "" && response.GetResourceVersion() != "" &&
		resourceownership.HasOwnerProof(response, owner)
}

func PrepareOwned(obj client.Object, owner client.Object, scheme *runtime.Scheme) error {
	if scheme == nil {
		return fmt.Errorf("scheme is required")
	}
	if err := resourceownership.RequireOwnerUID(owner); err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}
	if err := resourceownership.SetOwnerUIDAnnotation(obj, owner); err != nil {
		return err
	}
	return nil
}

func EnsureOwnedResourceProofStamped(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, obj client.Object) error {
	existing, err := getExistingObject(ctx, c, obj)
	if err != nil {
		return err
	}
	if err := requireUnchangedIdentity(obj, existing); err != nil {
		return err
	}
	if resourceownership.HasOwnerProof(existing, owner) {
		return nil
	}

	before, ok := existing.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("object %T does not implement client.Object", existing)
	}
	if err := PrepareOwned(existing, owner, scheme); err != nil {
		return err
	}
	if obj.GetUID() == "" {
		return fmt.Errorf("cannot repair owner proof without the written resource UID")
	}
	if err := c.Patch(ctx, existing, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to stamp owner proof on resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
		return fmt.Errorf("failed to stamp owner proof on resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

func EnsureRetainedResourceProofStamped(ctx context.Context, c client.Client, owner client.Object, obj client.Object) error {
	existing, err := getExistingObject(ctx, c, obj)
	if err != nil {
		return err
	}
	if err := requireUnchangedIdentity(obj, existing); err != nil {
		return err
	}
	if resourceownership.HasOwnerProof(existing, owner) {
		return nil
	}

	before, ok := existing.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("object %T does not implement client.Object", existing)
	}
	if err := resourceownership.SetOwnerUIDAnnotation(existing, owner); err != nil {
		return err
	}
	if obj.GetUID() == "" {
		return fmt.Errorf("cannot repair retained owner proof without the written resource UID")
	}
	if err := c.Patch(ctx, existing, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to stamp retained owner proof on resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
		return fmt.Errorf("failed to stamp retained owner proof on resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

func getExistingObject(ctx context.Context, c client.Client, obj client.Object) (client.Object, error) {
	if c == nil {
		return nil, fmt.Errorf("client is required")
	}
	if obj == nil {
		return nil, fmt.Errorf("object is required")
	}
	existing, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return nil, fmt.Errorf("object %T does not implement client.Object", obj)
	}
	prepareObjectForGet(existing, obj)
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return nil, operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to get resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
		return nil, fmt.Errorf("failed to get resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return existing, nil
}

func prepareObjectForGet(existing client.Object, obj client.Object) {
	gvk := existing.GetObjectKind().GroupVersionKind()
	existing.SetName(obj.GetName())
	existing.SetNamespace(obj.GetNamespace())
	existing.SetLabels(nil)
	existing.SetAnnotations(nil)
	existing.SetOwnerReferences(nil)
	existing.SetManagedFields(nil)
	existing.SetUID("")
	existing.SetResourceVersion("")
	existing.GetObjectKind().SetGroupVersionKind(gvk)
}

func requireUnchangedIdentity(expected, current client.Object) error {
	if expected.GetUID() != "" && current.GetUID() != expected.GetUID() {
		return operatorerrors.WrapTransientClusterState(fmt.Errorf("resource %s/%s was replaced after the write", expected.GetNamespace(), expected.GetName()))
	}
	return nil
}
