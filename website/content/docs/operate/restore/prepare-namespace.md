---
title: Prepare a recovery namespace
description: Prepare and approve a namespace where the operator may create restore targets.
eyebrow: Operate · Restore
weight: 4
verifiedBy:
  - config/policy/openbao-protect-restore-execution.yaml
  - internal/service/networking/manager.go
  - internal/service/restore/target.go
---

Restore targets run restored production data, which can include active integrations and stored credentials. A
platform administrator prepares the network boundary and then approves the namespace before the operator may create
targets in it.

{{< callout type="note" title="Guarantees and limits" >}}
- The operator does not create, verify, or monitor network isolation for restore targets.
- The approval label records an administrator decision. It is not a check of isolation.
{{< /callout >}}

## Choose a location

Use a dedicated namespace in the production Kubernetes cluster, or a separate recovery Kubernetes cluster with its own
operator. Enroll the namespace with the operator. Keep it enrolled until its restore and cluster resources finish
finalizing, and delete those resources before deleting the namespace.

## Prepare the network boundary

Allow only the connections each workload needs:

| Workload | Needs |
| --- | --- |
| Restore executor | Kubernetes API, object storage, target OpenBao API, DNS |
| Target OpenBao | Configured unseal provider, JWT discovery, DNS |

After restore, JWT discovery uses the configuration from the snapshot, which can differ from the bootstrap
configuration. Prepare access for it in advance.

The operator installs no NetworkPolicies on restore targets. Kubernetes NetworkPolicies are additive: any policy that
allows traffic to the same Pods widens the boundary. Review every policy that selects the namespace, and test the
result on your CNI, including node traffic, host networking, and IPv4/IPv6. A rule allowing a shared IP and port
allows every service at that endpoint. For Cilium, see
[Kubernetes API access with Cilium](../../../configure/network/#restore-kubernetes-api-access-with-cilium).

## Approve the namespace

After testing the boundary, a platform administrator approves the namespace:

```bash
kubectl label namespace recovery-tests openbao.org/restore-target-approved=true
```

Admission requires this label when a restore with `spec.clusterTemplate` is created, including scheduled restore
tests, and when the operator creates the target cluster. Restores into existing clusters do not need it. Limit
namespace label changes to trusted platform administrators.

To withdraw approval:

```bash
kubectl label namespace recovery-tests openbao.org/restore-target-approved-
```

New requests and targets are then rejected. Existing targets continue through handoff and cleanup and keep running
until removed, so keep the boundary in place until then.
