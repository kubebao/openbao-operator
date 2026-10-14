---
title: Schedule restore tests
description: Restore the latest backup into a disposable target on a schedule and report whether it applied.
eyebrow: Operate · Restore
weight: 5
verifiedBy:
  - api/v1alpha1/restore_test_types.go
  - internal/service/backup/restore_testing.go
  - internal/service/backup/restore_test_child.go
  - internal/service/backup/restore_test_result.go
  - internal/service/backup/snapshot_summary.go
  - internal/service/restore/target.go
  - internal/service/restore/target_cleanup.go
  - test/e2e/restore_minimal_test.go
---

`spec.backup.restoreTest` restores the latest successful backup into a disposable
[new cluster](../new-cluster/) on a schedule or after a number of backups. The result is recorded on the source
cluster and exported as metrics.

{{< callout type="note" title="Guarantees and limits" >}}
- A test passes when the snapshot digest recorded by the backup matches before submission, and the target then
  reports the source cluster ID, version, and healthy leadership.
- It does not test logins or read stored secrets.
- One test runs at a time. Status keeps the last result only, not a history.
- The destination namespace is in the source's Kubernetes cluster. For another Kubernetes cluster, create a
  [restore request there](../new-cluster/#restore-in-another-kubernetes-cluster).
- Operator retention protects the snapshot under test. Bucket lifecycle rules and external retention tools do not
  know about it.
{{< /callout >}}

## Before you begin

1. Prepare and approve a destination namespace. See [Prepare a recovery namespace](../prepare-namespace/).
2. Prepare destination storage credentials and access to the original unseal key. The configured unseal provider must
   decrypt the source snapshot. Storage connection settings come from the source backup configuration; `credentialsSecretRef` selects
   the destination credentials. Backup writer roles and workload identity metadata are not inherited.
   Select destination `credentialsSecretRef` or an existing reader `serviceAccountName`, even when the backup uses ambient credentials.
3. Give the person configuring the test permission in the destination namespace to create restores and clusters,
   perform restores, `get` the referenced Secrets, and `use` the StorageClass and any ServiceAccount. Custom images
   and workload identities need their usual delegation permissions there.

For cloud workload identity, create a dedicated storage-reader ServiceAccount in the destination namespace and
configure its federation before enabling the test. Select it with `serviceAccountName`. The account name stays the
same across generated targets. The operator reads this account but does not create, modify, adopt, or delete it.
Configure `roleArn` for an S3 reader role or `workloadIdentity` for reader metadata. Any requested ServiceAccount
annotations must already exist on the account; `workloadIdentity.podLabels` applies to the executor Pod.

Grant the reader only the storage permissions needed to download the snapshot. Keep upload and retention deletion
permissions on the backup writer. Grant the configuring user `use` on the selected destination ServiceAccount and
`usecloudidentities` in the destination when configuring cloud identity metadata.

Each target has its own request-specific Role and RoleBinding. Cleanup removes those grants and leaves the reader
ServiceAccount in place. Use a separate reader account for each trust domain; runs using the same account share its
Kubernetes and cloud principal. Submission claims still require a Pod-bound token and the executor's original Job.

## Configure a trigger

Add `restoreTest` beneath `spec.backup`. Use either `everySuccessfulBackups` or a five-field UTC cron `schedule`.

For any KMS seal plugin distributed as an OCI image, set
`spec.backup.restoreTest.clusterTemplate.configuration.plugin.autoDownload: true`. The operator copies these settings
to the generated restore request and target. The template does not inherit plugin settings from the source cluster.
See [Configure target plugin downloads](../new-cluster/#create-the-request) for registry and preinstalled-binary requirements.

```yaml
restoreTest:
  everySuccessfulBackups: 7
  # Alternatively: schedule: "0 4 * * 0"
  namespace: recovery-tests
  credentialsSecretRef:
    name: recovery-storage
  # For an existing reader account, including cloud workload identity:
  # serviceAccountName: snapshot-reader
  # roleArn: arn:aws:iam::123456789012:role/snapshot-reader
  cleanupAfterSeconds: 300
  clusterTemplate:
    version: "2.7.1"
    storage:
      size: 10Gi
    tls:
      enabled: true
      mode: OperatorManaged
      rotationPeriod: 720h
    unseal:
      type: transit
      credentialsSecretRef:
        name: recovery-transit
      transit:
        address: https://seal.example.com:8200
        mountPath: transit
        keyName: production-unseal
        tlsCACert: /etc/bao/seal-creds/ca.crt
```

Targets get generated names and require `OperatorManaged` TLS. `cleanupAfterSeconds` keeps a target for inspection
after application is confirmed, up to 24 hours. The default is immediate cleanup. Failed targets are always cleaned up.

Removing `restoreTest` stops new runs and removes the metrics. A running test still finishes cleanup, and the last
result stays in status.

## Read the result

Read `status.backup.restoreTest` on the source cluster:

- `last.outcome` is `Passed` or `Failed`. `last.reason` and `last.message` retain a bounded diagnostic after cleanup.
- `last.namespace`, `last.key`, and `last.digest` identify the destination and pinned snapshot. The digest is present
  when the test request was observed or its creation was definitively rejected.
- `lastSuccessTime` keeps the latest pass across later failures.
- `active` names the running test's `OpenBaoRestore` and the snapshot key.
- The `Passed` condition explains the current state.

| Metric | Meaning |
| --- | --- |
| `openbao_restore_test_success{namespace,name}` | 1 if the latest test passed, 0 if it failed |
| `openbao_restore_test_last_success_timestamp_seconds` | Time of the latest pass, or 0 |

Alert on the age of the last-success timestamp. A past pass does not show that tests still run.

| `Passed` reason | Meaning | Action |
| --- | --- | --- |
| `Running` | A test is in progress. | None. |
| `NoCompatibleSnapshot` | No backup yet records a source identity matching the template version. Backups from older executors lack it. | Wait for a new backup, or align `clusterTemplate.version`. |
| `InvalidSchedule` | The cron expression cannot be parsed. | Fix `schedule`. |
| `StorageIdentityRequired` | The test has no destination reader configuration. | Configure destination `credentialsSecretRef` or an existing reader `serviceAccountName`. Cloud metadata requires `serviceAccountName`. |
| `DestinationRejected` | Admission rejected the test request, for example a missing namespace approval or permission. A pre-check rejection consumes no run; a rejection at creation records `Failed`. | Fix the namespace or permissions. The next due run proceeds. |
| `CleanupBlocked` | The test target's cleanup failed. No further test starts. | Resolve it as in [Resolve blocked cleanup](../new-cluster/#resolve-blocked-cleanup). |
| `RequestMissing` | The operator could not confirm that the test request was created, for example after an API timeout. It never retries, so no further test starts and source deletion waits. | Inspect the destination, make sure no older controller is still running, then acknowledge the exact run as described below. |
| `RequestReplaced` | An object the operator did not create holds the test request's name. | Inspect the replacement and its owner, then acknowledge the exact run as described below. |

If a test request is deleted or replaced after the operator recorded it, the operator records `Failed`, ends the run,
and leaves the replacement alone.

## Release an uncertain run

For `RequestMissing` or `RequestReplaced`, inspect the destination and stop any older controller that could complete
a delayed request creation. Take responsibility for remaining destination resources. Then acknowledge the exact run
on the source cluster; do not edit its status subresource.

```bash
run=$(kubectl -n <source-namespace> get openbaocluster <source-cluster> -o jsonpath='{.status.backup.restoreTest.active.name}')
kubectl -n <source-namespace> annotate openbaocluster <source-cluster> \
  "openbao.org/restore-test-acknowledge=${run}/Release" --overwrite
```

This requires patch access and the `restore` verb on the source cluster, plus admission permissions for its configured
resources. Check that `active` is empty and `last.reason` is `AdministratorReleased`. The acknowledgement cannot
release a recorded request's cleanup or delete a replacement. It also works with testing disabled and during source
deletion. A consumed acknowledgement cannot release a later run; remove it when convenient.

An enabled schedule can start a later due run after release. Preserve external logs if you need detailed provider or
executor errors: the operator keeps only one sanitized result and deletes the test request and target.

To run a single test from a separate recovery cluster, create a disposable `OpenBaoRestore` with the source identity,
version, and digest as in [Restore into a new cluster](../new-cluster/).
