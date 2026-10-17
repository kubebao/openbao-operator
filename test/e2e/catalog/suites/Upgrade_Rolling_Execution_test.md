# Rolling execution identity

Source: `test/e2e/Upgrade_Rolling_Execution_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `rolling-consecutive-execution-identity` | completes consecutive upgrades while prior step-down Jobs remain | active | `rolling-execution-identity` | `upgrade`, `upgrades`, `rolling`, `cluster`, `slow`, `e2e-anchor` |

## `rolling-consecutive-execution-identity`

Path: `Rolling execution identity > completes consecutive upgrades while prior step-down Jobs remain`

State: `active`

Generated fallback ID: `upgrade-rolling-execution-completes-consecutive-upgrades-while-prior-step-32eaae65`

Covers: `rolling-execution-identity`

Labels: `upgrade`, `upgrades`, `rolling`, `cluster`, `slow`, `e2e-anchor`

Recorded checkpoints:
- Retaining successful prior-attempt Jobs for every ordinal before the next upgrade
