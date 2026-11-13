---
title: Restore authentication
description: Keep operator and lifecycle JWT trust working after a snapshot replaces the target's auth configuration.
eyebrow: Operate · Restore
weight: 6
verifiedBy:
  - api/v1alpha1/openbaocluster_selfinit_types.go
  - internal/adapter/config/selfinit_gohcl.go
  - internal/service/restore/recovery_restart.go
---

A snapshot replaces the target's auth configuration with the source's JWT roles. The first restore authenticates
against the target's own configuration. Afterwards, the controller and the backup, restore, and upgrade Jobs must be
accepted by the restored roles. The operator does not change authentication mode or repair JWT roles after a restore;
during `Resume` it reports `OperatorAccessUnavailable` and keeps the hold while controller access fails.

{{< callout type="note" title="Guarantees and limits" >}}
- Adding subjects extends which ServiceAccounts a role accepts. It does not extend JWT issuer or signature trust.
- Deleting and recreating an `OpenBaoCluster` changes its UID and controller audience, even with the same name.
  Preserve the CR where possible, and treat recreation or an older snapshot as an authentication recovery.
{{< /callout >}}

## Prepare the source before taking snapshots

For a recovery target in the same Kubernetes JWT trust domain, add its exact subjects to the source before the
source self-initializes:

{{< command label="configure" title="Authorize one recovery target without combining role privileges" >}}
selfInit:
  enabled: true
  oidc:
    enabled: true
    additionalSubjects:
      backup:
        - system:serviceaccount:recovery:prod-recovery-backup-serviceaccount
      restore:
        - system:serviceaccount:recovery:prod-recovery-restore-serviceaccount
      upgrade:
        - system:serviceaccount:recovery:prod-recovery-upgrade-serviceaccount
{{< /command >}}

The operator adds each subject only to its own generated role. Add `operator` subjects only when the recovery target
uses a different controller ServiceAccount. Restoring into the same `OpenBaoCluster` needs no extra subjects.

If an `OpenBaoRestore` selects an existing executor account with `spec.serviceAccountName`, authorize that account's
exact subject in the restore role. A fresh target adds the selected subject to its own bootstrap restore role.
For an existing target, prepare the role through its current administration path before submitting the request.
The snapshot can replace that role, so any later restore must also be accepted by the restored configuration.

Self-init runs once. Adding these fields to an initialized source does not update its roles; update them through an
authenticated administration path before taking the recovery snapshot.

For a Target-mode cluster, also prepare controller trust for the destination UID in the source, or keep an
administrator login that works in the restored state. Preauthorizing source and destination audiences places both in
one recovery authentication domain; do not add unrelated tenant audiences. See
[controller JWT migration](../../controller-jwt-migration/) for the role update and verification sequence.

## Repair trust after restore

For a destination on another Kubernetes control plane, or when the source was not prepared, use an administrator
session in the restored state to update:

| Configuration | Required destination value |
| --- | --- |
| `auth/jwt-operator/config` | Destination `bound_issuer` and signing-key validation. Import the destination public keys as `jwt_validation_pubkeys`, or configure a trusted, reachable JWKS endpoint. Remove conflicting validation methods. |
| Controller role | Destination controller ServiceAccount subject and the audience selected by the target's `controllerJWTMode`. Target mode uses the new cluster CR's UID. |
| Backup, restore, and upgrade roles | Corresponding destination ServiceAccount subjects. Preserve each role's own audiences, policies, token settings, and other restrictions. |

Read and update the existing roles; do not replace customized roles with bootstrap defaults. If a role uses both
`bound_subject` and `bound_claims.sub`, both must match the destination identity.

A restricted restore target has no Kubernetes API token, so it cannot read a JWKS endpoint that requires
authentication. Obtain the public signing keys through an authenticated administrator connection instead. With static
keys, update the restored configuration when the destination rotates its signing keys.

Before `Resume`, verify destination controller login and that unrelated audiences are rejected.
