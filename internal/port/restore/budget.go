package restore

import (
	"fmt"
	"math"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	mib                        = int64(1024 * 1024)
	restoreRuntimeStorageBytes = constants.RestoreRuntimeStorageBytes
)

// SnapshotBudget bounds staging and reserves disk storage for one restore.
type SnapshotBudget struct {
	MaxBytes              int64
	ScratchBytes          int64
	EphemeralStorageBytes int64
}

// ResolveSnapshotBudget retains the legacy limit when no budget is supplied.
// Storage includes ten percent headroom, rounded up to MiB, plus runtime space.
func ResolveSnapshotBudget(maxBytes int64) (SnapshotBudget, error) {
	if maxBytes == 0 {
		maxBytes = constants.DefaultRestoreSnapshotLimitBytes
	}
	if maxBytes < 0 {
		return SnapshotBudget{}, fmt.Errorf("maximum snapshot size must be positive")
	}
	headroom := maxBytes / 10
	if maxBytes%10 != 0 {
		headroom++
	}
	if maxBytes > math.MaxInt64-headroom-(mib-1)-restoreRuntimeStorageBytes {
		return SnapshotBudget{}, fmt.Errorf("maximum snapshot size is too large to reserve restore storage")
	}
	scratch := ((maxBytes + headroom + mib - 1) / mib) * mib
	return SnapshotBudget{
		MaxBytes: maxBytes, ScratchBytes: scratch,
		EphemeralStorageBytes: scratch + restoreRuntimeStorageBytes,
	}, nil
}
