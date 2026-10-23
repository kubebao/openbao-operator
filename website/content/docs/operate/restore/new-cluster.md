---
title: Restore into a new cluster
description: Let the operator create a single-voter target, restore a snapshot into it, and retain or delete the target.
eyebrow: Operate · Restore
weight: 2
verifiedBy:
  - api/v1alpha1/openbaorestore_target_types.go
  - api/v1alpha1/openbaorestore_types.go
  - internal/service/restore/target.go
  - internal/service/restore/target_cleanup.go
  - internal/service/restore/target_health.go
  - internal/service/workload/restore_handoff.go
  - config/policy/openbao-protect-restore-execution.yaml
  - test/e2e/restore_cross_cluster_test.go
---

Set `spec.clusterTemplate` on an `OpenBaoRestore` to have the operator create a new target cluster and restore into
it. Because the target starts empty, the operator can confirm that the snapshot applied.

To provision the target yourself, use a [restore request for an existing cluster](../) or follow
[Restore a snapshot manually](../manual/).

{{< callout type="note" title="Guarantees and limits" >}}
- The operator never adopts an existing cluster or data PVC, and never retries an uncertain target creation.
- Application is confirmed when the target reports the expected source cluster ID and version with healthy
  leadership. The ID and version are your assertion; supply `expectedDigest` to pin the exact snapshot bytes.
- Deleting a disposable target removes Kubernetes objects. It does not fence processes on unreachable nodes.
- The operator does not create network isolation. See [Prepare a recovery namespace](../prepare-namespace/).
{{< /callout >}}

## Before you begin

- The namespace is enrolled with the operator and approved for restore targets. See
  [Prepare a recovery namespace](../prepare-namespace/).
- The required unseal, storage, and TLS credentials are available in that namespace.
- The original unseal key material decrypts the source snapshot. `force: true` bypasses OpenBao's seal-consistency check; it does not
  make an incompatible key usable.
- The profile is one OpenBao **2.7.0** or **2.7.1** voter, matching the snapshot source version, with `OperatorManaged` or `External` TLS. `unseal` uses the same provider
  configuration as `OpenBaoCluster`. Supply required KMS plugins, credentials, workload identity, and network access,
  and rehearse your provider before relying on it.
- You know the source cluster's native ID, version, and ideally digest and size. The operator records them for each
  successful backup in `status.backup.latestSnapshot` on the source cluster; copy them before an outage.
- You have `create` on `openbaoclusters`, `get` on the referenced unseal and image pull Secrets, and `use` on the
  StorageClass and any ServiceAccount in the destination namespace.

Admission requires these fields whenever `spec.clusterTemplate` is set:

- `targetLifecycle` and `force: true`.
- `source.expectedClusterID`, and `source.expectedVersion` equal to `clusterTemplate.version`.
- No `jwtAuthRole`, `tokenSecretRef`, or `overrideOperationLock`; the operator bootstraps the target itself.

## Restore in another Kubernetes cluster

Create the `OpenBaoRestore` in the recovery Kubernetes cluster. Its operator creates and manages the target locally.
The source Kubernetes API does not need to be available after you have the snapshot and its metadata.

1. Install the operator in the recovery cluster and prepare an approved recovery namespace.
2. Provide access to the snapshot object and the original unseal key independently of the source cluster.
   Record the snapshot key, native cluster ID, version, digest, and size before an outage.
3. Apply the request below through the recovery cluster's kubeconfig. Use destination-local Secret references and
   endpoint addresses that the recovery workloads can reach.
4. For `Disposable`, wait for application confirmation and target cleanup. It does not require restored JWT trust
   repair.
5. For `Retain`, inspect the restored data through an administrator login that works without the source Kubernetes API.
   Repair [controller and lifecycle JWT trust](../authentication/), then acknowledge `Resume`.
   Wait for the managed restart and check the retained target before admitting application traffic.

Scheduled `spec.backup.restoreTest` runs within one Kubernetes cluster. A test in another Kubernetes cluster starts
with an administrator-created `OpenBaoRestore`; the operator does not discover remote backups or schedule remote runs.

## Create the request

For KMS plugins distributed as OCI images, set `spec.clusterTemplate.configuration.plugin.autoDownload: true`.
This applies to AWS, Azure, GCP, OCI, PKCS#11, and custom KMS plugins. Declaring `plugins` alone does not download
their binaries: [OpenBao defaults automatic downloads to false](https://openbao.org/docs/configuration/plugins/#plugin_auto_download).
The target needs registry access and a compatible seal key. Images with preinstalled plugin binaries can use
`command` declarations and leave automatic downloads disabled.

Add this fragment beside `clusterTemplate.plugins` when the target downloads its plugins:

```yaml
spec:
  clusterTemplate:
    configuration:
      plugin:
        autoDownload: true
```

The plugin settings use the same fields as `OpenBaoCluster.spec.configuration.plugin`. Other server configuration
fields are not exposed by the restore template.

### Select destination Service IP families

Set `spec.clusterTemplate.service` to create an optional ClusterIP Service for the target. For example, this fragment
requires both Service IP families with IPv6 first:

```yaml
spec:
  clusterTemplate:
    service:
      ipFamilyPolicy: RequireDualStack
      ipFamilies: [IPv6, IPv4]
```

Choose families supported by the recovery Kubernetes cluster. The operator does not inherit the source cluster's
Service settings or restore its allocated Service IPs. If you omit `service`, the target has only its internal headless
Service, which publishes both families when the cluster supports them. An empty `service: {}` creates a client Service
with Kubernetes defaults. These fields do not change Pod IP allocation.

Select the primary family before target creation. Kubernetes does not permit changing an existing Service's primary
family. See [IPv6 and dual-stack networks](../../../configure/network/#use-ipv6-and-dual-stack-networks).

### Transit example

For a Transit target, apply this request after replacing the source identity and storage settings:

```yaml
apiVersion: openbao.org/v1alpha1
kind: OpenBaoRestore
metadata:
  name: recovery-20261005
  namespace: recovery
spec:
  cluster: recovery-20261005
  targetLifecycle: Retain
  force: true
  source:
    target:
      provider: s3
      endpoint: https://storage.example.com
      bucket: snapshots
      credentialsSecretRef:
        name: recovery-storage
    key: clusters/production/snapshot.snap
    expectedClusterID: REPLACE_WITH_SOURCE_NATIVE_CLUSTER_ID
    expectedVersion: "2.7.1"
    # Recommended: expectedDigest: sha256:<64 lowercase hexadecimal characters>
    # Optional: expectedSize: <snapshot bytes>
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

The operator creates the cluster and waits up to about 30 minutes for it to bootstrap. It then runs the normal
restore executor. Within about 10 minutes of submission, the target must report the expected identity, which sets
`status.target.appliedAt`.

Before `Resume`, the target has no Kubernetes API token, service registration, or peer discovery. The operator installs
no NetworkPolicies or published endpoints for it.

During `Resume`, the operator prepares the normal configuration and Kubernetes API token mount under the restore hold.
It keeps automatic rollouts stopped and replaces the target Pod through the managed restart. It releases the hold only
after the replacement uses the prepared template and passes readiness, health, and controller authentication checks.
Normal reconciliation then resumes without a second Pod rollout.

## Choose a lifecycle

| `targetLifecycle` | Outcome |
| --- | --- |
| `Retain` | Keeps the cluster and storage. Inspect it, repair controller JWT trust if needed, then acknowledge as in [Recover an uncertain restore](../recover-uncertain/). `Resume` on a confirmed target ends `Completed`. `Abandon` keeps the phase `Unknown` and the cluster paused. A failure before submission leaves the target paused for you. |
| `Disposable` | Deletes the restore Job, the target, and its data PVC once the restore finishes or fails. Set `cleanupAfterSeconds` (up to 86400) to keep a confirmed target for inspection first. `Resume` is not supported. `status.target.cleanup` reports `Pending`, `Complete`, or `Failed`. |

## Resolve blocked cleanup

Cleanup ends `Failed` when a recorded cluster or PVC was replaced, or a PVC lacks proof that the target created it.
The operator still deletes the cluster it created; it leaves the refused PVC for inspection.

1. Compare the live objects with the UIDs in `status.target`.
2. Delete or retain those resources yourself.
3. Annotate the request with `openbao.org/restore-acknowledge=<restore-UID>/Abandon`.

The request ends `Failed`, and the operator permits deleting it. Replacement resources stay untouched, and cleanup
stays `Failed`.
