---
title: OpenBaoTenant API
description: Fields, defaults, and validation for the OpenBaoTenant API.
eyebrow: Reference · Generated API
weight: 3
verifiedBy:
  - api/v1alpha1 at HEAD
  - website/generated/api-reference.md at HEAD
---

{{< callout type="note" title="Generated reference" >}}

This page is synchronized from the generated API reference at `HEAD` for the `next` documentation line.
{{< /callout >}}


## Packages
- [openbao.org/v1alpha1](#openbaoorgv1alpha1)


## openbao.org/v1alpha1

Package v1alpha1 contains API Schema definitions for the openbao v1alpha1 API group.

### Resource Types
- [OpenBaoTenant](#openbaotenant)



#### OpenBaoTenant



OpenBaoTenant is the Schema for the openbaotenants API.
OpenBaoTenant is a governance CRD that explicitly declares which namespace
should be provisioned with tenant RBAC. This replaces the previous label-based
approach (openbao.org/tenant=true) to improve security by eliminating the need
for the Provisioner to have list/watch permissions on namespaces.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `openbao.org/v1alpha1` | | |
| `kind` _string_ | `OpenBaoTenant` | | |
| `spec` _[OpenBaoTenantSpec](#openbaotenantspec)_ |  |  |  |
| `status` _[OpenBaoTenantStatus](#openbaotenantstatus)_ |  |  |  |


#### OpenBaoTenantSpec



OpenBaoTenantSpec defines the desired state of OpenBaoTenant.



_Appears in:_
- [OpenBaoTenant](#openbaotenant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `targetNamespace` _string_ | TargetNamespace is the name of the namespace to provision with tenant RBAC.<br />The Provisioner will create Role and RoleBinding resources in this namespace<br />to grant the OpenBaoCluster controller permission to manage OpenBaoCluster<br />resources in that namespace. |  | MinLength: 1 <br /> |
| `quota` _[ResourceQuotaSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#resourcequotaspec-v1-core)_ | Quota defines the resource quota to apply to the tenant namespace. |  | Optional: \{\} <br /> |
| `limitRange` _[LimitRangeSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#limitrangespec-v1-core)_ | LimitRange defines the limit range to apply to the tenant namespace. |  | Optional: \{\} <br /> |


#### OpenBaoTenantStatus



OpenBaoTenantStatus defines the observed state of OpenBaoTenant.



_Appears in:_
- [OpenBaoTenant](#openbaotenant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `provisioned` _boolean_ | Provisioned indicates if the RBAC has been successfully applied to the target namespace. |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError reports any issues finding the namespace or applying RBAC. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represent the latest available observations of the tenant's state. |  | Optional: \{\} <br /> |
