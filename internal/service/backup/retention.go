package backup

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubebao/openbao-operator/internal/port/blobstore"
)

// RetentionPolicy defines the retention policy for backups.
type RetentionPolicy struct {
	// MaxCount is the maximum number of backups to retain. 0 means unlimited.
	MaxCount int32
	// MaxAge is the maximum age of backups to retain. Zero means no age limit.
	MaxAge time.Duration
	// ProtectedKey is never deleted, typically the most recently recorded backup.
	ProtectedKey string
	// RestoreTestKey protects the active restore test source until cleanup finishes.
	RestoreTestKey string
}

// RetentionResult contains the result of a retention policy application.
type RetentionResult struct {
	// TotalBackups is the number of scheduled backups found before retention.
	TotalBackups int
	// SkippedObjects is the number of objects under the prefix that are not scheduled
	// backups, such as pre-upgrade snapshots. Retention never deletes them.
	SkippedObjects int
	// DeletedByCount is the number of backups deleted due to MaxCount.
	DeletedByCount int
	// DeletedByAge is the number of backups deleted due to MaxAge.
	DeletedByAge int
	// Errors contains any errors encountered during deletion.
	Errors []error
}

// ApplyRetention applies retention policy to scheduled backups in the given prefix.
// It lists scheduled backups, sorts them by timestamp (newest first), and deletes
// backups that exceed MaxCount or are older than MaxAge.
//
// Retention is applied after a successful backup upload:
// 1. List all objects matching the prefix and keep only scheduled backup keys
// 2. Sort by timestamp (newest first)
// 3. If MaxCount > 0: Delete all objects beyond MaxCount
// 4. If MaxAge is set: Delete all objects older than Now - MaxAge
//
// The newest backup and policy.ProtectedKey are always retained, so retention
// never leaves the prefix without a restorable snapshot.
func ApplyRetention(
	ctx context.Context,
	logger logr.Logger,
	storageClient blobstore.BlobStore,
	prefix string,
	policy RetentionPolicy,
) (*RetentionResult, error) {
	if policy.MaxCount == 0 && policy.MaxAge == 0 {
		// No retention policy configured
		return &RetentionResult{}, nil
	}

	// List all backups
	objects, err := storageClient.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list backups for retention: %w", err)
	}

	if len(objects) == 0 {
		return &RetentionResult{}, nil
	}

	result := &RetentionResult{}

	// Parse backup timestamps and sort by time (newest first)
	type backupInfo struct {
		key       string
		timestamp time.Time
	}

	backups := make([]backupInfo, 0, len(objects))
	for _, obj := range objects {
		if !IsScheduledBackupKey(obj.Key, prefix) {
			result.SkippedObjects++
			continue
		}
		_, _, timestamp, _, parseErr := ParseBackupKey(obj.Key)
		if parseErr != nil {
			result.SkippedObjects++
			continue
		}
		backups = append(backups, backupInfo{
			key:       obj.Key,
			timestamp: timestamp,
		})
	}
	result.TotalBackups = len(backups)
	if result.SkippedObjects > 0 {
		logger.V(1).Info("Retention ignored objects that are not scheduled backups",
			"prefix", prefix,
			"skipped", result.SkippedObjects)
	}

	// Sort by timestamp, newest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].timestamp.After(backups[j].timestamp)
	})

	// Determine which backups to delete
	keysToDelete := make([]string, 0)
	now := time.Now().UTC()

	// Use int32 for loop index to match MaxCount type and avoid overflow
	// #nosec G115 -- len(backups) is bounded by practical limits (< 2^31), conversion is safe
	backupsLen := int32(len(backups))
	for i := int32(0); i < backupsLen; i++ {
		backup := backups[i]
		if i == 0 || (policy.RestoreTestKey != "" && backup.key == policy.RestoreTestKey) || (policy.ProtectedKey != "" && backup.key == policy.ProtectedKey) {
			continue
		}
		shouldDelete := false

		// Check MaxCount
		if policy.MaxCount > 0 && i >= policy.MaxCount {
			shouldDelete = true
			result.DeletedByCount++
		}

		// Check MaxAge (only if not already marked for deletion by count)
		if !shouldDelete && policy.MaxAge > 0 {
			age := now.Sub(backup.timestamp)
			if age > policy.MaxAge {
				shouldDelete = true
				result.DeletedByAge++
			}
		}

		if shouldDelete {
			keysToDelete = append(keysToDelete, backup.key)
		}
	}

	// Delete backups
	if len(keysToDelete) > 0 {
		logger.Info("Applying retention policy",
			"prefix", prefix,
			"totalBackups", result.TotalBackups,
			"toDelete", len(keysToDelete),
			"byCount", result.DeletedByCount,
			"byAge", result.DeletedByAge,
		)

		// Use batch delete for efficiency
		if err := storageClient.DeleteBatch(ctx, keysToDelete); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("failed to delete backups: %w", err))
			// DeleteBatch does not identify partial successes. Do not count planned
			// deletions as confirmed deletions when any part of the batch fails.
			result.DeletedByCount, result.DeletedByAge = 0, 0
			// Log but don't fail - retention errors shouldn't fail the backup
			logger.Error(err, "Failed to delete some backups during retention",
				"keysAttempted", len(keysToDelete))
		} else {
			logger.Info("Retention policy applied successfully",
				"deleted", len(keysToDelete))
		}
	}

	return result, nil
}

// ParseRetentionMaxAge parses a duration string for MaxAge retention.
// Accepts Go duration strings like "168h" (7 days), "720h" (30 days), etc.
func ParseRetentionMaxAge(maxAge string) (time.Duration, error) {
	if maxAge == "" {
		return 0, nil
	}

	duration, err := time.ParseDuration(maxAge)
	if err != nil {
		return 0, fmt.Errorf("invalid maxAge duration %q: %w", maxAge, err)
	}

	if duration <= 0 {
		return 0, fmt.Errorf("maxAge must be a positive duration, got %v", duration)
	}

	return duration, nil
}
