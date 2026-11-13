---
title: Review production readiness
description: Establish the security, durability, observability, and recovery controls required before serving production traffic.
eyebrow: Operate · Readiness
weight: 1
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - internal/controller/openbaocluster/status_production_ready.go
  - internal/controller/openbaocluster/status_production_ready_test.go
  - internal/platform/admission/check.go
  - internal/service/workload/pdb.go
---

Use `ProductionReady=True` as an operator-known configuration gate, then prove the controls that the operator cannot
observe: cloud permissions, client access, alert delivery, restore usability, and your response process.

## Meet the operator-known gate

For a Hardened cluster, `ProductionReady=True` requires the installed admission guardrails and the configured
security posture to pass the controller's checks. Depending on the configuration, those checks include:

- non-static unseal and a usable provider identity; plugin-backed KMS also requires a matching declared KMS plugin
- External or ACME TLS, with required integration and shared-cache conditions
- self-initialization instead of a stored root-token bootstrap
- acceptable network, audit storage, edge integration, and workload security settings

Read the condition before routing traffic:

{{< command label="verify" title="Inspect the production gate" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{range .status.conditions[?(@.type=="ProductionReady")]}{.status}{" "}{.reason}{"\n"}{.message}{"\n"}{end}'
{{< /command >}}

{{< callout type="note" title="ProductionReady is not an end-to-end test" >}}
The condition reports controls the operator can evaluate. It does not prove that users can log in, external firewalls
pass traffic, cloud-side policies grant access, alerts reach an operator, or a snapshot can be restored.
{{< /callout >}}

## Prove the operating controls

{{< checklist title="Before production traffic" >}}
- Set an availability objective and alert on `Available`, `Degraded`, `OpenBaoSealed`, and `OpenBaoLeader`.
- Monitor storage capacity, PVC health, restart loops, reconciliation errors, certificate expiry, and unseal dependencies.
- Run a scheduled backup, confirm the object in storage, and restore it into an isolated target.
- Record the backup identity, retention owner, recovery point objective, and recovery time objective.
- Test the human authentication path and the emergency-access custody process without using a root token for routine work.
- Pin an upgrade strategy, review the compatibility matrix, and document the rollback boundary.
- Confirm at least three voter replicas for a production Raft quorum and distribute them across failure domains.
- Verify that a node drain respects the managed PodDisruptionBudget.
{{< /checklist >}}

## Capture a baseline

{{< command label="verify" title="Record cluster, workload, and Raft state" >}}
kubectl -n <namespace> get openbaocluster <name> -o yaml
kubectl -n <namespace> get pods,pvc,pdb -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> exec <pod-name> -- bao status
kubectl -n <namespace> exec <pod-name> -- bao operator raft list-peers
{{< /command >}}

Keep this evidence with the service runbook. Recheck it after an upgrade, maintenance window, restore rehearsal, or
security-boundary change.

The Raft command requires an authenticated OpenBao session with configuration read access. Use your approved
interactive login path; do not pass a privileged token on the command line.

Next, [configure backups](../backups/) and [review the compatibility matrix](../../reference/compatibility/).
