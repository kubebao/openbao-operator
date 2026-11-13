---
title: Migrate a kubectl or Kustomize installation
description: Move a directly applied 0.6.1 operator through manual manifest checkpoints.
eyebrow: Operate · API group migration
weight: 20
verifiedBy:
  - cmd/api-migration/config.go
  - cmd/api-migration/install.go
  - cmd/api-migration/state.go
  - cmd/api-migration/procedure.go
  - config/components/api-migration-stopped/kustomization.yaml
  - config/components/api-migration-hold/kustomization.yaml
---

Use this guide when you apply operator manifests with `kubectl` or Kustomize and no GitOps controller manages them.
The migration command requests manual operator checkpoints; it never applies those manifests itself.

## Prepare operator-only inputs

1. Review the [shared preflight and context selection](../#before-you-start) and
   [availability requirements](../#plan-for-availability). Keep one source controller replica in the saved 0.6.1 inputs.
2. [Download and verify the destination command and artifacts](../#download-the-command), or use a
   [matching edge bundle](../#evaluate-a-matching-edge-build) for staging.
3. Save the source operator YAML or Kustomize overlay as it was installed. Prepare separate destination inputs
   for the selected release. A file or directory with `kustomization.yaml` is accepted.
   Include operator resources and CRDs, but exclude OpenBao custom resources and GitOps objects.
4. Preserve the namespace, tenancy, required environment, and resource settings. Replace source image and helper
   pins, including any `OPERATOR_VERSION`, `OPERATOR_INIT_IMAGE`, `OPERATOR_BACKUP_IMAGE`, and
   `OPERATOR_UPGRADE_IMAGE` overrides. Review the [source-pin rules](../#replace-source-image-pins).

## Write the configuration

Save the following as `config.json` with your paths and existing namespace. Omit `release`, `destinationChart`, and
`destinationValues` because this installation has no Helm release storage:

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

Naming both inputs lets the command generate ready-to-apply checkpoint Kustomizations for `install.yaml` and
Kustomize installations. Planning renders and checks the inputs; every checkpoint checks its input again.

Add [administrator access or an existing migrator](../#add-administrator-access-or-an-existing-migrator)
to the configuration when needed.

## Plan and migrate

1. Create a plan in a new, private state directory:

   ```sh
   ./api-migration plan --config config.json --state ./migration-state
   ```

   Review the context, releases, affected objects, snapshot coverage, and skipped checks. If planning leaves
   artifacts and fails, retain that directory and plan again in a new, empty directory.

2. Start migration:

   ```sh
   ./api-migration migrate --state ./migration-state
   ```

The command exits with status 4 when it needs an operator state. Read the named
`migration-state/checkpoints/<operator-state>.md`, apply that state, then rerun the same command.
Expect `stopped` and `held` during migration, `normal` during commit, and `source` during rollback.
Keep the same binary and state directory throughout.

3. At each generated checkpoint, replace `<operator-state>` with the state named by the command and run:

   ```sh
   kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/<operator-state>
   ./api-migration migrate --state ./migration-state
   ```

   The stopped input includes the destination CRDs and sets operator Deployments to zero. The held input starts
   the destination controller with its migration hold. The command owns Deployment replica fields while stopped;
   `--force-conflicts` transfers those fields to the reviewed checkpoint.

   If you did not name input paths, use the release's `api-migration-stopped` and `api-migration-hold` Kustomize
   components as the checkpoint requests. Copies are under `migration-state/checkpoints/components`.

   If the operator was installed from `helm template`, follow the printed chart-values checkpoint instead of
   expecting a generated Kustomization. Render the named source or destination chart with your values and the
   checkpoint's `<operator-state>-values.yaml` layered last. Preserve source settings for rollback and use the
   destination image/helper pins for every destination state. Review and apply only the rendered operator resources.
   Include the chart's CRDs in that output, or apply the selected destination's `crds.yaml` with
   `kubectl apply --server-side -f crds.yaml` before completing `stopped`.

{{< callout type="warning" title="Do not prune or apply OpenBao resources at checkpoints" >}}
Apply only the requested operator input, without `--prune`. The command imports OpenBao resources itself.
Pruning old CRDs deletes sources still needed for rollback. At `normal`, applying unconverted OpenBao manifests
can recreate source resources that commit has retired.
{{< /callout >}}

4. After `migrate` succeeds, inspect the held destination:

   ```sh
   ./api-migration status --state ./migration-state
   kubectl get openbaoclusters.kubebao.org,openbaotenants.kubebao.org,openbaorestores.kubebao.org -A
   ```

   Require `Phase: Verified` and `operations hold=true`. Review the recorded verification and skipped checks.
   `status` shows current observations but does not rerun migration verification. An open rollback window confirms
   that planned source identities remain; rollback checks further preconditions.
   Choose commit or the [rollback procedure](#roll-back) after reviewing the held destination.

## Commit the migration

{{< callout type="warning" title="Commit closes the rollback window" >}}
The first source deletion accepted by the API server closes rollback, including a deletion still waiting on
finalizers. After that point, resume `commit` if it stops.
{{< /callout >}}

1. Run:

   ```sh
   ./api-migration commit --state ./migration-state
   ```

2. At exit status 4, read the printed `normal` checkpoint. For a generated Kustomize checkpoint, apply it without
   migration components or pruning:

   ```sh
   kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/normal
   ./api-migration commit --state ./migration-state
   ```

   For a chart-rendered installation, apply the printed `normal` values instead. Require `Phase: Complete` and
   `operations hold=false`. This route has no GitOps hand-back file.

Update retained OpenBao manifests before applying them again. Replace `<files>` with their YAML file paths:

```sh
./api-migration convert --write <files>
```

Review the converted API versions and update custom RBAC, clients, and scripts separately. Documented labels,
annotations, and finalizers keep their `openbao.org/` prefixes.

## Verify and clean up

Verify representative application login, reads and writes, expected voter and read-replica readiness, storage
bindings, and backups under the surviving operator. Restore any cleared Cluster-level backup-helper pin with a
destination-compatible image before resuming that Cluster. Restore the desired controller replica count in the
surviving configuration, then resume Clusters that you intentionally left paused.

Complete [old CRD and state cleanup](../#clean-up-after-the-migration). Keep state private and revoke any OpenBao
tokens created for planning when they are no longer needed.

## Roll back

Rollback is available only before commit accepts a source deletion. Use the same binary and state directory:

```sh
./api-migration rollback --state ./migration-state
```

At exit status 4, read the `source` checkpoint. For a generated Kustomize checkpoint, apply the original 0.6.1
operator inputs without migration components or pruning, then rerun `rollback`:

```sh
kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/source
./api-migration rollback --state ./migration-state
```

For a chart-rendered installation, render and apply the original source chart with the printed source values.

Require `Phase: RolledBack`, the 0.6.1 source operator running, and successful source verification.
The command waits for the source rollout. The `kubebao.org` CRDs remain installed; qualify resource names with their
group while both APIs exist. To migrate again, create a new plan in a new state directory.

Verify source application access and storage. Restore any cleared source backup-helper pin and the desired
source controller replica count before resuming paused Clusters.
