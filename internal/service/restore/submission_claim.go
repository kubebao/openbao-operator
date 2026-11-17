package restore

import (
	"context"
	"fmt"
	"time"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/statuspatch"
	portrestore "github.com/kubebao/openbao-operator/internal/port/restore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClaimRestore reserves submission exactly once. Even when the write may have
// succeeded, an error forbids submission. Existing claims are never resumed.
func ClaimRestore(ctx context.Context, c client.Client, key types.NamespacedName,
	requestUID types.UID, claim openbaov1alpha1.RestoreSubmissionClaim,
) error {
	request := &openbaov1alpha1.OpenBaoRestore{}
	if err := c.Get(ctx, key, request); err != nil {
		return fmt.Errorf("read restore before claim: %w", err)
	}

	execution := request.Status.Execution
	if request.UID != requestUID || request.DeletionTimestamp != nil ||
		request.Status.Phase != openbaov1alpha1.RestorePhaseRunning || execution == nil ||
		execution.OperationID != string(requestUID) || execution.Stage != openbaov1alpha1.RestoreExecutionStageCreated ||
		execution.PreparedAt == nil || !time.Now().Before(execution.PreparedAt.Add(constants.DefaultRestorePreparationTimeout)) ||
		request.Status.SubmissionClaim != nil || claim.PodUID == "" || claim.Size <= 0 {
		return fmt.Errorf("restore cannot accept a submission claim")
	}

	budget, err := portrestore.ResolveSnapshotBudget(request.Spec.Source.MaxSnapshotSizeBytes)
	if err != nil {
		return fmt.Errorf("invalid restore snapshot budget: %w", err)
	}
	if claim.Size > budget.MaxBytes {
		return fmt.Errorf("staged snapshot size %d exceeds restore budget %d", claim.Size, budget.MaxBytes)
	}
	before := request.DeepCopy()
	if source := request.Spec.Source; (source.ExpectedDigest != "" && source.ExpectedDigest != claim.Digest) ||
		(source.ExpectedSize != 0 && source.ExpectedSize != claim.Size) {
		return fmt.Errorf("staged snapshot does not match the expected digest or size")
	}

	claim.ClaimedAt = metav1.Now()
	request.Status.SubmissionClaim = &claim
	if err := statuspatch.PatchMerge(ctx, c, request, before, client.MergeFromWithOptimisticLock{}); err != nil {
		return fmt.Errorf("submission claim not acknowledged; do not submit: %w", err)
	}

	return nil
}
