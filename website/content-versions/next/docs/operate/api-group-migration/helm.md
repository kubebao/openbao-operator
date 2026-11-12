---
title: Migrate a Helm CLI installation
description: Move an existing Helm-managed 0.6.1 operator to kubebao.org with the migration command.
eyebrow: Operate · API group migration
weight: 10
verifiedBy:
  - cmd/api-migration/config.go
  - cmd/api-migration/helm.go
  - cmd/api-migration/install.go
  - cmd/api-migration/procedure.go
  - cmd/api-migration/verification.go
---

Use this guide when the operator is an existing Helm release that no GitOps controller manages.
The migration command runs Helm for all operator transitions.

## Prepare the installation

1. Review the [shared preflight and context selection](../#before-you-start) and
   [availability requirements](../#plan-for-availability). Reduce the source controller to one replica in its
   0.6.1 Helm values, not only in the live Deployment. Enable the source admission policies.
2. [Download and verify the destination command](../#download-the-command), or select a
   [matching edge bundle](../#evaluate-a-matching-edge-build) for staging. Keep the binary for rollback and resumption.
3. Put Helm 4 on `PATH` using an absolute directory. `plan`, `migrate`, `commit`, and `rollback` run the `helm`
   executable on `PATH`, including resumed commands. Check it in the shell that runs the migration:

   ```sh
   helm version --template '{{.Version}}'
   ```

   Require a `v4.` version.
4. Unpack the destination chart. Replace `<release>` with the destination release:

   ```sh
   helm pull oci://ghcr.io/kubebao/charts/openbao-operator --version <release> --untar
   ```

   For edge, unpack the chart from the verified bundle.

## Write the configuration

Save the following as `config.json`. Replace the kubeconfig, context, release name, namespace, and paths with the
existing installation and prepared destination:

```json
{
  "kubeconfig": "/path/to/admin.kubeconfig",
  "context": "<context>",
  "release": "openbao-operator",
  "operatorNamespace": "openbao-operator-system",
  "sourceVersion": "0.6.1",
  "destinationChart": "./openbao-operator",
  "destinationValues": "./destination-values.yaml"
}
```

`destinationValues` is optional when no source pins need replacement. Otherwise write that file before planning;
the command layers it over the current release values. If Helm stores this release outside `operatorNamespace`,
set `releaseNamespace` to that existing storage namespace. Do not move the release or its storage.

Prepare the destination image and helper pins using the [source-pin rules](../#replace-source-image-pins).
Preserve the installation's namespace, tenancy, required environment, and resource overrides.
Add [administrator access or an existing migrator](../#add-administrator-access-or-an-existing-migrator)
to the configuration when needed.

## Plan and migrate

1. Create a plan in a new, private state directory:

   ```sh
   ./api-migration plan --config config.json --state ./migration-state
   ```

   Review the context, releases, affected objects, snapshot coverage, and skipped checks. If planning leaves
   artifacts and fails, retain that directory and plan again in a new, empty directory.

2. Start migration. The command stops the source operator, applies the destination chart, transfers ownership,
   starts the destination with operations held, and waits for verification:

   ```sh
   ./api-migration migrate --state ./migration-state
   ```

3. Inspect the held destination:

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

When verification passes and you accept the destination, run:

```sh
./api-migration commit --state ./migration-state
```

Require `Phase: Complete` and `operations hold=false`. The command writes no GitOps hand-back file for this route.
The release keeps `controller.apiMigrationHold: false` and the planned replica counts. With one controller and
one provisioner, these equal the chart defaults. Keep them, or remove them in your next upgrade.

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

Require `Phase: RolledBack`, the 0.6.1 source operator running, and successful source verification.
The command waits for the source rollout. The `kubebao.org` CRDs remain installed; qualify resource names with their
group while both APIs exist. To migrate again, create a new plan in a new state directory.

This route has no GitOps hand-back. Verify the source release values, application access, and storage.
Restore any cleared source backup-helper pin and the desired source controller replica count before resuming
paused Clusters.
