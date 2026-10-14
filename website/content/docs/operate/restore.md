---
title: Restore a snapshot
description: Create an immutable OpenBaoRestore request, monitor the restore Job, and use lock override only for disaster recovery.
eyebrow: Operate · Restore
weight: 10
verifiedBy:
  - api/v1alpha1/openbaorestore_types.go
  - api/v1alpha1/openbaocluster_operations_types.go
  - api/v1alpha1/openbaocluster_selfinit_types.go
  - config/policy/openbao-validate-openbaorestore.yaml
  - config/rbac/openbaocluster_restore_role.yaml
  - internal/service/restore/manager_validation.go
  - internal/service/restore/manager_running.go
  - internal/service/restore/post_restore_restart.go
  - internal/adapter/openbao/client_bootstrap.go
  - internal/adapter/config/selfinit_gohcl.go
  - cmd/bao-backup/restore_flow.go
  - internal/service/restore/read_replica_restore.go
  - internal/service/workloadidentity/readiness.go
  - internal/service/restore/manager_test.go
  - internal/service/bootstrap/unseal_validation_transit.go
aliases:
  - /docs/validated-deployments/runbooks/restore-from-s3-compatible-snapshot/
  - /docs/next/validated-deployments/runbooks/restore-from-s3-compatible-snapshot/
---

An `OpenBaoRestore` is an explicit, immutable request to download a snapshot and apply it to a target cluster. It
uses a dedicated Job and identity, owns the cluster operation lock while destructive work runs, and records a terminal
result.

{{< callout type="danger" title="Restore overwrites OpenBao Raft state" >}}
The selected snapshot replaces the target's current logical state, including stored secrets, policies, auth
configuration, and keys represented in that snapshot. Verify the namespace, cluster name, bucket, and object key with
a second operator before applying the request.
{{< /callout >}}

## Before you begin

{{< checklist title="Restore preflight" >}}
- The target `OpenBaoCluster` exists in the same namespace as the restore request.
- The exact snapshot key exists and has been tested in an isolated restore rehearsal.
- The restore Job can reach object storage and the target cluster.
- A restore JWT role or labeled static-token Secret grants update on `sys/storage/raft/snapshot`. Grant update on
  `sys/storage/raft/snapshot-force` only when the identity must support `force: true`.
- The storage identity is bound to the generated `<cluster>-restore-serviceaccount` or supplied explicitly.
- Prevent an upgrade or backup from running concurrently.
{{< /checklist >}}

By default, OpenBao verifies that the snapshot is compatible with the target cluster's Shamir or auto-unseal
configuration. The target must also be initialized and must not have `Upgrading=True`.

For Hardened targets, configure explicit, port-scoped `spec.network.egressRules` on the target cluster and set
`credentialsSecretRef`, workload-identity metadata, or S3 `roleArn` on the restore source.

## Executor limits

The executor downloads the complete snapshot into a disk-backed staging volume before authenticating and submitting it.
It checks the downloaded length and computes a SHA-256 digest. An object key and digest alone do not establish backup
provenance. HTTP redirects, automatic request replay, and restore container retries are disabled.

| Requirement | Value |
| --- | --- |
| Snapshot size | Up to 8 GiB |
| Restore Pod ephemeral storage | 9140 MiB requested and limited; check node capacity and namespace quotas |
| Preparation (scheduling, download, leader discovery) | About 30 minutes |
| Restore Job deadline | About 40 minutes, including image pulls |

The operator requires a terminal Job condition and terminated containers in any remaining executor Pods before
processing the result. Job success or failure counters alone do not establish completion. These observations do not
prove that a process stopped on an unreachable node or that OpenBao finished applying the snapshot.

## Prepare a cross-cluster restore

A snapshot does not replace the target's unseal root. The target cluster must be able to unwrap the restored barrier
keys with a compatible external seal. For Transit unseal, use the same Transit key and equivalent endpoint, trust,
and credential configuration on the source and target.

Keep the source and target on the same OpenBao version for the restore event. The operator does not validate snapshot
format compatibility across versions, so treat a cross-version restore as unqualified until it has a separate
rehearsal and support decision.

Keep traffic on the source until the target is unsealed, has a leader and expected Raft membership, accepts a real
human or workload login, and returns representative restored data. Cut over traffic manually after those checks.

### Preserve lifecycle JWT identities

The first restore authenticates against the target's current JWT configuration. The applied snapshot then replaces
that configuration with the source cluster's roles. A later backup, restore, or upgrade fails if the restored role
does not accept the target Job's ServiceAccount subject.

For a planned recovery target on the same Kubernetes JWT trust domain, add its exact subjects to the source cluster
before the source self-initializes:

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

The operator adds each subject only to its corresponding generated role. Add `operator` subjects only when a recovery
target uses a different controller ServiceAccount. A same-cluster restore does not need additional subjects because
the generated identities do not change.

Self-init is one-shot. Adding these fields to an initialized source does not update its OpenBao roles. Update each
role through an authenticated administration path before taking the recovery snapshot, or create and initialize the
source with the bindings already declared.

The subject allowlist does not extend JWT issuer or signature trust. For a target on another Kubernetes control plane,
the restored `jwt-operator` auth method must also validate that control plane's projected tokens. Qualify that trust
configuration separately before relying on the recovery path.

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
      region: us-east-1
      credentialsSecretRef:
        name: s3-restore-credentials
    key: clusters/security/prod-cluster/last-good.snap
  jwtAuthRole: openbao-operator-restore
{{< /command >}}

The target shape is shared with backups. GCS uses `provider: gcs`, `bucket`, optional `gcs.project`, and a
`credentials.json` Secret. Azure uses `provider: azure`, `bucket`, `azure.storageAccount`, optional
`azure.container`, and an `accountKey` or `connectionString` Secret.

Apply the request once:

{{< command label="apply" title="Start the restore" >}}
kubectl apply -f restore.yaml
{{< /command >}}

Admission requires the caller to have `restore` on the named target `OpenBaoCluster`. Referenced Secrets require
`get`; cloud identity metadata requires `usecloudidentities`; a custom restore image requires
`usecustomexecutables`. The spec cannot be edited after creation. Create a new request to change any restore intent.

## Monitor the lifecycle

{{< command label="verify" title="Watch restore and cluster state together" >}}
kubectl -n <namespace> get openbaorestore <restore-name> -w
kubectl -n <namespace> get openbaorestore <restore-name> -o yaml
kubectl -n <namespace> get jobs -l openbao.org/cluster=<cluster>
kubectl -n <namespace> get openbaocluster <cluster> -o yaml
{{< /command >}}

`status.phase` moves through `Pending`, `Validating`, and `Running`, then ends at `Completed` or `Failed`.
It changes to `Unknown` when the controller cannot prove whether a committed restore Job ran. The operator never
recreates that Job automatically. Inspect the retained Job when it exists, verify the cluster state, and delete the
immutable `OpenBaoRestore` only after you decide that another operation can proceed.
`RestoreConfigurationReady` reports the operator-known auth, storage identity, Secret, and egress prerequisites.

`status.execution` records the stable operation ID, expected Job name and UID, and the `Prepared`, `Committed`,
`Created`, `TerminalObserved`, and `FollowThroughComplete` receipts. The operator retains the Job without a TTL until
the terminal result and post-restore recovery are durable. It then removes the Job deliberately.

When steady read replicas are configured, the operator drains them before creating the restore Job. After the Job
succeeds, the operator keeps the restore lock and restarts every voter Pod through a StatefulSet rollout. The
`OpenBaoCluster` records the restore name, UID, and voter restart completion time in `status.restore`.

After all voter Pods run the restored revision and are Ready, the operator restores the desired read replicas. It waits
for `ReadReplicasReady`, `ReadServingAvailable`, and `RaftMembershipReady` before recording follow-through completion,
marking the restore `Completed`, and releasing the restore lock.

Deleting a restore before execution is committed cancels it. Deleting a committed restore does not terminate its Job.
The finalizer observes the terminal result, completes voter and read-replica recovery, and then removes the Job and the
restore request. Loss of an admission-policy dependency still blocks new restore work, but it does not stop this narrow
committed-execution drain path.

{{< callout type="note" title="Completed is not full service validation" >}}
`Completed` means the restore Job succeeded and every voter completed the required post-restore restart. When read
replicas are configured, it also means the read pool returned to its declared membership and readiness. The operator
does not validate restored application data or authentication semantics. Verify seal state, leader, Raft membership,
application data, and authentication after every restore.
{{< /callout >}}

## Use a force restore

Set `force: true` only when disaster recovery cannot use the normal verified restore:

{{< command label="configure" title="Bypass snapshot seal-consistency verification" >}}
spec:
  force: true
{{< /command >}}

This option uses OpenBao's `sys/storage/raft/snapshot-force` endpoint. It bypasses verification that the snapshot is
compatible with the target cluster's Shamir or auto-unseal configuration. It also skips the controller checks that
require the target to be initialized and not upgrading.

The force endpoint does not make incompatible seal material usable after the restore. Validate the snapshot source and
seal compatibility through another trusted process before you use it.

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
