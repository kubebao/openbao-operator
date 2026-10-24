---
title: Status and events
description: Condition types, phases, Kubernetes Events, and audit-log signals exposed by the operator.
eyebrow: Reference
weight: 2
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - api/v1alpha1/openbaorestore_types.go
  - api/v1alpha1/openbaotenant_types.go
  - internal/controller/openbaocluster
  - internal/app/provisioner
  - internal/service/backup
  - internal/service/restore
  - internal/service/upgrade
---

Use status for the latest controller observation and Events for the sequence that led to it. A condition's `reason`
and `message` contain the actionable detail; do not treat the high-level phase as a complete health check.

## Inspect current state

```bash
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.conditions}' | jq

kubectl -n <namespace> get openbaorestore <name> \
  -o jsonpath='{.status.conditions}' | jq

kubectl -n <namespace> get events --sort-by=.lastTimestamp
```

Use `kubectl describe` on the parent custom resource to see status and recent Events together.

## Workflow checkpoints

| Workflow | Conditions to inspect |
| --- | --- |
| Hardened cluster with External TLS | `Available`, `TLSReady`, `UserAccessBootstrap`, `ProductionReady` |
| Hardened cluster with ACME | `Available`, `ACMEIntegrationReady`, `ACMECacheReady`, `UserAccessBootstrap`, `ProductionReady` |
| Gateway exposure | `GatewayIntegrationReady`; inspect Route parent status for controller detail |
| Strict NetworkPolicy | `APIServerNetworkReady` |
| Scheduled backups | `BackupConfigurationReady`, `BackingUp` |
| File audit storage | `AuditFileStorageReady`; inspect `Degraded` when recreation is required |
| Restore | `RestoreConfigurationReady`, then `RestoreComplete` |

## OpenBaoCluster status

`status.phase` is one of `Initializing`, `Running`, `Upgrading`, `BackingUp`, or `Failed`. Conditions provide the
specific contract.

### Service and integration conditions

| Type | Signal |
| --- | --- |
| `Available` | Ready voter workload replicas |
| `TLSReady` | TLS assets required by the selected mode |
| `ACMEIntegrationReady` | Operator-known ACME reachability and Gateway prerequisites |
| `ACMECacheReady` | Shared ACME state for HA or blue-green topologies |
| `GatewayIntegrationReady` | Referenced Gateway, GatewayClass, listener, and managed Route attachment |
| `IngressIntegrationReady` | Managed Ingress prerequisites and load-balancer progress |
| `APIServerNetworkReady` | Kubernetes API egress represented by operator-managed NetworkPolicy |
| `AuditFileStorageReady` | Shared audit PVC readiness and workload mount adoption |

### Security and production conditions

| Type | Signal |
| --- | --- |
| `ProductionReady` | Operator-known Hardened posture checks; not API stability or project support |
| `UserAccessBootstrap` | Heuristic recognition of a human login bootstrap path; not proof that login works |
| `CloudUnsealIdentityReady` | Operator-known cloud KMS identity prerequisites |
| `EtcdEncryptionWarning` | The operator cannot verify Kubernetes etcd encryption |
| `SecurityRisk` | Development or otherwise relaxed security controls |
| `NodeSecurityCapabilityMismatch` | Requested workload hardening is unavailable on the node platform |

### Operation and storage conditions

| Type | Signal |
| --- | --- |
| `Upgrading` | Upgrade orchestration is active, idle, or failed |
| `BackingUp` | Backup orchestration is active or idle |
| `BackupConfigurationReady` | Operator-known backup authentication, storage, identity, and egress prerequisites |
| `StorageConfigured` | A consistent voter StorageClass is configured or resolved; not proof that a resize completed |
| `ReadReplicaStorageConfigured` | Equivalent storage selection for the read-replica pool |
| `ReadReplicasReady` | Desired read-replica Pods are Ready |
| `ReadServingAvailable` | An observed read replica can serve reads for the validated OpenBao version |
| `RaftMembershipReady` | Observed voter and non-voter membership matches the declared topology |
| `ReadReplicasAutopilotHealthy` | Autopilot reports healthy read-replica peers |
| `Degraded` | A workload, operation, configuration, or break-glass problem needs attention |

### Observed OpenBao conditions

| Type | Signal |
| --- | --- |
| `OpenBaoInitialized` | Initialization state observed from Kubernetes service-registration labels |
| `OpenBaoSealed` | Seal state observed from service-registration labels |
| `OpenBaoLeader` | Leader discovery from service-registration labels |

These three conditions report the labels OpenBao publishes; they are not independent API probes.

## OpenBaoRestore status

`status.phase` moves through `Pending`, `Validating`, `Running`, and either `Completed` or `Failed`.

| Type | Signal |
| --- | --- |
| `RestoreConfigurationReady` | Operator-known authentication, storage, identity, and egress prerequisites |
| `RestoreComplete` | Terminal restore result |
| `OperationLockOverride` | A forced disaster-recovery restore cleared another operation lock |

`AmbientIdentityAssumed` means the operator identified a provider default chain. It does not prove that the cloud-side
role, service account, or permission binding works.

## OpenBaoTenant status

Require `status.provisioned: true` and `Provisioned=True` for the current generation. When provisioning is blocked or
fails, the condition is false with a specific reason and `status.lastError` retains the failure summary for
compatibility.

## Kubernetes Events

The operator emits these lifecycle reasons on parent resources. `Normal` records progress or accepted input;
`Warning` records failure, contention, a safety override, or another state that needs attention.

| Workflow | Event reasons |
| --- | --- |
| Cluster safety and storage | `ProfileNotSet`, `DevelopmentProfile`, `UnsafeAdmissionDisabled`, `AmbientUnsealIdentity`, `StaticUnsealInUse`, `RootTokenStored`, image-verification reasons, `PVCResize`, `PVCResizeLeaderStepDown`, `PVCResizePodRestart` |
| Initialization | `InitStarted`, `InitCompleted`, `InitFailed` |
| Tenant Secret RBAC | `TenantSecretRBACSynchronized` |
| Upgrade | `UpgradeStarted`, `PreUpgradeSnapshotJobCreated`, `PreUpgradeSnapshotCompleted`, `PreUpgradeSnapshotFailed`, `RollingRetryRequested`, `RollingRetryAccepted`, `BlueGreenHoldEntered`, `BlueGreenPromotionApproved`, `UpgradeComplete`, `UpgradeFailed`, `RollbackStarted`, `BreakGlassEntered`, `BreakGlassAcknowledged`, `OperationLockBlocked` |
| Backup | `BackupManualTriggerAccepted`, `BackupSkipped`, `BackupStarted`, `BackupIdentityConfiguration`, `BackupJobCreated`, `BackupCompleted`, `BackupFailed`, `OperationLockBlocked` |
| Restore | `RestoreValidationStarted`, `RestoreStarted`, `RestoreIdentityConfiguration`, `RestoreJobCreated`, `RestoreCompleted`, `RestoreFailed`, `OperationLockBlocked`, `OperationLockLost`, `OperationLockOverride` |
| Tenant provisioning | `TenantProvisioned`, `TenantRBACCleaned`, `TenantProvisioningBlocked`, `TenantProvisioningFailed` |

`TenantRBACCleaned` confirms that tenant RBAC and the operator-managed `ResourceQuota` and `LimitRange` were removed.
Pod Security labels are intentionally outside that event's cleanup contract.

Controllers also write structured audit records to their logs for security-sensitive and lifecycle actions. Condition
types are API surface. Reason, Event, and audit-event values can expand as the controller gains new failure modes.
