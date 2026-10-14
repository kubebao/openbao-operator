---
title: Configure the server runtime
description: Tune OpenBao server settings, audit devices, plugins, and Raft Autopilot after the baseline works.
eyebrow: Configure · Advanced
weight: 5
verifiedBy:
  - api/v1alpha1/openbaocluster_configuration_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/adapter/config/builder.go
  - internal/adapter/raft/autopilot.go
  - internal/service/workload/plugin_directory.go
  - internal/service/workload/statefulset_builder_spec.go
---

Keep the operator defaults until measured requirements justify a change. Server-runtime tuning is not a prerequisite
for the first cluster.

## Choose the correct configuration surface

| Surface | Controls |
| --- | --- |
| `spec.configuration` | UI, log level, cache, lease, listener, logging, Raft, and plugin-runtime settings |
| `spec.audit` | Declarative file, HTTP, syslog, or socket audit devices |
| `spec.plugins` | Declarative auth, secret, database, or KMS plugin declarations |
| `spec.configuration.raft.autopilot` | Raft health thresholds, quorum bounds, and dead-peer cleanup |

External exposure, TLS, NetworkPolicy, and observability are separate service-boundary decisions. File audit storage is
owned by the [storage guide](../storage/#configure-audit-file-storage).

## Tune the core runtime

Use `spec.configuration` for a specific OpenBao requirement, not as a copied collection of arbitrary values.

{{< command label="configure" title="Set selected server options" >}}
spec:
  configuration:
    ui: true
    logLevel: info
    defaultLeaseTTL: "24h"
    maxLeaseTTL: "720h"
    raft:
      performanceMultiplier: 2
{{< /command >}}

| Field | Change it when |
| --- | --- |
| `ui` | The built-in UI is intentionally enabled or disabled; route exposure is configured elsewhere |
| `logLevel` and `logging` | Operational evidence requires a different level, format, file, or rotation policy |
| `cacheSize` and `disableCache` | Profiling and workload behavior justify changing cache behavior |
| `defaultLeaseTTL` and `maxLeaseTTL` | Application lease policy requires explicit bounds |
| `raft.performanceMultiplier` | Measured disk or network latency justifies slower Raft timing |
| `listener.proxyProtocolBehavior` | The selected load balancer sends Proxy Protocol and the trust boundary is understood |

Valid PROXY protocol values are `use_always`, `allow_authorized`, and `deny_unauthorized`. The latter two require
`listener.proxyProtocolAuthorizedAddrs`; follow [Configure trusted PROXY protocol sources](#configure-trusted-proxy-protocol-sources).
The operator rejects `allow_any`, and the CRD rejects `use_proxy_protocol`. Keep listener TLS aligned with the later
external-access design, and do not set `listener.tlsDisable: true` on Hardened clusters.

The Hardened profile also rejects `detectDeadlocks`, `rawStorageEndpoint`, `introspectionEndpoint`, and
`unsafeAllowAPIAuditCreation`. Those flags expose debugging or unsafe runtime surfaces rather than normal operations.

## Configure audit devices

Declare audit devices in `spec.audit` when they must exist as part of the cluster configuration. Supported structured
types are file, HTTP, syslog, and socket.

{{< command label="configure" title="Write file audit records to shared storage" >}}
spec:
  auditFileStorage:
    mode: ManagedPVC
    size: "20Gi"
    storageClassName: rwx-encrypted
  audit:
    - type: file
      path: secure-audit
      description: Secure audit logging
      fileOptions:
        filePath: /openbao/audit/audit.jsonl
        mode: "0600"
{{< /command >}}

The structured field is `fileOptions.filePath` in camel case. File paths must live under the effective
`auditFileStorage.mountPath`, which defaults to `/openbao/audit`.

| Type | Structured fields |
| --- | --- |
| File | `filePath` and optional octal `mode` |
| HTTP | `uri` and optional `headers` shaped as `map[string][]string` |
| Syslog | Optional `facility` and `tag` |
| Socket | Optional `address`, `socketType`, and `writeTimeout` |

The generic `options` object remains available for flat string-valued advanced options. Structured options take
precedence and reject nested objects or arrays except the typed HTTP headers field.

Audit storage is a collector handoff and replay buffer. It is not rotation, retention, tamper-proof archival, or a
complete collection pipeline.

## Register plugins

Use an OCI plugin when OpenBao can download the binary at startup. Use `command` only when the binary is already in the
runtime image or another deliberately managed path.

{{< command label="configure" title="Register an OCI plugin" >}}
spec:
  version: "2.6.2"
  configuration:
    plugin:
      autoDownload: true
      downloadBehavior: fail
  plugins:
    - type: secret
      name: example
      image: registry.example.com/openbao-plugin-secrets-example
      version: v1.0.0
      binaryName: openbao-plugin-secrets-example
      sha256sum: "<64-character-sha256>"
{{< /command >}}

{{< command label="configure" title="Register a preinstalled plugin" >}}
spec:
  plugins:
    - type: secret
      name: local-example
      command: openbao-plugin-secrets-example
      version: v1.0.0
      binaryName: openbao-plugin-secrets-example
      sha256sum: "<64-character-sha256>"
{{< /command >}}

Plugin requirements:

- `image` and `command` are mutually exclusive; one is required.
- Plugin download settings require OpenBao 2.5.0 or newer.
- `downloadBehavior: fail` stops startup after a failed download; `continue` logs the failure and continues.
- `args` and `env` are used only when `autoRegister` is enabled.
- OCI-downloaded binaries use a writable pod-local `emptyDir` at `/openbao/plugins`. It is a cache, not durable storage.
- OpenBao's runtime OCI client needs registry egress and its own authentication. Kubernetes `imagePullSecrets` cover
  Kubernetes image pulls, not necessarily plugin downloads inside the container.
- An identity selecting a plugin `image` or `command` needs delegated `usecustomexecutables` authority on the cluster.
- A plugin with `type: kms` can back `spec.unseal.type: kms` on OpenBao 2.6.0 and later. Its name must match
  `spec.unseal.kms.pluginName`; see [Configure unseal](../unseal/#configure-a-plugin-backed-kms-seal).

## Understand Raft Autopilot defaults

After initialization, the operator reconciles these effective defaults:

| Setting | Effective default |
| --- | --- |
| `deadServerLastContactThreshold` | `5m` |
| `lastContactThreshold` | `10s` |
| `maxTrailingLogs` | `1000` |
| `serverStabilizationTime` | `10s` |
| Hardened `minQuorum` | `3` for three voters; all voters when replicas exceed three |
| Development `minQuorum` | Replica count, with a minimum of one |
| `cleanupDeadServers` | `true`, except it is automatically disabled for a derived quorum below three unless explicitly overridden |

The CRD requires an explicitly configured `minQuorum` to be at least three. Small Development clusters can still have
a derived value below three; automatic cleanup is then disabled so OpenBao does not reject the configuration.

For self-init clusters, day-2 Autopilot reconciliation requires operator JWT bootstrap. Without
`selfInit.oidc.enabled`, the operator skips that authenticated reconciliation after initialization. Standard-init
clusters use the operator-managed root-token Secret.

Override Autopilot only after measuring the failure or latency behavior you need to change:

{{< command label="configure" title="Override Raft Autopilot" >}}
spec:
  profile: Hardened
  replicas: 5
  configuration:
    raft:
      autopilot:
        minQuorum: 4
        deadServerLastContactThreshold: "10m"
        lastContactThreshold: "30s"
        maxTrailingLogs: 2000
        serverStabilizationTime: "30s"
{{< /command >}}

If you set `cleanupDeadServers: false`, you take manual ownership of dead-peer removal. Treat that as a controlled
operational exception.

## Inspect the schema and rendered state

{{< command label="inspect" title="Inspect configuration fields" >}}
kubectl explain openbaocluster.spec.configuration
kubectl explain openbaocluster.spec.audit
kubectl explain openbaocluster.spec.plugins
{{< /command >}}

After an update, inspect the `OpenBaoCluster` conditions, generated ConfigMap, StatefulSet revision, Pod events, and
OpenBao logs. A schema-valid option can still be incompatible with the selected OpenBao version or external runtime.

## Configure trusted PROXY protocol sources

Use `allow_authorized` to trust client-address metadata from selected proxies. Replace the example addresses with the
source IP addresses or CIDRs that OpenBao observes for your load balancers. Hostnames are not accepted.

{{< command label="configure" title="Trust selected proxy sources" >}}
spec:
  configuration:
    listener:
      proxyProtocolBehavior: allow_authorized
      proxyProtocolAuthorizedAddrs:
        - "192.0.2.10"
        - "2001:db8::/64"
{{< /command >}}

Both `allow_authorized` and `deny_unauthorized` require a nonempty address list. With `allow_authorized`, OpenBao uses
PROXY metadata from trusted sources and the peer IP address for other sources. Direct connections remain available;
use NetworkPolicy or firewall rules to control API access. `deny_unauthorized` rejects connections from other sources,
including operator clients and health probes unless their source addresses are authorized. `use_always` trusts PROXY
metadata from every source and does not require an address list.

These settings apply to the API listener. The dedicated metrics listener retains its existing configuration.
