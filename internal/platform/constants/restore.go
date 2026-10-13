package constants

import "time"

// Restore staging uses disk-backed storage. These bounds also apply to legacy
// key-only sources, whose size is unknown until the executor reads object metadata.
const (
	DefaultRestoreSnapshotLimitBytes    int64 = 8 * 1024 * 1024 * 1024
	DefaultRestoreScratchLimitBytes     int64 = 9012 * 1024 * 1024 // 8 GiB + ceilMiB(10%).
	DefaultRestoreEphemeralStorageBytes int64 = DefaultRestoreScratchLimitBytes + 128*1024*1024
	DefaultRestorePreparationTimeout          = 30 * time.Minute
	PathRestoreScratch                        = "/var/lib/openbao-restore"
	EnvRestorePreparationDeadline             = "RESTORE_PREPARATION_DEADLINE"
)
