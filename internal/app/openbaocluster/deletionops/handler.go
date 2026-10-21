package deletionops

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/observability"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/workload"
)

// Dependencies contains external collaborators required for deletion orchestration.
type Dependencies struct {
	Client client.Client
	// APIReader supplies uncached execution custody and status observations.
	APIReader        client.Reader
	RetentionSecrets []string
}

// Handle applies the deletion policy for an OpenBaoCluster.
func Handle(ctx context.Context, logger logr.Logger, deps Dependencies, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	reader := deps.APIReader
	if reader == nil {
		reader = deps.Client
	}
	mutator := adminopsstatus.NewMutator(reader, deps.Client)
	if err := backup.CancelRetention(ctx, deps.Client, reader, mutator, cluster); err != nil {
		return recon.Result{}, err
	}
	if err := backup.CancelRestoreTest(ctx, reader, deps.Client, mutator, cluster); err != nil {
		return recon.Result{}, err
	}
	complete, err := workload.NewManager(deps.Client, nil, "").WithReader(reader).FinalizeDeletion(ctx, logger, cluster)
	if err != nil {
		return recon.Result{}, err
	}
	if !complete {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	policy := cluster.Spec.DeletionPolicy
	if policy == "" {
		policy = openbaov1alpha1.DeletionPolicyRetain
	}

	logger.Info("Applying DeletionPolicy for OpenBaoCluster", "deletionPolicy", string(policy))

	// CRITICAL: When DeletionPolicy is Retain, orphan required secrets before finalizer removal.
	if policy == openbaov1alpha1.DeletionPolicyRetain {
		retentionSecrets := deps.RetentionSecrets
		if len(retentionSecrets) == 0 {
			retentionSecrets = DefaultRetentionSecrets(cluster)
		}
		if err := OrphanRetentionSecrets(ctx, logger, deps.Client, cluster, retentionSecrets); err != nil {
			return recon.Result{}, fmt.Errorf("failed to orphan retention secrets: %w", err)
		}
	}

	// Clear per-cluster metrics to avoid stale series after deletion.
	clusterMetrics := observability.NewClusterMetrics(cluster.Namespace, cluster.Name)
	clusterMetrics.Clear()

	if err := Cleanup(ctx, logger, deps.Client, cluster, policy); err != nil {
		return recon.Result{}, err
	}

	// Backup deletion for DeletionPolicyDeleteAll will be implemented alongside BackupManager data path.
	return recon.Result{}, nil
}

// DefaultRetentionSecrets returns the generated secrets that must survive a retained cluster deletion.
func DefaultRetentionSecrets(cluster *openbaov1alpha1.OpenBaoCluster) []string {
	return []string{
		cluster.Name + constants.SuffixUnsealKey,
		cluster.Name + constants.SuffixRootToken,
	}
}
