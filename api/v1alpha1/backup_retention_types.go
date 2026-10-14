package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// BackupRetentionSucceededConditionType reports the independent retention outcome.
const BackupRetentionSucceededConditionType = "RetentionSucceeded"

// BackupRetentionStatus holds one active workload and one last result.
type BackupRetentionStatus struct {
	// Active pins the retention plan until its workload has stopped.
	// +optional
	Active *BackupRetentionRun `json:"active,omitempty"`
	// Last records the last finished or skipped retention attempt.
	// +optional
	Last *BackupRetentionResult `json:"last,omitempty"`
	// Conditions report retention progress independently of snapshot success.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BackupRetentionRun records the immutable deletion boundary and workload identity.
type BackupRetentionRun struct {
	// Name is the deterministic retention Job name.
	Name string `json:"name"`
	// UID binds the observed Job. Absence after submission is uncertain, not permission to retry.
	// +optional
	UID types.UID `json:"uid,omitempty"`
	// SourceJobName identifies the successful scheduled backup Job.
	SourceJobName string `json:"sourceJobName"`
	// SourceJobUID pins the successful backup Job.
	SourceJobUID types.UID `json:"sourceJobUID"`
	// Generation pins the source configuration used by the backup Job.
	Generation int64 `json:"generation"`
	// Key protects the last successful snapshot throughout retention.
	Key string `json:"key"`
	// Prefix confines deletion to scheduled snapshots directly under this cluster prefix.
	Prefix string `json:"prefix"`
	// RestoreTestKey protects a previously reserved restore-test snapshot.
	// +optional
	RestoreTestKey string `json:"restoreTestKey,omitempty"`
	// Policy is the frozen retention policy.
	Policy BackupRetention `json:"policy"`
	// StartedAt records reservation time.
	StartedAt metav1.Time `json:"startedAt"`
	// DeadlineAt is the absolute worker deadline, including time spent awaiting submission.
	DeadlineAt metav1.Time `json:"deadlineAt"`
	// SubmittedAt records the single permitted Create attempt before the API call.
	// +optional
	SubmittedAt *metav1.Time `json:"submittedAt,omitempty"`
}

// BackupRetentionOutcome separates cleanup results from snapshot results.
// +kubebuilder:validation:Enum=Succeeded;Failed;Skipped
type BackupRetentionOutcome string

const (
	BackupRetentionSucceeded BackupRetentionOutcome = "Succeeded"
	BackupRetentionFailed    BackupRetentionOutcome = "Failed"
	BackupRetentionSkipped   BackupRetentionOutcome = "Skipped"
)

// BackupRetentionResult is the last independently observed cleanup outcome.
type BackupRetentionResult struct {
	SourceJobUID types.UID `json:"sourceJobUID"`
	Name         string    `json:"name"`
	// Key identifies the successful snapshot that triggered this attempt.
	Key        string                 `json:"key"`
	StartedAt  metav1.Time            `json:"startedAt"`
	FinishedAt metav1.Time            `json:"finishedAt"`
	Outcome    BackupRetentionOutcome `json:"outcome"`
	// Reason is a stable reason code. Message describes the outcome and next action.
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// DeletedByCount and DeletedByAge count confirmed successful batch deletions.
	// A failed batch can have partial effects; its unknown partial count is not reported as success.
	// +kubebuilder:validation:Minimum=0
	DeletedByCount int32 `json:"deletedByCount"`
	// +kubebuilder:validation:Minimum=0
	DeletedByAge int32 `json:"deletedByAge"`
}
