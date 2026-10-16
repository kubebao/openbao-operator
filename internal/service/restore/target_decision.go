package restore

import (
	"time"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

type targetRecoveryAction uint8

const (
	targetRecoveryAcknowledge targetRecoveryAction = iota
	targetRecoveryObserve
	targetRecoveryRecordApplication
	targetRecoveryWait
	targetRecoveryCleanup
)

type targetApplicationState uint8

const (
	targetApplicationNotObserved targetApplicationState = iota
	targetApplicationUnavailable
	targetApplicationPending
	targetApplicationConfirmed
)

// decideTargetRecovery selects the next effect from durable receipts and a fresh
// observation. It never advances a deadline or authorizes snapshot submission.
func decideTargetRecovery(request *api.OpenBaoRestore, application targetApplicationState, now time.Time) targetRecoveryAction {
	target := request.Status.Target
	// Cleanup remains authoritative after it releases the application hold. A
	// completed receipt still needs its final status write after a lost response.
	if target != nil && target.Cleanup != "" {
		return targetRecoveryCleanup
	}
	if request.Status.Restart != nil || target == nil || target.BootstrapClusterID == "" {
		return targetRecoveryAcknowledge
	}
	if target.AppliedAt == nil && request.Status.SubmissionClaim != nil {
		switch application {
		case targetApplicationNotObserved:
			return targetRecoveryObserve
		case targetApplicationConfirmed:
			// A matching identity is evidence even after the observation deadline.
			return targetRecoveryRecordApplication
		case targetApplicationPending:
			if now.Before(request.Status.SubmissionClaim.ClaimedAt.Add(targetApplicationTimeout)) {
				return targetRecoveryWait
			}
		case targetApplicationUnavailable:
			// A missing or replaced target, or a released hold, ends observation.
		}
	}
	if request.Spec.TargetLifecycle != api.RestoreTargetLifecycleDisposable {
		return targetRecoveryAcknowledge
	}
	if target.AppliedAt != nil && now.Before(target.AppliedAt.Add(time.Duration(request.Spec.CleanupAfterSeconds)*time.Second)) {
		return targetRecoveryWait
	}
	return targetRecoveryCleanup
}
