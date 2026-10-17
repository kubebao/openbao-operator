/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// UpgradeProgress tracks the state of an in-progress upgrade.
type UpgradeProgress struct {
	// ExecutionID identifies the current rolling upgrade attempt.
	// It remains stable across controller restarts and changes when a retry is accepted.
	// Older in-flight upgrades can omit it until an explicit retry is accepted.
	// +optional
	ExecutionID string `json:"executionID,omitempty"`
	// TargetVersion is the version being upgraded to.
	TargetVersion string `json:"targetVersion"`
	// FromVersion is the version being upgraded from.
	FromVersion string `json:"fromVersion"`
	// StartedAt is when the upgrade began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CurrentPartition is the current StatefulSet partition value.
	CurrentPartition int32 `json:"currentPartition"`
	// CompletedPods lists ordinals of pods that have been successfully upgraded.
	// +optional
	CompletedPods []int32 `json:"completedPods,omitempty"`
	// LastStepDownTime records when the last leader step-down was performed.
	// +optional
	LastStepDownTime *metav1.Time `json:"lastStepDownTime,omitempty"`
	// Failure is the structured rolling-upgrade failure status.
	// When Failure.Reason is non-empty, the upgrade is considered failed.
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	Failure *ControllerErrorStatus `json:"failure,omitempty"`
}

// ControllerErrorStatus captures a controller-scoped error signal that the status controller
// can translate into high-level conditions.
type ControllerErrorStatus struct {
	// Reason is a low-cardinality identifier for the error.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable error message (best-effort).
	// +optional
	Message string `json:"message,omitempty"`
	// At is when the error was observed (best-effort).
	// +optional
	At *metav1.Time `json:"at,omitempty"`
}

// WorkloadControllerStatus holds status owned by the workload controller.
type WorkloadControllerStatus struct {
	// PolicyRevision identifies the last complete built-in policy bundle verified
	// or repaired successfully. This records reconciliation progress, not authorization.
	// +optional
	PolicyRevision string `json:"policyRevision,omitempty"`

	// PolicyReconciliation records individual policy observations and retry scheduling.
	// +optional
	PolicyReconciliation *PolicyReconciliationStatus `json:"policyReconciliation,omitempty"`

	// LastError is the last workload-controller error observed for this cluster.
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	LastError *ControllerErrorStatus `json:"lastError,omitempty"`
}

// PolicyReconciliationStatus records progress without granting OpenBao permissions.
type PolicyReconciliationStatus struct {
	// Revisions contains the last verified content digest for each observed policy.
	// An empty digest means the policy was observed missing or different and awaits repair.
	// An absent entry means the policy has not been observed; it does not block operations.
	// Failed reads retain the last observation; OpenBao still authorizes every operation.
	// +optional
	Revisions map[string]string `json:"revisions,omitempty"`
	// AttemptedRevision identifies the bundle used for the last attempt.
	// A desired bundle change bypasses the previous retry delay.
	// +optional
	AttemptedRevision string `json:"attemptedRevision,omitempty"`
	// LastVerified is when the complete desired bundle was last verified or repaired.
	// Unchanged successful observations are refreshed at most once every five minutes.
	// +optional
	LastVerified *metav1.Time `json:"lastVerified,omitempty"`
	// RetryAfter delays policy requests after a failure, including during unrelated reconciles.
	// +optional
	RetryAfter *metav1.Time `json:"retryAfter,omitempty"`
	// LastError reports policy failures independently of infrastructure and Autopilot errors.
	// +optional
	LastError *ControllerErrorStatus `json:"lastError,omitempty"`
}

// AdminOpsControllerStatus holds status owned by the adminops controller.
type AdminOpsControllerStatus struct {
	// LastError is the last adminops-controller error observed for this cluster.
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	LastError *ControllerErrorStatus `json:"lastError,omitempty"`
}

// BlueGreenPhase is a high-level summary of blue/green upgrade state.
// +kubebuilder:validation:Enum=Idle;DeployingGreen;JoiningMesh;Syncing;Promoting;DemotingBlue;Cleanup;RestoringReadReplicas;RollingBack;RollbackCleanup
type BlueGreenPhase string

const (
	// PhaseIdle indicates no blue/green upgrade is in progress.
	PhaseIdle BlueGreenPhase = "Idle"
	// PhaseDeployingGreen indicates the Green StatefulSet is being created and pods are becoming ready.
	// This phase includes waiting for pods to be unsealed.
	PhaseDeployingGreen BlueGreenPhase = "DeployingGreen"
	// PhaseJoiningMesh indicates Green pods are joining the Raft cluster as non-voters.
	PhaseJoiningMesh BlueGreenPhase = "JoiningMesh"
	// PhaseSyncing indicates waiting for Green nodes to catch up with Blue nodes.
	PhaseSyncing BlueGreenPhase = "Syncing"
	// PhasePromoting indicates Green nodes are being promoted to voters.
	PhasePromoting BlueGreenPhase = "Promoting"
	// PhaseDemotingBlue indicates Blue nodes are being demoted to non-voters.
	PhaseDemotingBlue BlueGreenPhase = "DemotingBlue"
	// PhaseCleanup indicates Blue StatefulSet is being deleted.
	PhaseCleanup BlueGreenPhase = "Cleanup"
	// PhaseRestoringReadReplicas indicates the steady-state read-replica pool is
	// being restored after cutover cleanup and must converge before the upgrade
	// returns to Idle.
	PhaseRestoringReadReplicas BlueGreenPhase = "RestoringReadReplicas"
	// PhaseRollingBack indicates the upgrade is being rolled back.
	// Blue nodes are re-promoted and Green nodes are demoted.
	PhaseRollingBack BlueGreenPhase = "RollingBack"
	// PhaseRollbackCleanup indicates Green StatefulSet is being deleted after rollback.
	PhaseRollbackCleanup BlueGreenPhase = "RollbackCleanup"
)

// BlueGreenValidationHookStage identifies the durable execution boundary
// reached by a pre-promotion validation hook.
// +kubebuilder:validation:Enum=Prepared;Committed;Created;TerminalObserved;Unknown
type BlueGreenValidationHookStage string

const (
	// BlueGreenValidationHookStagePrepared indicates that the expected Job
	// identity is durable, but Job creation has not been committed.
	BlueGreenValidationHookStagePrepared BlueGreenValidationHookStage = "Prepared"
	// BlueGreenValidationHookStageCommitted indicates that the controller
	// durably committed to one Job creation attempt. A missing Job after this
	// point is ambiguous and is not recreated automatically.
	BlueGreenValidationHookStageCommitted BlueGreenValidationHookStage = "Committed"
	// BlueGreenValidationHookStageCreated indicates that the controller
	// persisted the created Job identity.
	BlueGreenValidationHookStageCreated BlueGreenValidationHookStage = "Created"
	// BlueGreenValidationHookStageTerminalObserved indicates that the controller
	// persisted the terminal Job result.
	BlueGreenValidationHookStageTerminalObserved BlueGreenValidationHookStage = "TerminalObserved"
	// BlueGreenValidationHookStageUnknown indicates that the controller cannot
	// prove whether the committed validation hook ran.
	BlueGreenValidationHookStageUnknown BlueGreenValidationHookStage = "Unknown"
)

// BlueGreenValidationHookResult is the persisted terminal result of a
// pre-promotion validation hook Job.
// +kubebuilder:validation:Enum=Succeeded;Failed
type BlueGreenValidationHookResult string

const (
	// BlueGreenValidationHookResultSucceeded indicates that the validation hook
	// Job succeeded.
	BlueGreenValidationHookResultSucceeded BlueGreenValidationHookResult = "Succeeded"
	// BlueGreenValidationHookResultFailed indicates that the validation hook Job
	// failed.
	BlueGreenValidationHookResultFailed BlueGreenValidationHookResult = "Failed"
)

// BlueGreenValidationHookStatus records the expected identity and durable
// receipts for one pre-promotion validation hook execution.
type BlueGreenValidationHookStatus struct {
	// OperationID identifies the blue/green upgrade attempt that owns this hook.
	OperationID string `json:"operationID"`
	// GreenRevision identifies the Green revision validated by this hook.
	GreenRevision string `json:"greenRevision"`
	// SpecHash identifies the normalized validation hook specification.
	SpecHash string `json:"specHash"`
	// Stage is the latest durable execution boundary observed by the controller.
	Stage BlueGreenValidationHookStage `json:"stage"`
	// JobName is the expected validation hook Job name.
	JobName string `json:"jobName"`
	// JobUID is the UID returned for the created validation hook Job.
	// +optional
	JobUID types.UID `json:"jobUID,omitempty"`
	// PreparedAt is when the expected hook identity became durable.
	// +optional
	PreparedAt *metav1.Time `json:"preparedAt,omitempty"`
	// CommittedAt is when the controller committed to one Job creation attempt.
	// +optional
	CommittedAt *metav1.Time `json:"committedAt,omitempty"`
	// CreatedAt is when the controller persisted the created Job receipt.
	// +optional
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
	// TerminalResult is the persisted terminal Job result.
	// +optional
	TerminalResult BlueGreenValidationHookResult `json:"terminalResult,omitempty"`
	// TerminalObservedAt is when the controller persisted the terminal Job result.
	// +optional
	TerminalObservedAt *metav1.Time `json:"terminalObservedAt,omitempty"`
}

// BlueGreenStatus tracks the lifecycle of the "Green" revision during blue/green upgrades.
type BlueGreenStatus struct {
	// Phase is the current phase of the blue/green upgrade.
	Phase BlueGreenPhase `json:"phase,omitempty"`
	// OperationID identifies the current blue/green upgrade attempt.
	// +optional
	OperationID string `json:"operationID,omitempty"`
	// BlueRevision is the hash/name of the currently active cluster.
	BlueRevision string `json:"blueRevision,omitempty"`
	// BlueControllerRevision is the Kubernetes StatefulSet controller revision
	// of Blue. It identifies an unrevisioned rolling workload after switching to
	// BlueGreen without requiring the existing Pods to be restarted or relabeled.
	// +optional
	BlueControllerRevision string `json:"blueControllerRevision,omitempty"`
	// BlueImage is the container image used by the Blue cluster.
	// This ensures the Blue cluster is not actively upgraded when spec.image changes.
	BlueImage string `json:"blueImage,omitempty"`
	// BlueReplicas is the voter population captured when the upgrade starts.
	// It remains fixed until Green becomes the stable workload.
	// +optional
	// +kubebuilder:validation:Minimum=1
	BlueReplicas int32 `json:"blueReplicas,omitempty"`
	// GreenImage is the image selected for the in-flight target.
	// +optional
	GreenImage string `json:"greenImage,omitempty"`
	// GreenVersion is the version selected for the in-flight target.
	// +optional
	GreenVersion string `json:"greenVersion,omitempty"`
	// GreenReplicas is the replica count selected for the in-flight target.
	// +optional
	// +kubebuilder:validation:Minimum=1
	GreenReplicas int32 `json:"greenReplicas,omitempty"`
	// GreenRevision is the hash/name of the next cluster (if upgrade in progress).
	GreenRevision string `json:"greenRevision,omitempty"`
	// ManualPromotionRequired snapshots whether the current in-flight blue/green
	// upgrade requires an explicit spec.upgrade.requests.promote request before
	// promotion can proceed. It is derived from spec.upgrade.blueGreen.autoPromote
	// when the upgrade starts.
	// +optional
	ManualPromotionRequired bool `json:"manualPromotionRequired,omitempty"`
	// StartTime is when the current phase began.
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// JobFailureCount tracks consecutive job failures in the current phase.
	// Reset to 0 on phase transition or successful job completion.
	// +optional
	JobFailureCount int32 `json:"jobFailureCount,omitempty"`
	// LastJobFailure records the name of the last failed job for debugging.
	// +optional
	LastJobFailure string `json:"lastJobFailure,omitempty"`
	// PreUpgradeSnapshotJobName is the name of the backup job triggered at upgrade start.
	// +optional
	PreUpgradeSnapshotJobName string `json:"preUpgradeSnapshotJobName,omitempty"`
	// ValidationHook records the expected identity and durable execution receipts
	// for the current pre-promotion validation hook.
	// +optional
	ValidationHook *BlueGreenValidationHookStatus `json:"validationHook,omitempty"`
	// RollbackReason records why a rollback was triggered (if any).
	// +optional
	RollbackReason string `json:"rollbackReason,omitempty"`
	// RollbackStartTime is when the rollback was initiated.
	// +optional
	RollbackStartTime *metav1.Time `json:"rollbackStartTime,omitempty"`
	// RollbackAttempt increments each time rollback automation is retried.
	// It is used to produce stable, deterministic Job names per attempt.
	// +optional
	RollbackAttempt int32 `json:"rollbackAttempt,omitempty"`
}

// UpgradeRequestStatus tracks which explicit upgrade request values have already been handled.
type UpgradeRequestStatus struct {
	// LastHandledRetry is the last observed spec.upgrade.requests.retry value
	// that the operator has handled.
	// +optional
	LastHandledRetry string `json:"lastHandledRetry,omitempty"`
	// LastHandledPromote is the last observed spec.upgrade.requests.promote
	// value that the operator has handled.
	// +optional
	LastHandledPromote string `json:"lastHandledPromote,omitempty"`
	// LastHandledRollback is the last observed spec.upgrade.requests.rollback
	// value that the operator has handled.
	// +optional
	LastHandledRollback string `json:"lastHandledRollback,omitempty"`
}

// BackupStatus tracks the state of backups for a cluster.
type BackupStatus struct {
	// Retention stores the active cleanup workload and its last independent outcome.
	// +optional
	Retention *BackupRetentionStatus `json:"retention,omitempty"`

	// SuccessfulBackups counts newly observed successful backup Jobs.
	// +optional
	SuccessfulBackups int64 `json:"successfulBackups,omitempty"`
	// LatestSnapshot contains the latest executor's source observation.
	// +optional
	LatestSnapshot *BackupSnapshotSummary `json:"latestSnapshot,omitempty"`
	// RestoreTest stores one active run and one last result.
	// +optional
	RestoreTest *RestoreTestStatus `json:"restoreTest,omitempty"`

	// LastBackupTime is the timestamp of the last successful backup.
	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`
	// LastAttemptTime is the timestamp of the last backup attempt, regardless of outcome.
	// This is used to avoid retry loops when a scheduled backup fails.
	// +optional
	LastAttemptTime *metav1.Time `json:"lastAttemptTime,omitempty"`
	// LastAttemptScheduledTime is the scheduled time of the last backup attempt.
	// It is derived from the cron schedule and used to ensure at-most-once execution
	// per scheduled window.
	// +optional
	LastAttemptScheduledTime *metav1.Time `json:"lastAttemptScheduledTime,omitempty"`
	// LastHandledManualTrigger is the last observed manual trigger token that
	// has progressed into an actual backup attempt.
	// +optional
	LastHandledManualTrigger string `json:"lastHandledManualTrigger,omitempty"`
	// LastBackupSize is the size in bytes of the last successful backup.
	// +optional
	LastBackupSize int64 `json:"lastBackupSize,omitempty"`
	// LastBackupDuration is how long the last backup took (e.g., "45s").
	// +optional
	LastBackupDuration string `json:"lastBackupDuration,omitempty"`
	// LastBackupName is the object key/path of the last successful backup.
	// +optional
	LastBackupName string `json:"lastBackupName,omitempty"`
	// NextScheduledBackup is when the next backup is scheduled.
	// +optional
	NextScheduledBackup *metav1.Time `json:"nextScheduledBackup,omitempty"`
	// ConsecutiveFailures is the number of consecutive backup failures.
	// +optional
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`
	// LastFailureReason is the low-cardinality reason code for the last backup failure (if applicable).
	// +optional
	LastFailureReason string `json:"lastFailureReason,omitempty"`
	// LastFailureMessage is the detailed message for the last backup failure (if applicable).
	// +optional
	LastFailureMessage string `json:"lastFailureMessage,omitempty"`
	// LastFailureTime is when the last backup failure was recorded.
	// +optional
	LastFailureTime *metav1.Time `json:"lastFailureTime,omitempty"`
}

// ReadReplicaStorageStatus captures observed storage state for the read-replica
// pool.
type ReadReplicaStorageStatus struct {
	// DesiredPVCs is the number of data PVCs expected for the read-replica pool.
	// +optional
	DesiredPVCs int32 `json:"desiredPVCs,omitempty"`
	// BoundPVCs is the number of observed data PVCs for the read-replica pool.
	// +optional
	BoundPVCs int32 `json:"boundPVCs,omitempty"`
	// StorageClassName is the effective StorageClass observed for the
	// read-replica PVCs when it is consistent.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
}

// ReadReplicaStatus captures observed state for the read-replica pool.
type ReadReplicaStatus struct {
	// DesiredReplicas is the desired number of read replicas.
	// +optional
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
	// ReadyReplicas is the number of Ready read-replica Pods observed.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// RegisteredReplicas is the number of observed non-voter peers registered in
	// Raft membership.
	// +optional
	RegisteredReplicas int32 `json:"registeredReplicas,omitempty"`
	// HealthyReplicas is the number of read-replica peers that are currently
	// healthy according to the Raft Autopilot state endpoint.
	// +optional
	HealthyReplicas int32 `json:"healthyReplicas,omitempty"`
	// Storage captures read-replica-specific storage observation state.
	// +optional
	Storage ReadReplicaStorageStatus `json:"storage,omitempty"`
}

// ClusterRestoreStatus retains restart observations from earlier operator releases.
// New restores report status, including managed restarts, on OpenBaoRestore.
type ClusterRestoreStatus struct {
	// Name identifies the OpenBaoRestore recorded by an earlier release.
	// +optional
	Name string `json:"name,omitempty"`
	// UID identifies the OpenBaoRestore recorded by an earlier release.
	// +optional
	UID string `json:"uid,omitempty"`
	// RestartCompletedAt is the legacy timestamp for a controller-observed voter
	// restart. Administrator-led recovery does not update this field.
	// +optional
	RestartCompletedAt *metav1.Time `json:"restartCompletedAt,omitempty"`
}

// DriftStatus tracks drift detection and correction events for a cluster.
// OpenBaoClusterStatus defines the observed state of an OpenBaoCluster.
type OpenBaoClusterStatus struct {
	// ObservedGeneration is the most recent metadata.generation that has been
	// reconciled into this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is a high-level summary of the cluster state.
	// +optional
	Phase ClusterPhase `json:"phase,omitempty"`
	// ActiveLeader is the current Raft leader pod name, for example "prod-cluster-0".
	// +optional
	ActiveLeader string `json:"activeLeader,omitempty"`
	// ReadyReplicas is the number of replicas that are currently Ready.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// ReadReplicas captures observed state for the read-replica pool.
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	ReadReplicas *ReadReplicaStatus `json:"readReplicas,omitempty"`
	// CurrentVersion is the OpenBao version currently running on the cluster.
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`
	// AcceptedUpgradeStrategy is the upgrade strategy the operator has accepted
	// after applying idle-state transition guards. While a requested strategy
	// change is blocked, controllers continue using this strategy so an existing
	// operation can finish safely.
	// +optional
	AcceptedUpgradeStrategy UpdateStrategyType `json:"acceptedUpgradeStrategy,omitempty"`
	// Initialized indicates whether the OpenBao cluster has been initialized.
	// This is set to true after the first pod is initialized using bao operator init
	// or after self-initialization completes.
	// +optional
	Initialized bool `json:"initialized,omitempty"`
	// SelfInitialized indicates whether the cluster was initialized using
	// OpenBao's self-initialization feature. When true, no root token Secret
	// exists for this cluster (the root token was auto-revoked).
	// +optional
	SelfInitialized bool `json:"selfInitialized,omitempty"`
	// Upgrade tracks the state of an in-progress upgrade (if any).
	// When non-nil, an upgrade is in progress and the UpgradeManager is orchestrating
	// the pod-by-pod rolling update with leader step-down.
	// +optional
	// +kubebuilder:validation:Nullable
	Upgrade *UpgradeProgress `json:"upgrade,omitempty"`
	// UpgradeRequests tracks which explicit upgrade request values have already
	// been handled so one-shot requests are edge-triggered instead of level-triggered.
	// +optional
	// +kubebuilder:validation:Nullable
	UpgradeRequests *UpgradeRequestStatus `json:"upgradeRequests,omitempty"`
	// Backup tracks the state of backups for this cluster.
	// +optional
	// +kubebuilder:validation:Nullable
	Backup *BackupStatus `json:"backup,omitempty"`
	// Restore retains post-restore restart status written by earlier releases.
	// It is deprecated: new restores report status on OpenBaoRestore, and the
	// operator reads this field only to preserve legacy Pod templates.
	// +optional
	// +kubebuilder:validation:Nullable
	Restore *ClusterRestoreStatus `json:"restore,omitempty"`
	// BlueGreen tracks the state of blue/green upgrades (if enabled).
	// +optional
	// +kubebuilder:validation:Nullable
	BlueGreen *BlueGreenStatus `json:"blueGreen,omitempty"`
	// OperationLock prevents concurrent long-running operations (upgrade/backup/restore)
	// from acting on the same cluster at the same time.
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	OperationLock *OperationLockStatus `json:"operationLock,omitempty"`
	// BreakGlass records when the operator has halted quorum-risk automation and requires
	// explicit operator acknowledgment to continue.
	// +optional
	// +kubebuilder:validation:Nullable
	BreakGlass *BreakGlassStatus `json:"breakGlass,omitempty"`
	// Workload holds signals owned by the workload controller (infrastructure reconciliation).
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	Workload *WorkloadControllerStatus `json:"workload,omitempty"`
	// AdminOps holds signals owned by the adminops controller (upgrade + backup).
	// +optional
	// +nullable
	// +kubebuilder:validation:Nullable
	AdminOps *AdminOpsControllerStatus `json:"adminOps,omitempty"`
	// Conditions represent the current state of the OpenBaoCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterOperation identifies a mutually-exclusive operator operation.
// +kubebuilder:validation:Enum=Upgrade;Backup;Restore
type ClusterOperation string

const (
	ClusterOperationUpgrade ClusterOperation = "Upgrade"
	ClusterOperationBackup  ClusterOperation = "Backup"
	ClusterOperationRestore ClusterOperation = "Restore"
)

// OperationLockStatus represents a status-based lock held by the operator.
// +structType=atomic
type OperationLockStatus struct {
	// Operation is the operation currently holding the lock.
	// +optional
	Operation ClusterOperation `json:"operation,omitempty"`
	// Holder is a stable identifier for the lock holder (controller/component).
	// +optional
	Holder string `json:"holder,omitempty"`
	// Message provides human-readable context for why the lock is held.
	// +optional
	Message string `json:"message,omitempty"`
	// AcquiredAt is when the lock was first acquired.
	// +optional
	AcquiredAt *metav1.Time `json:"acquiredAt,omitempty"`
	// RenewedAt is updated when the holder reasserts the lock during reconciliation.
	// +optional
	RenewedAt *metav1.Time `json:"renewedAt,omitempty"`
}

// BreakGlassReason describes why the operator required manual intervention.
// +kubebuilder:validation:Enum=RollbackConsensusRepairFailed;RollbackCleanupPeerRemovalFailed
type BreakGlassReason string

const (
	BreakGlassReasonRollbackConsensusRepairFailed    BreakGlassReason = "RollbackConsensusRepairFailed"
	BreakGlassReasonRollbackCleanupPeerRemovalFailed BreakGlassReason = "RollbackCleanupPeerRemovalFailed"
)

// BreakGlassStatus captures safe-mode / break-glass state and recovery guidance.
type BreakGlassStatus struct {
	// Active indicates whether break glass mode is currently active.
	// +optional
	Active bool `json:"active,omitempty"`
	// Reason is a stable, typed reason for entering break glass mode.
	// +optional
	Reason BreakGlassReason `json:"reason,omitempty"`
	// Message provides a short summary of the detected unsafe state.
	// +optional
	Message string `json:"message,omitempty"`
	// Nonce is the acknowledgment token required to resume automation.
	// +optional
	Nonce string `json:"nonce,omitempty"`
	// EnteredAt is when break glass mode became active.
	// +optional
	EnteredAt *metav1.Time `json:"enteredAt,omitempty"`
	// Steps provides deterministic recovery guidance.
	// +optional
	Steps []string `json:"steps,omitempty"`
	// AcknowledgedAt records when break glass was acknowledged.
	// +optional
	AcknowledgedAt *metav1.Time `json:"acknowledgedAt,omitempty"`
}
