---
title: Configure operator authorization
description: Keep controller, backup, restore, and upgrade capabilities on separate OpenBao policies and identities.
eyebrow: Get started · Supporting decision
weight: 8
verifiedBy:
  - internal/adapter/config/builder.go
  - internal/adapter/config/testdata/render_self_init_backup_upgrade_policies.hcl
  - internal/service/backup
  - internal/service/restore
  - internal/service/upgrade
---

Give each controller or lifecycle Job only the OpenBao capabilities needed for its operation. Authentication proves
the actor's identity; these policies decide what that actor can do.

## Separate the policy surfaces

| Policy | Actor | Required capabilities | Reason for separation |
| --- | --- | --- | --- |
| Controller | Controller Deployment | Health, step-down, Raft configuration, peer removal, and Autopilot | Routine reconciliation must not receive restore authority |
| Backup | Backup Job | Read `sys/storage/raft/snapshot` | Snapshot streaming is independent of storage credentials |
| Restore | Restore Job | Update `sys/storage/raft/snapshot`; add `sys/storage/raft/snapshot-force` only for forced restores | Restore can replace the complete cluster state |
| Rolling upgrade | Upgrade Job | Health, step-down, snapshot read, and Autopilot state | Upgrade authority exists only on the executor |
| BlueGreen upgrade | Upgrade Job | Rolling capabilities plus join and peer management | Parallel cutover needs wider, temporary orchestration authority |

{{< callout type="warning" title="Restore is destructive" >}}
Both restore endpoints can replace data, policies, and keys across the cluster. The
`sys/storage/raft/snapshot-force` endpoint also bypasses seal-consistency verification. Bind restore capabilities only
to the restore identity. The restore Job is short-lived, but the OpenBao role and policy can remain configured when no
Job exists.
{{< /callout >}}

## Define the backup and restore policies

{{< command label="configure" title="Backup policy" >}}
path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}
{{< /command >}}

{{< command label="configure" title="Restore policy" >}}
path "sys/storage/raft/snapshot" {
  capabilities = ["update"]
}
{{< /command >}}

Add the force endpoint only when this identity must support `OpenBaoRestore.spec.force: true`:

{{< command label="configure" title="Optional forced-restore capability" >}}
path "sys/storage/raft/snapshot-force" {
  capabilities = ["update"]
}
{{< /command >}}

Backup and restore can use explicit token Secrets as a fallback. That does not make the controller or main OpenBao
ServiceAccount the correct identity for either operation. Self-init in 0.5.0 gives its generated restore policy both
restore capabilities. A custom policy can omit the force capability when forced restore is not part of its recovery
procedure.

## Define the upgrade policy

Rolling upgrades require snapshot read as well as health, step-down, and Autopilot state:

{{< command label="configure" title="RollingUpdate policy" >}}
path "sys/health" {
  capabilities = ["read"]
}

path "sys/step-down" {
  capabilities = ["sudo", "update"]
}

path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}

path "sys/storage/raft/autopilot/state" {
  capabilities = ["read"]
}
{{< /command >}}

BlueGreen uses the same baseline and adds these peer-management paths:

{{< command label="configure" title="Additional BlueGreen capabilities" >}}
path "sys/storage/raft/join" {
  capabilities = ["update"]
}

path "sys/storage/raft/configuration" {
  capabilities = ["read", "update"]
}

path "sys/storage/raft/remove-peer" {
  capabilities = ["update"]
}

path "sys/storage/raft/promote" {
  capabilities = ["update"]
}

path "sys/storage/raft/demote" {
  capabilities = ["update"]
}
{{< /command >}}

Built-in upgrade orchestration uses JWT. When an initialized cluster changes from `RollingUpdate` to `BlueGreen`,
self-init does not update the existing role. Add the peer-management capabilities before requesting the new strategy.

## Maintain policies after bootstrap

Self-init creates the initial policies only during initialization. For an existing cluster:

1. Review operator release notes for new OpenBao capabilities.
2. Compare the implemented policy with the cluster's current policy.
3. Apply the narrow policy change through an authenticated human administration path.
4. Verify the affected condition or operation before proceeding with other changes.

Missing controller capabilities can produce `Unknown` conditions or permission errors. The operator does not widen the
policy automatically.

{{< callout type="warning" title="Update restore policies before upgrading from 0.4.2" >}}
OpenBao Operator 0.4.2 generated restore policies with only `update` on
`sys/storage/raft/snapshot-force`. In 0.5.0, a restore with `force` omitted or set to `false` uses
`sys/storage/raft/snapshot`. Add the normal endpoint through an authenticated administration path before you upgrade
the controller. Self-init does not update an existing policy.
{{< /callout >}}

## Troubleshoot authorization

| Symptom | Likely boundary | Check first |
| --- | --- | --- |
| JWT login succeeds but the request is denied | Policy lacks the required path or capability | Identify the actor and compare its exact policy |
| Backup works but a normal restore returns 403 | Restore policy lacks update on `sys/storage/raft/snapshot` | Restore ServiceAccount, role, and policy binding |
| Normal restore works but `force: true` returns 403 | Restore policy lacks update on `sys/storage/raft/snapshot-force` | Forced-restore policy and recovery procedure |
| Rolling works but BlueGreen stalls | Peer-management paths were not added | Accepted strategy and upgrade policy |
| Controller has restore or broad upgrade powers | Job policies were merged into the controller | Remove the shortcut and restore separate roles |
| Upgrade fails before changing a Pod | Snapshot-read capability is missing | `sys/storage/raft/snapshot` on the upgrade policy |

Return to [operator authentication](../operator-authentication/) when the failure occurs before a policy decision.
