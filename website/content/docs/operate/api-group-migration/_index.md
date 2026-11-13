---
title: Migrate to the kubebao.org API group
description: Prepare a 0.6.1 API group migration and choose the guide for your installation method.
eyebrow: Operate · API group migration
weight: 47
hideChildren: true
verifiedBy:
  - cmd/api-migration/main.go
  - cmd/api-migration/config.go
  - cmd/api-migration/procedure.go
  - cmd/api-migration/verification.go
  - cmd/api-migration/helm.go
  - cmd/api-migration/install.go
  - cmd/api-migration/gitops.go
  - cmd/api-migration/convert.go
  - cmd/api-migration/primitives/inspect.go
  - cmd/api-migration/e2e_test.go
  - hack/ci/build-api-migration.sh
  - hack/ci/operator-upgrade-e2e.sh
  - .github/workflows/release.yml
  - .github/workflows/publish-edge.yml
  - .github/workflows/reusable-channel-hardening.yml
---

Starting with 0.7, the operator serves `OpenBaoCluster`, `OpenBaoTenant`, and `OpenBaoRestore` as
`kubebao.org/v1alpha1` instead of `openbao.org/v1alpha1`. Use the destination 0.7 release's `api-migration` command
to move an existing 0.6.1 installation. It re-creates the custom resources and transfers workload ownership while
keeping OpenBao data and storage in place. The destination operator can then roll the data Pods to its own template.
Read [Plan for availability](#plan-for-availability) before scheduling the migration.

{{< callout type="warning" title="Do not upgrade a 0.6 installation in place" >}}
A `helm upgrade` to 0.7, or applying the 0.7 manifests, starts an operator that does not reconcile the existing
`openbao.org` resources. Their workloads keep running without an operator. Use the migration command instead.
{{< /callout >}}

## Choose your installation route

Review the shared preparation on this page, then follow one installation guide. Each guide contains its
configuration, migration, rollback, and cleanup sequence.
Replace angle-bracket placeholders in examples with your installation's names, paths, and artifact references.

### Helm CLI {#helm-cli-checklist}

Follow [Migrate a Helm CLI installation](helm/). Set `release` to the existing Helm release. The command runs Helm 4
and applies the operator transitions itself.

### kubectl or Kustomize

Follow [Migrate a kubectl or Kustomize installation](kubectl/). Omit `release` and apply each operator-only
manifest checkpoint manually. This route has no GitOps controller.

### Flux HelmRelease {#flux-helmrelease-checklist}

Follow [Migrate a Flux HelmRelease installation](flux-helmrelease/). Set `release` to the existing Helm release.
The command runs Helm 4 while the HelmRelease and its parents remain suspended.

### Flux Kustomization {#flux-kustomization-checklist}

Follow [Migrate a Flux Kustomization installation](flux-kustomization/). Omit `release`, suspend the Kustomizations,
and apply operator-only manifest checkpoints manually.

### Argo CD {#argo-cd-checklist}

Follow [Migrate an Argo CD installation](argocd/). Omit `release`, even when Argo CD renders a Helm chart.
Argo CD applies the rendered resources without creating Helm release storage. Apply each checkpoint manually.

## Before you start

Meet these conditions before running `plan`. Your installation guide includes the configuration and suspension steps.

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

Select the migration cluster's kubeconfig and context for every operator, OpenBao, and checkpoint command.
Use the same values in `config.json`; the migration configuration does not change your shell's Kubernetes context.
If a GitOps controller runs elsewhere, use its own cluster context for controller suspension and hand-back.

{{< command label="context" title="Select the migration cluster" >}}
export KUBECONFIG="/path/to/admin.kubeconfig"
kubectl config use-context "<context>"
kubectl config current-context
{{< /command >}}

Confirm that the printed context identifies the intended cluster. Keep this selection for the whole procedure.

Before the maintenance window, review destination configuration and any retained OpenBao manifests. Search for
`openbao.org` API references in custom RBAC, clients, and scripts. Prepare their updates, but keep converted OpenBao
manifests inactive until commit finishes. The migration command imports the new-group resources itself.

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
  connects through the Service, an ingress, or a Gateway. Use a client that follows OpenBao redirects and retries
  operations according to their API semantics.
- `rollback` starts the source operator again. It renders the source Pod template and rolls the data Pods a
  second time.

To choose when that rollout happens, pause the Cluster before `plan` and resume it in a maintenance window after
`commit`. A paused Cluster is not re-rendered, and it takes no scheduled backups while paused.

An idle BlueGreen workload can retain old Pod revisions with `OnDelete`. An idle rolling workload can also hold
older Pods behind a partition. Migration preserves those settings and does not require those Pods to be replaced.
A rollout that has not reached the state required by its update strategy must settle first.

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
terminal. Use the matching chart, manifests, and image digests in your installation guide. Keep this binary and
state directory for every command, including rollback. Resuming with a binary whose destination version differs
from the saved plan is refused before cluster access. Edge delivery does not remove migration prerequisites.

## Write the configuration

Start with the complete `config.json` example in your installation guide. Keep the installation's namespace,
tenancy, TLS settings, required environment, and resource overrides. Add the optional settings below when needed.

### Replace source image pins

For Helm destination values, replace or clear any source `image.tag`, `image.digest`, `operatorVersion`, and
`helperImages` pins. Setting a key to `""` selects the destination chart's default. If `controller.extraEnv` overrides
`OPERATOR_BACKUP_IMAGE`, replace that array with a destination-compatible helper or remove the complete override.
A repository-only override receives the destination version tag.

For manifest installations, change or clear the complete source helper override in the destination Deployment.
The command refuses an unchanged source image or complete `valueFrom` reference. Argo CD values must also replace
source image and helper pins; `plan` cannot read the values Argo CD renders.

### Add administrator access or an existing migrator

1. Optional: nominate the Clusters, for example to add an OpenBao administrator token as extra evidence. The list
   must then name every Cluster. This is a fragment of the configuration:

   ```json
   "clusters": [{"namespace": "<namespace>", "name": "<cluster>", "openbao": {"tokenFile": "./<cluster>-token"}}]
   ```

   With a token, `plan` saves a Raft snapshot into the state directory and verification compares Raft membership.
   The token file must be readable only by you (mode `0600`). For a Cluster with external TLS or ACME, set
   `openbao.caFile` to the CA that issued its certificate and, if needed, `openbao.tlsServerName` to a name the
   certificate carries; the token is then optional. For an ACME Cluster, that is the ACME CA and one of its ACME
   domains. Otherwise the command reads the CA from the `<cluster>-tls-ca` Secret.

2. Optional: set `migrator` to an existing ServiceAccount, as `system:serviceaccount:<namespace>:<name>`, if you
   cannot let the command create its own. The command makes the ownership moves as this principal, and the admission
   exception admits only it. Grant it the Role in the command's
   [migrator permissions](https://github.com/kubebao/openbao-operator/blob/main/cmd/api-migration/README.md#use-an-existing-migrator) in every
   namespace with a Cluster or a restore Job. By default, the command creates the principal and removes it when
   `commit` or `rollback` completes.

## Suspend GitOps sync

The [Flux HelmRelease](flux-helmrelease/#suspend-reconciliation),
[Flux Kustomization](flux-kustomization/#suspend-reconciliation), and
[Argo CD](argocd/#suspend-reconciliation) guides give the suspension steps for their controllers.
Suspend every controller that manages the operator or its OpenBao resources, including outer parents.
Record the original settings and keep children paused in staged Git changes so parents cannot resume them.

The command checks the freeze requirements before every step and never changes Git or GitOps objects.
If a tracked controller runs in another cluster, suspend it there and set `"externalGitOpsSuspended": true`
in `config.json`. This confirms an administrator action; it does not establish that the remote controller was
checked. Leftover tracking metadata requires the same confirmation.

## Migrate the installation

Use your installation guide for the commands and expected results. All routes use the same lifecycle:

| Command | Result |
| --- | --- |
| `plan` | Inspect the source, log in as its controller, dry-run migrator permissions, and save private state and requested Raft snapshots. |
| `migrate` | Stop the source operator, import resources, transfer ownership, start the destination with operations held, and verify it. |
| `status` | Report live identities, operator state, the rollback window, recorded verification, and next actions. |
| `commit` | Retire source resources, release the hold, verify again, and remove the migrator. |
| `rollback` | Restore source ownership and 0.6.1 while the rollback window is open. |

`plan` does not change installation resources. It refuses a source that changes during inspection, including a
status write by the operator. Plan outside scheduled backup times. If planning writes artifacts and then fails,
keep that directory for diagnostics and create a new plan in a new, empty directory. Configuration errors detected
before state creation can be corrected using the same path.

Keep the state directory until the migration is complete. Only one process can use it at a time. After an
interrupted `migrate`, `commit`, or `rollback`, run the same command again with the same state directory: it
continues after the last completed step. The directory contains the private plan, including Helm values, and any
Raft snapshots. After the migration, keep or delete it as you handle backups. If you created OpenBao tokens for
`plan`, revoke them.
Exit status 3 means `--stop-after` stopped it, and exit status 4 means a checkpoint waits for you.

## Migrate an installation from manifests

Argo CD, Flux Kustomization, and direct manifest installations apply operator checkpoints manually. The command
exits with status 4 and writes `<state>/checkpoints/<operator-state>.md`. Apply only the requested operator state,
without pruning, then rerun the same command. Your installation guide gives the application steps.

| State | Reached at | Apply |
| --- | --- | --- |
| `stopped` | `migrate`, after it scales the source operator to zero | The destination release, including its `kubebao.org` CRDs, with every operator Deployment at zero replicas |
| `held` | `migrate`, before verification | The destination release with the controller's API migration hold |
| `normal` | `commit` | The destination release as you will run it |
| `source` | `rollback` | The source release as it was |

{{< callout type="warning" title="Apply only operator resources at checkpoints" >}}
Do not apply a full platform bundle, converted OpenBao resources, or GitOps objects. The command imports the new
resources itself. At the final checkpoint, unconverted Git can recreate source resources that commit has retired.
Do not prune old CRDs: pruning deletes their resources, including sources still needed for rollback.
{{< /callout >}}

## Return control to GitOps

After commit, follow `<state>/after-commit.md`; after rollback, follow `<state>/after-rollback.md`.
These files are written for tracked GitOps installations. Direct Helm and manifest installations have no hand-back file.

Prepare Git for the surviving installation while children remain suspended. Resume parents from the outermost
level inward, require the intended source artifacts, then restore child reconciliation. Keep pruning disabled until
the reviewed desired state matches the surviving installation. Before allowing old CRD pruning, confirm that no
`openbao.org` resource remains anywhere in the cluster.

Use the hand-back procedure for [Flux HelmRelease](flux-helmrelease/#commit-and-return-control-to-flux),
[Flux Kustomization](flux-kustomization/#commit-and-return-control-to-flux), or
[Argo CD](argocd/#commit-and-return-control-to-argo-cd). Their rollback sections also cover returning to 0.6.1.

Do not resume controllers during migration. If one changes a source, suspend it again, roll back while the window
is open, and create a new plan. After source retirement starts, continue with `commit`.

## Roll back

Each installation guide includes its rollback commands and source hand-back. Rollback restores source ownership,
the original 0.6.1 installation, and source-compatible helper pins. It waits for the source rollout before verification.
The `kubebao.org` CRDs remain installed. To migrate again, create a new plan in a new state directory.

{{< callout type="warning" title="Commit closes rollback at the first accepted source deletion" >}}
After the API server accepts the first source deletion, rollback is no longer possible, even while deletion is
pending. Continue with `commit`. If the migration cannot finish, recover from a backup or a Raft snapshot saved by
`plan` as a last resort.
{{< /callout >}}

## Clean up after the migration

The `openbao.org` CRDs remain after `commit`. While they exist, qualify resource names with their group, such as
`kubectl get openbaoclusters.kubebao.org`, because the short names match both groups. Helm and Argo CD keep the
chart's CRDs. A Flux Kustomization can prune them when it applies the destination build. Complete the empty-resource
check before allowing that pruning, as described in its installation guide.

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

## Migration tool lifecycle

0.8.0 is the target for removing `api-migration` from `main`. The planned upgrade path for 0.8.0 and later is
`0.6.1 → latest supported 0.7.x → destination release`. Use the migration binary that matches the 0.7.x destination,
finish `commit`, and then follow the destination release's normal upgrade procedure. Removing the tool requires
qualification and documentation of this upgrade path.

The published 0.7.x migration artifacts and versioned guide remain available. Maintainers must announce the end date
of 0.7.x migration support before publishing 0.8.0. Removing the tool from `main` does not end that support window or
remove the runtime compatibility that migrated Clusters need.
