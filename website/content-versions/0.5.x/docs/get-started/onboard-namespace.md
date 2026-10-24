---
title: Onboard a namespace
description: Authorize the controller to manage an existing namespace and verify the tenant guardrails and RBAC handoff.
eyebrow: Get started · Step 3
weight: 3
verifiedBy:
  - api/v1alpha1/openbaotenant_types.go
  - charts/openbao-operator/templates/admission/validate-openbao-tenant.yaml
  - internal/app/provisioner/tenant_reconcile.go
  - internal/service/provisioner/manager_tenant.go
  - internal/service/provisioner/manager_secret_rbac.go
  - internal/service/provisioner/manager_cleanup.go
  - internal/service/provisioner/quotas.go
  - internal/controller/openbaocluster/onboarding_runtime.go
---

`OpenBaoTenant` authorizes the controller to manage an existing namespace and applies tenant guardrails. It does not
create the namespace or an `OpenBaoCluster`.

Skip this page only when the operator uses the verified [single-tenant mode](../single-tenant/).
Review [tenant boundaries](../../security/tenant-boundaries/) when you need the authority model behind this handoff.

{{< callout type="note" title="Check namespace Pod Security label ownership first" >}}
If your platform manages or restricts Pod Security labels, ask the operator administrator to configure
[`tenancy.namespacePodSecurityLabels.mode=external`](../install/#choose-namespace-pod-security-label-ownership)
before you create `OpenBaoTenant`. This operator-wide Helm setting leaves label management to the platform.
{{< /callout >}}

## Choose the onboarding owner

| Model | Where to create `OpenBaoTenant` | Allowed customization |
| --- | --- | --- |
| Self-service | The target namespace | `metadata.namespace` must equal `spec.targetNamespace`; uses default quota and LimitRange |
| Centrally managed | The rendered operator namespace | A platform administrator can target another namespace and set custom quota or LimitRange |

The target namespace must already exist. `spec.targetNamespace` is required and admission treats it as immutable.

## Onboard through self-service

1. Create the target namespace if it does not exist.

   {{< command label="apply" title="Create the evaluation namespace" >}}
   kubectl create namespace openbao-demo
   {{< /command >}}

2. Save this request as `tenant.yaml`.

   {{< command label="configure" title="Declare a self-service tenant" >}}
   apiVersion: openbao.org/v1alpha1
   kind: OpenBaoTenant
   metadata:
     name: openbao-demo
     namespace: openbao-demo
   spec:
     targetNamespace: openbao-demo
   {{< /command >}}

3. Apply the request.

   {{< command label="apply" title="Onboard the namespace" >}}
   kubectl apply -f tenant.yaml
   {{< /command >}}

4. Verify that provisioning completed.

   {{< command label="verify" title="Verify the tenant handoff" >}}
   kubectl -n openbao-demo wait \
     --for=condition=Provisioned \
     openbaotenant/openbao-demo \
     --timeout=2m
   kubectl -n openbao-demo get openbaotenant openbao-demo \
     -o jsonpath='{.status.provisioned}{"\t"}{.status.conditions[?(@.type=="Provisioned")].status}{"\n"}'
   kubectl -n openbao-demo get rolebinding \
     openbao-operator-tenant-rolebinding
   {{< /command >}}

   The wait command must report `condition met`. The status command must print `true` and `True`. The condition carries
   the current generation, reason, and message; the RoleBinding is the concrete handoff that allows cluster
   reconciliation to continue.

## Onboard centrally

Create the request in the actual rendered operator namespace when a platform administrator manages onboarding for a
team:

{{< command label="configure" title="Declare a centrally managed tenant" >}}
apiVersion: openbao.org/v1alpha1
kind: OpenBaoTenant
metadata:
  name: team-a
  namespace: openbao-operator-system
spec:
  targetNamespace: team-a-prod
  # Optional platform-managed values:
  # quota:
  # limitRange:
{{< /command >}}

The trusted namespace is the rendered operator namespace, not a fixed project-wide value. If the release uses another
namespace, change `metadata.namespace` accordingly.

## Understand the applied resources

Onboarding manages:

- the namespace-scoped controller Role and `openbao-operator-tenant-rolebinding`;
- Secret allowlist Roles used by operator-managed workloads and Jobs;
- a ResourceQuota and LimitRange;
- restricted Pod Security `enforce`, `audit`, and `warn` labels by default.

It does not create workload NetworkPolicies. Those are reconciled after an `OpenBaoCluster` is accepted.
The explicit `OpenBaoTenant` request also means the Provisioner does not need to discover tenant namespaces through a
cluster-wide namespace label watch.

The default quota allows 50 Pods, 20 requested CPU, 64 GiB requested memory, 40 CPU limit, and 128 GiB memory limit.
The default per-container LimitRange is:

| Resource | Default request | Default limit |
| --- | --- | --- |
| CPU | 100m | 500m |
| Memory | 128Mi | 512Mi |

## Apply tenant and cluster together

GitOps can apply `OpenBaoTenant` and `OpenBaoCluster` in one synchronization. The cluster controller pauses until
`openbao-operator-tenant-rolebinding` exists, then continues without treating the missing handoff as a permanent
failure. Watching the tenant status first still gives the clearest rollout signal.

## Troubleshoot onboarding

| Symptom | Likely cause | Check first |
| --- | --- | --- |
| Security violation | A self-service request targets another namespace | `metadata.namespace` and `spec.targetNamespace` |
| `status.provisioned` stays false with namespace-not-found | The target namespace does not exist | Namespace name and platform provisioning |
| Cluster keeps waiting after the tenant is true | The handoff RoleBinding is absent or wrong | `openbao-operator-tenant-rolebinding` and its subject |
| Namespace label update is unauthorized | A platform controller or admission policy restricts Pod Security label updates | Inspect `status.lastError` and follow [external label ownership configuration](../install/#choose-namespace-pod-security-label-ownership) |
| Custom quota is ignored | The request used self-service | Create the request in the operator namespace through the platform path |
| Admission dependency errors | Required policies or bindings are not ready | Operator installation and admission-policy status |

For a rejected label update, inspect the tenant's error and conditions:

{{< command label="inspect" title="Inspect tenant provisioning errors" >}}
kubectl -n <request-namespace> get openbaotenant <name> -o yaml
{{< /command >}}

If the error identifies a platform restriction on Pod Security label updates, ask the operator administrator to
re-render the installation with `tenancy.namespacePodSecurityLabels.mode=external` and apply the updated resources.
Preserve the installation's other values and keep the required namespace policy under platform control. After the
Provisioner restarts, repeat the tenant verification step above. The `Provisioned` condition must become `True`;
inspect its message if it remains `False`.

## Remove or retarget a tenant

You cannot change `spec.targetNamespace` in place. Before deleting an `OpenBaoTenant`, delete every
`OpenBaoCluster` in the target namespace; the finalizer waits until they are gone.

When no other active, authorized `OpenBaoTenant` claim targets the namespace, cleanup removes tenant RBAC and the
operator-managed `ResourceQuota` and `LimitRange` before removing the finalizer. Another active claim preserves those
shared resources while the deleting claim finalizes. Claim discovery and cleanup fail closed: an error keeps the
finalizer in place.

Pod Security labels remain unchanged because the operator does not retain the namespace's earlier label state. Review
them explicitly when decommissioning or retargeting the namespace.

Continue with [cluster creation](../create-cluster/).
