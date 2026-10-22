---
title: Configure controller JWT authentication
description: Require Target for new clusters and migrate existing Shared authentication without recreating storage.
eyebrow: Operate
weight: 49
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/port/auth/operator_jwt.go
  - test/integration/controller_jwt_creation_test.go
---

Every new OpenBaoCluster must declare `spec.controllerJWTMode: Target`, enable `selfInit.enabled` and
`selfInit.oidc.enabled`, and provide a non-empty bootstrap request list. These requirements apply to both Development
and Hardened profiles. OpenBao initializes itself, configures controller authentication, and revokes its bootstrap root
token. Define a usable administrator login and its recovery path before creating the cluster.

The shipped admission policies enforce these requirements during CREATE. The field remains optional in the CRD
schema to preserve updates to existing clusters. The schema does not declare a default, and admission does not insert
a mode.

| Request | Admission behavior |
| --- | --- |
| CREATE with the mode omitted | Reject the request for either profile |
| CREATE a Development cluster with Shared | Reject the request |
| CREATE a cluster with Target | Require self-init and self-init OIDC |
| CREATE a Hardened cluster with Shared | Reject the request |
| UPDATE an existing cluster with an omitted or Shared mode | Preserve Shared compatibility; other validation still applies |
| UPDATE a Target cluster to Shared or remove its mode | Reject the request |

## Configure a new cluster

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


## Preserve existing clusters

Upgrading the operator or its CRDs does not change the mode of an existing cluster. An omitted mode remains effective
Shared authentication. Existing Hardened clusters can keep Shared during this upgrade; migrate them separately to
obtain target-specific controller credentials.

Existing GitOps manifests can continue to omit the field when updating the same object. Deleting and recreating the
object is a CREATE operation and requires Target and self-init OIDC for either profile.

To change an existing Shared cluster to Target, follow
[Migrate controller JWT authentication](../controller-jwt-migration/). Prepare and verify the OpenBao role before
changing the CR. The operator does not migrate existing roles. No bulk pinning tool or automatic defaulting webhook is
required.
