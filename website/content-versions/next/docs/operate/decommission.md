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
  - internal/service/workload/finalization.go
  - test/integration/workload_finalization_test.go
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

For `Retain`, the operator orphans generated recovery Secrets before waiting for workload cleanup. Admission prevents
garbage collection from deleting these Secrets in an active namespace, including when the parent cluster is deleted
with `--cascade=foreground`. This protects recovery material before the first deletion reconcile.

The finalizer requests foreground StatefulSet deletion and waits for workload cleanup before deleting data.
It can complete an already-deleting terminal Pod after checking its current ownership and stopped-container state.
It does not remove foreign finalizers or force deletion of a running or unknown Pod. A pause does not cancel the
delete request; a restore hold still prevents finalization.

For `DeletePVCs` and `DeleteAll`, the operator deletes generated recovery Secrets after workload cleanup. It reads
Secret metadata and checks the current cluster authority and ownership proof. Each delete uses the Secret UID and
resource version as preconditions. Other owned Secrets, such as TLS Secrets, remain subject to garbage collection.

Deleting the namespace also deletes retained namespaced resources. The recovery Secret admission protection permits
normal namespace teardown once the namespace has a deletion timestamp. Complete the cluster deletion and export any
required recovery material before deleting its namespace.

{{< command label="verify" title="Inspect remaining recovery material" >}}
kubectl -n <namespace> get pvc -l openbao.org/cluster=<name>
kubectl -n <namespace> get secret -l openbao.org/cluster=<name>
kubectl -n <namespace> get jobs -l openbao.org/cluster=<name>
{{< /command >}}

With `Retain`, the Raft PVCs and any generated `<cluster>-unseal-key` and `<cluster>-root-token` Secrets are expected to
remain. Protect them as sensitive recovery material. With a destructive policy, independently confirm the intended
PVCs are gone and decide what to do with external snapshots.

## Handle retained resources after deletion

A recreated `OpenBaoCluster` has a different UID, even when its name is unchanged. Retained PVCs and generated
recovery Secrets keep the old `openbao.org/owner-uid`. The operator rejects automatic adoption by the new cluster.
Do not rewrite that annotation to bypass ownership checks. Use the documented restore workflow for recovery.

If the retained material is no longer required, a platform administrator can delegate cleanup without disabling
admission. The maintenance authorizer checks permission on the original cluster name; the parent CR does not need
to exist. Unlike maintenance on a live cluster, the administrator must mark each retained resource directly.

{{< callout type="danger" title="Retained material is a recovery dependency" >}}
Deleting the static unseal key can make retained data unusable. Deleting a PVC can delete its backing volume. Confirm
that the material is no longer required or that a tested recovery copy exists before granting cleanup authority.
{{< /callout >}}

1. Stop any GitOps reconciliation that can recreate the cluster. Verify that workload Pods are gone. Record each
   intended resource's UID, old owner-UID annotation, and current finalizers. Resolve any active consumers or
   incomplete workload deletion first.
2. Grant the cleanup user the custom `maintenance` verb for the original cluster name and ordinary permissions on
   only the intended resource names. For example:

    ```yaml
    apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata:
      name: retained-openbao-cleanup
      namespace: <namespace>
    rules:
      - apiGroups: [openbao.org]
        resources: [openbaoclusters]
        resourceNames: [<original-cluster-name>]
        verbs: [maintenance]
      - apiGroups: [""]
        resources: [secrets]
        resourceNames: [<original-cluster-name>-unseal-key, <original-cluster-name>-root-token]
        verbs: [get, patch, delete]
      - apiGroups: [""]
        resources: [persistentvolumeclaims]
        resourceNames: [<retained-pvc-name>]
        verbs: [get, patch, delete]
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: RoleBinding
    metadata:
      name: retained-openbao-cleanup
      namespace: <namespace>
    roleRef:
      apiGroup: rbac.authorization.k8s.io
      kind: Role
      name: retained-openbao-cleanup
    subjects:
      - apiGroup: rbac.authorization.k8s.io
        kind: User
        name: <cleanup-user>
    ```

3. As the authorized cleanup user, add `openbao.org/maintenance=true` to one verified retained resource. Keep its
   original owner-UID annotation unchanged:

    ```sh
    kubectl -n <namespace> annotate secret <retained-secret-name> openbao.org/maintenance=true
    kubectl -n <namespace> get secret <retained-secret-name> \
      -o jsonpath='{.metadata.uid}{"\n"}{.metadata.resourceVersion}{"\n"}'
    ```

4. Prepare `delete-options.json` with the verified UID and the resource version read after annotation. Use API-server
   preconditions to reject a replacement or concurrent modification:

    ```json
    {
      "apiVersion": "v1",
      "kind": "DeleteOptions",
      "preconditions": {
        "uid": "<verified-resource-uid>",
        "resourceVersion": "<resource-version-after-annotation>"
      }
    }
    ```

5. Delete the selected Secret with those preconditions:

    ```sh
    kubectl delete --raw '/api/v1/namespaces/<namespace>/secrets/<retained-secret-name>' \
      -f delete-options.json
    ```

   For a PVC, use `persistentvolumeclaim` in the annotation and inspection commands and `persistentvolumeclaims` in
   the API path. If a precondition fails, inspect the resource again and verify that cleanup is still intended. Do not
   remove protection finalizers to bypass an active consumer.

6. Verify the intended deletion. Remove the temporary RoleBinding and Role. If cleanup stops before deletion, remove
   the maintenance annotation while the cleanup authority is still available.

This procedure authorizes cleanup. It does not transfer retained objects to a recreated cluster or repair an orphan
Pod whose original ownership chain is gone.
