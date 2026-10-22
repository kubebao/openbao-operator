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
  - .github/workflows/codeql.yml
  - .github/workflows/nightly.yml
  - .github/workflows/release.yml
  - test/e2e/suites.yaml
---

Pull-request CI routes checks by changed path and risk. Nightly and release workflows broaden compatibility, lifecycle, provenance, and reproducibility coverage.

The CodeQL workflow analyzes Actions, Go, JavaScript/TypeScript, and Python. It runs for pull requests, including
forks, and pushes to `main` and release branches. It also runs weekly and supports manual dispatch. Go analysis
builds all packages with the version from `go.mod` and dependencies from `vendor/`.
View findings in the repository [code scanning alerts](https://github.com/kubebao/openbao-operator/security/code-scanning).

CodeQL runs on hosted runners through `pull_request`, with no repository secrets or persisted checkout credentials.
Dependency caching is disabled. GitHub restricts fork PR tokens to read-only access; CodeQL supports
[uploading results from `pull_request` runs](https://docs.github.com/en/code-security/reference/code-scanning/troubleshoot-analysis-errors/resource-not-accessible)
without a personal access token. The `CodeQL Required` job rejects failed, cancelled, or
skipped analysis. Add it to required status checks during the [advanced setup migration](#activate-advanced-codeql-scanning).

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

{{< callout type="note" title="Publishing requires more than PR CI" >}}
Edge, nightly, prerelease, and stable channels add immutable-subject, provenance, reproducibility, signing, and release-evidence gates. Passing `ci-core` does not authorize publication.
{{< /callout >}}

Continue with [supply-chain controls]({{< relref "/contribute/supply-chain.md" >}}) and [release management]({{< relref "/contribute/release.md" >}}).

## Activate advanced CodeQL scanning

GitHub [default setup](https://docs.github.com/en/code-security/concepts/code-scanning/setup-types)
excludes fork pull requests and rejects analysis uploads from advanced CodeQL workflows.
Adding `codeql.yml` alone does not activate fork coverage while default setup remains enabled.

Use this procedure when deploying the workflow. Keep existing CI protections and the external-contributor approval
policy enabled throughout the migration.

1. Allow `github/codeql-action/init` and `github/codeql-action/analyze` at the exact revision in `codeql.yml` in
   KubeBao's organization Actions policy. The existing `upload-sarif` allowlist entry does not allow these actions.
2. Prepare reviewed workflow PRs for `main` and each supported release branch. Confirm that default setup has scanned
   the current branch heads. Keep default setup enabled until the replacement PRs are ready for validation.
3. In the repository's Code Security settings, switch CodeQL from default to advanced setup. GitHub documents the
   transition under [configuring advanced setup](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/configure-code-scanning/configuring-advanced-setup-for-code-scanning).
   Rerun the replacement workflows if their uploads failed while default setup was enabled. Verify successful analyses
   for all four language groups, processed results, and `CodeQL Required` on each PR's final commit before merging.
4. Merge the reviewed workflow PRs. Enable the `CodeQL` workflow if GitHub disabled it, then dispatch it on `main` and
   each supported release branch. Verify all four analyses, processed results, and `CodeQL Required` again.
5. Verify a real fork PR against its final commit. Approve the fork workflow when the repository policy requires it.
   Check that all four analyses and `CodeQL Required` finish. A same-repository PR does not establish fork coverage.
6. Add `CodeQL Required` to the required status checks for `main` and the protected release branches. Retain the existing
   required checks. A missing, unapproved, cancelled, or failed CodeQL run must prevent merging.

Do not treat a post-merge scan as evidence of pre-merge coverage. Scorecard checks the head commits of recently merged
PRs. Historical PRs without qualifying checks can keep its SAST score below 10 until they leave the sample.
After the new setup is verified, review stale default-setup configurations in Code Scanning before removing them.
