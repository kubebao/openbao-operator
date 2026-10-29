---
title: Candidate qualification pilot
description: Full-profile candidate qualification with isolated packages and authenticated evidence.
weight: 52
verifiedBy:
  - .github/workflows/release-qualification-pilot.yml
  - hack/ci/release-qualification.py
  - hack/ci/qualification-artifacts.py
  - hack/ci/qualification-origin.py
  - hack/ci/qualification-runtime.py
  - hack/ci/qualification-audit.py
  - hack/ci/qualification-helper-policy.py
  - test/utils/qualification/policy.go
---

The candidate qualification pilot builds and tests a proposed release before its PR merges. It retains an authenticated qualification record. It does not create an operator release or tag, promote production packages, or approve publication. The existing release workflows remain unchanged.

## Source and execution identity

`Release Candidate Qualification Pilot` accepts a manual dispatch from canonical `main`. The supplied controller SHA must match both the GitHub event and workflow SHA. The candidate must be an open, non-draft PR from the same repository, based on that controller commit.

The planner records these identities separately:

- **C:** the reviewed controller commit on `main`.
- **H:** the selected PR head.
- **M:** the genuine merge preview with parents `[C, H]`. Candidate images, payloads, and the E2E suite use M.

The planner records M's tree and committer timestamp. Both independent builds use that timestamp as `SOURCE_DATE_EPOCH`. It validates the candidate chart and release manifest against the proposed version and preserves the complete supplied UTF-8 release body without changing its bytes.

The GitHub event and signing certificate identify C. The qualification record identifies M as the product and suite source. The workflow does not replace `GITHUB_SHA` with M or claim that the certificate describes a future squash commit.

For this first pilot, the candidate must retain C's reviewed tests, fixtures, test manifests, helper utilities, build and gate scripts, local Actions, Make configuration, Semgrep configuration and exclusions, vulnerability exceptions, Git ignore rules, Go module locks, and vendored dependencies. The planner compares the corresponding Git objects. Product and release-metadata changes can differ. Supporting candidate changes to the qualification policy itself requires a separate reviewed design.

## Prepare isolated packages

Before dispatch, prepare these four **private** container packages and grant this repository's workflow access:

- `ghcr.io/kubebao/openbao-operator-qualification`
- `ghcr.io/kubebao/openbao-init-qualification`
- `ghcr.io/kubebao/openbao-backup-qualification`
- `ghcr.io/kubebao/openbao-upgrade-qualification`

The importer requires all four packages to exist and report private visibility before it writes any image. It does not create packages, change visibility, or grant access. Missing access or a public destination stops the experiment. Repository administrators must prepare these prerequisites separately; merging this workflow does not dispatch it.

## Execute qualification

The planner generates the complete `release-gate` matrix with controller tools. It accepts no lane, Kubernetes-version, or caller-supplied matrix filter. It freezes direct OpenBao, Kind, emulator, helper-client, and previous-stable image references by digest. It also freezes the previous-stable chart digest and manager image. A plan declares intended execution; it does not prove that tests passed.

The workflow runs these stages:

1. Build four dual-platform images twice on separate runners without registry-write or signing authority. Retain the OCI archives and actual-byte receipts.
2. Package the ten canonical release payloads twice. Retain production repository coordinates in those payloads.
3. On a fresh C-only runner, authenticate producing jobs and artifact digests, compare the independent image subjects, and copy exact OCI bytes into the four private packages. Use digest destinations without mutable transport tags. Sign each image and attach an import attestation that carries no runtime or publication approval.
4. Compile the candidate E2E suite from M. Build the Ginkgo runner from C. Bind their checksums to the source, plan, run, and attempt.
5. Run API and generated-file checks, govulncheck, Semgrep, and four configured Trivy image gates. Retain check logs, image scan reports, scanner metadata, and runner identifiers.
6. Run every planned E2E row once against the selected candidate images. Run the actual previous-stable operator upgrade separately. Retain raw Ginkgo reports, node versions, available image observations, and the upgrade assertions and custody UIDs.
7. On a fresh C-only runner, authenticate every required producing job and archive. Recompute E2E selection with C's reporter, reject missing cases, selected skips, retries, duplicate executions, failed suites, and changed selectors, and verify actual kubelet versions. Verify image signatures and import provenance, independent payload hashes, and the complete upgrade receipt before attesting the qualification record.
8. On a separate read-only runner, authenticate that record's artifact and signing bundle. Verify its exact workflow, C, run, attempt, and subject. Require rejection controls for changed bytes, source, signer, and workflow.

Artifacts have run-and-attempt-specific names and 30-day retention. The qualification record has a two-MiB limit; it binds larger raw observations by SHA-256 and authenticated artifact origin. The verifier rejects reused job records whose timestamps precede the selected attempt. A failed-job-only rerun cannot qualify by reusing successful work from an older attempt; dispatch a fresh complete run instead.

The 30-day retention applies only to GitHub Actions artifacts. Experimental GHCR image indexes, signatures, and attestations do not expire automatically. Before repeated use, define a registry retention and cleanup procedure that preserves active qualification evidence.

The signed `qualification.json` records the plan, C/H/M identities, image subjects, payload hashes, coverage, actual node versions, available runtime and scanner observations, upgrade custody assertions, artifact origins, and material limits. It sets `runtimeQualificationVerified: true` only after the audit succeeds. It always sets `publicationAuthorized: false`.

## Private helper verification

Normal tests retain their existing behavior when these variables are unset:

- `E2E_QUALIFICATION_POLICY_FILE`
- `E2E_QUALIFICATION_POLICY_SHA256`
- `E2E_QUALIFICATION_AUTH_FILE`
- `E2E_QUALIFICATION_IMAGE_MAP_FILE`
- `E2E_QUALIFICATION_IMAGE_MAP_SHA256`

The helper policy requires its three variables together. It names all four fixed repositories with immutable digests and records the controller and plan hashes. The map requires its file and SHA-256 together and rejects references absent from the frozen map.

The helper overlay uses the existing `operatorImageVerification` API. It requires the GitHub Actions issuer and exact experimental workflow subject, with `failurePolicy: Block` and transparency-log verification enabled. Init and configured upgrade images must match the selected role digests before the overlay creates a credential or changes a cluster.

Runtime jobs receive package-read access. They keep a dedicated `0600` Docker auth file under `RUNNER_TEMP`, outside uploaded artifacts. Positive Hardened fixtures create a namespace-local pull Secret and reference it from the workload and helper verification configuration. Credential bytes do not enter fixture patches or last-applied annotations.

Development images use local transport aliases after Docker verifies the selected repository digest. Hardened workloads retain the signed private digest reference and pull through their namespace Secret. The frozen runtime policy disables Hardened helper preloading because a direct node CRI pull does not use namespace credentials. This changes image transport, not the selected tests or verification requirements.

The upgrade harness installs the digest-pinned previous-stable chart and manager image. It retains the official baseline Hardened helper until candidate takeover, then creates the private pull Secret and reapplies the Hardened fixture with the candidate helper. It records successful Shared-to-Target migration, JWT rejection, Autopilot repair, preserved data, the legacy static-restore guardrail, retained Secret and PVC UIDs, and final success. Verification-only harness output is not runtime evidence.

## Evidence limits

Experimental helper trust does not qualify the default policy for public helper repositories. The first pilot does not claim a hermetic runtime: runner images, vulnerability databases, remote Semgrep rule packs, network services, and transitive fixture dependencies are not frozen. Available runner identifiers, scanner metadata, and before/after Pod image observations are retained, but these observations are not a complete dependency inventory. The OCI plugin fixture pins its binary checksum and version; its OCI descriptor is not frozen.

The workflow implementation and its local tests do not prove hosted qualification. A hosted run must complete all stages and authenticate its final record before that claim is made. Neither that record nor a passing pilot authorizes a production release.

## Run local checks

```sh
make verify-release-qualification
make verify-release-identity
make verify-operator-upgrade-e2e
GOFLAGS=-mod=vendor go test ./test/utils/qualification ./test/e2e/helpers ./hack/tools/e2e_report ./hack/tools/ci_checks
```

These checks exercise temporary Git objects, fake Kubernetes clients, parser and source rejection controls, and harness input validation. They do not create a cluster, publish a package, or create a release.

Planning, origin verification, and evidence validation are intended to remain as release CI tooling. Standalone pilot probes can be retired after productionisation consolidates the required checks. This tooling is not shipped in the operator and does not add a Python requirement for operator users.

Promotion and interruption recovery, shadow validation, and production cutover remain separate milestones. The pilot does not implement those controls.
