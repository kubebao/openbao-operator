package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The operators' lock policy denies every write to a managed resource except
// the controller's. While every operator is stopped, the procedure opens a
// handoff in the live policy: the nominated migrator may move a resource's
// controller reference and owner-uid provenance between the two API groups,
// and recreate a StatefulSet from its exact specification. Nothing else
// changes. It works on any installation, because it edits the policy that is
// live, and it is closed again before any operator starts. A running
// operator would also refuse it: it verifies live policies against its
// release contracts.

const (
	handoffReservedMessage = "The openbao.org/owner-uid annotation is reserved for OpenBao Operator managed-resource provenance."
	handoffManagedMessage  = "Direct modification of OpenBao-managed resources is prohibited; " +
		"modify the parent OpenBaoCluster/OpenBaoTenant instead."
	handoffVariablePrefix = "api_migration_"
	handoffOpening        = "variables.api_migration_handoff || ("
	handoffClosing        = ")"
	lockPolicySuffix      = "openbao-lock-managed-resource-mutations"
)

// unchanged compares one field path between the old and new object.
func unchanged(path string) string {
	return "(has(object." + path + ") == has(oldObject." + path + ") && (!has(object." + path +
		") || object." + path + " == oldObject." + path + "))"
}

func apiMigrationPayloadUnchanged() string {
	all := func(paths ...string) string {
		terms := make([]string, len(paths))
		for i, path := range paths {
			terms[i] = unchanged(path)
		}
		return strings.Join(terms, " && ")
	}
	specKinds := `["PersistentVolumeClaim", "Service", "StatefulSet", "Job", "PodDisruptionBudget", "NetworkPolicy",` +
		` "Ingress", "HTTPRoute", "TLSRoute", "BackendTLSPolicy", "ServiceMonitor", "CiliumNetworkPolicy"]`
	return strings.Join([]string{
		`(request.kind.kind in ["Secret", "ConfigMap", "ServiceAccount", "Role", "RoleBinding",` +
			` "PersistentVolumeClaim", "Service", "StatefulSet", "Job", "PodDisruptionBudget", "NetworkPolicy"] ||` +
			` (request.kind.group == "networking.k8s.io" && request.kind.kind == "Ingress") ||` +
			` (request.kind.group == "gateway.networking.k8s.io" &&` +
			` request.kind.kind in ["HTTPRoute", "TLSRoute", "BackendTLSPolicy"]) ||` +
			` (request.kind.group == "monitoring.coreos.com" && request.kind.kind == "ServiceMonitor") ||` +
			` (request.kind.group == "cilium.io" && request.kind.kind == "CiliumNetworkPolicy"))`,
		unchanged("status"),
		`(request.kind.kind != "CiliumNetworkPolicy" || ` + unchanged("specs") + `)`,
		`(request.kind.kind != "Secret" || (` + all("data", "stringData", "type", "immutable") + `))`,
		`(request.kind.kind != "ConfigMap" || (` + all("data", "binaryData", "immutable") + `))`,
		`(request.kind.kind != "ServiceAccount" || (` +
			all("secrets", "imagePullSecrets", "automountServiceAccountToken") + `))`,
		`(request.kind.kind != "Role" || ` + unchanged("rules") + `)`,
		`(request.kind.kind != "RoleBinding" || (` + all("subjects", "roleRef") + `))`,
		`(!(request.kind.kind in ` + specKinds + `) || ` + unchanged("spec") + `)`,
	}, " &&\n        ")
}

func handoffVariables(principal string) []admissionv1.Variable {
	controllers := func(object string) string {
		return object + " != null && has(" + object + ".metadata.ownerReferences) ?\n          " +
			object + ".metadata.ownerReferences.filter(r, has(r.controller) && r.controller) : []"
	}
	ownerUID := func(object string) string {
		return object + " != null && has(" + object + ".metadata.annotations) &&\n          " +
			`"openbao.org/owner-uid" in ` + object + ".metadata.annotations ?\n          " +
			object + `.metadata.annotations["openbao.org/owner-uid"] : ""`
	}
	owner := func(side, kind string) string {
		refs := "variables.api_migration_" + side + "_controllers"
		return refs + ".size() == 1 &&\n        " +
			refs + "[0].apiVersion in variables.api_migration_groups &&\n        " +
			refs + `[0].kind == "` + kind + `" &&` + "\n        " +
			"variables.api_migration_" + side + `_owner_uid != "" &&` + "\n        " +
			refs + "[0].uid == variables.api_migration_" + side + "_owner_uid"
	}
	variables := []struct{ name, expression string }{
		{"api_migration_groups", `["openbao.org/v1alpha1", "kubebao.org/v1alpha1"]`},
		{"api_migration_caller", "request.userInfo.username == variables.api_migration_principal &&\n        " +
			`(!has(request.subResource) || request.subResource == "")`},
		{"api_migration_new_controllers", controllers("object")},
		{"api_migration_old_controllers", controllers("oldObject")},
		{"api_migration_new_owner_uid", ownerUID("object")},
		{"api_migration_old_owner_uid", ownerUID("oldObject")},
		{"api_migration_new_cluster_owner", owner("new", "OpenBaoCluster")},
		{"api_migration_old_cluster_owner", owner("old", "OpenBaoCluster")},
		// A restore record controls only its executor Job.
		{"api_migration_new_restore_owner", owner("new", "OpenBaoRestore")},
		{"api_migration_old_restore_owner", owner("old", "OpenBaoRestore")},
		{"api_migration_payload_unchanged", apiMigrationPayloadUnchanged()},
		{"api_migration_metadata_unchanged", strings.Join([]string{
			unchanged("metadata.labels"), unchanged("metadata.finalizers"), unchanged("metadata.generateName"),
			"!has(object.metadata.deletionTimestamp) && !has(oldObject.metadata.deletionTimestamp)",
			"has(object.metadata.annotations) && has(oldObject.metadata.annotations)",
			`object.metadata.annotations.all(k, v, k == "openbao.org/owner-uid" ||` +
				"\n          (k in oldObject.metadata.annotations && oldObject.metadata.annotations[k] == v))",
			`oldObject.metadata.annotations.all(k, v, k == "openbao.org/owner-uid" ||` +
				"\n          (k in object.metadata.annotations && object.metadata.annotations[k] == v))",
		}, " &&\n        ")},
		// The controller reference moves between the two API groups for the same
		// Cluster, or restore record, name. Its UID follows the owner-uid
		// annotation on both sides.
		{"api_migration_controller_swapped", strings.Join([]string{
			"((variables.api_migration_new_cluster_owner && variables.api_migration_old_cluster_owner) ||" +
				"\n          (request.kind.group == \"batch\" && request.kind.kind == \"Job\" &&" +
				"\n            variables.api_migration_new_restore_owner && variables.api_migration_old_restore_owner))",
			"variables.api_migration_new_controllers[0].apiVersion != variables.api_migration_old_controllers[0].apiVersion",
			"variables.api_migration_new_controllers[0].name == variables.api_migration_old_controllers[0].name",
			"has(variables.api_migration_new_controllers[0].blockOwnerDeletion) ==" +
				"\n          has(variables.api_migration_old_controllers[0].blockOwnerDeletion)",
			"(!has(variables.api_migration_new_controllers[0].blockOwnerDeletion) ||" +
				"\n          variables.api_migration_new_controllers[0].blockOwnerDeletion ==" +
				"\n          variables.api_migration_old_controllers[0].blockOwnerDeletion)",
			"oldObject.metadata.ownerReferences.filter(r, !has(r.controller) || !r.controller) ==" +
				"\n          object.metadata.ownerReferences.filter(r, !has(r.controller) || !r.controller)",
		}, " &&\n        ")},
		// Retained Secrets, claims, and the named upgrade ServiceAccount carry
		// annotation-only provenance. Other ServiceAccounts need a controller.
		{"api_migration_retained_swapped", strings.Join([]string{
			`request.kind.group == "" && (request.kind.kind in ["Secret", "PersistentVolumeClaim"] ||` +
				` (request.kind.kind == "ServiceAccount" && has(object.metadata.labels) &&` +
				` "openbao.org/cluster" in object.metadata.labels &&` +
				` "openbao.org/service-account-role" in object.metadata.labels &&` +
				` "app.kubernetes.io/managed-by" in object.metadata.labels &&` +
				` object.metadata.labels["app.kubernetes.io/managed-by"] == "openbao-operator" &&` +
				` object.metadata.labels["openbao.org/service-account-role"] == "upgrade" &&` +
				` object.metadata.name == object.metadata.labels["openbao.org/cluster"] + "-upgrade-serviceaccount"))`,
			"variables.api_migration_new_controllers.size() == 0 && variables.api_migration_old_controllers.size() == 0",
			unchanged("metadata.ownerReferences"),
			"(!has(object.metadata.ownerReferences) ||" +
				"\n          object.metadata.ownerReferences.all(r, !(r.apiVersion in variables.api_migration_groups)))",
			`variables.api_migration_old_owner_uid != "" && variables.api_migration_new_owner_uid != ""`,
		}, " &&\n        ")},
		// The procedure recreates a StatefulSet from its exact specification to
		// move the immutable claim template provenance with the Cluster.
		{"api_migration_handoff", strings.Join([]string{
			"variables.api_migration_caller",
			"(\n          (request.operation == \"UPDATE\" && object != null && oldObject != null &&" +
				"\n            variables.api_migration_metadata_unchanged && variables.api_migration_payload_unchanged &&" +
				"\n            variables.api_migration_old_owner_uid != variables.api_migration_new_owner_uid &&" +
				"\n            (variables.api_migration_controller_swapped || variables.api_migration_retained_swapped)) ||" +
				"\n          (request.operation == \"CREATE\" && request.kind.group == \"apps\" &&" +
				" request.kind.kind == \"StatefulSet\" &&" +
				"\n            variables.api_migration_new_cluster_owner &&" +
				"\n            (!has(object.spec.volumeClaimTemplates) || object.spec.volumeClaimTemplates.all(t," +
				"\n              !has(t.metadata) || !has(t.metadata.annotations) ||" +
				"\n              !(\"openbao.org/owner-uid\" in t.metadata.annotations) ||" +
				"\n              t.metadata.annotations[\"openbao.org/owner-uid\"] == variables.api_migration_new_owner_uid))) ||" +
				"\n          (request.operation == \"DELETE\" && request.kind.group == \"apps\" &&" +
				" request.kind.kind == \"StatefulSet\" &&" +
				"\n            variables.api_migration_old_cluster_owner)\n        )",
		}, " &&\n        ")},
	}
	result := make([]admissionv1.Variable, 0, 1+len(variables))
	result = append(result, admissionv1.Variable{Name: "api_migration_principal", Expression: strconvQuote(principal)})
	for _, variable := range variables {
		result = append(result, admissionv1.Variable{Name: variable.name,
			Expression: lineBreaks.ReplaceAllString(variable.expression, " ")})
	}
	return result
}

// lineBreaks are the YAML folding the chart once rendered; a line break and
// its indentation read as one space.
var lineBreaks = regexp.MustCompile(`\n\s*`)

// strconvQuote renders a CEL string literal.
func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// openHandoff returns the policy with the handoff for principal added. Only
// the owner-uid and managed-mutation validations gain the alternative.
func openHandoff(spec admissionv1.ValidatingAdmissionPolicySpec, principal string) (admissionv1.ValidatingAdmissionPolicySpec, error) {
	if open, current := handoffPrincipal(spec); open {
		if current != principal {
			return spec, fmt.Errorf("the lock policy has a handoff for another principal")
		}
		return spec, nil
	}
	opened := *spec.DeepCopy()
	opened.Variables = append(opened.Variables, handoffVariables(principal)...)
	for _, message := range []string{handoffReservedMessage, handoffManagedMessage} {
		index := slices.IndexFunc(opened.Validations, func(v admissionv1.Validation) bool { return v.Message == message })
		if index == -1 {
			return spec, fmt.Errorf("the lock policy has no validation %q", message)
		}
		opened.Validations[index].Expression = handoffOpening + opened.Validations[index].Expression + handoffClosing
	}
	return opened, nil
}

// closeHandoff removes exactly what openHandoff added.
func closeHandoff(spec admissionv1.ValidatingAdmissionPolicySpec) (admissionv1.ValidatingAdmissionPolicySpec, error) {
	closed := *spec.DeepCopy()
	closed.Variables = slices.DeleteFunc(closed.Variables, func(v admissionv1.Variable) bool {
		return strings.HasPrefix(v.Name, handoffVariablePrefix)
	})
	for i, validation := range closed.Validations {
		if !strings.HasPrefix(validation.Expression, handoffOpening) {
			continue
		}
		if !strings.HasSuffix(validation.Expression, handoffClosing) ||
			(validation.Message != handoffReservedMessage && validation.Message != handoffManagedMessage) {
			return spec, fmt.Errorf("the lock policy has an unexpected handoff in %q", validation.Message)
		}
		closed.Validations[i].Expression = strings.TrimSuffix(strings.TrimPrefix(validation.Expression, handoffOpening),
			handoffClosing)
	}
	return closed, nil
}

// handoffPrincipal reports whether the policy has a handoff open, and for whom.
func handoffPrincipal(spec admissionv1.ValidatingAdmissionPolicySpec) (bool, string) {
	for _, variable := range spec.Variables {
		if variable.Name == "api_migration_principal" {
			var principal string
			_ = json.Unmarshal([]byte(variable.Expression), &principal)
			return true, principal
		}
	}
	return false, ""
}

// operatorsStopped requires every operator Deployment at zero with no Pods.
func (p *Procedure) operatorsStopped(ctx context.Context) error {
	for _, deployment := range p.plan.Deployments {
		current := &appsv1.Deployment{}
		if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: p.plan.Config.OperatorNamespace, Name: deployment.Name}, current); err != nil {
			return err
		}
		if current.UID != deployment.UID {
			return fmt.Errorf("operator Deployment %s was replaced", deployment.Name)
		}
		if current.Spec.Replicas == nil || *current.Spec.Replicas != 0 {
			return fmt.Errorf("operator Deployment %s is not scaled to zero", deployment.Name)
		}
		pods, err := p.kube.deploymentPods(ctx, p.plan.Config.OperatorNamespace, deployment.Name)
		if err != nil {
			return err
		}
		if len(pods) != 0 {
			return fmt.Errorf("operator Deployment %s still has %d Pod(s)", deployment.Name, len(pods))
		}
	}
	return nil
}

// setHandoff opens or closes the handoff and waits until the API server has
// processed the policy.
func (p *Procedure) setHandoff(ctx context.Context, open bool) (string, error) {
	policy, changed, err := p.patchHandoff(ctx, open)
	if err != nil {
		return "", err
	}
	if !changed {
		return map[bool]string{true: "handoff already open", false: "handoff already closed"}[open], nil
	}
	if err := p.kube.wait(ctx, "the lock policy to be processed", func(ctx context.Context) (bool, error) {
		current := &admissionv1.ValidatingAdmissionPolicy{}
		if err := p.kube.client.Get(ctx, client.ObjectKeyFromObject(policy), current); err != nil {
			return false, err
		}
		if current.UID != policy.UID {
			return false, fmt.Errorf("lock policy %s was replaced while waiting for processing", policy.Name)
		}
		return current.Status.ObservedGeneration == current.Generation, nil
	}); err != nil {
		return "", err
	}
	// The admission plugin observes the processed policy through its own informer.
	time.Sleep(2 * time.Second)
	return map[bool]string{true: "handoff opened for ", false: "handoff closed in "}[open] + policy.Name, nil
}

// patchHandoff retries resource-version conflicts, including policy status
// updates. Every retry reads the policy and opening guards again. A replacement
// policy or another migrator's exception requires administrator review.
func (p *Procedure) patchHandoff(ctx context.Context, open bool) (*admissionv1.ValidatingAdmissionPolicy, bool, error) {
	var policy *admissionv1.ValidatingAdmissionPolicy
	var uid types.UID
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if open {
			if err := p.operatorsStopped(ctx); err != nil {
				return fmt.Errorf("the handoff opens only while every operator is stopped: %w", err)
			}
		}
		current, err := p.installedPolicy(ctx, lockPolicySuffix)
		if err != nil {
			return err
		}
		if uid != "" && current.UID != uid {
			return fmt.Errorf("lock policy %s was replaced during the handoff update", current.Name)
		}
		uid, policy = current.UID, current
		isOpen, principal := handoffPrincipal(policy.Spec)
		if isOpen && principal != p.plan.Config.migratorPrincipal() {
			return fmt.Errorf("lock policy %s has a handoff for another principal", policy.Name)
		}
		if isOpen == open {
			return nil
		}
		var spec admissionv1.ValidatingAdmissionPolicySpec
		if open {
			spec, err = openHandoff(policy.Spec, p.plan.Config.migratorPrincipal())
		} else {
			spec, err = closeHandoff(policy.Spec)
		}
		if err != nil {
			return err
		}
		before := policy.DeepCopy()
		policy.Spec = spec
		if err := p.kube.client.Patch(ctx, policy, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}),
			client.FieldOwner(toolFieldManager)); err != nil {
			return fmt.Errorf("update lock policy %s: %w", policy.Name, err)
		}
		changed = true
		return nil
	})
	return policy, changed, err
}
