package restore

import (
	"fmt"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
)

type restoreDecisionKind uint8

const (
	restoreDecisionIdle restoreDecisionKind = iota
	restoreDecisionMarkUnknown
	restoreDecisionCreateJob
	restoreDecisionAdoptLegacyJob
	restoreDecisionRecordCreatedReceipt
	restoreDecisionRecordSucceededJob
	restoreDecisionRecordFailedJob
	restoreDecisionPollJob
	restoreDecisionContinueRecovery
	restoreDecisionFailRestore
	restoreDecisionCompleteRestore
)

type restoreJobState uint8

const (
	restoreJobNotObserved restoreJobState = iota
	restoreJobRunning
	restoreJobSucceeded
	restoreJobFailed
)

// restoreState projects observed facts into the next operation. The execution
// receipt on OpenBaoRestore remains the durable lifecycle state.
type restoreState struct {
	executionStage openbaov1alpha1.RestoreExecutionStage
	terminalResult openbaov1alpha1.RestoreExecutionResult
	legacy         bool
	jobState       restoreJobState
	unknownMessage string
	waitMessage    string
}

type restoreDecision struct {
	kind    restoreDecisionKind
	message string
}

func decideRestore(state restoreState) restoreDecision {
	if state.unknownMessage != "" {
		return restoreDecision{kind: restoreDecisionMarkUnknown, message: state.unknownMessage}
	}
	if state.legacy {
		return restoreDecision{kind: restoreDecisionAdoptLegacyJob}
	}

	switch state.executionStage {
	case openbaov1alpha1.RestoreExecutionStagePrepared:
		return restoreDecision{kind: restoreDecisionCreateJob}
	case openbaov1alpha1.RestoreExecutionStageCommitted:
		// Persist creation before acting on a terminal Job, even if it has finished.
		return restoreDecision{kind: restoreDecisionRecordCreatedReceipt}
	case openbaov1alpha1.RestoreExecutionStageCreated:
		switch state.jobState {
		case restoreJobSucceeded:
			return restoreDecision{kind: restoreDecisionRecordSucceededJob}
		case restoreJobFailed:
			return restoreDecision{kind: restoreDecisionRecordFailedJob}
		default:
			return restoreDecision{kind: restoreDecisionPollJob}
		}
	case openbaov1alpha1.RestoreExecutionStageTerminalObserved:
		switch state.terminalResult {
		case openbaov1alpha1.RestoreExecutionResultSucceeded:
			return restoreDecision{kind: restoreDecisionContinueRecovery}
		case openbaov1alpha1.RestoreExecutionResultFailed:
			return restoreDecision{kind: restoreDecisionFailRestore}
		default:
			return restoreDecision{
				kind: restoreDecisionMarkUnknown,
				message: fmt.Sprintf(
					"Restore terminal receipt has unsupported result %q. The operator will not create or recreate a restore Job.",
					state.terminalResult,
				),
			}
		}
	case openbaov1alpha1.RestoreExecutionStageFollowThroughComplete:
		return restoreDecision{kind: restoreDecisionCompleteRestore}
	case openbaov1alpha1.RestoreExecutionStageUnknown:
		return restoreDecision{kind: restoreDecisionIdle}
	default:
		return restoreDecision{
			kind: restoreDecisionMarkUnknown,
			message: fmt.Sprintf(
				"Restore execution has unsupported stage %q. The operator will not create or recreate a restore Job.",
				state.executionStage,
			),
		}
	}
}
