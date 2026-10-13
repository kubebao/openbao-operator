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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// RestorePhase represents the current phase of a restore operation.
// +kubebuilder:validation:Enum=Pending;Validating;Running;Completed;Failed;Unknown
type RestorePhase string

const (
	// OpenBaoRestoreFinalizer is the finalizer used to ensure lock cleanup logic
	// runs before an OpenBaoRestore is fully deleted.
	OpenBaoRestoreFinalizer = "openbao.org/openbaorestore-finalizer"

	// RestorePhasePending indicates the restore has been created but not yet started.
	RestorePhasePending RestorePhase = "Pending"
	// RestorePhaseValidating indicates the controller is validating preconditions.
	RestorePhaseValidating RestorePhase = "Validating"
	// RestorePhaseRunning indicates the restore job is executing.
	RestorePhaseRunning RestorePhase = "Running"
	// RestorePhaseCompleted indicates the restore completed successfully.
	RestorePhaseCompleted RestorePhase = "Completed"
	// RestorePhaseFailed indicates the restore failed.
	RestorePhaseFailed RestorePhase = "Failed"
	// RestorePhaseUnknown indicates the controller cannot determine whether the
	// destructive restore operation ran. The controller does not retry an
	// execution in this phase.
	RestorePhaseUnknown RestorePhase = "Unknown"
)

// RestoreExecutionStage identifies the durable execution boundary reached by a restore.
// +kubebuilder:validation:Enum=Prepared;Committed;Created;TerminalObserved;FollowThroughComplete;Unknown
type RestoreExecutionStage string

const (
	// RestoreExecutionStagePrepared indicates validation and resource preparation
	// completed, but Job creation has not been committed.
	RestoreExecutionStagePrepared RestoreExecutionStage = "Prepared"
	// RestoreExecutionStageCommitted indicates the controller durably committed to
	// one Job creation attempt. A missing Job after this point is ambiguous and is
	// not recreated automatically.
	RestoreExecutionStageCommitted RestoreExecutionStage = "Committed"
	// RestoreExecutionStageCreated indicates the controller persisted the created Job identity.
	RestoreExecutionStageCreated RestoreExecutionStage = "Created"
	// RestoreExecutionStageTerminalObserved indicates the controller persisted the terminal Job result.
	RestoreExecutionStageTerminalObserved RestoreExecutionStage = "TerminalObserved"
	// RestoreExecutionStageFollowThroughComplete indicates post-restore voter and
	// read-replica recovery completed.
	RestoreExecutionStageFollowThroughComplete RestoreExecutionStage = "FollowThroughComplete"
	// RestoreExecutionStageUnknown indicates the controller cannot prove whether
	// the committed execution ran.
	RestoreExecutionStageUnknown RestoreExecutionStage = "Unknown"
)

// RestoreExecutionResult is the persisted terminal result of a restore Job.
// +kubebuilder:validation:Enum=Succeeded;Failed
type RestoreExecutionResult string

const (
	// RestoreExecutionResultSucceeded indicates the restore Job succeeded.
	RestoreExecutionResultSucceeded RestoreExecutionResult = "Succeeded"
	// RestoreExecutionResultFailed indicates the restore Job failed.
	RestoreExecutionResultFailed RestoreExecutionResult = "Failed"
)

// RestoreExecutionStatus records the identity and durable receipts for one restore execution.
type RestoreExecutionStatus struct {
	// TargetUID prevents a replacement cluster from inheriting this execution.
	// +optional
	TargetUID types.UID `json:"targetUID,omitempty"`

	// OperationID identifies this immutable restore execution.
	OperationID string `json:"operationID"`

	// Stage is the latest durable execution boundary observed by the controller.
	Stage RestoreExecutionStage `json:"stage"`

	// JobName is the expected restore Job name for this execution.
	JobName string `json:"jobName"`

	// JobUID is the UID returned for the created restore Job.
	// +optional
	JobUID types.UID `json:"jobUID,omitempty"`

	// PreparedAt is when validation and execution preparation completed.
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
	TerminalResult RestoreExecutionResult `json:"terminalResult,omitempty"`

	// TerminalObservedAt is when the controller persisted the terminal Job result.
	// +optional
	TerminalObservedAt *metav1.Time `json:"terminalObservedAt,omitempty"`

	// FollowThroughCompletedAt is when post-restore recovery completed.
	// +optional
	FollowThroughCompletedAt *metav1.Time `json:"followThroughCompletedAt,omitempty"`
}

// RestoreSource defines where the snapshot comes from.
type RestoreSource struct {
	// ExpectedClusterID is the native ID observed on the snapshot source.
	// Required for a fresh target; supplied metadata is an administrator assertion.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ExpectedClusterID string `json:"expectedClusterID,omitempty"`
	// ExpectedVersion is the source version observed when taking the snapshot.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	// ExpectedDigest pins the staged bytes before submission.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +optional
	ExpectedDigest string `json:"expectedDigest,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8589934592
	// +optional
	ExpectedSize int64 `json:"expectedSize,omitempty"`

	// Target reuses BackupTarget for storage connection details.
	// This includes endpoint, bucket, region, credentials, etc.
	Target BackupTarget `json:"target"`

	// Key is the full path to the snapshot object in the bucket.
	// For example, "clusters/prod/2025-10-14-120000.snap".
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// OpenBaoRestoreSpec defines the desired state for a restore operation.
// An OpenBaoRestore acts as a "job request" - it is immutable after creation.
// +kubebuilder:validation:XValidation:rule="!has(self.clusterTemplate) || (has(self.targetLifecycle) && self.force && !has(self.tokenSecretRef) && !has(self.jwtAuthRole) && (!has(self.overrideOperationLock) || !self.overrideOperationLock) && has(self.source.expectedClusterID) && size(self.source.expectedClusterID) > 0 && has(self.source.expectedVersion) && self.source.expectedVersion == self.clusterTemplate.version)",message="fresh targets require a lifecycle, matching source identity/version, force, and generated JWT authentication"
// +kubebuilder:validation:XValidation:rule="has(self.clusterTemplate) || !has(self.targetLifecycle)",message="targetLifecycle requires clusterTemplate"
// +kubebuilder:validation:XValidation:rule="!has(self.cleanupAfterSeconds) || (has(self.targetLifecycle) && self.targetLifecycle == 'Disposable')",message="cleanupAfterSeconds requires a disposable fresh target"
type OpenBaoRestoreSpec struct {
	// CleanupAfterSeconds delays disposable target cleanup after snapshot application is confirmed.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=86400
	// +optional
	CleanupAfterSeconds int32 `json:"cleanupAfterSeconds,omitempty"`

	// ClusterTemplate creates a new single-voter target. Existing targets are rejected.
	// +optional
	ClusterTemplate *RestoreClusterTemplate `json:"clusterTemplate,omitempty"`
	// TargetLifecycle retains recovery targets or deletes disposable test resources.
	// +kubebuilder:validation:Enum=Retain;Disposable
	// +optional
	TargetLifecycle RestoreTargetLifecycle `json:"targetLifecycle,omitempty"`

	// Cluster is the name of the OpenBaoCluster to restore INTO.
	// Must be in the request namespace. ClusterTemplate requires a new name.
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// Source defines where the snapshot comes from.
	Source RestoreSource `json:"source"`

	// ServiceAccountName selects an existing executor ServiceAccount in this
	// namespace. The operator does not create, modify, or delete this account.
	// When omitted, it creates <cluster-name>-restore-serviceaccount.
	// JWT authentication must authorize the selected account. Fresh targets add
	// its subject to their generated restore role before snapshot submission.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// JWTAuthRole is the name of the JWT Auth role configured in OpenBao
	// for restore operations. When set, the restore executor will use JWT Auth
	// (projected ServiceAccount token) instead of a static token.
	//
	// The role must be configured in OpenBao and must grant the "update" capability on
	// sys/storage/raft/snapshot. To support force: true, it must also grant "update" on
	// sys/storage/raft/snapshot-force. The role must bind to the restore ServiceAccount
	// selected by serviceAccountName, or <cluster-name>-restore-serviceaccount
	// when serviceAccountName is omitted, in the cluster namespace.
	//
	// If this field is empty and the target OpenBaoCluster has OIDC enabled,
	// the operator will default to using the "openbao-operator-restore" role.
	// +optional
	JWTAuthRole string `json:"jwtAuthRole,omitempty"`

	// TokenSecretRef optionally references a Secret containing an OpenBao API
	// token to use for restore operations (fallback method).
	//
	// The Secret must exist in the same namespace as the OpenBaoRestore.
	// Cross-namespace references are not allowed for security reasons.
	//
	// The token must have permission to update sys/storage/raft/snapshot. To support
	// force: true, it must also have permission to update
	// sys/storage/raft/snapshot-force.
	//
	// If JWTAuthRole is set, this field is ignored in favor of JWT Auth.
	// +optional
	TokenSecretRef *corev1.LocalObjectReference `json:"tokenSecretRef,omitempty"`

	// Image is the container image to use for restore operations.
	// Defaults to the same image used for backup operations if not specified.
	// If the target OpenBaoCluster has image verification enabled, the operator will verify this image and pin the restore Job to the verified digest.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Image string `json:"image,omitempty"`

	// Force uses OpenBao's force-restore endpoint. This bypasses verification that
	// the snapshot is compatible with the target cluster's Shamir or auto-unseal
	// configuration. It also skips the controller checks that require the target
	// cluster to be initialized and not upgrading.
	//
	// Use this break-glass option only when the normal verified restore cannot run
	// and the snapshot source and target seal compatibility have been validated by
	// another trusted process.
	// +kubebuilder:default=false
	// +optional
	Force bool `json:"force,omitempty"`

	// OverrideOperationLock allows the restore controller to clear an active cluster
	// operation lock (upgrade/backup) and proceed with restore. This is a break-glass
	// escape hatch intended for disaster recovery.
	//
	// For safety, this requires force: true. When used, the controller emits a Warning
	// event and records a Condition on the OpenBaoRestore.
	//
	// +kubebuilder:default=false
	// +optional
	OverrideOperationLock bool `json:"overrideOperationLock,omitempty"`
}

// RestoreSubmissionClaim is a one-way submission reservation. A persisted claim
// never authorizes a restarted executor to submit again.
type RestoreSubmissionClaim struct {
	// PodUID identifies the executor Pod that won the claim.
	PodUID types.UID `json:"podUID"`
	// TargetPodName is the OpenBao Pod that received the snapshot.
	TargetPodName string `json:"targetPodName"`
	// TargetPodUID identifies the OpenBao Pod that received the snapshot.
	TargetPodUID types.UID `json:"targetPodUID"`
	// TargetPodIP is the address the executor connected to.
	TargetPodIP string `json:"targetPodIP"`
	// TargetContainerID identifies the OpenBao container that received the snapshot.
	TargetContainerID string `json:"targetContainerID"`
	// Digest is sha256: followed by the lowercase digest of the staged snapshot bytes.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	Digest string `json:"digest"`
	// Size is the staged snapshot size in bytes.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8589934592
	Size int64 `json:"size"`
	// ClaimedAt records when submission was reserved.
	ClaimedAt metav1.Time `json:"claimedAt"`
}

// RestoreRestartPod binds a managed restart to an original Pod and its StatefulSet.
type RestoreRestartPod struct {
	// Name is the original Pod name.
	Name string `json:"name"`
	// UID identifies the original Pod; a different UID marks a replacement.
	UID types.UID `json:"uid"`
	// StatefulSetUID identifies the StatefulSet that owned the original Pod.
	StatefulSetUID types.UID `json:"statefulSetUID"`
}

// RestoreRestartStatus records Resume before restarting workloads. Replacement
// Pod UIDs provide retry evidence; this status does not prove snapshot application.
type RestoreRestartStatus struct {
	// Pods contains the original voters and read replicas.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	Pods []RestoreRestartPod `json:"pods"`
	// CompletedAt records that all bound Pods were replaced and became ready.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// OpenBaoRestoreStatus defines the observed state of OpenBaoRestore.
type OpenBaoRestoreStatus struct {
	// Target binds the fresh cluster and storage to this request.
	// +optional
	Target *RestoreTargetStatus `json:"target,omitempty"`

	// SubmissionClaim reserves the only permitted snapshot submission.
	// +optional
	SubmissionClaim *RestoreSubmissionClaim `json:"submissionClaim,omitempty"`

	// Restart records managed workload recovery after administrator Resume.
	// +optional
	Restart *RestoreRestartStatus `json:"restart,omitempty"`

	// AdministratorDisposition records an operation-bound recovery acknowledgement.
	// +kubebuilder:validation:Enum=Resume;Abandon
	// +optional
	AdministratorDisposition RestoreAdministratorDisposition `json:"administratorDisposition,omitempty"`

	// Phase represents the current phase of the restore operation.
	// +kubebuilder:default=Pending
	Phase RestorePhase `json:"phase,omitempty"`

	// StartTime is when the restore operation started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the restore operation completed (success or failure).
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Execution records the stable operation identity and durable lifecycle receipts.
	// +optional
	Execution *RestoreExecutionStatus `json:"execution,omitempty"`

	// SnapshotKey is the key of the snapshot that was restored.
	// +optional
	SnapshotKey string `json:"snapshotKey,omitempty"`

	// SnapshotSize is the size of the restored snapshot in bytes.
	// +optional
	SnapshotSize int64 `json:"snapshotSize,omitempty"`

	// Message provides additional details about the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations of the restore's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=obrestore
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.cluster"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Recovery",type="string",JSONPath=".status.conditions[?(@.type=='RecoveryReleased')].reason"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.message",priority=1

// OpenBaoRestore represents a request to restore an OpenBao cluster from a snapshot.
// This resource is immutable after creation - it acts as a "job request".
type OpenBaoRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoRestoreSpec   `json:"spec"`
	Status OpenBaoRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenBaoRestoreList contains a list of OpenBaoRestore.
type OpenBaoRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenBaoRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenBaoRestore{}, &OpenBaoRestoreList{})
}
