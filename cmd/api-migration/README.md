# API group migration

`api-migration` moves an OpenBao Operator installation from `openbao.org/v1alpha1` to `kubebao.org/v1alpha1`.
It re-creates the custom resources and transfers workload ownership while keeping OpenBao data and storage in place.

**Follow the [migration guide](../../website/content-versions/next/docs/operate/api-group-migration/_index.md) to run it.**
The overview links to installation guides for Helm CLI, kubectl/Kustomize, Flux HelmRelease, Flux Kustomization,
and Argo CD. Each guide covers configuration, checkpoints, rollback, and cleanup for that installation method.
A direct Helm upgrade from 0.6 to 0.7 leaves the old resources unmanaged.

## Scope

The only source release is **0.6.1**. Each 0.7.x release binary accepts only its own destination release.
Supported CLI platforms are Linux amd64, Linux arm64, and macOS arm64. Intel Macs are unsupported.

Edge builds bind the binary to `edge-<12-character-commit>`. Download the verified Actions bundle linked from the
selected edge metadata and use its matching chart and images; see the guide's
[edge rehearsal procedure](../../website/content-versions/next/docs/operate/api-group-migration/_index.md#evaluate-a-matching-edge-build).
Saved plans require the same destination version when resuming, including rollback and status.

The scope includes Helm and manifest installations, single-tenant and multi-tenant deployments, Development and
Hardened profiles, voters and read replicas, Target and Shared controller JWT modes, paused Clusters, backups,
and finished restore records. See the [configuration coverage map](#configuration-coverage-map)
for automated scenarios and remaining environment-specific qualification.

## Prerequisites

- Upgrade the source operator to 0.6.1. Keep its controller at one replica in the source Helm values or manifests
  until `commit` or `rollback` finishes. Enable its normal admission policies.
- Require every Cluster to be healthy and idle, or paused at its observed generation. Finish pending operations
  and restore decisions. Unknown finalizers, unsupported ownership, and admission-disabled installations are excluded.
- Have a recent backup and a rehearsed restore path. Optional OpenBao administrator tokens let `plan` save Raft
  snapshots and let verification compare Raft membership.
- Prepare the destination chart or operator-only manifests. Replace source image, helper, and `operatorVersion`
  pins as described in the guide. Clear Cluster-level backup-helper pins through its pause procedure.
- Have cluster administrator access and `kubectl`; Helm installations also require Helm 4.
- Suspend every GitOps controller that manages the installation, including parents and remote controllers.
  Keep them suspended through migration and hand-back preparation.

Use the guide's [preflight instructions](../../website/content-versions/next/docs/operate/api-group-migration/_index.md#before-you-start)
for the complete checks and the Kubernetes context selection.

## Command flow

| Command | Result |
| --- | --- |
| `plan` | Checks the installation without changing its resources. Writes a private local plan and any requested Raft snapshots. |
| `migrate` | Stops the source operator, imports resources, transfers ownership, starts the destination with operations held, and verifies it. |
| `status` | Reports live identities, operator state, the rollback window, recorded verification, and next commands. |
| `commit` | Deletes source resources without cascading, releases the hold, verifies again, and removes the migrator. |
| `rollback` | Restores source ownership and 0.6.1 while the rollback window remains open. |
| `convert` | Rewrites the API version in manifest files. `--write` updates files in place; review the Git diff. |
| `version`, `help` | Prints the binary's release or command help without cluster access. |

`plan` and `status` support `--output json`. Their public summaries omit private Helm values.
`status` labels the last successful verification as recorded evidence; it does not repeat migration verification.
Run `api-migration help <command>` for flags.

## Installation routes

| Installation | Operator transitions |
| --- | --- |
| Helm CLI or Flux HelmRelease | Set `release`. The command drives Helm while GitOps stays suspended. |
| Chart rendered by Argo CD or `helm template` | Omit `release`. Apply each printed checkpoint with operator-only values or selective syncs. |
| `install.yaml` or Kustomize | Omit `release`. Apply each printed checkpoint to reviewed operator-only manifests. |

For manifests, exit status **4** requests an operator state: `stopped`, `held`, `normal`, or `source`.
Read `<state>/checkpoints/<state>.md`, apply its instructions, and rerun the same command.
The command verifies the live state before continuing. It never applies manifest checkpoints itself.

A Flux HelmRelease's Helm storage namespace can differ from the operator namespace. The command discovers it
locally or accepts `releaseNamespace`. Keep the existing storage and target namespaces; changing them can reinstall
the release. Helm transitions use server-side apply with forced field ownership. Resolve a `pending-*` revision
before retrying; a `failed` revision can be upgraded past.

### GitOps hand-back

Checkpoints must change only the operator, without pruning or applying OpenBao custom resources.
A shared Application or Kustomization needs operator-only actions at every checkpoint.

After `commit`, follow `<state>/after-commit.md`; after rollback, follow `<state>/after-rollback.md`.
These files are written for tracked GitOps installations. Update Git while children remain suspended, then resume
parents and sources before children. A Flux HelmRelease must wait until its chart source serves the destination.
The tool never edits Git or changes GitOps objects. Remote suspension relies on administrator confirmation.
See [Return control to GitOps](../../website/content-versions/next/docs/operate/api-group-migration/_index.md#return-control-to-gitops).

## State and recovery

Keep the state directory private. It contains the plan, Helm values, any Raft snapshots, and saved StatefulSet
manifests. Keep the directory until migration completes; only one process can use it at a time.

After an interrupted `migrate`, `rollback`, or `commit`, rerun that command with the same binary and state directory.
Each step observes the installation before acting. Exit status **3** is an intentional `--stop-after` stop.
If planning fails after writing artifacts, retain the failed directory and plan again in a new, empty directory.
Invalid configuration is rejected before state creation; the tool never clears existing state.

**Rollback ends at the first source deletion accepted by the API server during `commit`**, including a deletion
still waiting on finalizers. Continue with `commit` after that point. Backup recovery is the last resort if the
migration cannot finish.

## Availability

The ownership transfer recreates StatefulSets without restarting their Pods. Data, PVC/PV bindings, Raft membership,
seal material, Service endpoints, and workload names stay in place.

The destination operator can roll data Pods to its own template after it starts. A single-replica Cluster is
unavailable during replacement; a three-voter Cluster retains quorum during the rolling update.
To choose the rollout time, pause the Cluster before `plan` and resume it after `commit` in a maintenance window.
Paused Clusters do not take scheduled backups. Process recovery checks alone do not prove continuous availability;
see the [availability probe](#run-the-scenarios).

## Handoff contract

- Delete custom resources only with Orphan propagation, UID preconditions, and no remaining controlled resources.
  Retire source Clusters before source restore records and Tenants; rollback needs every source Cluster.
- Start an operator only after managed-resource ownership has moved to its group. Each provisioner re-renders
  tenant RBAC for its own group.
- Preserve controller authentication. Imported Target-mode Clusters retain their original JWT audience through
  the reserved, immutable `migration.kubebao.org/source-uid` marker. Verification logs in as the running controller.
- Open the live admission handoff exception only while every operator Deployment has no Pods. Admit only the
  migrator's ownership and provenance changes and exact StatefulSet recreation. Close the exception before startup;
  an operator refuses a policy that still contains it.

OpenBao calls use the tool's own Pod port-forward, tolerate standby read lag, and reconnect after Pod replacement.

## Development and qualification

Build a development binary from the repository root:

```sh
go build -mod=vendor -o bin/api-migration ./cmd/api-migration
```

Development binaries report `dev` and do not enforce a release-matched destination.
`hack/ci/build-api-migration.sh <directory> <version>` builds the three reproducible release payloads.
Older development plans without the source backup-helper selection cannot advance. Roll back and create a new
plan; if source retirement has started, finish `commit` with the original binary.

### Configuration coverage map

The rows identify automated scenarios and remaining qualification work. A scenario's presence in the suite is not
evidence that it passed for a particular candidate. Record the tested source release, destination tree, images,
platform, and results when qualifying a release. The full cross-product of these rows is not tested.

| Surface | Automated migration coverage | Remaining qualification |
| --- | --- | --- |
| Install and GitOps | Helm CLI; rendered chart; Kustomize; live Argo CD 3.4/3.5/3.6; Flux 2.9/2.10 Kustomizations and HelmReleases; shared operator/CR bundles; staged resume with Git-owned suspension and separate Helm storage | Remote GitOps controllers rely on administrator confirmation; custom parent arrangements need staging checks |
| Tenancy and topology | Single-tenant; central and self-service Tenants; three voters; one read replica; multiple Clusters; `operator-ha` prepares source Helm values at one controller replica, rolls back, commits, and restores two replicas | Larger topologies and custom namespace arrangements need staging runs |
| Update strategy | `bluegreen`: revisioned `OnDelete` voters with old Pod revisions; `partitioned`: an idle transition retaining them behind a rolling partition; both retain the upgrade ServiceAccount and migrate, roll back, and commit | Active upgrade operations remain excluded |
| Controller authentication | Target, explicit Shared, and legacy omitted mode; audience preservation and successful controller logins | Custom issuer, audience, and projected-token integrations need a combined staging run |
| Profiles and TLS | Development with operator-managed TLS; Hardened with ACME/Transit; external TLS in operator-upgrade E2E | Other private CA, listener, and issuer combinations need staging checks |
| Combined GitOps configuration | Development and three-voter Hardened ACME/Transit Clusters through Flux 2.10 Kustomization, Flux HelmRelease, and Argo CD 3.6; rollback and commit, then sync resumes | Other controller versions, parent arrangements, and remote management need staging runs |
| OpenBao versions | Default 2.6.5; `openbao-2.7`: 2.7.2 with three voters and one read replica, rollback and commit | Other supported patches are not individually migration-gated |
| Kubernetes and architecture | Kind 1.36.1; the general e2e suites cover the other supported Kubernetes lines; supported minimum is Kubernetes 1.34 | OpenShift and other distributions need target-cluster qualification |
| Networking and metrics | API-server forward/reverse transfers with RBAC and admission for Ingress, HTTPRoute, TLSRoute, BackendTLSPolicy, ServiceMonitor, and CiliumNetworkPolicy | Live ingress/Gateway/Cilium controllers, traffic continuity, and post-commit metrics scraping |
| Unseal | Static; Transit with namespace-local credentials; `pkcs11` with SoftHSM and a pinned KMS plugin; `kmip` with PyKMIP and Secret-mounted mTLS credentials; both replace a Pod after rollback and commit | Cloud KMS and workload identity, OCI, vendor HSMs, and production KMIP systems need target-environment runs |
| Storage | Bound PVC/PV identity checks; local-path data claims; retained RWX ACME cache claim; `audit-managed` and `audit-existing` with three voters and one read replica, retained audit records, and post-rollback/post-commit Pod replacement | The audit fixture uses single-node hostPath storage and does not qualify production RWX or CSI; storage backends, access modes, and cross-node restart and recovery need target-environment runs |
| Backup and restore | S3-compatible RustFS backups; finished in-place restore; retained fresh-target Resume handoff before migration, rollback and commit; post-migration operator-scheduled disposable restore with data and cleanup checks | Cloud object-store credentials and storage/provider combinations need target-environment runs |
| Release command and recovery | `cli` packages the three supported release binaries and runs the native versioned executable, rejects a mismatched destination, checks exit statuses and process locking, and restarts after SIGTERM/SIGKILL at accepted API writes; `cli-manifests` checks checkpoint exits and resumption | Verification of final release artifacts |
| Availability | Authenticated write/read probes through Services in probe-enabled Development and Hardened scenarios; zero custody failures required; HA rollout recovery within ten seconds and bounded sampling gaps | `cli` and `cli-manifests` have no continuous probes; single-replica outages are measured separately; live ingress/Gateway paths and production workload latency need staging runs |
| Private images, plugins, and custom runtime | Exact source spec and StatefulSet preservation; signed published images in Hardened cases; pinned PKCS#11 plugin fixture | Disconnected registries, private verification credentials, plugin download paths, other external plugins, and custom runtime combinations |

Run targeted combined scenarios for the rows affected by an installation. Keep provider or platform claims pending
until the run uses that provider or platform. Unknown finalizers, unsupported ownership, active operations, and
admission-disabled installations are exclusions.

### Run the scenarios

`e2e_test.go` runs each case on a new Kind cluster against the released 0.6.1 chart and a destination image. The
nightly workflow runs every case; the release-candidate workflow runs a core subset:

```sh
docker build -t api-migration.local/operator:dev .
docker build -f Dockerfile.backup -t api-migration.local/backup:dev .
make docker-build-e2e-openbao-softhsm OPENBAO_SOFTHSM_IMG=api-migration.local/softhsm:fixture
make docker-build-e2e-pykmip-server PYKMIP_SERVER_IMG=api-migration.local/pykmip:fixture
make test-e2e-api-migration API_MIGRATION_E2E_IMAGE=api-migration.local/operator:dev \
  API_MIGRATION_E2E_BACKUP_IMAGE=api-migration.local/backup:dev \
  API_MIGRATION_E2E_SOFTHSM_IMAGE=api-migration.local/softhsm:fixture \
  API_MIGRATION_E2E_KMIP_IMAGE=api-migration.local/pykmip:fixture   # API_MIGRATION_E2E_CASE=commit runs one
# Select another Kubernetes node image.
make test-e2e-api-migration API_MIGRATION_E2E_IMAGE=api-migration.local/operator:dev \
  API_MIGRATION_E2E_BACKUP_IMAGE=api-migration.local/backup:dev \
  API_MIGRATION_E2E_CASE=recovery API_MIGRATION_E2E_NODE_IMAGE=kindest/node:v1.35.1
```

The Make target saves probe logs and timing summaries under `dist/test/api-migration`. Override
`API_MIGRATION_E2E_ARTIFACT_DIR` to select another directory. CI jobs retain these
observations for seven days, including failed observations. They exclude credentials and migration state.

- `commit`: migrate and commit.
- `rollback`: migrate, let the destination roll the data Pods, roll back, and plan again.
- `interrupt`: stop after every `migrate` step and roll back each time. Then stop between a StatefulSet's orphan
  delete and its recreation, where its Pods have no controller, and roll back; stop there again, resume, and commit.
- `ha`: three voters, a read replica, and legacy restore history.
- `bluegreen` and `partitioned`: three voters on a revisioned StatefulSet. A source template change leaves healthy
  Pods on their old revision. The first case keeps `OnDelete`; the second changes to RollingUpdate with a partition
  holding all three Pods. Migrate, roll back, migrate, and commit; every voter Pod UID stays unchanged.
- `openbao-2.7`: OpenBao 2.7.2 with three voters and one read replica; migrate, roll back, migrate, and commit.
- `audit-managed` and `audit-existing`: file auditing with a managed or externally owned RWX claim, three voters,
  and one read replica. Migrate, roll back, migrate, and commit; preserve data and audit claim bindings, per-Pod
  mounts, and audit history. Replace a voter and read replica after rollback and commit, then verify new audit
  writes. The single-node hostPath fixture does not qualify production shared storage or cross-node attachment.
- `pkcs11` and `kmip`: one initialized voter with SoftHSM and the pinned PKCS#11 plugin, or an external PyKMIP
  server with mTLS credentials. Migrate, roll back, migrate, and commit; preserve credential Secrets, seal settings,
  key material, and data storage. Replace the voter after rollback and commit and require unseal, retained data,
  and controller login. Selected cases fail if their fixture image is missing. Vendor HSMs and production KMIP
  servers require separate qualification.
- `cli`: package the release binaries, execute the native 0.7.0 binary, reject a 0.7.1 destination, and check process
  locking and exit statuses. Kill the process after accepted StatefulSet deletion and roll back; terminate it after
  recreation and resume; kill it after source retirement, refuse rollback, and finish commit.
- `cli-manifests`: run the native release binary against a rendered-chart installation. Require exit status 4
  at the stopped, held, source, and normal checkpoints, apply the requested state, and resume through rollback and commit.
- `helm-csa`: record a client-side-applied source release, migrate with explicit server-side apply, roll back to
  its source revision, migrate again, and commit. Each resulting revision must use server-side apply.
- `operator-ha`: start with two controller replicas, require preparation in the source Helm values, migrate and
  roll back to one replica, migrate and commit, then restore two replicas in the surviving release.
- `restore-lifecycle`: create and resume a retained fresh-target restore on 0.6.1 with OpenBao 2.7.2. Migrate both
  Clusters and the completed record, refuse a CR-level source helper pin, roll back, migrate and commit. The source
  and destination use their own release-level backup helpers. Then require an operator-scheduled disposable
  restore to reproduce new data, report success, and remove its request, target, PVC, and executor Job.
- `backups`: a backup before the migration and another one after `commit`.
- `restores`: an in-place restore resumed by the administrator, then migrate, roll back, migrate, and commit. The
  destination controller then adopts the record, deletes it with its Job, and takes a backup.
- `tenants`: a multi-tenant installation with a central and a self-service Tenant, then migrate, roll back, migrate,
  and commit. Each side's provisioner must render the tenant RBAC for its own group.
- `chart` and `kustomize`: installations from manifests, the chart rendered with `helm template` as Argo CD does and
  `config/default` with the shipped components. Each runs migrate, rollback, migrate, and commit, and applies every
  checkpoint state.
- `legacy`: a Cluster created by the previous stable release (implicit Shared JWT), upgraded, then migrated.
- `paused`: the running Cluster paused and a second one paused before it initialized, then migrate, roll back,
  migrate, and commit with both paused. After `commit` both resume: the second one initializes under `kubebao.org`.
- `recovery`: two Clusters in one installation. `migrate` loses the response to a StatefulSet recreation the API
  server applied, then resumes. `rollback` stops at every step, between two ownership moves back, and after a lost
  response to one. `commit` refuses a finalizer added to the later Cluster before deleting either source. It stops
  at every step and after the first source deletion, where rollback must refuse, then
  loses the response to the second deletion; running it again finishes.
- `argocd-3.4`, `argocd-3.5`, `argocd-3.6`, `flux-2.9`, `flux-2.10`, `flux-2.9-helmrelease`, `flux-2.10-helmrelease`:
  a live controller of that version installs the operator and the OpenBao resources from an OCI registry inside the
  cluster, through a parent, everything syncing automatically with pruning. With Argo CD, an app-of-apps renders the
  chart. With Flux, a parent Kustomization applies the operator and a Kustomization of the OpenBao resources: in
  `flux-2.9` and `flux-2.10`, a Kustomization of the Kustomize build (`install.yaml`) in multi-tenant mode; in the
  `-helmrelease` cases, a HelmRelease of the chart in single-tenant mode, which the procedure upgrades and rolls back
  as a Helm release. The 0.6.1 chart includes the admission policy post-rendering fix (#934). `plan` must refuse while
  the controllers sync, and while only the children are suspended; Argo CD's parent heals them back. The Argo CD and
  Flux Kustomization cases go through the manifest checkpoints, Argo CD with manual syncs that keep the `openbao.org`
  CRDs. Flux rolls back and resumes sync, taking the source back over. Resuming one child mid-procedure stops
  `commit`. After `commit`, the manifests `convert` rewrote and the destination release are
  pushed and sync resumes: the controllers end synced and the Cluster keeps its object, its claim, and its data.
  A Flux Kustomization prunes the empty `openbao.org` CRDs. A HelmRelease keeps them, because Helm never deletes the
  CRDs in a chart's `crds` directory. Argo CD keeps them, because the chart marks them `helm.sh/resource-policy: keep`,
  which it treats as `Delete=false`.
- `hardened`: two Clusters in one installation, the Development one and a Hardened one with three voters, OpenBao's
  native ACME TLS, and Transit unseal, both served by an infra-bao fixture. Migrate, roll back, migrate, and commit;
  the ACME integration must stay ready and its cache claim must follow the active Cluster. The Hardened Cluster runs
  the published, signed images, so this case needs registry access.
- `flux-2.10-hardened`: the Hardened and Development Clusters in a multi-tenant installation managed by a live Flux
  2.10 Kustomization and its parent. Git manages both Clusters and the Tenants. The case checks rollback, commit,
  ACME cache custody, tenant RBAC, and GitOps resume.
- `flux-2.10-helmrelease-hardened` and `argocd-3.6-hardened`: the same mixed profiles through a HelmRelease and
  an Argo CD rendered chart, including rollback and GitOps resume.
- `argocd-3.6-shared` and `flux-2.10-shared`: one child Application or Kustomization manages both the operator and
  its OpenBao resources. Operator-only checkpoints, rollback, commit, and GitOps resume preserve destination
  identities and data. The Argo CD case also recreates a retired source through an unsafe whole-Application sync,
  requires final verification to refuse its new UID, removes only that test-created replacement with Orphan
  deletion, and resumes the same commit.
- `flux-2.10-helmrelease-explicit-suspend`: Git declares `suspend: false`, while Helm release records use the default
  HelmRelease namespace and the operator runs elsewhere. After commit, Git stages `suspend: true` for the child,
  advances the parent and chart source to the destination artifact, then restores `suspend: false`.

The availability probe acknowledges writes and reads a stable canary value. Standby reads are eventually
consistent, so it does not assume that a read returns the latest version of the preceding write. Each request
has a five-second timeout and no retries, so a slow request on a busy node does not count as an outage. Custody transfer requires zero failed operations. Sampling gaps and HA
rollout outages must not exceed ten seconds. The audit and seal cases replace Pods after rollback and commit,
outside these probe windows. Exact restored values have separate lifecycle assertions.

Each case checks the following on the surviving Cluster:
- data written before and during the migration is still readable;
- a new specification change is reconciled;
- a changed autopilot setting reaches OpenBao, which proves the surviving controller logs in with its JWT;
- the claim is still bound.

The `legacy` case runs with the minimal config: no Cluster list and no administrator token.

`hack/ci/operator-upgrade-e2e.sh` also runs the command on every pull request that changes it: while
`OPERATOR_UPGRADE_API_MIGRATION_SOURCE` is set, the operator upgrade job establishes that release from the reviewed
baseline chart and image digests, then moves it to the candidate with `plan`, `migrate`, and `commit`. Those fixtures
add a Hardened Cluster with Transit unseal and external TLS, a legacy Development Cluster, and a paused Cluster.

## Retirement policy

**0.8.0 is the target for removing `api-migration` from `main`.** The 0.7.x release line remains the migration
bridge from 0.6.1. After removal, the upgrade path is `0.6.1 → latest supported 0.7.x → destination release`.
Use the matching 0.7.x migration binary, finish `commit`, and then follow the destination release's normal upgrade
procedure. A direct migration from 0.6.1 to 0.8.0 or later is outside this policy.

Before removing the command, maintainers must:

1. Qualify and document the normal upgrade from 0.7.x to 0.8.0 using installations migrated from 0.6.1. Verify
   existing data, workload ownership, and controller authentication after the upgrade.
2. Retain the published 0.7.x binaries, charts, images, checksums, signatures, attestations, and versioned migration
   guide. Keep each binary matched to its destination release.
3. Announce the end date of 0.7.x migration support before publishing 0.8.0. Removing the command from `main` does
   not end that support window. During the window, backport migration security and correctness fixes to
   `release-0.7` and keep its full qualification matrix available. Require that matrix for migration-impacting
   changes and 0.7.x releases.

Defer removal if any prerequisite remains unmet. When they are met, remove the command, its dedicated tests and
fixtures, and its build, packaging, signing, attestation, and provenance entries from `main`. Remove the dedicated
Nightly and release-candidate migration matrices from `main`, while retaining qualification on `release-0.7` for
the support window. Update the operator-upgrade harness to use the supported 0.7.x baseline without invoking
`api-migration`. Preserve historical qualification evidence and unrelated API migration fixtures.

Two compatibility contracts have separate retirement conditions:

- **Schema comparisons:** keep `groupTransitions` while the supported baseline or required historical comparisons
  cross the API-group change. Remove the declaration and checker logic only through a separate baseline-policy
  review. Preserve historical snapshots and point migration references to the retained 0.7.x source or guide.
  See the [API stability inventory](../../api/stability/README.md#api-group-transitions).
- **Migrated installations:** retain source-UID-based controller authentication, admission protection for the
  immutable source-UID marker, and existing finalizer and ownership contracts while migrated resources depend on
  them. Removing the command does not authorize removing these runtime contracts or their regression tests.
  Any later retirement requires a separately qualified compatibility procedure.

## Use an existing migrator

The handoff writes run as a migrator principal, which the lock-policy exception admits only while the operators are
stopped. All other calls use the administrator's identity. By default the tool creates the principal itself
(`api-migrator-<release>` in the operator namespace, with a Role in each namespace it writes to), and deletes it when
`commit` or `rollback` completes. To use an existing ServiceAccount instead, set `migrator` and grant it the
following in every Cluster namespace, and in every namespace with a restore Job but no Cluster:

  ```yaml
  apiVersion: rbac.authorization.k8s.io/v1
  kind: Role
  metadata: {name: api-migration-handoff, namespace: <cluster namespace>}
  rules:
    - apiGroups: [""]
      resources: [secrets, configmaps, services, serviceaccounts, persistentvolumeclaims]
      verbs: [get, patch]
    - apiGroups: [policy]
      resources: [poddisruptionbudgets]
      verbs: [get, patch]
    - apiGroups: [networking.k8s.io]
      resources: [networkpolicies, ingresses]
      verbs: [get, patch]
    - apiGroups: [gateway.networking.k8s.io]
      resources: [httproutes, tlsroutes, backendtlspolicies]
      verbs: [get, patch]
    - apiGroups: [monitoring.coreos.com]
      resources: [servicemonitors]
      verbs: [get, patch]
    - apiGroups: [cilium.io]
      resources: [ciliumnetworkpolicies]
      verbs: [get, patch]
    - apiGroups: [rbac.authorization.k8s.io]
      resources: [roles, rolebindings]
      verbs: [get, patch, escalate, bind]
    - apiGroups: [apps]
      resources: [statefulsets]
      verbs: [get, patch, create, delete]
    - apiGroups: [batch]
      resources: [jobs]
      verbs: [get, patch]
    - apiGroups: [openbao.org, kubebao.org]
      resources: [openbaoclusters/finalizers, openbaorestores/finalizers]
      verbs: [update]
  ```
