---
title: Compatibility
description: Validated Kubernetes and OpenBao versions, CI coverage, and the production upgrade stance.
eyebrow: Reference
weight: 1
verifiedBy:
  - charts/openbao-operator/Chart.yaml at tag 0.4.2
  - docs/reference/compatibility.md at tag 0.4.2
  - test/e2e/suites.yaml at tag 0.4.2
---

The operator requires Kubernetes 1.33 or newer. The rows below describe the current validation baseline, not a blanket guarantee for every cloud, distribution, or topology.

## Kubernetes versions

| Version | Validation | Support posture |
| --- | --- | --- |
| 1.35.x | Pull request, nightly, release, Helm, and upgrade coverage | Primary validated line |
| 1.34.x | Nightly and release-gate lifecycle coverage | Validated compatibility line |
| 1.33.x | Not validated for the current release line | May work; validate in staging before adoption |
| OpenShift | Manifest, Helm, admission, and focused platform coverage | Validate on the target cluster |

## OpenBao versions

| Version | Validation | Production note |
| --- | --- | --- |
| 2.6.0 | Primary CI, nightly, release, and rolling-upgrade coverage | Primary validated target for 0.4.2 |
| Other 2.6.x | Best-effort support on the stable line; not release-gated by 0.4.2 | Validate the exact patch in staging |
| 2.5.x | Config compatibility and rolling-upgrade source coverage | Validate the transition in staging |
| 2.4.x | Config compatibility | Upgrade before a new production rollout |
| 2.3.x | Not validated | Out of support scope |

{{< callout type="warning" title="OpenBao 2.6 mixed-version limitation" >}}
OpenBao 2.6 changed its internal request-forwarding gRPC service name. Reproduction with 2.5.5 and 2.6.5 shows that
older voters cannot forward requests to an upgraded leader. The newer client's fallback does not provide the legacy
server endpoint that older clients need. Neither RollingUpdate nor BlueGreen provides a qualified crossing from
pre-2.6 servers to 2.6 or newer. Keep the current server version until a compatible upgrade path is qualified.
See the [upgrade limitation](../../operate/upgrades/#choose-the-strategy). This does not establish datastore downgrade
compatibility or qualify a cross-version snapshot restore.
{{< /callout >}}

## Production upgrade rule

Validate the exact Kubernetes distribution, OpenBao version, unseal mechanism, storage, networking, and upgrade strategy in staging before changing production—even when both versions appear in the validated matrix.
