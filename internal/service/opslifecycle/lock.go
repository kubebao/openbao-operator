package opslifecycle

import (
	"context"
	"errors"
	"fmt"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/statusapply"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OperationLock describes the intended lock identity for a long-running operation.
type OperationLock struct {
	Holder    string
	Operation openbaov1alpha1.ClusterOperation
}

// AcquireOptions configures how lock acquisition should behave.
type AcquireOptions struct {
	Message string
	Force   bool
	// ForceIf permits replacement of the current foreign lock when it returns
	// true. The function runs after each fresh read, including conflict retries.
	ForceIf func(*openbaov1alpha1.OperationLockStatus) bool
}

// AcquireResult describes the lock state observed by the successful acquire
// or renewal attempt.
type AcquireResult struct {
	PreviousLock *openbaov1alpha1.OperationLockStatus
	Forced       bool
}

var (
	// ErrLockHeld indicates an operation lock is held by another operation/holder.
	ErrLockHeld = errors.New("operation lock is held by another operation")
)

// HeldError provides structured information when a lock cannot be acquired.
type HeldError struct {
	Operation openbaov1alpha1.ClusterOperation
	Holder    string
	Message   string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%s: operation=%q holder=%q message=%q", ErrLockHeld, e.Operation, e.Holder, e.Message)
}

func (e *HeldError) Unwrap() error {
	return ErrLockHeld
}

// IsHeldBy reports whether status lock matches the expected lock identity.
func (l OperationLock) IsHeldBy(lock *openbaov1alpha1.OperationLockStatus) bool {
	return lock != nil && lock.Operation == l.Operation && lock.Holder == l.Holder
}

// AcquireWithReader acquires or renews the lock on the cluster status using a
// dedicated reader for fresh read-before-write visibility.
func AcquireWithReader(
	ctx context.Context,
	reader client.Reader,
	c client.Client,
	cluster *openbaov1alpha1.OpenBaoCluster,
	lock OperationLock,
	opts AcquireOptions,
) error {
	_, err := AcquireWithReaderResult(ctx, reader, c, cluster, lock, opts)
	return err
}

// AcquireWithReaderResult acquires or renews the lock and returns the lock
// observed by the successful fresh-read attempt.
func AcquireWithReaderResult(
	ctx context.Context,
	reader client.Reader,
	c client.Client,
	cluster *openbaov1alpha1.OpenBaoCluster,
	lock OperationLock,
	opts AcquireOptions,
) (AcquireResult, error) {
	if cluster == nil {
		return AcquireResult{}, fmt.Errorf("cluster is required")
	}
	if lock.Holder == "" {
		return AcquireResult{}, fmt.Errorf("holder is required")
	}
	if lock.Operation == "" {
		return AcquireResult{}, fmt.Errorf("operation is required")
	}

	result := AcquireResult{}
	err := mutateOperationLockStatus(ctx, reader, c, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		if obj.UID != cluster.UID {
			return fmt.Errorf("cluster UID changed before operation lock acquisition")
		}
		if backup := obj.Status.Backup; backup != nil && backup.Retention != nil && backup.Retention.Active != nil {
			return &HeldError{Operation: openbaov1alpha1.ClusterOperationBackup, Holder: "backup-retention",
				Message: fmt.Sprintf("retention Job %s has not terminated", backup.Retention.Active.Name)}
		}
		now := metav1.Now()
		current := obj.Status.OperationLock
		result = AcquireResult{}
		if current != nil {
			result.PreviousLock = current.DeepCopy()
		}
		switch {
		case current == nil:
			obj.Status.OperationLock = newOperationLockStatus(lock, opts.Message, now)
			return nil
		case lock.IsHeldBy(current):
			desired := current.DeepCopy()
			desired.Message = opts.Message
			desired.RenewedAt = &now
			if desired.AcquiredAt == nil {
				desired.AcquiredAt = &now
			}
			obj.Status.OperationLock = desired
			return nil
		case opts.Force || (opts.ForceIf != nil && opts.ForceIf(current)):
			result.Forced = true
			obj.Status.OperationLock = newOperationLockStatus(lock, opts.Message, now)
			return nil
		default:
			return heldErrorFor(current)
		}
	})
	if err != nil {
		return AcquireResult{}, err
	}
	return result, nil
}

// ReleaseWithReader releases the lock when it is owned by the given operation
// identity using a dedicated reader for fresh read-before-write visibility.
func ReleaseWithReader(
	ctx context.Context,
	reader client.Reader,
	c client.Client,
	cluster *openbaov1alpha1.OpenBaoCluster,
	lock OperationLock,
) error {
	if cluster == nil {
		return fmt.Errorf("cluster is required")
	}
	if lock.Holder == "" {
		return fmt.Errorf("holder is required")
	}
	if lock.Operation == "" {
		return fmt.Errorf("operation is required")
	}

	return mutateOperationLockStatus(ctx, reader, c, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		if obj.UID != cluster.UID {
			return fmt.Errorf("cluster UID changed before operation lock release")
		}
		current := obj.Status.OperationLock
		switch {
		case current == nil:
			return nil
		case !lock.IsHeldBy(current):
			return heldErrorFor(current)
		default:
			obj.Status.OperationLock = nil
			return nil
		}
	})
}

// IsLockHeld reports whether err indicates the lock is currently held by another operation.
func IsLockHeld(err error) bool {
	return errors.Is(err, ErrLockHeld)
}

// AsHeldError extracts operation lock holder details from a contention error.
func AsHeldError(err error) (*HeldError, bool) {
	var heldErr *HeldError
	if !errors.As(err, &heldErr) {
		return nil, false
	}
	return heldErr, true
}

// AddHeldAuditFields enriches audit fields with contention details when available.
func AddHeldAuditFields(fields map[string]string, err error) {
	if fields == nil {
		return
	}
	heldErr, ok := AsHeldError(err)
	if !ok {
		return
	}
	fields["held_by_operation"] = string(heldErr.Operation)
	fields["held_by_holder"] = heldErr.Holder
}

func newOperationLockStatus(
	lock OperationLock,
	message string,
	now metav1.Time,
) *openbaov1alpha1.OperationLockStatus {
	return &openbaov1alpha1.OperationLockStatus{
		Operation:  lock.Operation,
		Holder:     lock.Holder,
		Message:    message,
		AcquiredAt: &now,
		RenewedAt:  &now,
	}
}

func heldErrorFor(lock *openbaov1alpha1.OperationLockStatus) *HeldError {
	return &HeldError{
		Operation: lock.Operation,
		Holder:    lock.Holder,
		Message:   lock.Message,
	}
}

func mutateOperationLockStatus(
	ctx context.Context,
	reader client.Reader,
	c client.Client,
	cluster *openbaov1alpha1.OpenBaoCluster,
	mutate statusapply.OpenBaoClusterOperationLockStatusMutator,
) error {
	key := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
	updated, err := statusapply.MutateAndPatchOpenBaoClusterOperationLockStatusWithReader(ctx, reader, c, key, mutate)
	if err != nil {
		return fmt.Errorf("failed to patch operation lock status: %w", err)
	}

	cluster.ResourceVersion = updated.ResourceVersion
	if updated.Status.OperationLock == nil {
		cluster.Status.OperationLock = nil
	} else {
		cluster.Status.OperationLock = updated.Status.OperationLock.DeepCopy()
	}
	return nil
}
