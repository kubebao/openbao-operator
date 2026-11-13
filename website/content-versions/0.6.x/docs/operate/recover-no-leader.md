---
title: Recover from no leader
description: Diagnose pod, network, and membership failures before using destructive manual Raft recovery.
eyebrow: Operate · Recovery
weight: 8
verifiedBy:
  - api/v1alpha1/openbaocluster_status_types.go
  - internal/adapter/config/builder.go
  - internal/adapter/config/render_storage.go
  - internal/adapter/raft/autopilot.go
  - internal/adapter/raft/autopilot_runtime_test.go
  - internal/platform/constants/filesystem.go
  - internal/platform/resourceidentity/names.go
---

Treat no leader as a quorum incident, not a reason to edit Raft state immediately. Repair unhealthy Pods and cluster
transport first. Remove a peer only when a healthy member still has a trustworthy Raft view.

Raft inspection and peer removal require an authenticated OpenBao session with the corresponding `sys/storage/raft`
capabilities. Use your approved interactive login path. Do not pass a privileged token as a command argument or store
it in shell history.

## Preserve evidence and check the live view

{{< command label="inspect" title="Inspect voters and Raft membership" >}}
kubectl -n <namespace> get pods -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.activeLeader}{"\n"}{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
kubectl -n <namespace> exec <healthy-pod> -- bao status
kubectl -n <namespace> exec <healthy-pod> -- bao operator raft list-peers
{{< /command >}}

Resolve a crash loop, missing PVC, sealed member, or invalid configuration before membership work. If Pods are healthy
but Raft calls time out, test DNS and the cluster port between voters with an approved diagnostic tool. The rendered
default is TCP 8201 and the headless Service has the cluster name. The OpenBao image does not guarantee a bundled
network-debug utility.

Check the managed NetworkPolicy, Service and EndpointSlice state, node or zone isolation, and any service-mesh policy
before changing the peer set.

## Remove one confirmed stale peer

Use this only when a healthy leader answers Raft commands and the listed server ID belongs to a member that is
permanently gone.

{{< command label="apply" title="Remove a dead Raft peer" >}}
kubectl -n <namespace> exec <healthy-pod> -- \
  bao operator raft remove-peer <dead-server-id>
{{< /command >}}

The current CLI accepts the server ID as a positional argument. Read it from `bao operator raft list-peers`; do not
guess from an IP address. Recheck membership and leader status after each removal.

## Use peers.json only when normal quorum is impossible

{{< callout type="danger" title="Manual quorum recovery can discard newer committed state" >}}
`peers.json` forces a selected survivor to form a new Raft configuration. Choosing a stale volume, booting multiple old
members without a plan, or using the wrong address can create additional data loss. Take storage-level snapshots of
every surviving PVC and escalate to the service owner before continuing.
{{< /callout >}}

The operator renders the Raft storage path as `/bao/data`, the node ID as the Pod hostname, and the cluster address as
the Pod's headless-Service DNS name. A one-survivor recovery file therefore has this shape:

{{< command label="configure" title="Define the selected survivor" >}}
[
  {
    "id": "<survivor-pod>",
    "address": "<survivor-pod>.<cluster>.<namespace>.svc:8201",
    "non_voter": false
  }
]
{{< /command >}}

Enable maintenance mode and wait for the survivor Pod to receive `openbao.org/maintenance=true`. The caller also needs
ordinary Pod delete permission and the custom `maintenance` verb on the target cluster. Then pause the cluster, write
the reviewed file to `/bao/data/raft/peers.json` on only the selected survivor, and restart that Pod:

{{< command label="apply" title="Start the selected survivor" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p \
  '{"spec":{"maintenance":{"enabled":true}}}'
kubectl -n <namespace> get pod <survivor-pod> \
  -o jsonpath='{.metadata.annotations.openbao\.org/maintenance}{"\n"}'
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{"spec":{"paused":true}}'
kubectl -n <namespace> exec -i <survivor-pod> -- \
  sh -c 'umask 077; tee /bao/data/raft/peers.json >/dev/null' < peers.json
kubectl -n <namespace> delete pod <survivor-pod>
{{< /command >}}

Do not restart the remaining old PVCs as a group. First verify the survivor's seal state, leader status, and data, then
write an explicit replacement and join plan for each other member. Resume reconciliation only after the desired Raft
topology is coherent.

{{< command label="verify" title="Verify the recovered survivor" >}}
kubectl -n <namespace> exec <survivor-pod> -- bao status
kubectl -n <namespace> exec <survivor-pod> -- bao operator raft list-peers
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{"spec":{"paused":false}}'
kubectl -n <namespace> patch openbaocluster <name> --type merge -p \
  '{"spec":{"maintenance":{"enabled":false}}}'
{{< /command >}}

If no surviving volume is trustworthy, use a validated [snapshot restore](../restore/) instead of forcing an unknown
Raft member to become authoritative.
