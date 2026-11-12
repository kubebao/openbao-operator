---
title: Migrate a Flux HelmRelease installation
description: Suspend Flux, migrate its existing Helm release, and return reconciliation to reviewed Git.
eyebrow: Operate · API group migration
weight: 30
verifiedBy:
  - cmd/api-migration/gitops.go
  - cmd/api-migration/helm.go
  - cmd/api-migration/config.go
  - cmd/api-migration/install.go
  - cmd/api-migration/procedure.go
---

Use this guide when Flux manages the operator through a HelmRelease. The migration command drives Helm while
Flux reconciliation remains suspended. A HelmRelease still requires the local `helm` executable to be Helm 4.

## Prepare the installation

1. Review the [shared preflight and context selection](../#before-you-start) and
   [availability requirements](../#plan-for-availability). Keep one source controller replica in the 0.6.1 release values.
2. [Download and verify the destination command and chart](../#download-the-command), or use a
   [matching edge bundle](../#evaluate-a-matching-edge-build) for staging. Put Helm 4 on `PATH` using an absolute
   directory and require `helm version --template '{{.Version}}'` to print a `v4.` version.
   Every installation-changing invocation checks this, including resumed commands.
3. Identify the HelmRelease, all parent Kustomizations, Kustomizations that apply OpenBao resources, and the
   Git and chart sources. Record their original settings. Sources only fetch artifacts and stay active.
4. Prepare an inactive destination Git diff: the selected chart version or digest, destination image/helper pins,
   converted OpenBao API versions, and any custom RBAC or scripts that name `openbao.org`.
   Do not apply that Git state during migration.

## Write the configuration

Save the following as `config.json`. Use the existing Helm release name and operator namespace:

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

Unpack the selected chart into `destinationChart`. `destinationValues` layers your destination overrides over the
current release values; omit it only when no source pin needs replacement.

The command discovers a local HelmRelease's storage namespace from its tracking metadata. If its controller is
remote, set `releaseNamespace` to the existing namespace reported by `HelmRelease.status.storageNamespace`.
Keep the HelmRelease's current storage and target namespaces. Changing either can uninstall and reinstall the release.

Prepare the destination image and helper pins using the [source-pin rules](../#replace-source-image-pins).
Preserve the installation's namespace, tenancy, required environment, and resource overrides.
Add [administrator access or an existing migrator](../#add-administrator-access-or-an-existing-migrator)
to the configuration when needed.

## Suspend reconciliation

1. Suspend every relevant Kustomization, outermost parents first, then the HelmRelease. Replace the placeholders
   below with each detected object's name and namespace:

   ```sh
   flux suspend kustomization <kustomization> -n <namespace>
   flux suspend helmrelease <helmrelease> -n <namespace>
   ```

2. Confirm that parents cannot clear child suspension. Keep sources active. If Git declares `spec.suspend: false`,
   keep those children suspended in any staged Git change too.
3. Review the [remote-controller confirmation](../#suspend-gitops-sync) if tracking names a controller outside this cluster.
   `plan` prints detected freeze requirements even when an active controller blocks planning. If that attempt writes
   artifacts, retain its state and use a new directory for the next plan.

## Plan and migrate

1. Create a plan in a new, private state directory:

   ```sh
   ./api-migration plan --config config.json --state ./migration-state
   ```

   Review the context, releases, affected objects, snapshot coverage, and skipped checks. If planning leaves
   artifacts and fails, retain that directory and plan again in a new, empty directory.

2. Start migration; leave Flux suspended while the command runs Helm:

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

## Commit and return control to Flux

{{< callout type="warning" title="Commit closes the rollback window" >}}
The first source deletion accepted by the API server closes rollback, including a deletion still waiting on
finalizers. After that point, resume `commit` if it stops.
{{< /callout >}}

1. Run commit and require `Phase: Complete` and `operations hold=false`:

   ```sh
   ./api-migration commit --state ./migration-state
   ```

2. Follow `migration-state/after-commit.md`. In Git, select the destination chart and retain its image/helper pins,
   disable the hold, and restore planned replica counts. Convert every managed OpenBao manifest, replacing
   `<files>` with its YAML file paths:

   ```sh
   ./api-migration convert --write <files>
   ```

   Review the diff. Labels, annotations, and finalizers keep their documented `openbao.org/` prefixes.
   Update custom RBAC and scripts separately. Restore cleared Cluster-level backup pins with compatible images.
3. Stage and publish Git with the HelmRelease and OpenBao-resource children at `spec.suspend: true`.
   An intermediate operator Kustomization that applies the HelmRelease and its chart source is a parent of the
   HelmRelease; it must apply the staged source configuration before that release can resume.
4. Reconcile the staged Git source, then resume parents from the outermost level inward:

   ```sh
   flux reconcile source git <git-source> -n <namespace>
   flux resume kustomization <parent-kustomization> -n <namespace>
   ```

   Repeat for each parent, including the intermediate operator Kustomization. Require the intended applied Git
   revision and confirm after each step that the HelmRelease and OpenBao-resource children remain suspended.
   Require each source to be Ready at its current observed generation and to serve the intended artifact revision.
5. Require the chart source to serve the destination chart version or digest before resuming the HelmRelease.
   For an OCIRepository, reconcile and inspect that existing source:

   ```sh
   flux reconcile source oci <chart-source> -n <namespace>
   kubectl -n <namespace> get ocirepository <chart-source> -o yaml
   ```

   Check current `status.observedGeneration`, `Ready=True`, and `status.artifact.revision` against the selected
   chart digest. For another chart-source type, verify its equivalent current artifact. Review the HelmRelease's
   desired values against the surviving release: intended images, helper pins, hold disabled, and planned replicas.
6. Before allowing pruning of old CRDs, require no `openbao.org` resource anywhere in the cluster. Protect CRDs
   still used by another installation. Remove child suspension in a separate Git change and also resume it manually:

   ```sh
   flux resume helmrelease <helmrelease> -n <namespace>
   flux resume kustomization <openbao-kustomization> -n <namespace>
   ```

   Flux can retain suspension set by `flux suspend` after Git drops the field. Require the operator's intended
   chart and each child's intended Git revision to be Ready. The first Helm reconciliation can add a revision
   with the same chart because migration changed Helm history outside Flux.

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

Before source retirement, the command restores source ownership and the saved 0.6.1 Helm release, then verifies it.
Require `Phase: RolledBack` and follow `migration-state/after-rollback.md`.

If Git still describes the source, it needs no change. Otherwise restore the source chart, values, helper pins,
and `openbao.org/v1alpha1` manifests. Resume parents first, confirming that children remain suspended and sources
serve the intended source artifacts. Resume the HelmRelease only after its chart source serves 0.6.1, then resume
OpenBao-resource children. Run `flux resume` for every suspended object even if Git removes `spec.suspend`.

Verify source application access, storage, Flux revisions, and readiness. Restore cleared source backup pins and
the desired controller replica count before resuming paused Clusters. The destination CRDs remain installed.
To migrate again, create a new plan in a new state directory.
