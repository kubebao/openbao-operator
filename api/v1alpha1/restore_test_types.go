package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// RestoreTest schedules one disposable restore at a time.
// The destination must be an administrator-prepared operator-managed namespace.
// +kubebuilder:validation:XValidation:rule="has(self.schedule) != has(self.everySuccessfulBackups)",message="choose a schedule or everySuccessfulBackups"
// +kubebuilder:validation:XValidation:rule="self.clusterTemplate.tls.mode == 'OperatorManaged'",message="scheduled restore tests require OperatorManaged TLS for generated target names"
// +kubebuilder:validation:XValidation:rule="(!has(self.roleArn) && !has(self.workloadIdentity)) || has(self.serviceAccountName)",message="restore test cloud identities require an existing destination serviceAccountName"
// +kubebuilder:validation:XValidation:rule="has(self.serviceAccountName) || (has(self.credentialsSecretRef) && has(self.credentialsSecretRef.name) && size(self.credentialsSecretRef.name) > 0)",message="restore tests require destination reader credentials or an existing reader serviceAccountName"
type RestoreTest struct {
	// Schedule uses five-field cron syntax and UTC.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:MinLength=1
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	// +optional
	EverySuccessfulBackups int64 `json:"everySuccessfulBackups,omitempty"`

	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	Namespace       string                 `json:"namespace"`
	ClusterTemplate RestoreClusterTemplate `json:"clusterTemplate"`

	// CredentialsSecretRef is the existing destination storage credential Secret.
	// Storage connection settings are inherited from the source backup target.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// ServiceAccountName selects an existing executor ServiceAccount in Namespace.
	// The operator does not create, modify, or delete this account. It must have
	// its own storage-reader identity; backup writer identities are not inherited.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// RoleARN selects the S3 reader role independently of the backup writer role.
	// +kubebuilder:validation:MinLength=1
	// +optional
	RoleARN string `json:"roleArn,omitempty"`

	// WorkloadIdentity supplies destination reader metadata. ServiceAccount
	// annotations must already exist on ServiceAccountName; PodLabels apply to
	// the executor Pod. The operator does not update the existing account.
	// +optional
	WorkloadIdentity *WorkloadIdentityConfig `json:"workloadIdentity,omitempty"`

	// CleanupAfterSeconds delays deletion after snapshot application to permit inspection.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=86400
	// +optional
	CleanupAfterSeconds int32 `json:"cleanupAfterSeconds,omitempty"`
}

// BackupSnapshotSummary is the latest successful executor's bounded observation.
// Identity and version are observed before and after streaming the snapshot.
type BackupSnapshotSummary struct {
	// Digest is sha256: followed by the lowercase digest of the uploaded snapshot.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	Digest string `json:"digest"`
	// ClusterID is the source native cluster ID observed during the backup.
	// +kubebuilder:validation:MaxLength=128
	ClusterID string `json:"clusterID"`
	// Version is the source OpenBao version observed during the backup.
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version"`
	// Size is the snapshot size in bytes.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8589934592
	Size int64 `json:"size"`
}

// RestoreTestStatus stores at most one active request and one last result.
type RestoreTestStatus struct {
	// +optional
	Active *RestoreTestRun `json:"active,omitempty"`

	// +optional
	Last *RestoreTestResult `json:"last,omitempty"`

	// LastSuccessTime survives later failures and controller restarts.
	// +optional
	LastSuccessTime *metav1.Time `json:"lastSuccessTime,omitempty"`

	// LastScheduledAt records the last time a restore test was started.
	// +optional
	LastScheduledAt *metav1.Time `json:"lastScheduledAt,omitempty"`

	// LastBackupCount is the successful-backup count at the last started test.
	// +optional
	LastBackupCount int64 `json:"lastBackupCount,omitempty"`

	// Conditions reports scheduling and restore test outcomes without secret data.
	// The Passed condition summarizes the current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RestoreTestRun reserves one child request and protects its source key.
type RestoreTestRun struct {
	// Namespace is the destination namespace of the child OpenBaoRestore.
	Namespace string `json:"namespace"`
	// Name is the generated name of the child OpenBaoRestore.
	Name string `json:"name"`
	// Key is the snapshot object under test; retention protects it until the run ends.
	Key string `json:"key"`
	// StartedAt records when the run was reserved.
	StartedAt metav1.Time `json:"startedAt"`

	// UID identifies the child OpenBaoRestore once its creation is observed.
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// RestoreTestOutcome reports whether a disposable restore test passed or failed.
// +kubebuilder:validation:Enum=Passed;Failed
type RestoreTestOutcome string

// RestoreTestPassedConditionType summarizes the current restore test state.
const RestoreTestPassedConditionType = "Passed"

const (
	RestoreTestPassed RestoreTestOutcome = "Passed"
	RestoreTestFailed RestoreTestOutcome = "Failed"
)

// RestoreTestResult contains no secret data or authentication tokens.
type RestoreTestResult struct {
	// Name is the child OpenBaoRestore that produced this result.
	Name string `json:"name"`
	// FinishedAt records when the result was recorded.
	FinishedAt metav1.Time `json:"finishedAt"`

	// Namespace identifies the destination namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key identifies the tested snapshot object in backup storage.
	// +optional
	Key string `json:"key,omitempty"`

	// Digest is the pinned snapshot digest, when available.
	// +optional
	Digest string `json:"digest,omitempty"`

	// Reason and Message retain a bounded summary after the child is deleted.
	// They contain operator-defined diagnostics, never provider responses or logs.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message contains the operator-defined diagnostic for this result.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Message string `json:"message,omitempty"`

	// Outcome is Passed or Failed.
	Outcome RestoreTestOutcome `json:"outcome"`
}
