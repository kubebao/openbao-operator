# Controller JWT lifecycle

Source: `test/e2e/Controller_JWT_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `controller-jwt-renewal-migration-recovery` | renews Target credentials and recovers an initialized migration from an older snapshot | active | `controller-jwt-renewal`, `controller-jwt-migration`, `controller-jwt-recovery` | `lifecycle`, `controller-jwt`, `slow` |

## `controller-jwt-renewal-migration-recovery`

Path: `Controller JWT lifecycle > renews Target credentials and recovers an initialized migration from an older snapshot`

State: `active`

Generated fallback ID: `controller-jwt-renews-target-credentials-and-recovers-an-d3ad7cce`

Covers: `controller-jwt-renewal`, `controller-jwt-migration`, `controller-jwt-recovery`

Labels: `lifecycle`, `controller-jwt`, `slow`

Recorded checkpoints:
- bootstrapping Target mode with the operator-generated roles and policy approval
- deleting an operational policy while the original Target credential ages
- initializing an existing-style Shared cluster and saving its actual Raft snapshot
- preparing the existing role, switching the CR, then removing shared trust
- restoring the pre-migration snapshot while the CR remains in Target mode
- repairing restored audience trust through the independent administrator login
- observing periodic policy repair during the original Target credential's lifetime
- keeping the same controller process alive beyond the initial JWT's ten-minute lifetime
- repairing a deleted operational policy using the renewed Target credential
