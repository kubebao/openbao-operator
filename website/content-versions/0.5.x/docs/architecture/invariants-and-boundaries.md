---
title: Invariants and boundaries
description: The safety properties, package layers, controller split, and status ownership model of OpenBao Operator.
eyebrow: Control plane
weight: 1
verifiedBy:
  - .agents/rules/architecture.md
  - .ast-grep/policy/architecture-boundaries.yml
  - internal/controller/openbaocluster/split_reconcilers.go
  - internal/controller/openbaocluster/split_reconcilers_test.go
  - internal/app/openbaocluster/applications.go
  - internal/app/openbaocluster/applications_test.go
  - internal/app/openbaocluster/patch_test.go
  - internal/port/adminops/status.go
  - internal/app/openbaocluster/adminopsstatus/mutate_test.go
  - test/integration/adminops_status_test.go
  - test/integration/adminops_restore_status_test.go
  - test/integration/adminops_upgrade_requests_test.go
  - internal/service/upgrade/acknowledgements.go
  - internal/platform/statusapply/openbaocluster_test.go
---

The operator is a lifecycle supervisor for operator-owned `OpenBaoCluster` resources. It is not an import layer for
arbitrary unmanaged OpenBao clusters.

## Preserve these invariants

| Area | Invariant |
| --- | --- |
| Identity | Provisioner and controller identities remain separate. Tenant access is explicitly provisioned, and Secret read or write access is derived from named resources rather than wildcard enumeration. |
| Ownership | The operator owns generated configuration, identity, networking, and workload resources. Users change the custom resource or use a documented maintenance path instead of editing generated objects. |
| Production posture | The Hardened profile requires self-initialization, trusted TLS, and a non-static unseal path. `OperatorManaged` TLS is not a Hardened production trust model. |
| Guardrails | Request-time authorization belongs to admission. Reconciliation rechecks the subset that must remain visible at runtime. Sensitive reconciliation pauses when required admission dependencies are unavailable. |
| Integrations | Gateway, ACME, audit storage, Kubernetes API, backup, and restore dependencies surface through explicit status and readiness contracts. The surrounding platform still owns the external systems. |
| Lifecycle | A new cluster starts with one pod. Restore remains an explicit destructive request. Backup, restore, and upgrade do not overlap on the same cluster. |
| Data consistency | OpenBao remains the source of truth for Raft snapshots, membership, and data consistency. The operator coordinates those operations instead of reimplementing the data plane. |
| Optional modules | Core APIs remain in `openbao.org`. Optional modules use separate API groups, may depend on stable core contracts, and must not become prerequisites of the core build or runtime. |

`internal/platform/hardenedcontract` assigns stable rule IDs and enforcement ownership to Hardened guardrails. Moving a
rule between admission and runtime enforcement is a contract change and requires the catalog, policy, runtime, and
agreement tests to change together.

## Follow the layer direction

| Layer | Packages | Responsibility |
| --- | --- | --- |
| L0 | `api/v1alpha1` | Declarative API data and validation markers |
| L1 | `cmd/*`, `internal/platform/entrypoint` | Process startup and dependency construction |
| L2 | `internal/controller/*` | Fetch, observe, delegate, patch, and requeue |
| L3 | `internal/app/*` | Coordinate domain workflows and phase ordering |
| L4 | `internal/service/*` | Own domain behavior such as workload, backup, restore, and upgrade |
| L5 | `internal/port/*` | Stable interfaces and neutral contract types |
| L6 | `internal/adapter/*` | Implement integrations and ports |
| L7 | `internal/platform/*` | Cross-cutting reconciliation, status, security, logging, and ownership utilities |

Dependencies point inward through the declared seams:

- controllers normally call app facades and platform utilities;
- app packages call services, ports, and platform utilities, but not adapters;
- services may call ports, adapters, and platform utilities, but never controllers;
- adapters may call ports and platform utilities, but never services or controllers;
- ports never import adapters.

The generated architecture policy enforces package allowlists. A new import across these boundaries requires an explicit
architecture decision, not only a compiling build.

## Keep controller work separated

| Controller path | Owns | Reason for the split |
| --- | --- | --- |
| `OpenBaoCluster` workload | Certificates, infrastructure, storage, initialization, Autopilot follow-up, and workload-side status | High-churn repair must continue while administrative work is waiting. |
| `OpenBaoCluster` AdminOps | Backup and upgrade orchestration | Long-running workflows need their own retry and status model. |
| `OpenBaoCluster` status | Observation, conditions, finalizer, and cluster deletion | Status aggregation and deletion must not be coupled to workload mutation. |
| `OpenBaoRestore` | Validation and destructive restore workflow | Restore has its own request, status, finalizer, and operation-lock lifecycle. |
| `OpenBaoTenant` provisioner | Tenant onboarding and namespace-scoped guardrails | Privileged namespace setup is a Day 0 responsibility, not a workload side effect. |

Each controller delegates to its matching `internal/app` package. Application code sequences domain services; services
own their write surfaces. Controllers must not accumulate broad business orchestration.

## Treat status as owned planes

The `OpenBaoCluster` status is divided among server-side apply field managers.

| Plane | Owns |
| --- | --- |
| Observed status | Phase, leader, replicas, current version, observed generation, and conditions |
| Workload status | Initialization, self-initialization, and workload progress |
| AdminOps status | Backup, upgrade, restore, blue-green, requests, break-glass, and AdminOps state |
| Operation lock | `status.operationLock` only |

A writer applies only its plane. Within the shared AdminOps plane, a writer must read the latest object, mutate its
concern, and apply the complete plane; omitting a sibling field with the same field manager can clear it. Read directly
from the API after a write when the next decision depends on the committed value because the controller cache can lag.

The AdminOps mutation gateway returns the object read after the apply, including any concurrent updates visible to that
read. It does not substitute the intended state for the observed state. If read-back fails, it returns an error even
though the apply succeeded. The application wrapper updates the caller's AdminOps fields and resource version only
after a successful read-back; it leaves other fields unchanged.

The final AdminOps patch persists pending changes to accepted upgrade strategy, blue-green state, break-glass state,
and AdminOps state. Managers persist backup, rolling-upgrade progress, and restore restart state.
The final patch preserves the latest API values for those manager-owned fields in the complete SSA payload. Changes to
those fields alone do not trigger a final patch.

Upgrade managers return pending request acknowledgements in `upgrade.ReconcileResult`, separate from observed cluster
status. The application accumulates these tokens across sub-reconcilers and applies only those acknowledgements in its
final write, including error and requeue paths. Checkpoint read-back cannot discard pending acknowledgements. A newer
request in spec remains pending because the write records the captured token, and unrelated acknowledgement fields
retain their latest API values. Promotion and rollback acknowledgements are saved with their resulting blue-green
state. Failed rolling retries retain their immediate checkpoint, which clears failure state and acknowledges the retry
in the same write.

Managers share the `adminops.StatusMutator` contract. The application layer binds it to the API reader and client through
`adminopsstatus.NewMutator`. Each write selects an ownership policy:

| Policy | Field ownership behavior |
| --- | --- |
| `RespectOwnership` | Never force ownership. |
| `ForceOwnershipOnConflict` | Retry without force first. If conflicts persist, retry with forced ownership. |
| `ForceOwnership` | Force ownership from the first attempt. |

Every policy retries conflicts with a fresh read and mutation. The fallback policy applies to both resource-version and
field-ownership conflicts. Routine AdminOps writes use that fallback; rolling upgrade finalization and retry cleanup
force ownership immediately. These policies apply to the AdminOps status plane.

## Account for tenancy watch boundaries

Single-tenant mode can watch owned child resources in the operator namespace. Multi-tenant mode avoids cluster-wide
child watches because those watches would require list and watch access across tenant namespaces. It relies on parent
events, explicit progress requeues, retries, and periodic status refresh instead.

Adding multi-tenant child watches changes the RBAC and trust model. Review the permissions, controller registration,
repair latency, and tests together.

{{< callout type="warning" title="Break glass is not normal ownership" >}}
Force ownership, restore lock override, and other escape hatches are explicit recovery paths. They must not become the
default way to resolve reconciliation conflicts.
{{< /callout >}}
