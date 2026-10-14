---
title: Workload lifecycle
description: How the workload path prepares TLS and infrastructure, initializes one node, and scales safely.
eyebrow: Cluster lifecycle
weight: 2
verifiedBy:
  - internal/app/openbaocluster/runtime_applications.go
  - internal/app/openbaocluster/infra.go
  - internal/app/openbaocluster/infra_operational_ordering_test.go
  - internal/app/openbaocluster/workload.go
  - internal/app/openbaocluster/workload_test.go
  - internal/service/certs/manager.go
  - internal/service/certs/manager_test.go
  - internal/service/certs/reload_test.go
  - internal/service/init/manager_reconcile.go
  - internal/service/init/manager_initialize.go
  - internal/service/init/manager_test.go
---

The workload controller converges the resources needed to run OpenBao. First boot is deliberately different from steady
state: the operator starts one pod, confirms initialization, and only then applies the requested replica count.

## Reconcile in dependency order

The application layer runs the workload reconcilers in this order:

1. Reconcile certificate material or verify its readiness.
2. Reconcile infrastructure. This step validates the version and image, computes safe StatefulSet intent, renders
   bootstrap configuration, and then reconciles networking, identity, and voter and read-replica workloads.
3. Reconcile storage expansion and any restart needed to observe it.
4. Reconcile initialization while the cluster is uninitialized.
5. Reconcile Day 2 Autopilot settings after initialization, when no upgrade blocks the change.

The infrastructure step is an application-level sequence, not six independent top-level reconcilers. That ordering keeps
configuration and resource identity aligned before the workload is applied.

## Keep service write surfaces narrow

| Service | Primary responsibility |
| --- | --- |
| Certificates | Create or observe TLS material and signal in-pod reload when active leaf content changes |
| Bootstrap and configuration | Render `config.hcl`, self-init requests, seal prerequisites, ACME cache storage, and managed audit-file storage |
| Networking | Reconcile Services, Ingress or Gateway resources, backend trust, and NetworkPolicies |
| Identity | Reconcile the workload ServiceAccount and namespaced RBAC |
| Workload | Reconcile voter and read-replica StatefulSets, PodDisruptionBudgets, revision resources, and rollout triggers |
| Storage | Reconcile volume expansion and controlled restart progress |
| Initialization | Detect or perform initialization, persist the appropriate status, and make the first Autopilot attempt |

The services share three contracts:

- `internal/service/configuration` keeps `config.hcl` semantics consistent between normal bootstrap and blue-green startup;
- `internal/platform/resourceidentity` keeps names, labels, and selectors consistent;
- `internal/platform/resourceapply` provides owner-aware server-side apply behavior while service-specific exceptions stay
  with the owning service.

## Apply the TLS ownership model

| Mode | Certificate owner | Certificate service behavior |
| --- | --- | --- |
| `OperatorManaged` | Operator | Create and rotate the CA and server Secrets; signal reload after leaf content changes |
| `External` | User or external controller | Wait for the required Secrets, validate them, and signal reload after their content changes |
| `ACME` | OpenBao | Do not create or watch certificate Secrets; OpenBao and the rendered listener configuration own issuance and cache lifecycle |

Certificate changes do not require a StatefulSet rollout. The certificate service computes the active certificate hash
and signals the ready workload to reload only when the hash changes.

{{< callout type="note" title="ACME is outside the certificate service" >}}
The certificate service returns without action in ACME mode. Listener rendering and cache prerequisites belong to the
bootstrap and configuration path; live issuance belongs to OpenBao.
{{< /callout >}}

## Initialize one pod, then scale

The workload specification is capped at one voter until `status.initialized` is true. This prevents multiple fresh pods
from racing to form the first Raft cluster.

### Self-initialization

OpenBao performs its own initialization. The initialization service observes readiness, health, and registration state,
then sets `status.initialized` and `status.selfInitialized`. It does not create a root-token Secret. After success, it
makes the self-init request ConfigMap inert so a later restart cannot replay the bootstrap request.

### Operator initialization

The initialization service waits for the first pod and required TLS material, checks whether the cluster is already
initialized, and calls the init API only when needed. It stores the returned root token in the cluster root-token Secret
without logging the init response, then marks the cluster initialized.

Detecting an already initialized cluster is a recovery path for an operator-managed cluster. It does not turn the CRD
into an import API for unmanaged clusters.

### Autopilot and scale-out

Initialization makes an initial attempt to configure Raft Autopilot. A separate Day 2 reconciler retries and applies
later Autopilot changes after initialization. It yields while an upgrade owns the relevant lifecycle state.

Once initialization status is durable, the infrastructure path removes the one-pod cap. Additional voter and read
replica pods join through the rendered Raft configuration. Safe scale-down and restart ordering are computed before the
StatefulSets are applied.

## Preserve the handoff

| Boundary | Required outcome |
| --- | --- |
| Before the first pod | TLS or ACME prerequisites, configuration, networking, identity, and storage are ready enough for startup |
| Before initialization | Pod 0 is reachable through the configured trust path |
| Before scale-out | Initialization is confirmed in status; self-init bootstrap requests cannot replay |
| During steady state | Generated resources remain operator-owned and changes route through the custom resource |
| During an upgrade | Workload repair continues, but Autopilot and rollout changes respect upgrade ordering |

For user-facing configuration choices, see [Configure a cluster](../../configure/).
