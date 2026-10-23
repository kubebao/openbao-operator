---
title: Get started
description: Choose a deployment model, install OpenBao Operator, onboard a namespace, and create the first cluster.
eyebrow: Get started
weight: 1
hideChildren: true
verifiedBy:
  - charts/openbao-operator/values.yaml
  - api/v1alpha1/openbaotenant_types.go
  - api/v1alpha1/openbaocluster_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
aliases:
  - /latest/user-guide/
  - /dev/user-guide/
  - /docs/next/get-started/
---

Use this guide to install the operator with an explicit ownership model, create the first cluster, and identify the
controls required before production.

## Before you begin

You need:

- a Kubernetes cluster that meets the [compatibility requirements](../reference/compatibility/);
- cluster-admin access to install CRDs, RBAC, and admission policies;
- `kubectl` configured for the cluster;
- Helm with OCI registry support for the recommended multi-tenant install;
- ownership decisions for the operator namespace, target namespace, and OpenBao administration.

If the cluster cannot pull from public registries, follow [Use private registries](../configure/air-gapped/) to mirror
runtime images and configure registry credentials before installing the operator.

## Choose the outcome

| Outcome | Use this route | Exit condition |
| --- | --- | --- |
| Evaluate the operator | [Install in multi-tenant mode](install/) and create a disposable `Development` cluster | The tenant handoff exists and the cluster reports `Available=True` |
| Prepare production | [Choose the deployment model](deployment-model/), then complete the `Hardened` configuration and operational prerequisites | Human access, external trust, storage, backup, restore, monitoring, and upgrade ownership are tested |
| Operate one dedicated namespace | [Install in single-tenant mode](single-tenant/) with Helm or the maintained Kustomize overlay | Watched namespace, target RoleBinding, controller identity, and namespace all match |

{{< callout type="warning" title="The executable quickstart is for evaluation" >}}
The quickstart uses the `Development` profile, operator-managed TLS, and static auto-unseal. These choices store
sensitive material in Kubernetes Secrets and do not satisfy the `Hardened` profile contract.
{{< /callout >}}

## Choose the tenancy path

Start with [Choose a deployment model](deployment-model/) to decide tenancy, security profile, bootstrap, TLS, and
installation ownership.

For multi-tenant operation:

1. [Install the operator](install/) and verify the controller, Provisioner, CRDs, admission policies, and identities.
2. [Onboard a namespace](onboard-namespace/) through `OpenBaoTenant` and verify the tenant RoleBinding.
3. [Create the first cluster](create-cluster/).
4. [Prepare for production operations](prepare-day-2/).

For one dedicated namespace:

1. [Install in single-tenant mode](single-tenant/) and verify the watched namespace and controller identity.
2. Skip `OpenBaoTenant`; the Provisioner is not part of this model.
3. [Create the first cluster](create-cluster/) in the watched namespace.
4. [Prepare for production operations](prepare-day-2/).

## Review supporting decisions

- [Single-tenant mode](single-tenant/) explains the controller-only Helm and Kustomize paths and their namespace
  contract.
- [Operator authentication](operator-authentication/) maps ServiceAccounts, projected JWTs, OpenBao roles, and human
  bootstrap access.
- [Operator authorization](operator-authorization/) records the distinct controller, backup, restore, and upgrade
  policies.
- [Compatibility](../reference/compatibility/) separates chart constraints from the versions exercised by current CI.

## Finish Get Started with

- a repeatable, version-pinned operator installation;
- an explicit multi-tenant or single-tenant namespace boundary;
- a verified first-cluster status and workload state;
- separate operator and human authentication paths;
- named owners and tested procedures for backup, restore, exposure, monitoring, and upgrade.
