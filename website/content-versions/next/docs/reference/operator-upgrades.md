---
title: Upgrade the operator
description: Supported operator upgrade paths, CRD sequencing, verification, and downgrade stance.
eyebrow: Reference
weight: 3
verifiedBy:
  - docs/user-guide/operator/installation.md
  - charts/openbao-operator/README.md
  - internal/service/upgrade/version.go
  - .github/workflows/release.yml
---

Upgrade the CRDs before the controller whenever the target release changes the CRD bundle. This page covers the
operator installation. Changes to the OpenBao workload version use `spec.version` and the cluster upgrade strategy.

## Supported paths

| Operator path | Project stance | Action |
| --- | --- | --- |
| Stable patch, `0.Y.Z` to the next patch | Supported maintenance path | Review the release notes and upgrade |
| Stable minor, `0.Y.Z` to `0.(Y+1).0` | Supported with release-note review | Move sequentially and validate in staging |
| Skip multiple minor releases | Not recommended | Apply intermediate migrations in order |
| Routine operator downgrade | Unsupported | Prefer a forward fix; treat any rollback as recovery |

{{< callout type="warning" title="Back up before changing the operator" >}}
Take and verify a current backup for every managed production cluster. An operator rollback is not a data rollback.
{{< /callout >}}

## Restore executor changes in Next

Restore executors stage snapshots before submission and reject snapshots larger than 8 GiB. Each executor requests
about 9 GiB of ephemeral storage. Check node capacity and namespace quotas before upgrading. Restore Jobs use a
40-minute deadline and do not retry failed containers or follow HTTP redirects. Finish in-flight restores before
upgrading because an older executor does not provide these guarantees.

## Upgrade a Helm installation

1. Replace `X.Y.Z` with the exact target release.
2. Apply the release CRDs.

   ```bash
   kubectl apply -f \
     https://github.com/dc-tec/openbao-operator/releases/download/X.Y.Z/crds.yaml
   ```

3. Upgrade the chart.

   ```bash
   helm upgrade openbao-operator \
     oci://ghcr.io/dc-tec/charts/openbao-operator \
     --version X.Y.Z \
     --namespace openbao-operator-system
   ```

Helm does not upgrade CRDs stored in a chart's `crds/` directory. Applying the release bundle first prevents the new
controller from starting against an older schema.

## Verify the upgrade

1. Verify the operator workloads.

   ```bash
   kubectl -n openbao-operator-system get deployments,pods
   ```

2. Verify that the stored CRD version is `v1alpha1` and that the CRDs report `Established=True`.

   ```bash
   kubectl get crd openbaoclusters.openbao.org \
     -o jsonpath='{.status.storedVersions}{"\n"}{.status.conditions}{"\n"}'
   ```

3. Inspect each managed cluster's phase, conditions, and recent events.

   ```bash
   kubectl get openbaocluster -A
   kubectl -n <namespace> describe openbaocluster <name>
   ```

## Recover from a failed operator upgrade

Prefer a forward fix on a newer stable release. If that is not possible, validate the exact older controller and CRD
combination in staging before changing production. Use backup and restore procedures when the failure affects the
OpenBao data path; do not assume a chart rollback reverses CRD or workload state.
