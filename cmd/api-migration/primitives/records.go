package primitives

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// validateRestoreSource accepts only a finished restore record: its execution
// ended, and any administrator decision, managed restart, and target cleanup
// completed. The destination restore controller then does no further work for
// it, so the source Cluster UID recorded in its status is never acted on.
func validateRestoreSource(source *unstructured.Unstructured) error {
	if len(source.GetOwnerReferences()) != 0 {
		return fmt.Errorf("restore records with owner references are unsupported")
	}
	restore := &api.OpenBaoRestore{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(source.Object, restore); err != nil {
		return fmt.Errorf("source does not match the released restore schema")
	}
	status := restore.Status
	disposition := status.AdministratorDisposition
	switch status.Phase {
	case api.RestorePhaseCompleted, api.RestorePhaseFailed:
	case api.RestorePhaseUnknown:
		if disposition == "" {
			return fmt.Errorf("restore is not released: wait for its recovery, or acknowledge Resume or Abandon")
		}
	default:
		return fmt.Errorf("restore is still running; wait for it to finish")
	}
	if disposition == api.RestoreAdministratorResume && status.Restart != nil && status.Restart.CompletedAt == nil {
		return fmt.Errorf("restore's managed restart is still in progress")
	}
	if target := status.Target; target != nil && disposition != api.RestoreAdministratorAbandon {
		if target.Cleanup == api.RestoreTargetCleanupFailed {
			return fmt.Errorf("restore target cleanup failed; clean up, then acknowledge Abandon")
		}
		if restore.Spec.TargetLifecycle == api.RestoreTargetLifecycleDisposable &&
			target.Cleanup != api.RestoreTargetCleanupComplete {
			return fmt.Errorf("restore's disposable target is not cleaned up yet")
		}
	}
	return nil
}

// validateTenantSource accepts any Tenant that matches the released schema.
// Its provisioning is redone by the destination provisioner.
func validateTenantSource(source *unstructured.Unstructured) error {
	if len(source.GetOwnerReferences()) != 0 {
		return fmt.Errorf("tenants with owner references are unsupported")
	}
	tenant := &api.OpenBaoTenant{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(source.Object, tenant); err != nil {
		return fmt.Errorf("source does not match the released tenant schema")
	}
	if tenant.Spec.TargetNamespace == "" {
		return fmt.Errorf("tenant has no target namespace")
	}
	return nil
}
