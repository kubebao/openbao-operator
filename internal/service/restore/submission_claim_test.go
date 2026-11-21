package restore

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestSubmissionClaimRejectsChangedOrExpiredExecution(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"committed", "expired", "claimed", "replaced", "digest", "size"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			f := newRestoreRecoveryFixture(t)
			require.NoError(t, f.step(t))
			request := f.restore(t)
			uid := request.UID
			switch mutation {
			case "committed":
				request.Status.Execution.Stage = openbaov1alpha1.RestoreExecutionStageCommitted
			case "expired":
				expired := metav1.NewTime(time.Now().Add(-time.Hour))
				request.Status.Execution.PreparedAt = &expired
			case "claimed":
				request.Status.SubmissionClaim = &openbaov1alpha1.RestoreSubmissionClaim{PodUID: "other"}
			case "digest":
				request.Spec.Source.ExpectedDigest = "sha256:expected"
			case "size":
				request.Spec.Source.ExpectedSize = 2
			case "replaced":
				uid = "another-request"
			}
			if mutation == "digest" || mutation == "size" {
				require.NoError(t, f.base.Update(t.Context(), request))
			}
			require.NoError(t, f.base.Status().Update(t.Context(), request))
			err := ClaimRestore(t.Context(), f.base, client.ObjectKeyFromObject(request), uid,
				openbaov1alpha1.RestoreSubmissionClaim{PodUID: "executor", Size: 1})
			require.Error(t, err)
			require.Equal(t, request.Status.SubmissionClaim, f.restore(t).Status.SubmissionClaim)
		})
	}
}

func TestSubmissionClaimEnforcesSelectedSnapshotBudget(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	for _, tc := range []struct {
		name         string
		budget, size int64
		allowed      bool
	}{
		{"default rejects large snapshot", 0, 9 * gib, false},
		{"explicit budget accepts pinned large snapshot", 16 * gib, 9 * gib, true},
		{"explicit budget rejects excess", 16 * gib, 16*gib + 1, false},
		{"overflow fails closed", math.MaxInt64, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestoreRecoveryFixture(t)
			require.NoError(t, f.step(t))
			request := f.restore(t)
			request.Spec.Source.MaxSnapshotSizeBytes = tc.budget
			request.Spec.Source.ExpectedSize = tc.size
			require.NoError(t, f.base.Update(t.Context(), request))
			err := ClaimRestore(t.Context(), f.base, client.ObjectKeyFromObject(request), request.UID,
				openbaov1alpha1.RestoreSubmissionClaim{PodUID: "executor", Size: tc.size})
			if !tc.allowed {
				require.Error(t, err)
				require.Nil(t, f.restore(t).Status.SubmissionClaim)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.size, f.restore(t).Status.SubmissionClaim.Size)
			require.Error(t, ClaimRestore(t.Context(), f.base, client.ObjectKeyFromObject(request), request.UID,
				openbaov1alpha1.RestoreSubmissionClaim{PodUID: "second", Size: tc.size}), "a larger budget must not permit another submission")
		})
	}
}
