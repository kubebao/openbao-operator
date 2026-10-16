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

import networkingv1 "k8s.io/api/networking/v1"

// ACMEConfig configures ACME certificate management for OpenBao.
// See: https://openbao.org/docs/configuration/listener/tcp/#acme-parameters
type ACMEConfig struct {
	// DirectoryURL is the ACME directory URL (e.g., "https://acme-v02.api.letsencrypt.org/directory").
	// +kubebuilder:validation:MinLength=1
	DirectoryURL string `json:"directoryURL"`
	// Domains is the list of domain names for which to obtain the certificate.
	// This maps to OpenBao's listener `tls_acme_domains` field.
	//
	// When empty, the operator will default to an internal Service name suitable for
	// private ACME CAs running inside the cluster (e.g., "<cluster>-acme.<namespace>.svc").
	// +kubebuilder:validation:MinItems=1
	// +optional
	Domains []string `json:"domains,omitempty"`
	// Email is the email address to use for ACME registration.
	// +optional
	Email string `json:"email,omitempty"`
	// SharedCache configures a filesystem cache shared across OpenBao replicas for ACME account
	// and certificate state. This is required for HA ACME topologies where more than one Pod
	// can serve the same hostname concurrently.
	// +optional
	SharedCache *ACMESharedCacheConfig `json:"sharedCache,omitempty"`
}

// ACMESharedCacheMode controls how the operator provides a shared filesystem for OpenBao's ACME cache.
// +kubebuilder:validation:Enum=ManagedPVC;ExistingPVC
type ACMESharedCacheMode string

const (
	// ACMESharedCacheModeManagedPVC instructs the operator to create a dedicated RWX PVC.
	ACMESharedCacheModeManagedPVC ACMESharedCacheMode = "ManagedPVC"
	// ACMESharedCacheModeExistingPVC instructs the operator to mount an existing RWX PVC.
	ACMESharedCacheModeExistingPVC ACMESharedCacheMode = "ExistingPVC"
)

// ACMESharedCacheConfig configures the shared filesystem cache for ACME account and certificate state.
// See: https://openbao.org/docs/configuration/listener/tcp/#acme-parameters
// +kubebuilder:validation:XValidation:rule="self.mode != 'ManagedPVC' || !has(self.existingClaimName) || size(self.existingClaimName) == 0",message="tls.acme.sharedCache.existingClaimName is only supported when mode is ExistingPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ExistingPVC' || size(self.existingClaimName) > 0",message="tls.acme.sharedCache.existingClaimName is required when mode is ExistingPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ExistingPVC' || !has(self.size) || size(self.size) == 0",message="tls.acme.sharedCache.size is only supported when mode is ManagedPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ExistingPVC' || !has(self.storageClassName) || size(self.storageClassName) == 0",message="tls.acme.sharedCache.storageClassName is only supported when mode is ManagedPVC"
// +kubebuilder:validation:XValidation:rule="self.mode != 'ManagedPVC' || size(self.size) > 0",message="tls.acme.sharedCache.size is required when mode is ManagedPVC"
type ACMESharedCacheConfig struct {
	// Mode selects whether the operator creates a dedicated RWX PVC or mounts an existing one.
	Mode ACMESharedCacheMode `json:"mode"`
	// ExistingClaimName is the name of a pre-created RWX PVC in the same namespace.
	// Required when Mode is ExistingPVC.
	// +kubebuilder:validation:MinLength=1
	// +optional
	ExistingClaimName string `json:"existingClaimName,omitempty"`
	// Size is the requested capacity for the managed ACME cache PVC.
	// Required when Mode is ManagedPVC.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Size string `json:"size,omitempty"`
	// StorageClassName is an optional StorageClass for the managed ACME cache PVC.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TLSConfig captures TLS configuration for an OpenBaoCluster.
type TLSConfig struct {
	// Enabled controls whether TLS is enabled for the cluster.
	// +kubebuilder:validation:Required
	Enabled bool `json:"enabled"`
	// Mode controls who manages the certificate lifecycle.
	// +kubebuilder:validation:Enum=OperatorManaged;External;ACME
	// +kubebuilder:default=OperatorManaged
	// +optional
	Mode TLSMode `json:"mode,omitempty"`
	// ACME configures settings when Mode is 'ACME'.
	// +optional
	ACME *ACMEConfig `json:"acme,omitempty"`
	// RotationPeriod is a duration string (for example, "720h") controlling certificate rotation.
	// Only used when Mode is OperatorManaged.
	// +kubebuilder:validation:MinLength=1
	// +optional
	RotationPeriod string `json:"rotationPeriod,omitempty"`
	// ExtraSANs lists additional subject alternative names for server certificates.
	// In OperatorManaged mode, the operator includes these names when issuing the certificate.
	// In External mode, the operator requires the supplied certificate to contain them.
	// Values that parse as IP addresses are treated as IP SANs; all other values are DNS SANs.
	// +optional
	ExtraSANs []string `json:"extraSANs,omitempty"`
}

// IngressPathType identifies how a Kubernetes Ingress path should match requests.
// +kubebuilder:validation:Enum=Prefix;Exact;ImplementationSpecific
type IngressPathType string

const (
	// IngressPathTypePrefix uses prefix path matching.
	IngressPathTypePrefix IngressPathType = "Prefix"
	// IngressPathTypeExact uses exact path matching.
	IngressPathTypeExact IngressPathType = "Exact"
	// IngressPathTypeImplementationSpecific defers path matching to the controller.
	IngressPathTypeImplementationSpecific IngressPathType = "ImplementationSpecific"
)

// IngressReadinessMode identifies how the operator decides whether ingress
// integration is ready for endpoint publication.
// +kubebuilder:validation:Enum=Created;LoadBalancerPublished
type IngressReadinessMode string

const (
	// IngressReadinessModeCreated considers ingress integration ready once the
	// managed Ingress object exists.
	IngressReadinessModeCreated IngressReadinessMode = "Created"
	// IngressReadinessModeLoadBalancerPublished considers ingress integration
	// ready only after the managed Ingress reports a published load balancer
	// address in status.
	IngressReadinessModeLoadBalancerPublished IngressReadinessMode = "LoadBalancerPublished"
)

// IngressConfig controls optional HTTP(S) ingress in front of the OpenBao Service.
type IngressConfig struct {
	// Enabled controls whether the Operator manages an Ingress for external access.
	// +optional
	Enabled bool `json:"enabled"`
	// ClassName is an optional IngressClassName (for example, "nginx", "traefik").
	// +optional
	ClassName *string `json:"className,omitempty"`
	// Host is the primary host for external access, for example "bao.example.com".
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`
	// Path is the HTTP path to route to OpenBao, defaulting to "/".
	// +optional
	Path string `json:"path,omitempty"`
	// PathType identifies how the ingress controller should interpret Path.
	// +kubebuilder:default=Prefix
	// +optional
	PathType IngressPathType `json:"pathType,omitempty"`
	// TLSSecretName is an optional TLS Secret name; when empty the cluster TLS Secret is used.
	// +optional
	TLSSecretName string `json:"tlsSecretName,omitempty"`
	// Annotations are additional annotations to apply to the Ingress.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// ReadinessMode identifies when the operator should consider ingress
	// integration ready for endpoint publication.
	// +kubebuilder:default=LoadBalancerPublished
	// +optional
	ReadinessMode IngressReadinessMode `json:"readinessMode,omitempty"`
}

// NetworkConfig configures network-related settings for the OpenBaoCluster.
type NetworkConfig struct {
	// APIServerEgress adds a CNI-specific Kubernetes API egress policy alongside
	// the standard NetworkPolicies. Omit this field for standard IP-based rules.
	// +optional
	APIServerEgress *APIServerEgressConfig `json:"apiServerEgress,omitempty"`

	// APIServerCIDR is an optional CIDR block for the Kubernetes API server.
	// When specified, this value is used instead of auto-detection for NetworkPolicy egress rules.
	// This is useful when you want an explicit allow-list (or when the in-cluster service VIP
	// injected into pods is unavailable/unusable in your environment).
	// Example: "10.43.0.0/16" for service network or "192.168.1.0/24" for control plane nodes.
	// +optional
	APIServerCIDR string `json:"apiServerCIDR,omitempty"`

	// APIServerEndpointIPs is an optional list of Kubernetes API server endpoint IPs.
	// When set, the operator adds least-privilege NetworkPolicy egress rules for these IPs on port 6443.
	// Use this on CNI implementations where egress enforcement happens on the post-NAT
	// destination (the API server endpoint) rather than the kubernetes Service IP (10.43.0.1:443).
	//
	// The operator does not auto-detect these endpoint IPs because doing so reliably requires broader
	// cluster permissions (list/watch). Configure this field explicitly when needed.
	// Example (k3d): ["192.168.166.2"]
	// +optional
	APIServerEndpointIPs []string `json:"apiServerEndpointIPs,omitempty"`

	// DNSNamespace specifies the namespace where the cluster DNS service resides.
	// Defaults to "kube-system" if not specified.
	// +optional
	// +kubebuilder:default="kube-system"
	DNSNamespace string `json:"dnsNamespace,omitempty"`

	// DNSEndpointIPs is an optional list of DNS resolver endpoint IPs that should be
	// allow-listed directly in the operator-managed NetworkPolicy on TCP/UDP port 53.
	//
	// Use this for clusters that resolve DNS through node-local or host-networked caches
	// instead of pod-backed DNS Services in a namespace. These IP-based rules are additive
	// to the namespace-based allow-list controlled by DNSNamespace.
	//
	// The operator does not auto-detect these endpoint IPs because doing so reliably would
	// require environment-specific node or DNS discovery logic outside the current trust model.
	// Example: ["169.254.20.10"]
	// +optional
	DNSEndpointIPs []string `json:"dnsEndpointIPs,omitempty"`

	// EgressRules allows users to specify additional egress rules that will be merged into
	// the operator-managed NetworkPolicy. This is useful for allowing access to external
	// services such as transit seal backends, object storage endpoints, or other dependencies.
	//
	// The operator's default egress rules (DNS, API server, cluster pods) are always included
	// and cannot be overridden. User-provided rules are appended to the operator-managed rules.
	// Hardened clusters require every user-provided egress rule to be port-scoped and to target
	// explicit non-wildcard peers.
	//
	// Example: Allow egress to a transit seal backend in another namespace:
	//   egressRules:
	//   - to:
	//     - namespaceSelector:
	//         matchLabels:
	//           kubernetes.io/metadata.name: transit-namespace
	//     ports:
	//     - protocol: TCP
	//       port: 8200
	// +optional
	EgressRules []networkingv1.NetworkPolicyEgressRule `json:"egressRules,omitempty"`

	// IngressRules allows users to specify additional ingress rules that will be merged into
	// the operator-managed NetworkPolicy. This is useful for allowing access from external
	// services, monitoring tools, or other components that need to reach OpenBao pods.
	//
	// The operator's default ingress rules (cluster pods and operator-managed jobs)
	// are always included and cannot be overridden. User-provided rules are appended to
	// the operator-managed rules. Gateway or ingress-controller data-plane reachability
	// should be modeled with trustedIngressPeers.
	// Hardened clusters reject raw ingress rules; use trustedIngressPeers or managed
	// Gateway/Ingress integration for application access.
	//
	// Example: Allow ingress from a monitoring namespace:
	//   ingressRules:
	//   - from:
	//     - namespaceSelector:
	//         matchLabels:
	//           kubernetes.io/metadata.name: monitoring
	//     ports:
	//     - protocol: TCP
	//       port: 8200
	// +optional
	IngressRules []networkingv1.NetworkPolicyIngressRule `json:"ingressRules,omitempty"`

	// TrustedIngressPeers allows users to declare ingress-controller or passthrough-proxy peers
	// that should be allowed to reach OpenBao on the API port without writing full raw
	// NetworkPolicy ingress rules.
	//
	// This is useful for user-managed TCP passthrough or external ingress components that the
	// operator does not manage directly. The operator adds least-privilege ingress rules for
	// port 8200 using these peers.
	// Hardened clusters require trusted ingress peers to select explicit non-wildcard sources.
	//
	// Example: Allow a Traefik namespace to reach OpenBao on port 8200:
	//   trustedIngressPeers:
	//   - namespaceSelector:
	//       matchLabels:
	//         kubernetes.io/metadata.name: traefik
	//
	// Example: Allow only specific ingress-controller pods in another namespace:
	//   trustedIngressPeers:
	//   - namespaceSelector:
	//       matchLabels:
	//         kubernetes.io/metadata.name: ingress-system
	//     podSelector:
	//       matchLabels:
	//         app.kubernetes.io/name: traefik
	// +optional
	TrustedIngressPeers []networkingv1.NetworkPolicyPeer `json:"trustedIngressPeers,omitempty"`
}

// APIServerEgressConfig selects an additional Kubernetes API egress policy.
type APIServerEgressConfig struct {
	// Mode selects the Cilium kube-apiserver entity policy. This requires Cilium
	// and its namespaced cilium.io/v2 CiliumNetworkPolicy API.
	// +kubebuilder:validation:Enum=Cilium
	Mode string `json:"mode"`

	// Port is the API server's TCP backend port after Service translation.
	// Set this to the cluster's actual backend port, which can differ from 443.
	// +kubebuilder:default=6443
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
}

// GatewayConfig configures Kubernetes Gateway API access for the OpenBao cluster.
// This is an alternative to Ingress for external access, using the more modern
// and expressive Gateway API.
type GatewayConfig struct {
	// Enabled activates Gateway API support for this cluster.
	// When true, the Operator creates an HTTPRoute for the cluster.
	Enabled bool `json:"enabled"`
	// ListenerName optionally targets a specific listener (sectionName) on the referenced Gateway.
	// When set, the generated Route (HTTPRoute or TLSRoute) attaches only to that listener.
	// This is useful when a Gateway exposes multiple listeners for the same hostname (e.g. Traefik
	// "web" and "websecure") and you want deterministic attachment.
	// +optional
	ListenerName string `json:"listenerName,omitempty"`
	// GatewayRef references an existing Gateway resource that will handle
	// traffic for this OpenBao cluster. The Gateway must already exist.
	GatewayRef GatewayReference `json:"gatewayRef"`
	// Hostname for routing traffic to this OpenBao cluster.
	// This hostname will be automatically added to the TLS SANs.
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`
	// Path prefix for the HTTPRoute (defaults to "/").
	// +optional
	Path string `json:"path,omitempty"`
	// Annotations to apply to the HTTPRoute resource.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// BackendTLS configures BackendTLSPolicy for end-to-end TLS between the Gateway and OpenBao.
	// When enabled, the Operator creates a BackendTLSPolicy that configures the Gateway to use
	// HTTPS when communicating with the OpenBao backend service and validates the backend
	// certificate using the cluster's CA certificate.
	// +optional
	BackendTLS *BackendTLSConfig `json:"backendTLS,omitempty"`
	// TLSPassthrough enables TLS passthrough mode using TLSRoute instead of HTTPRoute.
	// When true, the Operator creates a TLSRoute that routes encrypted TLS traffic based on SNI
	// without terminating TLS at the Gateway. OpenBao terminates TLS directly.
	// When false (default), the Operator creates an HTTPRoute with TLS termination at the Gateway.
	// Note: TLSRoute and HTTPRoute are mutually exclusive - only one can be used per cluster.
	// BackendTLSPolicy is not needed when TLSPassthrough is enabled since the Gateway does not
	// decrypt traffic. The Gateway listener must be configured with protocol: TLS and mode: Passthrough.
	// +optional
	TLSPassthrough bool `json:"tlsPassthrough,omitempty"`
}

// BackendTLSConfig configures BackendTLSPolicy for Gateway API.
type BackendTLSConfig struct {
	// Enabled controls whether the Operator creates a BackendTLSPolicy.
	// When true (default when Gateway is enabled), the Operator creates a BackendTLSPolicy
	// that enables HTTPS and certificate validation for backend connections.
	// When false, no BackendTLSPolicy is created and the Gateway will use HTTP (or rely on
	// external configuration for TLS).
	// Hardened clusters reject backendTLS.enabled=false.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Hostname is the hostname to verify in the backend certificate.
	// If not specified, defaults to the stable TLS server name used by operator-managed
	// clients (for example, openbao-cluster-<cluster-name>.local).
	// This must match a DNS SAN in the backend certificate.
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

// GatewayReference identifies a Gateway resource.
type GatewayReference struct {
	// Name of the Gateway resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace of the Gateway resource. If empty, uses the OpenBaoCluster namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}
