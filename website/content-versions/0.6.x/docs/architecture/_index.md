---
title: Architecture
description: The control-plane boundaries and lifecycle contracts that keep OpenBao Operator safe to reconcile.
eyebrow: Architecture
weight: 6
hideChildren: true
verifiedBy:
  - .agents/rules/architecture.md
  - .ast-grep/policy/architecture-boundaries.yml
  - internal/app/openbaocluster/runtime_applications.go
  - internal/controller/openbaocluster/split_reconcilers.go
---

OpenBao Operator separates fast workload repair from long-running operations and destructive recovery. The architecture
is organized around the contracts that must survive retries, controller restarts, and partial failure.

## Read by concern

| Concern | Reference |
| --- | --- |
| Package direction, controller roles, status ownership, and cross-cutting safety rules | [Invariants and boundaries](invariants-and-boundaries/) |
| Certificate readiness, configuration, workload resources, initialization, and scale-out | [Workload lifecycle](workload-lifecycle/) |
| Backup, restore, upgrade, operation locks, and resumable status | [Operations](operations/) |
| Tenant namespace RBAC, Secret allowlists, quotas, and controller handoff | [Tenant provisioning](provisioning/) |

## Lifecycle at a glance

1. The provisioner establishes a tenant namespace boundary when multi-tenancy is enabled.
2. The workload path prepares TLS and infrastructure, starts one OpenBao pod, initializes the cluster, and then scales it.
3. Workload reconciliation keeps generated resources converged while the status controller observes health.
4. The AdminOps path coordinates backup and upgrade. A dedicated restore path handles destructive recovery.
5. Backup, restore, and upgrade use one persisted operation lock so disruptive work does not overlap.

{{< callout type="note" title="Architecture is a change contract" >}}
Use these pages when changing controller responsibility, package dependencies, status ownership, or lifecycle ordering.
Use the task guides to install, configure, and operate a cluster.
{{< /callout >}}
