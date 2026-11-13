---
title: Restore a snapshot manually
description: Apply a standard Raft snapshot to a separately provisioned cluster without an OpenBaoRestore request.
eyebrow: Operate · Restore
weight: 3
verifiedBy:
  - cmd/bao-backup/backup_flow.go
  - api/v1alpha1/openbaocluster_types.go
  - internal/controller/openbaocluster/split_reconcilers.go
  - config/policy/openbao-lock-managed-resource-mutations.yaml
---

Operator backups contain standard Raft snapshots, so you can restore one with the OpenBao CLI. This needs no
`OpenBaoRestore`, no source Kubernetes API, and no operator at the destination. Use a fresh, isolated single-voter
cluster on the same OpenBao version, provisioned and initialized through your normal procedure, inside or outside
Kubernetes.

{{< callout type="note" title="The administrator owns the restore" >}}
A manual restore has no restore hold, submission record, outcome status, managed restart, `Resume`, or automatic
cleanup. You control submission, recovery, validation, and return to service. For operator-managed execution,
[choose another restore method](../).
{{< /callout >}}

## Before you begin

- Prepare a new Raft data volume and one initialized, unsealed voter, with application traffic disconnected.
- Recreate server configuration, TLS files, plugins, and external credentials separately. A snapshot contains only
  Raft storage.
- Provide the original unseal material: the original auto-unseal key with access to its provider, the original static
  key bytes, or the source Shamir shares. A target-generated key cannot decrypt the snapshot.
- Keep an administrator login that works in the restored state, because the snapshot replaces the target's auth
  configuration. For cross-cluster recovery it must work without the source Kubernetes API.
- Obtain the snapshot object, its source version, and a trusted size and SHA-256 digest. Keep the original object until
  recovery is validated. If the source must be unavailable, provide independent access to storage and the unseal
  provider.
- Isolate the target from production clients, peers, and integrations; restored configuration can contain live
  credentials and endpoints. On Kubernetes, see the
  [network boundary requirements](../prepare-namespace/#prepare-the-network-boundary). The namespace approval label
  does not apply to this path, but the boundary still does.

## Prepare an operator-managed target

Skip this section if the target has no operator. If you provision it as an `OpenBaoCluster`:

1. Wait for initialization, unseal, and healthy single-voter leadership. Do not configure backups, restore tests, or
   read replicas on it yet.
2. Suspend GitOps changes for the target, or declare the maintenance and pause settings below in its desired state.
3. Enable [maintenance mode](../../maintenance/#authorize-direct-maintenance-only-when-required) and wait for the
   maintenance annotation on its Pod, so an authorized administrator can restart it later.
4. [Pause reconciliation](../../maintenance/#pause-reconciliation-for-a-bounded-repair) with `spec.paused: true`.
5. Confirm there is no active backup, upgrade, restore request, or executor Job; pausing does not stop them. For an
   existing restore hold, follow [Recover an uncertain restore](../recover-uncertain/) first.

Keep the target paused until verification is complete, and prevent new restore requests; a pause is not an
access-control boundary. Do not remove finalizers or edit restore status to bypass an active operation.

## Download and verify the snapshot

Download the exact object with your storage provider's authenticated client, selecting the recorded object version
when the provider supports it. Save it unmodified as `snapshot.snap` on a protected filesystem, then compare its size
and digest with your trusted backup record:

```bash
wc -c < snapshot.snap
sha256sum snapshot.snap   # macOS: shasum -a 256 snapshot.snap
```

A digest calculated only after download proves nothing without a trusted value to compare against.

## Apply the snapshot

1. Point the matching-version `bao` CLI directly at the recovery voter over verified TLS: set `BAO_ADDR` to that voter
   and `BAO_CACERT` to its CA file. Avoid shared addresses or load balancers that can select another server.
2. Log in with the target's current administrator identity. It needs `update` on `sys/storage/raft/snapshot`, or on
   `sys/storage/raft/snapshot-force` for `-force`. Keep tokens out of shell history.
3. Check `bao status` and `bao operator raft list-peers`: an unsealed leader and exactly the intended voter.
4. Run one of the commands below, once. Do not wrap it in a retry loop.

{{< callout type="danger" title="Restore replaces the target's data and authentication" >}}
The snapshot replaces the target's Raft state. Confirm the target and the original unseal material before submitting.
{{< /callout >}}

When the target's seal state matches the snapshot:

```bash
BAO_MAX_RETRIES=0 BAO_DISABLE_REDIRECTS=true bao operator raft snapshot restore snapshot.snap
```

For an independently initialized target, use `-force` **instead**, after confirming the original unseal material can
decrypt the snapshot. See [force restore](../#use-a-force-restore) for what it skips.

```bash
BAO_MAX_RETRIES=0 BAO_DISABLE_REDIRECTS=true bao operator raft snapshot restore -force snapshot.snap
```

These settings disable client retries and redirects, but there is no persisted submission guard. After a timeout or
error, inspect server logs and state before deciding whether another submission is safe. Success means the request
was accepted, not that recovery is complete. See the upstream
[snapshot restore command](https://openbao.org/docs/commands/operator/raft/#snapshot-restore) and
[CLI settings](https://openbao.org/docs/commands/#environment-variables).

## Verify and return the target to service

1. Watch OpenBao logs and health until application finishes. If the target is sealed, repair unseal access; do not
   initialize it again or replace the original key material.
2. Log in with an administrator identity from the restored state and check representative data and auth methods. For a
   managed target, repair [controller and lifecycle JWT trust](../authentication/) before resuming the operator.
3. Restart the voter through your process controls and verify it unseals and elects a leader. For a paused
   `OpenBaoCluster`, use the authorized Pod maintenance path, preserve its PVC, and wait for the StatefulSet to
   recreate the Pod. Deleting a Pod on an unreachable node does not fence the old process.
4. Repeat the health, membership, login, and data checks, then verify a representative application login.
5. For a managed target, disable maintenance and clear `spec.paused` in the desired state, and resume GitOps. Wait for
   `Available=True` and the expected ready replicas before adding peers or read replicas.
6. Restore the backup schedule and network access, then switch application traffic to the verified target.

For a rehearsal, [decommission the target](../../decommission/) instead, confirm its data volumes are removed as
intended, and delete local snapshot copies.
