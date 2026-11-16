---
title: Configure storage
description: Choose data and audit storage, expand PVCs safely, and understand the current workload-resource controls.
eyebrow: Configure · Persistence
weight: 4
verifiedBy:
  - api/v1alpha1/openbaocluster_workload_types.go
  - api/v1alpha1/openbaocluster_configuration_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/app/openbaocluster/storage.go
  - internal/app/openbaocluster/storage_pvc.go
  - internal/controller/openbaocluster/status_audit_file_storage.go
  - internal/controller/openbaocluster/status_condition_builders.go
  - internal/service/bootstrap/audit_file_storage.go
  - internal/service/workload/statefulset_builder_containers.go
  - internal/service/workload/statefulset_builder_spec.go
---

Choose the voter StorageClass and initial capacity before creating the `OpenBaoCluster`. The storage class cannot be
changed afterward, and capacity can only grow.

## Configure voter storage

`spec.storage.size` is required. `storageClassName` is optional, but omitting it delegates the decision to the default
StorageClass at PVC creation time.

{{< command label="configure" title="Set the data storage contract" >}}
spec:
  storage:
    size: "50Gi"
    storageClassName: fast-encrypted
{{< /command >}}

The operator creates one `ReadWriteOnce` data PVC for each voter. Before creating the cluster, verify that the chosen
StorageClass provides the required IOPS, latency, topology, encryption, expansion, backup, and failure-domain behavior.

The user applying an explicit StorageClass must have the delegated `use` permission on that StorageClass. This prevents
a tenant from selecting platform storage without authorization.

{{< callout type="warning" title="StorageClass is immutable from cluster creation" >}}
Admission rejects changes to `spec.storage.storageClassName` after the `OpenBaoCluster` is created. Runtime reconciliation
also rejects any requested class that differs from existing PVCs. Moving data to another class requires an explicit
migration or restore procedure.
{{< /callout >}}

## Allow more time for database startup

Large Raft databases and storage with slow random reads can take longer to open than the default startup probe
budget. The kubelet restarts the container if its startup probe keeps failing. Database size alone does not predict
startup time; rehearse a cold start with representative storage and data.

To extend the budget, set `spec.probes.startup.failureThreshold`:

```yaml
spec:
  probes:
    startup:
      failureThreshold: 180
```

The startup probe runs every 5 seconds after an initial 10-second delay. The default threshold is 60, approximately
5 minutes. The example allows approximately 15 minutes. Supported thresholds range from 60 to 720; the maximum
allows approximately 1 hour. This setting applies to voters and read replicas. Readiness and liveness probes do
not change.

{{< callout type="warning" title="Changing the startup budget can restart Pods" >}}
This setting changes the Pod template. For an affected existing cluster, pause the cluster and wait for the pause
to be observed before upgrading the operator. Install the release's CRDs, set the longer startup budget while the
cluster remains paused, then resume reconciliation. The operator renders the longer budget before replacing Pods.
Schedule a maintenance window and verify quorum and client behavior during the rollout.
{{< /callout >}}

When the startup budget is extended, the rolling-upgrade readiness deadline allows the startup budget plus its
existing readiness margin for each server Pod in sequence, including read replicas. Clusters using the default
budget retain the existing 10-minute upgrade deadline. A longer deadline does not allow the operator to advance
past an unhealthy voter.

For a fresh restore or scheduled restore test, configure the same `probes` block in `clusterTemplate`. Fresh-target
bootstrap and restore-application deadlines also allow the extended startup budget. Configure the migration CLI's
`--timeout` to cover the complete sequential rollout when migrating a cluster with a longer budget.

## Understand PVC retention

The voter StatefulSet uses these retention policies:

- deleting the StatefulSet retains its data PVCs;
- scaling the StatefulSet down deletes PVCs for removed replicas.

Do not treat a replica reduction as a reversible scheduling change. Confirm Raft membership, backup state, and the data
retention consequence before scaling down.

## Expand data PVCs

Increase `spec.storage.size` only after confirming that the StorageClass and CSI driver support expansion. The operator
patches each managed PVC request upward. It rejects a smaller requested size.

{{< command label="apply" title="Request a larger voter volume" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge \
  -p '{"spec":{"storage":{"size":"100Gi"}}}'
{{< /command >}}

Some CSI drivers finish filesystem expansion only after the mounted Pod restarts. When a PVC reports
`FileSystemResizePending`, enable maintenance mode so the operator can perform a controlled restart:

{{< command label="apply" title="Allow controlled resize restarts" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge \
  -p '{"spec":{"maintenance":{"enabled":true}}}'
{{< /command >}}

If maintenance is disabled, the operator reports `StorageRestartRequired`. Disable maintenance again after the PVCs and
Pods converge.

{{< callout type="warning" title="Expansion depends on Kubernetes and the CSI driver" >}}
The operator can request a larger PVC and coordinate a required restart. It cannot make a StorageClass expandable or
guarantee that its backend expanded successfully. Inspect the PVC capacity and conditions.
{{< /callout >}}

## Configure voter and read-replica resources

Read replicas inherit voter storage unless `spec.readReplicas.storage` overrides it. A read-replica size cannot be
smaller than the voter size.

Set `spec.resources` for voter OpenBao containers. Read replicas use their separate template so each pool can be sized
for its role:

{{< command label="configure" title="Size voter and read-replica pools" >}}
spec:
  resources:
    requests:
      cpu: "1"
      memory: 2Gi
    limits:
      cpu: "2"
      memory: 4Gi
  readReplicas:
    replicas: 2
    template:
      resources:
        requests:
          cpu: 500m
          memory: 1Gi
        limits:
          cpu: "1"
          memory: 2Gi
    storage:
      size: 100Gi
      storageClassName: fast-encrypted
{{< /command >}}

Resource settings do not change storage capacity or quorum. Validate Pod placement, namespace quota, disruption
budget, and node headroom with the rendered voter and read-replica StatefulSets.

## Configure audit file storage

Use `spec.auditFileStorage` when file audit devices need a shared collector handoff. It is separate from the Raft data
path and is not the authoritative retention archive.

### Let the operator create the audit PVC

{{< command label="configure" title="Create a managed audit PVC" >}}
spec:
  auditFileStorage:
    mode: ManagedPVC
    size: "20Gi"
    storageClassName: rwx-encrypted
  audit:
    - type: file
      path: file
      fileOptions:
        filePath: /openbao/audit/audit.jsonl
{{< /command >}}

Managed mode creates one dedicated, sensitive-labeled `ReadWriteMany` PVC. Size it for collector lag and replay, then
ship records to an external log system or immutable archive for retention and search.

### Mount a platform-owned audit PVC

{{< command label="configure" title="Use an existing audit PVC" >}}
spec:
  auditFileStorage:
    mode: ExistingPVC
    existingClaimName: openbao-audit
    mountPath: /openbao/audit
{{< /command >}}

The claim must exist in the same namespace, be `Bound`, and include `ReadWriteMany`. The applying user needs delegated
`use` permission on the PVC. `size` and `storageClassName` are valid only in `ManagedPVC` mode.

Each OpenBao Pod writes through the same mount path but uses its own pod-name subdirectory on the shared claim. A
collector can mount the claim read-only and read those per-Pod files.

On standard Kubernetes, generated Pods default to UID `100`, GID `1000`, and `fsGroup: 1000`. OpenShift leaves those
IDs unset for Security Context Constraints, and `spec.securityContext` can override them. Verify writable ownership
against the rendered Pod security context and the CSI driver's `fsGroup` behavior.

{{< callout type="warning" title="Adding audit storage can require StatefulSet recreation" >}}
Volume and mount fields are locked on an existing StatefulSet. If `AuditFileStorageReady=False` reports
`AuditFileStorageStatefulSetRecreateRequired`, recreate or replace the StatefulSet through a controlled maintenance
workflow while preserving every data PVC.
{{< /callout >}}

## Verify storage

1. Inspect every cluster-owned PVC. The cluster label also selects audit and ACME cache claims, not only data PVCs.

   {{< command label="inspect" title="Inspect cluster PVCs" >}}
   kubectl -n <namespace> get pvc \
     -l openbao.org/cluster=<name> \
     -o wide
   {{< /command >}}

2. Inspect PVC capacity and resize conditions.

   {{< command label="verify" title="Check PVC capacity and conditions" >}}
   kubectl -n <namespace> get pvc <pvc-name> \
     -o jsonpath='{.spec.resources.requests.storage}{"\t"}{.status.capacity.storage}{"\n"}{range .status.conditions[*]}{.type}={.status}{"\t"}{.message}{"\n"}{end}'
   {{< /command >}}

3. Inspect the operator's storage conditions.

   {{< command label="verify" title="Check cluster storage conditions" >}}
   kubectl -n <namespace> get openbaocluster <name> \
     -o jsonpath='{range .status.conditions[*]}{.type}={.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'
   {{< /command >}}

`StorageConfigured=True` confirms the effective voter StorageClass is resolved and consistent. It does not prove
requested capacity or filesystem expansion completed. `AuditFileStorageReady=True` confirms the audit claim is Bound,
RWX, and mounted by the generated workload. A configured audit claim that is not ready blocks Hardened
`ProductionReady`; it does not independently determine Pod availability.

Continue with [server runtime configuration](../server/).
