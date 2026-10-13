---
title: Install the operator
description: Evaluate unreleased main through the edge channel or deploy a development image from source.
eyebrow: Get started · Step 2
weight: 2
verifiedBy:
  - .github/workflows/ci.yml
  - .github/workflows/publish-edge.yml
  - hack/ci/generate-channel-manifests.sh
  - hack/ci/publish-edge-chart.sh
  - hack/tools/edge_chart
  - internal/platform/constants/images.go
  - charts/openbao-operator/values.yaml
  - charts/openbao-operator/templates/provisioner/deployment.yaml
  - charts/openbao-operator/templates/rbac/provisioner-clusterroles.yaml
  - charts/openbao-operator/templates/admission/provisioner-namespace-mutations.yaml
  - internal/service/provisioner/manager_tenant.go
  - config/default/kustomization.yaml
  - cmd/controller/startup_helpers.go
---

Next tracks unreleased behavior on `main`. Use the edge channel for an executable evaluation install, or build from
source when you are developing the operator. Do not treat either path as a stable production contract.

{{< callout type="warning" title="Use stable documentation for production" >}}
The edge channel advances after successful `main` validation. Each chart version identifies one verified candidate. Use the current stable documentation
and a pinned release for production. OpenBao Operator 0.5.0 is the current stable release.
{{< /callout >}}

## Before you begin

- Confirm that the cluster meets the [Next compatibility requirements](../../reference/compatibility/).
- Use an identity that can create cluster-scoped CRDs, RBAC, and ValidatingAdmissionPolicies.
- Install `kubectl`, Helm, `curl`, `jq`, and Cosign. Source deployments also require the repository toolchain and a
  registry the cluster can pull from.
- Decide the tenancy model. Use the [single-tenant procedure](../single-tenant/) for one watched namespace.
- If the cluster cannot pull from public registries, [mirror runtime images and configure registry credentials](../../configure/air-gapped/)
  before installing the operator.

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

The generated edge installer and default source deployment use `enforce` mode. Helm values do not configure those
manifests. Use the edge Helm chart or [local Helm rendering](#render-the-local-helm-contract) for external label ownership.

## Install an edge chart

Edge charts use `oci://ghcr.io/kubebao/charts-edge/openbao-operator`. This repository is not registered in Artifact Hub.
Each package contains the CRDs, RBAC, and admission policies from the candidate commit and pins the controller,
Provisioner, and default helper images to the verified image digests. OpenBao server images remain cluster configuration.

Use this procedure for a new Helm installation or an existing edge Helm release. It does not adopt an installation
created with the generated manifest. Store your Helm overrides in `operator-values.yaml`; use an empty mapping (`{}`)
if you do not need overrides.

1. Download the current channel metadata and inspect the candidate.

   {{< command label="inspect" title="Select an edge candidate" >}}
   curl --fail --silent --show-error \
     https://docs.kubebao.org/edge/latest/metadata.json \
     --output edge-metadata.json
   jq '{sha, chart, images}' edge-metadata.json
   export EDGE_CHART=oci://ghcr.io/kubebao/charts-edge/openbao-operator
   export EDGE_CHART_DIGEST="$(jq -er '.chart.digest' edge-metadata.json)"
   {{< /command >}}

   The chart version has the form `X.Y.Z-edge.<CI-run-id>.<attempt>.g<commit>`. Retain the metadata with your evaluation
   configuration. Pin the chart digest in GitOps so a new merge does not change the selected candidate.

2. Verify the chart publisher identity.

   {{< command label="verify" title="Verify the edge chart signature" >}}
   cosign verify --new-bundle-format=true \
     --certificate-identity \
       https://github.com/kubebao/openbao-operator/.github/workflows/publish-edge.yml@refs/heads/main \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     "${EDGE_CHART#oci://}@${EDGE_CHART_DIGEST}"
   {{< /command >}}

   Cosign must report successful verification. The channel's `provenance-index.json` also records the chart digest
   and GitHub attestation identity.

3. Apply the CRDs from the selected chart before installing or upgrading the controller.

   {{< command label="apply" title="Apply the matching edge CRDs" >}}
   helm show crds "${EDGE_CHART}@${EDGE_CHART_DIGEST}" > edge-crds.yaml
   kubectl apply --server-side --field-manager=openbao-operator-crds -f edge-crds.yaml
   {{< /command >}}

   Helm does not upgrade existing CRDs. Review any field ownership conflict before changing the existing CRD manager.
   An older controller or Helm rollback does not restore a previous CRD schema.

4. Install the selected chart and reapply your current overrides.

   {{< command label="apply" title="Install the pinned edge chart" >}}
   helm upgrade --install openbao-operator "${EDGE_CHART}@${EDGE_CHART_DIGEST}" \
     --namespace openbao-operator-system \
     --create-namespace \
     --reset-values \
     --values operator-values.yaml \
     --wait --timeout 5m
   {{< /command >}}

   Use `--reset-values` on edge upgrades so the new package supplies its image pins. Reapply intentional custom values
   from the values file. Avoid `--reuse-values`, which can retain image defaults from the previous candidate.
   Overriding `image` or `helperImages`, or setting helper image environment variables through `controller.extraEnv`,
   can replace the package's candidate image selection.

5. Verify both deployments in multi-tenant mode.

   {{< command label="verify" title="Verify the edge Helm installation" >}}
   kubectl -n openbao-operator-system rollout status deployment/openbao-operator-controller --timeout=2m
   kubectl -n openbao-operator-system rollout status deployment/openbao-operator-provisioner --timeout=2m
   kubectl -n openbao-operator-system get deployments \
     -o 'custom-columns=NAME:.metadata.name,IMAGE:.spec.template.spec.containers[0].image'
   {{< /command >}}

   Both deployments must use the manager digest in `edge-metadata.json`. Single-tenant mode runs only the controller.
   Continue with [namespace onboarding](../onboard-namespace/).

Published edge charts and their referenced images have no automatic expiry under the current retention policy.
They remain evaluation artifacts with no production support commitment. Nightly Helm charts are not published yet.

## Install the generated edge manifest

The edge publisher promotes images and generates manifests from the same successful `main` commit. Inspect the channel
metadata before applying it so you know the exact commit and image digests under evaluation.

1. Set the edge channel URL and inspect its metadata.

   {{< command label="inspect" title="Review the current edge build" >}}
   export EDGE_ROOT=https://docs.kubebao.org/edge/latest
   curl --fail --silent --show-error "${EDGE_ROOT}/metadata.json"
   {{< /command >}}

   Record the `sha`, generated time, and image digests. Follow the
   [supply-chain verification](../../security/supply-chain/) procedure when the evaluation requires provenance
   verification.

2. Apply the generated multi-tenant installer.

   {{< command label="apply" title="Install the edge manifest" >}}
   kubectl apply -f "${EDGE_ROOT}/install.yaml"
   {{< /command >}}

3. Wait for both multi-tenant controllers.

   {{< command label="verify" title="Verify the controller and Provisioner" >}}
   kubectl -n openbao-operator-system rollout status \
     deployment/openbao-operator-controller --timeout=2m
   kubectl -n openbao-operator-system rollout status \
     deployment/openbao-operator-provisioner --timeout=2m
   {{< /command >}}

4. Verify the installed APIs and admission policies.

   {{< command label="verify" title="Verify cluster-scoped resources" >}}
   kubectl get crd \
     openbaoclusters.openbao.org \
     openbaotenants.openbao.org \
     openbaorestores.openbao.org
   kubectl get validatingadmissionpolicies
   {{< /command >}}

5. Verify the controller identity.

   {{< command label="inspect" title="Inspect the controller identity" >}}
   kubectl -n openbao-operator-system get serviceaccount \
     openbao-operator-controller
   kubectl -n openbao-operator-system get deployment \
     openbao-operator-controller -o yaml
   {{< /command >}}

   Confirm that the Deployment uses the rendered ServiceAccount and projected `openbao-token`. Continue with
   [operator authentication](../operator-authentication/) when you customize the JWT audience or identity.

## Deploy from source

Use a source deployment when you need a local change or an exact checkout that has not reached the edge channel.

1. Check out the intended commit and prepare the toolchain.

   {{< command label="configure" title="Prepare the source checkout" >}}
   git clone https://github.com/kubebao/openbao-operator.git
   cd openbao-operator
   git checkout <commit>
   make bootstrap
   {{< /command >}}

2. Build and push an image that the cluster can pull.

   {{< command label="build" title="Publish the development image" >}}
   export IMG=<registry>/openbao-operator:<commit>
   make docker-build docker-push IMG="${IMG}"
   {{< /command >}}

   For a local Kind cluster, load the image into every node instead of pushing it, then use the same image reference
   for deployment.

3. Deploy the generated resources and the selected image.

   {{< command label="apply" title="Deploy the source build" >}}
   make deploy IMG="${IMG}" OPERATOR_VERSION=edge
   {{< /command >}}

## Render the local Helm contract

Use the checked-out chart when you need to evaluate Helm rendering, including the single-tenant or OpenShift paths.
The edge image and operator version keep helper-image selection aligned with the unreleased build.

Save any overrides in `operator-values.yaml`, including [label ownership](#choose-namespace-pod-security-label-ownership)
when required. If you do not need overrides, create an empty file with `touch operator-values.yaml`.

{{< command label="inspect" title="Render the local edge chart" >}}
helm template openbao-operator charts/openbao-operator \
  --namespace openbao-operator-system \
  --values operator-values.yaml \
  --include-crds \
  --set image.tag=edge \
  --set operatorVersion=edge
{{< /command >}}

Review the controller and Provisioner ServiceAccounts, RoleBinding subjects, admission-policy identities, projected
token audience, images, and namespaces before applying the render.

`helm template` omits CRDs unless you pass `--include-crds`. When applying rendered manifests directly, create the
operator namespace and wait for the CRDs to become `Established` before starting the controllers or applying
OpenBao custom resources. Configure the same ordering when a GitOps controller applies the manifests.

## Select the target platform

The chart defaults to `platform=auto`. The controller checks the API groups during startup and selects OpenShift
when `security.openshift.io` is present. A failed discovery request or a request that exceeds 10 seconds blocks startup.
Check API server connectivity and discovery permissions before restarting the controller.

To bypass discovery, set the chart value to `platform=kubernetes` or `platform=openshift`. OpenShift mode omits fixed
`runAsUser` and `fsGroup` IDs so the Security Context Constraint can assign namespace-scoped IDs.

The chart sets `OPERATOR_PLATFORM`. This environment variable takes precedence over the deprecated `--platform`
controller flag. Accepted values are `auto`, `kubernetes`, and `openshift`; other values block startup.

## Refresh an edge installation

The edge channel is mutable. Re-read `metadata.json`, then apply CRDs before the complete installer when the recorded
commit changes.

{{< command label="upgrade" title="Refresh to the current edge build" >}}
kubectl apply -f "${EDGE_ROOT}/crds.yaml"
kubectl apply -f "${EDGE_ROOT}/install.yaml"
{{< /command >}}

Re-render custom identities and review OpenBao-side policy changes before refreshing. Self-init does not update
existing OpenBao policies.

## Remove the source or edge deployment

Use the same generated manifest that installed the operator. Inventory every `OpenBaoCluster`, `OpenBaoTenant`, and
`OpenBaoRestore` before deleting CRDs because CR deletion can trigger lifecycle behavior.

{{< command label="remove" title="Remove the edge deployment" >}}
kubectl delete -f "${EDGE_ROOT}/install.yaml"
{{< /command >}}

## Troubleshoot installation

| Symptom | Check |
| --- | --- |
| `no matches for kind "OpenBaoCluster"` | Verify that the CRDs are installed and `Established`. Include `--include-crds` when rendering the chart with `helm template`. |
| Controller starts but Provisioner is absent | Confirm that you did not render `tenancy.mode=single` |
| Tenant provisioning fails on a namespace label update | Inspect the tenant error and [configure label ownership](#choose-namespace-pod-security-label-ownership) if the platform restricts Pod Security label updates |
| Pods cannot pull the source image | Push it to a cluster-reachable registry or load it into every local node |
| Pods run but admission rejects ordinary resources | Inspect policy bindings, rendered identity variables, and API-server ValidatingAdmissionPolicy support |
| Custom names break reconciliation | Compare every ServiceAccount, RoleBinding subject, admission variable, and JWT bound subject |
| OpenShift rejects Pod identity fields | Render `platform=openshift`, then review SCC ownership |

In multi-tenant mode, continue with [namespace onboarding](../onboard-namespace/).
