---
title: Choose controller JWT authentication
description: Declare Target or Shared for new clusters and preserve existing authentication during upgrades.
eyebrow: Operate
weight: 49
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/port/auth/operator_jwt.go
  - test/integration/controller_jwt_creation_test.go
---

Declare `spec.controllerJWTMode` when you create an OpenBaoCluster. Use `Target` when the cluster supports self-init
OIDC. New Hardened clusters require `Target`. Development clusters can select `Shared` when they do not use self-init
OIDC or need the installation-scoped credential.

The shipped admission policies enforce these requirements during CREATE. The field remains optional in the CRD
schema to preserve updates to existing clusters. The schema does not declare a default, and admission does not insert
a mode.

| Request | Admission behavior |
| --- | --- |
| CREATE with the mode omitted | Reject the request for either profile |
| CREATE a Development cluster with Shared | Accept the mode; other validation still applies |
| CREATE a cluster with Target | Require self-init and self-init OIDC |
| CREATE a Hardened cluster with Shared | Reject the request |
| UPDATE an existing cluster with an omitted or Shared mode | Preserve Shared compatibility; other validation still applies |
| UPDATE a Target cluster to Shared or remove its mode | Reject the request |

## Configure a new Target cluster

Include these fields in the full cluster manifest:

```yaml
spec:
  controllerJWTMode: Target
  selfInit:
    enabled: true
    oidc:
      enabled: true
```

This fragment does not replace the required self-init requests, TLS, unseal, storage, and other profile requirements.
Target requests a short-lived controller JWT whose audience contains the OpenBaoCluster UID. It never falls back to
the shared controller credential. Executor Job authentication is unchanged.

For a Development cluster without self-init OIDC, declare `controllerJWTMode: Shared` instead. Targets that trust the
same shared controller identity remain exposed to credential replay between those targets.

## Preserve existing clusters

Upgrading the operator or its CRDs does not change the mode of an existing cluster. An omitted mode remains effective
Shared authentication. Existing Hardened clusters can keep Shared during this upgrade; migrate them separately to
obtain target-specific controller credentials.

Existing GitOps manifests can continue to omit the field when updating the same object. Deleting and recreating the
object is a CREATE operation and requires a declared mode. The new Hardened requirement also applies to a recreated
Hardened object.

To change an existing Shared cluster to Target, follow
[Migrate controller JWT authentication](../controller-jwt-migration/). Prepare and verify the OpenBao role before
changing the CR. The operator does not migrate existing roles. No bulk pinning tool or automatic defaulting webhook is
required.
