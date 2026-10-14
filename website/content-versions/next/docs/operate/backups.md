---
title: Back up a cluster
description: Schedule Raft snapshots, choose storage and OpenBao identities, trigger a backup, and verify the result.
eyebrow: Operate · Data protection
weight: 2
verifiedBy:
  - cmd/bao-backup/backup_flow.go
  - api/v1alpha1/openbaocluster_operations_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/service/backup/manager_reconcile.go
  - internal/service/backup/manager_effects.go
  - test/integration/backup_manager_test.go
  - internal/service/backup/manager_trigger.go
  - internal/service/backup/retention_lifecycle.go
  - cmd/bao-backup/retention_flow.go
  - internal/service/workloadidentity/readiness.go
  - internal/service/backup/manager_trigger_test.go
---

The operator creates a transient Job for each due or manually requested backup. The Job authenticates to OpenBao,
streams a Raft snapshot to object storage, and records the result on `status.backup`. Snapshot bytes do not pass
through the controller.

The stored object is a standard OpenBao Raft snapshot. Recovery can use an [operator-managed restore](../restore/)
or a [manual restore](../restore/manual/) into a separately provisioned cluster. An operator installation is not
required for the manual path.

## Before you begin

Prepare two independent identities:

1. An OpenBao identity that can read `sys/storage/raft/snapshot`. Prefer a JWT role bound to the generated
   `<cluster>-backup-serviceaccount`; use `tokenSecretRef` only as a compatibility path.
2. An object-storage identity. Use `credentialsSecretRef`, workload-identity metadata, or S3 `s3.roleArn`.

When self-init OIDC bootstrap is enabled, an empty `jwtAuthRole` resolves to `openbao-operator-backup` and the operator
creates the policy and role during initial bootstrap. Otherwise, configure `jwtAuthRole` or `tokenSecretRef` explicitly.

Hardened clusters also require explicit, port-scoped `spec.network.egressRules` and an explicit storage identity. See
[Configure network policy](../../configure/network/).

## Configure an S3 backup

This is the preferred minimal shape when self-init OIDC bootstrap is enabled:

{{< command label="configure" title="Schedule an S3 snapshot" >}}
spec:
  backup:
    schedule: "0 3 * * *"
    target:
      provider: s3
      endpoint: https://s3.amazonaws.com
      bucket: openbao-backups
      s3:
        region: us-east-1
      pathPrefix: clusters
      credentialsSecretRef:
        name: s3-backup-credentials
    retention:
      maxCount: 14
      maxAge: 336h
{{< /command >}}

`schedule` is a five-field cron expression. The default provider is `s3`, but set it explicitly so intent remains
clear.

The operator persists the first scheduled run in `status.backup.nextScheduledBackup` before it waits for that run.
The deadline survives reconciliation and controller restarts. Check `status.backup.lastBackupTime` and
`status.backup.lastBackupName` to verify that a scheduled backup succeeds.

| Provider | Required target fields | Secret keys |
| --- | --- | --- |
| S3 | `endpoint`, `bucket`; `s3.region` defaults through the compatibility field to `us-east-1` | `accessKeyId`, `secretAccessKey`; optional `sessionToken`, `region`, `caCert` |
| GCS | `bucket`; optional `gcs.project` and endpoint | `credentials.json` |
| Azure | `bucket`, `azure.storageAccount`; optional endpoint | `accountKey` or `connectionString` |

`bucket` is required for every provider. Azure uses it as the container name. The existing `azure.container` field
remains a compatibility alias; a nonempty value takes precedence over `bucket`. Use `bucket` alone in new manifests.

For S3, group `region`, `usePathStyle`, and `roleArn` under `target.s3`. Each configured nested field overrides its
top-level compatibility field. Omitted nested fields use the top-level value, including its existing CRD default.
The nested block has no defaults: `s3: {}` preserves the compatibility values, `s3.usePathStyle: false` overrides
top-level `true`, and `s3.roleArn: ""` disables the top-level role path. An empty `s3.region` uses `us-east-1`.
Existing top-level S3 manifests remain supported. The `s3` block is accepted only for `provider: s3`.

For Azure, `target.endpoint` is the complete Blob service URL. The operator uses it without adding the storage
account name. Omit it to use `https://<storageAccount>.blob.core.windows.net`, including when private DNS resolves
that hostname to a private endpoint. For Azurite, include the account path, for example
`http://azurite.azure.svc.cluster.local:10000/devstoreaccount1`. A credential Secret's `connectionString` supplies
its own endpoint and takes precedence over `target.endpoint`.

For a storage endpoint signed by a private certificate authority, add its PEM certificate bundle under `caCert` in
the referenced storage Secret. S3, GCS, and Azure backup and restore executors add this bundle to the system trust
roots. The bundle is separate from the OpenBao API CA. Keep `insecureSkipVerify` disabled.

The Secret can contain only `caCert` when authentication uses workload identity or the provider credential chain.
Adding storage trust does not select static credentials; retain the required role or workload-identity configuration.

For platform workload identity, set `target.workloadIdentity.serviceAccountAnnotations` and, when required,
`target.workloadIdentity.podLabels`. For S3 web identity, the effective `target.s3.roleArn` or compatibility
`target.roleArn` projects an STS token and configures the
Job explicitly.

{{< callout type="warning" title="Storage identity is separate from unseal identity" >}}
Backup Jobs use their own ServiceAccount. A cloud identity attached to the OpenBao workload is not inherited by the
backup Job. Bind the generated backup ServiceAccount or supply storage credentials deliberately.
{{< /callout >}}

## Use a static OpenBao token only when needed

If JWT auth is unavailable, create a same-namespace Secret whose `token` value can read the Raft snapshot endpoint,
then reference it:

{{< command label="configure" title="Reference a backup token" >}}
spec:
  backup:
    tokenSecretRef:
      name: openbao-backup-token
{{< /command >}}

`jwtAuthRole` takes precedence when both fields are present. Rotate and revoke a static token as a high-sensitivity
credential.

## Delegate configuration safely

The identity that changes the `OpenBaoCluster` needs:

- `get` on each referenced Secret
- `usecloudidentities` on the cluster when it sets an effective S3 role or workload-identity metadata
- `usecustomexecutables` on the cluster when it overrides `spec.backup.image` (`usehelperimages` remains a compatibility alias)

Admission rejects loopback and link-local storage endpoints. Hardened clusters also reject insecure TLS and ambient
storage identity.

## Trigger and verify a backup

The operator does not create a CronJob. Trigger an immediate backup by changing the one-shot annotation value:

{{< command label="apply" title="Request a manual backup" >}}
kubectl -n <namespace> annotate openbaocluster <name> \
  openbao.org/trigger-backup="$(date -u +%Y-%m-%dT%H:%M:%SZ)" --overwrite
{{< /command >}}

The controller clears the annotation after accepting the request. It skips a duplicate request while another backup
Job is active.

{{< command label="verify" title="Inspect backup readiness and outcome" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{range .status.conditions[?(@.type=="BackupConfigurationReady")]}{.status}{" "}{.reason}{"\n"}{.message}{"\n"}{end}'
kubectl -n <namespace> get openbaocluster <name> -o jsonpath='{.status.backup}' | jq
kubectl -n <namespace> get jobs -l openbao.org/cluster=<name>
{{< /command >}}

A successful run advances `lastBackupTime`, `lastBackupName`, `lastBackupSize`, and `lastBackupDuration`. Failure
details are in `lastFailureReason`, `lastFailureMessage`, and `lastFailureTime`. Confirm the object independently in
storage before relying on it.

If the snapshot stream fails during upload, the executor aborts the write, so the failed snapshot creates no object.
After upload, it checks that the stored object size matches the number of bytes streamed and deletes the object if they
differ. That deletion can fail on object-lock or write-only buckets, so check for leftover objects after a failed run.

## Set retention and pre-upgrade snapshots

`maxCount: 0` and an empty `maxAge` mean unlimited retention. The operator applies retention after a successful
upload and never turns a retention error into a failed snapshot. Retention always keeps the newest snapshot and the
snapshot recorded in `status.backup.lastBackupName`, even when they are older than `maxAge`. Snapshot object keys use
the time the backup Job was created, so a backup delayed past its schedule is not treated as old.

Retention only manages scheduled and manual snapshots stored directly under the cluster prefix. It leaves pre-upgrade
and blue-green phase snapshots, nested objects, and any other files in that prefix untouched. Remove those by hand or
with storage lifecycle rules.

Retention runs in a separate Job with the successful backup's writer ServiceAccount and storage credentials.
This supports credential Secrets, S3 web identity, and GCS or Azure workload identity. The storage identity needs
permission to list and delete snapshots in addition to the permissions needed to upload and verify them.

The operator records snapshot success before it reserves retention. It holds the backup operation lock during
retention, which can delay the next backup or upgrade. New restore-test reservations wait for retention; an existing
restore test's snapshot remains protected. The worker has an absolute four-minute deadline, and the Job has a
five-minute active deadline. A delayed start does not extend the worker deadline. There is one Create attempt per
successful source Job; retention failure does not change the snapshot result. The next successful backup can retry.

Check `status.backup.retention.active`, `status.backup.retention.last`, and the `RetentionSucceeded` condition under
`status.backup.retention.conditions`. `Succeeded`, `Failed`, and `Skipped` refer to cleanup, independently of
`status.backup.lastBackupName`. A failed batch can have partial effects; unknown partial deletions are not counted
as successful deletions. The worker uses the successful backup's frozen target and configuration generation.
A configuration change before submission skips that attempt. A submitted worker finishes with its frozen plan.

{{< callout type="warning" title="Uncertain retention execution requires inspection" >}}
A missing or replaced submitted Job, missing Pod evidence, or a container whose termination cannot be observed
keeps the retention reservation active. A deadline or Kubernetes deletion does not prove that execution stopped.
This blocks new backups and restore-test reservations and keeps the source cluster's deletion finalizer.
Inspect executor termination and object-storage activity before an administrator records a failed or skipped last
outcome for the active `sourceJobUID` and clears `active` through the status subresource. Preserve the source Job UID
in the last outcome to prevent replay of that attempt. This disposition accepts any remaining process or provider uncertainty. Update custom backup images to an executor that
supports `EXECUTOR_MODE=retention`; unsupported executors report a separate retention failure.
{{< /callout >}}

Enable `spec.upgrade.preUpgradeSnapshot: true` to require a snapshot before either upgrade strategy. Blue-green also
accepts `spec.upgrade.blueGreen.preUpgradeSnapshot`, but the top-level field keeps one policy across strategies. The
upgrade does not continue if its required snapshot fails.

A backup is complete only after you [restore it into an isolated target](../restore/) and validate the recovered data.
[Schedule restore tests](../restore/tests/) to do this automatically for the latest backup.
