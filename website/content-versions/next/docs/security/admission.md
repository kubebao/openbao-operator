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
  - internal/service/workload/pod_deletion.go
  - test/integration/terminal_pod_cleanup_test.go
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
While running, cluster, restore, tenant-provisioning, and tenant Secret-RBAC paths pause when the dependency set is
lost.

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

## Allow scoped PVC binding synchronization

Some virtual Kubernetes clusters copy a host PVC binding into the virtual PVC through the main resource endpoint.
For example, vCluster 0.37.3 uses `kubernetes-super-admin` for this internal update. The managed-resource mutation
lock rejects that caller by default. The host PVC can be `Bound` while the virtual PVC remains `Pending`.

A platform administrator can allow constrained binding updates for exact caller and namespace pairs. For Helm,
add the observed caller and target namespace to your installation values:

```yaml
admissionPolicies:
  enabled: true
  pvcBindingSync:
    allowedCallers:
      - username: kubernetes-super-admin
        namespaces:
          - openbao-demo
```

The default caller list is empty. Usernames and namespace names match exactly; wildcards and group grants are not
supported. Use the identity observed in the virtual API audit log. The syncer's host-cluster ServiceAccount can
differ from the identity it uses inside the virtual cluster.

The allowance applies only to updates of existing operator-managed PVCs with stored owner-UID provenance. It can
set `spec.volumeName` to a nonempty value when the field is empty, and add these binding annotations:

- `pv.kubernetes.io/bind-completed` with value `yes`;
- `pv.kubernetes.io/bound-by-controller` with value `yes`;
- `volume.beta.kubernetes.io/storage-provisioner` with a nonempty value;
- `volume.kubernetes.io/storage-provisioner` with a nonempty value; and
- `volume.kubernetes.io/selected-node` with a nonempty value.

Existing volume bindings and annotation values cannot change. Labels, owner references, finalizers, provenance,
status, and all other PVC spec fields must remain unchanged. The allowance does not grant PVC creation or deletion,
resize, or access to other managed resource kinds. The policy does not match the PVC `/status` subresource.

{{< callout type="note" title="Trust the configured identity within the declared scope" >}}
Admission applies this allowance to every request from the configured identity in the listed namespaces. It cannot
distinguish a syncer from a person using the same identity. The syncer remains responsible for supplying the correct
host-derived volume mapping, provisioner, and selected node. The selected node affects storage placement. This
allowance supports initial binding metadata; later changes or removals of these annotations remain blocked. Keep
admission enabled and grant only the required caller and namespaces.
{{< /callout >}}

For Kustomize, adapt `config/overlays/pvc-binding-sync/kustomization.yaml`. Its replacement sets the named
`pvc_binding_sync_callers` policy variable without replacing the other checks. The generated ConfigMap is a build
input; the policy does not read it at runtime. Rebuild and reapply the overlay after changing the caller list.

After applying the configuration, verify that the virtual PVC becomes `Bound` and the syncer no longer reports
mutation-lock denials for that PVC. Existing retry backoff can delay synchronization. Removing the configured caller
blocks later binding updates; it does not undo an existing binding.

## Complete deletion of terminal workload Pods

The operator can complete a previously requested deletion of a managed workload Pod. This handles virtual-cluster
cleanup paths where the host Pod has stopped, but admission rejects the syncer's delete of the virtual Pod.
The syncer does not receive additional Pod permissions.

Cleanup requires all of these conditions:

- The stored Pod has a deletion timestamp and phase `Succeeded` or `Failed`.
- No regular, init, or ephemeral container reports a running state.
- The Pod has no finalizers.
- Live reads confirm the Pod's StatefulSet controller UID and the StatefulSet's OpenBaoCluster ownership and owner-UID
  provenance.
- The current OpenBaoCluster has the expected UID, has a profile, is not paused or deleting, and has no restore hold.

The operator requests deletion with zero grace and both Pod UID and resource-version preconditions. A replacement
Pod or concurrent Pod change causes the delete to fail. The operator reevaluates current state on a later reconcile.
It never clears finalizers or starts deletion of a running or pending Pod. Admission and tenant onboarding gates
still apply.

Cleanup runs before other workload steps and handles at most one Pod per reconcile. It uses existing namespace
permissions and the operator controller identity. It does not require a syncer allowlist or host-cluster credentials.
Successful idle workload reconciliation schedules periodic checks in both tenant modes, even when policy
reconciliation is disabled. These checks handle later Pod status and finalizer changes without a Pod watch.
Cleanup failures are logged and retried during later workload reconciliation. They do not block other workload steps.
Cleanup relies on the virtual API's Pod status; a deletion timestamp or `NotReady` condition alone does not establish that
containers have stopped. Unknown or nonterminal status remains blocked.

This cleanup requires a live ownership chain. It does not adopt orphan Pods or override cluster deletion, pause,
restore holds, or finalizer handling. It is not a general force-deletion or host-node fencing mechanism.

## Verify the installed guardrails

{{< command label="inspect" title="List policies and enforcing bindings" >}}
kubectl get validatingadmissionpolicies,validatingadmissionpolicybindings
{{< /command >}}

Also inspect manager startup logs for `Admission policy dependencies ready` and cluster conditions for
`AdmissionPoliciesNotReady` or `UnsafeAdmissionDisabled`. A policy object merely existing is insufficient; its
binding, validation actions, match constraints, and release compatibility are part of the contract.

Do not patch generated workloads as an operational shortcut. Change the owning custom resource or use a documented
maintenance, upgrade, restore, or recovery workflow.
