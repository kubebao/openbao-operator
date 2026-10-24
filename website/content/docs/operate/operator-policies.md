---
title: Reconcile approved operator policies
description: Enroll policy reconciliation, approve changed capabilities, and recover missing built-in policies.
weight: 65
verifiedBy:
  - internal/adapter/config/operator_policies.go
  - internal/adapter/config/selfinit_gohcl.go
  - internal/service/configuration/policies.go
  - .github/workflows/release.yml
  - test/e2e/policyreconciliation/policies_test.go
---

Set `spec.reconcilePolicies: true` to let the operator repair its built-in OpenBao policies. The feature is disabled
by default. An administrator approves exact policy contents; the operator restores them when they are missing or changed.
Approval covers both supported upgrade strategies, so switching strategies needs no new approval.

The operator replaces custom edits to `openbao-operator`, `openbao-operator-upgrade`, and `openbao-operator-restore`.
It also manages `openbao-operator-backup` when `spec.backup` is configured. Custom JWT role names do not change these
policy names. Auth methods, JWT roles, and the approval policy remain administrator-managed after initialization.

See the [security boundary](../../security/threat-model/#approved-policy-reconciliation) before enabling the feature.

## Enable a new cluster

Add this fragment before the cluster initializes:

```yaml
spec:
  reconcilePolicies: true
  selfInit:
    enabled: true
    oidc:
      enabled: true
```

Self-init creates the initial approval and attaches it to the controller role. No manual enrollment is needed.
Keep your [human administrator authentication](../../get-started/operator-authentication/)
configuration; operator authentication does not provide human access.

If the cluster is already initialized, use the enrollment procedure below, including when it originally used self-init.

## Get an approval file

1. Download the matching asset from the intended operator version's
   [GitHub release](https://github.com/kubebao/openbao-operator/releases). These assets are introduced with this feature
   and are not available in earlier releases.

   | Intended configuration | Release asset |
   | --- | --- |
   | `spec.backup` is absent | `operator-policy-approval.hcl` |
   | `spec.backup` is present | `operator-policy-approval-with-backup.hcl` |

2. [Verify the release file](../../security/supply-chain/#verify-policy-approval-files) against the release's signed
   checksums. Use the intended operator version, rather than the latest release by default.
3. Review the approved paths and capabilities. Save the file as `policy-approval.hcl` in your administrator-managed
   configuration repository. Preserve its contents: approval matches exact policy text, including formatting.

Both files approve the RollingUpdate and BlueGreen variants. The operator installs only the variant required by the
current strategy and preserves the permissions needed by an unfinished upgrade.

## Enroll an existing cluster

First configure [operator authentication](../../get-started/operator-authentication/) if the cluster does not already
have the controller's JWT auth mount and role. Policy reconciliation also works without self-init, but does not create
or repair authentication configuration.

1. [Get and review an approval file](#get-an-approval-file), authenticate to OpenBao as an administrator, and apply it:

   ```sh
   bao policy write openbao-operator-policy-approval policy-approval.hcl
   ```

   If the existing policy requires compare-and-set, use [CAS](#update-with-compare-and-set) instead.
2. Add `openbao-operator-policy-approval` to `token_policies` in the complete administrator-managed definition of
   `auth/jwt-operator/role/openbao-operator`. Apply that complete definition.
   Preserve its subject, audience, claims, and token settings; a partial role write can reset omitted settings.
   Attach approval only to the controller role, not the backup, upgrade, or restore roles.
3. Set `spec.reconcilePolicies: true` and [verify reconciliation](#verify-reconciliation).
   With standard JWT authentication, cached tokens might lack the new policy until they expire. Restart the controller
   if it needs to obtain fresh tokens sooner. Inline JWT authentication uses the updated role on its next request.

## Approve changed capabilities

Get and review a new approval file when an operator release changes policy contents or you add backup configuration.
Apply it with `bao policy write` as above, or CAS when required, before deploying the change. Role enrollment is not
repeated. For a shared operator, approve every affected cluster before upgrading the operator.

Replace the previous approval rather than accumulating old policy versions. Each retained version remains available
to the controller. An operator upgrade with unchanged policies, or a switch between supported upgrade strategies,
needs no new approval. An existing administrator-managed fleet workflow can apply the reviewed files.

## Verify reconciliation

Check the condition and last successful verification:

```sh
kubectl -n <namespace> get openbaocluster <cluster> \
  -o jsonpath='{.status.conditions[?(@.type=="PolicyReconciliationReady")]}{"\n"}{.status.workload.policyReconciliation.lastVerified}{"\n"}'
```

Expect `PolicyReconciliationReady=True` and a populated `lastVerified`. A successful observation does not validate a
backup or upgrade end to end. If the condition is false, inspect its message and the cluster's warning events.

| Failure | Administrator action |
| --- | --- |
| Approval, controller role, or auth mount is missing | Restore the missing configuration; the operator does not fall back to a root token |
| A policy write is denied | Apply approval for the intended operator version and backup configuration |
| Backup was added after initialization | Create the backup JWT role if it is missing, as well as updating approval |
| Recovery restored older policies or authentication | Restore the intended authentication configuration and approval |

Verification runs at most every five minutes when unchanged and healthy. A desired policy change triggers an immediate
check. Permission failures retry after five minutes. See [workload reconciliation](../../architecture/workload-lifecycle/#reconcile-approved-policies)
and [operation gating](../../architecture/operations/#check-policy-readiness-per-operation) for the full behavior.

## Update with compare-and-set

On OpenBao 2.6 or later, compare-and-set (CAS) prevents overwriting concurrent approval changes. Read the current version
and replace `REPLACE_WITH_VERSION` below with that value. For a missing policy, use `cas=-1`.

```sh
bao read -field=version sys/policies/acl/openbao-operator-policy-approval
bao write sys/policies/acl/openbao-operator-policy-approval \
  policy=@policy-approval.hcl cas=REPLACE_WITH_VERSION cas_required=true
```

Setting `cas_required=true` requires CAS on later updates. If the check fails, read and review the current approval
before retrying. CAS protects writes; it does not guarantee a fresh read from a standby.

## Revoke policy management

Delete `openbao-operator-policy-approval` in OpenBao and remove it from the controller role. Set
`spec.reconcilePolicies: false` to stop reconciliation attempts. Disabling that field alone does not revoke permissions
already granted in OpenBao.

Revoking policy management does not revoke operational policies or lifecycle identities. Removing backup configuration
also leaves its policy and role in place. Retire these separately through administrator configuration when required.
