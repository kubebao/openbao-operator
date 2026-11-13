---
title: Tenant provisioning
description: The namespace-scoped RBAC, Secret access, policy defaults, and handoff established by OpenBaoTenant.
eyebrow: Day 0
weight: 4
verifiedBy:
  - internal/controller/provisioner/controller.go
  - internal/controller/provisioner/namespace_provisioner_integration_test.go
  - internal/controller/provisioner/tenant_secrets_rbac_controller_test.go
  - internal/app/provisioner/tenant_reconcile.go
  - internal/app/provisioner/tenant_reconcile_test.go
  - internal/service/provisioner/manager_tenant.go
  - internal/service/provisioner/manager_cleanup.go
  - internal/service/provisioner/manager_test.go
  - internal/service/provisioner/rbac_test.go
  - internal/service/provisioner/quotas_test.go
---

`OpenBaoTenant` establishes the namespace boundary before an `OpenBaoCluster` controller writes into a tenant namespace.
The provisioning controller uses its own identity and delegates through `internal/app/provisioner` to
`internal/service/provisioner`.

## Provision the boundary

The application validates who may target the namespace, waits for admission dependencies, and invokes the provisioner
service. The service reconciles:

- a namespaced Role and RoleBinding for the operator controller identity;
- separate Secret reader and writer Roles and RoleBindings derived from current cluster and restore references;
- restricted Pod Security enforce, audit, and warn labels when the provisioner owns namespace labels;
- optional `ResourceQuota` and `LimitRange` objects from `OpenBaoTenant.spec`.

The core tenant Role does not grant Secret access. The writer role can create Secrets and mutate only named
operator-owned Secrets. The reader role can read only named user-provided Secrets. A separate Secret-RBAC reconciler
updates these allowlists as cluster and restore references change and removes the roles when no names remain.

## Enforce the targeting rule

A self-service `OpenBaoTenant` may target its own namespace. Cross-namespace targeting is accepted only when the tenant
request is created in the trusted operator namespace. Rejected targeting is recorded in status and is not retried until
the request changes.

The target namespace must already exist. Provisioning does not create namespaces.

## Wait for the handoff marker

The core tenant RoleBinding is the readiness marker for workload, AdminOps, and status controllers. In multi-tenant mode,
those controllers requeue before finalizer, status, or workload mutation until the RoleBinding exists.

This makes a GitOps submission containing both `OpenBaoTenant` and `OpenBaoCluster` deterministic: the cluster may exist,
but its controllers do not cross the namespace boundary before provisioning completes.

## Assign namespace policy ownership

| Mode | Pod Security label owner |
| --- | --- |
| `enforce` | The provisioner writes `restricted` enforce, audit, and warn labels and treats update denial as a provisioning failure. This is the default when the mode is unset. |
| `external` | The surrounding platform owns namespace Pod Security labels; the provisioner does not update them. |

Quota and limit-range objects are optional and are reconciled only when their corresponding spec fields are present.
They do not expand the tenant Role, which deliberately excludes quota mutation permissions.

## Clean up without stranding clusters

`OpenBaoTenant` has a finalizer. During deletion, the application keeps tenant RBAC while any `OpenBaoCluster` remains in
the target namespace so cluster finalizers can still run. After the last cluster is gone, it checks for another active,
authorized `OpenBaoTenant` claim on the namespace. A remaining claim preserves the shared tenant resources while the
deleting claim finalizes.

Without another claim, cleanup deletes the core tenant Role and RoleBinding, both Secret allowlist Role and RoleBinding
pairs, and the operator-managed `ResourceQuota` and `LimitRange` before removing the tenant finalizer. Claim discovery
and resource deletion fail closed, so an error retains the finalizer. Namespace Pod Security labels remain unchanged
because the Provisioner does not retain enough prior state to restore platform-owned labels safely.

{{< callout type="note" title="Provisioning does not discover tenant namespaces" >}}
Tenants are onboarded through explicit `OpenBaoTenant` requests. The provisioner does not need broad namespace list or
watch permission to find them.
{{< /callout >}}
