package openbaocluster

import (
	"context"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/deletionops"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
)

// DeletionDependencies defines dependencies for OpenBaoCluster deletion orchestration.
type DeletionDependencies = deletionops.Dependencies

// HandleDeletion applies deletion policy side effects.
func HandleDeletion(ctx context.Context, logger logr.Logger, deps DeletionDependencies, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	if len(deps.RetentionSecrets) == 0 {
		deps.RetentionSecrets = deletionops.DefaultRetentionSecrets(cluster)
	}
	return deletionops.Handle(ctx, logger, deps, cluster)
}
