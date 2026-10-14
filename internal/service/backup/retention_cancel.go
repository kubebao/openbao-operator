package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

// CancelRetention keeps source deletion's finalizer until submitted execution
// has stopped. Job absence after submission requires administrator inspection.
func CancelRetention(ctx context.Context, c client.Client, reader client.Reader,
	mutator adminops.StatusMutator, cluster *api.OpenBaoCluster) error {
	live := &api.OpenBaoCluster{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(cluster), live); err != nil {
		return fmt.Errorf("read retention before source cleanup: %w", err)
	}
	if live.UID != cluster.UID {
		return fmt.Errorf("source UID changed before retention cancellation")
	}
	if !retentionActive(live) {
		return nil
	}
	cluster = live
	manager := (&Manager{client: c, reader: reader}).WithAdminOpsStatusMutator(mutator)
	if cluster.Status.Backup.Retention.Active.SubmittedAt == nil {
		return manager.finishRetention(ctx, cluster, api.BackupRetentionSkipped, "SourceDeleting",
			"Retention was not submitted because the source cluster is deleting", nil, time.Now().UTC())
	}
	if _, err := manager.reconcileRetention(ctx, logr.Discard(), cluster, time.Now().UTC()); err != nil {
		return err
	}
	if retentionActive(cluster) {
		return fmt.Errorf("source deletion waits for retention executor termination or administrator inspection")
	}
	return nil
}
