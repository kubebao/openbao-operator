---
title: Recover an uncertain restore
description: Inspect a restore that ended Unknown, complete recovery, and release management with Resume or Abandon.
eyebrow: Operate · Restore
weight: 1
verifiedBy:
  - internal/service/restore/acknowledgement.go
  - internal/service/restore/manager_effects.go
  - internal/service/restore/target_health.go
  - internal/service/restore/recovery_restart.go
  - internal/service/workload/restore_handoff.go
  - internal/service/restore/recovery_status.go
  - config/policy/openbao-protect-restore-execution.yaml
---

A restore ends `Unknown` when the snapshot was submitted but the operator cannot confirm that it finished applying,
or when it cannot determine whether submission happened. The operator never retries it. The target keeps its
`openbao.org/restore-hold` annotation and the operation lock until an administrator acknowledges recovery.

{{< callout type="note" title="Guarantees and limits" >}}
- The hold stops operator management of the target. It does not stop executor or OpenBao processes.
- `Resume` checks health and restored operator access, then restarts voters and read replicas one at a time.
  It does not prove snapshot application or fence old processes.
- An existing-cluster restore stays `Unknown` after management resumes. Its recovery disposition records the handoff.
{{< /callout >}}

## Recover the target

1. Inspect the restore status, executor logs, and restored data. Stop remaining restore executors and stop or fence
   old server processes outside the recovered workload. Deleting a Pod object does not stop a process on an
   unreachable node. An executor Pod that failed before any container started never reports termination; fence its
   node if needed, then delete that Pod.
2. Repair voter health, unseal access, and restored operator JWT/TLS trust when needed. See
   [Restore authentication](../authentication/). Keep application traffic stopped
   throughout recovery.
3. Acknowledge `Resume` and wait for the managed restart to finish. Use `Abandon` if recovery must continue by hand.

## Acknowledge the outcome

{{< command label="recover" title="Release management after recovery" >}}
operation_id=$(kubectl -n <namespace> get openbaorestore <restore-name> -o jsonpath='{.metadata.uid}')
kubectl -n <namespace> annotate openbaorestore <restore-name> \
  "openbao.org/restore-acknowledge=${operation_id}/Resume" --overwrite
{{< /command >}}

| Action | Effect |
| --- | --- |
| `Resume` | Keeps the hold and lock while replacing each original voter and read-replica Pod. Releases management after all replacements are ready. |
| `Abandon` | Pauses the original target, removes its hold, and releases the lock. The administrator takes responsibility for remaining processes and resources. |

`Resume` requires a recorded Job UID, terminated executor Pods, and a live, unchanged target that is not paused, not
being deleted, and not locked by another operation. Disposable targets do not support `Resume`. If the target is
missing or replaced, `status.message` directs you to use `Abandon`. Acknowledgements are accepted after the restore is `Unknown`, or
after target cleanup fails (`Abandon` only). You cannot pre-approve a future outcome.

## Monitor recovery

Watch the `Recovery` column and `status.message`. The request keeps its hold while waiting. Recovery has no automatic
timeout or replay.

| `RecoveryReleased` reason | Meaning |
| --- | --- |
| `AwaitingAcknowledgement` | No valid acknowledgement yet. |
| `Restarting` | A Pod is being replaced; the message shows progress. |
| `WaitingForPod` | A Pod is missing or not Ready. Check its events, unseal access, and readiness. |
| `VoterHealthUnavailable` | Not every voter is initialized, unsealed, and healthy. |
| `OperatorAccessUnavailable` | The operator cannot authenticate or read Raft membership. Repair JWT and TLS trust. |
| `MembershipChanged` | Raft membership does not match the workload. Inspect it before continuing. |
| `RecoveryBlocked` | A precondition failed, such as a changed topology or target identity. The message names it; restore the condition or use `Abandon`. |
| `Resumed`, `Abandoned` | Management was handed back. |

The operator prefers remaining standbys and steps down a multi-voter leader before restarting it. A single-voter
restart interrupts service. Both `RollingUpdate` and `OnDelete` use this path. With manual unseal, unseal each new Pod
before recovery can continue. Do not change the workload topology during the restart.

For a fresh retained target, `Resume` also enables the normal configuration and Kubernetes API token mount before
replacing its Pod. The hold remains until that replacement is healthy and controller authentication succeeds.

Wait for `status.restart.completedAt`, `status.administratorDisposition: Resume`, and `RecoveryReleased=True` with
reason `Resumed`. A retained fresh target can then become `Completed` if its application was confirmed. An
existing-cluster request remains `Unknown`; that phase no longer means management is held once recovery is released.

The `openbao_restore_state` metric reports 0 when no request exists, 1 for pending or running, 2 for success, 3 for failure, 4 for an unresolved unknown
outcome or blocked cleanup, 5 for restarting, 6 for resumed management, and 7 for administrator handoff. It is rebuilt
from retained requests. An unresolved recovery takes precedence over newer requests; otherwise the newest request
wins. Deleting the last request removes the series, which the dashboard displays as no restore.

## Cancel managed recovery or take over

Once `status.restart` is recorded, removing the annotation does not cancel the restart. Set the same request UID
with `/Abandon` to take over before recovery is released. After `status.administratorDisposition` is recorded,
admission rejects a conflicting decision. Manage the target directly after that handoff. If the controller restarted
after releasing the hold but before recording `Resume`, `Abandon` still pauses the cluster; check `RecoveryReleased`
before switching.

Before `Abandon`, suspend GitOps reconciliation for the target or ensure its desired configuration keeps
`spec.paused: true`. Otherwise GitOps can revert the pause and allow normal management to resume. Pausing management
does not stop running Pods or client traffic.

After the handoff, check `status.administratorDisposition`, `status.completionTime`, and `RecoveryReleased`.
Delete the request when its evidence is no longer needed. Keep the namespace enrolled and the operator running until
finalizers finish. For an abandoned target, set `spec.paused: false` only after administrator recovery is complete.
