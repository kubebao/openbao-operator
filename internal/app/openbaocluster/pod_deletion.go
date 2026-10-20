package openbaocluster

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

type terminalPodCleanupReconciler struct {
	manager *workloadsvc.Manager
}

// NewTerminalPodCleanupReconciler wires cleanup before workload creation and initialization.
func NewTerminalPodCleanupReconciler(c client.Client, reader client.Reader) SubReconciler {
	if reader == nil {
		return &terminalPodCleanupReconciler{}
	}
	return &terminalPodCleanupReconciler{manager: workloadsvc.NewManager(c, nil, "").WithReader(reader)}
}

func (r *terminalPodCleanupReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	if r.manager == nil {
		return recon.Result{}, nil
	}
	requested, err := r.manager.CompleteTerminatedPodDeletion(ctx, logger, cluster)
	if err != nil {
		logger.Error(err, "Failed to complete terminated Pod deletion")
		return recon.Result{}, nil
	}
	if requested {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}
	return recon.Result{}, nil
}
