# Cluster Networking

Source: `test/e2e/Cluster_Networking_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `network-addresses-and-policy` | advertises valid Pod IP URLs and enforces workload egress policy | active | `pod-ip-advertisement`, `network-policy-enforcement` | `networking`, `critical`, `e2e-anchor` |
| `network-external-tls-and-policy` | uses External TLS with only the stable server name | active | `pod-ip-advertisement`, `network-policy-enforcement` | `networking`, `critical`, `e2e-anchor` |

## `network-addresses-and-policy`

Path: `Cluster Networking > advertises valid Pod IP URLs and enforces workload egress policy`

State: `active`

Generated fallback ID: `cluster-networking-advertises-valid-pod-ip-urls-and-4de2c193`

Covers: `pod-ip-advertisement`, `network-policy-enforcement`

Labels: `networking`, `critical`, `e2e-anchor`


## `network-external-tls-and-policy`

Path: `Cluster Networking > uses External TLS with only the stable server name`

State: `active`

Generated fallback ID: `cluster-networking-uses-external-tls-with-only-the-2e2c13a7`

Covers: `pod-ip-advertisement`, `network-policy-enforcement`

Labels: `networking`, `critical`, `e2e-anchor`
