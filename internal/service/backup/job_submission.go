package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

const backupSubmissionPrefix = "submitting backup job "

type uncertainBackupSubmissionError struct {
	cause error
}

func (e *uncertainBackupSubmissionError) Error() string { return e.cause.Error() }
func (e *uncertainBackupSubmissionError) Unwrap() error { return e.cause }

func backupCreateError(err error) error {
	if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) ||
		apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err) {
		return err
	}
	return &uncertainBackupSubmissionError{cause: err}
}

func backupSubmissionUncertain(err error) bool {
	var uncertain *uncertainBackupSubmissionError
	return errors.As(err, &uncertain)
}

func backupLockNamesJob(lock *api.OperationLockStatus, name string) bool {
	return backupOperationLock.IsHeldBy(lock) &&
		(lock.Message == backupJobLockMessage(name) || lock.Message == backupSubmissionPrefix+name)
}

func pendingBackupSubmission(cluster *api.OpenBaoCluster) string {
	if !backupOperationLock.IsHeldBy(cluster.Status.OperationLock) ||
		!strings.HasPrefix(cluster.Status.OperationLock.Message, backupSubmissionPrefix) {
		return ""
	}
	return strings.TrimPrefix(cluster.Status.OperationLock.Message, backupSubmissionPrefix)
}

// Mark submission before Create. Absence after an uncertain response must not
// release the operation lock or authorize another Job creation.
func (m *Manager) markBackupSubmission(ctx context.Context, cluster *api.OpenBaoCluster, name string) error {
	return opslifecycle.AcquireWithReader(ctx,
		retentionGuardReader{Reader: m.reader, uid: cluster.UID, lockMessage: backupJobLockMessage(name)},
		m.client, cluster, backupOperationLock, opslifecycle.AcquireOptions{Message: backupSubmissionPrefix + name})
}

// An owned, observed Job resolves submission uncertainty. The reader guards the
// previous message so another backup operation cannot be rebound by this write.
func (m *Manager) observeBackupSubmission(ctx context.Context, cluster *api.OpenBaoCluster, name string) error {
	if !backupLockNamesJob(cluster.Status.OperationLock, name) {
		return fmt.Errorf("backup Job %s/%s does not match the held operation", cluster.Namespace, name)
	}
	if cluster.Status.OperationLock.Message == backupJobLockMessage(name) {
		return nil
	}
	return opslifecycle.AcquireWithReader(ctx,
		retentionGuardReader{Reader: m.reader, uid: cluster.UID, lockMessage: backupSubmissionPrefix + name},
		m.client, cluster, backupOperationLock, opslifecycle.AcquireOptions{Message: backupJobLockMessage(name)})
}
