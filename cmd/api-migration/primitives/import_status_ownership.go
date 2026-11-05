package primitives

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const importStatusFieldManager = "kubebao-api-migration-status-import"

// importStatusOwnership identifies only this importer's status ownership. The
// controllers must remain stopped: another status manager is evidence of drift.
func importStatusOwnership(object *unstructured.Unstructured) ([]metav1.ManagedFieldsEntry, bool, error) {
	retained := make([]metav1.ManagedFieldsEntry, 0, len(object.GetManagedFields()))
	pending := false
	for _, entry := range object.GetManagedFields() {
		if entry.Subresource != "status" {
			retained = append(retained, entry)
			continue
		}
		if entry.Manager != importStatusFieldManager || entry.Operation != metav1.ManagedFieldsOperationUpdate ||
			entry.APIVersion != PlanDestinationAPIVersion {
			return nil, false, fmt.Errorf("destination has unexpected status field ownership; refusing import")
		}
		pending = true
	}
	return retained, pending, nil
}

// Release only the importer's status field ownership after seeding the exact
// source-derived status. Otherwise the normal controllers' SSA updates conflict
// when those values change. The main resource is required: Kubernetes ignores
// managedFields edits submitted through the status subresource.
func importReleaseStatusOwnership(ctx context.Context, c client.Client, request ImportRequest,
	payload *ImportPayload, destination *unstructured.Unstructured, beforeWrite func(context.Context) error,
) (ImportResult, error) {
	result := ImportResult{Destination: identity(PlanDestinationAPIVersion, request.Source.Kind, destination)}
	retained, pending, err := importStatusOwnership(destination)
	if err != nil {
		return result, err
	}
	if !pending {
		result.StatusImported = true
		return result, nil
	}
	if request.CreateOnly {
		return result, nil
	}
	if err := beforeWrite(ctx); err != nil {
		return result, err
	}
	patch, err := json.Marshal([]transferPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: string(destination.GetUID())},
		{Op: "test", Path: "/metadata/resourceVersion", Value: destination.GetResourceVersion()},
		{Op: "replace", Path: "/metadata/managedFields", Value: retained},
	})
	if err != nil {
		return result, fmt.Errorf("encode imported status ownership release: %w", err)
	}
	if err := c.Patch(ctx, destination, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		return result, fmt.Errorf("status ownership release requires inspection before resume: %w", err)
	}
	observed := payload.Object.DeepCopy()
	if err := c.Get(ctx, client.ObjectKeyFromObject(observed), observed); err != nil {
		return result, fmt.Errorf("read imported status ownership: %w", err)
	}
	request.ExpectedDestinationUID = result.Destination.UID
	if err := importMatchDestination(observed, request, payload); err != nil {
		return result, err
	}
	status, _, err := unstructured.NestedMap(observed.Object, "status")
	if err != nil || !reflect.DeepEqual(status, payload.Status) {
		return result, fmt.Errorf("imported status changed during field ownership release")
	}
	_, pending, err = importStatusOwnership(observed)
	if err != nil {
		return result, err
	}
	if pending {
		return result, fmt.Errorf("imported status ownership release was not observed")
	}
	result.Destination = identity(PlanDestinationAPIVersion, request.Source.Kind, observed)
	result.StatusImported = true
	return result, nil
}

// ImportStatusPending reports whether the importer still owns status fields,
// meaning the status import has not been completed.
func ImportStatusPending(object *unstructured.Unstructured) bool {
	for _, entry := range object.GetManagedFields() {
		if entry.Manager == importStatusFieldManager && entry.Subresource == "status" {
			return true
		}
	}
	return false
}
