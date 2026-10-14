package backup

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// applyRetention applies the retention policy after a successful backup.
func (m *Manager) applyRetention(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, metrics *Metrics) error {
	retention := cluster.Spec.Backup.Retention
	if retention == nil {
		return nil
	}

	if cluster.Spec.Backup.Target.EffectiveS3RoleARN() != "" {
		logger.Info("Skipping retention for workload identity backup target",
			"cluster_namespace", cluster.Namespace,
			"cluster_name", cluster.Name)
		return nil
	}
	if cluster.Spec.Backup.Target.CredentialsSecretRef == nil {
		logger.Info("Skipping retention because no storage credentials Secret is configured",
			"cluster_namespace", cluster.Namespace,
			"cluster_name", cluster.Name)
		return nil
	}

	maxAge, err := ParseRetentionMaxAge(retention.MaxAge)
	if err != nil {
		return fmt.Errorf("failed to parse retention maxAge: %w", err)
	}

	policy := RetentionPolicy{
		MaxCount: retention.MaxCount,
		MaxAge:   maxAge,
	}
	if cluster.Status.Backup != nil {
		policy.ProtectedKey = cluster.Status.Backup.LastBackupName
	}
	// A cached cluster can lag a restore test reservation; never delete its key.
	live := &openbaov1alpha1.OpenBaoCluster{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(cluster), live); err != nil {
		return fmt.Errorf("failed to read restore test reservation for retention: %w", err)
	}
	if live.Status.Backup != nil && live.Status.Backup.RestoreTest != nil && live.Status.Backup.RestoreTest.Active != nil {
		policy.RestoreTestKey = live.Status.Backup.RestoreTest.Active.Key
	}

	storageClient, err := m.openBackupStorageClient(ctx, cluster, false)
	if err != nil {
		return fmt.Errorf("failed to create storage client for retention: %w", err)
	}
	defer func() {
		_ = storageClient.Close()
	}()

	prefix := GetBackupListPrefix(
		cluster.Spec.Backup.Target.PathPrefix,
		cluster.Namespace,
		cluster.Name,
	)

	result, err := ApplyRetention(ctx, logger, storageClient, prefix, policy)
	if err != nil {
		return err
	}

	totalDeleted := result.DeletedByCount + result.DeletedByAge
	if totalDeleted > 0 {
		metrics.IncrementRetentionDeleted(totalDeleted)
	}

	return nil
}
