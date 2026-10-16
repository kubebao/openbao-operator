package restore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestDecideTargetRecovery(t *testing.T) {
	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		edit        func(*api.OpenBaoRestore)
		application targetApplicationState
		want        targetRecoveryAction
	}{
		{name: "fresh target needs observation", want: targetRecoveryObserve},
		{name: "existing target needs acknowledgement", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target = nil
		}, want: targetRecoveryAcknowledge},
		{name: "unbootstrapped target needs acknowledgement", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.BootstrapClusterID = ""
		}, want: targetRecoveryAcknowledge},
		{name: "restart resumes acknowledgement", edit: func(r *api.OpenBaoRestore) {
			r.Status.Restart = &api.RestoreRestartStatus{}
		}, want: targetRecoveryAcknowledge},
		{name: "cleanup precedes restart and observation", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = api.RestoreTargetCleanupPending
			r.Status.Restart = &api.RestoreRestartStatus{}
		}, application: targetApplicationConfirmed, want: targetRecoveryCleanup},
		{name: "failed cleanup keeps acknowledgement in cleanup handler", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = api.RestoreTargetCleanupFailed
		}, want: targetRecoveryCleanup},
		{name: "completed cleanup finishes status", edit: func(r *api.OpenBaoRestore) {
			r.Status.Target.Cleanup = api.RestoreTargetCleanupComplete
			r.Status.Target.BootstrapClusterID = ""
		}, want: targetRecoveryCleanup},
		{name: "identity matches before deadline", application: targetApplicationConfirmed, want: targetRecoveryRecordApplication},
		{name: "identity matches at deadline", edit: func(r *api.OpenBaoRestore) {
			r.Status.SubmissionClaim.ClaimedAt = metav1.NewTime(now.Add(-targetApplicationTimeout))
		}, application: targetApplicationConfirmed, want: targetRecoveryRecordApplication},
		{name: "identity matches after deadline", edit: func(r *api.OpenBaoRestore) {
			r.Status.SubmissionClaim.ClaimedAt = metav1.NewTime(now.Add(-2 * targetApplicationTimeout))
		}, application: targetApplicationConfirmed, want: targetRecoveryRecordApplication},
		{name: "pending before deadline", application: targetApplicationPending, want: targetRecoveryWait},
		{name: "pending at deadline", edit: func(r *api.OpenBaoRestore) {
			r.Status.SubmissionClaim.ClaimedAt = metav1.NewTime(now.Add(-targetApplicationTimeout))
		}, application: targetApplicationPending, want: targetRecoveryCleanup},
		{name: "pending after deadline", edit: func(r *api.OpenBaoRestore) {
			r.Status.SubmissionClaim.ClaimedAt = metav1.NewTime(now.Add(-2 * targetApplicationTimeout))
		}, application: targetApplicationPending, want: targetRecoveryCleanup},
		{name: "retained target waits for acknowledgement after deadline", edit: func(r *api.OpenBaoRestore) {
			r.Spec.TargetLifecycle = api.RestoreTargetLifecycleRetain
			r.Status.SubmissionClaim.ClaimedAt = metav1.NewTime(now.Add(-targetApplicationTimeout))
		}, application: targetApplicationPending, want: targetRecoveryAcknowledge},
		{name: "unavailable disposable target ends observation", application: targetApplicationUnavailable, want: targetRecoveryCleanup},
		{name: "unavailable retained target needs acknowledgement", edit: func(r *api.OpenBaoRestore) {
			r.Spec.TargetLifecycle = api.RestoreTargetLifecycleRetain
		}, application: targetApplicationUnavailable, want: targetRecoveryAcknowledge},
		{name: "no claim cannot confirm application", edit: func(r *api.OpenBaoRestore) {
			r.Status.SubmissionClaim = nil
		}, application: targetApplicationConfirmed, want: targetRecoveryCleanup},
		{name: "applied retained target still needs acknowledgement", edit: func(r *api.OpenBaoRestore) {
			r.Spec.TargetLifecycle = api.RestoreTargetLifecycleRetain
			r.Status.Target.AppliedAt = &metav1.Time{Time: now}
		}, want: targetRecoveryAcknowledge},
		{name: "applied target before cleanup deadline", edit: func(r *api.OpenBaoRestore) {
			r.Spec.CleanupAfterSeconds = 60
			r.Status.Target.AppliedAt = &metav1.Time{Time: now.Add(-time.Minute + time.Nanosecond)}
		}, want: targetRecoveryWait},
		{name: "applied target at cleanup deadline", edit: func(r *api.OpenBaoRestore) {
			r.Spec.CleanupAfterSeconds = 60
			r.Status.Target.AppliedAt = &metav1.Time{Time: now.Add(-time.Minute)}
		}, want: targetRecoveryCleanup},
		{name: "applied target after cleanup deadline", edit: func(r *api.OpenBaoRestore) {
			r.Spec.CleanupAfterSeconds = 60
			r.Status.Target.AppliedAt = &metav1.Time{Time: now.Add(-time.Minute - time.Nanosecond)}
		}, want: targetRecoveryCleanup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &api.OpenBaoRestore{
				Spec: api.OpenBaoRestoreSpec{TargetLifecycle: api.RestoreTargetLifecycleDisposable},
				Status: api.OpenBaoRestoreStatus{
					Target: &api.RestoreTargetStatus{BootstrapClusterID: "bootstrap"},
					SubmissionClaim: &api.RestoreSubmissionClaim{
						ClaimedAt: metav1.NewTime(now.Add(-targetApplicationTimeout + time.Nanosecond)),
					},
				},
			}
			if tc.edit != nil {
				tc.edit(request)
			}
			before := request.DeepCopy()
			require.Equal(t, tc.want, decideTargetRecovery(request, tc.application, now))
			require.Equal(t, before, request, "decisions must not change durable receipts")
		})
	}
}
