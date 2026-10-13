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
	"k8s.io/apimachinery/pkg/api/resource"
)

// StorageConfig captures storage-related configuration for the StatefulSet.
type StorageConfig struct {
	// Size is the requested persistent volume size, for example "10Gi".
	// +kubebuilder:validation:MinLength=1
	Size string `json:"size"`
	// StorageClassName is an optional StorageClass for the PVCs.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// ServiceConfig controls how the main OpenBao Service is exposed.
type ServiceConfig struct {
	// Type is the Kubernetes Service type, for example "ClusterIP" or "LoadBalancer".
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`
	// Annotations are additional annotations to apply to the Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ReadReplicaServiceConfig controls the optional read-only Service for the
// read-replica pool.
type ReadReplicaServiceConfig struct {
	// Enabled controls whether the operator creates a dedicated Service for the
	// read-replica pool.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Type is the Kubernetes Service type, for example "ClusterIP" or
	// "LoadBalancer".
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`
	// Annotations are additional annotations to apply to the read Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ReadReplicaSchedulingConfig defines scheduling overrides for read replicas.
type ReadReplicaSchedulingConfig struct {
	// NodeSelector defines node-selection constraints for read-replica Pods.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations defines Pod tolerations for read-replica Pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Affinity defines Pod affinity / anti-affinity rules for read-replica Pods.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// TopologySpreadConstraints defines topology spread constraints for
	// read-replica Pods.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// ReadReplicaTemplateConfig defines Pod-template overrides for read replicas.
type ReadReplicaTemplateConfig struct {
	// Metadata defines additional labels and annotations applied only to the
	// read-replica Pod template.
	// +optional
	Metadata *PodMetadataConfig `json:"metadata,omitempty"`
	// Resources defines container resource requests and limits for read replicas.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Scheduling defines node-placement and topology overrides for read replicas.
	// +optional
	Scheduling *ReadReplicaSchedulingConfig `json:"scheduling,omitempty"`
}

// ReadReplicaStorageConfig defines storage overrides for the read-replica
// StatefulSet.
type ReadReplicaStorageConfig struct {
	// Size is the requested persistent volume size for read replicas.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`
	// StorageClassName is an optional StorageClass for read-replica PVCs.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// ReadReplicaConfig defines the steady-state read-replica pool.
type ReadReplicaConfig struct {
	// Replicas is the desired number of permanent non-voters.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// Service configures an optional dedicated Service for read traffic.
	// +optional
	Service *ReadReplicaServiceConfig `json:"service,omitempty"`
	// Template configures read-replica-specific Pod template overrides.
	// +optional
	Template *ReadReplicaTemplateConfig `json:"template,omitempty"`
	// Storage configures read-replica-specific storage overrides.
	// +optional
	Storage *ReadReplicaStorageConfig `json:"storage,omitempty"`
}

// MaintenanceConfig defines supported maintenance operations.
// This is intended to provide a first-class workflow for day-2 operations in
// clusters that enforce managed-resource mutation locks via admission policy.
type MaintenanceConfig struct {
	// Enabled enables maintenance mode for this cluster.
	// When true, the operator annotates managed resources (Pods/StatefulSet) with
	// `openbao.org/maintenance=true` to allow controlled restarts/deletes where
	// admission policies require an explicit maintenance signal.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// RuntimeConfig defines explicit runtime control requests for the OpenBao
// workload.
type RuntimeConfig struct {
	// RestartAt triggers a rolling restart when changed.
	// The operator propagates this value as a Pod template annotation; any change
	// results in a new StatefulSet revision and a controlled restart.
	// Recommended value is an RFC3339 timestamp string.
	// +kubebuilder:validation:MinLength=1
	// +optional
	RestartAt string `json:"restartAt,omitempty"`
}

// InitContainerConfig configures the init container used to render OpenBao configuration.
// The init container is responsible for rendering the final config.hcl from a template
// using environment variables such as HOSTNAME and POD_IP.
//
// The operator relies on this init container to render config.hcl at runtime. Disabling
// the init container is not supported and will be rejected by validation.
type InitContainerConfig struct {
	// Enabled controls whether the init container is used to render the configuration.
	// The operator requires the init container; disabling it is not supported.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Image is the container image to use for the init container.
	// If not specified, OPERATOR_INIT_IMAGE can supply a complete image reference, including a digest.
	// Otherwise, defaults to "<repo>:X.Y.Z" where <repo> is derived from OPERATOR_INIT_IMAGE_REPOSITORY
	// (default: "ghcr.io/kubebao/openbao-init") and the tag matches OPERATOR_VERSION.
	// +optional
	Image string `json:"image,omitempty"`
}

// ServiceAccountConfig configures the ServiceAccount used by OpenBao pods.
type ServiceAccountConfig struct {
	// Name overrides the generated ServiceAccount name.
	// If not specified, defaults to "<cluster-name>-serviceaccount".
	// +optional
	Name string `json:"name,omitempty"`

	// Annotations to add to the ServiceAccount.
	// Useful for cloud provider Workload Identity (e.g. eks.amazonaws.com/role-arn).
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PodMetadataConfig configures additional metadata for the OpenBao Pod template.
type PodMetadataConfig struct {
	// Labels are merged into the generated OpenBao Pod template labels.
	// Operator-managed labels take precedence if the same key is specified here.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are merged into the generated OpenBao Pod template annotations.
	// Operator-managed annotations take precedence if the same key is specified here.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ObservabilityConfig configures observability features.
type ObservabilityConfig struct {
	// Metrics configures integration with Prometheus/OpenMetrics.
	// +optional
	Metrics *MetricsConfig `json:"metrics,omitempty"`
}

// MetricsConfig configures metrics collection.
type MetricsConfig struct {
	// Enabled configures the OpenBao telemetry stanza and creates a ServiceMonitor.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// ScrapeProfile selects which OpenBao pods are targeted by generated scrape resources.
	// Active targets only the active OpenBao pod. AllNodes targets every OpenBao pod and
	// requires a dedicated metrics-only listener.
	// +kubebuilder:validation:Enum=Active;AllNodes
	// +kubebuilder:default=Active
	// +optional
	ScrapeProfile string `json:"scrapeProfile,omitempty"`

	// MetricsOnlyListener configures a dedicated listener for metrics scraping.
	// It is enabled automatically when scrapeProfile is AllNodes.
	// +optional
	MetricsOnlyListener *MetricsOnlyListenerConfig `json:"metricsOnlyListener,omitempty"`

	// ServiceMonitor controls whether to create a Prometheus Operator ServiceMonitor.
	// +optional
	ServiceMonitor *ServiceMonitorConfig `json:"serviceMonitor,omitempty"`
}

// MetricsOnlyListenerConfig configures a dedicated metrics-only TCP listener.
type MetricsOnlyListenerConfig struct {
	// Enabled controls whether to render the dedicated metrics-only listener.
	// When omitted, the listener is enabled automatically for the AllNodes scrape profile.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Port is the dedicated metrics listener port.
	// +kubebuilder:default=8202
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`

	// UnauthenticatedMetricsAccess allows unauthenticated access to /v1/sys/metrics
	// on the metrics-only listener. AllNodes scraping needs this so standby nodes can
	// expose metrics. Restrict this listener with NetworkPolicy.
	// +optional
	UnauthenticatedMetricsAccess *bool `json:"unauthenticatedMetricsAccess,omitempty"`
}

// ServiceMonitorConfig configures the Prometheus ServiceMonitor.
type ServiceMonitorConfig struct {
	// Enabled controls whether to create the ServiceMonitor.
	// Defaults to true if Metrics are enabled.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Interval is the scrape interval.
	// +kubebuilder:default="30s"
	// +optional
	Interval string `json:"interval,omitempty"`

	// ScrapeTimeout is the scrape timeout.
	// +kubebuilder:default="10s"
	// +optional
	ScrapeTimeout string `json:"scrapeTimeout,omitempty"`

	// Labels are added to the ServiceMonitor metadata.
	// Use this for Prometheus selectors, such as release labels used by kube-prometheus-stack.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are added to the ServiceMonitor metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// JobLabel selects the Service label Prometheus uses as the job label.
	// Defaults to app.kubernetes.io/name.
	// +optional
	JobLabel string `json:"jobLabel,omitempty"`

	// Authorization configures an optional ServiceMonitor authorization block.
	// Use this for authenticated /v1/sys/metrics scraping.
	// +optional
	Authorization *ServiceMonitorAuthorizationConfig `json:"authorization,omitempty"`

	// TLSConfig configures TLS verification for the OpenBao scrape endpoint.
	// +optional
	TLSConfig *ServiceMonitorTLSConfig `json:"tlsConfig,omitempty"`
}

// ServiceMonitorAuthorizationConfig configures Prometheus Operator endpoint authorization.
type ServiceMonitorAuthorizationConfig struct {
	// Type is the authorization type.
	// Defaults to Bearer when credentialsSecret is set.
	// +optional
	Type string `json:"type,omitempty"`

	// CredentialsSecret references a Secret key containing the authorization credentials.
	// The Secret must exist in the same namespace as the ServiceMonitor.
	CredentialsSecret ServiceMonitorKeySelector `json:"credentialsSecret"`
}

// ServiceMonitorTLSConfig configures TLS settings for the Prometheus Operator endpoint.
type ServiceMonitorTLSConfig struct {
	// ServerName verifies the hostname in the OpenBao serving certificate.
	// +optional
	ServerName string `json:"serverName,omitempty"`

	// InsecureSkipVerify disables TLS certificate verification.
	// Use only for temporary non-production environments.
	// Hardened clusters reject insecureSkipVerify=true.
	// +optional
	InsecureSkipVerify *bool `json:"insecureSkipVerify,omitempty"`

	// CAConfigMap references a ConfigMap key containing the CA certificate.
	// Mutually exclusive with CASecret.
	// +optional
	CAConfigMap *ServiceMonitorKeySelector `json:"caConfigMap,omitempty"`

	// CASecret references a Secret key containing the CA certificate.
	// Mutually exclusive with CAConfigMap.
	// +optional
	CASecret *ServiceMonitorKeySelector `json:"caSecret,omitempty"`
}

// ServiceMonitorKeySelector identifies a key in a Secret or ConfigMap.
type ServiceMonitorKeySelector struct {
	// Name is the Secret or ConfigMap name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key within the Secret or ConfigMap.
	// Defaults to token for authorization credentials and ca.crt for CA references.
	// +optional
	Key string `json:"key,omitempty"`
}
