---
title: Tenant boundaries
description: Verify how multi-tenant mode separates namespace introduction, cluster discovery, namespaced mutation, and tenant-user access.
eyebrow: Security · Multi-tenant
weight: 2
verifiedBy:
  - api/v1alpha1/openbaotenant_types.go
  - internal/app/provisioner/tenant_reconcile.go
  - internal/controller/openbaocluster/onboarding_runtime.go
  - internal/controller/openbaocluster/setup.go
  - internal/service/provisioner/manager_tenant.go
  - internal/service/provisioner/manager_secret_rbac.go
  - internal/service/provisioner/manager_cleanup.go
  - internal/service/provisioner/rbac.go
  - internal/service/networking/manager.go
  - config/rbac/openbaocluster_admin_role.yaml
  - config/rbac/openbaocluster_editor_role.yaml
  - config/rbac/openbaotenant_editor_role.yaml
  - charts/openbao-operator/templates/rbac
  - charts/openbao-operator/templates/admission/validate-openbao-tenant.yaml
---

Multi-tenant mode separates the identity that grants namespace access from the controller that uses that access. The
boundary limits routine workload mutation to namespaces introduced through `OpenBaoTenant`; it does not make a shared
Kubernetes cluster a complete security boundary by itself.

Use [single-tenant mode](../../get-started/single-tenant/) when one team owns one operator and one namespace and does
not need the Provisioner handoff.

## Understand the authority split

| Actor | Cluster-wide authority | Namespaced authority |
| --- | --- | --- |
| Provisioner | Watches `OpenBaoTenant` and applies the approved tenant-governance resources | Creates tenant RBAC, Secret-access roles, quota, LimitRange, and optional Pod Security labels |
| Controller | Gets, lists, and watches `OpenBaoCluster` and `OpenBaoRestore` resources for discovery | Mutates workloads and supporting resources through Roles bound only in onboarded namespaces |
| Tenant author | Depends on how a platform administrator binds the supplied ClusterRoles | Can manage tenant or cluster intent in the namespace without receiving broad Secret access |
| Kubernetes administrator | Can alter CRDs, admission, RBAC, namespaces, and operator workloads | Remains trusted outside the operator's tenant boundary |

The controller is not fully namespace-scoped: it discovers operator custom resources cluster-wide. Its workload
permissions are namespace-scoped because multi-tenant mode does not grant cluster-wide mutation of StatefulSets,
Services, Pods, Jobs, Secrets, or the other generated resources.

## Use the onboarding handoff

`OpenBaoTenant` names an existing target namespace. The Provisioner creates
`openbao-operator-tenant-rolebinding`, which binds the controller ServiceAccount to the generated tenant Role. The
cluster controller treats that RoleBinding as the handoff marker and pauses until it exists.

Admission and runtime checks enforce two onboarding paths:

- A self-service request lives in its target namespace, targets only that namespace, and uses the platform defaults.
- A centrally managed request lives in the rendered operator namespace and can target another namespace or override
  its quota and LimitRange.

Apply `OpenBaoTenant` and `OpenBaoCluster` in the same GitOps synchronization when needed. Reconciliation requeues
until the handoff is ready instead of treating that ordering as a permanent failure. Follow the
[namespace-onboarding procedure](../../get-started/onboard-namespace/) for manifests and status checks.

## Know which controls onboarding creates

| Control | Boundary | Important limitation |
| --- | --- | --- |
| Tenant Role and RoleBinding | Grants the controller namespaced workload-management permissions | The RoleBinding is not a grant to tenant users |
| Secret reader Role | Grants `get` only for Secret names referenced by tenant resources | The Role is removed when no referenced Secrets remain |
| Secret writer Role | Grants name-scoped get, update, patch, and delete for operator-owned Secrets | Kubernetes RBAC cannot restrict `create` by `resourceNames`, so Secret creation is collection-scoped |
| ResourceQuota and LimitRange | Establishes default namespace resource ceilings and container defaults | Centrally managed onboarding can replace the defaults |
| Pod Security labels | Sets `enforce`, `audit`, and `warn` to `restricted` by default | `external` mode delegates only label ownership |
| NetworkPolicy | Created during `OpenBaoCluster` reconciliation | It is not evidence that tenant onboarding completed, and enforcement depends on the cluster network implementation |

The base tenant Role contains no Secret permissions. The Provisioner derives separate reader and writer allowlists
from the `OpenBaoCluster` and `OpenBaoRestore` specifications in that namespace and removes empty allowlist roles.

## Bind tenant users deliberately

The supplied user-facing roles are all `ClusterRole` objects. A `ClusterRole` defines reusable rules; the binding
determines their effective scope. Use a `RoleBinding` for one tenant namespace. Reserve a `ClusterRoleBinding` for an
identity that intentionally needs the permissions across all namespaces.

| Purpose | Raw-manifest name | Helm name suffix | Permissions |
| --- | --- | --- | --- |
| Cluster administration | `openbaocluster-admin-role` | `openbaocluster-admin` | All verbs on `OpenBaoCluster`; read its status |
| Cluster editing | `openbaocluster-editor-role` | `openbaocluster-editor` | Create, read, update, patch, delete, list, and watch `OpenBaoCluster`; read its status |
| Self-service onboarding | `openbaotenant-editor-role` | `openbaotenant-editor` | Manage `OpenBaoTenant`; read its status |

Helm prefixes the suffixes with the rendered release name. Inspect the rendered ClusterRole name before creating a
binding. The cluster-administration role administers `OpenBaoCluster`; it does not itself grant permission to create
or modify Kubernetes RBAC.

Protected operations such as restore, network publication, custom executable use, cloud identity, and image trust
roots require separate delegated verbs. Do not add those permissions to the ordinary editor binding for convenience.

## Verify the effective boundary

1. Verify the onboarding handoff and generated controls.

   {{< command label="inspect" title="Inspect the tenant namespace" >}}
   kubectl -n <request-namespace> get openbaotenant <name> \
     -o jsonpath='{.status.provisioned}{"\n"}'
   kubectl -n <target-namespace> get \
     role,rolebinding,resourcequota,limitrange \
     -l app.kubernetes.io/managed-by=openbao-operator
   {{< /command >}}

   The first command must print `true`. Confirm that the controller ServiceAccount is the subject of
   `openbao-operator-tenant-rolebinding`.

2. Test the tenant user's actual binding.

   {{< command label="verify" title="Check tenant-user permissions" >}}
   kubectl auth can-i create openbaoclusters.openbao.org \
     -n <target-namespace> --as <tenant-user>
   kubectl auth can-i get secrets \
     -n <target-namespace> --as <tenant-user>
   {{< /command >}}

   An ordinary cluster editor should receive `yes` for cluster creation and `no` for broad Secret reads. Test the
   real group or ServiceAccount identity as well when bindings use groups or workload identities.

3. After creating an `OpenBaoCluster`, verify the workload network boundary separately.

   {{< command label="inspect" title="Inspect cluster network policy" >}}
   kubectl -n <target-namespace> get networkpolicy
   {{< /command >}}

   Confirm that the installed CNI enforces NetworkPolicy and test the allowed ingress and egress paths. Object-storage
   credentials and backup prefixes also need tenant-specific isolation outside Kubernetes RBAC.

## Remove a tenant safely

`spec.targetNamespace` is immutable. Deleting `OpenBaoTenant` waits until every `OpenBaoCluster` in the target
namespace is gone. If no other active, authorized tenant claim targets that namespace, cleanup removes the controller
RoleBinding, tenant Role, Secret allowlist RBAC, and the operator-managed `ResourceQuota` and `LimitRange`. An active
claim preserves those shared resources while the deleting claim finalizes. Discovery and cleanup failures retain the
finalizer.

Tenant cleanup leaves Pod Security labels unchanged because the operator cannot restore their earlier state. Review
those labels explicitly before reusing or deleting the namespace. Cluster-owned NetworkPolicies follow the cluster
lifecycle, not the tenant finalizer.

Continue with the [threat model](../threat-model/) to review the cluster-administrator, supply-chain, OpenBao, and
external-service boundaries that tenancy does not cover.
