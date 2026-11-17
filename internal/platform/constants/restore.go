package constants

import "time"

// Restore staging uses disk-backed storage. These bounds also apply to legacy
// key-only sources, whose size is unknown until the executor reads object metadata.
const (
	// AnnotationRestoreHold blocks ordinary management until administrator release.
	AnnotationRestoreHold = "openbao.org/restore-hold"
	// AnnotationRestoreAcknowledge carries <operationID>/Resume or /Abandon.
	AnnotationRestoreAcknowledge = "openbao.org/restore-acknowledge"

	// AnnotationRestoreTestAcknowledge carries <run-name>/Release on the source.
	AnnotationRestoreTestAcknowledge = "openbao.org/restore-test-acknowledge"

	DefaultRestoreSnapshotLimitBytes    int64 = 8 * 1024 * 1024 * 1024
	DefaultRestoreScratchLimitBytes     int64 = 9012 * 1024 * 1024 // 8 GiB + ceilMiB(10%).
	RestoreRuntimeStorageBytes          int64 = 128 * 1024 * 1024
	DefaultRestoreEphemeralStorageBytes int64 = DefaultRestoreScratchLimitBytes + RestoreRuntimeStorageBytes
	DefaultRestorePreparationTimeout          = 30 * time.Minute
	PathRestoreScratch                        = "/var/lib/openbao-restore"
	EnvRestorePreparationDeadline             = "RESTORE_PREPARATION_DEADLINE"
	EnvRestoreMaxSnapshotSizeBytes            = "RESTORE_MAX_SNAPSHOT_SIZE_BYTES"
)
