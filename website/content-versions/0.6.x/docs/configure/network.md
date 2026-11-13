---
title: Configure network policy
description: Allow only the DNS, Kubernetes API, edge, and external dependency paths that an OpenBao cluster needs.
eyebrow: Configure · Service boundary
weight: 8
verifiedBy:
  - api/v1alpha1/openbaocluster_networking_types.go
  - api/v1alpha1/openbaocluster_workload_types.go
  - cmd/bao-wrapper/environment.go
  - internal/service/networking/services.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/service/networking/policy_build.go
  - internal/service/networking/policy_rules.go
  - internal/service/networking/api_server_network.go
  - internal/controller/openbaocluster/status_api_server_network.go
---

The operator renders ingress-and-egress NetworkPolicies for OpenBao workload pods and lifecycle Job pods, then adds
the paths required for cluster operation. Other pods in the namespace are not selected by these policies. Enforcement
depends on a NetworkPolicy-capable CNI.

The operator manages standard Kubernetes `NetworkPolicy` resources. The platform administrator manages CNI settings
and any supplementary CNI-specific policies.

Add environment-specific peers and egress explicitly; enabling an edge resource does not silently widen pod access.

## Use IPv6 and dual-stack networks

OpenBao advertises the primary Pod IP from `status.podIP`. IPv6 literals include brackets, for example
`https://[fd00::12]:8200`. Raft peers use the shared headless Service and Pod DNS names. On dual-stack clusters, that
Service publishes both IP families. On single-stack clusters, it uses the available family.

The main client Service and dedicated read-replica Service retain the Kubernetes default IP family policy unless
you configure `ipFamilyPolicy` or `ipFamilies`. To request both families with IPv6 as the primary family:

{{< command label="configure" title="Request IPv6-first dual-stack client Services" >}}
spec:
  service:
    type: ClusterIP
    ipFamilyPolicy: RequireDualStack
    ipFamilies: [IPv6, IPv4]
  readReplicas:
    replicas: 1
    service:
      enabled: true
      ipFamilyPolicy: RequireDualStack
      ipFamilies: [IPv6, IPv4]
{{< /command >}}

`RequireDualStack` requires a dual-stack Kubernetes cluster. `PreferDualStack` also works on a single-stack cluster
when you omit `ipFamilies`. Use `SingleStack` with `ipFamilies: [IPv6]` to request an IPv6-only client Service on a
cluster that supports that family. Two entries in `ipFamilies` require `PreferDualStack` or `RequireDualStack`.
Choose the primary family before creating the Service; Kubernetes does not allow changing an existing Service's primary
family. If the requested primary family differs from the allocated family, the operator reports
`Degraded=True` with reason `ServiceIPFamilyChangeNotSupported`. Keep the allocated family first in `ipFamilies`, or omit
`ipFamilies` to retain it. The operator does not delete and recreate the Service. External load balancers and edge
controllers must support the selected families independently.

Use `/32` for a single IPv4 endpoint and `/128` for a single IPv6 endpoint in NetworkPolicy CIDR rules. Specify literal
IPv6 URL endpoints with brackets. DNS endpoints can resolve to either family. Certificate verification still uses the
configured TLS server name; selecting an IP family does not add IP SANs to an externally supplied certificate.

The Cilium API-server policy requirement below applies to IPv4, IPv6, and dual-stack clusters. A configured allow-list
does not prove that the CNI permits traffic.

Scheduled network tests cover IPv4, IPv6, and both dual-stack primary-family orders with Cilium. They verify OperatorManaged
and External TLS, peer discovery, client Services, and workload and lifecycle Job egress policy. IPv6 lifecycle tests
cover scaling, restart, rolling upgrade, S3 and Azure backup and restore, and GCS backup through storage emulators.
Cloud load balancers, other CNIs, ACME issuance over IPv6, and storage-driver compatibility require separate qualification.

## Understand the managed baseline

| Direction | Managed path | Purpose |
| --- | --- | --- |
| Ingress | OpenBao peers on ports 8200 and 8201 | Client handling and Raft traffic |
| Ingress | Operator and operator-managed backup or restore Jobs on port 8200 | Lifecycle operations |
| Egress | Cluster DNS on TCP and UDP 53 | Name resolution |
| Egress | Kubernetes API service on 443 and configured endpoint IPs on 6443 | Service registration and lifecycle operations |
| Egress, opt-in Cilium mode | `kube-apiserver` entity on the configured TCP backend port | API access for workloads and lifecycle Jobs |
| Egress | Cluster workload peers | OpenBao cluster communication |

The operator always keeps these baseline rules. `spec.network` adds, rather than replaces, them.

Development lifecycle Jobs are the exception to the strict default: when no `egressRules` exist, their policy permits
IPv4 and IPv6 HTTPS egress on port 443. Adding any explicit egress rule removes that fallback. Hardened backup and
restore paths require explicit, scoped egress.

## Configure DNS and Kubernetes API egress

The DNS namespace defaults to `kube-system`. Set it when the resolver pods live elsewhere. Add endpoint IPs for
node-local or host-networked resolvers that namespace selection cannot reach.

{{< command label="configure" title="Allow a non-default and node-local DNS path" >}}
spec:
  network:
    dnsNamespace: openshift-dns
    dnsEndpointIPs:
      - 169.254.20.10
{{< /command >}}

`dnsEndpointIPs` becomes an exact host CIDR and applies to the main workload and operator-managed Jobs.

The operator normally derives the Kubernetes API service address. Some CNIs enforce egress after destination NAT and
therefore also need the control-plane endpoint IPs.

{{< command label="configure" title="Pin Kubernetes API destinations" >}}
spec:
  network:
    apiServerCIDR: 10.43.0.1/32
    apiServerEndpointIPs:
      - 192.168.166.2
{{< /command >}}

Use the smallest correct ranges. The operator does not auto-discover endpoint IPs because that would require broader
cluster permissions and environment-specific assumptions.

## Restore Kubernetes API access with Cilium

By default, Cilium's CIDR rules exclude cluster nodes. An API server on a control-plane node can remain blocked
with correct `apiServerEndpointIPs`, even when `APIServerNetworkReady=True`. See [Cilium's node CIDR matching
rules][cilium-node-cidrs].

Confirm that node CIDR matching is disabled and traffic to the API endpoint is dropped. If Hubble is enabled, inspect
flows on the Cilium agent on the affected pod's node. Replace `<cilium-pod>`, `<namespace>`, and `<pod>` with their names;
adjust `kube-system` if Cilium uses another namespace.

{{< command label="inspect" title="Check Cilium settings and API policy drops" >}}
kubectl -n kube-system get configmap cilium-config -o jsonpath='{.data.policy-cidr-match-mode}{"\n"}'
kubectl -n kube-system exec <cilium-pod> -c cilium-agent -- hubble observe --since 3m --pod <namespace>/<pod>
{{< /command >}}

An empty setting disables node CIDR matching. Look for `Policy denied` drops to the API endpoint with destination
identity `kube-apiserver` or `remote-node`. A timeout alone does not identify this failure mode.

Enable the operator-owned supplementary policy for this cluster:

{{< command label="configure" title="Enable Cilium API-server egress" >}}
spec:
  network:
    apiServerEgress:
      mode: Cilium
      port: 6443
{{< /command >}}

Set `port` to the API server's TCP backend port after Service translation. It defaults to 6443; some platforms use
443 or another port. Cilium [matches the translated backend port][cilium-ports], so the Service port alone is insufficient.
This mode requires Cilium and the namespaced `cilium.io/v2` `CiliumNetworkPolicy` API. The operator does not install
Cilium, detect the active CNI, or change cluster-wide Cilium settings.

The operator creates `<cluster>-api-server` in the cluster's namespace. It selects only pods labeled
`openbao.org/cluster: <cluster>`, including voters, read replicas, and lifecycle Jobs. Its only allowance is TCP to
Cilium's `kube-apiserver` entity on the configured backend port. The existing Kubernetes NetworkPolicies remain in force.
The supplementary policy sets `enableDefaultDeny.ingress` and `enableDefaultDeny.egress` to `false`; it does not introduce
a separate default-deny posture. Cilium [deny policies][cilium-deny] still take precedence.

Upgrade the operator's RBAC and admission policies with the controller. Both installation modes grant namespaced
`get`, `create`, `patch`, and `delete` access to `ciliumnetworkpolicies`; no cluster-wide Cilium policy access is required.
If the requested API or permissions are missing, the operator reports `APIServerNetworkConfigurationInvalid`.

When migrating from the administrator workaround, enable this field first. Verify that the operator-owned policy exists,
then remove the administrator-owned exception through the tooling that created it. The operator does not adopt or remove
that exception. Removing `apiServerEgress` deletes only the operator-owned policy. Keep the field enabled while the
cluster depends on Cilium entity matching.

Verify new API requests from the affected workload and Jobs. Expect forwarded connections and responses in Hubble,
and successful API operations in application logs. An HTTP `401` or `403` confirms connectivity, not authorization.
`APIServerNetworkReady=True` confirms the applied configuration; it does not probe connectivity or prove that Cilium
has realized the policy. Restore-origin destination clusters retain administrator-managed isolation and do not receive
this ordinary API egress allowance.

[cilium-node-cidrs]: https://docs.cilium.io/en/stable/security/policy/layer3/#cidr-select-nodes
[cilium-ports]: https://docs.cilium.io/en/stable/security/policy/layer4/
[cilium-deny]: https://docs.cilium.io/en/stable/security/policy/deny/

## Allow edge and monitoring peers

Use `trustedIngressPeers` for Gateway data planes, ingress controllers, passthrough proxies, and monitoring systems.
The operator limits these peers to port 8200 and also to the configured metrics-listener port when that listener is
enabled.

{{< command label="configure" title="Allow selected ingress and monitoring pods" >}}
spec:
  network:
    trustedIngressPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: gateway-system
        podSelector:
          matchLabels:
            app.kubernetes.io/name: traefik
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: monitoring
{{< /command >}}

Hardened clusters require explicit, non-wildcard sources. They reject raw `spec.network.ingressRules`. That raw field
is a Development compatibility path; prefer `trustedIngressPeers` even outside Hardened.

## Allow external dependencies

Add `egressRules` for transit unseal, KMS or PKI endpoints, object storage, plugin registries, and other dependencies.
Hardened clusters require every user rule to select explicit peers and ports.

{{< command label="configure" title="Allow a transit service and an external HTTPS endpoint" >}}
spec:
  network:
    egressRules:
      - to:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: openbao-infra
        ports:
          - protocol: TCP
            port: 8200
      - to:
          - ipBlock:
              cidr: 192.0.2.40/32
        ports:
          - protocol: TCP
            port: 443
{{< /command >}}

Object-store Jobs use a separate operator-managed NetworkPolicy. The same `egressRules` list is appended to both the
OpenBao workload policy and the Job policy; it cannot currently scope a transit rule only to OpenBao or a storage rule
only to Jobs. The explicit contract is also used to decide whether Hardened backup and restore configurations are safe.

## Read the conditions

| Condition | Meaning | Response |
| --- | --- | --- |
| `APIServerNetworkReady=True` | IP-based targets are configured, or the owned Cilium policy matches the requested configuration | Continue with runtime checks |
| `APIServerNetworkReady=Unknown` | The VIP path is configured, the Cilium policy is pending, or restore isolation is administrator-managed | Inspect the reason and use the CNI-specific configuration |
| `APIServerNetworkReady=False` with reason `APIServerNetworkConfigurationInvalid` | API targets or the requested Cilium policy cannot be configured or observed | Check targets, API availability, RBAC, and ownership |
| `OpenBaoCluster` `BackupConfigurationReady=False` with reason `NetworkEgressRulesRequired` | Hardened backup lacks acceptable explicit storage egress | Add the storage target peers and ports |
| `OpenBaoRestore` `RestoreConfigurationReady=False` with reason `NetworkEgressRulesRequired` | Hardened restore lacks acceptable explicit storage egress | Add the storage target peers and ports |

Inspect both `<cluster>-network-policy` and `<cluster>-jobs-network-policy`, then test DNS, Kubernetes API access, the
selected edge path, and every external dependency from the affected pod or Job context. A ready condition proves the
configuration shape, not that the CNI, NAT path, or external firewall accepts the traffic.
