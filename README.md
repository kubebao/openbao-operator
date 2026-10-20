<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="website/static/img/brand/kubebao-logo-dark.png" />
  <img src="website/static/img/brand/kubebao-logo.png" alt="KubeBao" width="320" height="320" />
</picture>

**Secure lifecycle management for OpenBao on Kubernetes.**

[![CI](https://github.com/kubebao/openbao-operator/actions/workflows/ci.yml/badge.svg)](https://github.com/kubebao/openbao-operator/actions/workflows/ci.yml)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/15732/badge)](https://www.bestpractices.dev/projects/15732)
[![Go Version](https://img.shields.io/github/go-mod/go-version/kubebao/openbao-operator?filename=go.mod&label=Go&logo=go&logoColor=white)](https://github.com/kubebao/openbao-operator/blob/main/go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Docs](https://img.shields.io/badge/Docs-Live-green)](https://docs.kubebao.org/)
[![Artifact Hub](https://img.shields.io/badge/Artifact_Hub-Helm_OCI-417598?logo=artifacthub&logoColor=white)](https://artifacthub.io/packages/search?repo=openbao-operator)

[Get started](#get-started) • [Compatibility](#compatibility) • [Documentation](#documentation) • [Contributing](#contributing)

</div>

> [!WARNING]
> **Pre-GA Release**: OpenBao Operator is intended for real deployments, but the CRD API remains `v1alpha1`, minor releases may introduce breaking changes, and support is best-effort for the latest stable line only. For production, use the `Hardened` profile, keep admission enforcement enabled, pin explicit versions, and validate upgrades in staging.

---

OpenBao Operator is a Kubernetes operator for [OpenBao](https://openbao.org) that automates lifecycle management: provisioning, TLS, backups/restores, upgrades, horizontal read scaling, and multi-tenancy controls.

OpenBao Operator is maintained by [KubeBao](https://kubebao.org), an independent open-source project. It is not an official OpenBao project.

## Get started

Follow the **[Getting started guide](https://docs.kubebao.org/docs/get-started/)** to choose a deployment
model, install the operator, onboard a namespace, and create your first OpenBao cluster.

The documentation covers evaluation and production prerequisites, including platform-managed namespace security and
private registries.
For upgrades and removal, see [Upgrade the operator](https://docs.kubebao.org/docs/get-started/install/#upgrade-the-operator)
and [Uninstall the operator](https://docs.kubebao.org/docs/get-started/install/#uninstall-the-operator).

## Documentation

Full documentation is available at **[docs.kubebao.org](https://docs.kubebao.org/)**.

| | |
| :---: | :---: |
| [![User Guide](https://img.shields.io/badge/User_Guide-007EC6?style=for-the-badge&logo=readthedocs&logoColor=white)](https://docs.kubebao.org/docs/get-started) | [![Architecture](https://img.shields.io/badge/Architecture-326CE5?style=for-the-badge&logo=kubernetes&logoColor=white)](https://docs.kubebao.org/docs/architecture) |
| **Installation, Operations, Day-2 Tasks** | **Component Design, Boundaries, Flows** |
| [![Security](https://img.shields.io/badge/Security-000000?style=for-the-badge&logo=imou&logoColor=white)](https://docs.kubebao.org/docs/security) | [![Contributing](https://img.shields.io/badge/Contributing-181717?style=for-the-badge&logo=github&logoColor=white)](https://docs.kubebao.org/contribute) |
| **Threat Model, Hardening, RBAC** | **Dev Setup, Coding Standards, Release** |
| [![Compatibility](https://img.shields.io/badge/Compatibility-10b981?style=for-the-badge&logo=kubernetes&logoColor=white)](https://docs.kubebao.org/next/docs/reference/compatibility) | [![Samples](https://img.shields.io/badge/Samples-9333ea?style=for-the-badge&logo=yaml&logoColor=white)](config/samples/) |
| **Validated K8s/OpenBao Versions** | **Ready-to-apply Example Manifests** |

Recommended entry points:

- [Deployment Decision Guide](https://docs.kubebao.org/docs/get-started/deployment-model/)
- [Operator Invariants](https://docs.kubebao.org/docs/architecture/invariants-and-boundaries/)
- [Production Checklist](https://docs.kubebao.org/docs/operate/production-readiness/)

## Compatibility

For full details, see the [Compatibility Matrix](https://docs.kubebao.org/next/docs/reference/compatibility).

- **Kubernetes**: requires `v1.33+`; release validation runs on `v1.34`–`v1.36`
- **OpenBao**: primary validation target on `main` is `2.6.4`, with config compatibility checks for `2.4.4`, `2.5.5`, `2.6.4`, and `2.7.1`
- **Support posture**: best-effort support for the latest stable release line

## CRDs (API Surface)

- `OpenBaoCluster`: Deploy and operate an OpenBao cluster (TLS, unseal, backups, upgrades).
- `OpenBaoRestore`: Restore a cluster from a backup (separate controller).
- `OpenBaoTenant`: Multi-tenant provisioning flow (multi-tenant mode).

## Features

- **Two-Controller Architecture**: Separate controller and provisioner components with least-privilege RBAC boundaries.
- **Security Profiles with Guardrails**: `Development` vs `Hardened`, enforced by admission policies to prevent insecure combinations.
- **Self-Init + OIDC Bootstrap**: OpenBao self-initialization, with optional JWT/OIDC bootstrap via `spec.selfInit.oidc.enabled`.
- **TLS, Your Way**: Operator-managed TLS with rotation, external TLS, and ACME mode where OpenBao owns certificates (with ACME challenge Service support).
- **Streaming Raft Backups**: Snapshot streaming to S3/GCS/Azure with retention controls (no local staging).
- **Declarative Restores**: Restore workflows via `OpenBaoRestore` with operation locking and safe overrides.
- **Safe Upgrades**: Rolling and blue/green upgrade strategies, including pre-upgrade snapshots. `RollingUpdate` is the default recommended strategy.
- **Horizontal Read Scaling**: Steady-state read replicas as non-voters, with a dedicated read Service and a shared primary endpoint that can include read replicas.
- **Multi-Tenancy**: Namespace-scoped tenancy model with policy enforcement via `OpenBaoTenant`. Multi-tenant mode is the default and recommended production operating model.

## Security Model

- **Threat model**: Design assumptions and attacker model ([Threat Model](https://docs.kubebao.org/docs/security/threat-model/))
- **RBAC boundaries**: Least-privilege split between controller and provisioner ([Tenant Boundaries](https://docs.kubebao.org/docs/security/tenant-boundaries/))
- **Guardrails**: Validating admission policies that block dangerous settings before they reach the cluster ([Admission Policies](https://docs.kubebao.org/docs/security/admission/))
- **Multi-tenancy**: Namespace isolation guarantees and limits ([Tenant Boundaries](https://docs.kubebao.org/docs/security/tenant-boundaries/))

## Contributing

We welcome contributions! Please see the [Contributing Guide](https://docs.kubebao.org/contribute) for details on:

- Setting up your development environment.
- Running the PR-equivalent local gate (`devenv test` followed by the `operator:bootstrap`, `operator:doctor`, and
  `operator:ci-core` Devenv tasks).
- Our AI-Assisted Contribution Policy.

## Official OpenBao Documentation

- [OpenBao Documentation](https://openbao.org/docs/)
- [OpenBao Upgrade Guide](https://openbao.org/docs/upgrading/)

## License

Apache-2.0. See `LICENSE`.
