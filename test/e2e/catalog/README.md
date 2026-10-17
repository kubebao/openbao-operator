# E2E Case Catalog

Generated from `ginkgo outline` for the files under `test/e2e/`.

Notes:
- Suite and spec inventory comes from `ginkgo outline`; `case:` and `covers:` labels are the stable tracking fields.
- `steps` are optional recorded checkpoints derived from literal `By(...)` text that `ginkgo outline` can see.
- Missing checkpoints do not imply missing coverage.
- `case:` labels become the primary catalog IDs when present; otherwise a generated fallback ID is used.
- Use `cases.json` for automation; use the suite pages for human review.

## Summary

- Files: `27`
- Specs: `102`
- Explicit case IDs: `102`
- Coverage tags: `60`

## Suites

| Suite | Cases | Tracked | Pending | Labels | Source |
| --- | ---: | ---: | ---: | --- | --- |
| [Cluster Lifecycle: Deletion Policy](suites/Cluster_DeletionPolicy_test.md) | 3 | 3 | 0 | `lifecycle`, `cluster`, `deletion` | `test/e2e/Cluster_DeletionPolicy_test.go` |
| [Cluster Lifecycle](suites/Cluster_Lifecycle_test.md) | 9 | 9 | 0 | `lifecycle`, `cluster`, `profile-development`, `scaling`, `autopilot`, `smoke`, `critical`, `tenant`, `audit` | `test/e2e/Cluster_Lifecycle_test.go` |
| [Cluster Networking](suites/Cluster_Networking_test.md) | 2 | 2 | 0 | `networking`, `critical`, `e2e-anchor` | `test/e2e/Cluster_Networking_test.go` |
| [Hardened profile (External TLS + Transit auto-unseal + SelfInit)](suites/Cluster_Profile_Hardened_test.md) | 5 | 5 | 0 | `profile-hardened`, `security`, `cluster`, `upgrade`, `bluegreen`, `hardened`, `rolling` | `test/e2e/Cluster_Profile_Hardened_test.go` |
| [Cluster Runtime Controls](suites/Cluster_Runtime_Controls_test.md) | 3 | 3 | 0 | `lifecycle`, `cluster`, `runtime` | `test/e2e/Cluster_Runtime_Controls_test.go` |
| [Self-init Secret inputs](suites/Cluster_SelfInit_Secret_Inputs_test.md) | 1 | 1 | 0 | `lifecycle`, `cluster`, `self-init-inputs` | `test/e2e/Cluster_SelfInit_Secret_Inputs_test.go` |
| [ACME TLS (OpenBao native ACME client)](suites/Cluster_TLS_ACME_test.md) | 2 | 2 | 0 | `tls`, `security`, `slow` | `test/e2e/Cluster_TLS_ACME_test.go` |
| [Cluster TLS Lifecycle](suites/Cluster_TLS_Lifecycle_test.md) | 1 | 1 | 0 | `tls`, `cluster`, `lifecycle` | `test/e2e/Cluster_TLS_Lifecycle_test.go` |
| [Cluster KMIP Unseal](suites/Cluster_Unseal_KMIP_test.md) | 1 | 1 | 0 | `cluster`, `lifecycle`, `unseal`, `kmip`, `hsm` | `test/e2e/Cluster_Unseal_KMIP_test.go` |
| [Cluster PKCS#11 Unseal](suites/Cluster_Unseal_PKCS11_test.md) | 1 | 1 | 0 | `cluster`, `lifecycle`, `unseal`, `pkcs11`, `hsm` | `test/e2e/Cluster_Unseal_PKCS11_test.go` |
| [Controller JWT lifecycle](suites/Controller_JWT_test.md) | 1 | 1 | 0 | `lifecycle`, `controller-jwt`, `slow` | `test/e2e/Controller_JWT_test.go` |
| [Manager Resilience](suites/Manager_Resilience_test.md) | 3 | 3 | 0 | `manager`, `cluster`, `e2e-anchor` | `test/e2e/Manager_Resilience_test.go` |
| [Manager](suites/Operator_Manager_test.md) | 1 | 1 | 0 | `manager`, `critical`, `smoke` | `test/e2e/Operator_Manager_test.go` |
| [OpenShift Platform](suites/Platform_OpenShift_test.md) | 2 | 2 | 0 | `openshift`, `platform` | `test/e2e/Platform_OpenShift_test.go` |
| [Security Guardrails](suites/Security_Guardrails_test.md) | 22 | 22 | 0 | `security`, `critical`, `admission`, `pentest`, `config`, `tokens`, `rbac`, `tamper` | `test/e2e/Security_Guardrails_test.go` |
| [Tenant Data Isolation](suites/Tenant_Data_Isolation_test.md) | 1 | 1 | 0 | `security`, `tenant`, `tenancy` | `test/e2e/Tenant_Data_Isolation_test.go` |
| [Tenant Isolation](suites/Tenant_Isolation_test.md) | 6 | 6 | 0 | `security`, `tenant`, `tenancy`, `critical`, `single-tenant` | `test/e2e/Tenant_Isolation_test.go` |
| [Upgrade Strategies: Gateway API](suites/Upgrade_Gateway_test.md) | 2 | 2 | 0 | `upgrade`, `upgrades`, `cluster`, `slow`, `gateway`, `requires-gateway-api`, `tls-passthrough`, `bluegreen`, `e2e-anchor` | `test/e2e/Upgrade_Gateway_test.go` |
| [Upgrade Strategies: Operation Lock Contention](suites/Upgrade_Operation_Lock_test.md) | 3 | 3 | 0 | `upgrade`, `backup`, `operation-lock`, `slow`, `e2e-anchor` | `test/e2e/Upgrade_Operation_Lock_test.go` |
| [Rolling execution identity](suites/Upgrade_Rolling_Execution_test.md) | 1 | 1 | 0 | `upgrade`, `upgrades`, `rolling`, `cluster`, `slow`, `e2e-anchor` | `test/e2e/Upgrade_Rolling_Execution_test.go` |
| [Upgrade Strategies](suites/Upgrade_Strategies_test.md) | 10 | 10 | 0 | `upgrade`, `upgrades`, `cluster`, `slow`, `bluegreen`, `verification`, `e2e-anchor`, `failure`, `rollback`, `rolling`, `recovery`, `snapshot`, `read-replicas`, `read-replicas-rolling`, `chaos` | `test/e2e/Upgrade_Strategies_test.go` |
| [Upgrade Strategy Switching](suites/Upgrade_Strategy_Switch_test.md) | 1 | 1 | 0 | `upgrade`, `rolling`, `bluegreen`, `slow` | `test/e2e/Upgrade_Strategy_Switch_test.go` |
| [Upgrade Strategies: Blue/Green Drift](suites/Upgrade_Target_Drift_test.md) | 1 | 1 | 0 | `upgrade`, `bluegreen`, `slow` | `test/e2e/Upgrade_Target_Drift_test.go` |
| [Security: Anti-Tamper Policy](suites/anti_tamper_policy_test.md) | 2 | 2 | 0 | `security`, `tamper`, `cluster`, `slow` | `test/e2e/anti_tamper_policy_test.go` |
| [DR: Storage Providers Backup & Restore](suites/backup_restore_test.md) | 9 | 9 | 0 | `dr`, `backup`, `restore`, `storage-providers`, `nightly`, `slow`, `e2e-anchor`, `provider-smoke`, `read-replicas`, `read-replicas-restore`, `failure-injection` | `test/e2e/backup_restore_test.go` |
| [Restore across independent Kubernetes clusters](suites/restore_cross_cluster_test.md) | 2 | 2 | 0 | `restore-cross-cluster`, `dr`, `restore`, `slow` | `test/e2e/restore_cross_cluster_test.go` |
| [Minimal fresh restore and scheduled restore tests](suites/restore_minimal_test.md) | 7 | 7 | 0 | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` | `test/e2e/restore_minimal_test.go` |

## Coverage Tags

| Coverage | Cases |
| --- | ---: |
| `deletion-policy` | 3 |
| `lifecycle-job-owner-proof` | 3 |
| `operation-lock` | 3 |
| `anti-tamper` | 2 |
| `backup-owner-first` | 2 |
| `network-policy-enforcement` | 2 |
| `pod-ip-advertisement` | 2 |
| `pvc-cleanup` | 2 |
| `recoverability-secret-cleanup` | 2 |
| `rolling-upgrade` | 2 |
| `admission-runtime-recheck` | 1 |
| `backup-queueing` | 1 |
| `bluegreen-drift` | 1 |
| `bluegreen-operation-identity` | 1 |
| `bluegreen-replica-drift` | 1 |
| `bluegreen-upgrade` | 1 |
| `bootstrap-secret-removal` | 1 |
| `cert-replacement` | 1 |
| `configmap-protection` | 1 |
| `controller-failover` | 1 |
| `controller-jwt-migration` | 1 |
| `controller-jwt-recovery` | 1 |
| `controller-jwt-renewal` | 1 |
| `controller-outage` | 1 |
| `controller-restart` | 1 |
| `data-plane-isolation` | 1 |
| `existing-cluster-adoption` | 1 |
| `external-service` | 1 |
| `idempotent-reconcile` | 1 |
| `ingress` | 1 |
| `leader-election` | 1 |
| `managed-resource-pause-on-policy-loss` | 1 |
| `manager-metrics-endpoint` | 1 |
| `network-isolation` | 1 |
| `plugin-auto-download` | 1 |
| `plugin-directory` | 1 |
| `pod-rollout` | 1 |
| `pod-stability` | 1 |
| `post-failover-reconcile` | 1 |
| `post-outage-reconcile` | 1 |
| `pvc-retention` | 1 |
| `recoverability-secret-retention` | 1 |
| `restart-at` | 1 |
| `restore-static-token-secret-identity` | 1 |
| `rolling-execution-identity` | 1 |
| `rolling-retry-rendered-target` | 1 |
| `scale-reconcile` | 1 |
| `secret-regeneration` | 1 |
| `self-init-secret-inputs` | 1 |
| `stable-workload-identity` | 1 |
| `stale-green-cleanup` | 1 |
| `statefulset-protection` | 1 |
| `target-revision-drift` | 1 |
| `tenant-isolation` | 1 |
| `tls-hot-reload` | 1 |
| `tls-lifecycle` | 1 |
| `tls-san` | 1 |
| `tls-secret-cleanup` | 1 |
| `tls-verification` | 1 |
| `upgrade-strategy-switch` | 1 |
