---
trigger: always_on
glob: "**/*.go"
description: Security practices for the OpenBao Operator
---

# Security Practices

See [Security guidance](../../website/content/docs/security/_index.md) and [project standards](../../website/content/contribute/standards.md).

## Critical Security Rules

1. **File Permissions**: Use `0600` for secrets, `0644` for config
2. **Crypto**: Use `crypto/rand`, NEVER `math/rand`
3. **No Logging Secrets**: Never log tokens, keys, unseal keys, passwords
4. **Input Validation**: Validate all CRD inputs before use
5. **No Shelling Out**: Use Go SDKs, not kubectl/helm/cloud CLIs

## RBAC Model (Zero Trust)

This operator uses a **Zero Trust RBAC model**:

- Controller does NOT have cluster-wide permissions
- Provisioner grants permissions per-namespace
- Do NOT add `// +kubebuilder:rbac` to `internal/controller/openbaocluster/`
- New permissions go in `internal/service/provisioner/rbac.go`
- Update `internal/service/provisioner/rbac_test.go` to lock permissions

## Secrets Handling

```go
// Clear sensitive data when done
defer func() {
    for i := range key {
        key[i] = 0
    }
}()
```

## ValidatingAdmissionPolicy

Changes to RBAC/admission must be reviewed carefully.
