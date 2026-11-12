---
title: Migrate to the kubebao.org API group
description: Move a 0.6.1 installation from openbao.org to kubebao.org without rebuilding its OpenBao clusters.
eyebrow: Operate · Change management
weight: 47
verifiedBy:
  - cmd/api-migration/main.go
  - cmd/api-migration/config.go
  - cmd/api-migration/procedure.go
  - cmd/api-migration/install.go
  - cmd/api-migration/gitops.go
  - cmd/api-migration/convert.go
  - cmd/api-migration/primitives/inspect.go
  - cmd/api-migration/e2e_test.go
  - config/components/api-migration-stopped/kustomization.yaml
  - config/components/api-migration-hold/kustomization.yaml
  - hack/ci/build-api-migration.sh
  - hack/ci/operator-upgrade-e2e.sh
  - .github/workflows/release.yml
  - .github/workflows/publish-edge.yml
  - .github/workflows/reusable-channel-hardening.yml
---

Starting with 0.7, the operator serves `OpenBaoCluster`, `OpenBaoTenant`, and `OpenBaoRestore` as
`kubebao.org/v1alpha1` instead of `openbao.org/v1alpha1`. Move an existing 0.6.1 installation with the `api-migration`
command that each 0.7 release publishes. The command re-creates every resource in the new group and moves the
ownership of the managed workloads to it. Data and storage stay in place, and nothing in OpenBao changes. Only the
operator stops while ownership moves, and OpenBao keeps serving meanwhile. The destination operator can then roll the
data Pods once, as an operator upgrade does: see [Plan for availability](#plan-for-availability).

{{< callout type="warning" title="Do not upgrade a 0.6 installation in place" >}}
A `helm upgrade` to 0.7, or applying the 0.7 manifests, starts an operator that does not reconcile the existing
`openbao.org` resources. Their workloads keep running without an operator. Use this procedure instead.
{{< /callout >}}

## Migration tool lifecycle

0.8.0 is the target for removing `api-migration` from `main`. The planned upgrade path for 0.8.0 and later is
`0.6.1 → latest supported 0.7.x → destination release`. Use the migration binary that matches the 0.7.x destination,
finish `commit`, and then follow the destination release's normal upgrade procedure. Removing the tool requires
qualification and documentation of this upgrade path.

The published 0.7.x migration artifacts and versioned guide remain available. Maintainers must announce the end date
of 0.7.x migration support before publishing 0.8.0. Removing the tool from `main` does not end that support window or
remove the runtime compatibility that migrated Clusters need.

## Choose your installation route

GitOps reconciliation stays suspended while the command moves ownership. The installation method determines who
applies the temporary operator states:

| Installation | Configuration | Operator transitions |
| --- | --- | --- |
| [Argo CD renders the chart](#argo-cd-checklist) | Omit `release` | Apply each checkpoint manually, without pruning. Use operator-only syncs for a shared Application. |
| [Flux Kustomization applies manifests](#flux-kustomization-checklist) | Omit `release`; optionally name operator-only `sourceManifests` and `destinationManifests` | Apply the checkpoint's operator manifests with `kubectl`; keep the Kustomization suspended. |
| [Flux HelmRelease](#flux-helmrelease-checklist) | Set `release` to the existing Helm release name | The command drives Helm while the HelmRelease and its parents remain suspended. |
| [Helm CLI](#helm-cli-checklist) | Set `release` to the existing Helm release name | The command drives Helm. |

A shared Application or Kustomization can manage both the operator and its OpenBao resources. Checkpoints must
change only the operator. Applying converted OpenBao resources before import creates destination objects too early;
reapplying old API manifests at the final checkpoint recreates source objects that commit has retired.

Before the maintenance window, prepare and review the destination Git diff, including API versions, image and helper
pins, and any custom RBAC or client changes. Search Git for other references to the `openbao.org` group:
Argo CD AppProject resource allow-lists, `ignoreDifferences` rules, and resource health customizations such as
`openbao.org_OpenBaoCluster`; RBAC `apiGroups`; and scripts. An AppProject that allows only `openbao.org` refuses
to sync the converted resources. Record the original sync settings. Keep the destination Git changes
inactive until the hand-back step. For a manifest installation, also prepare the operator-only checkpoint inputs.

Use the checklist for your route to select the applicable parts of this guide. Each checklist follows the same
phases: prepare, suspend, migrate, decide, and return control to GitOps.

### Argo CD checklist

1. Complete [preflight](#before-you-start) and [configuration](#write-the-configuration) with `release` omitted.
   Identify whether the Application also contains OpenBao custom resources.
2. [Suspend sync](#suspend-gitops-sync), including parent Applications and ApplicationSet updates. Confirm that
   parents cannot restore child automatic sync before running `plan`.
3. [Migrate](#migrate-the-installation). At each [manifest checkpoint](#migrate-an-installation-from-manifests),
   apply the printed operator values and sync only the requested operator resources, without pruning. If the
   Application sets `skipCrds: true`, apply the destination CRDs before the `stopped` checkpoint.
4. Review `status`. Commit only after migration verification passes with operations held.
   [Roll back](#roll-back) while the window is open if you decide to return to the source, including after an
   interrupted migration. The first accepted source deletion during `commit` closes that window.
5. Follow the generated hand-back file and [return control to GitOps](#return-control-to-gitops). Apply the reviewed
   Git revision with child automatic sync disabled. Resume parents, confirm that children remain suspended, then
   restore child sync against the intended revision.

### Flux Kustomization checklist

1. Complete [preflight](#before-you-start) and [configuration](#write-the-configuration) with `release` omitted.
   Prepare operator-only `sourceManifests` and `destinationManifests` when using generated checkpoint inputs.
2. [Suspend sync](#suspend-gitops-sync), including parent Kustomizations. Confirm that parents cannot restore child
   reconciliation before running `plan`.
3. [Migrate](#migrate-the-installation). Apply each printed [manifest checkpoint](#migrate-an-installation-from-manifests)
   without pruning, then rerun the same command. Keep OpenBao custom resources out of the checkpoint inputs.
4. Review `status`. Commit only after migration verification passes with operations held.
   [Roll back](#roll-back) while the window is open if you decide to return to the source, including after an
   interrupted migration. The first accepted source deletion during `commit` closes that window.
5. Follow the generated hand-back file and [return control to GitOps](#return-control-to-gitops). Stage children
   with `spec.suspend: true` in Git. Resume parents and sources, verify the intended artifact revision and suspended
   children, then restore child reconciliation. Protect old CRDs from pruning until their resources are gone.

### Flux HelmRelease checklist

1. Complete [preflight](#before-you-start) and [configuration](#write-the-configuration). Set `release` to the existing
   Helm release name and preserve its storage and target namespaces. The command runs the `helm` executable that it
   finds on `PATH`, and `plan` refuses any version other than Helm 4.
2. [Suspend sync](#suspend-gitops-sync) for the HelmRelease and its parents. Confirm that parent reconciliation
   cannot clear the suspension before running `plan`.
3. [Migrate](#migrate-the-installation). The command drives the Helm transitions; leave the HelmRelease suspended.
4. Review `status`. Commit only after migration verification passes with operations held.
   [Roll back](#roll-back) while the window is open if you decide to return to the source, including after an
   interrupted migration. The first accepted source deletion during `commit` closes that window.
5. Follow the generated hand-back file and [return control to GitOps](#return-control-to-gitops). Stage the
   HelmRelease with `spec.suspend: true` in Git. Resume parents and sources, then require the intended chart artifact
   to serve the destination release before resuming the HelmRelease.

### Helm CLI checklist

1. Complete [preflight](#before-you-start) and [configuration](#write-the-configuration). Set `release` to the existing
   Helm release name. The command runs the `helm` executable that it finds on `PATH`, and `plan` refuses any version
   other than Helm 4. Put Helm 4 in a directory with an absolute path on `PATH`.
2. [Migrate](#migrate-the-installation). The command drives the Helm transitions.
3. Review `status`. Commit only after migration verification passes with operations held.
   [Roll back](#roll-back) while the window is open if you decide to return to the source, including after an
   interrupted migration. The first accepted source deletion during `commit` closes that window.
4. [Clean up](#clean-up-after-the-migration). The command writes no hand-back file. The release values keep
   `controller.apiMigrationHold: false` and the planned replica counts. With one controller and one provisioner
   replica, these equal the chart defaults. Keep them, or remove them in your next upgrade.

## Before you start

{{< checklist title="Preflight" >}}
- The operator runs 0.6.1. The command refuses any other source release, so upgrade an older installation to 0.6.1
  first.
- The operator controller Deployment runs exactly one replica, and the source admission policies are enabled.
- Every `OpenBaoCluster` is available at its current generation, or paused with the pause observed by the operator.
- No upgrade, backup, restore, blue-green, or break-glass operation is in flight, and no backup trigger is pending:
  no Cluster has the `openbao.org/trigger-backup` annotation. The controller removes it when it accepts a request.
- Every `OpenBaoRestore` is finished: `Completed`, `Failed`, or `Unknown` with a `Resume` or `Abandon` decision and
  no managed restart or target cleanup in progress.
- No `OpenBaoTenant` is being deleted.
- Every Cluster has a recent backup with a rehearsed [restore path](../restore/).
- You have cluster administrator access and `kubectl`. For a Helm CLI or Flux HelmRelease installation, Helm 4 is
  the `helm` executable on `PATH`: the command runs it for both routes.
- Every GitOps controller that manages the operator or its resources is suspended. See [Suspend GitOps sync](#suspend-gitops-sync).
{{< /checklist >}}

Select the kubeconfig and context for every `kubectl` command in this procedure. Use the same values in
`config.json`; the migration command's configuration does not change your shell's Kubernetes context.

{{< command label="context" title="Select the migration cluster" >}}
export KUBECONFIG="/path/to/admin.kubeconfig"
kubectl config use-context "<context>"
kubectl config current-context
{{< /command >}}

Confirm that the printed context identifies the intended cluster. Keep this selection for the whole procedure.

If you normally run multiple controller replicas, reduce the source installation to one before `plan`. For Helm,
change the source 0.6.1 release's values; scaling only the Deployment leaves a rollback revision with a different
replica count. For manifests, update the source manifests. Keep one controller replica until `commit` or `rollback`
finishes. Then restore the desired count in the surviving release's configuration before resuming GitOps sync.
The chart's unsafe `admissionPolicies.enabled: false` mode is outside migration support; enable the source release's
normal policies first.

If a Cluster sets `spec.backup.image`, pause the Cluster and wait for the pause to be observed. Record the pin, then
clear it before `plan`. Configure a destination-compatible backup helper in the destination release; the source
helper cannot submit restore claims to the new API group. After `commit`, restore any custom pin with its
0.7-compatible image before resuming the Cluster. After `rollback`, restore the source pin before resuming.
Finished restore records keep their historical image references. Helm destination values must replace a pinned
`helperImages.backup`; the command refuses the unchanged source reference.

The command refuses transient states that prevent a safe handover. Finish or acknowledge the operation, then run
it again. It also refuses unsupported ownership and source resources with additional finalizers. Review the
reported resource and its owning controller before retrying; the command never removes an unknown finalizer.

Managed Ingresses, HTTPRoutes, TLSRoutes, BackendTLSPolicies, ServiceMonitors, and CiliumNetworkPolicies move with
their Cluster. Their specifications and status remain unchanged.

A paused Cluster moves as it is, including a Cluster paused before it initialized and a Cluster left paused by an
abandoned restore. The command does not require it to be available. It verifies the controller login only when
OpenBao was ready at `plan`, and requires the Pods that were ready then to stay ready. Resume the Cluster after
`commit`.

## Plan for availability

The migration keeps data and availability apart:

- The command never recreates claims, volumes, Raft membership, or seal material.
- While ownership moves, OpenBao keeps serving: the command restarts no Pod and the Services keep their endpoints.
- After the destination operator starts, it renders its own release's Pod template. Where that differs from the
  source's, it rolls the data Pods once, as any operator upgrade does. A single-replica Cluster is unavailable while
  its Pod restarts. With three or more voters, the rolling update keeps quorum. Verification waits for the rollout.
  Any client can receive errors for several seconds while a Pod restarts and the active node changes, whether it
  connects through the Service, an ingress, or a Gateway. Use clients that retry failed requests.
- `rollback` starts the source operator again. It renders the source Pod template and rolls the data Pods a
  second time.

To choose when that rollout happens, pause the Cluster before `plan` and resume it in a maintenance window after
`commit`. A paused Cluster is not re-rendered, and it takes no scheduled backups while paused.

An idle BlueGreen workload can retain old Pod revisions with `OnDelete`. An idle rolling workload can also hold
older Pods behind a partition. Migration preserves those settings and does not require those Pods to be replaced.
A rollout that has not reached the state required by its update strategy must settle first.

## Migration qualification

Installation support and configuration qualification are recorded separately. The automated suite includes Helm,
manifest and Kustomize installations, live Argo CD and Flux controllers, single-tenant and multi-tenant setups,
HA and read replicas, paused Clusters, and forward, rollback, and interruption scenarios.

| Configuration | Migration-specific coverage | Qualification boundary |
| --- | --- | --- |
| Kubernetes and OpenBao | Full Kind Kubernetes 1.36.1 suite; `commit`, `recovery`, and `restore-lifecycle` on Kind 1.34.3 and 1.35.1; OpenBao 2.6.5 and 2.7.2 scenarios | Other supported Kubernetes lines, OpenShift, architectures, and OpenBao patches need candidate-specific runs |
| Hardened | ACME TLS and Transit unseal, including Flux Kustomization, HelmRelease, and Argo CD scenarios; a staging run on an Argo CD app-of-apps installation with Transit and PKCS#11 seals | Other cloud KMS, workload identity, HSM, and GitOps combinations need staging qualification |
| Networking and metrics | API-server ownership transfers with RBAC, admission, and unchanged payloads | These tests do not qualify a live ingress/Gateway/Cilium data plane or metrics scraping |
| Storage, images, and plugins | PVC/PV identity checks, local storage fixtures, retained ACME cache, and published signed images | Qualify the exact CSI backend, private registries, verification credentials, plugins, and runtime settings |
| Backups and restores | S3-compatible backups, completed in-place and retained fresh-target records, and a post-migration scheduled restore | Cloud storage credentials and storage/provider combinations need target-environment runs |

The suite does not test every combination of these features. A release qualification must record successful runs
for the candidate; a listed scenario alone does not establish a passing result. The command's
[configuration coverage map](https://github.com/kubebao/openbao-operator/blob/main/cmd/api-migration/README.md#configuration-coverage-map)
lists the automated scenarios and remaining qualification work. Rehearse the migration with your production
combination in staging.

## Download the command

1. Download the binary for your platform from the release you migrate to. Replace `<release>` with that release,
   such as `0.7.0`, and `<platform>` with `linux-amd64`, `linux-arm64`, or `darwin-arm64`.
   Intel Macs are not supported.

   {{< command label="download" title="Download and verify the command" >}}
   gh release download <release> --repo kubebao/openbao-operator --pattern 'api-migration-<platform>'
   gh attestation verify api-migration-<platform> --repo kubebao/openbao-operator \
     --signer-workflow kubebao/openbao-operator/.github/workflows/release.yml \
     --source-ref refs/tags/<release>
   chmod +x api-migration-<platform>
   mv api-migration-<platform> api-migration
   ./api-migration version
   {{< /command >}}

2. Require a successful attestation verification. The `version` command prints the release the binary belongs
   to. The command migrates only to that release: it refuses a destination chart, or a destination operator at a
   checkpoint, of any other release.

The binary is also listed in the release's signed `checksums.txt`. See [Verify policy approval files](../../security/supply-chain/#verify-policy-approval-files)
for the checksum procedure.

### Evaluate a matching edge build

For a staging rehearsal of unreleased `main`, use the CLI, chart, and image digests from one edge commit.
An authenticated GitHub CLI session is required. The promoted `manifests-edge` Actions bundle contains the three
CLI binaries, chart, manifests, checksums, and provenance index. It is retained for seven days; the edge metadata
links to the exact bundle. The CLI version is `edge-<12-character-commit>` and must match the chart's `appVersion`
and the operator version. A release or `dev` binary does not exercise this edge version binding.

Replace `<commit>` with the selected 12-character commit and `<platform>` with `linux-amd64`, `linux-arm64`, or
`darwin-arm64`. The newest edge build's commit is the `short_sha` field of
`https://docs.kubebao.org/edge/latest/metadata.json`. If the migration host has no authenticated GitHub CLI,
download and verify on another host, then copy the directory. Download into a new directory:

```sh
curl --fail --silent --show-error \
  https://docs.kubebao.org/edge/<commit>/metadata.json --output edge-metadata.json
jq '{sha, version, chart, api_migration}' edge-metadata.json
gh run download "$(jq -er '.api_migration.run_id' edge-metadata.json)" \
  --repo kubebao/openbao-operator --name manifests-edge --dir edge-artifacts
gh attestation verify edge-artifacts/checksums.txt --repo kubebao/openbao-operator \
  --signer-workflow kubebao/openbao-operator/.github/workflows/publish-edge.yml \
  --source-ref refs/heads/main --deny-self-hosted-runners
gh attestation verify edge-artifacts/api-migration-<platform> --repo kubebao/openbao-operator \
  --signer-workflow kubebao/openbao-operator/.github/workflows/reusable-channel-hardening.yml \
  --source-ref refs/heads/main --source-digest "$(jq -er '.sha' edge-metadata.json)" \
  --deny-self-hosted-runners
(cd edge-artifacts && shasum -a 256 --check checksums.txt)   # on Linux: sha256sum --check checksums.txt
cp edge-artifacts/api-migration-<platform> ./api-migration
chmod +x ./api-migration
./api-migration version
```

The documentation site publishes the metadata after the edge build finishes. If `metadata.json` returns 404,
wait for the site deployment and retry, or select an earlier edge commit.

Require successful attestation and checksum verification and a printed version matching `version` in the selected
metadata. `gh attestation verify` exits with status 0 on success and can print nothing when its output is not a
terminal. Unpack the chart archive in that bundle and set `destinationChart` to the unpacked directory. For Argo CD or a
Flux chart source, pin the chart version or digest from the same metadata. Keep this binary and state directory for every command, including
rollback. Resuming with a binary whose destination version differs from the saved plan is refused before cluster
access. Continue with the preparation and GitOps steps below; edge delivery does not remove migration prerequisites.

## Suspend GitOps sync

1. Identify the controllers that manage the operator and its OpenBao resources, including their parents.
   [Write the configuration](#write-the-configuration) first: `plan` then prints the detected freeze requirements,
   even when an active controller blocks planning. The report lists
   parents before children and identifies unknown or remotely managed controllers. Review the detected list
   against your actual hierarchy.
2. Suspend the outermost parents, then their children. Keep them suspended until the command finishes and Git
   describes the surviving installation:
   - Set `spec.suspend: true` on Flux Kustomizations and HelmReleases, for example with
     `flux suspend kustomization <name> -n <namespace>` or `flux suspend helmrelease <name> -n <namespace>`.
     Flux sources such as GitRepository and OCIRepository only fetch artifacts and stay active.
   - Disable automatic sync on Argo CD Applications, for example with
     `argocd app set <application> --sync-policy none`, or with
     `kubectl -n argocd patch application <application> --type json -p '[{"op":"remove","path":"/spec/syncPolicy/automated"}]'`.
   - For an owning ApplicationSet, preserve the child's sync policy with an `ignoreApplicationDifferences` rule
     for `/spec/syncPolicy` that covers the Application, or an effective `create-only` or `create-delete` policy.
     The ApplicationSet controller's policy takes precedence unless policy override is enabled.
3. Confirm that parent reconciliation cannot undo the suspension. Record the original settings for the final
   hand-back. If Git declares `spec.suspend: false`, keep the child paused in the staged Git changes too.

The command checks these requirements before every step. It never changes Git or GitOps objects. If a tracked
controller runs in another cluster, suspend it there and set `"externalGitOpsSuspended": true` in the configuration.
This confirms an administrator action; it does not establish that the remote controller was checked. Leftover
tracking metadata needs the same confirmation.

## Write the configuration

1. For a Helm release, name the release and the destination chart. Replace the paths and names with your own:

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

   `destinationChart` is the unpacked chart of the destination release, for example from
   `helm pull oci://ghcr.io/kubebao/charts/openbao-operator --version <release> --untar`. The command layers the
   optional `destinationValues` over the release's current values. If those values pin `image.tag`, `image.digest`,
   `operatorVersion`, or a `helperImages` entry, set the same key in `destinationValues`, or set it to `""` to use the
   destination chart's default: `plan` refuses to keep a value of the source release. If `controller.extraEnv`
   overrides `OPERATOR_BACKUP_IMAGE`, replace that array to select a destination-compatible helper or remove the
   complete image override. A repository-only override receives the destination version tag. For manifest
   installations, change or clear the complete helper override in the destination Deployment. The command refuses
   the same source image or complete `valueFrom` reference.

   For a Flux HelmRelease, the command discovers the existing Helm storage namespace from its local controller
   metadata. Helm storage can differ from `operatorNamespace`. If the HelmRelease is remote, set
   `"releaseNamespace": "<existing-storage-namespace>"`. The active namespace is reported in
   `HelmRelease.status.storageNamespace`. Do not change the HelmRelease's storage or target namespace to run the
   migration: those changes can uninstall and reinstall the release.

2. For an installation from manifests, which has no Helm release, leave out `release`, `destinationChart`, and
   `destinationValues`. See [Migrate an installation from manifests](#migrate-an-installation-from-manifests).

3. Optional: nominate the Clusters, for example to add an OpenBao administrator token as extra evidence. The list
   must then name every Cluster. This is a fragment of the configuration:

   ```json
   "clusters": [{"namespace": "<namespace>", "name": "<cluster>", "openbao": {"tokenFile": "./<cluster>-token"}}]
   ```

   With a token, `plan` saves a Raft snapshot into the state directory and verification compares Raft membership.
   The token file must be readable only by you (mode `0600`). For a Cluster with external TLS or ACME, set
   `openbao.caFile` to the CA that issued its certificate and, if needed, `openbao.tlsServerName` to a name the
   certificate carries; the token is then optional. For an ACME Cluster, that is the ACME CA and one of its ACME
   domains. Otherwise the command reads the CA from the `<cluster>-tls-ca` Secret.

4. Optional: set `migrator` to an existing ServiceAccount, as `system:serviceaccount:<namespace>:<name>`, if you
   cannot let the command create its own. The command makes the ownership moves as this principal, and the admission
   exception admits only it. Grant it the Role in the command's
   [migrator permissions](https://github.com/kubebao/openbao-operator/blob/main/cmd/api-migration/README.md#use-an-existing-migrator) in every
   namespace with a Cluster or a restore Job. By default, the command creates the principal and removes it when
   `commit` or `rollback` completes.

## Migrate the installation

1. Plan the migration into a new, private state directory. `plan` reads the installation without changing its
   resources: it checks every resource, previews the ownership move, logs in to OpenBao as the running controller,
   and dry-runs the migrator it will create. It writes local state and any requested Raft snapshots.

   {{< command label="plan" title="Plan the migration" >}}
   ./api-migration plan --config config.json --state ./migration-state
   {{< /command >}}

   Review the summary, including the Kubernetes context, releases, affected objects, snapshot coverage, and
   skipped checks. The complete private plan remains in `<state>/plan.json`.

   `plan` refuses a source that changes while it inspects the source, including a status write by the operator.
   Plan outside scheduled backup times, or run `plan` again in a new directory.

   Configuration errors detected before state creation can be corrected using the same path. If planning leaves
   state files, keep that directory for its diagnostics or snapshots and use a new, empty directory for the next
   plan.

2. Migrate. The command stops the operator, installs the destination release with operations held, opens a narrow
   admission exception for its own migrator, re-creates every Tenant, Cluster, and restore record in `kubebao.org`,
   moves the ownership of every managed resource, and recreates each StatefulSet without restarting its Pods. It then
   starts the destination operator and verifies it.

   {{< command label="migrate" title="Move the installation" >}}
   ./api-migration migrate --state ./migration-state
   {{< /command >}}

3. Inspect the destination. Backups, restores, and other administrative operations stay held until you commit.

   {{< command label="inspect" title="Check the migrated resources" >}}
   ./api-migration status --state ./migration-state
   kubectl get openbaoclusters.kubebao.org,openbaotenants.kubebao.org,openbaorestores.kubebao.org -A
   {{< /command >}}

   After a successful `migrate`, `status` reports `Phase: Verified` and `operations hold=true`.
   The text output separates live observations from the last recorded verification. An open rollback window
   confirms that the planned source identities still exist and are not being deleted; rollback checks further
   preconditions. For automation, use `./api-migration status --state ./migration-state --output json`.

4. Commit, or [roll back](#roll-back). `commit` deletes the `openbao.org` resources without deleting what they
   managed, releases the hold, verifies again, and removes the migrator.

   Rollback ends when the API server accepts the first source resource deletion, even if that resource still
   exists with a deletion timestamp. If `commit` stops after that point, resume `commit`.

   {{< command label="commit" title="Finish the migration" >}}
   ./api-migration commit --state ./migration-state
   {{< /command >}}

Keep the state directory until the migration is complete. Only one process can use it at a time. After an
interrupted `migrate`, `commit`, or `rollback`, run the same command again with the same state directory: it
continues after the last completed step. The directory contains the private plan, including Helm values, and any
Raft snapshots. After the migration, keep or delete it as you handle backups. If you created OpenBao tokens for
`plan`, revoke them.
Exit status 3 means `--stop-after` stopped it, and exit status 4 means a checkpoint waits for you.

## Migrate an installation from manifests

Use operator-only input manifests for these checkpoints. Do not pass an entire platform bundle that also contains
`OpenBaoCluster`, `OpenBaoTenant`, or `OpenBaoRestore` objects.

The command never applies manifests itself. At each operator state it needs, it exits with status 4 and writes what
to apply into `<state-directory>/checkpoints/<operator-state>.md`. Apply it, then run the same command again:

| State | Reached at | Apply |
| --- | --- | --- |
| `stopped` | `migrate`, after it scales the source operator to zero | The destination release, including its `kubebao.org` CRDs, with every operator Deployment at zero replicas |
| `held` | `migrate`, before verification | The destination release with the controller's API migration hold |
| `normal` | `commit` | The destination release as you will run it |
| `source` | `rollback` | The source release as it was |

- For a chart rendered by Argo CD or `helm template`, set the values in
  `<state-directory>/checkpoints/<operator-state>-values.yaml` on top of your own, with the chart version and image
  of that release. With Argo CD, set them as Helm values and sync the operator resources manually, without
  pruning. At the final `normal` checkpoint, a shared Application must selectively sync only the operator
  Deployments. Use the resource selectors printed by the command; do not sync the whole Application while its
  OpenBao manifests still name the old API group.

  `plan` cannot read values that Argo CD renders. If your values pin the source release's `image` or
  `helperImages`, override them in every destination checkpoint. The command checks the running operator's release
  at each checkpoint.

  If the chart's CRDs are not installed with it, for example with Argo CD `skipCrds: true`, apply `crds.yaml` from
  the destination release with `kubectl apply --server-side -f crds.yaml` before the `stopped` checkpoint. When a
  rerun still waits at a checkpoint, the command names the unmet conditions.
- For `install.yaml` or Kustomize, add the `api-migration-stopped` or `api-migration-hold` component from the
  release's `config/components` directory. Copies are in `<state>/checkpoints/components`. If the configuration
  names `destinationManifests` and `sourceManifests`, use operator-only inputs. Planning renders and checks both
  inputs; each checkpoint checks its input again. The command refuses OpenBao custom resources and GitOps objects
  in these inputs. Each checkpoint writes a kustomization to apply with
  `kubectl apply --server-side --force-conflicts -k ./migration-state/checkpoints/<operator-state>`. Replace
  `<operator-state>` with the state in the table above. The migration command owns the
  operator Deployments' replica fields while they are stopped. Apply these reviewed operator-only inputs with
  `--force-conflicts` so the checkpoint can replace that field ownership.

{{< callout type="warning" title="Do not prune until commit" >}}
The destination release no longer contains the `openbao.org` CRDs. Pruning a CRD deletes every resource of that kind,
including the source Clusters the procedure still needs. The chart marks its CRDs `helm.sh/resource-policy: keep`,
which Helm and Argo CD honor. A Kustomize build such as `install.yaml` has no such marking, so a Flux Kustomization
or `kubectl apply --prune` would prune them: apply every checkpoint without pruning.
{{< /callout >}}

## Return control to GitOps

For a GitOps-managed installation, follow `<state>/after-commit.md` after `commit`. The command prints the same
instructions. Direct Helm and manifest installations have no GitOps hand-back file. Keep controllers frozen
while preparing these changes:

1. Set the operator to the destination release, remove the migration hold and stopped components, and restore the
   desired controller replica count. Keep the prepared destination image and helper settings. Restore any cleared
   custom `spec.backup.image` with a destination-compatible image before resuming its Cluster.
2. Change every managed OpenBao manifest to `apiVersion: kubebao.org/v1alpha1`, including shared bundles.
   `./api-migration convert --write <file>...` rewrites top-level OpenBao API versions. Review the Git diff and update
   custom RBAC rules, API clients, and scripts separately. Existing documented label, annotation, and finalizer
   names keep their `openbao.org/` prefix.
3. Stage the Git update with children still suspended. Set Flux children to `spec.suspend: true` in Git and keep
   Argo child automatic sync disabled, including ApplicationSet protection. Publish the intended chart or manifest
   artifact before a parent can resume its children.
4. Resume parents from the outermost level inward. Wait for each parent to apply the staged revision and confirm
   that children remain suspended. For Flux, wait for each source to be Ready at its current observed generation
   and to serve the intended `status.artifact.revision`. An old Ready condition alone is insufficient.
5. Review the child output, then remove child suspension in a separate Git change or resume the child manually.
   Flux keeps a suspension set with `flux suspend` or `kubectl` after Git drops the field, so also run
   `flux resume` for each child. Require reconciliation of the intended revision. A HelmRelease must see the
   destination chart before it resumes; otherwise it can install the previous chart again. Its first
   reconciliation can add a Helm revision with the same chart.

Before allowing pruning of old CRDs, confirm that no `openbao.org` resource remains in any namespace. If another
installation still needs those CRDs, keep them protected from pruning. See [Clean up after the migration](#clean-up-after-the-migration).

Do not resume controllers during the migration. They can start the previous operator or prune resources still
needed for rollback. If a controller changes a source, suspend it again, roll back while the window is open, and
create a new plan. After source retirement starts, continue with `commit`.

Imported copies do not inherit their source's GitOps tracking annotation or Flux ownership labels. The controller
tracks the destination when the reviewed Git state names it. Argo CD reports imported resources as OutOfSync until
their first sync adds the tracking annotation; that sync does not change their specifications.

Checkpoint values set directly on a live Argo CD Application do not stay: a parent that applies the staged
revision with server-side apply replaces the Application's source list. Confirm that the live source matches Git.

## Roll back

Roll back before `commit` starts deleting source resources, including after an interrupted `migrate`. `rollback`
moves ownership back, restores the source release, deletes the `kubebao.org` resources without deleting what they
managed, and verifies the source.

{{< command label="rollback" title="Return to openbao.org" >}}
./api-migration rollback --state ./migration-state
{{< /command >}}

Rollback waits for the source operator's rollout before it verifies the source.

For a GitOps-managed installation, follow `<state>/after-rollback.md` after a successful rollback. Restore the
source chart or manifests, source image and helper pins, and `openbao.org/v1alpha1` manifests in Git. Revert any
prepared destination changes in Git. Use the same staged parent/source/child sequence as the commit hand-back.
If you staged no destination change, Git still describes the source and needs no change. Confirm that each live
Argo CD Application source matches Git. If a live Application keeps checkpoint values, follow the parent and child
steps of the hand-back to replace them. Then confirm that each controller's desired state matches the live source
and restore the recorded sync settings. For Flux, run `flux resume` for each object you suspended, parents first.

To migrate again after a rollback, create a new plan in a new state directory. Rollback keeps the `kubebao.org`
CRDs, which the next migration uses. While both groups are installed, qualify resource names with their group.

For every installation, restore the desired controller replica count and any cleared source backup pins before
resuming paused Clusters.

After the API server accepts the first source deletion, rollback is no longer possible. Continue with `commit`.
If the migration cannot finish, restore from a backup, or from the Raft snapshot `plan` saved, as a last resort.

## Clean up after the migration

The `openbao.org` CRDs remain after `commit`. While they exist, qualify resource names with their group, such as
`kubectl get openbaoclusters.kubebao.org`, because the short names match both groups. Helm and Argo CD keep the
chart's CRDs. A Flux Kustomization that applies a Kustomize build prunes them when you resume sync after the Git
update, so check the first step below before you resume.

1. Confirm that no `openbao.org` resource remains anywhere in the cluster:

   {{< command label="verify" title="List remaining openbao.org resources" >}}
   kubectl get openbaoclusters.openbao.org,openbaotenants.openbao.org,openbaorestores.openbao.org -A
   {{< /command >}}

2. When the list is empty, delete the CRDs.

   {{< callout type="warning" title="Deleting a CRD deletes its resources in every namespace" >}}
   Delete the `openbao.org` CRDs only after every installation in the cluster has migrated. A CRD deletion removes
   every remaining resource of that kind, and the finalizers of those resources can block it.
   {{< /callout >}}

   {{< command label="delete" title="Remove the openbao.org CRDs" >}}
   kubectl delete crd openbaoclusters.openbao.org openbaotenants.openbao.org openbaorestores.openbao.org
   {{< /command >}}

A migrated Target-mode Cluster keeps the controller identity of the Cluster it continues. See
[operator authentication](../../get-started/operator-authentication/).
