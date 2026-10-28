---
title: Validate advisory release evidence
description: Check declared release evidence locally without granting publication authority.
weight: 8
verifiedBy:
  - hack/ci/release-evidence.py
  - hack/ci/test_release_evidence.py
  - mk/development.mk
---

The advisory evidence validator checks a proposed release identity against local Git objects, input files, payloads
and caller-selected evidence references. It runs offline and does not change the release workflows.

Its report always sets `authenticationPerformed`, `runtimeQualificationVerified` and `publicationAuthorized` to
`false`. A successful result means that the supplied declarations are consistent. It does not establish that GitHub
issued a certificate, that a particular job produced an archive, or that runtime and security gates passed.

The [artifact pilot](../release-artifact-pilot/) remains a separate experiment. Its receipts do not implement this
schema and cannot be used as qualification records.

## Test the contract

Run the local negative fixtures:

~~~sh
make verify-release-evidence
~~~

The tests use temporary Git repositories and opaque archive fixtures. They cover source and epoch changes,
producer and attempt substitution, changed notes and payloads, expired or duplicate artifact references, and
non-authorizing reports. They do not contact GitHub, build images, create tags or publish packages.

## Prepare declarations

No workflow emits these declarations yet. The version 1 format is an advisory contract for the next implementation
stage. It accepts stable versions in `major.minor.patch` form.

The manifest contains these fields:

| Field | Required value |
| --- | --- |
| `schema` | Integer `1`. |
| `claim` | `release-evidence-structure-only`. |
| `identity` | The source, release, input and subject fields below. |
| `records` | Exactly `qualification`, `finalBuild` and `comparison`. |

The identity contains:

| Field | Required value |
| --- | --- |
| `repository` | `kubebao/openbao-operator`. |
| `sources` | `candidateSHA`, `finalSHA`, `tree`, `baseSHA` and positive integer `sourceDateEpoch`. |
| `release` | `version` and SHA-256 `bodySHA256` of the exact assembled UTF-8 release body. |
| `inputsSHA256` | SHA-256 of the frozen build and test input file. |
| `testPlanSHA256` | SHA-256 of the selected test plan file. |
| `images` | OCI index digest declarations for `operator`, `init`, `backup` and `upgrade`. |
| `files` | SHA-256 values for the ten payload files used by the artifact pilot. |

Git object identifiers must be full lowercase SHA-1 strings. SHA-256 file hashes use lowercase hexadecimal without
a prefix. OCI and archive digests use `sha256:<lowercase-hex>`.

The candidate and final commits must differ and have the declared tree. The final commit must have exactly
`baseSHA` as its parent and be reachable from the local `origin/main` reference. The base must be an ancestor of
the candidate. The epoch must equal the candidate committer timestamp. The source chart and Release Please
manifest must declare the selected version. Fetch the required Git objects before running the validator. It
disables replacement objects and lazy fetching, and removes inherited Git repository-location overrides.

Chart root keys must use the project's plain, unquoted key syntax. Nested values must be indented. Duplicate,
quoted, tagged or complex root keys, root flow mappings, root merge keys and document markers are rejected.
Version values must be single-line literals. Other root values and nested values are opaque. The validator checks
this restricted syntax; it does not implement a general YAML parser.

Each record contains `identitySHA256`, `sourceSHA`, `producer` and a nonempty `artifacts` array. The identity hash
uses the validator's sorted-key, compact JSON encoding. The qualification subject uses the candidate SHA.
The other subjects use the final SHA.

A producer contains `repository`, `workflow`, `sourceSHA`, `signerSHA`, `runID`, `jobID` and `attempt`.
The workflow is a repository-relative `.github/workflows/<name>.yml` path. IDs and attempts are positive integers.
The producer source and signer fields describe declared certificate context. They are not verified certificates.
In particular, the qualification subject can differ from its trusted controller's source. The final build's
declared event source must equal the final commit.

An artifact reference contains a positive integer `id`, a `digest` and an `expiresAt` UTC timestamp in
`YYYY-MM-DDTHH:MM:SSZ` form. IDs must be unique across all records. The corresponding local archive is
`<archives-directory>/<id>.zip`. The validator checks its hash and declared expiry. It treats archive contents as
opaque bytes.

The separate policy contains integer `schema: 1`, the selected `identitySHA256` and a `records` map with the same
three roles. Each policy record contains the selected `producer` and `artifacts` values. These must match the
manifest exactly. Unknown fields and duplicate JSON keys are rejected. Manifest and policy documents must not
exceed one MiB.

The policy is caller-supplied. Matching it does not authenticate the policy or its producer selections.

## Run the validator

Prepare the manifest, policy, exact notes body, frozen input file, test plan, payload directory and retained
archives described above. Select a new output path; the validator does not overwrite an existing report.

~~~sh
python3 hack/ci/release-evidence.py \
  --manifest <manifest.json> --policy <policy.json> \
  --repo <local-repository> --notes <release-body.txt> \
  --inputs <frozen-inputs.json> --plan <test-plan.json> \
  --payloads <payload-directory> --archives <archive-directory> \
  --output <new-report.json>
~~~

A consistent declaration exits with status `0`. A rejected declaration exits with a nonzero status and writes a
report with `consistent: false` when the selected output path is available. Existing reports remain unchanged.

## Establish production trust separately

The [identity pilot](../release-identity-pilot/) tests cross-run authentication with a separate signed metadata
probe. It does not authenticate this validator's declarations or grant publication authority.

Before a production consumer can use this contract, it must authenticate the selected producer workflow and
signer version, repository, source context, run, job, attempt and artifact IDs against signed evidence and platform
metadata. It must inspect the actual qualification results and image contents.

Signing must run in a fresh trusted job that does not execute candidate code. Production integration must also
freeze the complete release body, preserve each producing execution and retained artifact, and define recovery
of a complete signed publication set. Approval, promotion, consumer verification and release creation remain
outside this validator.
