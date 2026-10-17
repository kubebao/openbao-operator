# Scale-down recovery

Source: `test/e2e/Scale_Down_Recovery_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `scale-down-read-replica-interruption` | finishes a read-replica decrement after native peer removal | active | `interrupted-scale-down` | `lifecycle`, `cluster`, `scaling`, `recovery` |
| `scale-down-voter-interruption` | finishes a voter decrement after native peer removal | active | `interrupted-scale-down` | `lifecycle`, `cluster`, `scaling`, `recovery` |

## `scale-down-read-replica-interruption`

Path: `Scale-down recovery > finishes a read-replica decrement after native peer removal`

State: `active`

Generated fallback ID: `scale-down-recovery-finishes-a-read-replica-decrement-after-e2186dac`

Covers: `interrupted-scale-down`

Labels: `lifecycle`, `cluster`, `scaling`, `recovery`


## `scale-down-voter-interruption`

Path: `Scale-down recovery > finishes a voter decrement after native peer removal`

State: `active`

Generated fallback ID: `scale-down-recovery-finishes-a-voter-decrement-after-native-7ab90e6d`

Covers: `interrupted-scale-down`

Labels: `lifecycle`, `cluster`, `scaling`, `recovery`
