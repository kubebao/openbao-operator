---
title: Threat model
description: Assets, actors, trust boundaries, threats, and operator controls for an OpenBao service on Kubernetes.
eyebrow: Security · Fundamentals
weight: 1
verifiedBy:
  - internal/adapter/config/operator_policies.go
  - internal/adapter/config/selfinit_gohcl.go
  - test/e2e/policyreconciliation/policies_test.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/adapter/security/workload_labels.go
  - internal/platform/admission/check.go
  - internal/platform/resourceownership
  - internal/service/provisioner/manager_secret_rbac.go
  - config/policy/openbao-validate-openbaorestore.yaml
  - internal/service/restore/manager_reconcile.go
  - internal/service/restore/manager_status.go
  - test/integration/restore_authority_test.go
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

## Approved policy reconciliation

`openbao-operator-policy-approval` grants the controller read and update access to fixed built-in policy names.
`allowed_parameters` restricts writes to exact approved text. The grant excludes arbitrary policies, the legacy
`sys/policy/` path, auth methods, JWT roles, and the approval policy itself. Broader grants attached to the controller
can defeat this restriction.

Approval authorizes both upgrade variants, including installing BlueGreen permissions on a RollingUpdate cluster.
Runtime reconciliation selects the required variant. Changed policy contents require new administrator approval.

Self-init can bootstrap approval; administrators own later approval changes and authentication repair. Only the
controller role receives the approval grant. Executor roles retain their operation-specific policies.

This feature does not contain a compromised controller: it remains trusted Kubernetes infrastructure with authority
over workloads and their identities. See [tenant boundaries](../tenant-boundaries/) and
[policy enrollment and revocation](../../operate/operator-policies/).

## Assumptions and exclusions

Accepted `OpenBaoRestore` requests carry durable authority for their immutable intent. Revoking the submitter's
permissions does not cancel queued work. Cancellation requires deletion before execution commitment; after commitment,
the operator preserves execution receipts and holds uncertain outcomes for administrator recovery. The controller
pins the target UID during preparation, rather than at API acceptance. Secret contents and named ServiceAccount
identity can change after acceptance. Review [restore authorization and cancellation](../../operate/restore/#understand-authorization-and-cancellation)
before delegating restore access.

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

## Controller credentials across targets

The controller remains trusted infrastructure with Kubernetes authority over its assigned targets. Target-specific
JWT authentication limits replay of credentials captured at a managed OpenBao endpoint. It does not contain a
compromised controller or Kubernetes administrator.

With `spec.controllerJWTMode: Target`, the controller sends a JWT whose sole audience identifies the cluster CR UID.
The target's controller role must accept only its designated audience and expected subject. The bootstrap supplies
these bindings for new clusters. Administrators own later role changes. Successful login alone does not prove that
another accepted audience was removed; verify rejection during [migration](../../operate/controller-jwt-migration/).

Shared mode permits replay between reachable targets that trust the same issuer, subject, and installation audience.
Mixed installations retain this exposure among legacy targets. Use Target mode with verified role bindings before
claiming credential isolation between mutually untrusted tenants. Separate operator installations with distinct
identities and scoped permissions remain an alternative deployment boundary.

JWT validation through OIDC or static keys does not check whether the bound Pod still exists. A deleted Pod's JWT can
remain valid at OpenBao until expiration plus the role's validation leeway. Standard-login OpenBao tokens have their
own lifetimes. Pod binding and short issuance periods do not replace role restrictions or session revocation during an incident.
