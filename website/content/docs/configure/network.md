---
title: Configure network policy
description: Allow only the DNS, Kubernetes API, edge, and external dependency paths that an OpenBao cluster needs.
eyebrow: Configure · Service boundary
weight: 8
verifiedBy:
  - api/v1alpha1/openbaocluster_networking_types.go
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

## Understand the managed baseline

| Direction | Managed path | Purpose |
| --- | --- | --- |
| Ingress | OpenBao peers on ports 8200 and 8201 | Client handling and Raft traffic |
| Ingress | Operator and operator-managed backup or restore Jobs on port 8200 | Lifecycle operations |
| Egress | Cluster DNS on TCP and UDP 53 | Name resolution |
| Egress | Kubernetes API service on 443 and configured endpoint IPs on 6443 | Service registration and lifecycle operations |
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

Keep the operator-managed NetworkPolicies and add the following supplementary policy through your platform deployment
tooling. Replace `<namespace>` and `<cluster>` with the OpenBaoCluster namespace and name. The selector covers its
workload and lifecycle Job pods. Use the actual API backend port if it differs from 6443.

{{< command label="configure" title="Allow API-server egress for one OpenBao cluster" >}}
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: <cluster>-apiserver
  namespace: <namespace>
spec:
  endpointSelector:
    matchLabels:
      openbao.org/cluster: <cluster>
  egress:
    - toEntities:
        - kube-apiserver
      toPorts:
        - ports:
            - port: "6443"
              protocol: TCP
{{< /command >}}

The platform administrator owns this policy. Cilium [deny policies][cilium-deny] still take precedence.

Verify new API requests from the affected workload and Jobs. Expect forwarded connections and responses in Hubble,
and successful API operations in application logs. An HTTP `401` or `403` confirms connectivity, not authorization.

{{< callout type="note" title="Alternative: enable node CIDR matching" >}}
A cluster administrator can set Cilium's `policyCIDRMatchMode: [nodes]` to allow `ipBlock` rules to match node IPs.
This changes policy interpretation across the cluster; review existing policies before choosing this alternative.
{{< /callout >}}

[cilium-node-cidrs]: https://docs.cilium.io/en/stable/security/policy/layer3/#cidr-select-nodes
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
| `APIServerNetworkReady=True` | Service VIP and explicit endpoint IPs are configured | Continue with runtime checks |
| `APIServerNetworkReady=Unknown` | The service-VIP path exists, but post-DNAT requirements cannot be proven | Add `apiServerEndpointIPs` if the CNI requires them |
| `APIServerNetworkReady=False` with reason `APIServerNetworkConfigurationInvalid` | A safe API allow-list could not be built | Correct the CIDR or endpoint entries |
| `OpenBaoCluster` `BackupConfigurationReady=False` with reason `NetworkEgressRulesRequired` | Hardened backup lacks acceptable explicit storage egress | Add the storage target peers and ports |
| `OpenBaoRestore` `RestoreConfigurationReady=False` with reason `NetworkEgressRulesRequired` | Hardened restore lacks acceptable explicit storage egress | Add the storage target peers and ports |

Inspect both `<cluster>-network-policy` and `<cluster>-jobs-network-policy`, then test DNS, Kubernetes API access, the
selected edge path, and every external dependency from the affected pod or Job context. A ready condition proves the
configuration shape, not that the CNI, NAT path, or external firewall accepts the traffic.
