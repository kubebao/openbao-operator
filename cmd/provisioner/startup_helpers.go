package provisioner

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/entrypoint"
	"github.com/kubebao/openbao-operator/internal/platform/logging"
)

func newManagerOptions(
	metricsServerOptions metricsserver.Options,
	probeAddr string,
	enableLeaderElection bool,
) ctrl.Options {
	return ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "openbao-provisioner-leader.openbao.org",
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{
					&corev1.ServiceAccount{},
					&corev1.Namespace{},
					&corev1.ResourceQuota{},
					&corev1.LimitRange{},
					&rbacv1.Role{},
					&rbacv1.RoleBinding{},
				},
			},
		},
	}
}

func initializeAdmissionTracker(
	ctx context.Context, reader client.Reader, cfg runConfig,
) (*admission.Tracker, error) {
	admissionTracker := admission.NewTracker(
		reader,
		admission.DefaultDependencies(),
		admission.DefaultNamePrefixes(),
		30*time.Second,
	)
	if admission.UnsafeAdmissionDisabled() {
		setupLog.Info(
			"UNSAFE MODE: admission policy enforcement disabled; " +
				"skipping dependency checks and allowing provisioning without guardrails",
		)
		logging.LogAuditEvent(setupLog, logging.EventAdmissionUnsafeModeEnabled, map[string]string{
			"component":             "provisioner",
			"admission_enforcement": cfg.admissionEnforcement,
		})
		admission.SetAdmissionDependenciesReady(true)
		admissionTracker.MarkReadyForUnsafeMode()
		return admissionTracker, nil
	}

	switch cfg.admissionEnforcement {
	case entrypoint.AdmissionEnforcementFail:
		setupLog.Info("Waiting for admission policy dependencies", "timeout", cfg.admissionStartupTimeout)
		status, err := admission.WaitForDependencies(
			ctx,
			reader,
			admission.DefaultDependencies(),
			admission.DefaultNamePrefixes(),
			cfg.admissionStartupTimeout,
			2*time.Second,
		)
		admission.SetAdmissionDependenciesReady(status.OverallReady)
		if !status.OverallReady {
			if err == nil {
				err = fmt.Errorf("admission policy dependencies not ready")
			}
			logging.LogAuditEvent(setupLog, logging.EventAdmissionStartupBlocked, map[string]string{
				"component":             "provisioner",
				"admission_enforcement": cfg.admissionEnforcement,
				"summary":               status.SummaryMessage(),
			})
			setupLog.Error(
				err,
				"Admission policy dependencies not ready; refusing to start",
				"summary",
				status.SummaryMessage(),
			)
			return nil, fmt.Errorf("admission policy dependencies not ready (%s): %w",
				status.SummaryMessage(), err)
		}
		admissionTracker.Set(status)
		setupLog.Info("Admission policy dependencies ready")
		logging.LogAuditEvent(setupLog, logging.EventAdmissionDependenciesReady, map[string]string{
			"component":             "provisioner",
			"admission_enforcement": cfg.admissionEnforcement,
		})

	default:
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		status := admission.Status{}
		checkedStatus, err := admission.CheckDependencies(
			checkCtx,
			reader,
			admission.DefaultDependencies(),
			admission.DefaultNamePrefixes(),
		)
		if err != nil {
			setupLog.Error(err, "Failed to evaluate admission policy dependencies; treating admission as not ready")
		} else {
			status = checkedStatus
		}
		admission.SetAdmissionDependenciesReady(status.OverallReady)
		admissionTracker.Set(status)
		if status.OverallReady {
			setupLog.Info("Admission policy dependencies ready")
			logging.LogAuditEvent(setupLog, logging.EventAdmissionDependenciesReady, map[string]string{
				"component":             "provisioner",
				"admission_enforcement": cfg.admissionEnforcement,
			})
		} else {
			setupLog.Info("Admission policy dependencies not ready", "summary", status.SummaryMessage())
			logging.LogAuditEvent(setupLog, logging.EventAdmissionDependenciesNotReady, map[string]string{
				"component":             "provisioner",
				"admission_enforcement": cfg.admissionEnforcement,
				"summary":               status.SummaryMessage(),
			})
		}
	}

	return admissionTracker, nil
}

func verifyAdmissionCanary(ctx context.Context, config *rest.Config) error {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logging.LogAuditEvent(setupLog, logging.EventAdmissionCanaryFailed, map[string]string{
			"component": "provisioner",
			"reason":    "clientset_creation_failed",
		})
		return fmt.Errorf("create Kubernetes clientset for admission canary: %w", err)
	}

	canaryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := admission.VerifyProvisionerRBACEnforcement(canaryCtx, clientset, "default"); err != nil {
		logging.LogAuditEvent(setupLog, logging.EventAdmissionCanaryFailed, map[string]string{
			"component": "provisioner",
			"reason":    "policy_not_enforced",
		})
		return fmt.Errorf("admission canary failed: %w", err)
	}

	setupLog.Info("Admission canary succeeded")
	logging.LogAuditEvent(setupLog, logging.EventAdmissionCanaryPassed, map[string]string{
		"component": "provisioner",
	})
	return nil
}

func operatorNamespaceFromEnv() string {
	operatorNS := os.Getenv("POD_NAMESPACE")
	if operatorNS == "" {
		operatorNS = os.Getenv("OPERATOR_NAMESPACE")
	}
	if operatorNS == "" {
		operatorNS = "openbao-operator-system"
	}

	return operatorNS
}
