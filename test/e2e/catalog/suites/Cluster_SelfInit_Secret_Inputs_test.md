# Self-init Secret inputs

Source: `test/e2e/Cluster_SelfInit_Secret_Inputs_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `self-init-secret-input-lifecycle` | requires inputs before initialization and restarts without the bootstrap Secret | active | `self-init-secret-inputs`, `bootstrap-secret-removal` | `lifecycle`, `cluster`, `self-init-inputs` |

## `self-init-secret-input-lifecycle`

Path: `Self-init Secret inputs > requires inputs before initialization and restarts without the bootstrap Secret`

State: `active`

Generated fallback ID: `cluster-selfinit-secret-inputs-requires-inputs-before-initialization-and-restarts-c76fd642`

Covers: `self-init-secret-inputs`, `bootstrap-secret-removal`

Labels: `lifecycle`, `cluster`, `self-init-inputs`

Recorded checkpoints:
- blocking startup before OpenBao initializes when the Secret is absent
- keeping startup blocked when the Secret lacks the selected key
- consuming the selected key without giving the controller Secret read access
- checking generated resources, events, configuration, and normal logs for the canary
- completing bootstrap and removing the Secret input projections
- deleting the bootstrap Secret and replacing the initialized Pod
