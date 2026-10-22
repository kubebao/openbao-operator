# Controller JWT lifecycle

Source: `test/e2e/Controller_JWT_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `controller-jwt-renewal-recovery` | renews Target credentials and recovers older snapshot authentication | active | `controller-jwt-renewal`, `controller-jwt-recovery` | `lifecycle`, `controller-jwt`, `slow` |

## `controller-jwt-renewal-recovery`

Path: `Controller JWT lifecycle > renews Target credentials and recovers older snapshot authentication`

State: `active`

Generated fallback ID: `controller-jwt-renews-target-credentials-and-recovers-older-b9ed93f0`

Covers: `controller-jwt-renewal`, `controller-jwt-recovery`

Labels: `lifecycle`, `controller-jwt`, `slow`

Recorded checkpoints:
- bootstrapping Target mode with the operator-generated roles and policy approval
- deleting an operational policy while the original Target credential ages
- capturing a snapshot with older shared role trust on a Target cluster
- repairing role trust before restoring the older snapshot
- restoring the older snapshot while the CR remains in Target mode
- repairing restored audience trust through the independent administrator login
- observing periodic policy repair during the original Target credential's lifetime
- keeping the same controller process alive beyond the initial JWT's ten-minute lifetime
- repairing a deleted operational policy using the renewed Target credential
