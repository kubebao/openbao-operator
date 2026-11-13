---
title: Migrate controller JWT authentication
description: Replace the shared controller credential with a target-specific audience on an existing cluster.
eyebrow: Operate
weight: 48
verifiedBy:
  - internal/adapter/auth/controller_jwt.go
  - internal/port/auth/operator_jwt.go
  - test/integration/controller_jwt_test.go
  - test/integration/controller_jwt_issuance_test.go
---

Migrate one OpenBaoCluster at a time. The procedure changes controller authentication without recreating OpenBao or
its storage. Existing clusters keep Shared mode after an operator upgrade, including existing Hardened clusters.
For new clusters, [declare the controller JWT mode](../controller-jwt-mode/). All new clusters require Target and
self-init OIDC, regardless of security profile.

## Prepare the installation

1. Install the new CRDs, admission policies, controller RBAC, and controller Deployment. Helm upgrades do not update existing CRDs;
   follow the [installation procedure](../../get-started/install/) for your installation method.
2. Confirm the controller has permission to create tokens for its own ServiceAccount in the operator namespace.
   The installation Role restricts `serviceaccounts/token` to that ServiceAccount name. Do not grant this permission
   across tenant namespaces.
3. Confirm the controller Deployment supplies `POD_NAME`, `POD_UID`, `POD_NAMESPACE`, and
   `OPERATOR_SERVICE_ACCOUNT_NAME`. The shipped Helm and Kustomize installations supply these values.
4. Establish an OpenBao administrator session that can read and update `auth/jwt-operator/role/openbao-operator`.
   Retain an administrator login path independently of the controller JWT. `spec.reconcilePolicies` does not grant
   permission to update JWT roles.
5. Finish active upgrade and restore operations before migration. Record the current controller role, its accepted
   audiences, exact subject restrictions, policies, and token lifetime settings. Preserve custom restrictions and
   the policy-approval grant if enrolled.

## Prepare the target role

Set the namespace and cluster name, then derive the target audience from the stored CR UID:

```sh
namespace=<namespace>
cluster=<cluster>
cluster_uid=$(kubectl -n "$namespace" get openbaocluster "$cluster" -o jsonpath='{.metadata.uid}')
test -n "$cluster_uid"
target_audience="urn:openbao:controller:$cluster_uid"
```

Using your OpenBao administrator session, add `target_audience` to the existing controller role's `bound_audiences`.
Retain the existing audience temporarily. Preserve all other role fields, including the subject allowlist, policy
assignments, issuer configuration, and TTL restrictions. Do not replace a customized role with the minimal bootstrap
example. Keep backup, restore, and upgrade roles unchanged.

Verify a fresh JWT issued for the target audience can log in as `openbao-operator` before changing the CR. Use the
actual controller ServiceAccount and Kubernetes issuer. Handle test JWTs as credentials: do not print them, pass them
in command-line arguments, or retain them in shell history or logs.

The role still accepts the shared credential during this stage. This is migration preparation, not proof of isolation.

## Switch the controller

Set the mode in the GitOps source of truth, or apply this patch if the resource is managed directly:

```sh
kubectl -n "$namespace" patch openbaocluster "$cluster" --type=merge \
  -p '{"spec":{"controllerJWTMode":"Target"}}'
```

The CR must have both `spec.selfInit.enabled` and `spec.selfInit.oidc.enabled` set to `true`. This does not rerun self-init
on an initialized cluster. The operator requests a ten-minute Pod-bound JWT for the target audience and uses it for
Raft maintenance and approved policy reconciliation. Verify that these operations succeed without authentication errors.

{{< callout type="warning" title="Target mode cannot be downgraded" >}}
With admission enforcement enabled, selecting Target prevents changing the mode to Shared or removing the field. This prevents an old
manifest from restoring shared credentials. If authentication fails, repair issuance permissions or the OpenBao role
through your administrator session. The controller never falls back to the shared JWT. Do not downgrade to an operator
version that predates this field: an older binary can ignore the field and resume sending its shared credential.
{{< /callout >}}

## Remove shared trust and verify isolation

1. Update the controller role to accept only the target audience. Preserve its other restrictions and grants.
2. Verify a fresh target-specific JWT succeeds against this target.
3. Verify a fresh JWT for the old shared audience fails against this target.
4. Verify a target-specific JWT for an unrelated cluster fails against this target, and this target's JWT fails there.
5. Test the configured transport. If both `inline` and `standard` are supported in your deployment, verify both.
6. Revoke existing controller OpenBao sessions issued before the role change, or wait for their effective maximum
   lifetime. Changing a JWT role does not revoke previously issued OpenBao tokens. Account for renewable sessions.
7. Take a new snapshot after role verification and record the audience in the recovery procedure.

Successful login with the new JWT does not establish isolation while another role or audience still accepts the shared
controller identity. Inspect administrator-created roles on the JWT mount as part of verification. The selected CR mode
records how the controller obtains credentials; it does not attest to every authentication grant inside OpenBao.

Shared targets in the same installation remain exposed to replay among targets that trust the same controller identity
and audience. Complete migration or use separate, scoped installations before claiming isolation for the whole fleet.

## Recover from older snapshots

Snapshots include JWT role configuration. An older snapshot can remove target trust or restore shared trust. A recreated
OpenBaoCluster also receives a new UID and audience. Arrange a usable administrator login in the restored state, or
preauthorize the recovery destination's audience on the source before taking the snapshot. Source and destination then
share a deliberate recovery authentication domain; unrelated tenants must not join that allowlist.

After recovery, reapply the intended role configuration and repeat the positive and negative authentication checks.
The controller remains in Target mode and can report authentication failures until this is complete. See
[restore authentication](../restore/authentication/).
