package resourceapply

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	"github.com/kubebao/openbao-operator/internal/adapter/kube"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

// ApplyObserved creates an object observed as absent or applies to its recorded
// UID and resource version. Call EnsureOwnedResourceManageable before this write
// when ownership is required. Conflicts require a fresh observation and check.
func ApplyObserved(ctx context.Context, c client.Client, obj client.Object, fieldOwner string) (client.Object, error) {
	if fieldOwner == "" {
		fieldOwner = constants.FieldOwnerOpenBaoOperator
	}
	if obj.GetResourceVersion() == "" {
		if obj.GetUID() != "" {
			return nil, fmt.Errorf("cannot update resource %s/%s without its observed resource version", obj.GetNamespace(), obj.GetName())
		}
		if err := c.Create(ctx, obj, client.FieldOwner(fieldOwner)); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil, operatorerrors.WrapTransientClusterState(fmt.Errorf("resource %s/%s appeared after observing absence: %w", obj.GetNamespace(), obj.GetName(), err))
			}
			return nil, fmt.Errorf("failed to create resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
		if err := promoteCreateOwnership(ctx, c, obj, fieldOwner); err != nil {
			return nil, err
		}
		return obj, nil
	}
	if err := promoteCreateOwnership(ctx, c, obj, fieldOwner); err != nil {
		return nil, err
	}
	applyConfig, response, err := kube.ToApplyConfigurationWithResponse(obj, c)
	if err != nil {
		return nil, fmt.Errorf("failed to convert object to ApplyConfiguration: %w", err)
	}
	if err := c.Apply(ctx, applyConfig, client.ForceOwnership, client.FieldOwner(fieldOwner)); err != nil {
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) {
			return nil, operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("failed to apply resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
		return nil, fmt.Errorf("failed to apply resource %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	return response, nil
}

// Create records Update ownership. Promote only this manager's fields to Apply
// ownership so later SSA omissions remove fields created by the operator.
// Retrying this step also recovers a reconcile interrupted after Create.
func promoteCreateOwnership(ctx context.Context, c client.Client, obj client.Object, fieldOwner string) error {
	defer obj.SetManagedFields(nil)
	entries := obj.GetManagedFields()
	var source *metav1.ManagedFieldsEntry
	for i := range entries {
		entry := &entries[i]
		if entry.Manager == fieldOwner && entry.Subresource == "" && entry.Operation == metav1.ManagedFieldsOperationUpdate {
			source = entry
			break
		}
	}
	if source == nil {
		return nil
	}
	fields := fieldpath.NewSet()
	var promoted metav1.ManagedFieldsEntry
	var retained []metav1.ManagedFieldsEntry
	for _, entry := range entries {
		if entry.Manager != fieldOwner || entry.Subresource != "" || entry.APIVersion != source.APIVersion {
			retained = append(retained, entry)
			continue
		}
		set := fieldpath.NewSet()
		if entry.FieldsV1 == nil {
			return fmt.Errorf("resource %s/%s has incomplete field ownership", obj.GetNamespace(), obj.GetName())
		}
		if err := set.FromJSON(entry.FieldsV1.GetRawReader()); err != nil {
			return fmt.Errorf("failed to decode field ownership for %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
		fields = fields.Union(set)
		promoted = entry
	}
	raw, err := fields.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to encode field ownership: %w", err)
	}
	promoted.Operation = metav1.ManagedFieldsOperationApply
	promoted.FieldsV1 = metav1.NewFieldsV1(string(raw))
	retained = append(retained, promoted)
	patch, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{
		"uid": obj.GetUID(), "resourceVersion": obj.GetResourceVersion(), "managedFields": retained,
	}})
	if err != nil {
		return fmt.Errorf("failed to encode field ownership patch: %w", err)
	}
	response := obj.DeepCopyObject().(client.Object)
	if err := c.Patch(ctx, response, client.RawPatch(types.MergePatchType, patch)); err != nil {
		wrapped := fmt.Errorf("failed to promote create field ownership for %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		if operatorerrors.IsTransientKubernetesAPI(err) || apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return operatorerrors.WrapTransientKubernetesAPI(wrapped)
		}
		return wrapped
	}
	obj.SetResourceVersion(response.GetResourceVersion())
	return nil
}
