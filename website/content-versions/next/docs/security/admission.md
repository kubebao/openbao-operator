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
  - internal/service/provisioner/namespace_finalization.go
  - test/integration/provisioner_namespace_finalization_test.go
  - test/integration/terminal_pod_cleanup_test.go
  - test/integration/vap_pod_metadata_sync_test.go
  - test/integration/workload_finalization_test.go
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

## Allow scoped Pod annotation synchronization

vCluster can copy host Pod annotations before it writes the virtual Pod's `/status`. If the main-resource annotation
update is denied, that sync pass returns before the status write. The host Pod can run while the virtual Pod stays
`Pending`. Allowing `/status` alone does not resolve this sequence; the mutation policy does not match `pods/status`.

Inspect the denied virtual API request and compare its annotations with the stored Pod. A platform administrator can
allow exact annotation keys for an exact caller and namespace:

```yaml
admissionPolicies:
  enabled: true
  podMetadataSync:
    allowedCallers:
      - username: kubernetes-super-admin
        namespaces:
          - openbao-demo
        annotationKeys:
          - example.com/observed-host-annotation
```

Replace the identity, namespace, and example annotation with values from the denied request. The default list is
empty. Permissions remain separate for each username and namespace, including when the same username has multiple
entries. Wildcards, group grants, and label synchronization are not supported. The caller still needs Kubernetes
RBAC permission to update Pods.

The allowance applies only to updates of existing Pods with the operator's managed labels and a controller UID
reference. OpenBao server Pods require a StatefulSet controller reference. Helper Pods require a Job controller
reference and component `backup`, `restore`, `upgrade`, `upgrade-snapshot`, or `validation-hook`. This includes
backup, restore, upgrade, pre-upgrade snapshot, retention, and validation hook Jobs. It preserves an owner-UID
annotation when present. It can add, change, or remove the configured annotations. Pod spec, status, labels, owner
references, finalizers, deletion metadata, and all other annotations must remain unchanged. It grants no Pod creation
or deletion and no access to other resource kinds.
Annotation keys under `openbao.org/`, `vcluster.loft.sh/`, `kubernetes.io/`, `k8s.io/`, or subdomains of the two
Kubernetes domains remain protected. The Helm schema rejects these keys. In a Kustomize configuration, one protected
key disables the whole allowance for that caller and namespace, including otherwise permitted keys.

{{< callout type="note" title="Review each annotation's effects" >}}
The configured identity can supply any value for the allowed keys on managed server and helper Pods in the listed
namespace. Admission cannot prove that a value came from the host cluster or distinguish a syncer from another client
using the same identity. Grant only observed keys whose effects the platform administrator accepts.
{{< /callout >}}

For Kustomize, adapt `config/overlays/pod-metadata-sync/kustomization.yaml`. The replacement configures only
`pod_metadata_sync_callers`; the policy does not read the generated ConfigMap at runtime. Combine this configuration
with the separate PVC binding allowance when both paths require it.

Verify that the main-resource denial stops and the virtual Pod receives status updates. Syncer retry backoff can
delay recovery. A policy change does not establish that the syncer has retried an existing Pod; inspect its requests
before choosing a version-specific resynchronization procedure. Removing an allowance blocks later annotation
updates and does not undo values already synchronized.

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

### Finish cluster deletion before releasing ownership

Cluster finalization uses a separate cleanup path. The operator requests foreground deletion of its StatefulSets
and keeps the `OpenBaoCluster` finalizer until the workload Pods and StatefulSets are gone. Foreground deletion keeps
the StatefulSet available as ownership proof while its dependent Pods terminate.

The operator can complete an already-deleting terminal Pod under the same Pod-state, ownership, finalizer, and
conditional-delete checks described above. This path requires the original deleting cluster with its operator
finalizer and a foreground-deleting StatefulSet. A paused cluster can finish an authorized deletion; a restore hold
still blocks it. Running, unknown, or unproven Pods and foreign finalizers keep deletion pending.

For legacy StatefulSets without an `openbao.org/owner-uid` annotation, the operator first validates the exact cluster
controller reference and managed labels. It adds only the missing annotation with UID and resource-version checks,
then requeues before deletion. Conflicting provenance is never overwritten. This migration also runs for paused or
already-deleting workloads; subsequent cleanup still requires full managed ownership proof.

The operator does not scale the StatefulSet to zero for this cleanup because `whenScaled: Delete` would remove data
PVCs. For `Retain`, it orphans generated recovery Secrets before waiting for workloads. Admission blocks garbage
collection of managed `<cluster>-unseal-key` and `<cluster>-root-token` Secrets in an active namespace. This also
protects the interval before the first reconcile of a foreground-deleting cluster. The guard uses the generated
name and managed-resource labels, so it also covers Secrets created before owner-UID annotations were introduced.

For `DeletePVCs` and `DeleteAll`, the operator deletes generated recovery Secrets after workload cleanup, using
metadata-only reads, current deletion authority, ownership proof, and UID and resource-version preconditions.
Operator identities and authorized maintenance retain deletion access. Namespace teardown can delete these Secrets
once the namespace has a deletion timestamp; the existing mutation lock still checks the caller. Namespace deletion
cannot preserve namespaced recovery material.

Namespace cleanup controllers cannot delete the provisioner-owned tenant Roles and RoleBindings. These permissions
remain available until all cluster and restore resources in the terminating namespace finish finalization. The
provisioner then deletes only its named, owned tenant resources after checking the live Namespace UID. Each delete
also checks the resource UID and resource version. Foreign finalizers remain untouched. The provisioner polls only
declared target namespaces; this adds no namespace list/watch permissions.

The operator releases the cluster finalizer after cleanup. See [decommission a cluster](../../operate/decommission/).

Both paths require current ownership proof. They do not adopt existing orphan Pods or remove foreign finalizers.
They are not general force-deletion or host-node fencing mechanisms.

## Verify the installed guardrails

{{< command label="inspect" title="List policies and enforcing bindings" >}}
kubectl get validatingadmissionpolicies,validatingadmissionpolicybindings
{{< /command >}}

Also inspect manager startup logs for `Admission policy dependencies ready` and cluster conditions for
`AdmissionPoliciesNotReady` or `UnsafeAdmissionDisabled`. A policy object merely existing is insufficient; its
binding, validation actions, match constraints, and release compatibility are part of the contract.

Do not patch generated workloads as an operational shortcut. Change the owning custom resource or use a documented
maintenance, upgrade, restore, or recovery workflow.
