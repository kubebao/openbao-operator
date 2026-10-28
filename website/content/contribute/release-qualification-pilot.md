---
title: Candidate qualification groundwork
description: Planning and test-fixture boundaries for the isolated release qualification pilot.
weight: 52
---

The candidate qualification groundwork freezes a candidate merge preview and prepares Hardened test fixtures for isolated helper repositories. It does not include a hosted qualification workflow or authorize publication.

## Freeze the candidate inputs

`hack/ci/release-qualification.py` accepts only a canonical repository workflow dispatch from `main`. The expected controller SHA must match both the GitHub event and workflow SHA. The candidate must be an open, non-draft pull request from the same repository, based on that controller commit.

The planner records the controller commit, PR head, genuine merge preview, Git tree, suite source, and candidate committer timestamp separately. It validates the chart and release manifest against the proposed version. It preserves the complete supplied UTF-8 release body without changing its bytes.

The freeze command generates the complete `release-gate` matrix with controller tools and candidate suite and catalog data. It accepts no lane, Kubernetes version, or caller-supplied matrix filter. The resulting plan includes each effective selector and expected case ID. A plan is a declaration of intended execution; it is not evidence that tests passed.

The fixed experimental repositories are:

- `ghcr.io/kubebao/openbao-operator-qualification`
- `ghcr.io/kubebao/openbao-init-qualification`
- `ghcr.io/kubebao/openbao-backup-qualification`
- `ghcr.io/kubebao/openbao-upgrade-qualification`

These names reserve the experiment's allowed coordinates. The planner does not create packages, change their visibility, or write registry data.

## Configure experimental helper verification

Normal tests retain their existing behavior when all three variables are unset:

- `E2E_QUALIFICATION_POLICY_FILE`
- `E2E_QUALIFICATION_POLICY_SHA256`
- `E2E_QUALIFICATION_AUTH_FILE`

The opt-in path requires all three variables. The policy file must match its SHA-256 digest and name all four fixed repositories with immutable image digests. It also records the controller SHA and plan digest. Both values are validated as identifiers; this fixture hook does not authenticate the plan or its producer.

The helper policy uses the existing `operatorImageVerification` API. It requires the GitHub Actions issuer and the exact experimental workflow subject, with `failurePolicy: Block` and transparency-log verification enabled. It does not change the operator's default trust policy for official helper repositories. Configured experimental trust does not prove that default policy.

The init image must match the selected init digest. When the fixture declares an upgrade configuration, its image must also match the selected upgrade digest. The hook rejects a mismatch before creating a credential or changing the cluster. An init-only fixture keeps its upgrade configuration unset.

The auth file must contain a Docker configuration for `ghcr.io` only. The future workflow must supply a package-read credential, keep its file outside uploaded artifacts, and restrict file permissions. The fixture validator checks the credential format; it cannot determine the credential's permissions. Tests create a namespace-local image pull Secret and reference it from both the workload and helper verification configuration. They do not put credential bytes in a fixture patch or a last-applied annotation.

The operator upgrade harness keeps the official baseline helper until candidate takeover. It then creates the experimental pull Secret and reapplies the Hardened fixture with the selected candidate helper and verification policy. Verification-only harness output proves input validation, not an upgrade execution.

## Run local checks

```sh
make verify-release-qualification
make verify-operator-upgrade-e2e
GOFLAGS=-mod=vendor go test ./test/utils/qualification
```

These checks use temporary Git objects, fake Kubernetes clients, and harness input validation. They do not build or sign images, create a cluster, or create a release.

## Remaining qualification work

The hosted workflow must still separate candidate execution from signing authority, authenticate producing jobs and artifacts, resolve external runtime image inputs, run the full release profile, and audit retained raw evidence before signing a qualification statement. Independent build equivalence, promotion and interruption recovery, shadow validation, and production cutover remain separate milestones. Neither a plan nor an experimental qualification statement authorizes release publication.
