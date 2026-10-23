---
title: Decommission a cluster
description: Select the deletion policy, delete the cluster, and verify retained or removed data.
eyebrow: Operate · Teardown
weight: 5
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - internal/app/openbaocluster/deletionops/handler.go
  - internal/app/openbaocluster/deletionops/cleanup.go
  - internal/app/openbaocluster/deletionops/retention.go
  - internal/controller/openbaocluster/deletion_integration_test.go
  - internal/service/workload/statefulset_builder.go
---

Deleting an `OpenBaoCluster` removes its operator-managed compute and supporting resources. Choose what happens to
the local data path before you delete the custom resource.

## Choose the deletion policy

| Policy | PVCs | Generated unseal and root-token Secrets | External snapshots |
| --- | --- | --- | --- |
| `Retain` (default) | Retained | Orphaned from the cluster and retained when present | Retained |
| `DeletePVCs` | Operator-owned PVCs are deleted | Deleted with other owned resources | Retained |
| `DeleteAll` | Operator-owned PVCs are deleted | Deleted with other owned resources | Retained in the current implementation |

`DeletePVCs` and `DeleteAll` remove only PVCs that have OpenBao ownership proof. A label match alone is not enough.
PVCs referenced as an existing ACME cache or existing audit-file claim remain outside this cleanup.

{{< callout type="danger" title="PVC deletion is a data-loss action" >}}
Deleting the Raft PVCs removes the local recovery path. Confirm an external snapshot and its restore credentials before
using `DeletePVCs` or `DeleteAll`.
{{< /callout >}}

`DeleteAll` does not currently delete S3, GCS, or Azure objects. Apply the storage system's retention or deletion
process separately.

## Set the policy

Set the policy before issuing the delete:

{{< command label="configure" title="Retain the recovery material" >}}
spec:
  deletionPolicy: Retain
{{< /command >}}

Use `DeletePVCs` only for an intentional destructive teardown:

{{< command label="apply" title="Select PVC deletion" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{
  "spec": {
    "deletionPolicy": "DeletePVCs"
  }
}'
{{< /command >}}

## Delete and verify

{{< command label="apply" title="Delete the cluster" >}}
kubectl -n <namespace> delete openbaocluster <name>
{{< /command >}}

The finalizer applies the policy before Kubernetes garbage collection removes owned resources.

{{< command label="verify" title="Inspect remaining recovery material" >}}
kubectl -n <namespace> get pvc -l openbao.org/cluster=<name>
kubectl -n <namespace> get secret -l openbao.org/cluster=<name>
kubectl -n <namespace> get jobs -l openbao.org/cluster=<name>
{{< /command >}}

With `Retain`, the Raft PVCs and any generated `<cluster>-unseal-key` and `<cluster>-root-token` Secrets are expected to
remain. Protect them as sensitive recovery material. With a destructive policy, independently confirm the intended
PVCs are gone and decide what to do with external snapshots.
