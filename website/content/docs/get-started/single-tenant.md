---
title: Install in single-tenant mode
description: Run one controller for one existing namespace with Helm or the Kustomize overlay.
eyebrow: Get started · Supporting decision
weight: 6
verifiedBy:
  - cmd/controller/startup_helpers.go
  - charts/openbao-operator/templates/controller/deployment.yaml
  - charts/openbao-operator/templates/rbac/single-tenant-clusterrole.yaml
  - hack/helmchart/main_test.go
  - config/overlays/single-tenant/kustomization.yaml
  - config/overlays/single-tenant/target_namespace_config.yaml
  - config/overlays/single-tenant/target_namespace_rolebinding.yaml
  - config/overlays/single-tenant-custom-identity/kustomization.yaml
  - test/integration/kustomize_contract_test.go
---

Single-tenant mode runs only the controller and limits its workload permissions to one existing namespace. It does
not run the Provisioner or use `OpenBaoTenant`.

## Choose single-tenant mode

| Use multi-tenant mode when | Use single-tenant mode when |
| --- | --- |
| A platform team operates OpenBao for several namespaces. | One team owns one operator and one target namespace. |
| Namespace access must pass through `OpenBaoTenant`. | The team does not need the tenant-onboarding workflow. |
| The controller discovers clusters across the platform and receives workload permissions only in onboarded namespaces. | The controller watches one namespace through `WATCH_NAMESPACE`. |

Single-tenant mode reduces the shared-platform machinery. It also gives the dedicated controller direct permissions
in the target namespace, so the platform must own that RoleBinding explicitly.

## Install with Helm

Use Helm for a released 0.5.x installation.

1. Create the target namespace through the platform's normal workflow. The chart must create a RoleBinding in this
   namespace during installation.

   {{< command label="apply" title="Create the target namespace" >}}
   kubectl create namespace openbao
   {{< /command >}}

2. Install the operator.

   {{< command label="apply" title="Install a single-tenant operator" >}}
   helm upgrade --install openbao-operator \
     oci://ghcr.io/kubebao/charts/openbao-operator \
     --version 0.5.2 \
     --namespace openbao-operator-system \
     --create-namespace \
     --set tenancy.mode=single \
     --set tenancy.targetNamespace=openbao
   {{< /command >}}

When `tenancy.targetNamespace` is omitted, the chart watches its release namespace and `--create-namespace` creates
that namespace.

3. Verify that the rendered Deployment contains `WATCH_NAMESPACE=openbao`, the target RoleBinding is in `openbao`, and
   no Provisioner resources exist. Custom release names or `fullnameOverride` values change the controller identity;
   keep manually managed JWT roles aligned with the rendered ServiceAccount.

   {{< command label="verify" title="Verify single-tenant scope" >}}
   kubectl -n openbao-operator-system rollout status \
     deployment/openbao-operator-controller --timeout=2m
   kubectl -n openbao-operator-system get deployment \
     openbao-operator-controller \
     -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="WATCH_NAMESPACE")].value}{"\n"}'
   kubectl -n openbao get rolebinding openbao-operator-single-tenant
   kubectl -n openbao-operator-system get deployment
   {{< /command >}}

   The JSONPath command must print `openbao`. The Deployment list must contain the controller and no Provisioner.

## Install with Kustomize

1. Create the operator and target namespaces through your normal platform workflow.

   {{< command label="apply" title="Create the example namespaces" >}}
   kubectl create namespace openbao-operator-system
   kubectl create namespace openbao
   {{< /command >}}

2. Obtain the operator source for the version you intend to install.

   {{< command label="configure" title="Clone a pinned operator release" >}}
   git clone --branch 0.5.2 --depth 1 \
     https://github.com/kubebao/openbao-operator.git
   cd openbao-operator
   {{< /command >}}

3. Set the operator namespace in `config/overlays/single-tenant/kustomization.yaml`.

   The shipped value is `openbao-operator-system`. Change both the `namespace` field and the namespace resource when
   your platform uses another namespace.

4. Set `data.WATCH_NAMESPACE` in `config/overlays/single-tenant/target_namespace_config.yaml`.

   The overlay uses this value for both the controller environment and the target RoleBinding namespace. The shipped
   value is `openbao`.

5. Render the overlay before applying it.

   {{< command label="inspect" title="Render the single-tenant install" >}}
   kubectl kustomize config/overlays/single-tenant
   {{< /command >}}

   Confirm that:

   - the controller Deployment contains the intended `WATCH_NAMESPACE`;
   - the `openbao-operator-single-tenant` RoleBinding is in that namespace;
   - the RoleBinding subject names the rendered controller ServiceAccount and operator namespace;
   - no Provisioner Deployment, ServiceAccount, Service, or binding remains.

6. Apply the overlay.

   {{< command label="apply" title="Install the single-tenant operator" >}}
   kubectl apply -k config/overlays/single-tenant
   {{< /command >}}

7. Verify the controller and namespace scope.

   {{< command label="verify" title="Verify single-tenant mode" >}}
   kubectl -n openbao-operator-system rollout status \
     deployment/openbao-operator-controller --timeout=2m
   kubectl -n openbao-operator-system get deployment \
     openbao-operator-controller \
     -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="WATCH_NAMESPACE")].value}{"\n"}'
   kubectl -n openbao get rolebinding openbao-operator-single-tenant
   {{< /command >}}

   The JSONPath command must print the target namespace. No Provisioner Pod must be running.

## Customize the Kustomize controller identity

Use `config/overlays/single-tenant-custom-identity` when you also need a custom namespace or `namePrefix`. That overlay
updates the controller ServiceAccount, target RoleBinding, controller environment, and admission-policy identity
variables together.

Render the overlay and verify every identity reference before applying it:

{{< command label="inspect" title="Render the custom-identity variant" >}}
kubectl kustomize config/overlays/single-tenant-custom-identity
{{< /command >}}

Also update any manually managed OpenBao JWT role so its `bound_subject` matches the rendered ServiceAccount. See
[operator authentication](../operator-authentication/).

## Change tenancy modes carefully

- Before moving from multi-tenant to single-tenant, remove every `OpenBaoTenant` dependency and verify the new direct
  RoleBinding before removing the Provisioner.
- Before moving from single-tenant to multi-tenant, onboard the namespace and verify the tenant handoff before removing
  the direct single-tenant RoleBinding.
- Do not leave both authorization models in place. Stale RoleBindings can preserve authority that the new model did
  not intend.

After verification, [create the cluster](../create-cluster/) in the watched namespace. Skip the onboarding step.
