---
title: Security
description: Review OpenBao Operator trust boundaries, security profiles, admission controls, workload posture, and tenant isolation.
eyebrow: Security
weight: 4
hideChildren: true
aliases:
  - /latest/security/
  - /dev/security/
  - /docs/next/security/
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/platform/admission/check.go
  - internal/platform/hardenedcontract/contract.go
---

The operator coordinates sensitive infrastructure; it does not make every identity trusted. Review who can express intent, who may approve dangerous controls, and which component owns each secret or resource.

## Security routes

<div class="link-grid">
  <a href="threat-model/"><strong>Threat model</strong><p>Assets, actors, boundaries, assumptions, and controls.</p></a>
  <a href="tenant-boundaries/"><strong>Tenant boundaries</strong><p>Namespace introduction, controller authority, user bindings, and cleanup.</p></a>
  <a href="secrets/"><strong>Secrets and credentials</strong><p>Generated bootstrap material, referenced credentials, retention, and access.</p></a>
  <a href="admission/"><strong>Admission guardrails</strong><p>Required policies, protected intent, and fail-closed behavior.</p></a>
  <a href="workload/"><strong>Workload security</strong><p>Pod contexts, token projection, writable paths, and job limits.</p></a>
  <a href="supply-chain/"><strong>Supply chain</strong><p>Signature verification, digest pinning, image credentials, and remaining gaps.</p></a>
</div>

## Security profiles

`Hardened` requires explicit, reviewable production controls. `Development` permits a smaller evaluation setup and
must not be mistaken for an equivalent security posture. See [Choose a security profile](../configure/security-profile/)
for the enforced contract.

## Identity and admission

Creating a cluster does not automatically authorize every high-impact field. Delegated Kubernetes permissions protect
publication, custom images, trust roots, cloud identity references, restore controls, and other dangerous choices. See
[operator authorization](../get-started/operator-authorization/) for the grant procedure.

## Workload and tenant boundaries

The operator uses namespace-scoped delegation, managed-resource provenance, admission policy, security contexts, TLS, and network controls together. No single control substitutes for the rest.
Use [network policy](../configure/network/) and [service exposure and TLS](../configure/expose/) for the configuration
procedures; this section keeps only the security contracts that are not already owned there.

{{< callout type="warning" title="OpenBao still owns its own authorization" >}}
Kubernetes admission and operator RBAC govern the infrastructure control plane. OpenBao policies, auth methods, and audit configuration govern access inside OpenBao.
{{< /callout >}}
