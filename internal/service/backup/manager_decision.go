package backup

import "time"

type backupDecisionKind uint8

const (
	backupDecisionIdle backupDecisionKind = iota
	backupDecisionBlocked
	backupDecisionCreate
	backupDecisionObserve
	backupDecisionFinalize
)

type backupBlockerKind uint8

const (
	backupNotBlocked backupBlockerKind = iota
	backupBlockedByRestore
	backupBlockedByPrecondition
)

type backupObservation struct {
	configured          bool
	ownsLock            bool
	due                 bool
	manualTriggerToken  string
	now                 time.Time
	scheduledTime       time.Time
	initialNextSchedule time.Time
	nextSchedule        time.Time
	jobs                backupJobObservation
	blocker             backupBlockerKind
	precondition        *backupPreconditionError
}

type backupDecision struct {
	kind        backupDecisionKind
	observation backupObservation
}

func decideBackup(observation backupObservation) backupDecision {
	var kind backupDecisionKind

	switch {
	case observation.ownsLock && observation.jobs.operationActive:
		kind = backupDecisionObserve
	case observation.ownsLock:
		kind = backupDecisionFinalize
	case !observation.configured:
		kind = backupDecisionIdle
	case observation.jobs.hasActive:
		kind = backupDecisionObserve
	case observation.jobs.pendingTerminal != nil:
		kind = backupDecisionFinalize
	case observation.blocker != backupNotBlocked:
		kind = backupDecisionBlocked
	case observation.due:
		kind = backupDecisionCreate
	case observation.jobs.mostRecentTerminal != nil:
		kind = backupDecisionFinalize
	default:
		kind = backupDecisionIdle
	}

	return backupDecision{kind: kind, observation: observation}
}
