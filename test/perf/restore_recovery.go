package perf

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// resumeFixtureRestore acts as the administrator of the harness-owned fixture.
// Resume measures managed recovery; it does not confirm snapshot application.
func (n *nativeScenarioContext) resumeFixtureRestore(
	ctx context.Context,
	request *api.OpenBaoRestore,
	fixture *api.OpenBaoCluster,
	phases *[]Phase,
	phaseTimes map[string]time.Time,
) (bool, error) {
	if err := validateFixtureRestoreReceipt(request, fixture, n.runID); err != nil {
		return false, err
	}
	recordPhaseOnce(phases, phaseTimes, "restore_submission_accepted",
		request.Status.Execution.TerminalObservedAt.Time, "openbaorestore_status")

	cluster := &api.OpenBaoCluster{}
	if err := n.client.Get(ctx, client.ObjectKeyFromObject(fixture), cluster); err != nil {
		return false, fmt.Errorf("read restore fixture: %w", err)
	}
	if cluster.UID != fixture.UID || cluster.DeletionTimestamp != nil {
		return false, fmt.Errorf("restore fixture was replaced or is deleting")
	}

	if request.Status.AdministratorDisposition == api.RestoreAdministratorResume {
		released := meta.FindStatusCondition(request.Status.Conditions, constants.RestoreRecoveryReleasedConditionType)
		if released == nil || released.Status != metav1.ConditionTrue ||
			released.ObservedGeneration != request.Generation || request.Status.Restart == nil ||
			request.Status.Restart.CompletedAt == nil || len(request.Status.Restart.Pods) == 0 ||
			cluster.Annotations[constants.AnnotationRestoreHold] != "" || cluster.Status.OperationLock != nil {
			return false, nil
		}
		for _, original := range request.Status.Restart.Pods {
			pod := &corev1.Pod{}
			if err := n.client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: original.Name}, pod); err != nil {
				return false, fmt.Errorf("read recovered fixture Pod: %w", err)
			}
			if pod.UID == original.UID || pod.DeletionTimestamp != nil || podReadyTransitionTime(pod).IsZero() {
				return false, nil
			}
		}
		recordPhaseOnce(phases, phaseTimes, "restore_recovery_released", conditionTransitionTime(released),
			"openbaorestore_status")
		return true, nil
	}
	if request.Status.AdministratorDisposition != "" {
		return false, fmt.Errorf("restore fixture recovery was abandoned")
	}
	if cluster.Annotations[constants.AnnotationRestoreHold] != string(request.UID) {
		return false, fmt.Errorf("restore fixture does not hold this request")
	}
	acknowledgement := string(request.UID) + "/Resume"
	if request.Annotations[constants.AnnotationRestoreAcknowledge] == acknowledgement {
		return false, nil
	}
	before := request.DeepCopy()
	if request.Annotations == nil {
		request.Annotations = make(map[string]string)
	}
	request.Annotations[constants.AnnotationRestoreAcknowledge] = acknowledgement
	if err := n.client.Patch(ctx, request,
		client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("acknowledge fixture restore recovery: %w", err)
	}
	recordPhaseOnce(phases, phaseTimes, "restore_recovery_requested", time.Now().UTC(), "harness")
	return false, nil
}

func validateFixtureRestoreReceipt(request *api.OpenBaoRestore, fixture *api.OpenBaoCluster, runID string) error {
	execution := request.Status.Execution
	if runID == "" || request.UID == "" || fixture.UID == "" || request.Labels[perfRunIDLabel] != runID ||
		fixture.Labels[perfRunIDLabel] != runID || request.Namespace != fixture.Namespace ||
		request.Spec.Cluster != fixture.Name || execution == nil || execution.TargetUID != fixture.UID ||
		execution.OperationID != string(request.UID) || execution.JobUID == "" {
		return fmt.Errorf("restore receipt does not identify the harness-owned fixture")
	}
	if execution.TerminalResult != api.RestoreExecutionResultSucceeded || execution.TerminalObservedAt == nil ||
		request.Status.SubmissionClaim == nil || request.Status.SubmissionClaim.PodUID == "" {
		return fmt.Errorf("restore requires investigation before fixture Resume: %s", request.Status.Message)
	}
	return nil
}
