# Cluster Lifecycle

Source: `test/e2e/Cluster_Lifecycle_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `cluster-lifecycle-creates-a-cluster-with-self-init-19ac290f` | creates a cluster with self-init disabled and produces expected Secrets | active | _none_ | `lifecycle`, `cluster`, `profile-development` |
| `cluster-lifecycle-creates-a-cluster-with-1-replica-9792a98c` | creates a cluster with 1 replica and verifies autopilot min_quorum=1 | active | _none_ | `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke` |
| `cluster-lifecycle-scales-down-to-1-replica-remains-f03c312f` | scales down to 1 replica, remains responsive, and verifies autopilot min_quorum=1 | active | _none_ | `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke` |
| `cluster-lifecycle-scales-up-to-3-replicas-and-9bd63a38` | scales up to 3 replicas and verifies autopilot min_quorum=3 | active | _none_ | `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke` |
| `cluster-lifecycle-creates-an-openbaocluster-and-converges-to-00cd448b` | creates an OpenBaoCluster and converges to Available | active | _none_ | `lifecycle`, `cluster`, `critical`, `tenant` |
| `cluster-lifecycle-expands-storage-by-increasing-spec-storage-212f934d` | expands storage by increasing spec.storage.size (if supported) | active | _none_ | `lifecycle`, `cluster`, `critical`, `tenant` |
| `cluster-lifecycle-provisions-tenant-rbac-via-openbaotenant-1798196a` | provisions tenant RBAC via OpenBaoTenant | active | _none_ | `lifecycle`, `cluster`, `critical`, `tenant` |
| `wrapper-graceful-pod-termination` | terminates OpenBao gracefully when deleting a Pod | active | _none_ | `lifecycle`, `cluster`, `critical`, `tenant` |
| `cluster-lifecycle-writes-file-audit-records-to-managed-fd0d4c2f` | writes file audit records to managed audit storage | active | _none_ | `lifecycle`, `cluster`, `critical`, `tenant`, `audit` |

## `cluster-lifecycle-creates-a-cluster-with-self-init-19ac290f`

Path: `Cluster Lifecycle > Development Profile: Manual Init (Self-Init Disabled) > creates a cluster with self-init disabled and produces expected Secrets`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `profile-development`

Recorded checkpoints:
- waiting for Secrets to be created
- waiting for root token Secret (self-init disabled)


## `cluster-lifecycle-creates-a-cluster-with-1-replica-9792a98c`

Path: `Cluster Lifecycle > Development Profile: Scaling with Autopilot Reconciliation > creates a cluster with 1 replica and verifies autopilot min_quorum=1`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke`

Recorded checkpoints:
- waiting for StatefulSet to be ready with 1 replica
- waiting for Available condition
- waiting for SelfInit to complete (SelfInitialized status)
- ensuring public service exists for autopilot verification
- waiting a bit for autopilot config to be reconciled after initialization
- verifying Raft Autopilot min_quorum=1 (Development profile with 1 replica)


## `cluster-lifecycle-scales-down-to-1-replica-remains-f03c312f`

Path: `Cluster Lifecycle > Development Profile: Scaling with Autopilot Reconciliation > scales down to 1 replica, remains responsive, and verifies autopilot min_quorum=1`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke`

Recorded checkpoints:
- updating cluster to 1 replica
- waiting for StatefulSet to scale down to 1 replica
- waiting for the remaining pod to be ready
- waiting for Available condition after scale down
- getting public service for autopilot verification
- triggering reconcile after scale down so autopilot settings are refreshed promptly
- verifying Raft Autopilot min_quorum=1 (Development profile with 1 replica)
- verifying the remaining cluster still serves JWT-authenticated KV traffic


## `cluster-lifecycle-scales-up-to-3-replicas-and-9bd63a38`

Path: `Cluster Lifecycle > Development Profile: Scaling with Autopilot Reconciliation > scales up to 3 replicas and verifies autopilot min_quorum=3`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke`

Recorded checkpoints:
- updating cluster to 3 replicas
- waiting for StatefulSet to scale to 3 replicas
- waiting for all pods to be ready
- getting public service for autopilot verification
- triggering reconcile after scale up so autopilot settings are refreshed promptly
- verifying Raft Autopilot min_quorum=3 (Development profile with 3 replicas)


## `cluster-lifecycle-creates-an-openbaocluster-and-converges-to-00cd448b`

Path: `Cluster Lifecycle > Tenant + Cluster lifecycle (Self-Init) > creates an OpenBaoCluster and converges to Available`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `critical`, `tenant`

Recorded checkpoints:
- waiting for OpenBaoCluster to be observed by the API server
- waiting for StatefulSet to be created
- triggering a reconcile and waiting for Available condition
- verifying the approved policy bundle was reconciled
- verifying reconcile metrics are emitted for the cluster
- verifying Raft Autopilot is configured


## `cluster-lifecycle-expands-storage-by-increasing-spec-storage-212f934d`

Path: `Cluster Lifecycle > Tenant + Cluster lifecycle (Self-Init) > expands storage by increasing spec.storage.size (if supported)`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `critical`, `tenant`

Recorded checkpoints:
- waiting for the data PVC to exist
- capturing the current pod UID (to detect potential restarts)
- updating OpenBaoCluster spec.storage.size from 1Gi to 2Gi
- waiting for the PVC storage request to be updated by the operator
- ensuring the cluster remains Available
- waiting for the pod to restart OR the filesystem resize to complete


## `cluster-lifecycle-provisions-tenant-rbac-via-openbaotenant-1798196a`

Path: `Cluster Lifecycle > Tenant + Cluster lifecycle (Self-Init) > provisions tenant RBAC via OpenBaoTenant`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `critical`, `tenant`

Recorded checkpoints:
- verifying OpenBaoTenant is provisioned


## `wrapper-graceful-pod-termination`

Path: `Cluster Lifecycle > Tenant + Cluster lifecycle (Self-Init) > terminates OpenBao gracefully when deleting a Pod`

State: `active`

Generated fallback ID: `cluster-lifecycle-terminates-openbao-gracefully-when-deleting-a-0a25963e`

Covers: _none_

Labels: `lifecycle`, `cluster`, `critical`, `tenant`

Recorded checkpoints:
- retaining the terminating Pod until its child exit status can be inspected
- deleting the Pod with its normal termination grace period
- releasing the Pod and waiting for its replacement to become ready


## `cluster-lifecycle-writes-file-audit-records-to-managed-fd0d4c2f`

Path: `Cluster Lifecycle > Tenant + Cluster lifecycle (Self-Init) > writes file audit records to managed audit storage`

State: `active`

Covers: _none_

Labels: `lifecycle`, `cluster`, `critical`, `tenant`, `audit`

Recorded checkpoints:
- waiting for audit file storage readiness
- verifying the managed audit PVC is Bound and RWX
- waiting for SelfInit to create the JWT role used by the audit request
- triggering an audited OpenBao API request
- reading audit records through a collector-style read-only PVC mount
