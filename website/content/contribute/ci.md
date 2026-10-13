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
  - hack/tools/ci_checks/main.go
  - hack/tools/ci_report/main.go
  - test/e2e/suites.yaml
---

Pull-request CI routes checks by changed path and risk. Nightly and release workflows broaden compatibility, lifecycle, provenance, and reproducibility coverage.

Change detection must resolve the tested commits and complete the Git diff. An empty successful diff selects no
path-based checks. A failed diff stops routing. Initial pushes with an all-zero prior SHA classify every tracked path.
Renames classify both the previous and current paths.

The `Detect Changes` job records required job expectations in the `ci-routing` artifact. The workflow uses
that record to select jobs. `CI Required` rejects a failed routing job, missing results, unexpected job skips, and failed
or cancelled checks. The record identifies the tested SHA and explains exclusions for fork pull requests.
The routing artifact remains available when only failed jobs are rerun.
Fork pull requests still need a maintainer or local run for cluster qualification.

The advisory `CI Measurement` job collects the current attempt's job and step timings after validation and edge
qualification. Its `ci-measurements-<attempt>` artifact contains raw run and job metadata, a JSON summary, and a
Markdown summary. E2E reports add lane counts and specification timings. The snapshot excludes the measurement job.
Runner-minutes sum job execution time; concurrent jobs overlap. Failed-job reruns measure only that attempt.
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
review-required schema changes against the released 0.5.0 CRDs. It runs for contract-related changes, pushes to
`main`, and manual CI runs. Release validation runs the same gate before image builds and publication.
`make report-crd-compatibility` produces a diagnostic report without weakening the required gate.

The E2E manifest owns suite routing, isolation, parallelism, and supported test versions. Do not duplicate those values in workflow logic. Run `make e2e-ci-matrix` or the nightly matrix commands before changing routing.

The `backup-restore` lane covers storage-provider backup and restore behavior. The `restore-tests` lane covers
managed targets, scheduled restore tests, and cleanup after the full confirmation timeout. These lanes run in
separate clusters, have independent time limits, and use the same selectors on PRs and `main`.
Cross-cluster restore qualification requires a manually
provisioned environment and is excluded from ordinary CI, nightly, and release-gate selectors.

{{< callout type="note" title="Publishing requires more than PR CI" >}}
Edge, nightly, prerelease, and stable channels add immutable-subject, provenance, reproducibility, signing, and release-evidence gates. Passing `ci-core` does not authorize publication.
{{< /callout >}}

Continue with [supply-chain controls]({{< relref "/contribute/supply-chain.md" >}}) and [release management]({{< relref "/contribute/release.md" >}}).
