---
title: Migrate an Argo CD installation
description: Apply chart checkpoints with Argo CD suspended and return Applications to reviewed Git.
eyebrow: Operate · API group migration
weight: 50
verifiedBy:
  - cmd/api-migration/config.go
  - cmd/api-migration/install.go
  - cmd/api-migration/gitops.go
  - cmd/api-migration/procedure.go
  - cmd/api-migration/verification.go
---

Use this guide when Argo CD renders the operator's Helm chart and applies the resulting resources.
Argo CD creates no Helm release storage, so omit `release` from the migration configuration.

## Prepare the Applications and destination

1. Review the [shared preflight and context selection](../#before-you-start) and
   [availability requirements](../#plan-for-availability). Keep one controller replica in the source chart values.
2. [Download and verify the destination command and artifacts](../#download-the-command), or use a
   [matching edge bundle](../#evaluate-a-matching-edge-build) for staging. Select the chart version/digest and
   image/helper references from that same release or edge commit.
3. Identify the operator Application, Applications that apply OpenBao resources, parent Applications, and any
   owning ApplicationSets. Record original automatic-sync, pruning, self-healing, and ApplicationSet policies.
   Determine whether an Application contains both operator and OpenBao resources.
4. Prepare an inactive destination Git diff: chart and image/helper pins, converted OpenBao API versions, and
   custom RBAC/client changes. Check AppProject resource allow-lists, `ignoreDifferences`, health customizations
   such as `openbao.org_OpenBaoCluster`, and scripts. An AppProject restricted to `openbao.org` refuses the new group.
   Do not let Argo CD apply that destination during migration.

## Write the configuration

Save `config.json` with your existing operator namespace and selected cluster:

```json
{
  "kubeconfig": "/path/to/admin.kubeconfig",
  "context": "<context>",
  "operatorNamespace": "openbao-operator-system",
  "sourceVersion": "0.6.1"
}
```

Leave out `release`, `destinationChart`, and `destinationValues`. Argo CD renders its own chart; the command uses
manual checkpoints instead of Helm transitions. For a multi-source Application, identify the operator Helm source
and preserve all other sources when changing checkpoint values.

Prepare the destination image and helper pins using the [source-pin rules](../#replace-source-image-pins).
Preserve the installation's namespace, tenancy, required environment, and resource overrides.
Add [administrator access or an existing migrator](../#add-administrator-access-or-an-existing-migrator)
to the configuration when needed.

## Suspend reconciliation

1. Disable automatic sync on outermost parent Applications, then on their children. Replace `<application>`
   with each relevant Application:

   ```sh
   argocd app set <application> --sync-policy none
   ```

   Alternatively, remove an existing automatic-sync field through the Kubernetes API. Use the namespace where
   Argo CD stores Application objects for `<argocd-namespace>`:

   ```sh
   kubectl -n <argocd-namespace> patch application <application> --type=json \
     -p '[{"op":"remove","path":"/spec/syncPolicy/automated"}]'
   ```

2. For an owning ApplicationSet, preserve child sync settings with an `ignoreApplicationDifferences` rule for
   `/spec/syncPolicy` that covers the Application, or an effective `create-only` or `create-delete` policy.
   The controller's policy takes precedence unless policy override is enabled.
3. Confirm that parents and ApplicationSets cannot restore child automatic sync. Review the
   [remote-controller confirmation](../#suspend-gitops-sync) for controllers outside this cluster or stale tracking.
   `plan` prints detected freeze requirements even when an active controller blocks planning. Retain any failed-plan
   artifacts and use a new state directory on the next attempt.

## Plan and apply migration checkpoints

1. Create a plan in a new, private state directory:

   ```sh
   ./api-migration plan --config config.json --state ./migration-state
   ```

   Review the context, releases, affected objects, snapshot coverage, and skipped checks. If planning leaves
   artifacts and fails, retain that directory and plan again in a new, empty directory.

2. Start migration while all relevant Applications stay suspended:

   ```sh
   ./api-migration migrate --state ./migration-state
   ```

The command exits with status 4 when it needs an operator state. Read the named
`migration-state/checkpoints/<operator-state>.md`, apply that state, then rerun the same command.
Expect `stopped` and `held` during migration, `normal` during commit, and `source` during rollback.
Keep the same binary and state directory throughout.

3. At each checkpoint, read the printed instructions. Layer
   `migration-state/checkpoints/<operator-state>-values.yaml` over your own values on the verified operator Helm
   source. Use the destination chart version and image/helper pins for `stopped`, `held`, and `normal`.
   `plan` cannot read the values Argo CD renders, so replace source pins at every destination checkpoint.
4. At `stopped`, keep every operator Deployment at zero and include the destination CRDs. If Argo CD sets
   `skipCrds: true`, or another process manages CRDs, first apply the selected destination's CRDs:

   ```sh
   kubectl apply --server-side -f crds.yaml
   ```

   Review the rendered operator resources, then sync only the resources requested by the checkpoint, without pruning.
   At `held`, start the destination controller with its migration hold.
5. Rerun the same migration command after each successful manual checkpoint:

   ```sh
   ./api-migration migrate --state ./migration-state
   ```

   If it still waits, inspect the named unmet conditions and the Application's actual applied revision and output.
   Keep parents and ApplicationSets from overwriting live checkpoint values.

{{< callout type="warning" title="Keep checkpoint syncs operator-only" >}}
Do not sync a full shared Application or apply OpenBao custom resources at a checkpoint. The command imports those
resources itself. Keep pruning disabled. At `normal`, use the exact Deployment selectors printed by the checkpoint;
a full sync of unconverted Git can recreate source resources that commit has retired.
{{< /callout >}}

6. When `migrate` succeeds, inspect the held destination:

   ```sh
   ./api-migration status --state ./migration-state
   kubectl get openbaoclusters.kubebao.org,openbaotenants.kubebao.org,openbaorestores.kubebao.org -A
   ```

   Require `Phase: Verified` and `operations hold=true`. Review the recorded verification and skipped checks.
   `status` shows current observations but does not rerun migration verification. An open rollback window confirms
   that planned source identities remain; rollback checks further preconditions.
   Choose commit or the [rollback procedure](#roll-back) after reviewing the held destination.

## Commit and return control to Argo CD

{{< callout type="warning" title="Commit closes the rollback window" >}}
The first source deletion accepted by the API server closes rollback, including a deletion still waiting on
finalizers. After that point, resume `commit` if it stops.
{{< /callout >}}

1. Run commit:

   ```sh
   ./api-migration commit --state ./migration-state
   ```

2. At the `normal` checkpoint, apply its values to the operator Helm source. Run the checkpoint's printed
   `argocd app sync --resource ...` command to update only the named operator Deployments, without pruning.
   Use those selectors even when the operator has a separate Application. Then rerun commit:

   ```sh
   ./api-migration commit --state ./migration-state
   ```

   Require `Phase: Complete` and `operations hold=false`.
3. Follow `migration-state/after-commit.md`. Update Git to the selected destination chart and image/helper pins,
   hold disabled, and planned replicas. Convert every managed OpenBao manifest, replacing `<files>` with its YAML
   file paths:

   ```sh
   ./api-migration convert --write <files>
   ```

   Review the diff, AppProject permissions, custom RBAC, health/ignore rules, and scripts. Documented labels,
   annotations, and finalizers retain their `openbao.org/` prefixes. Restore cleared Cluster-level backup pins
   with destination-compatible images before resuming those Clusters.
4. Publish a staged Git revision that keeps child automatic sync disabled and ApplicationSet protection effective.
   Resume or manually sync parents from the outermost level inward, without pruning. Require each parent's actual
   applied staged revision and confirm that its children remain suspended.
5. Compare each child's complete live `spec.source` or `spec.sources` with the reviewed Git definition.
   Replace retained checkpoint fields using the [source-replacement procedure](#replace-retained-checkpoint-values)
   below, then compare again. Review the rendered operator and OpenBao output before syncing children.
6. Sync children against the intended revision without pruning. Imported resources lack the source's tracking
   metadata, so Argo CD can report OutOfSync until this sync adds it. Confirm the destination release/group,
   disabled hold, planned replicas, helper pins, and successful reconciliation.
7. Restore child automation in a separate Git change. Restore parent automation and ApplicationSet policy only
   after the child desired state is safe. Before enabling pruning of old CRDs, require no `openbao.org` resource
   anywhere in the cluster; protect any CRD still used by another installation.

### Replace retained checkpoint values

Argo CD server-side apply can retain fields added by a live checkpoint patch, including values inside
`spec.source.helm.valuesObject`. Reapplying Git does not prove that those fields were removed. If the live source
differs, keep child automatic sync disabled and use JSON Patch to replace the complete source field from reviewed
Git. A merge patch can retain omitted values too. Compare the complete source again before resuming the child.

For a single-source Application, save its reviewed Git manifest as `operator-application.yaml`. Generate a patch
that replaces only `spec.source`, then apply it to that Application:

```sh
kubectl create --dry-run=client -f operator-application.yaml -o json > operator-application.json
jq -e '.spec.source | select(type == "object") |
  [{op: "replace", path: "/spec/source", value: .}]' \
  operator-application.json > application-source-patch.json
kubectl patch -f operator-application.yaml --type=json --patch-file application-source-patch.json
```

For a multi-source Application, generate the replacement for `/spec/sources` from the reviewed `.spec.sources`
array instead. The patch must replace the source field; it must not restore automatic sync before verification.

## Verify and clean up

Verify representative application login, reads and writes, expected voter and read-replica readiness, storage
bindings, and backups under the surviving operator. Restore any cleared Cluster-level backup-helper pin with a
destination-compatible image before resuming that Cluster. Restore the desired controller replica count in the
surviving configuration, then resume Clusters that you intentionally left paused.

Complete [old CRD and state cleanup](../#clean-up-after-the-migration). Keep state private and revoke any OpenBao
tokens created for planning when they are no longer needed.

## Roll back

Keep parents, children, and ApplicationSets suspended and run:

```sh
./api-migration rollback --state ./migration-state
```

At exit status 4, apply the printed `source` chart values over your original 0.6.1 source chart and image/helper pins.
Sync only the requested operator resources without pruning, then rerun the same rollback command.
Require `Phase: RolledBack` and successful source verification.

Follow `migration-state/after-rollback.md`. Revert prepared destination changes in Git to the source chart, values,
helper pins, and `openbao.org/v1alpha1` manifests. If Git still describes the source, it needs no change, but live
Applications can retain checkpoint values.

Use the same staged parent/child sequence as the commit hand-back: keep children suspended while parents apply the
source Git revision, replace any retained source fields from reviewed Git, compare complete sources, and sync the
source output without pruning. Restore recorded automation and ApplicationSet policies only after source equality
and reconciliation pass. Verify application access and storage. Restore cleared source backup pins and desired
controller replicas before resuming paused Clusters.

The destination CRDs remain installed. To migrate again, create a new plan in a new state directory.
