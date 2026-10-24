---
title: Troubleshoot a cluster
description: Use conditions, events, workloads, and dependency checks to isolate an OpenBao failure before recovery.
eyebrow: Operate · Diagnosis
weight: 6
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - internal/controller/openbaocluster/status_condition_builders.go
  - internal/controller/openbaocluster/status_tls.go
  - internal/controller/openbaocluster/status_api_server_network.go
  - internal/controller/openbaocluster/status_gateway_integration.go
  - internal/controller/openbaocluster/status_acme_integration.go
  - internal/service/networking
---

Diagnose the failing boundary before changing the resource. `status.phase` is a summary; condition `reason` and
`message`, Kubernetes Events, and the affected Pod or Job logs contain the actionable detail.

## Collect evidence

{{< command label="inspect" title="Capture status, events, and workloads" >}}
kubectl -n <namespace> get openbaocluster <name> -o yaml
kubectl -n <namespace> describe openbaocluster <name>
kubectl -n <namespace> get pods,pvc,services,networkpolicies -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> get jobs -l openbao.org/cluster=<name>
kubectl -n <namespace> get events --sort-by=.lastTimestamp
{{< /command >}}

Get logs from the failing container or Job, including the previous container when a Pod restarts:

{{< command label="inspect" title="Read current and previous logs" >}}
kubectl -n <namespace> logs <pod-name> -c openbao
kubectl -n <namespace> logs <pod-name> -c openbao --previous
kubectl -n <namespace> logs job/<job-name>
{{< /command >}}

## Route by condition

| Signal | Check next |
| --- | --- |
| `TLSReady=False` | Referenced Secrets, certificate SANs and chain, expiry, and Pod `x509` errors |
| `ACMEIntegrationReady=False` | Directory trust, shared cache, TLS-ALPN reachability on 443, and Gateway passthrough |
| `GatewayIntegrationReady=False` | Gateway, listener, and managed Route status; route-specific reasons identify rejected or unresolved attachment |
| `APIServerNetworkReady=False` | API service CIDR and endpoint IPs required by the CNI's pre- or post-NAT enforcement |
| `BackupConfigurationReady=False` | OpenBao auth, storage identity, Secret references, and Hardened Job egress |
| `CloudUnsealIdentityReady=False` | Workload ServiceAccount binding, cloud permission, credentials, and KMS reachability |
| `NodeSecurityCapabilityMismatch=True` | Requested AppArmor or other node hardening is unavailable on scheduled nodes |
| `OpenBaoSealed=True` | [Recover a sealed cluster](../recover-sealed/) |
| `OpenBaoLeader=False` or no active leader | [Recover from no leader](../recover-no-leader/) |
| `Degraded=True` with break glass | [Recover a failed rollback](../recover-failed-rollback/) |

Observed `OpenBaoInitialized`, `OpenBaoSealed`, and `OpenBaoLeader` conditions come from OpenBao service-registration
labels on Pods. Confirm critical incidents with `bao status` from a reachable Pod.

## Check common integration boundaries

For External TLS, confirm the Secret names and keys match the selected configuration. For ACME with a private root,
confirm the configured CA bundle. Public ACME also requires external port 443 to reach OpenBao end to end; the
operator cannot prove the public firewall path.

For Gateway API, TLS termination in front of an ACME listener is not equivalent to TLS passthrough.
`GatewayRoutePending` means the current managed Route status is not available yet;
`GatewayRouteNotAccepted` and `GatewayRouteReferencesUnresolved` identify explicit attachment failures. Inspect the
Route parent status and backend policy for controller detail.

For Kubernetes API egress, some CNIs enforce NetworkPolicy after destination NAT. Add
`spec.network.apiServerEndpointIPs` when the service VIP is allowed but the control-plane endpoint is still blocked.
On Cilium, also check [node identity matching](../../configure/network/#restore-kubernetes-api-access-with-cilium),
even when `APIServerNetworkReady=True`.
See [Configure network policy](../../configure/network/) and [Expose OpenBao](../../configure/expose/).

## Escalate deliberately

Move to recovery when you have identified a seal dependency, leadership failure, failed rollback, or need for
snapshot restore. Do not use restore as a generic diagnostic action: it overwrites current OpenBao state.

If you need to stop operator changes while preserving evidence, use a bounded [cluster pause](../maintenance/). A pause
does not stop Kubernetes controllers or prove the workload is healthy.
