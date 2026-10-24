---
title: Choose a deployment model
description: Choose tenancy, security, bootstrap, TLS, installation, and upgrade contracts for the environment.
eyebrow: Get started · Step 1
weight: 1
verifiedBy:
  - charts/openbao-operator/values.yaml
  - api/v1alpha1/openbaocluster_types.go
  - api/v1alpha1/openbaocluster_networking_types.go
  - api/v1alpha1/openbaocluster_selfinit_types.go
  - api/v1alpha1/openbaocluster_operations_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
aliases:
  - /latest/user-guide/deployment-decision-guide/
---

## Decide the tenancy model

| Model | Use it when | Control-plane behavior | Namespace handoff |
| --- | --- | --- | --- |
| Multi-tenant | A platform team operates OpenBao for one or more namespaces | Runs the controller and Provisioner | Every target namespace is introduced through `OpenBaoTenant` |
| Single-tenant | One team owns one operator and one namespace | Runs only the controller with `WATCH_NAMESPACE` | A direct target RoleBinding replaces `OpenBaoTenant` |

The Helm chart defaults to multi-tenant mode. This is an implementation default, not a claim that every production
deployment must be shared. Use [single-tenant mode](../single-tenant/) when its dedicated ownership boundary fits.

## Choose the cluster contract

| Decision | Evaluation | Production recommendation | Actual default or requirement |
| --- | --- | --- | --- |
| Security profile | `Development` | `Hardened` | `spec.profile` is required and has no default |
| Replicas | One or more | At least three voters | API defaults to three; `Hardened` rejects fewer than three |
| TLS | `OperatorManaged` | `External` or `ACME` | TLS mode defaults to `OperatorManaged`; `Hardened` rejects it |
| Unseal | Static auto-unseal | External KMS, transit, KMIP, OCI KMS, or PKCS#11 | Omitted unseal defaults to static |
| Initialization | Operator-managed evaluation flow | Self-init with operator and human authentication | Self-init defaults to disabled; `Hardened` requires it |
| Upgrade | `RollingUpdate` | Start with `RollingUpdate`; choose `BlueGreen` for controlled cutover | Rolling update is the API default |
| Admission | Leave enabled | Leave enabled | Chart default is enabled |

Do not create a `Hardened` cluster until all required fields are complete. Admission requires External or ACME TLS,
non-static unseal, enabled self-init, and at least three replicas. Self-init also requires a non-empty request list.

## Choose the installation owner

| Install path | Use it when | Primary verification |
| --- | --- | --- |
| Helm | The platform wants a released, configurable lifecycle | Pinned chart version, release namespace, Deployments, CRDs, admission policies |
| Pinned release manifest | The platform wants the published default resources without Helm | Exact release URL and resulting default identities |
| Kustomize overlay | Namespace, identity, or single-tenant wiring must be customized together | Rendered ServiceAccounts, RoleBindings, admission variables, and environment |
| Source deployment | Local development or contribution only | Built image, generated resources, and development namespace |

Use Helm multi-tenant mode for the core guide. The 0.5.x chart also supports the dedicated single-tenant path: it
sets `WATCH_NAMESPACE` from `tenancy.targetNamespace` and renders the matching target RoleBinding without a
Provisioner.

## Separate authentication decisions

Operator lifecycle access and human login are different contracts:

- `spec.selfInit.oidc.enabled` bootstraps projected-JWT authentication for the controller and lifecycle Jobs.
- `spec.selfInit.requests` must create at least one usable human authentication path before the root token is revoked.
- Existing clusters own later OpenBao policy changes; self-init does not continuously reconcile them.

Review [operator authentication](../operator-authentication/) and [operator authorization](../operator-authorization/)
before a production bootstrap or any custom controller identity.

## Decide whether BlueGreen is justified

Start with `RollingUpdate` when sequential Pod replacement and lower resource overhead are acceptable. Choose
`BlueGreen` when parallel validation, manual promotion, or a stronger cutover boundary justifies the extra workloads,
PVCs, states, and peer-management authority.

The operator can switch either strategy on an existing healthy, idle cluster. The strategy change must be a separate
request from any version, image, replica, storage, or restart change. See
[prepare for production operations](../prepare-day-2/#switch-an-idle-upgrade-strategy).

Continue with [installation](../install/) only when these choices are explicit.
