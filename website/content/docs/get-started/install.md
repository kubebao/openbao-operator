---
title: Install the operator
description: Install OpenBao Operator 0.5.3 with the intended tenancy, platform, identity, CRDs, and admission policy contract.
eyebrow: Get started · Step 2
weight: 2
verifiedBy:
  - charts/openbao-operator/Chart.yaml
  - charts/openbao-operator/values.yaml
  - charts/openbao-operator/templates/controller/deployment.yaml
  - charts/openbao-operator/templates/provisioner/deployment.yaml
  - charts/openbao-operator/templates/rbac/provisioner-clusterroles.yaml
  - charts/openbao-operator/templates/admission
  - internal/service/provisioner/manager_tenant.go
  - config/default/kustomization.yaml
  - config/manager/kustomization.yaml at 0.5.3
  - config/manager/controller.yaml at 0.5.3
  - config/manager/provisioner.yaml at 0.5.3
  - config/overlays/custom-identity/kustomization.yaml
  - internal/adapter/config/builder.go
  - cmd/bao-backup/restore_flow.go
  - .github/workflows/release.yml
---

Install a pinned 0.5.3 release and verify the rendered namespace, identities, controllers, CRDs, and admission policies.
The core procedure uses Helm in the chart's default multi-tenant mode. New 0.5.3 artifacts use KubeBao coordinates.
Historical 0.5.1 artifacts retain their `dc-tec` publisher identity.

## Before you begin

- Confirm that the cluster meets the [compatibility requirements](../../reference/compatibility/). Kubernetes 1.33
  satisfies the chart constraint, while current release gates validate Kubernetes 1.34, 1.35, and 1.36.
- Use an identity that can create cluster-scoped CRDs, RBAC, and ValidatingAdmissionPolicies.
- Decide whether Helm, a release manifest, or a maintained Kustomize overlay owns future upgrades.
- Decide the tenancy model. Use the separate [single-tenant procedure](../single-tenant/) for one watched namespace.
- If the cluster cannot pull from public registries, [mirror runtime images and configure registry credentials](../../configure/air-gapped/)
  before installing the operator.

## Choose an installation path

| Requirement | Path | What to verify |
| --- | --- | --- |
| Standard multi-tenant installation | Helm | Controller, Provisioner, CRDs, policies, and release identity |
| Default resources without Helm | Pinned `install.yaml` release asset | Published version and default namespace or identity |
| Custom namespace or prefix | `config/overlays/custom-identity` | Every subject and admission identity after rendering |
| Dedicated single namespace | Helm `tenancy.mode=single` or `config/overlays/single-tenant` | Controller-only runtime and `WATCH_NAMESPACE` |
| OpenShift | Helm with `platform=openshift`, or auto-detection | SCC-compatible workload security context |
| Local development | Source deployment | Development image and generated resources |

## Choose namespace Pod Security label ownership

In multi-tenant mode, the Provisioner sets the namespace labels `pod-security.kubernetes.io/enforce`,
`pod-security.kubernetes.io/audit`, and `pod-security.kubernetes.io/warn` to `restricted` by default.

If a platform controller owns these labels or an admission policy restricts their updates, configure external
ownership before onboarding a namespace. Add this fragment to the operator's Helm values file:

```yaml
tenancy:
  namespacePodSecurityLabels:
    mode: external
```

This setting applies to all tenant namespaces served by the installation; it is not an `OpenBaoTenant` field.
The chart removes namespace update and patch permissions from the Provisioner and configures admission to deny its
namespace updates. Existing labels remain unchanged. The platform team must apply the required Pod Security policy.
The operator still manages tenant RBAC, Secret allowlists, ResourceQuota, and LimitRange.

Rancher is one example: its webhook requires separate authorization to update Pod Security labels. See
[Rancher's namespace admission guidance](https://ranchermanager.docs.rancher.com/reference-guides/rancher-webhook#application-fails-to-deploy-due-to-rancher-webhook-blocking-access).
Other platform controllers and admission policies can impose similar restrictions.

Use the Helm procedure below when you need this setting. The published `install.yaml` uses the default `enforce` mode;
Helm values do not configure that manifest.

## Install with Helm

1. Set the release values.

   {{< command label="configure" title="Set the 0.5.3 release values" >}}
   export OPERATOR_RELEASE=openbao-operator
   export OPERATOR_NAMESPACE=openbao-operator-system
   export CHART_VERSION=0.5.3
   {{< /command >}}

2. Inspect the chart defaults when the platform needs overrides.

   {{< command label="inspect" title="Read the pinned values" >}}
   helm show values \
     oci://ghcr.io/kubebao/charts/openbao-operator \
     --version "${CHART_VERSION}"
   {{< /command >}}

   Pin the chart and normally let its `appVersion` select the matching operator image. Set `image.tag` only for a
   controlled prerelease or test. The complete pinned reference is
   [`values.yaml`](https://github.com/kubebao/openbao-operator/blob/0.5.3/charts/openbao-operator/values.yaml).

3. Save your overrides in `operator-values.yaml`, including external label ownership when required.

   If you do not need overrides, create an empty values file:

   {{< command label="configure" title="Prepare the operator values file" >}}
   touch operator-values.yaml
   {{< /command >}}

4. Render the installation with the values file.

   {{< command label="inspect" title="Render the Helm release" >}}
   helm template "${OPERATOR_RELEASE}" \
     oci://ghcr.io/kubebao/charts/openbao-operator \
     --version "${CHART_VERSION}" \
     --namespace "${OPERATOR_NAMESPACE}" \
     --include-crds \
     --values operator-values.yaml
   {{< /command >}}

   Check the controller and Provisioner ServiceAccounts, RoleBinding subjects, admission-policy identity variables,
   projected token audience, images, and namespaces.

   `helm template` omits CRDs unless you pass `--include-crds`. When applying rendered manifests directly, create the
   operator namespace and wait for the CRDs to become `Established` before starting the controllers or applying
   OpenBao custom resources. Configure the same ordering when a GitOps controller applies the manifests.

5. Install the chart with the same values file.

   {{< command label="apply" title="Install OpenBao Operator" >}}
   helm upgrade --install "${OPERATOR_RELEASE}" \
     oci://ghcr.io/kubebao/charts/openbao-operator \
     --version "${CHART_VERSION}" \
     --namespace "${OPERATOR_NAMESPACE}" \
     --values operator-values.yaml \
     --create-namespace \
     --wait
   {{< /command >}}

6. Wait for both multi-tenant Deployments.

   {{< command label="verify" title="Verify the controller and Provisioner" >}}
   kubectl -n "${OPERATOR_NAMESPACE}" rollout status \
     deployment/openbao-operator-controller --timeout=2m
   kubectl -n "${OPERATOR_NAMESPACE}" rollout status \
     deployment/openbao-operator-provisioner --timeout=2m
   {{< /command >}}

7. Verify the installed APIs and admission policies.

   {{< command label="verify" title="Verify cluster-scoped resources" >}}
   kubectl get crd \
     openbaoclusters.openbao.org \
     openbaotenants.openbao.org \
     openbaorestores.openbao.org
   kubectl get validatingadmissionpolicies \
     -l app.kubernetes.io/instance="${OPERATOR_RELEASE}"
   {{< /command >}}

8. Verify the default controller JWT contract.

   {{< command label="inspect" title="Inspect the controller identity and audience" >}}
   kubectl -n "${OPERATOR_NAMESPACE}" get serviceaccount \
     openbao-operator-controller
   kubectl -n "${OPERATOR_NAMESPACE}" get deployment \
     openbao-operator-controller -o yaml
   {{< /command >}}

   Confirm that the Deployment uses the rendered ServiceAccount, mounts the projected `openbao-token`, and aligns its
   audience with `OPENBAO_JWT_AUDIENCE`. Continue with [operator authentication](../operator-authentication/) when you
   customize these values.

## Install the published manifest

Use the release asset when the platform wants the published default resources without a Helm release:

{{< command label="apply" title="Apply the 0.5.3 installer manifest" >}}
kubectl apply -f \
  https://github.com/kubebao/openbao-operator/releases/download/0.5.3/install.yaml
{{< /command >}}

The manifest uses the repository's default operator namespace and identity. Do not rewrite the rendered YAML by hand
for a custom identity; maintain a Kustomize overlay instead.

## Install with a custom raw identity

Use `config/overlays/custom-identity` when the platform owns a different operator namespace or `namePrefix`.
Obtain the pinned release source before changing the overlay:

{{< command label="configure" title="Clone the 0.5.3 release source" >}}
git clone --branch 0.5.3 --depth 1 \
  https://github.com/kubebao/openbao-operator.git openbao-operator-0.5.3
cd openbao-operator-0.5.3
{{< /command >}}

In `config/manager/kustomization.yaml`, replace `newName` and `newTag` in the existing `controller` image entry
with the values in this YAML fragment:

```yaml
images:
- name: controller
  newName: ghcr.io/kubebao/openbao-operator
  newTag: 0.5.3
```

Set the existing `OPERATOR_VERSION` environment entry to `0.5.3` in both `config/manager/controller.yaml` and
`config/manager/provisioner.yaml`. This value selects the matching helper images.

The tagged source contains placeholder image and version values until these entries are changed. Render the overlay
after setting both:

{{< command label="inspect" title="Render the custom identity" >}}
kubectl kustomize config/overlays/custom-identity
{{< /command >}}

Before applying the overlay, confirm:

1. Controller and Provisioner Deployments use `ghcr.io/kubebao/openbao-operator:0.5.3`.
2. Both Deployments set `OPERATOR_VERSION` to `0.5.3`.
3. Controller and Provisioner ServiceAccounts have the intended names and namespace.
4. RoleBinding and ClusterRoleBinding subjects point at those ServiceAccounts.
5. Admission-policy variables use the same namespace and ServiceAccount names.
6. `OPENBAO_JWT_AUDIENCE` matches the projected `openbao-token` audience.
7. The OpenBao JWT role's bound subject matches the rendered controller identity.

Apply only after the render is internally consistent:

{{< command label="apply" title="Apply the custom identity overlay" >}}
kubectl apply -k config/overlays/custom-identity
{{< /command >}}

## Install on OpenShift

The chart defaults to `platform=auto`. Set `platform=openshift` when the installation must force OpenShift behavior:

{{< command label="apply" title="Force OpenShift rendering" >}}
helm upgrade --install openbao-operator \
  oci://ghcr.io/kubebao/charts/openbao-operator \
  --version 0.5.3 \
  --namespace openbao-operator-system \
  --create-namespace \
  --set platform=openshift
{{< /command >}}

OpenShift mode omits fixed `runAsUser` and `fsGroup` IDs so the Security Context Constraint can assign namespace-scoped
IDs. Validate the result against the target cluster's SCC and admission configuration.

## Install from source for development

Use this path only for local development and contribution:

{{< command label="apply" title="Deploy a development image" >}}
make install
make deploy IMG=ghcr.io/kubebao/openbao-operator:dev
{{< /command >}}

## Upgrade the operator

The following procedure starts from 0.5.2. If you use 0.5.1, first follow the
[0.5.2 upgrade guidance](../../../releases/0.5.2/) for PROXY protocol changes and restore staging requirements.
If you use 0.5.0 or earlier, complete the [0.5.1 migration](../../../releases/0.5.1/) before those 0.5.2 steps.

Follow the [0.5.3 release procedure](../../../releases/0.5.3/) for helper Pod replacement and affected TLS certificates.
A successful Helm upgrade does not replace all existing helper binaries.

Helm does not upgrade installed CRDs. For every release with CRD changes, apply the release CRDs before the controller:

{{< callout type="warning" title="Update 0.4.2 restore policies before the controller" >}}
OpenBao Operator 0.4.2 generated restore policies with only `update` on
`sys/storage/raft/snapshot-force`. In 0.5.0 and later, a restore with `force` omitted or set to `false` uses
`sys/storage/raft/snapshot`. Add `update` on the normal endpoint through an authenticated administration path before
you upgrade the controller. Self-init does not update an existing policy.
{{< /callout >}}

{{< command label="upgrade" title="Upgrade to 0.5.3" >}}
kubectl apply -f \
  https://github.com/kubebao/openbao-operator/releases/download/0.5.3/crds.yaml
helm upgrade openbao-operator \
  oci://ghcr.io/kubebao/charts/openbao-operator \
  --version 0.5.3 \
  --namespace openbao-operator-system \
  --reuse-values \
  --set-string image.repository=ghcr.io/kubebao/openbao-operator \
  --set-string image.tag=0.5.3 \
  --set-string image.digest= \
  --set-string operatorVersion=0.5.3 \
  --wait
{{< /command >}}

The command selects the official 0.5.3 manager image and clears an older digest pin. If you use a private registry,
replace the repository and tag with your verified mirror. Update any `helperImages`, helper repository environment
variables, and cluster-level helper image overrides to the matching 0.5.3 images before upgrading.

Re-render custom identities and review OpenBao-side policy changes before upgrading. Self-init does not update existing
OpenBao policies.

## Uninstall the operator

{{< command label="remove" title="Remove the Helm release" >}}
helm uninstall openbao-operator --namespace openbao-operator-system
{{< /command >}}

{{< callout type="warning" title="Helm retains all three CRDs" >}}
Deleting the CRDs also deletes their custom resources from the Kubernetes API and can trigger or bypass lifecycle
expectations depending on what remains. Inventory every `OpenBaoCluster`, `OpenBaoTenant`, and `OpenBaoRestore` before a
separate CRD-deletion operation.
{{< /callout >}}

## Troubleshoot installation

| Symptom | Check |
| --- | --- |
| `no matches for kind "OpenBaoCluster"` | Verify that the CRDs are installed and `Established`. Include `--include-crds` when rendering the chart with `helm template`. |
| Controller starts but Provisioner is absent | Confirm that the chart did not render `tenancy.mode=single` |
| Tenant provisioning fails on a namespace label update | Inspect the tenant error and [configure label ownership](#choose-namespace-pod-security-label-ownership) if the platform restricts Pod Security label updates |
| Pods run but admission rejects ordinary resources | Inspect policy bindings, rendered identity variables, and the API server's ValidatingAdmissionPolicy support |
| Custom names break reconciliation | Compare every ServiceAccount, RoleBinding subject, admission variable, and JWT bound subject |
| OpenShift rejects Pod identity fields | Confirm auto-detection or set `platform=openshift`, then review SCC ownership |
| Helm upgrade leaves an old API schema | Apply the release `crds.yaml` before retrying the controller upgrade |

In multi-tenant mode, continue with [namespace onboarding](../onboard-namespace/).
