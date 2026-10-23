---
title: Operate OpenBao
description: Back up, upgrade, maintain, troubleshoot, recover, and restore an OpenBao cluster.
eyebrow: Day 2 operations
weight: 3
hideChildren: true
verifiedBy:
  - api/v1alpha1/openbaocluster_operations_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - api/v1alpha1/openbaorestore_types.go
  - internal/controller/openbaocluster
  - internal/service/backup
  - internal/service/restore
  - internal/service/upgrade
---

Prepare routine operations before you need an incident response. A supportable cluster has a proven snapshot and
restore path, an upgrade policy, monitored conditions, and named owners for maintenance and recovery.

## Choose a task

| Goal | Start here |
| --- | --- |
| Establish the production operating baseline | [Review production readiness](production-readiness/) |
| Create and retain Raft snapshots | [Back up a cluster](backups/) |
| Change the OpenBao version | [Upgrade a cluster](upgrades/) |
| Drain, scale, restart, or pause a cluster | [Run planned maintenance](maintenance/) |
| Find the cause of a degraded service | [Troubleshoot a cluster](troubleshoot/) |
| Delete a cluster intentionally | [Decommission a cluster](decommission/) |
| Repair a sealed cluster | [Recover a sealed cluster](recover-sealed/) |
| Repair leadership or quorum | [Recover from no leader](recover-no-leader/) |
| Continue after a failed blue-green rollback | [Recover a failed rollback](recover-failed-rollback/) |
| Reintroduce state from a snapshot | [Restore a snapshot](restore/) |

## Start every incident with status

Status is the operator's latest observation. Events show how it got there. Inspect both before changing the cluster.

{{< command label="inspect" title="Collect the first cluster signals" >}}
kubectl -n <namespace> get openbaocluster <name> -o yaml
kubectl -n <namespace> get pods,pvc,services
kubectl -n <namespace> get events --sort-by=.lastTimestamp
{{< /command >}}

Use the condition `reason` and `message`, not only `status.phase`. See [Status and events](../reference/status-and-events/)
for the observable contract.

{{< callout type="warning" title="Choose repair before restore" >}}
Restore overwrites the target OpenBao Raft state. Use a symptom-specific recovery procedure first when the live
cluster can still be repaired safely.
{{< /callout >}}
