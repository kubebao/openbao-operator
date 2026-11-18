package backup

import "github.com/kubebao/openbao-operator/internal/platform/constants"

// Reason constants for OpenBaoBackup conditions.
const (
	// ReasonBackupManualTriggerAccepted indicates a manual backup request was accepted.
	ReasonBackupManualTriggerAccepted = "BackupManualTriggerAccepted"

	// ReasonBackupManualTriggerRejected indicates an oversized manual request was rejected.
	ReasonBackupManualTriggerRejected = "BackupManualTriggerRejected"

	// ReasonBackupSkipped indicates a due backup was intentionally skipped.
	ReasonBackupSkipped = "BackupSkipped"

	// ReasonBackupStarted indicates the operator has started a backup attempt.
	ReasonBackupStarted = "BackupStarted"

	// ReasonBackupIdentityConfiguration describes how the generated backup workload receives cloud identity.
	ReasonBackupIdentityConfiguration = "BackupIdentityConfiguration"

	// ReasonBackupJobCreated indicates the backup Job was created successfully.
	ReasonBackupJobCreated = "BackupJobCreated"

	// ReasonBackupCompleted indicates the backup completed successfully.
	ReasonBackupCompleted = "BackupCompleted"

	// ReasonBackupFailed indicates the backup failed.
	ReasonBackupFailed = "BackupFailed"

	// ReasonOperationLockBlocked indicates backup could not proceed because another operation holds the cluster lock.
	ReasonOperationLockBlocked = constants.ReasonOperationLockBlocked

	// ComponentBackup is the component name for backup resources.
	ComponentBackup = "backup"
)
