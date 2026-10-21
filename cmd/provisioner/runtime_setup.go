package provisioner

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	appprovisioner "github.com/kubebao/openbao-operator/internal/app/provisioner"
	provisionercontroller "github.com/kubebao/openbao-operator/internal/controller/provisioner"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/entrypoint"
)

type provisionerProcessRuntime struct {
	provisioner       appprovisioner.Provisioner
	operatorNamespace string
	admissionTracker  *admission.Tracker
}

func buildProvisionerProcessRuntime(
	ctx context.Context, mgr ctrl.Manager, cfg runConfig,
) (provisionerProcessRuntime, error) {
	provisionerRuntime, err := appprovisioner.NewProvisioner(appprovisioner.ProvisionerDependencies{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Logger:    setupLog.WithName("provisioner"),
	})
	if err != nil {
		return provisionerProcessRuntime{}, fmt.Errorf("unable to create provisioner runtime: %w", err)
	}

	admissionTracker, err := initializeAdmissionTracker(ctx, mgr.GetAPIReader(), cfg)
	if err != nil {
		return provisionerProcessRuntime{}, err
	}
	if cfg.admissionCanary && cfg.admissionEnforcement == entrypoint.AdmissionEnforcementFail &&
		!admission.UnsafeAdmissionDisabled() {
		if err := verifyAdmissionCanary(ctx, mgr.GetConfig()); err != nil {
			return provisionerProcessRuntime{}, err
		}
	}

	return provisionerProcessRuntime{
		provisioner:       provisionerRuntime,
		operatorNamespace: operatorNamespaceFromEnv(),
		admissionTracker:  admissionTracker,
	}, nil
}

func setupControllers(mgr ctrl.Manager, runtime provisionerProcessRuntime) error {
	if err := (&provisionercontroller.NamespaceProvisionerReconciler{
		Client:            mgr.GetClient(),
		APIReader:         mgr.GetAPIReader(),
		Recorder:          mgr.GetEventRecorder("namespace-provisioner"),
		Provisioner:       runtime.provisioner,
		OperatorNamespace: runtime.operatorNamespace,
		AdmissionTracker:  runtime.admissionTracker,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller NamespaceProvisioner: %w", err)
	}

	if err := (&provisionercontroller.TenantSecretsRBACReconciler{
		Client:           mgr.GetClient(),
		AdmissionTracker: runtime.admissionTracker,
		APIReader:        mgr.GetAPIReader(),
		Recorder:         mgr.GetEventRecorder("namespace-provisioner-tenant-secrets"),
		Provisioner:      runtime.provisioner,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller TenantSecretsRBAC: %w", err)
	}

	return nil
}

func addManagerHealthChecks(ctx context.Context, mgr ctrl.Manager) error {
	return entrypoint.AddManagerHealthChecks(ctx, mgr,
		&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoTenant{}, &openbaov1alpha1.OpenBaoRestore{},
	)
}
