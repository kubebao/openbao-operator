---
title: Run planned maintenance
description: Drain, scale, restart, or pause a cluster without bypassing quorum and admission safeguards.
eyebrow: Operate · Maintenance
weight: 4
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - api/v1alpha1/openbaocluster_workload_types.go
  - config/policy/openbao-lock-managed-resource-mutations.yaml
  - config/rbac/openbaocluster_maintenance_role.yaml
  - internal/controller/openbaocluster/split_reconcilers.go
  - internal/controller/openbaocluster/status_pause_profile.go
  - internal/service/workload/maintenance.go
  - internal/service/workload/pdb.go
  - internal/app/openbaocluster/infra_scale_down.go
  - internal/app/openbaocluster/infra_scale_down_resume.go
  - cmd/bao-wrapper/process_runner.go
---

Use the parent `OpenBaoCluster` controls for planned work. Directly changing operator-managed StatefulSets, Pods, or
PVCs bypasses lifecycle coordination and is normally blocked by admission.

## Check quorum and disruption protection

For three or more voter replicas, the operator creates `<cluster>-pdb` with `maxUnavailable: 1`. It does not create a
voter PodDisruptionBudget for one or two replicas because one voluntary eviction would already break quorum.

{{< command label="verify" title="Check placement and the disruption budget" >}}
kubectl -n <namespace> get pods -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> get pdb <name>-pdb -o yaml
kubectl -n <namespace> exec <pod-name> -- bao operator raft list-peers
{{< /command >}}

The Raft command requires an authenticated OpenBao session with configuration read access. Use your approved
interactive login path; do not pass a privileged token on the command line.

{{< callout type="note" title="A PodDisruptionBudget covers voluntary disruption only" >}}
Node drains, autoscaler evictions, and the eviction API respect a PDB. Node loss, OOM termination, and kernel failure
do not. Keep enough voters across independent failure domains.
{{< /callout >}}

Drain one failure domain at a time and wait for replacement readiness before continuing:

{{< command label="apply" title="Drain a node" >}}
kubectl drain <node-name> --ignore-daemonsets --delete-emptydir-data
{{< /command >}}

Do not use `--disable-eviction` to bypass the PDB during routine maintenance.

During normal Pod termination, the wrapper forwards `SIGTERM` to OpenBao and waits for it to exit. It sends
`SIGKILL` if the child remains alive after 25 seconds. Managed OpenBao Pods use the default 30-second termination grace
period. Forced deletion or a shorter grace period can prevent OpenBao from completing shutdown.

## Scale with the parent resource

Change `spec.replicas`, not the StatefulSet. During a voter scale-down, the operator removes the highest ordinal one
step at a time, steps it down if it is the leader, removes its Raft peer, and waits for the StatefulSet to settle before
the next decrement.

If peer removal succeeds but the replica update is interrupted, the removed process can remain Ready. If a
separate disruption leaves the removed Pod terminating, the operator can finish that decrement after authenticated
Raft observations confirm that the selected peer is absent and every retained member has the expected role,
address, health, and ready Pod.

Recovery changes only the replica count, then requeues before applying other workload changes. Unavailable or
ambiguous observations keep the workload unchanged. For a terminating victim, recovery requires the same
highest-ordinal Pod UID, StatefulSet owner, and current revision. Only that target workload can have one fewer
current replica; companion workloads retain their normal checks.

Recovery compares two authenticated configurations that bracket fresh retained-member health checks. The member
IDs, normalized addresses, roles, and leader must match. OpenBao 2.6.4 returns a zero configuration index, so
recovery treats zero as unavailable metadata. A changed index still blocks recovery when either observation
supplies a nonzero value. These observations do not establish a committed configuration epoch. Recovery assumes
operator-managed membership without concurrent native membership changes. Matching observations cannot exclude a
change that is reverted between reads.

[Raft permits one uncommitted membership change at a
time](https://github.com/hashicorp/raft/blob/v1.7.3/configuration.go#L139-L160). Interrupted voter recovery
requires enough healthy retained voters for a majority of the pre-decrement voter count. This permits recovery
from three voters to two. Interrupted recovery from two voters to one remains blocked and requires manual
recovery. Read-replica recovery retains every voter and requires the voter workload to be settled and healthy.

This recovery applies to voters and read replicas. Status observations and operation-lock renewals do not block
it. The operator checks the scale decision and operation identity again before changing the replica count. A
changed operation, active restore hold, or break-glass state blocks recovery.

{{< command label="apply" title="Set the voter count" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{
  "spec": {
    "replicas": 3
  }
}'
{{< /command >}}

Hardened clusters reject fewer than three voters. The StatefulSet deletes the PVC for an ordinal removed by scaling,
so take a recovery snapshot before reducing capacity.

## Request a rolling restart

Change `spec.runtime.restartAt` to a new non-empty value. An RFC 3339 timestamp is recommended.

{{< command label="apply" title="Restart managed workloads" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p "{
  \"spec\": {
    \"runtime\": {
      \"restartAt\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
    }
  }
}"
{{< /command >}}

The value becomes a Pod-template annotation and creates a new StatefulSet revision. When steady read replicas exist,
the operator converges that pool before it restarts voters.

## Authorize direct maintenance only when required

Set maintenance mode only for a controlled action that must directly mutate or delete a managed resource:

{{< command label="apply" title="Enable maintenance mode" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{
  "spec": {
    "maintenance": {
      "enabled": true
    }
  }
}'
{{< /command >}}

The operator writes `openbao.org/maintenance=true` to managed voter and read-replica Pods and StatefulSets. Admission
allows a caller through only when that annotation is present and the caller has the custom `maintenance` verb on the
owning `OpenBaoCluster`. The caller still needs ordinary Kubernetes RBAC for the target action.

Disable maintenance mode as soon as the direct action is complete.

## Pause reconciliation for a bounded repair

Use `spec.paused` when the operator must stop changing one cluster while you inspect it:

{{< command label="apply" title="Pause a cluster" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{"spec":{"paused":true}}'
{{< /command >}}

The workload and admin-operation reconcilers stop, while the status reconciler continues deletion and finalizer work
and marks evaluated conditions with reason `Paused`. `Available` becomes `Unknown`; a paused status is not proof of
health.

Kubernetes controllers continue acting while the operator is paused. A StatefulSet can recreate a Pod, a scheduler
can move it, and storage or node controllers can still change infrastructure.

Resume only after you record the manual change and its desired-state consequence:

{{< command label="apply" title="Resume reconciliation" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{"spec":{"paused":false}}'
{{< /command >}}

Pause is independent of blue-green break glass. If `status.breakGlass.active=true`, follow
[Recover a failed rollback](../recover-failed-rollback/) and acknowledge its current nonce only after repair.

## Close the window

Verify `status.phase=Running`, `Available=True`, all declared replicas Ready, and the expected Raft membership. Then
uncordon drained nodes and remove any temporary maintenance RBAC binding.
