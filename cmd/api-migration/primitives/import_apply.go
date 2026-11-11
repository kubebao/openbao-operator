package primitives

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// importApplyPayload is shared by source-derived import builders. The callback
// rechecks every source and reference pin immediately before each API write.
func importApplyPayload(ctx context.Context, c client.Client, request ImportRequest,
	payload *ImportPayload, beforeWrite func(context.Context) error,
) (ImportResult, error) {
	result := ImportResult{}
	destination := payload.Object.DeepCopy()
	err := c.Get(ctx, client.ObjectKeyFromObject(destination), destination)
	if apierrors.IsNotFound(err) {
		if request.ExpectedDestinationUID != "" {
			return result, fmt.Errorf("recorded import destination is absent; refusing replacement")
		}
		if err := beforeWrite(ctx); err != nil {
			return result, err
		}
		destination = payload.Object.DeepCopy()
		if err := c.Create(ctx, destination); err != nil {
			return importInspectCreate(ctx, c, request, payload, err)
		}
	} else if err != nil {
		return result, fmt.Errorf("read import destination: %w", err)
	}
	if err := importMatchDestination(destination, request, payload); err != nil {
		return result, err
	}
	result.Destination = identity(PlanDestinationAPIVersion, request.Source.Kind, destination)
	if payload.Status == nil {
		result.StatusImported = true
		return result, nil
	}
	current, _, err := unstructured.NestedMap(destination.Object, "status")
	if err != nil {
		return result, fmt.Errorf("destination status is malformed")
	}
	if reflect.DeepEqual(current, payload.Status) {
		return importReleaseStatusOwnership(ctx, c, request, payload, destination, beforeWrite)
	}
	if len(current) != 0 {
		return result, fmt.Errorf("destination status drifted; refusing to overwrite it")
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
		{Op: "add", Path: "/status", Value: payload.Status},
	})
	if err != nil {
		return result, fmt.Errorf("encode imported status: %w", err)
	}
	if _, _, err := importStatusOwnership(destination); err != nil {
		return result, err
	}
	if err := c.Status().Patch(ctx, destination, client.RawPatch(types.JSONPatchType, patch),
		client.FieldOwner(importStatusFieldManager)); err != nil {
		return result, fmt.Errorf("status import result requires inspection before resume: %w", err)
	}
	observed := payload.Object.DeepCopy()
	if err := c.Get(ctx, client.ObjectKeyFromObject(observed), observed); err != nil {
		return result, fmt.Errorf("read imported status: %w", err)
	}
	request.ExpectedDestinationUID = result.Destination.UID
	if err := importMatchDestination(observed, request, payload); err != nil {
		return result, err
	}
	status, _, err := unstructured.NestedMap(observed.Object, "status")
	if err != nil || !reflect.DeepEqual(status, payload.Status) {
		return result, fmt.Errorf("imported status readback differs from the source-derived payload")
	}
	return importReleaseStatusOwnership(ctx, c, request, payload, observed, beforeWrite)
}
