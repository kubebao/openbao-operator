package backup

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	annotationBackupGeneration  = "openbao.org/backup-generation"
	labelRetentionRun           = "openbao.org/retention-run"
	retentionComponent          = "retention"
	retentionType               = "retention"
	retentionTimeout            = 4 * time.Minute
	retentionJobDeadlineSeconds = int64(300)

	EnvRetentionPrefix         = "RETENTION_PREFIX"
	EnvRetentionKey            = "RETENTION_KEY"
	EnvRetentionRestoreTestKey = "RETENTION_RESTORE_TEST_KEY"
	EnvRetentionMaxCount       = "RETENTION_MAX_COUNT"
	EnvRetentionMaxAge         = "RETENTION_MAX_AGE"
	EnvRetentionDeadline       = "RETENTION_DEADLINE"
	EnvRetentionSourceJobUID   = "RETENTION_SOURCE_JOB_UID"
)

// RetentionExecutorConfig needs storage credentials only. It does not discover or authenticate to OpenBao.
type RetentionExecutorConfig struct {
	Storage      *ExecutorConfig
	Prefix       string
	Policy       RetentionPolicy
	Deadline     time.Time
	SourceJobUID string
}

// Retention report reasons are shared by the executor and controller receipt handling.
const (
	RetentionReasonCompleted           = "Completed"
	RetentionReasonDeadlineExceeded    = "DeadlineExceeded"
	RetentionReasonStorageAccessFailed = "StorageAccessFailed"
	RetentionReasonStorageListFailed   = "StorageListFailed"
	RetentionReasonStorageDeleteFailed = "StorageDeleteFailed"
	RetentionReasonOutcomeInvalid      = "OutcomeInvalid"
)

// RetentionReport is a bounded worker receipt read from an owned terminated container.
type RetentionReport struct {
	SourceJobUID   string `json:"sourceJobUID"`
	Key            string `json:"key"`
	Succeeded      bool   `json:"succeeded"`
	Reason         string `json:"reason"`
	DeletedByCount int32  `json:"deletedByCount"`
	DeletedByAge   int32  `json:"deletedByAge"`
}

// LoadRetentionExecutorConfig loads a frozen storage-only retention plan.
func LoadRetentionExecutorConfig() (*RetentionExecutorConfig, error) {
	storage := &ExecutorConfig{
		ClusterNamespace: strings.TrimSpace(os.Getenv(constants.EnvClusterNamespace)),
		ClusterName:      strings.TrimSpace(os.Getenv(constants.EnvClusterName)),
	}
	if storage.ClusterName == "" || storage.ClusterNamespace == "" {
		return nil, fmt.Errorf("cluster name and namespace are required for retention")
	}
	if err := loadStorageConfig(storage); err != nil {
		return nil, err
	}
	if err := validateStorageConfiguration(storage); err != nil {
		return nil, err
	}
	if value := os.Getenv(constants.EnvBackupInsecureSkipVerify); value != "" {
		insecure, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid storage TLS setting: %w", err)
		}
		storage.InsecureSkipVerify = insecure
	}
	maxCount, err := strconv.ParseInt(os.Getenv(EnvRetentionMaxCount), 10, 32)
	if err != nil || maxCount < 0 {
		return nil, fmt.Errorf("retention maxCount must be a nonnegative integer")
	}
	maxAge, err := ParseRetentionMaxAge(os.Getenv(EnvRetentionMaxAge))
	if err != nil {
		return nil, err
	}
	deadline, err := time.Parse(time.RFC3339, os.Getenv(EnvRetentionDeadline))
	if err != nil {
		return nil, fmt.Errorf("invalid absolute retention deadline: %w", err)
	}
	prefix := GetBackupListPrefix(storage.BackupPathPrefix, storage.ClusterNamespace, storage.ClusterName)
	if os.Getenv(EnvRetentionPrefix) != prefix || !IsScheduledBackupKey(os.Getenv(EnvRetentionKey), prefix) {
		return nil, fmt.Errorf("retention prefix or protected snapshot is outside the cluster boundary")
	}
	if os.Getenv(EnvRetentionSourceJobUID) == "" {
		return nil, fmt.Errorf("retention source Job UID is required")
	}
	return &RetentionExecutorConfig{Storage: storage, Prefix: prefix, Deadline: deadline,
		SourceJobUID: os.Getenv(EnvRetentionSourceJobUID),
		Policy: RetentionPolicy{MaxCount: int32(maxCount), MaxAge: maxAge,
			ProtectedKey: os.Getenv(EnvRetentionKey), RestoreTestKey: os.Getenv(EnvRetentionRestoreTestKey)}}, nil
}
