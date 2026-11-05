---
title: Upgrade the operator
description: Supported operator upgrade paths, CRD sequencing, verification, and downgrade stance.
eyebrow: Reference
weight: 3
verifiedBy:
  - cmd/api-migration/README.md
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

{{< callout type="warning" title="0.6 to 0.7 moves the API group" >}}
The 0.7 line serves `kubebao.org/v1alpha1` instead of `openbao.org/v1alpha1`. Move a 0.6 installation with
[the API group migration](../../operate/api-group-migration/), not with the Helm upgrade on this page.
{{< /callout >}}

{{< callout type="warning" title="Back up before changing the operator" >}}
Take and verify a current backup for every managed production cluster. An operator rollback is not a data rollback.
{{< /callout >}}

## Restore changes in this release

- Finish in-flight restores before upgrading. Requests from older releases do not use the submission claim.
- A restore into an existing cluster now ends `Unknown` and waits for an administrator acknowledgement. The operator
  no longer drains read replicas or restarts voters automatically; `Resume` runs a managed restart instead. See
  [Recover an uncertain restore](../../operate/restore/recover-uncertain/).
- `status.restore` on `OpenBaoCluster` is deprecated and no longer written.
- `openbao_restore_state` adds values 4–7. An unresolved recovery takes precedence; otherwise the newest retained
  request determines the value. Update alerts that assume the previous values.
- Static-unseal clusters can use `spec.unseal.credentialsSecretRef` as their key source, and admission prevents
  changing it later. Earlier releases ignored this field for static clusters. A cluster that already has an
  operator-generated `<cluster>-unseal-key` keeps using that key, so an existing reference stays ignored.

## Upgrade a Helm installation

1. Replace `X.Y.Z` with the exact target release.
2. Apply the release CRDs.

   ```bash
   kubectl apply -f \
     https://github.com/kubebao/openbao-operator/releases/download/X.Y.Z/crds.yaml
   ```

3. Upgrade the chart.

   ```bash
   helm upgrade openbao-operator \
     oci://ghcr.io/kubebao/charts/openbao-operator \
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
   kubectl get crd openbaoclusters.kubebao.org \
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
