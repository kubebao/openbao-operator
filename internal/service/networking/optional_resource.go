package networking

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

type optionalResourceOptions struct {
	kind       string
	apiVersion string

	enabled bool
	name    types.NamespacedName
	owner   client.Object

	logger logr.Logger

	logKey string

	deleteDisabledMsg string
	deleteInvalidMsg  string

	newEmpty     func() client.Object
	buildDesired func() (client.Object, bool, error)

	get    func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
	delete func(context.Context, client.Object, ...client.DeleteOption) error
	apply  func(context.Context, client.Object) error

	degradeOnCRDMissing bool
	ignoreCRDMissing    bool
}

func reconcileOptionalResource(ctx context.Context, opts optionalResourceOptions) error {
	if opts.get == nil || opts.delete == nil || opts.apply == nil {
		return fmt.Errorf("optional %s %s/%s: missing get/delete/apply functions", opts.kind, opts.name.Namespace, opts.name.Name)
	}

	deleteIfExists := func(msg string) error {
		empty := opts.newEmpty()
		if empty == nil {
			return fmt.Errorf("optional %s %s/%s: newEmpty returned nil", opts.kind, opts.name.Namespace, opts.name.Name)
		}

		if err := opts.get(ctx, opts.name, empty); err != nil {
			if operatorerrors.IsCRDMissingError(err) || apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get %s %s/%s: %w", opts.kind, opts.name.Namespace, opts.name.Name, err)
		}
		if opts.owner != nil {
			if err := resourceownership.RequireOwnerProof("delete optional "+opts.kind, empty, opts.owner); err != nil {
				return err
			}
		}

		if msg != "" {
			if opts.logKey != "" {
				opts.logger.Info(msg, opts.logKey, opts.name.Name)
			} else {
				opts.logger.Info(msg, "name", opts.name.Name)
			}
		}

		uid := empty.GetUID()
		if err := opts.delete(ctx, empty, client.Preconditions{UID: &uid}); err != nil {
			if operatorerrors.IsCRDMissingError(err) || apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to delete %s %s/%s: %w", opts.kind, opts.name.Namespace, opts.name.Name, err)
		}

		return nil
	}

	if !opts.enabled {
		return deleteIfExists(opts.deleteDisabledMsg)
	}

	desired, valid, err := opts.buildDesired()
	if err != nil {
		return err
	}
	if !valid || desired == nil {
		return deleteIfExists(opts.deleteInvalidMsg)
	}

	gvk := schema.FromAPIVersionAndKind(opts.apiVersion, opts.kind)
	desired.GetObjectKind().SetGroupVersionKind(gvk)

	if err := opts.apply(ctx, desired); err != nil {
		if opts.ignoreCRDMissing && (operatorerrors.IsCRDMissingError(err) || apierrors.IsNotFound(err)) {
			opts.logger.V(1).Info("Optional resource CRD missing; skipping reconciliation", "kind", opts.kind, "name", opts.name.Name)
			return nil
		}
		if opts.degradeOnCRDMissing && (operatorerrors.IsCRDMissingError(err) || apierrors.IsNotFound(err)) {
			return ErrGatewayAPIMissing
		}
		return fmt.Errorf("failed to ensure %s %s/%s: %w", opts.kind, opts.name.Namespace, opts.name.Name, err)
	}

	return nil
}
