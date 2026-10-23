---
title: Prepare for production operations
description: Establish access, backup, restore, monitoring, and safe upgrade procedures after creating the first cluster.
eyebrow: Get started · Step 5
weight: 5
verifiedBy:
  - api/v1alpha1/openbaocluster_operations_types.go
  - api/v1alpha1/openbaorestore_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - internal/service/upgrade/strategy_transition.go
  - internal/service/upgrade/strategy_transition_test.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - docs/user-guide/openbaocluster/operations/upgrades.md
---

A running cluster proves the installation path. Complete these controls before teams depend on it as a production
service.

## Complete the production baseline

| Area | Required outcome |
| --- | --- |
| Security | A complete `Hardened` profile, external trust, non-static unseal, and reviewed delegated authority |
| Access | Tested operator lifecycle authentication and at least one tested human login |
| Durability | Explicit storage, retention, deletion, and failure-domain decisions |
| Recovery | Scheduled snapshots and a successful restore rehearsal |
| Exposure | Reviewed TLS, service, DNS, ingress or Gateway API, and NetworkPolicy boundary |
| Observability | Alerts for status conditions, sealing, leadership, storage, Jobs, and reconciliation failures |
| Change | A staged upgrade procedure with backup, rollback, and post-change checks |
| Ownership | Named responders for Kubernetes, OpenBao administration, unseal, backup storage, and external dependencies |

Use the [configuration guide](../../configure/) to replace evaluation defaults. Use the
[security guide](../../security/) to review authority and trust boundaries.

## Test access before removing bootstrap options

- Confirm the controller can authenticate through its projected JWT and perform its maintenance calls.
- Test the human authentication method, role, policy, and network path with a real non-root identity.
- Confirm backup, restore, and upgrade Jobs use their own ServiceAccounts and policies.
- Store no root token or unseal material in tickets, shell history, logs, or unencrypted support bundles.

`ProductionReady=True` is not proof of a usable human login. The user-access condition is a heuristic, not an
end-to-end authentication test.

## Prove backup and restore

Configure the backup target, credentials or workload identity, schedule, retention, and required egress. Then verify:

1. The backup Job authenticates to OpenBao and reads a Raft snapshot.
2. The Job writes the expected object with the intended encryption and retention.
3. Monitoring records both success and failure.
4. A separate restore rehearsal reconstructs a cluster and verifies data, leadership, sealing, and client access.

A successful snapshot is not a completed recovery control until restore has been exercised.

## Establish the service boundary

Choose the external access mechanism together with its TLS mode, DNS owner, source restrictions, and failure behavior.
Review which controller owns each generated Service, Ingress, TLSRoute, certificate, and NetworkPolicy. Temporary
evaluation exposure must not become the production boundary by accident.

## Plan upgrades

Start with `RollingUpdate` unless parallel Green validation or manual promotion justifies the extra BlueGreen
resources and authority. Before changing a version:

- verify the exact operator, Kubernetes, and OpenBao versions in the [compatibility matrix](../../reference/compatibility/);
- make a restorable pre-upgrade snapshot;
- confirm the upgrade Job's JWT role and image;
- define hold, retry, rollback, and post-upgrade checks;
- stage the exact topology and integrations outside production.

## Switch an idle upgrade strategy

OpenBao Operator 0.4.2 and later can switch an existing cluster from `RollingUpdate` to `BlueGreen` or from `BlueGreen` to
`RollingUpdate` without renaming the active StatefulSet or replacing its PVCs.

The cluster must be initialized and idle. Every voter and configured read replica must be Ready,
`status.currentVersion` must equal `spec.version`, and no upgrade, backup, restore, resize, restart, Green workload,
pending request, failure, operation lock, or safe-mode recovery can be active.

1. Finish or recover every active disruptive operation.
2. Verify `status.phase=Running`, `Available=True`, all replicas Ready, and BlueGreen absent or `Idle` without a Green
   revision.
3. Before switching to `BlueGreen`, configure a resolvable upgrade executor image and ensure its JWT role already has
   the [BlueGreen peer-management capabilities](../operator-authorization/#define-the-upgrade-policy).
4. Patch only `spec.upgrade.strategy`.
5. Wait for `status.acceptedUpgradeStrategy` to report the requested value.
6. Change the OpenBao version, image, replicas, storage, or restart controls in a later request.

{{< command label="switch" title="Switch BlueGreen to RollingUpdate" >}}
kubectl -n <namespace> patch openbaocluster <name> \
  --type merge \
  -p '{"spec":{"upgrade":{"strategy":"RollingUpdate"}}}'
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.acceptedUpgradeStrategy}{"\n"}'
{{< /command >}}

{{< command label="switch" title="Switch RollingUpdate to BlueGreen" >}}
kubectl -n <namespace> patch openbaocluster <name> \
  --type merge \
  -p '{"spec":{"upgrade":{"strategy":"BlueGreen"}}}'
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.acceptedUpgradeStrategy}{"\n"}'
{{< /command >}}

{{< callout type="warning" title="Separate the strategy and version requests" >}}
Do not combine a strategy change with a workload change. In particular, a pre-2.6 Blue leader cannot safely promote
a 2.6-or-newer Green revision. Switch an idle pre-2.6 `BlueGreen` cluster to `RollingUpdate`, wait for acceptance, and
only then request the 2.6.x upgrade.
{{< /callout >}}

Self-init policies are not rewritten after bootstrap. A cluster initialized with rolling-only upgrade permissions
needs a manual policy update before it can switch to `BlueGreen`.

## Monitor the operating contract

Alert on `Available`, `Degraded`, `ProductionReady`, TLS, storage, sealing, leadership, backup, restore, upgrade, and
operation-lock signals that apply to the cluster. Pair every alert with an owner, diagnostic entry point, and safe exit
condition.

Continue in the [operations guide](../../operate/) for routine changes, incidents, and recovery.
