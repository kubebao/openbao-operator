---
title: Follow project standards
description: Apply the repository rules for Go, controllers, errors, security, documentation, commits, and generated artifacts.
eyebrow: Contribute
weight: 2
verifiedBy:
  - .ast-grep/policy/architecture-boundaries.yml
  - Makefile
  - mk/development.mk
---

Keep changes typed, level-triggered, reviewable, and aligned with the repository's enforced package boundaries. Prefer a direct implementation over a speculative abstraction.

## Shape the change

- Keep one main theme per pull request. Avoid unrelated cleanup and drive-by formatting.
- Avoid `any` and `interface{}` in core logic unless an external boundary requires them.
- Wait for a repeated pattern before extracting a shared abstraction.
- Use defined types for enum-like values. Avoid packages named `util`, `common`, or `shared`.
- Prefer additive CRD evolution. Document and test any unavoidable migration.
- Update architecture policy before introducing a boundary the current policy forbids.

Controllers observe current state, delegate work, patch status, and requeue. They must be idempotent. Do not use unmanaged goroutines, `time.Sleep`, or shell commands such as `kubectl`, `helm`, or `bao` in controller paths.

## Write idiomatic, diagnosable Go

- Keep acronyms consistently cased: `ServeHTTP`, `ParseURL`, and `userID`.
- Wrap errors with local context and `%w`.
- Use Kubernetes typed-error helpers instead of matching error strings.
- Use exported sentinel errors only when callers need `errors.Is` control flow.
- Return errors from controllers; do not panic.
- Use structured `logr` fields. Never log tokens, keys, passwords, or Secret data.
- Name important constants and keep imports grouped as standard library, third party, and local packages.

```go
if err := r.Client.Create(ctx, secret); err != nil {
    return fmt.Errorf("create bootstrap secret %s/%s: %w", secret.Namespace, secret.Name, err)
}
```

## Preserve security boundaries

Use `crypto/rand` for security-sensitive values, restrict secret files to permissions such as `0600`, validate CR-derived paths and ranges, and minimize sensitive data lifetime in memory. Reuse the repository's identity, apply, ownership, and configuration boundaries instead of copying their logic into another service.

Metrics use the `openbao_` prefix and stable labels. Logs use consistent, contextual field names.

## Regenerate owned artifacts

Never edit generated output by hand.

| Changed source | Owning command |
| --- | --- |
| Go API types | `make manifests generate` |
| Go module versions and OSV policy sources | `make vendor` |
| API comments or shapes | `make api-reference` |
| Served CRD stability decisions | `make update-api-stability-inventory` |
| Chart CRD, admission, policy, or RBAC inputs | `make helm-sync` |
| Architecture boundary policy | `make generate-ast-rules` |
| Configuration renderer behavior | `make test-update-golden` |

Review every regenerated diff and commit source and output together.

## Write task-oriented documentation

Lead with the operator's decision or action. Use sentence-case headings, imperative steps, consistent product names, concise explanations, and concrete verification. Choose a page contract—landing, task, concept, runbook, or reference—and keep structural components to those that improve scanning.

The repository-owned Hugo build is canonical. Use the existing shortcodes only when their semantics improve the task;
do not introduce a package-manager-backed documentation component layer.

## Use conventional commits

Use `<type>(<scope>): <description>` for commit subjects and pull-request titles. Common types include `feat`, `fix`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, and `revert`. Mark breaking changes with `!` or a `BREAKING CHANGE:` footer.

Continue with [testing]({{< relref "/contribute/testing.md" >}}) and [CI]({{< relref "/contribute/ci.md" >}}).
