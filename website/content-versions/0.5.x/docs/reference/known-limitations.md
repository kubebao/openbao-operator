---
title: Known limitations
description: Current unsupported paths, incomplete contracts, and operational boundaries in the pre-GA release line.
eyebrow: Reference
weight: 5
verifiedBy:
  - api/v1alpha1
  - config/crd/bases
  - internal/app/openbaocluster/deletionops
  - internal/controller/openbaocluster/status_helpers.go
  - internal/service/upgrade
  - internal/service/workload/statefulset_builder_containers.go
---

Review these limits before you design an upgrade, recovery, storage, or lifecycle workflow.

| Area | Current limit | Required response |
| --- | --- | --- |
| CRD versioning | Every CRD serves and stores only `openbao.org/v1alpha1`; there is no conversion webhook | Review every minor release for API migrations |
| Cluster adoption | The operator does not import an arbitrary unmanaged OpenBao cluster | Create an operator-managed cluster and use backup and restore to move data |
| Operator and workload downgrade | Routine downgrades are unsupported; OpenBao version downgrades are blocked | Prefer a forward fix or follow a rehearsed recovery plan |
| External backup deletion | `DeleteAll` removes operator-owned PVCs but does not delete snapshots from object storage | Delete external backups explicitly during decommissioning |
| etcd encryption | The operator cannot prove that the Kubernetes API server encrypts Secret data at rest | Verify etcd encryption with the cluster platform owner |
| Helm CRD lifecycle | Helm does not upgrade or delete installed CRDs automatically | Apply the target release's `crds.yaml` before the chart upgrade |
| Audit file storage | The audit PVC is a collector handoff and replay buffer, not an archive or collector | Ship records to retention-controlled storage and manage rotation there |
| OpenBao 2.6 BlueGreen transition | Pre-2.6 and 2.6-or-newer peers cannot use the mixed-version Autopilot path | Switch an idle, healthy cluster to `RollingUpdate`, wait for acceptance, then upgrade |

## Switch the 2.6 upgrade strategy

Use this exception only while the cluster is healthy and no upgrade, backup, or restore owns the operation lock.

```bash
kubectl -n <namespace> patch openbaocluster <name> \
  --type merge \
  -p '{"spec":{"upgrade":{"strategy":"RollingUpdate"}}}'

kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.acceptedUpgradeStrategy}{"\n"}'
```

Continue only after the command prints `RollingUpdate`. Fresh OpenBao 2.6 clusters and the validated rolling path do
not require this transition.

{{< callout type="warning" title="DeleteAll is not an object-storage eraser" >}}
Inventory the backup prefix before deleting a cluster. Snapshot objects can outlive the Kubernetes resource and its
PVCs, even when `spec.deletionPolicy` is `DeleteAll`.
{{< /callout >}}
