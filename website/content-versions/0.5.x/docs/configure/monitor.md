---
title: Monitor OpenBao
description: Scrape operator and workload metrics with explicit authentication, TLS verification, and network reachability.
eyebrow: Configure · Observe
weight: 9
verifiedBy:
  - internal/platform/entrypoint/health.go
  - charts/openbao-operator/values.yaml
  - charts/openbao-operator/templates/networkpolicy.yaml
  - charts/openbao-operator/templates/metrics
  - api/v1alpha1/openbaocluster_workload_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/adapter/config/builder.go
  - internal/adapter/config/render_listener.go
  - internal/controller/openbaocluster/split_reconcilers.go
  - internal/platform/constants/timing.go
  - internal/platform/observability/metrics.go
  - internal/platform/observability/kubernetes.go
  - internal/service/networking/metrics.go
  - internal/service/networking/services.go
---

Monitor two separate surfaces: the operator control plane and each OpenBao workload. Enabling one does not configure
the other.

| Surface | Configure through | Shows |
| --- | --- | --- |
| Operator metrics | Helm or Kustomize installation | Reconcile, cluster, backup, restore, and upgrade behavior |
| Workload telemetry | `spec.observability.metrics` on an `OpenBaoCluster` | OpenBao application and Raft metrics |
| Audit records | `spec.audit` and audit file storage | Security events; see [storage](../storage/#configure-audit-file-storage) |

## Scrape operator metrics

The operator exposes an HTTPS, RBAC-protected endpoint. With Prometheus Operator, enable the metrics reader binding and
ServiceMonitor resources through Helm:

{{< command label="configure" title="Enable operator ServiceMonitors" >}}
metrics:
  enabled: true
  rbac:
    enabled: true
    subjects:
      - name: prometheus-k8s
        namespace: monitoring
  serviceMonitor:
    enabled: true
    namespace: monitoring
    interval: 30s
    scrapeTimeout: 10s
    tlsConfig:
      insecureSkipVerify: true
{{< /command >}}

The chart creates a controller ServiceMonitor and, in multi-tenant mode, a provisioner ServiceMonitor. Its default
self-signed metrics certificate makes `insecureSkipVerify: true` the working bootstrap configuration. For strict
verification, configure the chart's CA and server-name fields with certificate material your monitoring platform can
read.

When the chart renders a provisioner scraper in multi-tenant mode, it also renders a provisioner-specific ingress
NetworkPolicy. Both controller and Provisioner policies use `networkPolicy.metricsAllowedNamespaceLabels`; label the
monitoring namespace to match that selector.

VictoriaMetrics users can set `metrics.victoriaMetrics.enabled: true` instead. A plain Prometheus installation can
scrape a reachable operator metrics Service on HTTPS port 8443 with a bearer token authorized for `GET /metrics`.

The selector defaults to `metrics: enabled`.

## Estimate multi-tenant API request load

Multi-tenant mode avoids cluster-wide watches for namespaced child resources. The workload reconciler instead uses a
periodic refresh to detect drift. Kubernetes API request volume therefore increases approximately linearly with the
number of `OpenBaoCluster` resources. See [tenancy watch boundaries](../../architecture/invariants-and-boundaries/#account-for-tenancy-watch-boundaries)
for the underlying controller design.

The 0.5.0 pre-release qualification run on Kubernetes 1.36.1 measured the following steady-state controller traffic:

| Test shape | Controller requests |
| --- | ---: |
| 25 clusters | 2,328 per minute, or 38.8 per second |
| Increment for 19 one-replica `Development` clusters | 1,691 per minute |
| Observed incremental slope | 89 per cluster per minute |

Each added cluster contributed 63 successful `GET` requests, 14 expected `GET` requests that returned `404`, 11
no-op `PATCH` requests, and one expected `POST` request that returned `409` per minute. A representative
`resourceVersion` sample did not change during the measurement, so the no-op patches did not persist object updates
in that sample.

Use this result as an initial capacity estimate, not as a supported-cluster limit or a capacity guarantee. Cluster
profiles, enabled features, object state, API server latency, and reconciliation activity change the request rate.
Measure `rest_client_requests_total` and API server throttling, latency, and errors under a representative workload
before you consolidate many clusters under one operator installation.

## Scrape the active OpenBao node

The default `Active` profile selects the active pod and reads `/v1/sys/metrics?format=prometheus` on the API listener.
Enabling metrics configures OpenBao telemetry and creates a metrics Service. A ServiceMonitor is created by default
when the Prometheus Operator CRD is installed, but metrics access is not made unauthenticated.

{{< command label="configure" title="Create an authenticated workload ServiceMonitor" >}}
spec:
  observability:
    metrics:
      enabled: true
      scrapeProfile: Active
      serviceMonitor:
        enabled: true
        interval: 30s
        scrapeTimeout: 10s
        labels:
          release: kube-prometheus-stack
        authorization:
          credentialsSecret:
            name: openbao-metrics-token
            key: token
        tlsConfig:
          serverName: openbao-cluster-prod-cluster.local
          caConfigMap:
            name: prod-cluster-metrics-ca
            key: ca.crt
  network:
    trustedIngressPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: monitoring
{{< /command >}}

Create the token with only `read` on `sys/metrics`. Trusted platform automation must also create and refresh the
same-namespace `prod-cluster-metrics-ca` ConfigMap with the public `ca.crt` bundle. The operator does not create that
observability ConfigMap. Do not place a CA private key in it.

The identity applying the `OpenBaoCluster` needs `get` on the authorization Secret and `use` or `get` on the CA
ConfigMap. The monitoring peer is also required for the generated workload NetworkPolicy. Hardened clusters reject
`insecureSkipVerify: true` for workload ServiceMonitors.

## Scrape every OpenBao node

Use `AllNodes` only when standby and per-node Raft visibility justifies an additional listener. It requires OpenBao
2.5.0 or newer, is not supported with ACME TLS, and enables unauthenticated metrics access on a metrics-only listener
by default. Restrict that listener with NetworkPolicy.

{{< command label="configure" title="Expose all-node metrics only to monitoring" >}}
spec:
  observability:
    metrics:
      enabled: true
      scrapeProfile: AllNodes
      metricsOnlyListener:
        port: 8202
        unauthenticatedMetricsAccess: true
      serviceMonitor:
        enabled: true
        labels:
          release: kube-prometheus-stack
        tlsConfig:
          serverName: openbao-cluster-prod-cluster.local
          caConfigMap:
            name: prod-cluster-metrics-ca
            key: ca.crt
  network:
    trustedIngressPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: monitoring
{{< /command >}}

The operator renders the listener, a headless metrics Service, and ServiceMonitor relabeling for pod and node names.
Use a pod selector in `trustedIngressPeers` as well when the monitoring namespace contains unrelated workloads.

## Check manager probes

The controller and Provisioner expose separate process probes:

- `/healthz` checks that the process responds. Admission, OpenBao, KMS, and object-storage outages do not fail liveness.
- `/readyz` requires synchronized caches for the manager's watched resources and a fresh, ready admission-dependency
  check. Standby replicas can become ready without acquiring leadership. Readiness does not prove that a controller
  has completed its first reconciliation or that an OpenBao cluster is available.

Each process checks admission dependencies every 15 seconds with a 10-second request deadline. A result becomes stale
at 30 seconds. Probe requests inspect maintained state and do not query the Kubernetes API. With the current 12
admission dependencies, a successful scan uses 24 GET requests when the first configured name prefix matches;
alternate-prefix lookups add requests. This polling cost applies per manager process, independently of cluster count.
The existing admission metric remains driven by startup and reconciliation checks, not this private probe snapshot.

In `--admission-enforcement=fail` mode, missing dependencies continue to block startup. In `warn` mode, the manager
starts but remains not-ready until its admission checks pass. If admission is lost after startup, readiness fails
while liveness continues to pass. The unsafe admission bypass skips only the admission check; cache synchronization
is still required. Workload and administrative operations retain their immediate admission checks before mutation.

## Start with a small signal set

| Concern | Operator signals |
| --- | --- |
| Availability | `openbao_cluster_ready_replicas` and `Available` or `Degraded` conditions |
| Reconciliation | `openbao_reconcile_errors_total` and `openbao_reconcile_duration_seconds` |
| Kubernetes API requests | `openbao_kube_client_requests_total` |
| Backup | `openbao_backup_last_success_timestamp` and backup readiness or failure state |
| Upgrade | `openbao_upgrade_in_progress`, failure, rollback, and duration metrics |
| Read pool | `openbao_cluster_read_replicas_desired`, `_ready`, `_registered`, and `_healthy` |

The repository includes focused dashboards under `config/grafana/dashboards/`; apply `config/grafana` as a starting
point. Alert rules remain user-managed. Begin with availability, stale backups, sustained reconciliation errors, and
read-pool degradation, then tune thresholds from observed behavior. The last-success backup metric exists only after
the first successful backup, so a freshness rule must also detect an absent or never-successful series.

Keep audit records out of the metrics pipeline. The audit PVC is a collector handoff buffer, not the final retention
or tamper-resistance boundary.

Finally, verify the Service, ServiceMonitor or VMServiceScrape, Prometheus target state, certificate validation, token
scope, NetworkPolicy path, and a representative query from each surface.

## Attribute Kubernetes API requests

Use `openbao_kube_client_requests_total` to compare request rates before and after an operator change:

```promql
sum by (verb, resource, subresource, result) (
  rate(openbao_kube_client_requests_total[5m])
)
```

The counter records HTTP attempts, including retries. It excludes reads served from the client cache.
The `verb` label distinguishes `apply` from other `patch` requests, as well as `get`, `list`, `watch`, `create`,
`update`, and `delete`. Discovery and unsupported operations use `other`.
The `resource` and `subresource` labels use fixed allowlists; unknown values use `other`, and absent subresources use
`none`. Labels exclude object names, namespaces, URLs, and error text. The `result` label is `success`, `conflict`,
`not_found`, `forbidden`, or `error`.

Count requests separately from storage writes. A successful APPLY can leave the stored object unchanged.
Use the request counter to measure API traffic and processing demand; it does not measure etcd writes.
