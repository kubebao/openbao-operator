---
title: Migrate a Flux Kustomization installation
description: Apply operator-only checkpoints while Flux is suspended, then resume reviewed destination sources.
eyebrow: Operate · API group migration
weight: 40
verifiedBy:
  - cmd/api-migration/gitops.go
  - cmd/api-migration/config.go
  - cmd/api-migration/install.go
  - cmd/api-migration/state.go
  - cmd/api-migration/procedure.go
  - config/components/api-migration-stopped/kustomization.yaml
  - config/components/api-migration-hold/kustomization.yaml
---

Use this guide when a Flux Kustomization applies the operator's manifests. There is no Helm release storage.
Keep Flux suspended while you apply the operator checkpoints manually.

## Prepare operator-only inputs and Git

1. Review the [shared preflight and context selection](../#before-you-start) and
   [availability requirements](../#plan-for-availability). Keep one source controller replica in the saved 0.6.1 inputs.
2. [Download and verify the destination command and manifests](../#download-the-command), or select a
   [matching edge bundle](../#evaluate-a-matching-edge-build) for staging.
3. Identify every parent and child Kustomization that manages the operator or OpenBao resources. Record their
   original suspension settings and Git sources. Sources only fetch artifacts and stay active.
4. Prepare operator-only source and destination YAML or Kustomize directories. Preserve the namespace, tenancy,
   environment, and resource settings. Replace source operator/helper pins and `OPERATOR_VERSION` overrides.
   Exclude OpenBao custom resources and GitOps objects from checkpoint inputs.
5. Prepare an inactive Git diff for the destination operator and converted OpenBao resources. Update custom
   RBAC and scripts that name `openbao.org`. Do not let Flux apply the prepared destination during migration.

For a staging edge build fetched from the upstream operator GitRepository, pin its full metadata `sha` on `main`:

```yaml
spec:
  url: https://github.com/kubebao/openbao-operator
  ref:
    branch: main
    commit: <full-sha-from-edge-metadata>
```

This is a source-reference fragment. Keep the GitOps configuration repository distinct from the upstream operator
source. The [Flux commit reference](https://fluxcd.io/flux/components/source/gitrepositories/#commit-example)
selects the exact commit from the named branch. Preserve existing Kustomization image and environment patches
while replacing their source release values.

## Write the configuration

Save `config.json` with the existing namespace and prepared input paths. Omit `release`, `destinationChart`, and
`destinationValues`:

```json
{
  "kubeconfig": "/path/to/admin.kubeconfig",
  "context": "<context>",
  "operatorNamespace": "openbao-operator-system",
  "sourceVersion": "0.6.1",
  "sourceManifests": "/path/to/operator-source",
  "destinationManifests": "/path/to/operator-destination"
}
```

Naming both inputs lets the command generate ready-to-apply checkpoint Kustomizations. Planning renders and checks
them; each checkpoint checks its input again.

Add [administrator access or an existing migrator](../#add-administrator-access-or-an-existing-migrator)
to the configuration when needed.

## Suspend reconciliation

1. Suspend all relevant Kustomizations, outermost parents first. Replace the name and namespace for each object:

   ```sh
   flux suspend kustomization <kustomization> -n <namespace>
   ```

2. Keep GitRepository and other fetch-only sources active. Confirm that parents cannot clear child suspension.
   If Git declares `spec.suspend: false`, keep the child suspended in staged Git changes too.
3. Review the [remote-controller confirmation](../#suspend-gitops-sync) for external controllers or stale tracking.
   `plan` prints detected freeze requirements even when an active controller blocks planning. Retain any written
   failed-plan artifacts and use a new directory on the next attempt.

## Plan and migrate

1. Create a plan in a new, private state directory:

   ```sh
   ./api-migration plan --config config.json --state ./migration-state
   ```

   Review the context, releases, affected objects, snapshot coverage, and skipped checks. If planning leaves
   artifacts and fails, retain that directory and plan again in a new, empty directory.

2. Run migration while Flux stays suspended:

   ```sh
   ./api-migration migrate --state ./migration-state
   ```

The command exits with status 4 when it needs an operator state. Read the named
`migration-state/checkpoints/<operator-state>.md`, apply that state, then rerun the same command.
Expect `stopped` and `held` during migration, `normal` during commit, and `source` during rollback.
Keep the same binary and state directory throughout.

3. Apply each generated operator checkpoint, replacing `<operator-state>` with its printed state, then resume migration:

   ```sh
   kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/<operator-state>
   ./api-migration migrate --state ./migration-state
   ```

   The destination's `stopped` state includes its CRDs and zero operator replicas; `held` starts the destination
   with operations held. `--force-conflicts` transfers the Deployment replica fields from the migration command
   to the reviewed checkpoint. If no input paths were configured, use the release's migration components as printed.

   If the Kustomization consumes YAML rendered from a Helm chart, the command prints chart-values checkpoints.
   Render the named source or destination chart with your values and `<operator-state>-values.yaml` layered last.
   Review and apply only that operator output without pruning. Keep Flux suspended and preserve destination
   image/helper pins in every destination state. Use the same chart-rendering approach for `normal` and `source`.

{{< callout type="warning" title="Keep Flux suspended and do not prune" >}}
Do not reconcile a whole platform bundle at a checkpoint. Apply only operator resources, without pruning.
The destination build omits old CRDs; pruning them deletes source resources still needed for rollback.
At `normal`, unconverted OpenBao Git manifests can recreate sources that commit has retired.
{{< /callout >}}

4. Inspect the held destination after `migrate` succeeds:

   ```sh
   ./api-migration status --state ./migration-state
   kubectl get openbaoclusters.kubebao.org,openbaotenants.kubebao.org,openbaorestores.kubebao.org -A
   ```

   Require `Phase: Verified` and `operations hold=true`. Review the recorded verification and skipped checks.
   `status` shows current observations but does not rerun migration verification. An open rollback window confirms
   that planned source identities remain; rollback checks further preconditions.
   Choose commit or the [rollback procedure](#roll-back) after reviewing the held destination.

## Commit and return control to Flux

{{< callout type="warning" title="Commit closes the rollback window" >}}
The first source deletion accepted by the API server closes rollback, including a deletion still waiting on
finalizers. After that point, resume `commit` if it stops.
{{< /callout >}}

1. Run commit:

   ```sh
   ./api-migration commit --state ./migration-state
   ```

2. At the `normal` checkpoint, apply only the destination operator without migration components or pruning.
   For a generated Kustomize checkpoint, run:

   ```sh
   kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/normal
   ./api-migration commit --state ./migration-state
   ```

   For a chart-values checkpoint, render and apply the printed `normal` values instead, then rerun commit.
   Require `Phase: Complete` and `operations hold=false`.
3. Follow `migration-state/after-commit.md`. In Git, select the destination operator source, images, and helpers;
   remove stopped/hold components; restore planned replicas; and convert all managed OpenBao manifests.
   Replace `<files>` with their YAML file paths:

   ```sh
   ./api-migration convert --write <files>
   ```

   Review the diff and custom RBAC/scripts separately. Documented labels, annotations, and finalizers keep their
   `openbao.org/` prefixes. Restore cleared Cluster-level backup pins with destination-compatible images.
4. Stage and publish Git with every operator/OpenBao-resource child at `spec.suspend: true`. Resume outer parents
   and let them apply the staged source configuration while those children remain suspended:

   ```sh
   flux reconcile source git <gitops-source> -n <namespace>
   flux resume kustomization <parent-kustomization> -n <namespace>
   ```

   Repeat for each parent. Require the intended applied revision and confirm children remain suspended after
   each parent. If a still-suspended child owns a source object, apply only that reviewed source configuration
   without pruning so it can fetch the destination; do not resume the whole child to obtain its source update.
5. Require both the GitOps source and upstream operator source to be Ready at their current observed generations
   and serve the intended `status.artifact.revision`:

   ```sh
   flux reconcile source git <operator-source> -n <namespace>
   kubectl -n <namespace> get gitrepository <operator-source> -o yaml
   ```

   An old Ready condition or artifact does not prove that the new ref is served. If reconciliation times out,
   inspect current conditions and source-controller logs before retrying. Keep children suspended until resolved.
6. Review the child builds for the intended operator version, image/helper pins, hold disabled, planned replicas,
   and converted API group. Before resuming any child that can prune CRDs, list all old resources:

   ```sh
   kubectl get openbaoclusters.openbao.org,openbaotenants.openbao.org,openbaorestores.openbao.org -A
   ```

   Require an empty result. If another installation still needs these CRDs, protect them from pruning first.
7. Remove child suspension in a separate Git change, then also resume each child manually:

   ```sh
   flux resume kustomization <operator-kustomization> -n <namespace>
   flux resume kustomization <openbao-kustomization> -n <namespace>
   ```

   Flux can retain a suspension set by `flux suspend` after Git drops the field. Require each child Ready at the
   intended Git/operator artifact revision, with none left suspended. Imported resources acquire Flux tracking
   metadata on reconciliation.

## Verify and clean up

Verify representative application login, reads and writes, expected voter and read-replica readiness, storage
bindings, and backups under the surviving operator. Restore any cleared Cluster-level backup-helper pin with a
destination-compatible image before resuming that Cluster. Restore the desired controller replica count in the
surviving configuration, then resume Clusters that you intentionally left paused.

Complete [old CRD and state cleanup](../#clean-up-after-the-migration). Keep state private and revoke any OpenBao
tokens created for planning when they are no longer needed.

## Roll back

Keep Flux suspended and run:

```sh
./api-migration rollback --state ./migration-state
```

At exit status 4, read the `source` checkpoint. For a generated Kustomize checkpoint, apply only the original
operator without migration components or pruning, then rerun rollback:

```sh
kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/source
./api-migration rollback --state ./migration-state
```

For a chart-values checkpoint, render and apply the original 0.6.1 chart with its source values instead, then rerun
rollback.
Require `Phase: RolledBack` and successful source verification. Follow `migration-state/after-rollback.md`.
Restore the source operator ref, image/helper pins, and `openbao.org/v1alpha1` manifests in Git if they changed.
If Git still describes the source, it needs no change.

Resume parents first, confirm children remain suspended, and require each source's current generation and intended
source artifact before resuming its child. Review source builds, then run `flux resume` on each child even if Git
removes `spec.suspend`. Verify application access, storage, Flux readiness, and applied revisions. Restore any cleared
source backup pin and desired controller replicas before resuming paused Clusters.

The destination CRDs remain installed. To migrate again, create a new plan in a new state directory.
