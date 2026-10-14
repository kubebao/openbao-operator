# DR: Storage Providers Backup & Restore

Source: `test/e2e/backup_restore_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `dr-azure-retention` | applies Azure retention in a separate storage workload | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor` |
| `dr-azure-provider-backup-smoke` | executes a manual backup to Azure | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `provider-smoke` |
| `backup-restore-restores-from-azure-backup-using-openbaorestore-b3ea354a` | restores from Azure backup using OpenBaoRestore CR | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow` |
| `dr-gcs-provider-backup-smoke` | executes a manual backup to GCS | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `provider-smoke` |
| `dr-s3-restorable-backup` | creates a restorable S3 backup | active | `lifecycle-job-owner-proof` | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `read-replicas`, `read-replicas-restore` |
| `backup-restore-handles-transient-s3-auth-failure-with-10300347` | handles transient S3 auth failure with backup retry after controller restart | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `failure-injection` |
| `dr-s3-restore-cr` | restores from S3 backup using OpenBaoRestore CR | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `read-replicas`, `read-replicas-restore` |
| `dr-s3-restore-controller-restart` | retains the restore hold after controller restart while running | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `failure-injection` |
| `dr-s3-initial-schedule` | starts the first scheduled S3 backup | active | _none_ | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor` |

## `dr-azure-retention`

Path: `DR: Storage Providers Backup & Restore > Azure Backup & Restore with Azurite > applies Azure retention in a separate storage workload`

State: `active`

Generated fallback ID: `backup-restore-applies-azure-retention-in-a-separate-5bbff6a1`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`

Recorded checkpoints:
- waiting for the first backup and retention to release the operation lock
- retaining one snapshot and creating another verified backup
- checking that the worker retains storage identity without OpenBao authentication


## `dr-azure-provider-backup-smoke`

Path: `DR: Storage Providers Backup & Restore > Azure Backup & Restore with Azurite > executes a manual backup to Azure`

State: `active`

Generated fallback ID: `backup-restore-executes-a-manual-backup-to-azure-e51c2b76`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `provider-smoke`

Recorded checkpoints:
- annotating the cluster to trigger a manual Azure backup
- forcing a reconcile after the manual Azure backup trigger
- waiting for an Azure backup job to be created
- waiting for the Azure backup job to complete successfully
- recording the latest Azure backup key from cluster status


## `backup-restore-restores-from-azure-backup-using-openbaorestore-b3ea354a`

Path: `DR: Storage Providers Backup & Restore > Azure Backup & Restore with Azurite > restores from Azure backup using OpenBaoRestore CR`

State: `active`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`

Recorded checkpoints:
- creating an OpenBaoRestore resource from the Azure backup key


## `dr-gcs-provider-backup-smoke`

Path: `DR: Storage Providers Backup & Restore > GCS Backup with fake-gcs-server > executes a manual backup to GCS`

State: `active`

Generated fallback ID: `backup-restore-executes-a-manual-backup-to-gcs-32e6203f`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `provider-smoke`

Recorded checkpoints:
- annotating the cluster to trigger a manual GCS backup
- forcing a reconcile after the manual GCS backup trigger
- waiting for a GCS backup job to be created
- waiting for the GCS backup job to complete successfully


## `dr-s3-restorable-backup`

Path: `DR: Storage Providers Backup & Restore > S3 Backup & Restore with RustFS > creates a restorable S3 backup`

State: `active`

Generated fallback ID: `backup-restore-creates-a-restorable-s3-backup-44a8d036`

Covers: `lifecycle-job-owner-proof`

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `read-replicas`, `read-replicas-restore`

Recorded checkpoints:
- Writing a secret before backup
- waiting for any earlier scheduled backup to release its operation lock
- waiting for the S3 backup job to complete successfully
- waiting for a new snapshot that includes the pre-backup secret
- recording the latest S3 backup key from cluster status


## `backup-restore-handles-transient-s3-auth-failure-with-10300347`

Path: `DR: Storage Providers Backup & Restore > S3 Backup & Restore with RustFS > handles transient S3 auth failure with backup retry after controller restart`

State: `active`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `failure-injection`

Recorded checkpoints:
- injecting invalid backup credentials
- triggering a manual backup with invalid credentials
- waiting for backup activity
- restarting the controller while failed backup status is being reconciled
- observing failure status with invalid credentials
- restoring valid credentials and retriggering backup
- verifying backup recovery clears stale failure fields
- ensuring backup operation lock is released after recovery


## `dr-s3-restore-cr`

Path: `DR: Storage Providers Backup & Restore > S3 Backup & Restore with RustFS > restores from S3 backup using OpenBaoRestore CR`

State: `active`

Generated fallback ID: `backup-restore-restores-from-s3-backup-using-openbaorestore-cbd34175`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `read-replicas`, `read-replicas-restore`

Recorded checkpoints:
- recording voter Pod identities before restore
- verifying restore configuration is accepted before execution
- verifying the operator did not restart voters on HTTP acceptance
- acknowledging recovery and waiting for managed voter and read-replica restarts
- Verifying secret persists after restore
- Verifying restore metrics are emitted


## `dr-s3-restore-controller-restart`

Path: `DR: Storage Providers Backup & Restore > S3 Backup & Restore with RustFS > retains the restore hold after controller restart while running`

State: `active`

Generated fallback ID: `backup-restore-retains-the-restore-hold-after-controller-0a16537a`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `failure-injection`

Recorded checkpoints:
- switching the idle cluster to BlueGreen so voters use OnDelete
- waiting for restore to enter Running phase
- restarting controller deployment during restore execution
- authorizing managed voter and read-replica restarts


## `dr-s3-initial-schedule`

Path: `DR: Storage Providers Backup & Restore > S3 Backup & Restore with RustFS > starts the first scheduled S3 backup`

State: `active`

Generated fallback ID: `backup-restore-starts-the-first-scheduled-s3-backup-519ec7a4`

Covers: _none_

Labels: `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`

Recorded checkpoints:
- creating a cluster without backup status or a manual trigger
- checking that the first deadline is persisted before any backup attempt
- waiting for a scheduled snapshot without sending a manual trigger
