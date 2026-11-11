---
trigger: always_on
glob: "{api,cmd,internal}/**/*.go"
description: Layered architecture and dependency direction rules for the OpenBao Operator
---

# Architecture Rules

See [Architecture Documentation](../../website/content/docs/architecture/_index.md).

## Layer Model (L0-L7)

- `L0` API types: `api/v1alpha1`
- `L1` Entrypoints/bootstrap: `cmd/*`, `internal/platform/entrypoint`
- `L2` Controller plumbing: `internal/controller/*`
- `L3` App orchestration: `internal/app/*`
- `L4` Services/managers:
  `internal/service/{backup,bootstrap,certs,configuration,identity,init,networking,opslifecycle,provisioner,restore,upgrade,workload,workloadidentity}`
- `L5` Ports/contracts:
  `internal/port/{adminops,auth,backup,blobstore,imageverify,initmanager,openbao,security,workload}`
- `L6` Adapters/integrations:
  `internal/adapter/{auth,cluster,config,kube,openbao,probe,raft,revision,security,storage,storageenv}`
- `L7` Platform/cross-cutting:
  `internal/platform/{admission,constants,entrypoint,errors,hardenedcontract,logging,observability,openbaotls,predicates,reconcile,resourceapply,resourceidentity,resourceownership,semver,statusapply,statuspatch,testutil}`

## Separation of Concerns

### Controllers (`internal/controller/*`, L2)

Reconcilers should do only:

1. Fetch input state from the API
2. Observe and assemble reconcile context
3. Delegate orchestration to `internal/app/*` and focused helpers
4. Apply final patch/result handling

Do not embed broad business orchestration directly in controllers.
Controller-local dependency adapters are allowed for import-surface management, but they must not become a business-logic layer.

### App Orchestration (`internal/app/*`, L3)

App packages coordinate domain workflows and phase sequencing.

App packages may call services/managers, ports, and platform utilities.

### Services/Managers (`internal/service/*`, L4)

Service packages own domain behavior.
Prefer `internal/port/*` when a stable contract or neutral type improves reuse, but focused direct adapter imports are acceptable when a service package is the correct home for concrete apply/build/integration logic.
Ports stay contract-only: they may contain interfaces, neutral types, and domain helpers, but they must not import concrete adapter packages.

Services must not import controller packages.

### Adapters (`internal/adapter/*`, L6)

Adapters implement ports and integration logic.

Adapters must not import controllers or service/manager packages.

### API Types (`api/v1alpha1`, L0)

CRD structs are data containers:

- Keep logic minimal and data-oriented
- Avoid external service behavior in API types

## Manager Pattern

Each domain manager should:

- Own its domain behavior
- Expose focused methods with explicit inputs/outputs
- Depend on narrow ports/interfaces where external dependencies are needed

Not every service package must expose a `Manager` type.
Shared coordination packages such as `internal/service/opslifecycle` are still service-layer code and should stay narrow, explicit, and domain-scoped.

## No God Objects

Do NOT pass entire Reconciler to helpers:

```go
// Bad
func helper(r *Reconciler, cluster *v1alpha1.OpenBaoCluster)

// Good
func helper(ctx context.Context, client client.Client, cluster *v1alpha1.OpenBaoCluster)
```

## Dependency Direction (Strict)

Allowed direction:

- `L1 -> L2/L3/L7`
- `L2 -> L3/L7/(small focused L4 usage when unavoidable)`
- `L3 -> L4/L5/L7`
- `L4 -> L5/L6/L7`
- `L6 -> L5/L7`
- Any layer may consume `L0` types as needed

Disallowed direction:

- `L3 -> L6` (app importing adapters directly)
- `L5 -> L6` (ports importing adapters directly)
- `L4/L6 -> L2` (service/adapter importing controllers)
- `L6 -> L4` (adapter importing services/managers)
- Re-introducing `internal/interfaces` as a generic dependency bucket

## Optional Module Boundaries

- Core lifecycle APIs live under `kubebao.org`; `OpenBaoCluster`, `OpenBaoRestore`, and `OpenBaoTenant` are core.
- Optional modules own separate API groups and versions. The Claims and Service Offerings module uses
  `claims.kubebao.org`.
- Optional module packages may depend on stable core APIs and narrow contracts. Core packages must not import
  optional module packages.
- Core generation, installation, startup, and reconciliation must work when optional module CRDs and controllers
  are absent.
- A module proposal must add architecture-policy and core-only test coverage before it lands.

## Verification

For architecture-impacting changes, run:

```sh
make generate-ast-rules
make verify-arch-policy
make test-ast
make lint-ast
make report-internal-deps
```

Review:

- `dist/architecture/internal-dependency-report.md`
- `hack/architecture/dependency-policy-exceptions.tsv`
- `.ast-grep/policy/architecture-boundaries.yml`

Expectation:

- Keep dependency policy warnings at `None` unless adding a deliberate, documented exception.
- Keep ast-grep architecture and reconcile guardrails green.
