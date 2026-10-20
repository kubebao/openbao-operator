---
title: Run CI-equivalent checks
description: Map local validation to pull-request, main, nightly, and release workflow lanes.
eyebrow: Contribute
weight: 4
verifiedBy:
  - devenv.nix
  - devenv.yaml
  - devenv.lock
  - hack/dev/verify-devenv.sh
  - .github/workflows/ci.yml
  - .github/workflows/nightly.yml
  - .github/workflows/release.yml
  - .github/workflows/scorecard.yml
  - hack/tools/ci_checks/main.go
  - hack/tools/ci_report/main.go
  - hack/tools/e2e_plan/selection.go
  - hack/tools/e2e_report/selection.go
  - hack/ci/e2e-selection-paths.txt
  - .github/actions/run-e2e-lane/action.yml
  - hack/ci/build-e2e-binaries.sh
  - hack/ci/e2e-binaries.py
  - hack/ci/run-e2e.sh
  - hack/ci/e2e-execution-paths.txt
  - test/e2e/suites.yaml
---

Pull-request CI routes checks by changed path and risk. Nightly and release workflows broaden compatibility, lifecycle, provenance, and reproducibility coverage.

CodeQL uses GitHub default setup to analyze Actions, Go, JavaScript/TypeScript, and Python. Repository security
settings control its languages and query suite. View its findings in the repository
[code scanning alerts](https://github.com/kubebao/openbao-operator/security/code-scanning).

OpenSSF Scorecard runs on pushes to `main` and weekly. It publishes repository security findings to the
[Scorecard viewer](https://scorecard.dev/viewer/?uri=github.com/kubebao/openbao-operator) and GitHub Code Scanning.
The `openssf-scorecard` artifact retains the SARIF report for seven days. The workflow uses the default GitHub token;
it does not require a personal access token.

Scorecard is advisory. Its score is not a required pull-request check or a release gate. Review findings against
the repository's code, workflows, and settings before changing controls or exceptions.

KubeBao's organization Actions policy allows the Scorecard and SARIF-upload actions at specific commit revisions.
Before merging an update to either action pin, add the reviewed revision to the organization Actions allowlist.
Repository settings cannot override this inherited policy. A blocked action causes a workflow startup failure
before any jobs run.

Change detection must resolve the tested commits and complete the Git diff. An empty successful diff selects no
path-based checks. A failed diff stops routing. Initial pushes with an all-zero prior SHA classify every tracked path.
Renames classify both the previous and current paths.

The `Detect Changes` job records required job expectations in the `ci-routing` artifact. The workflow uses
that record to select jobs. `CI Required` rejects a failed routing job, missing results, unexpected job skips, and failed
or cancelled checks. The record identifies the tested SHA and explains intentional E2E exclusions.
The routing artifact remains available when only failed jobs are rerun.
Fork pull requests use read-only E2E jobs after any approval required by the repository policy.

The advisory `CI Measurement` job collects the current attempt's job and step timings after validation and edge
qualification. Its `ci-measurements-<attempt>` artifact contains raw run and job metadata, a JSON summary, and a
Markdown summary. E2E reports add lane counts and specification timings. The snapshot excludes the measurement job.
Runner-minutes sum job execution time; concurrent jobs overlap. The E2E category includes shared compilation in
`Plan E2E Matrix`. Failed-job reruns measure only that attempt.
Successful jobs inherited from previous attempts remain visible but do not count toward its runner consumption.
E2E artifact names include the attempt number so reports from previous attempts are excluded.
Missing reports do not prove qualification.

Run `make test-ci-tools` to test routing and reporting locally. `make verify-workflows` also runs these tests.
To summarize saved evidence, use the jobs response from the run-attempt endpoint with all pages combined into one
`jobs` array. The optional E2E directory contains downloaded lane artifacts with `summary.json` and `ginkgo.json`.

```sh
go run ./hack/tools/ci_report \
  --run artifacts/ci-measurements/run.json \
  --jobs artifacts/ci-measurements/jobs.json \
  --e2e-dir artifacts/e2e-measurements \
  --json-out artifacts/ci-measurements/summary.json \
  --markdown-out artifacts/ci-measurements/summary.md
```

Docker-based jobs in `ci.yml` configure the runner's Docker daemon and the E2E image builder to use
[Google's public Docker Hub cache](https://docs.cloud.google.com/artifact-registry/docs/pull-cached-dockerhub-images).
Image references and digest pins stay unchanged. A cache miss falls back to Docker Hub and can still reach its
anonymous pull limit. The setup action restarts Docker, so run it before creating containers or Buildx builders.
This runner configuration does not configure registry mirrors inside Kind nodes.

{{< command label="verify" title="Run the local pull-request baseline" >}}
devenv test
devenv tasks run operator:bootstrap
devenv tasks run operator:doctor
devenv tasks run operator:ci-core
{{< /command >}}

The advisory `Devenv Contract` job runs on toolchain changes and pushes to `main`. It proves that the pinned base and
editor-profile shells can be constructed on the CI runner and that their version contracts pass. Base and editor
duration and closure measurements remain separate. The job pulls from the public project and Devenv caches. It
publishes newly built paths to the project cache only from pushes to `main` when the repository has a per-cache
`CACHIX_AUTH_TOKEN`; pull requests are always read-only. The existing required jobs remain the merge authority while
the project migrates their setup steps incrementally.

| CI concern | Local entry point |
| --- | --- |
| Pinned environment contract | `devenv test` |
| Core quality gates | `devenv tasks run operator:ci-core` |
| API inventory and released CRD compatibility | `make verify-api-contract` |
| Hugo documentation and redirects | `make docs-build` |
| Vendored dependencies and licenses | `make verify-vendor`, `make license-check` |
| Static and filesystem security | `make semgrep-ci`, `make security-ci` |
| Focused cluster behavior | `make test-e2e-ci`, `make helm-e2e-smoke` |
| Prior-stable operator upgrade | `make test-e2e-operator-upgrade` |
| Existing platform cluster | `make test-e2e-existing` |

Docs-only changes normally avoid broad cluster E2E. Backup, upgrade, security, provisioner, admission, controller-critical, or seal-path changes expand into targeted shards. Maintainers can request the full E2E set with the repository's CI label.

The required `API Contract` job rejects unclassified API fields, stale inventory snapshots, and breaking or
review-required schema changes against the released 0.5.2 CRDs. It runs for contract-related changes, pushes to
`main`, and manual CI runs. Release validation runs the same gate before image builds and publication.
`make report-crd-compatibility` produces a diagnostic report without weakening the required gate.

The E2E manifest owns suite routing, isolation, parallelism, and supported test versions. Do not duplicate those values in workflow logic. Run `make e2e-ci-matrix` or the nightly matrix commands before changing routing.

`ci_checks` records E2E scopes, full-suite selection, and pentest inclusion with the job expectations.
`e2e_plan` consumes that record and emits only selected lanes. Each row contains the effective `label_filter` and
`expected_case_ids`. The `e2e-plan` artifact retains the matrix. To reproduce CI selection, run `e2e_plan` with
`--ci-plan artifacts/ci-routing/expected-checks.json --sha <tested-sha>` from the tested checkout.
Without `--ci-plan`, `make e2e-ci-matrix` emits the full primary-version matrix.

PR filters retain E2E anchors and exclude `lower-layer-covered` scenarios. The default security lane also excludes
pentest scenarios unless a hardening label or manual hardening input requests them. Full runs use the full selectors.
Path, label, and manual-input selection share the same recorded scopes. Shared planner and lane-policy paths in
`hack/ci/e2e-selection-paths.txt` select every automatic lane family on trusted and fork PRs.

Matrix planning first runs `make verify-e2e-manifest` to reject a stale generated catalog.
The planner validates scenario IDs and suite `ci.lanes` against the full selectors. It rejects undeclared owners,
overlapping owners, orphaned automatic scenarios, and empty selected lanes. The idle strategy-switch scenario belongs
to `upgrade-bluegreen-core`. The `controller-jwt` lane owns the real credential-lifetime witness and uses its own
operator, separate from manager restart tests. The `restore-tests` lane uses two workers for independent fixture
groups; source, destination, storage, and approval changes remain group-owned.
Kubernetes versions and profile coverage remain independent qualification contexts.
Each automatic scenario must have an owner included in the full CI matrix with an automatic PR scope.
Disabled lanes and manual scopes cannot satisfy automatic coverage requirements.

CI, nightly, and release lanes run `e2e_report --expected-cases` against the planned IDs. Every planned scenario must
pass exactly once. Missing, extra, duplicate, skipped, pending, and failed scenarios block the lane. Missing reports,
selector mismatches, and dry-run reports also fail validation. Lane artifacts retain `expected-cases.json`, the Ginkgo
report, and the validation summary.

The matrix-planning job builds the E2E test suite, vendored Ginkgo runner, and report summarizer once, alongside
image preparation. The run-scoped `e2e-binaries` artifact records the source SHA, operating system, architecture,
pinned Go and Ginkgo versions, CGO setting, build tag, and SHA-256 checksums. Builds require a clean checkout.
The artifact remains available when only failed lanes are rerun; full reruns replace it.

CI, nightly, and release workflows call `.github/actions/run-e2e-lane`. The action consumes the planned row and
workflow-owned image references. Release helper overrides and per-run seal fixtures remain separate inputs.
Each lane verifies the bundle before cluster preparation. Missing, stale, incompatible, or corrupted binaries fail
the lane. CI does not fall back to compiling the suite in a lane. Downloads stay in the runner's temporary directory,
so lane evidence does not upload the binary bundle again.
Shared harness paths in `hack/ci/e2e-execution-paths.txt` select all lane families on trusted and fork PRs.

The shared action prepares Kind and images, runs the verified suite with the planned worker count, and validates its
reports. It collects diagnostics before deleting the cluster, including after preparation or execution failures.
Evidence uploads run after cleanup. Lane artifacts include the binary metadata, expected IDs, JSON and JUnit reports,
and validation summary. Go setup remains available because suite install and deploy helpers still use Make and Go tools.

The `backup-restore` lane covers storage-provider backup and restore behavior. The `restore-tests` lane covers
managed targets, scheduled restore tests, and cleanup after the full confirmation timeout. These lanes run in
separate clusters, have independent time limits, and use the same selectors on PRs and `main`.
Cross-cluster restore qualification requires a manually
provisioned environment and is excluded from ordinary CI, nightly, and release-gate selectors.

{{< callout type="note" title="Publishing requires more than PR CI" >}}
Edge, nightly, prerelease, and stable channels add immutable-subject, provenance, reproducibility, signing, and release-evidence gates. Passing `ci-core` does not authorize publication.
{{< /callout >}}

Continue with [supply-chain controls]({{< relref "/contribute/supply-chain.md" >}}) and [release management]({{< relref "/contribute/release.md" >}}).

## Fork pull requests

Fork PRs use the same scenario selection, signed fixtures, report validation, and operator-upgrade harness as
same-repository PRs. Their image-build, lane, and operator-upgrade jobs declare only `contents: read` and use
GitHub-hosted runners. Checkouts do not persist credentials. Fork jobs do not receive publishing or signing
credentials, OIDC permissions, or environment secrets. Go and repo-tool cache persistence is disabled for forks.
The fork image builder does not export Buildx caches.

GitHub's repository or organization approval policy controls whether an external contributor's workflow can start.
Configure **Require approval for all external contributors**. Keep fork secret forwarding and write-token forwarding
disabled wherever those options are available. Approval authorizes runner use; it does not make contributor code,
workflow edits, binaries, reports, or images trusted. Review changes to workflow files and local actions before
approval. The YAML cannot enforce an approval policy against a contributor who can modify that YAML.

The fork image builder exports one image archive instead of publishing to GHCR. Consumers select the exact artifact
from the same workflow run and the successful producer's build attempt. Image metadata binds the repository,
workflow run, PR head SHA, tested merge SHA, platform, image IDs, and archive checksum. Binary metadata records the
same workflow identity. Failed-job reruns can reuse an earlier successful producer from the same run and source.
Missing or mismatched artifacts fail; consumers do not fetch artifacts from another run or rebuild missing inputs.

The loader validates image roles, tags, config digests, platform, and archive paths without extracting files into the
workspace. It constructs a Docker-only archive with controlled member names before importing images. Unrelated OCI
indexes and image aliases cannot select a different import. Missing candidate images fail local preparation;
No registry-pull fallback replaces them. Public signed Hardened fixtures retain their registry pulls and real
verification. A seal lane requires both candidate SoftHSM and PyKMIP fixtures.

Checksums and source metadata provide transport checks, not trusted provenance. Fork artifacts and caches must not
be promoted into privileged workflows or release inputs. Edge and release publication use trusted triggers and
build their own candidate artifacts. Do not execute fork code or binaries in privileged `pull_request_target`,
`workflow_run`, or comment-triggered jobs. Local tests cover routing and transport contracts; validating native
approval, effective token permissions, and elapsed time also requires an approved fork run on GitHub.
