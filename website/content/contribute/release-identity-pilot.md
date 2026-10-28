---
title: Verify release evidence identities
description: Test cross-run producer authentication with a signed metadata probe.
weight: 57
verifiedBy:
  - .github/workflows/release-evidence-identity-producer.yml
  - .github/workflows/release-evidence-identity-consumer.yml
  - hack/ci/release-identity.py
  - hack/ci/test_release_identity.py
  - hack/tools/ci_checks/release_identity_test.go
---

The identity pilot tests whether a separate workflow can authenticate a producing execution and its exact artifact.
It signs a generated JSON identity record with GitHub artifact attestations. It creates no operator release, tag,
container image, chart or deployment. Its report always sets `publicationAuthorized` and
`runtimeQualificationVerified` to `false`.

The producer grants `id-token: write` and `attestations: write` only to one trusted job. That job checks out its
reviewed controller commit and runs no candidate code. The consumer has only read permissions.
The [artifact pilot](../release-artifact-pilot/) retains its separate artifact-only boundary.

## Run the hosted proof

Both workflows must be reviewed and merged before dispatch. Choose a period when `main` can remain at the same
commit for both runs. This pilot requires the producer and consumer to use the same controller SHA; it does not
accept evidence from an older controller after `main` advances.

1. Read the full current `main` commit SHA and review that controller version. Replace `<controller-sha>` below.
2. Dispatch the producer:

   ~~~sh
   gh workflow run release-evidence-identity-producer.yml \
     --repo kubebao/openbao-operator --ref main -f controller_sha=<controller-sha>
   ~~~

3. Wait for the producer to succeed. Record its exact run ID, attempt, and the artifact ID named
   `release-identity-<run-id>-<attempt>`. Do not select an artifact by a moving name or by the latest run.
4. Dispatch the consumer with those recorded values:

   ~~~sh
   gh workflow run release-evidence-identity-consumer.yml \
     --repo kubebao/openbao-operator --ref main -f controller_sha=<controller-sha> \
     -f producer_run=<run-id> -f producer_attempt=<attempt> -f producer_artifact=<artifact-id>
   ~~~

5. Inspect the retained consumer `report.json`. A successful proof requires `identityVerified: true`,
   `authenticationPerformed: true`, all four cryptographic rejection controls, and all seven metadata rejection
   controls. Workflow success without this report is not acceptance.

The consumer checks the canonical numeric repository identity, successful producing attempt and job, immutable
artifact ID, expiration, archive SHA256 and exact probe bytes. It verifies the signed subject with checksum-pinned
GitHub CLI 2.96.0. Certificate policy binds the GitHub OIDC issuer, hosted runner, workflow path, source and signer
commit, source ref, and producing run/attempt. It checks certificate fields after cryptographic verification;
caller-supplied JSON cannot replace that verification.

The consumer retains the archive, signed bundle, API metadata, verified attestation and trusted roots.
Cryptographic negative controls use the same downloaded bundle and trusted roots, so they do not repeat artifact
downloads or attestation lookups. They reject another source commit, signer commit, workflow identity and changed
subject bytes. Metadata controls reject a different artifact ID, expired evidence, missing digest, changed archive,
wrong run, wrong attempt and mismatched certificate attempt.

## Interpret the result

A passing hosted proof authenticates this metadata probe only. It does not qualify operator artifacts or authenticate
the declarations accepted by the [advisory evidence validator](../release-evidence-contract/).
Full release-profile qualification, durable evidence retention, isolated protected promotion and recovery,
shadow validation, and production cutover remain separate milestones. Pilot artifacts expire after seven days.

Run `make verify-release-identity` for local policy and failure-path tests. Those tests use synthetic verifier
output and provide no cryptographic or hosted execution evidence.
