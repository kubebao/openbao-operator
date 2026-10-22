# Upgrade Strategy Switching

Source: `test/e2e/Upgrade_Strategy_Switch_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `idle-upgrade-strategy-switch` | switches both directions at idle and preserves the active workload | active | `upgrade-strategy-switch`, `stable-workload-identity` | `upgrade`, `rolling`, `bluegreen`, `slow` |

## `idle-upgrade-strategy-switch`

Path: `Upgrade Strategy Switching > switches both directions at idle and preserves the active workload`

State: `active`

Generated fallback ID: `upgrade-strategy-switch-switches-both-directions-at-idle-and-ac0fc484`

Covers: `upgrade-strategy-switch`, `stable-workload-identity`

Labels: `upgrade`, `rolling`, `bluegreen`, `slow`

Recorded checkpoints:
- recording the initial rolling StatefulSet identity
- switching only the idle strategy from RollingUpdate to BlueGreen
- performing a blue-green upgrade to the intermediate version
- switching only the idle strategy from BlueGreen to RollingUpdate
- performing a rolling upgrade to the final version against the same StatefulSet
