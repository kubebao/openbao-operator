---
title: Configure read replicas
description: Add a steady non-voter pool, choose its client endpoint, and operate its storage and lifecycle safely.
eyebrow: Configure · Deployment
weight: 9
verifiedBy:
  - api/v1alpha1/openbaocluster_workload_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/app/openbaocluster/infra_read_replica_cleanup.go
  - internal/app/openbaocluster/infra_scale_down.go
  - internal/app/openbaocluster/storage.go
  - internal/service/networking/services.go
  - internal/service/networking/service_revision.go
  - internal/service/upgrade/rolling/read_replicas.go
  - internal/service/upgrade/bluegreen/read_replica_restore.go
  - internal/service/workload/statefulset_builder.go
---

Read replicas are permanent Raft non-voters in a second StatefulSet. Use them for read capacity or independent
placement, not as another quorum tier. Test the request behavior and failure topology you intend to depend on; the
operator does not promise that every read is served locally or remains available without voter quorum.

## Add the read pool

{{< command label="configure" title="Create two read replicas" >}}
spec:
  replicas: 3
  storage:
    size: 10Gi
  readReplicas:
    replicas: 2
    service:
      enabled: true
      type: ClusterIP
    template:
      resources:
        requests:
          cpu: 250m
          memory: 512Mi
      scheduling:
        topologySpreadConstraints:
          - maxSkew: 1
            topologyKey: topology.kubernetes.io/zone
            whenUnsatisfiable: ScheduleAnyway
            labelSelector:
              matchLabels:
                openbao.org/cluster: prod-cluster
    storage:
      size: 10Gi
      storageClassName: fast-ssd
{{< /command >}}

Read-replica storage defaults to the voter storage contract. An explicit read size cannot be smaller than voter
storage, and the effective read StorageClass cannot change after read PVCs exist. A StorageClass reference requires
delegated `use` permission.

## Choose the endpoint

| Endpoint | Selection behavior | Limit |
| --- | --- | --- |
| `<cluster>-public` | With RollingUpdate, selects voter and steady read-replica Pods | A BlueGreen revision selector excludes the steady read pool |
| `<cluster>-read` | When enabled, selects only the steady read-replica StatefulSet | The operator does not create a Gateway or Ingress route for it |

The dedicated Service is an endpoint-selection tool, not a read-only enforcement boundary. The operator does not
inspect methods or paths, and write-class requests can still rely on OpenBao request forwarding. Keep the main
endpoint for general clients unless you have measured and tested a reason to split traffic.

See [Expose OpenBao](../expose/) before publishing either Service. The Gateway and Ingress integrations route only to
the main public Service.

## Read pool status

| Condition | Meaning |
| --- | --- |
| `ReadReplicasReady` | The desired number of read Pods is Ready |
| `ReadServingAvailable` | At least one Ready read Pod reports a serving health state |
| `RaftMembershipReady` | Observed voters and non-voters match the declared topology |
| `ReadReplicasAutopilotHealthy` | Raft Autopilot reports the read peers healthy |
| `ReadReplicaStorageConfigured` | Read PVC count, binding, size, and StorageClass match the effective contract |

{{< command label="inspect" title="Inspect read-replica status" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.readReplicas}{"\n"}{range .status.conditions[*]}{.type}={.status}{"\t"}{.reason}{"\n"}{end}'
{{< /command >}}

Treat `Unknown` as missing evidence, not success. Inspect the read StatefulSet, Pods, PVCs, Raft membership, and
Autopilot state before sending production traffic to the pool.

## Understand day-2 behavior

- Scale-down removes the departing non-voter from Raft before reducing the StatefulSet one ordinal at a time.
- A rolling upgrade waits for the read pool to reach the target revision and pass health checks before voters roll.
- Blue-green upgrades and restores drain steady read replicas, complete the destructive phase, and restore the pool
  before reporting completion.
- Removing `spec.readReplicas` drains the pool, deletes the read StatefulSet and ConfigMap, and removes the optional
  read Service.

{{< callout type="warning" title="Scale-down deletes read-replica PVCs" >}}
The generated StatefulSet uses `WhenScaled: Delete`. Scaling the pool down, including disabling it, deletes PVCs for
removed ordinals after their Raft peers are removed. Deleting the StatefulSet without scaling retains its remaining
PVCs, but the normal disable workflow first scales to zero. Treat a later re-enable as a fresh read pool.
{{< /callout >}}

There is no fixed supported RTT budget for read replicas. Validate cross-zone or cross-region placement with the
actual latency, replication lag, Autopilot state, and client workload you expect to run.
