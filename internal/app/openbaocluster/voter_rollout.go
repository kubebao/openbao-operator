package openbaocluster

import (
	"context"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

type voterRolloutReconciler struct {
	manager *workloadsvc.Manager
}

func (r *voterRolloutReconciler) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	return r.manager.ReconcileVoterRollout(ctx, logger, cluster)
}
