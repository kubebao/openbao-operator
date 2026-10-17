# Upgrade Strategies: Blue/Green Drift

Source: `test/e2e/Upgrade_Target_Drift_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `bluegreen-target-drift-restart` | abandons an outdated green revision without rolling back | active | `bluegreen-drift`, `target-revision-drift`, `stale-green-cleanup` | `upgrade`, `bluegreen`, `slow`, `e2e-anchor` |

## `bluegreen-target-drift-restart`

Path: `Upgrade Strategies: Blue/Green Drift > abandons an outdated green revision without rolling back`

State: `active`

Generated fallback ID: `upgrade-target-drift-abandons-an-outdated-green-revision-without-0a3dcf0d`

Covers: `bluegreen-drift`, `target-revision-drift`, `stale-green-cleanup`

Labels: `upgrade`, `bluegreen`, `slow`, `e2e-anchor`

Recorded checkpoints:
- recording Blue ownership and data before the aborted upgrade
- starting a blue/green upgrade
- waiting for the first green revision to enter an early phase
- changing the desired target image while the first green revision is still in flight
- verifying the stale green workload is cleaned up
- verifying abort preserves Blue and both revisions' recorded data claims
- verifying the stale target is abandoned before any rollback
- verifying no stale workload remains in the namespace
