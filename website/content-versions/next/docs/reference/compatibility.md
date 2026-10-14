---
title: Compatibility
description: Validated Kubernetes and OpenBao versions, CI coverage, and the production upgrade stance.
eyebrow: Reference
weight: 1
verifiedBy:
  - test/e2e/suites.yaml
  - test/e2e/e2e_versions.go
  - test/e2e/Upgrade_Strategies_test.go
  - test/e2e/backup_restore_test.go
  - internal/service/upgrade/bluegreen/version_compatibility.go
---

The operator requires Kubernetes 1.33 or newer. The rows below describe the current validation baseline, not a blanket guarantee for every cloud, distribution, or topology.

## Kubernetes versions

| Version | Validation | Support posture |
| --- | --- | --- |
| 1.36.x | Pull request, nightly, release, Helm, and upgrade coverage | Primary validated line |
| 1.35.x | Daily smoke, alternating weekly full coverage, and release coverage | Validated compatibility line |
| 1.34.x | Daily smoke, alternating weekly full coverage, and release coverage | Validated compatibility line |
| 1.33.x | Not validated for the current release line | Upstream end of life; validate in staging before adoption |
| OpenShift | Manifest, Helm, admission, and focused platform coverage | Validate on the target cluster |

## OpenBao versions

| Version | Validation | Production note |
| --- | --- | --- |
| 2.7.1 | Config parser, CI container checks, and focused local lifecycle, rolling-upgrade, and restore qualification | Prepare external plugins before upgrading; see [OpenBao 2.7 migration](../../operate/openbao-270/) |
| 2.7.0 | Config parser and focused local lifecycle, upgrade, restore, and PKCS#11 qualification; rolling-upgrade source for local 2.7.1 qualification | Use the latest qualified security patch |
| 2.6.4 | Default CI, nightly, and release target; local lifecycle, rolling-upgrade, backup, restore, and SoftHSM PKCS#11 and KMIP seal qualification | Unreleased `main` baseline; validate the exact environment in staging |
| 2.6.3 | OpenBao Operator 0.5.1 validation target; rolling-upgrade source for local 2.6.4 qualification | Use the latest qualified security patch |
| 2.6.2 | OpenBao Operator 0.5.0 release target | Use the latest qualified security patch |
| Other 2.6.x | Not individually release-gated | Validate the exact patch in staging |
| 2.5.x | Historical rolling-upgrade source coverage; outside the default config gate | Upgrade to a maintained release |
| 2.4.x | Outside the default config gate | Upgrade before a new production rollout |
| 2.3.x | Not validated | Out of support scope |

The configuration compatibility gate checks OpenBao 2.6.4 as a migration target and 2.7.1 as the current release.
These labels describe operator validation. The [upstream support policy](https://openbao.org/community/policies/support/)
provides best-effort support centered on the latest release and does not promise a two-minor maintenance window.

{{< callout type="warning" title="OpenBao 2.6 BlueGreen limitation" >}}
OpenBao 2.6.0 changed its internal request-forwarding gRPC service name.
[OpenBao 2.6.3](https://github.com/openbao/openbao/releases/tag/v2.6.3) fixes cross-version request forwarding,
but the operator still blocks pre-2.6 to 2.6-or-newer `BlueGreen` transitions until an exact version pair is qualified.
The forwarding fix does not establish datastore downgrade compatibility.
{{< /callout >}}

## OpenBao 2.6.3 qualification

Local qualification uses Kubernetes 1.34.3 on Linux ARM64 with static unseal and operator-managed TLS. It covers
fresh self-initialization, file audit output, a 2.6.2 to 2.6.3 rolling upgrade with three voters and one read replica,
and S3 backup and restore with RustFS. The restore checks voter replacement, read-replica recovery, and retained data.
These checks do not qualify external unseal providers, cloud storage credentials, or a pre-2.6 `BlueGreen` transition.

## OpenBao 2.7.0 qualification

Focused qualification uses Kubernetes 1.34.3 on Linux ARM64. It covers fresh self-initialization and file audit
storage, 2.6.3 to 2.7.0 rolling and BlueGreen upgrades with retained data, and S3 backup and restore with RustFS.
The rolling case includes three voters and a read replica. The BlueGreen case includes a pre-upgrade snapshot,
non-voter synchronization, membership changes, and service availability assertions.

The external PKCS#11 0.1.0 plugin passes SoftHSM initialization, restart, and scale tests on both 2.6.3 and 2.7.0.
A one-voter PKCS#11 cluster also completes a 2.6.3 to 2.7.0 upgrade with the same cluster identity and PVCs.
A separate server configuration check covers a digest-pinned OCI plugin with inferred metadata. These results do
not qualify cloud KMS credentials, vendor HSMs, PKCS#11 0.2.0, or removed application plugins.

## OpenBao 2.6.4 and 2.7.1 qualification

Both releases are upstream security patches. Local qualification uses Kubernetes 1.36.1 on Linux AMD64 with Kind.
The core lifecycle and manager selection passes on each version. Rolling upgrades pass from 2.6.3 to 2.6.4, from
2.7.0 to 2.7.1, and from 2.5.5 to 2.6.4, including operation-lock, recovery, and idle strategy-switch coverage. S3
backup and `OpenBaoRestore` recovery pass on 2.6.4 with RustFS. Fresh-target restores and scheduled restore tests pass
on 2.7.1, which restore cluster templates now accept alongside 2.7.0. Policy reconciliation and controller JWT checks
pass on both versions, and hybrid PQ TLS passes on 2.7.1.

The SoftHSM fixture builds on 2.6.4 with the pinned PKCS#11 0.1.0 plugin. On 2.6.4, a SoftHSM-backed PKCS#11 cluster
and a PyKMIP-backed KMIP cluster each initialize, auto-unseal after a Pod restart, and scale up and back down with the
seeded key material. These checks do not qualify cloud KMS providers, vendor HSMs, PKCS#11 or KMIP seals on 2.7.1,
cloud storage credentials, or other Kubernetes versions and architectures.

## Production upgrade rule

Validate the exact Kubernetes distribution, OpenBao version, unseal mechanism, storage, networking, and upgrade strategy in staging before changing production—even when both versions appear in the validated matrix.
