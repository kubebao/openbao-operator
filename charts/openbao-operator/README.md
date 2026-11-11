# OpenBao Operator Helm Chart

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/kubebao/openbao-operator/main/website/static/img/brand/kubebao-logo-dark.png" />
  <img src="https://raw.githubusercontent.com/kubebao/openbao-operator/main/website/static/img/brand/kubebao-logo.png" alt="KubeBao" width="240" height="240" />
</picture>

This chart installs the OpenBao Operator and its cluster-scoped dependencies.

## Prerequisites

- Kubernetes `>=1.34.0-0`
- Helm `v3` or `v4`

## Install

Follow the [Getting started guide](https://docs.kubebao.org/docs/get-started/) to choose a deployment
model, install the operator, onboard a namespace, and create your first OpenBao cluster.
The [installation procedure](https://docs.kubebao.org/docs/get-started/install/) covers Helm values,
namespace label ownership, CRDs, and verification.

The source chart uses KubeBao image repositories. Its version fields remain at the last release until release preparation updates them. To evaluate unreleased changes, use a verified edge chart or set the manager and helper image references to matching candidate builds. For 0.5.1 and earlier, follow the stable installation guide, which uses the published `dc-tec` artifacts.

## Common Configuration

The examples below are Helm values fragments. Merge the settings you need into `operator-values.yaml` and use that
file with the installation procedure.

### Multi-tenant mode (default)

```yaml
tenancy:
  mode: multi
```

### Multi-tenant mode with platform-managed Pod Security labels

Use this when Rancher or another platform policy layer owns namespace labels. The chart keeps tenant RBAC and quota onboarding in the operator, removes namespace update/patch RBAC from the Provisioner, and configures admission policy to deny Provisioner namespace mutations.

```yaml
tenancy:
  mode: multi
  namespacePodSecurityLabels:
    mode: external
```

### Single-tenant mode

```yaml
tenancy:
  mode: single
  targetNamespace: openbao-system
```

### Single-tenant mode with custom Helm identity

Use the release name or `fullnameOverride` when you want a custom operator identity. The chart keeps the controller `ServiceAccount`, single-tenant `RoleBinding`, and admission-policy references aligned from the rendered fullname.

```yaml
fullnameOverride: team-bao-operator

tenancy:
  mode: single
  targetNamespace: openbao-system
```

### Pin default helper images

Set `helperImages.init`, `helperImages.backup`, and `helperImages.upgrade` to complete image references when the
installation must pin default helpers by digest. Empty values use the configured repository and operator version.
Image fields on an `OpenBaoCluster` or `OpenBaoRestore` still take precedence over these defaults.

Edge packages populate these values and the manager digest from the verified candidate. See the
[edge installation instructions](https://docs.kubebao.org/next/docs/get-started/install/).

## Upgrade

Follow [Upgrade the operator](https://docs.kubebao.org/docs/get-started/install/#upgrade-the-operator)
to update CRDs and review OpenBao policy changes before upgrading the controller.

## Uninstall

Follow [Uninstall the operator](https://docs.kubebao.org/docs/get-started/install/#uninstall-the-operator)
for removal instructions and CRD-retention requirements.

## Values Reference

See:

- `charts/openbao-operator/values.yaml`
- `charts/openbao-operator/values.schema.json`

## More Information

- [Documentation](https://docs.kubebao.org/)
- [Compatibility matrix](https://docs.kubebao.org/docs/reference/compatibility/)
- [Source](https://github.com/kubebao/openbao-operator)
