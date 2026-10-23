---
title: Admission guardrails
description: Keep the operator's required admission policies enforced and understand which actions they protect.
eyebrow: Security · Infrastructure
weight: 4
verifiedBy:
  - charts/openbao-operator/templates/admission
  - charts/openbao-operator/templates/controller/deployment.yaml
  - charts/openbao-operator/templates/provisioner/deployment.yaml
  - config/policy
  - internal/controller/openbaocluster/admission_runtime.go
  - internal/controller/openbaorestore/controller.go
  - internal/platform/admission/check.go
  - internal/platform/entrypoint/flags.go
---

The operator treats its `ValidatingAdmissionPolicy` objects and bindings as runtime dependencies. They validate custom
resources, constrain operator identities, protect managed resources, and enforce digest-only images where required.
Keep the complete policy set installed with the matching operator release.

## Keep the release policy set intact

The required set covers these boundaries:

- `OpenBaoCluster`, `OpenBaoRestore`, and `OpenBaoTenant` validation;
- safe controller and provisioner RBAC, ServiceAccount, Secret, and namespace mutations;
- StatefulSet and other managed-resource mutation locks; and
- digest enforcement for Hardened operator-managed StatefulSets and Jobs.

The startup check verifies both each policy and its binding, including that the binding enforces `Deny`. Repository
tests keep the dependency inventory aligned with `config/policy`; do not install a hand-selected subset.

## Use fail-closed mode

`--admission-enforcement=fail` is the manager default. The controller and provisioner wait up to
`--admission-startup-timeout` (60 seconds by default) and refuse to start if the policy dependencies are not ready.
While running, cluster, new restore work, tenant-provisioning, and tenant Secret-RBAC paths pause when the dependency
set is lost. A restore that already crossed its durable Job-creation boundary continues through a narrow drain path.
That path can observe the recorded Job and finish post-restore recovery, but it cannot create or recreate a restore Job.

{{< callout type="warning" title="Disabling chart admission is unsafe mode" >}}
Setting Helm `admissionPolicies.enabled: false` starts both managers with warning enforcement and
`OPENBAO_UNSAFE_ADMISSION_DISABLED=true`. Reconciliation continues without the fail-closed gate, `SecurityRisk`
reports the unsafe mode, and a Hardened cluster cannot report `ProductionReady=True`.
{{< /callout >}}

Use unsafe mode only for isolated development where the risk is explicit. It is not a compatibility setting for a
production cluster whose API server lacks the required admission capability.

## Understand protected intent

An identity that can edit `OpenBaoCluster` still needs explicit authority for high-impact choices. Admission checks
delegated verbs for operations such as network publication, restore, custom executables, cloud identities, image
trust roots, referenced ServiceAccounts, StorageClasses, Gateways, IngressClasses, Secrets, and PVCs.

Admission also requires owner references or operator-written owner-UID provenance before operator identities mutate
or delete deterministic child resources. This blocks a pre-created object with the expected name from being adopted
silently.

Use [operator authorization](../../get-started/operator-authorization/) for grant examples and
[tenant boundaries](../tenant-boundaries/) for the controller/provisioner authority split.

## Verify the installed guardrails

{{< command label="inspect" title="List policies and enforcing bindings" >}}
kubectl get validatingadmissionpolicies,validatingadmissionpolicybindings
{{< /command >}}

Also inspect manager startup logs for `Admission policy dependencies ready` and cluster conditions for
`AdmissionPoliciesNotReady` or `UnsafeAdmissionDisabled`. A policy object merely existing is insufficient; its
binding, validation actions, match constraints, and release compatibility are part of the contract.

Do not patch generated workloads as an operational shortcut. Change the owning custom resource or use a documented
maintenance, upgrade, restore, or recovery workflow.
