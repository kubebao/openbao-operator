package upgrade

import (
	"time"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/raftops"
)

// Default timeouts and intervals for upgrade operations.
const (
	// DefaultPodReadyTimeout is the maximum time to wait for a pod to become ready.
	DefaultPodReadyTimeout = 10 * time.Minute

	// DefaultPodReadyCheckInterval is how often to check pod readiness.
	DefaultPodReadyCheckInterval = 5 * time.Second

	// DefaultStepDownTimeout is the maximum time to wait for a leader step-down.
	DefaultStepDownTimeout = 60 * time.Second

	// DefaultHealthCheckTimeout is the timeout for individual health check requests.
	DefaultHealthCheckTimeout = 5 * time.Second

	// DefaultHealthCheckInterval is how often to perform health checks.
	DefaultHealthCheckInterval = 2 * time.Second

	// DefaultLeaderCheckInterval is how often to check for a leader.
	DefaultLeaderCheckInterval = 5 * time.Second

	// ComponentUpgrade is the component name for upgrade resources.
	ComponentUpgrade = "upgrade"

	// DefaultMaxPreUpgradeBackupRetries is the default number of retry attempts for pre-upgrade backups.
	// If a pre-upgrade backup job fails, the operator will delete the failed job and retry up to this many times.
	DefaultMaxPreUpgradeBackupRetries = 3
)

// Reason constants for condition updates.
// These are used to set the Reason field in Kubernetes Conditions.
const (
	// ReasonUpgradeStarted indicates the upgrade process has begun.
	ReasonUpgradeStarted = constants.ReasonUpgradeStarted

	// ReasonUpgradeInProgress indicates the rolling update is in progress.
	ReasonUpgradeInProgress = "UpgradeInProgress"

	// ReasonUpgradeComplete indicates the upgrade finished successfully.
	ReasonUpgradeComplete = constants.ReasonUpgradeComplete

	// ReasonUpgradeFailed indicates the upgrade process failed.
	ReasonUpgradeFailed = constants.ReasonUpgradeFailed

	// ReasonUpgradePaused indicates the upgrade was paused.
	ReasonUpgradePaused = "UpgradePaused"

	// ReasonQuorumLost indicates the cluster has lost quorum and upgrades
	// cannot proceed safely.
	ReasonQuorumLost = "QuorumLost"

	// ReasonLeaderUnknown indicates the operator could not determine the
	// cluster leader, possibly due to split-brain or network issues.
	ReasonLeaderUnknown = constants.ReasonLeaderUnknown

	// ReasonStepDownTimeout indicates a leader step-down operation timed out.
	ReasonStepDownTimeout = "StepDownTimeout"

	// ReasonStepDownFailed indicates a leader step-down operation failed.
	ReasonStepDownFailed = "StepDownFailed"

	// ReasonPodNotReady indicates a pod failed to become ready within timeout.
	ReasonPodNotReady = "PodNotReady"

	// ReasonHealthCheckFailed indicates OpenBao health checks failed.
	ReasonHealthCheckFailed = "HealthCheckFailed"

	// ReasonPreUpgradeBackupFailed indicates the pre-upgrade backup failed.
	ReasonPreUpgradeBackupFailed = "PreUpgradeBackupFailed"

	// ReasonPreUpgradeSnapshotJobCreated indicates the pre-upgrade snapshot Job was created.
	ReasonPreUpgradeSnapshotJobCreated = "PreUpgradeSnapshotJobCreated"

	// ReasonPreUpgradeSnapshotCompleted indicates the pre-upgrade snapshot completed successfully.
	ReasonPreUpgradeSnapshotCompleted = "PreUpgradeSnapshotCompleted"

	// ReasonPreUpgradeSnapshotFailed indicates the pre-upgrade snapshot failed.
	ReasonPreUpgradeSnapshotFailed = "PreUpgradeSnapshotFailed"

	// ReasonNoUpgradeNeeded indicates spec.version matches status.currentVersion.
	ReasonNoUpgradeNeeded = "NoUpgradeNeeded"

	// ReasonVersionMismatch indicates spec.version changed during an upgrade.
	ReasonVersionMismatch = "VersionMismatch"

	// ReasonInvalidVersion indicates the target version is invalid.
	ReasonInvalidVersion = constants.ReasonInvalidVersion

	// ReasonDowngradeBlocked indicates a downgrade was attempted but blocked.
	ReasonDowngradeBlocked = constants.ReasonDowngradeBlocked

	// ReasonImageVersionMismatch indicates spec.image conflicts with spec.version.
	ReasonImageVersionMismatch = constants.ReasonImageVersionMismatch

	// ReasonUpgradeVersionIncompatible indicates the requested OpenBao
	// versions cannot safely coexist during an automated upgrade.
	ReasonUpgradeVersionIncompatible = constants.ReasonUpgradeVersionIncompatible

	// ReasonClusterNotReady indicates the cluster is not in a healthy state for upgrade.
	ReasonClusterNotReady = "ClusterNotReady"

	// ReasonOperationLockBlocked indicates an upgrade could not acquire the cluster operation lock.
	ReasonOperationLockBlocked = constants.ReasonOperationLockBlocked

	// ReasonRollingRetryRequested indicates a failed rolling upgrade retry was requested.
	ReasonRollingRetryRequested = "RollingRetryRequested"

	// ReasonRollingRetryAccepted indicates a failed rolling upgrade retry was accepted.
	ReasonRollingRetryAccepted = "RollingRetryAccepted"

	// ReasonRollingPartitionRetry indicates that a partition update will be retried.
	ReasonRollingPartitionRetry = "RollingPartitionRetry"
)

// Message constants for condition updates.
const (
	MessageUpgradeStarted             = "Upgrade from %s to %s has started"
	MessageUpgradeInProgress          = "Rolling update in progress: %d/%d replicas updated (partition: %d)"
	MessageUpgradeComplete            = "Upgrade from %s to %s finished successfully"
	MessageUpgradeFailed              = "Upgrade failed: %s"
	MessageUpgradePaused              = "Upgrade paused at partition %d"
	MessageUpgradeResumed             = "Upgrade resumed at partition %d"
	MessageStepDownTimeout            = "Leader step-down timed out for pod %s"
	MessagePodNotReady                = "Pod %s failed to become ready within %v"
	MessageHealthCheckFailed          = "OpenBao health check failed for pod %s: %s"
	MessagePreUpgradeBackupStarted    = "Pre-upgrade backup started"
	MessagePreUpgradeBackupComplete   = "Pre-upgrade backup finished successfully"
	MessageDowngradeBlocked           = "downgrade from %s to %s is not supported"
	MessageInvalidVersion             = "invalid target version %q"
	MessageInvalidImageReference      = "invalid spec.image %q"
	MessageImageVersionMismatch       = "spec.image tag %q does not match spec.version %q"
	MessageUpgradeVersionIncompatible = "upgrade from OpenBao %s to %s is blocked for RollingUpdate and BlueGreen because pre-2.6 peers cannot forward requests to a 2.6 or newer leader; keep the current server version until a compatible upgrade path is qualified"
	MessageClusterNotReady            = "Cluster is not ready for upgrade: %s"
)

// ExecutorAction selects which upgrade operation the upgrade executor performs.
type ExecutorAction = raftops.ExecutorAction

const (
	ExecutorActionBlueGreenJoinGreenNonVoters          ExecutorAction = raftops.ExecutorActionBlueGreenJoinGreenNonVoters
	ExecutorActionBlueGreenWaitGreenSynced             ExecutorAction = raftops.ExecutorActionBlueGreenWaitGreenSynced
	ExecutorActionBlueGreenPromoteGreenVoters          ExecutorAction = raftops.ExecutorActionBlueGreenPromoteGreenVoters
	ExecutorActionBlueGreenDemoteBlueNonVotersStepDown ExecutorAction = raftops.ExecutorActionBlueGreenDemoteBlueNonVotersStepDown
	ExecutorActionBlueGreenRemoveBluePeers             ExecutorAction = raftops.ExecutorActionBlueGreenRemoveBluePeers
	ExecutorActionBlueGreenRemoveGreenPeers            ExecutorAction = raftops.ExecutorActionBlueGreenRemoveGreenPeers

	// ExecutorActionBlueGreenRepairConsensus repairs Raft consensus during rollback by
	// ensuring Blue nodes are voters and Green nodes are non-voters in a single pass.
	ExecutorActionBlueGreenRepairConsensus ExecutorAction = raftops.ExecutorActionBlueGreenRepairConsensus

	ExecutorActionRollingStepDownLeader ExecutorAction = raftops.ExecutorActionRollingStepDownLeader
)
