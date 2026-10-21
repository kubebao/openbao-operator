package deletionops

import (
	"context"
	"fmt"
	"slices"

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
	current, err := readDeletionAuthority(ctx, reader, cluster)
	if err != nil {
		return recon.Result{}, err
	}
	cluster = current
	policy := cluster.Spec.DeletionPolicy
	if policy == "" {
		policy = openbaov1alpha1.DeletionPolicyRetain
	}

	logger.Info("Applying DeletionPolicy for OpenBaoCluster", "deletionPolicy", string(policy))

	// Admission prevents GC from deleting these Secrets before this reconcile.
	// Orphan retained material before waiting for workload termination.
	if policy == openbaov1alpha1.DeletionPolicyRetain {
		retentionSecrets := deps.RetentionSecrets
		if len(retentionSecrets) == 0 {
			retentionSecrets = DefaultRetentionSecrets(cluster)
		}
		if err := OrphanRetentionSecrets(ctx, logger, deps.Client, cluster, retentionSecrets); err != nil {
			return recon.Result{}, fmt.Errorf("failed to orphan retention secrets: %w", err)
		}
	}
	complete, err := workload.NewManager(deps.Client, nil, "").WithReader(reader).FinalizeDeletion(ctx, logger, cluster)
	if err != nil {
		return recon.Result{}, err
	}
	if !complete {
		return recon.Result{RequeueAfter: constants.RequeueShort}, nil
	}

	// Clear per-cluster metrics to avoid stale series after deletion.
	clusterMetrics := observability.NewClusterMetrics(cluster.Namespace, cluster.Name)
	clusterMetrics.Clear()

	if err := Cleanup(ctx, logger, deps.Client, cluster, policy); err != nil {
		return recon.Result{}, err
	}
	if policy == openbaov1alpha1.DeletionPolicyDeletePVCs || policy == openbaov1alpha1.DeletionPolicyDeleteAll {
		if err := deleteRecoverySecrets(ctx, deps.Client, reader, cluster); err != nil {
			return recon.Result{}, err
		}
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

func readDeletionAuthority(ctx context.Context, reader client.Reader, expected *openbaov1alpha1.OpenBaoCluster) (*openbaov1alpha1.OpenBaoCluster, error) {
	current := &openbaov1alpha1.OpenBaoCluster{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(expected), current); err != nil {
		return nil, fmt.Errorf("read cluster authority for deletion: %w", err)
	}
	if current.UID == "" || current.UID != expected.UID || current.DeletionTimestamp.IsZero() ||
		!slices.Contains(current.Finalizers, openbaov1alpha1.OpenBaoClusterFinalizer) ||
		current.Annotations[constants.AnnotationRestoreHold] != "" {
		return nil, fmt.Errorf("cleanup requires the original deleting cluster with its operator finalizer and no restore hold")
	}
	return current, nil
}
