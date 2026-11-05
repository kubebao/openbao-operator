---
title: Claims and Service Offerings
description: Future self-service design above the concrete OpenBaoCluster lifecycle API.
eyebrow: Project · Design
weight: 2
verifiedBy:
  - api/v1alpha1
  - config/crd/bases
---

Claims and Service Offerings are a proposed optional module for requesting an OpenBao service without exposing the
entire `OpenBaoCluster` workload specification to every tenant.

{{< callout type="warning" title="Not available in 0.5.x" >}}
The current repository API and CRDs do not contain `OpenBaoClusterClaim`, `OpenBaoServiceOffering`, or the proposed
`claims.kubebao.org` API group. This page is a design boundary, not usage documentation.
{{< /callout >}}

## Intended model

1. A platform operator defines an approved offering and the parameters a tenant may choose.
2. A tenant submits a small claim that selects an offering.
3. The module validates the tenant, offering revision, and exposed parameters.
4. It renders a deterministic, claim-owned `OpenBaoCluster`.
5. It projects useful cluster state and connection information back to the claim.

The concrete cluster remains visible and operator-managed. Claims do not replace the direct lifecycle API.

## Module contract

| Concern | Proposed boundary |
| --- | --- |
| API ownership | Module resources use an independently versioned `claims.kubebao.org` group |
| Dependency direction | Claims can consume stable core contracts; core packages cannot depend on the Claims module |
| Installation | The lifecycle core starts and reconciles without Claims CRDs or controllers |
| Materialization | A claim creates a deterministic, ownership-marked `OpenBaoCluster` in the same cluster |
| Failure mode | Unsupported or unsafe shapes block with status instead of silently weakening the service |

## Security boundary

- Tenant namespace access still requires explicit onboarding.
- A non-controller identity cannot spoof or mutate claim ownership markers.
- Connection output remains bound to the claim's custody boundary.
- Dependencies use explicit references instead of broad discovery.
- Secret values stay out of CRDs, status, logs, and Events.
- Ordinary claim edits do not trigger hidden adoption, replacement, or data migration.

## Initial non-goals

- importing unmanaged OpenBao clusters;
- adopting a directly managed cluster during ordinary reconciliation;
- exposing arbitrary low-level workload fields through a claim;
- managing auth methods, policies, engines, or audit devices continuously after bootstrap; or
- moving secret values between clusters.

The design can move into current documentation only after its API packages, generated CRDs, installation wiring,
admission, architecture rules, and lifecycle tests land together.
