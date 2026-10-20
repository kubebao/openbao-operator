---
title: Release and support lifecycle
description: Release channels, support window, stable gates, and the pre-GA API lifecycle.
eyebrow: Reference
weight: 4
verifiedBy:
  - SECURITY.md
  - .github/workflows/release.yml
  - .github/workflows/nightly.yml
  - docs/contribute/release-management.md
  - config/crd/bases
---

The project provides best-effort maintenance for the latest stable release. Prerelease, edge, and nightly builds are
validation channels, not additional supported release lines.

## Release channels

| Channel | Use | Support posture |
| --- | --- | --- |
| Stable (`X.Y.Z`) | Real deployments | Latest stable line receives best-effort maintenance |
| Prerelease (`-rc`, `-beta`, `-alpha`) | Evaluate the next stable release | Staging and evaluation only |
| Edge | Validate each green merge to `main` | No production support commitment |
| Nightly | Scheduled lifecycle and drift validation | No production support commitment |

Pin an exact stable operator and chart version in production. Stay close to the latest stable line, and rehearse the
upgrade on the same Kubernetes distribution and integrations before changing production.

## Documentation lines

Documentation is versioned by minor release, not by patch:

| Route | Contract |
| --- | --- |
| Unprefixed | Current stable minor line |
| `/latest/` | Compatibility redirect to the current stable home |
| `/0.4.x/` | Previous stable 0.4.x line through 0.4.2 |
| `/next/` | Unreleased behavior on `main`; evaluation only |

A patch release updates the existing minor line and its release note. Before a new `0.X.0` release, the reviewed
`next` contract becomes that minor line and its generated API reference is pinned to the final tag.

## Stable release cadence

The project targets one stable release every four weeks when its release gates are green. Normal development occupies
the first three weeks. The fourth week focuses on documentation, generated artifacts, regressions, and upgrade,
backup, and restore confidence.

Minor releases and changes with meaningful runtime, security, or lifecycle impact use a release candidate for soak
time. If a release is not ready, maintainers skip the window instead of weakening a gate.

Patch releases can ship outside the regular window for a regression, security fix, or sharp correctness issue. Keep a
patch narrow and avoid unrelated refactoring.

### Stable release gates

A stable release requires:

- clean pull-request CI on the release branch or release pull request;
- current end-to-end release evidence;
- reviewed documentation, compatibility data, and generated artifacts;
- reviewed nightly and performance signals;
- no known upgrade, backup, or restore regression; and
- no open release blocker.

A tracked flaky nightly does not automatically block a release when the release-specific evidence is clean. A
confirmed product regression does.

## Compatibility and support

[Compatibility](../compatibility/) records the versions and platforms exercised by CI. That evidence does not create
a longer support window or guarantee every cloud, distribution, topology, and integration.

The `Hardened` profile describes the production security posture. It does not change the pre-GA API stability or
best-effort support contract.

## Pre-GA API lifecycle

The operator currently serves one API version, `openbao.org/v1alpha1`.

| Change | Current contract |
| --- | --- |
| Minor release (`0.Y.0`) | Can include breaking API or behavior changes with an explicit migration path |
| Patch release (`0.Y.Z`) | Avoids intentional breaks except when safety or data integrity requires urgent action |
| Security or integrity fix | Can shorten the normal deprecation period; release notes must identify the exception |

The lifecycle policy covers CRD versions and fields, user-visible defaults, installation, and upgrade behavior. When
the project deprecates a field or behavior, the same release must identify the replacement in API comments,
documentation, and release notes. Keep a deprecated field for at least one minor release when feasible.

When the project adds another CRD version, it will use Kubernetes-native `served`, `storage`, `deprecated`, and
`deprecationWarning` controls. The current CRDs do not need a conversion webhook because each exposes only
`v1alpha1`.

## Security fixes

Security fixes target the latest released version. Report vulnerabilities through the repository's private GitHub
Security Advisory form; do not include sensitive details in a public issue.
