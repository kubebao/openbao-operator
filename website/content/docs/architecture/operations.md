---
title: Operations
description: The coordination model for backup, restore, upgrade, status persistence, and operation locks.
eyebrow: Day 2 and recovery
weight: 3
verifiedBy:
  - internal/app/openbaocluster/adminops/reconcile.go
  - internal/service/configuration/policies.go
  - internal/app/openbaocluster/adminops/reconcile.go
  - internal/app/openbaocluster/patch_test.go
  - internal/app/openbaorestore/reconcile.go
  - internal/app/openbaorestore/reconcile_test.go
  - internal/service/opslifecycle/lock.go
  - internal/service/opslifecycle/lock_test.go
  - internal/service/backup/manager_reconcile.go
  - internal/service/backup/manager_retention_test.go
  - internal/service/restore/manager_reconcile.go
  - internal/service/restore/manager_test.go
  - internal/service/upgrade/rolling/manager_test.go
  - internal/service/upgrade/bluegreen/manager_phase_machine_test.go
  - internal/service/upgrade/snapshot/reconcile_test.go
---

Backup, restore, and upgrade are long-running operations with different control paths but one collision boundary. Their
progress is persisted so reconciliation can resume after a process restart or a transient failure.

## Separate routine repair from disruptive work

| Path | Responsibility |
| --- | --- |
| Workload controller | Keep configuration, identity, networking, storage, and StatefulSets converged |
| AdminOps controller | Run backup schedules and upgrade strategies without blocking workload repair |
| Restore controller | Validate and execute explicit `OpenBaoRestore` requests outside normal cluster reconciliation |
| Operation lifecycle service | Standardize lock identity, acquire and release behavior, retry intent, and phase audit fields |

`internal/service/opslifecycle` coordinates operations; it does not choose an upgrade strategy, validate a restore
source, or decide whether a backup target is usable. Those decisions remain in the domain services.

## Use one persisted operation lock

`status.operationLock` is the mutex for disruptive work on an `OpenBaoCluster`. The lock records both the operation and
holder. Acquisition renews an exact matching lock, and release succeeds only for the matching identity.

If another operation owns the lock, the caller records actionable contention and requeues. Force takeover is limited to
an explicit restore override or server-side apply conflict recovery; normal backup and upgrade work does not steal the
lock.

The lock has its own server-side apply field manager. Backup and upgrade state share the AdminOps status plane, so those
writers use a fresh read, mutate one concern, and apply the whole AdminOps plane to preserve sibling fields.

## Check policy readiness per operation

When policy reconciliation is enabled, a new backup or upgrade checks only its corresponding policy observation.
An absent observation does not block the operation: enabling reconciliation before enrollment preserves existing
operations. Enrollment failures remain visible through `PolicyReconciliationReady=False` and warning events.

Once a policy has been observed, a missing policy or contents that differ from the required variant block new
operations that need it until repair succeeds. Failed reads preserve earlier observations. A blocked upgrade does not
block an independently approved backup. Running operations can finish and release their locks.

Restore never waits for policy reconciliation. Its configured OpenBao credentials must still authorize the restore.
Policy observations coordinate operations; OpenBao ACLs remain the authorization boundary.

## Back up without moving data through the controller

The backup service runs from the AdminOps path:

1. Evaluate a cron window or manual trigger and prevent duplicate launches for the same window.
2. Validate cluster, authentication, target, and conflicting-operation prerequisites.
3. Acquire the backup operation lock.
4. Create an executor Job. The Job authenticates to OpenBao, streams the Raft snapshot, and uploads it to object storage.
5. Observe the terminal Job result and update backup timing, success, or failure status once.
6. If status already records a successful backup, attempt retention after terminal processing, then release the lock.

The controller never handles snapshot bytes. Provider credentials and transport details are Job inputs; schedule,
status, lock, and retention decisions remain in the control plane. A later failed attempt does not clear
`status.backup.lastBackupTime`, so the current implementation can run retention after that failure using the last
successful backup as its guard.

## Restore through an explicit request

`OpenBaoRestore` is an immutable, CRD-backed job request. Its dedicated controller delegates to the restore application
and service instead of hiding destructive recovery in `OpenBaoCluster` reconciliation.

The restore service validates the target, snapshot source, credentials, initialization state, and lock state before it
creates a restore Job. A force option may override specific safety checks, and operation-lock override requires explicit
intent. The executor claims submission once and never retries it. When the operator cannot confirm that the
snapshot applied, the request ends `Unknown` and keeps the restore hold and operation lock until an administrator
acknowledges `Resume` or `Abandon`. `Resume` runs a managed restart before releasing management.

A fresh target created from `spec.clusterTemplate` can end `Completed`, because the operator observes the expected
source identity on an empty target. `Completed` still does not prove that applications can use the restored data.

## Upgrade with strategy-owned state machines

Upgrade runs on the AdminOps path. Version and image policy, health, operation-lock state, and optional snapshot
prerequisites are checked before a strategy advances.

| Package | Responsibility |
| --- | --- |
| `internal/service/upgrade/rolling` | StatefulSet partition progression, reverse-ordinal replacement, leader step-down, health checks, retries, and convergence |
| `internal/service/upgrade/bluegreen` | Green deployment, synchronization, promotion, cutover, rollback, cleanup, and break-glass |
| `internal/service/upgrade/core` | Shared lock, status, metrics session, and strategy-neutral lifecycle mechanics |
| `internal/service/upgrade/snapshot` | Shared pre-upgrade snapshot validation, Job preparation, and existing-Job result handling |
| `internal/service/upgrade/raftops` | OpenBao and Raft leader, membership, promotion, demotion, removal, and Autopilot actions |

### Rolling strategy

The manager holds the StatefulSet partition and replaces one pod at a time in reverse ordinal order. It transfers
leadership before replacing the leader, waits for Kubernetes and OpenBao health after each step, and finalizes only when
revision and workload state converge.

### Blue-green strategy

The manager creates a parallel green revision, joins and synchronizes it, promotes it through explicit phases, and
switches the client Service only after the green side can safely take over. Late failures enter rollback. If consensus
repair cannot continue safely, the manager records break-glass state and stops risky automation.

Blue-green needs a second workload revision and approximately doubles storage use during the transition.

## Persist enough state to resume

| Surface | Durable intent |
| --- | --- |
| `status.backup` | Schedule window, last attempt and success, next run, and failure counters |
| `OpenBaoRestore.status` | Validation, running, terminal result, conditions, and Job progress |
| `status.upgrade` | Rolling progress, completed pods, failure, and finalization state |
| `status.blueGreen` | Active phase, promotion, cutover, cleanup, or rollback progress |
| `status.upgradeRequests` | Edge-trigger bookkeeping for retry, promote, and rollback requests |
| `status.breakGlass` | Nonce and diagnostics for manual acknowledgement |
| `status.operationLock` | Current disruptive operation and exact holder |

Pre-upgrade snapshot flows and routine backup flows share backup port types and Job-building/runtime contracts, but the
upgrade snapshot package owns pre-upgrade orchestration. This preserves the service dependency boundary while keeping
executor behavior consistent.

{{< callout type="warning" title="Availability wins over automatic progress" >}}
An ambiguous health result pauses or retries a rolling upgrade. A late blue-green failure rolls back or enters break
glass. A restore never becomes a routine reconcile side effect.
{{< /callout >}}
