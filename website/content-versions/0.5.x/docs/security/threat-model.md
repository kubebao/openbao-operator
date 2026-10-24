---
title: Threat model
description: Assets, actors, trust boundaries, threats, and operator controls for an OpenBao service on Kubernetes.
eyebrow: Security · Fundamentals
weight: 1
verifiedBy:
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/adapter/security/workload_labels.go
  - internal/platform/admission/check.go
  - internal/platform/resourceownership
  - internal/service/provisioner/manager_secret_rbac.go
---

Use this model to review a concrete deployment. It names the boundary; it does not certify the surrounding cluster, cloud account, or human process.

## Protected assets

- OpenBao storage data, unseal material, recovery keys, and root-level credentials
- Kubernetes Secrets and projected service-account tokens used by lifecycle jobs
- the `OpenBaoCluster`, `OpenBaoRestore`, and `OpenBaoTenant` intent accepted by the API
- operator-managed workloads, RBAC, network policy, services, and certificates
- backup objects and the identities that may read or write them

## Actors and boundaries

| Actor | Expected authority | Boundary to review |
| --- | --- | --- |
| Platform administrator | Installs the operator and delegates dangerous controls | Cluster-wide CRDs, admission, operator identity, and namespace onboarding |
| Tenant author | Manages ordinary cluster intent in an introduced namespace | Must not gain implicit publication, restore, image, or trust-root authority |
| Operator controller | Reconciles declared state into namespaced resources | Must use least privilege and preserve managed-resource ownership |
| OpenBao administrator | Manages auth, policy, audit, and secrets inside OpenBao | Separate from Kubernetes resource authorship |
| Supply-chain attacker | Attempts to replace images, actions, packages, or release artifacts | Digests, signatures, provenance, protected workflows, and dependency review |

## Primary threats

### Privilege expansion through cluster intent

An otherwise ordinary cluster author requests a public endpoint, custom helper image, cloud identity, trust root, or restore target that crosses the platform boundary.

**Control:** delegated RBAC and admission require explicit authority for protected fields and references.

### Mutation of managed resources

An identity patches an operator-managed workload or policy directly, bypassing the owning custom resource.

**Control:** provenance annotations and admission protection make the owning resource the expected mutation path.

### Credential exposure

Secrets, tokens, keys, or bootstrap material appear in logs, command arguments, status, or long-lived configuration.

**Control:** Secret references, projected tokens, redaction discipline, one-shot bootstrap cleanup, and narrow retention.

### Compromised artifact

A mutable or malicious image, dependency, workflow action, or release artifact enters the runtime or publication path.

**Control:** pinned dependencies, image verification, signed artifacts, provenance, and CI security gates.

## Assumptions and exclusions

{{< callout type="warning" title="Kubernetes administrator remains trusted" >}}
A cluster administrator can normally bypass namespace RBAC, alter admission configuration, read cluster-level credentials, or replace the operator. Protect that identity outside the operator.
{{< /callout >}}

The model also assumes the Kubernetes API, storage encryption, node security, cloud identity plane, DNS, and external key management systems are operated according to their own threat models.

## Review checklist

{{< checklist title="Deployment review" >}}
- list every identity that can create or update operator custom resources
- list every identity delegated publication, restore, image, trust-root, or cloud-identity controls
- confirm tenant namespaces cannot mutate operator-owned cluster-scoped policy
- confirm images and release artifacts are pinned and verifiable
- rehearse loss of leadership, sealed state, and restore without relying on bootstrap credentials
{{< /checklist >}}
