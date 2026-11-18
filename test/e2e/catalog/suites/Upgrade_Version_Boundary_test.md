# Upgrade protocol boundary

Source: `test/e2e/Upgrade_Version_Boundary_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `upgrade-protocol-boundary` | rejects a pre-2.6 crossing in both strategies without replacing voters | active | `upgrade-version-compatibility` | `upgrade`, `rolling`, `e2e-anchor` |

## `upgrade-protocol-boundary`

Path: `Upgrade protocol boundary > rejects a pre-2.6 crossing in both strategies without replacing voters`

State: `active`

Generated fallback ID: `upgrade-version-boundary-rejects-a-pre-2-6-crossing-e78a85ce`

Covers: `upgrade-version-compatibility`

Labels: `upgrade`, `rolling`, `e2e-anchor`

Recorded checkpoints:
- clearing only the rejected version request before any strategy change
