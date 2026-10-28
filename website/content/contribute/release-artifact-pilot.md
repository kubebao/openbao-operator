---
title: Test release artifact equivalence
description: Compare a premerge candidate with its squash commit without publishing a release.
weight: 7
verifiedBy:
  - .github/workflows/release-artifact-pilot.yml
  - hack/ci/release-pilot.py
  - hack/ci/release-pilot-inputs.json
  - hack/ci/package-release-pilot.sh
  - hack/ci/test_release_pilot.py
  - hack/tools/ci_checks/release_pilot_test.go
---

The **Release Artifact Pilot** workflow tests whether a premerge candidate and its final squash commit produce identical artifacts when both builds use frozen inputs. It runs manually in `kubebao/openbao-operator` from `main`.

The pilot does not create Git tags, GitHub releases, registry entries, signatures or attestations. Its token grants `contents: read`. It does not access release App credentials or enter a deployment environment. The existing release workflows and required checks remain unchanged.

## Select the source commits

Use two different full commit SHAs. The candidate can be a PR head or a preserved merge preview. The merged commit must have one parent and must be reachable from `origin/main`. Its parent must be an ancestor of the candidate, and both commits must have the same Git tree. The chart must declare matching SemVer `version` and `appVersion` values.

The candidate timestamp becomes `SOURCE_DATE_EPOCH` for both builds. The plan also freezes the source identities, workflow controller, run and attempt, platforms, Helm version, Buildx version, BuildKit image digest and Syft version and binary checksum. Do not replace the candidate timestamp with the squash commit timestamp.

## Run the hosted pilot

Merge the reviewed pilot workflow before dispatching it. GitHub requires manually dispatched workflows to exist on the default branch.

```sh
gh workflow run release-artifact-pilot.yml --ref main \
  -f candidate_sha=<full-candidate-commit-sha> \
  -f merged_sha=<full-squash-commit-sha>
```

The workflow runs eight image builds: four images for each source commit. Each build produces a local OCI archive containing `linux/amd64` and `linux/arm64`. Jobs do not import or export build caches. At most two image jobs run concurrently.

Each source produces ten release payload files: the installer, CRDs, two policy approval files, chart archive, four normalized SPDX SBOMs and checksums. SBOMs catalog `linux/amd64`; the image comparison covers both architectures.

The comparison checks every OCI index, platform manifest, config and referenced layer against its digest. It verifies receipt identities and file hashes, then runs the existing byte reproducibility gate. Negative controls must reject a changed installer, substituted image digest, missing file and mismatched receipt fields.

Download the `release-pilot-<run-id>-<attempt>-comparison` artifact to inspect `comparison.json` and the positive and negative byte gate logs. The plan, images and release files have separate artifacts with the same run and attempt prefix. Artifacts remain available for seven days.

Start a new run or rerun **all jobs** after a failure. Failed-job-only reruns cannot mix artifacts from different attempts.

## Interpret the result

A successful report claims **artifact equivalence only**. It does not prove that the premerge images passed the full release test suite. It does not rerun the Kubernetes matrix, operator upgrade, security scanners or consumer verification. It does not authorize a merge or publication.

GitHub signing identities, protected approvals, promotion and tag creation still require a separate hosted validation stage. The current pilot cannot exercise those actions because it has no publication capability.

## Run local checks

```sh
make verify-release-pilot
make verify-workflows
```

The pilot tests use temporary Git repositories and OCI fixtures. They exercise actual squash commit relationships, frozen timestamps, artifact corruption, platform coverage, cross-run identities and the repository byte gate. They do not start Docker or contact GitHub.
