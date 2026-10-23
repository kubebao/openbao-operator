---
title: Recover a failed rollback
description: Repair a blue-green rollback failure, acknowledge the current nonce, and retry or restore safely.
eyebrow: Operate · Recovery
weight: 9
verifiedBy:
  - api/v1alpha1/openbaocluster_status_types.go
  - api/v1alpha1/openbaocluster_types.go
  - internal/service/upgrade/bluegreen/break_glass.go
  - internal/service/upgrade/bluegreen/rollback_workflow.go
  - internal/service/upgrade/bluegreen/break_glass_test.go
  - internal/controller/openbaocluster/status_upgrade_helpers.go
---

The current break-glass surface is specific to failed blue-green rollback work. It halts blue-green reconciliation
until `spec.breakGlassAck` matches the current status nonce. Acknowledgment means “retry the halted automation”; it is
not a repair by itself.

## Read the recorded failure

{{< command label="inspect" title="Capture break-glass and rollback state" >}}
kubectl -n <namespace> get openbaocluster <name> -o jsonpath='{.status.breakGlass}' | jq
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.blueGreen.phase}{"\n"}{.status.blueGreen.lastJobFailure}{"\n"}'
kubectl -n <namespace> get jobs -l openbao.org/cluster=<name>
kubectl -n <namespace> logs job/<job-from-status>
{{< /command >}}

| Reason | Expected phase | Repair boundary |
| --- | --- | --- |
| `RollbackConsensusRepairFailed` | `RollingBack` | Inspect the rollback repair Job, Pod health, and current Raft membership |
| `RollbackCleanupPeerRemovalFailed` | `RollbackCleanup` | Confirm stale Green peers have been removed or can be removed safely |

The status `steps` are generated for the specific failed Job. Follow them before applying a generic procedure.

Raft inspection requires an authenticated OpenBao session with configuration read access. Use your approved
interactive login path; do not put a privileged token in the command line or shell history.

## Stabilize the cluster

Inspect Pods and membership from a healthy voter:

{{< command label="inspect" title="Check rollback health" >}}
kubectl -n <namespace> get pods -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> exec <healthy-pod> -- bao status
kubectl -n <namespace> exec <healthy-pod> -- bao operator raft list-peers
{{< /command >}}

If direct Pod work is required, use [planned maintenance](../maintenance/) so admission has an explicit maintenance
signal. Pause the cluster while performing a longer manual repair if normal reconciliation would race your change.

Do not lower `spec.version` to escape the incident. Downgrades below `status.currentVersion` are blocked, and changing
the requested version does not repair rollback membership.

## Acknowledge only after repair

Copy the current nonce immediately before the acknowledgment. A later break-glass event has a different nonce.

{{< command label="apply" title="Acknowledge the current break-glass event" >}}
NONCE=$(kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.breakGlass.nonce}')
kubectl -n <namespace> patch openbaocluster <name> --type merge -p "{
  \"spec\": {
    \"breakGlassAck\": \"${NONCE}\"
  }
}"
{{< /command >}}

For either current reason, a matching acknowledgment clears active break glass, increments the rollback attempt,
clears the recorded Job failure count, and schedules a new rollback or cleanup attempt. Watch the replacement Job and
`status.blueGreen.phase`.

## Wait for rollback cleanup

After repairing Blue consensus and removing Green peers from Raft, the operator deletes the discarded Green
StatefulSet. It waits for the Green Pods to disappear and for all Pod references to their data claims to be released,
then deletes those data PVCs. A retry of the same version provisions fresh Green data claims; removed Raft peers cannot
rejoin with their old data.

Cleanup preserves Blue data claims and shared ACME cache or audit storage. This upgrade cleanup is independent of
`spec.deletionPolicy`, which applies when deleting the cluster. Each deleted PVC's volume follows its PV reclaim policy.

Rollback remains in `RollbackCleanup` while a Pod still references a discarded claim or a PVC is terminating. Inspect
the remaining Pods, PVCs, and storage events. Do not remove protection finalizers or clear upgrade status to force a retry.

## Restore when rollback repair is no longer trustworthy

If the cluster state cannot be repaired safely, select a known-good snapshot and use the forced lock-override path in
[Restore a snapshot](../restore/). That path is destructive and must be a separate, immutable `OpenBaoRestore`
request. Do not acknowledge rollback automation and start a forced restore concurrently.
