package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// RestoreTargetLifecycle determines whether a fresh target is retained or deleted.
type RestoreTargetLifecycle string

const (
	RestoreTargetLifecycleRetain     RestoreTargetLifecycle = "Retain"
	RestoreTargetLifecycleDisposable RestoreTargetLifecycle = "Disposable"
)

// RestoreTargetCleanup describes deletion of a disposable target's resources.
type RestoreTargetCleanup string

const (
	RestoreTargetCleanupPending  RestoreTargetCleanup = "Pending"
	RestoreTargetCleanupComplete RestoreTargetCleanup = "Complete"
	RestoreTargetCleanupFailed   RestoreTargetCleanup = "Failed"
)

// RestoreAdministratorDisposition records the administrator's recovery decision.
type RestoreAdministratorDisposition string

const (
	RestoreAdministratorResume  RestoreAdministratorDisposition = "Resume"
	RestoreAdministratorAbandon RestoreAdministratorDisposition = "Abandon"
)

// RestoreClusterTemplate is the supported fresh recovery target profile.
// Administrators must prepare the destination namespace network boundary.
// +kubebuilder:validation:XValidation:rule="self.tls.enabled && self.tls.mode in ['OperatorManaged', 'External'] && !has(self.tls.acme)",message="restore targets require OperatorManaged or External TLS"
// +kubebuilder:validation:XValidation:rule="self.unseal.type != 'static' || (has(self.unseal.credentialsSecretRef) && size(self.unseal.credentialsSecretRef.name) > 0)",message="static restore targets require the snapshot's static key in credentialsSecretRef"
// +kubebuilder:validation:XValidation:rule="!has(self.unseal.transit) || (has(self.unseal.credentialsSecretRef) && size(self.unseal.credentialsSecretRef.name) > 0 && (!has(self.unseal.transit.token) || size(self.unseal.transit.token) == 0) && (!has(self.unseal.transit.tlsSkipVerify) || !self.unseal.transit.tlsSkipVerify))",message="Transit restore targets require credential references and TLS verification"
// +kubebuilder:validation:XValidation:rule="self.unseal.type in ['static', 'transit', 'kmip', 'kms'] || (has(self.plugins) && self.plugins.exists(p, p.type == 'kms' && p.name == self.unseal.type))",message="OpenBao 2.7 restore targets require a matching KMS plugin for external seal providers"
// +kubebuilder:validation:XValidation:rule="!has(self.plugins) || self.plugins.all(p, p.type == 'kms')",message="restore target plugins are limited to KMS seal plugins"
// +kubebuilder:validation:XValidation:rule="!has(self.initContainer) || self.initContainer.enabled",message="restore targets require the configuration init container"
type RestoreClusterTemplate struct {
	// Version must match the administrator-observed snapshot source version.
	// +kubebuilder:validation:Enum="2.7.0";"2.7.1"
	Version string `json:"version"`

	// Image defaults to the version-derived image.
	// +optional
	Image   string        `json:"image,omitempty"`
	Storage StorageConfig `json:"storage"`
	TLS     TLSConfig     `json:"tls"`

	// Unseal uses the same provider configuration as OpenBaoCluster. The target
	// must be able to decrypt the snapshot with the original seal key material.
	Unseal UnsealConfig `json:"unseal"`

	// ServiceAccount configures credentials supplied through workload identity.
	// +optional
	ServiceAccount *ServiceAccountConfig `json:"serviceAccount,omitempty"`

	// PodMetadata supplies provider-specific workload identity metadata.
	// +optional
	PodMetadata *PodMetadataConfig `json:"podMetadata,omitempty"`

	// Plugins declares KMS seal plugins required by Unseal. OpenBao 2.7 requires
	// plugins for AWS, Azure, GCP, OCI, and PKCS#11 seals.
	// +optional
	Plugins []Plugin `json:"plugins,omitempty"`

	// Configuration configures KMS plugin behavior for the target server.
	// +optional
	Configuration *RestoreTargetConfiguration `json:"configuration,omitempty"`

	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// +optional
	InitContainer *InitContainerConfig `json:"initContainer,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=2
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// RestoreTargetConfiguration exposes server settings needed by KMS seal plugins.
type RestoreTargetConfiguration struct {
	// Plugin controls downloads, registration, and file ownership for Plugins.
	// Set autoDownload to true to download declared OCI plugins on startup.
	// +optional
	Plugin *PluginConfig `json:"plugin,omitempty"`
}

// RestoreTargetStatus records the single creation attempt and original identities.
// Cleanup reports Kubernetes object deletion, never physical process fencing.
type RestoreTargetStatus struct {
	// ReservedAt records the single target creation attempt.
	ReservedAt metav1.Time `json:"reservedAt"`

	// UID identifies the target OpenBaoCluster created for this request.
	// +optional
	UID types.UID `json:"uid,omitempty"`

	// DataPVCUID identifies the target's original data PersistentVolumeClaim.
	// +optional
	DataPVCUID types.UID `json:"dataPVCUID,omitempty"`

	// BootstrapClusterID is the native cluster ID of the empty target before restore.
	// +optional
	BootstrapClusterID string `json:"bootstrapClusterID,omitempty"`

	// AppliedAt records when the target reported the expected source identity.
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`

	// Cleanup is Pending, Complete, or Failed. A failed ownership check preserves
	// the refused resource; cleanup can still delete the original bound cluster.
	// +kubebuilder:validation:Enum=Pending;Complete;Failed
	// +optional
	Cleanup RestoreTargetCleanup `json:"cleanup,omitempty"`
}
