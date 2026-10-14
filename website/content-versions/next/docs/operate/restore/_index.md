---
title: Restore a snapshot
description: Choose a restore method or submit an OpenBaoRestore request for an existing cluster.
eyebrow: Operate · Restore
weight: 10
verifiedBy:
  - api/v1alpha1/openbaorestore_types.go
  - api/v1alpha1/openbaocluster_operations_types.go
  - api/v1alpha1/openbaocluster_selfinit_types.go
  - config/policy/openbao-validate-openbaorestore.yaml
  - config/rbac/openbaocluster_restore_role.yaml
  - internal/service/restore/manager_validation.go
  - internal/service/restore/manager_effects.go
  - internal/adapter/openbao/client_bootstrap.go
  - internal/adapter/config/selfinit_gohcl.go
  - cmd/bao-backup/restore_flow.go
  - cmd/bao-backup/backup_flow.go
  - internal/service/workloadidentity/readiness.go
  - internal/service/restore/manager_test.go
  - internal/service/bootstrap/unseal_validation_transit.go
aliases:
  - /docs/validated-deployments/runbooks/restore-from-s3-compatible-snapshot/
  - /docs/next/validated-deployments/runbooks/restore-from-s3-compatible-snapshot/
---

Operator backups contain standard OpenBao Raft snapshots. You can restore them through `OpenBaoRestore` or use the
OpenBao CLI on a cluster you provision yourself, including one without the operator.

## Choose a restore method

| Task | Page |
| --- | --- |
| Restore into an existing `OpenBaoCluster` | This page |
| Let the operator create the target | [Restore into a new cluster](new-cluster/) |
| Restore with the OpenBao CLI, without `OpenBaoRestore` | [Restore a snapshot manually](manual/) |
| A restore ended `Unknown` | [Recover an uncertain restore](recover-uncertain/) |
| Keep operator and Job authentication working | [Restore authentication](authentication/) |
| Prepare a namespace for new targets | [Prepare a recovery namespace](prepare-namespace/) |
| Test backups on a schedule | [Schedule restore tests](tests/) |

An `OpenBaoRestore` is an immutable request to download a snapshot and apply it. It uses a dedicated Job and
identity, owns the cluster operation lock while destructive work runs, and records the outcome.

{{< callout type="danger" title="Restore overwrites OpenBao Raft state" >}}
The selected snapshot replaces the target's current logical state, including stored secrets, policies, auth
configuration, and keys represented in that snapshot. Verify the namespace, cluster name, bucket, and object key with
a second operator before applying the request.
{{< /callout >}}

{{< callout type="note" title="Guarantees and limits" >}}
- The operator submits a snapshot at most once per request. It never retries or recreates a committed restore.
- The executor stages and size-checks the whole snapshot before submitting it. With `expectedDigest`, it also
  checks the SHA-256 digest. Without a digest, a key-only source can be replaced by another object.
- On an existing cluster, the operator can confirm that OpenBao accepted the snapshot, not that it finished applying.
  Such a restore ends `Unknown` and keeps the cluster held until an administrator acknowledges recovery.
- The hold stops operator management. It does not stop running processes or client traffic.
- After administrator `Resume`, the operator restarts voters and read replicas. The administrator still validates
  restored data and repairs trust when needed.
{{< /callout >}}

## Before you begin

{{< checklist title="Restore preflight" >}}
- The target `OpenBaoCluster` exists in the same namespace as the restore request.
- The exact snapshot key exists and has been tested in an isolated restore rehearsal.
- The restore Job can reach object storage and the target cluster.
- Stop application traffic to all target voter and read-replica endpoints for the restore and recovery window.
- A restore JWT role or labeled static-token Secret grants update on `sys/storage/raft/snapshot`. Grant update on
  `sys/storage/raft/snapshot-force` only when the identity must support `force: true`.
- The storage identity is bound to the generated `<cluster>-restore-serviceaccount` or supplied explicitly.
- Prevent an upgrade or backup from running concurrently.
- No restore from an older operator release is still in flight. Upgrade the operator and executor images together.
{{< /checklist >}}

By default, OpenBao verifies that the snapshot is compatible with the target cluster's Shamir or auto-unseal
configuration. The target must also be initialized and must not have `Upgrading=True`.

For Hardened targets, configure explicit, port-scoped `spec.network.egressRules` on the target cluster and set
`credentialsSecretRef`, workload-identity metadata, or S3 `s3.roleArn` on the restore source.

| Requirement | Value |
| --- | --- |
| Snapshot size | Up to 8 GiB |
| Restore Pod ephemeral storage | About 9 GiB requested and limited; check node capacity and namespace quotas |
| Preparation (scheduling, download, leader discovery) | About 30 minutes |
| Restore Job deadline | About 40 minutes, including image pulls |

## Prepare a cross-cluster restore

The target must unwrap the restored barrier keys with a compatible seal: for Transit, the same key and equivalent
endpoint, trust, and credentials. Keep source and target on the same OpenBao version; cross-version restores are
unqualified. Prepare JWT trust before taking the snapshot, as described in
[Restore authentication](authentication/).

For a planned cutover, keep traffic on the source until the target is unsealed, has its expected leader and Raft
membership, accepts a real login, and returns representative data. Cut over traffic manually.

## Choose restore authentication

Prefer JWT auth. When self-init OIDC bootstrap is enabled, an empty restore role resolves to
`openbao-operator-restore`, which is bound to the generated restore ServiceAccount during initial bootstrap.

If you use a static token, the same-namespace Secret must have both identity labels:

{{< command label="configure" title="Create a scoped restore-token Secret" >}}
apiVersion: v1
kind: Secret
metadata:
  name: restore-token
  namespace: <namespace>
  labels:
    openbao.org/cluster: <cluster>
    openbao.org/credential-purpose: restore-token
stringData:
  token: <openbao-token>
{{< /command >}}

Reference it as `spec.tokenSecretRef.name`. A configured `jwtAuthRole` takes precedence.

## Create the restore request

{{< command label="configure" title="Restore an S3 snapshot" >}}
apiVersion: openbao.org/v1alpha1
kind: OpenBaoRestore
metadata:
  name: prod-restore-001
  namespace: security
spec:
  cluster: prod-cluster
  source:
    target:
      provider: s3
      endpoint: https://s3.amazonaws.com
      bucket: openbao-backups
      s3:
        region: us-east-1
      credentialsSecretRef:
        name: s3-restore-credentials
    key: clusters/security/prod-cluster/last-good.snap
  jwtAuthRole: openbao-operator-restore
{{< /command >}}

The target shape is shared with backups. GCS uses `provider: gcs`, `bucket`, optional `gcs.project`, and a
`credentials.json` Secret. Azure uses `provider: azure`, `bucket` as its container, `azure.storageAccount`, and an
`accountKey` or `connectionString` Secret. The existing `azure.container` compatibility alias takes precedence
when nonempty; `bucket` remains required.

S3 uses `source.target.s3` for `region`, `usePathStyle`, and `roleArn`. Configured nested fields override the
top-level compatibility fields independently, including `false` addressing and an empty role override.
Omitted nested fields retain the top-level values and defaults. See [storage target configuration](../backups/).

Apply the request once:

{{< command label="apply" title="Start the restore" >}}
kubectl apply -f restore.yaml
{{< /command >}}

Admission requires the caller to have `restore` on the named target `OpenBaoCluster`. Referenced Secrets require
`get`; cloud identity metadata requires `usecloudidentities`; a custom restore image requires
`usecustomexecutables` or `usehelperimages`. The spec cannot be edited after creation; create a new request to change
restore intent.

## Monitor the lifecycle

{{< command label="verify" title="Watch restore and cluster state together" >}}
kubectl -n <namespace> get openbaorestore <restore-name> -w
kubectl -n <namespace> get openbaorestore <restore-name> -o yaml
kubectl -n <namespace> get jobs -l openbao.org/cluster=<cluster>
kubectl -n <namespace> get openbaocluster <cluster> -o yaml
{{< /command >}}

`status.phase` moves through `Pending`, `Validating`, and `Running`, then ends at one of:

| Phase | Meaning | Next step |
| --- | --- | --- |
| `Failed` | Validation or the executor failed before submitting the snapshot. The hold is released. | Fix the cause and create a new request. |
| `Unknown` | The snapshot was submitted, or the outcome cannot be determined. The cluster stays held until recovery is released. | [Recover an uncertain restore](recover-uncertain/). |

`RestoreConfigurationReady` reports the auth, storage, Secret, and egress prerequisites the operator can check.
`status.execution` records the operation ID and Job identity, and `status.submissionClaim` records the executor that
submitted. The operator retains the Job for inspection.

Deleting a request before its Job is committed cancels it. After commitment, admission rejects deletion until the
request has failed before submission, completed, finished disposable cleanup, or been acknowledged. Do not remove
finalizers or the `openbao.org/restore-hold` annotation to bypass recovery.

## Use a force restore

Set `force: true` only when disaster recovery cannot use the normal verified restore. It uses OpenBao's
`sys/storage/raft/snapshot-force` endpoint, which skips the snapshot's seal-consistency check, and skips the
controller checks that the target is initialized and not upgrading. It does not make incompatible seal material
usable; confirm seal compatibility through another trusted process first.

## Override a stuck operation lock

Use this only when disaster recovery cannot wait for an upgrade or backup lock to clear normally:

{{< command label="configure" title="Force restore ownership of the operation lock" >}}
spec:
  force: true
  overrideOperationLock: true
{{< /command >}}

`overrideOperationLock` requires `force: true`. The controller can replace a non-restore operation lock and records an
`OperationLockOverride` condition, Warning Event, and audit signal. It does not acknowledge
`status.breakGlass`; that is a separate rollback-recovery decision.

After a forced restore, verify `bao status`, `bao operator raft list-peers`, declared replica readiness, client login,
and representative application data before returning traffic. If the target remains sealed or leaderless, continue
with the corresponding recovery page.
