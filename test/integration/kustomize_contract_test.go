//go:build integration
// +build integration

package integration

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

func TestKustomizeClusterScopedResourcesHaveNoNamespace(t *testing.T) {
	testCases := []struct {
		name string
		dir  string
	}{
		{
			name: "config-default",
			dir:  filepath.Join("..", "..", "config", "default"),
		},
		{
			name: "config-overlays-single-tenant",
			dir:  filepath.Join("..", "..", "config", "overlays", "single-tenant"),
		},
		{
			name: "config-overlays-single-tenant-custom-identity",
			dir:  filepath.Join("..", "..", "config", "overlays", "single-tenant-custom-identity"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			yamlBytes := kustomizeBuild(t, tc.dir)
			decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(yamlBytes), 4096)

			for {
				var raw map[string]any
				if err := decoder.Decode(&raw); err != nil {
					if errors.Is(err, io.EOF) {
						return
					}
					t.Fatalf("decode YAML: %v", err)
				}
				if len(raw) == 0 {
					continue
				}

				obj := &unstructured.Unstructured{Object: raw}
				if obj.GetAPIVersion() == "" || obj.GetKind() == "" || obj.GetName() == "" {
					continue
				}

				if !isClusterScopedManifestObject(obj.GroupVersionKind()) {
					continue
				}

				if (tc.name == "config-overlays-single-tenant" || tc.name == "config-overlays-single-tenant-custom-identity") &&
					allowsClusterScopedNamespaceInSingleTenantOverlay(obj.GroupVersionKind()) {
					continue
				}

				if obj.GetNamespace() != "" {
					t.Fatalf("cluster-scoped %s %s has unexpected namespace %q", obj.GetKind(), obj.GetName(), obj.GetNamespace())
				}
			}
		})
	}
}

func allowsClusterScopedNamespaceInSingleTenantOverlay(gvk schema.GroupVersionKind) bool {
	return gvk.Group == testAdmissionRegistrationGroup &&
		(gvk.Kind == testKindVAP || gvk.Kind == testKindVAPBinding)
}

func TestKustomizePolicy_BindingsReferenceExistingPolicies(t *testing.T) {
	testCases := []struct {
		name string
		dir  string
	}{
		{
			name: "config-policy",
			dir:  filepath.Join("..", "..", "config", "policy"),
		},
		{
			name: "config-default",
			dir:  filepath.Join("..", "..", "config", "default"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			yamlBytes := kustomizeBuild(t, tc.dir)
			objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
				gvk := u.GroupVersionKind()
				return gvk.Group == testAdmissionRegistrationGroup &&
					(gvk.Kind == testKindVAP || gvk.Kind == testKindVAPBinding)
			})

			policies := make(map[string]struct{})
			bindings := make(map[string]string)
			for _, obj := range objs {
				switch obj.GetKind() {
				case testKindVAP:
					policies[obj.GetName()] = struct{}{}
				case testKindVAPBinding:
					policyName, found, err := unstructured.NestedString(obj.Object, "spec", "policyName")
					if err != nil {
						t.Fatalf("read spec.policyName for binding %s: %v", obj.GetName(), err)
					}
					if !found || policyName == "" {
						t.Fatalf("binding %s has empty spec.policyName", obj.GetName())
					}
					bindings[obj.GetName()] = policyName
				}
			}

			if len(bindings) == 0 {
				t.Fatal("expected at least one ValidatingAdmissionPolicyBinding")
			}

			for bindingName, policyName := range bindings {
				if _, ok := policies[policyName]; !ok {
					t.Fatalf("binding %s references missing policy %s", bindingName, policyName)
				}
			}
		})
	}
}

func TestKustomizeDefault_LockManagedPolicyRequiresOpenBaoLabels(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		gvk := u.GroupVersionKind()
		return gvk.Group == testAdmissionRegistrationGroup &&
			gvk.Kind == testKindVAP &&
			strings.HasSuffix(u.GetName(), "openbao-lock-managed-resource-mutations")
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one openbao-lock-managed-resource-mutations policy, got %d", len(objs))
	}

	resourceRules, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "matchConstraints", "resourceRules")
	if err != nil {
		t.Fatalf("read spec.matchConstraints.resourceRules: %v", err)
	}
	if !found {
		t.Fatal("openbao-lock-managed-resource-mutations policy missing spec.matchConstraints.resourceRules")
	}
	var hasServiceMonitorRule bool
	var hasCiliumPolicyRule bool
	var hasPersistentVolumeClaimRule bool
	var hasServiceAccountRule bool
	for _, rule := range resourceRules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		groups, _, _ := unstructured.NestedStringSlice(ruleMap, "apiGroups")
		resources, _, _ := unstructured.NestedStringSlice(ruleMap, "resources")
		if containsString(groups, "cilium.io") && containsString(resources, "ciliumnetworkpolicies") {
			hasCiliumPolicyRule = true
		}
		if containsString(groups, "monitoring.coreos.com") && containsString(resources, "servicemonitors") {
			hasServiceMonitorRule = true
		}
		if containsString(groups, "") && containsString(resources, "persistentvolumeclaims") {
			hasPersistentVolumeClaimRule = true
		}
		if containsString(groups, "") && containsString(resources, "serviceaccounts") {
			hasServiceAccountRule = true
		}
	}
	if !hasCiliumPolicyRule {
		t.Fatal("managed-resource policy must protect CiliumNetworkPolicies")
	}
	if !hasServiceMonitorRule {
		t.Fatalf("openbao-lock-managed-resource-mutations policy does not protect monitoring.coreos.com ServiceMonitors")
	}
	if !hasPersistentVolumeClaimRule {
		t.Fatalf("openbao-lock-managed-resource-mutations policy does not protect PersistentVolumeClaims")
	}
	if !hasServiceAccountRule {
		t.Fatalf("openbao-lock-managed-resource-mutations policy does not protect ServiceAccounts")
	}

	variables, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "variables")
	if err != nil {
		t.Fatalf("read spec.variables: %v", err)
	}
	if !found {
		t.Fatal("openbao-lock-managed-resource-mutations policy missing spec.variables")
	}

	var hasOpenBaoLabelExpression string
	var maintenanceAuthorizedExpression string
	var maintenanceClusterNameExpression string
	var isManagedExpression string
	var isServiceMonitorRequestExpression string
	var hasCurrentOwnerUIDAnnotationExpression string
	var hasOldOwnerUIDAnnotationExpression string
	var ownerUIDAnnotationAllowedExpression string
	var isKubeSchedulerExpression string
	var isStorageProvisionerServiceAccountExpression string
	var isPersistentVolumeBinderExpression string
	var isPVCProtectionControllerExpression string
	var isStatefulSetControllerExpression string
	var isNamespaceControllerServiceAccountExpression string
	var isStoragePVCBindingUpdateExpression string
	var currentServiceMonitorOwnedExpression string
	var oldServiceMonitorOwnedExpression string
	for _, variable := range variables {
		variableMap, ok := variable.(map[string]any)
		if !ok {
			continue
		}
		name, _ := variableMap["name"].(string)
		expression, _ := variableMap["expression"].(string)
		switch name {
		case "has_openbao_specific_label":
			hasOpenBaoLabelExpression = expression
		case "maintenance_authorized":
			maintenanceAuthorizedExpression = expression
		case "maintenance_cluster_name":
			maintenanceClusterNameExpression = expression
		case "is_managed":
			isManagedExpression = expression
		case "is_service_monitor_request":
			isServiceMonitorRequestExpression = expression
		case "has_current_owner_uid_annotation":
			hasCurrentOwnerUIDAnnotationExpression = expression
		case "has_old_owner_uid_annotation":
			hasOldOwnerUIDAnnotationExpression = expression
		case "owner_uid_annotation_allowed":
			ownerUIDAnnotationAllowedExpression = expression
		case "is_kube_scheduler":
			isKubeSchedulerExpression = expression
		case "is_storage_provisioner_serviceaccount":
			isStorageProvisionerServiceAccountExpression = expression
		case "is_persistent_volume_binder":
			isPersistentVolumeBinderExpression = expression
		case "is_pvc_protection_controller":
			isPVCProtectionControllerExpression = expression
		case "is_statefulset_controller":
			isStatefulSetControllerExpression = expression
		case "is_namespace_controller_serviceaccount":
			isNamespaceControllerServiceAccountExpression = expression
		case "is_storage_pvc_binding_update":
			isStoragePVCBindingUpdateExpression = expression
		case "current_service_monitor_is_operator_owned":
			currentServiceMonitorOwnedExpression = expression
		case "old_service_monitor_is_operator_owned":
			oldServiceMonitorOwnedExpression = expression
		}
	}

	if !strings.Contains(hasOpenBaoLabelExpression, `k.startsWith("openbao.org/")`) {
		t.Fatalf(
			"has_openbao_specific_label expression does not enforce openbao.org/* label gate: %q",
			hasOpenBaoLabelExpression,
		)
	}
	if !strings.Contains(isManagedExpression, "variables.has_openbao_specific_label") {
		t.Fatalf("is_managed expression does not require has_openbao_specific_label: %q", isManagedExpression)
	}
	if !strings.Contains(maintenanceClusterNameExpression, `"openbao.org/cluster"`) {
		t.Fatalf(
			"maintenance_cluster_name expression does not prefer openbao.org/cluster: %q",
			maintenanceClusterNameExpression,
		)
	}
	if !strings.Contains(maintenanceClusterNameExpression, `"app.kubernetes.io/instance"`) {
		t.Fatalf(
			"maintenance_cluster_name expression does not fall back to app.kubernetes.io/instance: %q",
			maintenanceClusterNameExpression,
		)
	}
	if !strings.Contains(maintenanceClusterNameExpression, `request.operation == "CREATE"`) ||
		!strings.Contains(maintenanceClusterNameExpression, `variables.maintenance_old_labels["openbao.org/cluster"]`) ||
		!strings.Contains(maintenanceClusterNameExpression, `variables.maintenance_old_labels["app.kubernetes.io/instance"]`) {
		t.Fatalf(
			"maintenance_cluster_name expression must authorize updates/deletes from old object labels: %q",
			maintenanceClusterNameExpression,
		)
	}
	if !strings.Contains(maintenanceAuthorizedExpression, `authorizer.group("openbao.org")`) {
		t.Fatalf("maintenance_authorized expression does not use the CEL authorizer: %q", maintenanceAuthorizedExpression)
	}
	if !strings.Contains(maintenanceAuthorizedExpression, `check("maintenance")`) {
		t.Fatalf(
			"maintenance_authorized expression does not check the custom maintenance verb: %q",
			maintenanceAuthorizedExpression,
		)
	}
	if !strings.Contains(isServiceMonitorRequestExpression, `request.kind.group == "monitoring.coreos.com"`) ||
		!strings.Contains(isServiceMonitorRequestExpression, `request.kind.kind == "ServiceMonitor"`) {
		t.Fatalf("is_service_monitor_request expression does not target ServiceMonitors: %q", isServiceMonitorRequestExpression)
	}
	if !strings.Contains(hasCurrentOwnerUIDAnnotationExpression, `"openbao.org/owner-uid"`) {
		t.Fatalf("has_current_owner_uid_annotation expression does not check owner UID provenance: %q", hasCurrentOwnerUIDAnnotationExpression)
	}
	if !strings.Contains(hasOldOwnerUIDAnnotationExpression, `"openbao.org/owner-uid"`) {
		t.Fatalf("has_old_owner_uid_annotation expression does not check owner UID provenance: %q", hasOldOwnerUIDAnnotationExpression)
	}
	if !strings.Contains(ownerUIDAnnotationAllowedExpression, "variables.is_operator_controller") ||
		!strings.Contains(ownerUIDAnnotationAllowedExpression, "variables.is_operator_provisioner") ||
		!strings.Contains(ownerUIDAnnotationAllowedExpression, "variables.is_kube_controller_manager") ||
		!strings.Contains(ownerUIDAnnotationAllowedExpression, "variables.is_system_controller") ||
		!strings.Contains(ownerUIDAnnotationAllowedExpression, "variables.is_kube_system_controller") {
		t.Fatalf("owner_uid_annotation_allowed expression does not restrict owner UID provenance writers: %q", ownerUIDAnnotationAllowedExpression)
	}
	if !strings.Contains(isStatefulSetControllerExpression, "system:controller:statefulset-controller") ||
		!strings.Contains(isStatefulSetControllerExpression, "system:serviceaccount:kube-system:statefulset-controller") {
		t.Fatalf("is_statefulset_controller expression does not recognize StatefulSet controller identities: %q", isStatefulSetControllerExpression)
	}
	if !strings.Contains(isNamespaceControllerServiceAccountExpression, "system:serviceaccount:kube-system:namespace-controller") {
		t.Fatalf(
			"is_namespace_controller_serviceaccount expression does not recognize the namespace controller identity: %q",
			isNamespaceControllerServiceAccountExpression,
		)
	}
	if !strings.Contains(isKubeSchedulerExpression, "system:kube-scheduler") ||
		!strings.Contains(isKubeSchedulerExpression, "system:serviceaccount:kube-system:kube-scheduler") {
		t.Fatalf("is_kube_scheduler expression does not recognize scheduler identities: %q", isKubeSchedulerExpression)
	}
	if !strings.Contains(isStorageProvisionerServiceAccountExpression, "system:serviceaccount:") ||
		!strings.Contains(isStorageProvisionerServiceAccountExpression, "csi|provisioner") {
		t.Fatalf("is_storage_provisioner_serviceaccount expression does not recognize CSI/provisioner identities: %q", isStorageProvisionerServiceAccountExpression)
	}
	if !strings.Contains(isPersistentVolumeBinderExpression, "system:serviceaccount:kube-system:persistent-volume-binder") {
		t.Fatalf("is_persistent_volume_binder expression does not recognize the PV binder identity: %q", isPersistentVolumeBinderExpression)
	}
	if !strings.Contains(isPVCProtectionControllerExpression, "system:serviceaccount:kube-system:pvc-protection-controller") {
		t.Fatalf("is_pvc_protection_controller expression does not recognize the PVC protection identity: %q", isPVCProtectionControllerExpression)
	}
	if !strings.Contains(isStoragePVCBindingUpdateExpression, "variables.is_kube_scheduler") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "variables.is_kube_controller_manager") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "variables.is_persistent_volume_binder") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "variables.is_pvc_protection_controller") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "variables.is_storage_provisioner_serviceaccount") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "object.metadata.labels == oldObject.metadata.labels") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, `k.startsWith("openbao.org/")`) ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "object.spec.resources == oldObject.spec.resources") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "object.spec.volumeName") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "external-provisioner.volume.kubernetes.io/finalizer") ||
		!strings.Contains(isStoragePVCBindingUpdateExpression, "kubernetes.io/pvc-protection") {
		t.Fatalf("is_storage_pvc_binding_update expression does not constrain storage binding updates: %q", isStoragePVCBindingUpdateExpression)
	}
	if !strings.Contains(currentServiceMonitorOwnedExpression, `object.metadata.name.endsWith("-metrics")`) ||
		!strings.Contains(currentServiceMonitorOwnedExpression, `"app.kubernetes.io/managed-by"`) ||
		!strings.Contains(currentServiceMonitorOwnedExpression, `"openbao.org/cluster"`) ||
		!strings.Contains(currentServiceMonitorOwnedExpression, `ref.kind == "OpenBaoCluster"`) ||
		!strings.Contains(currentServiceMonitorOwnedExpression, `has(ref.controller)`) {
		t.Fatalf("current ServiceMonitor ownership expression is incomplete: %q", currentServiceMonitorOwnedExpression)
	}
	if !strings.Contains(oldServiceMonitorOwnedExpression, `oldObject.metadata.name.endsWith("-metrics")`) ||
		!strings.Contains(oldServiceMonitorOwnedExpression, `"app.kubernetes.io/managed-by"`) ||
		!strings.Contains(oldServiceMonitorOwnedExpression, `"openbao.org/cluster"`) ||
		!strings.Contains(oldServiceMonitorOwnedExpression, `ref.kind == "OpenBaoCluster"`) ||
		!strings.Contains(oldServiceMonitorOwnedExpression, `has(ref.controller)`) {
		t.Fatalf("old ServiceMonitor ownership expression is incomplete: %q", oldServiceMonitorOwnedExpression)
	}

	validations, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "validations")
	if err != nil || !found {
		t.Fatalf("read policy validations: found=%v err=%v", found, err)
	}
	var foundServiceMonitorOwnershipGuard bool
	var foundOwnerUIDAnnotationGuard bool
	var foundStatefulSetPVCGuard bool
	var foundStoragePVCBindingGuard bool
	var foundNamespaceControllerDeleteGuard bool
	for _, validation := range validations {
		validationMap, ok := validation.(map[string]any)
		if !ok {
			continue
		}
		message, _ := validationMap["message"].(string)
		expression, _ := validationMap["expression"].(string)
		if strings.Contains(message, "ServiceMonitors that match the OpenBao metrics ownership shape") &&
			strings.Contains(expression, "variables.is_operator_controller") &&
			strings.Contains(expression, "variables.current_service_monitor_is_operator_owned") &&
			strings.Contains(expression, "variables.old_service_monitor_is_operator_owned") {
			foundServiceMonitorOwnershipGuard = true
		}
		if strings.Contains(message, "openbao.org/owner-uid annotation is reserved") &&
			strings.Contains(expression, "variables.owner_uid_annotation_allowed") &&
			strings.Contains(expression, "variables.has_current_owner_uid_annotation") &&
			strings.Contains(expression, "variables.has_old_owner_uid_annotation") {
			foundOwnerUIDAnnotationGuard = true
		}
		if strings.Contains(message, "Direct modification of OpenBao-managed resources is prohibited") &&
			strings.Contains(expression, "variables.is_statefulset_controller") &&
			strings.Contains(expression, `"persistentvolumeclaims"`) &&
			strings.Contains(expression, `request.operation == "CREATE"`) {
			foundStatefulSetPVCGuard = true
		}
		if strings.Contains(message, "Direct modification of OpenBao-managed resources is prohibited") &&
			strings.Contains(expression, "variables.is_storage_pvc_binding_update") {
			foundStoragePVCBindingGuard = true
		}
		if strings.Contains(message, "Direct modification of OpenBao-managed resources is prohibited") &&
			strings.Contains(expression, "variables.is_namespace_controller_serviceaccount") &&
			strings.Contains(expression, `request.operation == "DELETE"`) {
			foundNamespaceControllerDeleteGuard = true
		}
	}
	if !foundServiceMonitorOwnershipGuard {
		t.Fatalf("openbao-lock-managed-resource-mutations policy missing ServiceMonitor ownership guard")
	}
	if !foundOwnerUIDAnnotationGuard {
		t.Fatalf("openbao-lock-managed-resource-mutations policy missing owner UID provenance annotation guard")
	}
	if !foundStatefulSetPVCGuard {
		t.Fatalf("openbao-lock-managed-resource-mutations policy missing StatefulSet controller PVC create guard")
	}
	if !foundStoragePVCBindingGuard {
		t.Fatalf("openbao-lock-managed-resource-mutations policy missing storage PVC binding update guard")
	}
	if !foundNamespaceControllerDeleteGuard {
		t.Fatalf("openbao-lock-managed-resource-mutations policy missing namespace controller delete guard")
	}
}

func TestKustomizeDefault_OpenBaoClusterPolicyGuardsUpgradeStrategySwitches(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		gvk := u.GroupVersionKind()
		return gvk.Group == testAdmissionRegistrationGroup &&
			gvk.Kind == testKindVAP &&
			strings.HasSuffix(u.GetName(), "openbao-validate-openbaocluster")
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one openbao-validate-openbaocluster policy, got %d", len(objs))
	}

	variables, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "variables")
	if err != nil || !found {
		t.Fatalf("read policy variables: found=%v err=%v", found, err)
	}

	var hasRequestedStrategy bool
	var hasPreviousStrategy bool
	var hasStrategyChanged bool
	var hasIdleStatusGuard bool
	var idleStatusExpression string
	var hasHandledRequestsGuard bool
	for _, variable := range variables {
		variableMap, ok := variable.(map[string]any)
		if !ok {
			continue
		}
		name, _ := variableMap["name"].(string)
		switch name {
		case "requested_upgrade_strategy":
			hasRequestedStrategy = true
		case "previous_upgrade_strategy":
			hasPreviousStrategy = true
		case "upgrade_strategy_changed":
			hasStrategyChanged = true
		case "upgrade_strategy_status_idle":
			hasIdleStatusGuard = true
			idleStatusExpression, _ = variableMap["expression"].(string)
		case "upgrade_requests_handled":
			hasHandledRequestsGuard = true
		}
	}

	if !hasRequestedStrategy || !hasPreviousStrategy || !hasStrategyChanged || !hasIdleStatusGuard || !hasHandledRequestsGuard {
		t.Fatalf(
			"expected guarded strategy transition variables, got requested=%v previous=%v changed=%v idle=%v requests=%v",
			hasRequestedStrategy,
			hasPreviousStrategy,
			hasStrategyChanged,
			hasIdleStatusGuard,
			hasHandledRequestsGuard,
		)
	}
	if !strings.Contains(idleStatusExpression, "!has(oldObject.status.operationLock) || oldObject.status.operationLock == null") {
		t.Fatalf("idle strategy transition guard must treat an SSA-cleared operation lock as idle, got %q", idleStatusExpression)
	}

	validations, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "validations")
	if err != nil || !found {
		t.Fatalf("read policy validations: found=%v err=%v", found, err)
	}

	wantMessages := []string{
		"Change only spec.upgrade.strategy first; wait for the operator to accept it before changing version, image, replicas, storage, or restart controls.",
		"spec.upgrade.strategy can change only while the cluster is initialized, healthy, fully ready, at spec.version, and no upgrade, backup/restore lock, restart/resize, BlueGreen phase, failure, pending Green workload, or safe-mode recovery is active.",
		"Finish or clear pending spec.upgrade.requests before changing spec.upgrade.strategy.",
		"Switching to BlueGreen requires spec.upgrade.jwtAuthRole or the default upgrade role created by enabled spec.selfInit.oidc bootstrap.",
	}
	foundMessages := make(map[string]bool, len(wantMessages))
	for _, validation := range validations {
		validationMap, ok := validation.(map[string]any)
		if !ok {
			continue
		}
		message, _ := validationMap["message"].(string)
		for _, wantMessage := range wantMessages {
			if message == wantMessage {
				foundMessages[wantMessage] = true
			}
		}
	}

	for _, wantMessage := range wantMessages {
		if !foundMessages[wantMessage] {
			t.Fatalf("openbao-validate-openbaocluster policy is missing strategy transition guard %q", wantMessage)
		}
	}
}

func TestKustomizeDefault_OpenBaoClusterPolicyProtectsTransitUnseal(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		gvk := u.GroupVersionKind()
		return gvk.Group == testAdmissionRegistrationGroup &&
			gvk.Kind == testKindVAP &&
			strings.HasSuffix(u.GetName(), "openbao-validate-openbaocluster")
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one openbao-validate-openbaocluster policy, got %d", len(objs))
	}

	validations, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "validations")
	if err != nil || !found {
		t.Fatalf("read policy validations: found=%v err=%v", found, err)
	}

	var foundHTTPS bool
	var foundUnsafeURLComponents bool
	var foundSecretAuthorizer bool
	var foundBackupSecretAuthorizer bool
	var foundServiceMonitorSecretAuthorizer bool
	var foundCustomExecutablesAuthorizer bool
	var foundImageTrustRootsAuthorizer bool
	var foundCloudIdentityAuthorizer bool
	var foundNetworkPublicationAuthorizer bool
	var foundServiceAccountUseAuthorizer bool
	var foundImagePullSecretUseAuthorizer bool
	var foundIngressTLSSecretAuthorizer bool
	var foundGatewayUseAuthorizer bool
	var foundPVCUseAuthorizer bool
	var foundStorageClassUseAuthorizer bool
	var foundImageVerificationPullSecretAuthorizer bool
	var foundServiceMonitorTLSReferenceAuthorizer bool
	var foundSystemSecretBlock bool
	for _, validation := range validations {
		validationMap, ok := validation.(map[string]any)
		if !ok {
			continue
		}
		message, _ := validationMap["message"].(string)
		expression, _ := validationMap["expression"].(string)
		switch {
		case message == "Transit unseal address must use HTTPS." &&
			strings.Contains(expression, `object.spec.unseal.transit.address.startsWith("https://")`):
			foundHTTPS = true
		case strings.Contains(message, "must not include userinfo") &&
			strings.Contains(expression, `contains("@")`) &&
			strings.Contains(expression, `169\\.254`) &&
			strings.Contains(expression, `[fe80:`):
			foundUnsafeURLComponents = true
		case strings.Contains(message, "Users configuring unseal credentials") &&
			strings.Contains(expression, `authorizer.group("")`) &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("get")`) &&
			!strings.Contains(expression, `object.spec.unseal.type != "transit"`):
			foundSecretAuthorizer = true
		case strings.Contains(message, "Users configuring backup credentials") &&
			strings.Contains(expression, `authorizer.group("")`) &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("get")`) &&
			strings.Contains(expression, `object.spec.backup.target.credentialsSecretRef`) &&
			strings.Contains(expression, `object.spec.backup.tokenSecretRef`):
			foundBackupSecretAuthorizer = true
		case strings.Contains(message, "Users configuring ServiceMonitor authorization") &&
			strings.Contains(expression, `authorizer.group("")`) &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("get")`) &&
			strings.Contains(expression, `object.spec.observability.metrics.serviceMonitor.authorization.credentialsSecret`):
			foundServiceMonitorSecretAuthorizer = true
		case strings.Contains(message, "CR-selected custom executables") &&
			strings.Contains(expression, `variables.custom_executables_authorized`) &&
			strings.Contains(expression, `object.spec.initContainer.image`) &&
			strings.Contains(expression, `object.spec.backup.image`) &&
			strings.Contains(expression, `object.spec.upgrade.image`) &&
			strings.Contains(expression, `object.spec.upgrade.blueGreen.verification.prePromotionHook`) &&
			strings.Contains(expression, `object.spec.plugins.all`):
			foundCustomExecutablesAuthorizer = true
		case strings.Contains(message, "custom image verification trust roots") &&
			strings.Contains(expression, `variables.has_custom_main_image_trust_roots`) &&
			strings.Contains(expression, `variables.has_custom_operator_image_trust_roots`) &&
			strings.Contains(expression, `variables.image_trust_roots_authorized`):
			foundImageTrustRootsAuthorizer = true
		case strings.Contains(message, "use cloud identities") &&
			strings.Contains(expression, `variables.has_cloud_identity_metadata`) &&
			strings.Contains(expression, `variables.cloud_identities_authorized`):
			foundCloudIdentityAuthorizer = true
		case strings.Contains(message, "publish networking") &&
			strings.Contains(expression, `variables.has_network_publication_controls`) &&
			strings.Contains(expression, `variables.network_publication_authorized`):
			foundNetworkPublicationAuthorizer = true
		case strings.Contains(message, "spec.serviceAccount.name") &&
			strings.Contains(expression, `resource("serviceaccounts")`) &&
			strings.Contains(expression, `check("use")`):
			foundServiceAccountUseAuthorizer = true
		case strings.Contains(message, "spec.imagePullSecrets") &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("use")`) &&
			strings.Contains(expression, `check("get")`):
			foundImagePullSecretUseAuthorizer = true
		case strings.Contains(message, "spec.ingress.tlsSecretName") &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("use")`) &&
			strings.Contains(expression, `check("get")`):
			foundIngressTLSSecretAuthorizer = true
		case strings.Contains(message, "spec.gateway.gatewayRef") &&
			strings.Contains(expression, `resource("gateways")`) &&
			strings.Contains(expression, `check("use")`):
			foundGatewayUseAuthorizer = true
		case strings.Contains(message, "existing PVC references") &&
			strings.Contains(expression, `resource("persistentvolumeclaims")`) &&
			strings.Contains(expression, `check("use")`):
			foundPVCUseAuthorizer = true
		case strings.Contains(message, "StorageClass references") &&
			strings.Contains(expression, `resource("storageclasses")`) &&
			strings.Contains(expression, `check("use")`):
			foundStorageClassUseAuthorizer = true
		case strings.Contains(message, "image verification pull Secrets") &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("get")`):
			foundImageVerificationPullSecretAuthorizer = true
		case strings.Contains(message, "ServiceMonitor TLS references") &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `resource("configmaps")`) &&
			strings.Contains(expression, `check("use")`) &&
			strings.Contains(expression, `check("get")`):
			foundServiceMonitorTLSReferenceAuthorizer = true
		case strings.Contains(message, "system secrets") &&
			strings.Contains(expression, "object.spec.unseal.credentialsSecretRef") &&
			strings.Contains(expression, "object.spec.observability.metrics.serviceMonitor.authorization.credentialsSecret") &&
			strings.Contains(expression, "root-token"):
			foundSystemSecretBlock = true
		}
	}

	if !foundHTTPS ||
		!foundUnsafeURLComponents ||
		!foundSecretAuthorizer ||
		!foundBackupSecretAuthorizer ||
		!foundServiceMonitorSecretAuthorizer ||
		!foundCustomExecutablesAuthorizer ||
		!foundImageTrustRootsAuthorizer ||
		!foundCloudIdentityAuthorizer ||
		!foundNetworkPublicationAuthorizer ||
		!foundServiceAccountUseAuthorizer ||
		!foundImagePullSecretUseAuthorizer ||
		!foundIngressTLSSecretAuthorizer ||
		!foundGatewayUseAuthorizer ||
		!foundPVCUseAuthorizer ||
		!foundStorageClassUseAuthorizer ||
		!foundImageVerificationPullSecretAuthorizer ||
		!foundServiceMonitorTLSReferenceAuthorizer ||
		!foundSystemSecretBlock {
		t.Fatalf(
			"openbao-validate-openbaocluster protections missing: https=%v unsafeURL=%v transitAuthorizer=%v backupAuthorizer=%v serviceMonitorAuthorizer=%v executableCodeAuthorizer=%v imageTrustRootsAuthorizer=%v cloudIdentityAuthorizer=%v networkPublicationAuthorizer=%v serviceAccountUseAuthorizer=%v imagePullSecretUseAuthorizer=%v ingressTLSSecretAuthorizer=%v gatewayUseAuthorizer=%v pvcUseAuthorizer=%v storageClassUseAuthorizer=%v imageVerificationPullSecretAuthorizer=%v serviceMonitorTLSReferenceAuthorizer=%v systemSecret=%v",
			foundHTTPS,
			foundUnsafeURLComponents,
			foundSecretAuthorizer,
			foundBackupSecretAuthorizer,
			foundServiceMonitorSecretAuthorizer,
			foundCustomExecutablesAuthorizer,
			foundImageTrustRootsAuthorizer,
			foundCloudIdentityAuthorizer,
			foundNetworkPublicationAuthorizer,
			foundServiceAccountUseAuthorizer,
			foundImagePullSecretUseAuthorizer,
			foundIngressTLSSecretAuthorizer,
			foundGatewayUseAuthorizer,
			foundPVCUseAuthorizer,
			foundStorageClassUseAuthorizer,
			foundImageVerificationPullSecretAuthorizer,
			foundServiceMonitorTLSReferenceAuthorizer,
			foundSystemSecretBlock,
		)
	}

	variables, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "variables")
	if err != nil || !found {
		t.Fatalf("read policy variables: found=%v err=%v", found, err)
	}
	var foundCustomExecutablesVariable bool
	var foundImageTrustRootsVariable bool
	var foundCloudIdentitiesVariable bool
	var foundNetworkPublicationVariable bool
	for _, variable := range variables {
		variableMap, ok := variable.(map[string]any)
		if !ok {
			continue
		}
		name, _ := variableMap["name"].(string)
		expression, _ := variableMap["expression"].(string)
		switch name {
		case "custom_executables_authorized":
			foundCustomExecutablesVariable = strings.Contains(expression, `check("usecustomexecutables")`) &&
				strings.Contains(expression, `check("usehelperimages")`)
		case "image_trust_roots_authorized":
			foundImageTrustRootsVariable = strings.Contains(expression, `check("useimagetrustroots")`)
		case "cloud_identities_authorized":
			foundCloudIdentitiesVariable = strings.Contains(expression, `check("usecloudidentities")`)
		case "network_publication_authorized":
			foundNetworkPublicationVariable = strings.Contains(expression, `check("publishnetworking")`)
		}
	}
	if !foundCustomExecutablesVariable || !foundImageTrustRootsVariable || !foundCloudIdentitiesVariable || !foundNetworkPublicationVariable {
		t.Fatalf(
			"openbao-validate-openbaocluster delegation variables missing: customExecutables=%v trustRoots=%v cloudIdentities=%v networkPublication=%v",
			foundCustomExecutablesVariable,
			foundImageTrustRootsVariable,
			foundCloudIdentitiesVariable,
			foundNetworkPublicationVariable,
		)
	}
}

func TestKustomizeDefault_OpenBaoRestorePolicyProtectsSecretRefs(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		gvk := u.GroupVersionKind()
		return gvk.Group == testAdmissionRegistrationGroup &&
			gvk.Kind == testKindVAP &&
			strings.HasSuffix(u.GetName(), "openbao-validate-openbaorestore")
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one openbao-validate-openbaorestore policy, got %d", len(objs))
	}

	validations, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "validations")
	if err != nil || !found {
		t.Fatalf("read policy validations: found=%v err=%v", found, err)
	}

	var foundRestoreSecretAuthorizer bool
	var foundRestoreHelperImageAuthorizer bool
	var foundRestoreTargetAuthorizer bool
	var foundRestoreCloudIdentityAuthorizer bool
	var foundSystemSecretBlock bool
	for _, validation := range validations {
		validationMap, ok := validation.(map[string]any)
		if !ok {
			continue
		}
		message, _ := validationMap["message"].(string)
		expression, _ := validationMap["expression"].(string)
		switch {
		case strings.Contains(message, "Users configuring restore credentials") &&
			strings.Contains(expression, `authorizer.group("")`) &&
			strings.Contains(expression, `resource("secrets")`) &&
			strings.Contains(expression, `check("get")`) &&
			strings.Contains(expression, `object.spec.source.target.credentialsSecretRef`) &&
			strings.Contains(expression, `object.spec.tokenSecretRef`):
			foundRestoreSecretAuthorizer = true
		case strings.Contains(message, "custom restore helper images") &&
			strings.Contains(expression, `object.spec.image`) &&
			strings.Contains(expression, `variables.custom_executables_authorized`):
			foundRestoreHelperImageAuthorizer = true
		case strings.Contains(message, "must be authorized to restore the target OpenBaoCluster") &&
			strings.Contains(expression, `variables.restore_authorized`):
			foundRestoreTargetAuthorizer = true
		case strings.Contains(message, "restore S3 roleArn or workloadIdentity metadata") &&
			strings.Contains(expression, `variables.has_restore_cloud_identity_metadata`) &&
			strings.Contains(expression, `variables.cloud_identities_authorized`):
			foundRestoreCloudIdentityAuthorizer = true
		case strings.Contains(message, "system secrets") &&
			strings.Contains(expression, "object.spec.source.target.credentialsSecretRef") &&
			strings.Contains(expression, "object.spec.tokenSecretRef") &&
			strings.Contains(expression, "root-token"):
			foundSystemSecretBlock = true
		}
	}

	if !foundRestoreSecretAuthorizer ||
		!foundRestoreHelperImageAuthorizer ||
		!foundRestoreTargetAuthorizer ||
		!foundRestoreCloudIdentityAuthorizer ||
		!foundSystemSecretBlock {
		t.Fatalf(
			"openbao-validate-openbaorestore protections missing: authorizer=%v helperImageAuthorizer=%v restoreTargetAuthorizer=%v restoreCloudIdentityAuthorizer=%v systemSecret=%v",
			foundRestoreSecretAuthorizer,
			foundRestoreHelperImageAuthorizer,
			foundRestoreTargetAuthorizer,
			foundRestoreCloudIdentityAuthorizer,
			foundSystemSecretBlock,
		)
	}

	variables, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "variables")
	if err != nil || !found {
		t.Fatalf("read policy variables: found=%v err=%v", found, err)
	}
	var foundCustomExecutablesVariable bool
	var foundCloudIdentitiesVariable bool
	var foundRestoreVariable bool
	for _, variable := range variables {
		variableMap, ok := variable.(map[string]any)
		if !ok {
			continue
		}
		name, _ := variableMap["name"].(string)
		expression, _ := variableMap["expression"].(string)
		if name == "custom_executables_authorized" {
			foundCustomExecutablesVariable = strings.Contains(expression, `object.spec.cluster`) &&
				strings.Contains(expression, `check("usecustomexecutables")`) &&
				strings.Contains(expression, `check("usehelperimages")`)
		}
		if name == "cloud_identities_authorized" {
			foundCloudIdentitiesVariable = strings.Contains(expression, `object.spec.cluster`) &&
				strings.Contains(expression, `check("usecloudidentities")`)
		}
		if name == "restore_authorized" {
			foundRestoreVariable = strings.Contains(expression, `object.spec.cluster`) &&
				strings.Contains(expression, `check("restore")`)
		}
	}
	if !foundCustomExecutablesVariable || !foundCloudIdentitiesVariable || !foundRestoreVariable {
		t.Fatalf(
			"openbao-validate-openbaorestore delegation variables missing: customExecutables=%v cloudIdentities=%v restore=%v",
			foundCustomExecutablesVariable,
			foundCloudIdentitiesVariable,
			foundRestoreVariable,
		)
	}
}

func TestKustomizeDefault_OpenBaoClusterCRDDoesNotMakeUpgradeStrategyImmutable(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		return u.GetAPIVersion() == "apiextensions.k8s.io/v1" &&
			u.GetKind() == "CustomResourceDefinition" &&
			u.GetName() == "openbaoclusters.openbao.org"
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one openbaoclusters CRD, got %d", len(objs))
	}

	versions, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "versions")
	if err != nil || !found {
		t.Fatalf("read CRD versions: found=%v err=%v", found, err)
	}

	const wantMessage = "spec.upgrade.strategy is immutable after creation; " +
		"switching between RollingUpdate and BlueGreen is not supported."
	for _, version := range versions {
		versionMap, ok := version.(map[string]any)
		if !ok {
			continue
		}
		name, _ := versionMap["name"].(string)
		if name != "v1alpha1" {
			continue
		}

		schemaMap, ok := versionMap["schema"].(map[string]any)
		if !ok {
			t.Fatalf("v1alpha1 version missing schema")
		}
		openAPIV3Schema, ok := schemaMap["openAPIV3Schema"].(map[string]any)
		if !ok {
			t.Fatalf("v1alpha1 version missing openAPIV3Schema")
		}
		properties, ok := openAPIV3Schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("CRD root schema missing properties")
		}
		specSchema, ok := properties["spec"].(map[string]any)
		if !ok {
			t.Fatalf("CRD root schema missing spec property")
		}
		validations, ok := specSchema["x-kubernetes-validations"].([]any)
		if !ok {
			t.Fatal("spec schema missing x-kubernetes-validations")
		}

		for _, validation := range validations {
			validationMap, ok := validation.(map[string]any)
			if !ok {
				continue
			}
			message, _ := validationMap["message"].(string)
			rule, _ := validationMap["rule"].(string)
			if message == wantMessage || strings.Contains(rule, "oldSelf.upgrade.strategy") {
				t.Fatalf("v1alpha1 CRD schema still contains the upgrade strategy immutability rule: %#v", validationMap)
			}
		}

		return
	}

	t.Fatal("v1alpha1 version not found in openbaoclusters CRD")
}

func TestKustomizeDefault_ControllerOpenBaoAudienceMatchesProjection(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		return u.GetAPIVersion() == "apps/v1" &&
			u.GetKind() == "Deployment" &&
			u.GetName() == testControllerSAName
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one controller deployment, got %d", len(objs))
	}

	controller := objs[0]
	envAudience := kustomizeEnvVarValue(t, controller, "OPENBAO_JWT_AUDIENCE")
	projectedAudience := kustomizeProjectedTokenAudience(t, controller, "openbao-token")

	if envAudience != projectedAudience {
		t.Fatalf("controller OPENBAO_JWT_AUDIENCE=%q, projected openbao-token audience=%q", envAudience, projectedAudience)
	}
}

func TestKustomizeDefault_ManagerMetricsResourcesExposeControllerAndProvisioner(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, nil)

	testCases := []struct {
		name           string
		deploymentName string
		serviceName    string
		component      string
	}{
		{
			name:           "controller",
			deploymentName: testControllerSAName,
			serviceName:    "openbao-operator-controller-metrics-service",
			component:      "controller",
		},
		{
			name:           "provisioner",
			deploymentName: "openbao-operator-provisioner",
			serviceName:    "openbao-operator-provisioner-metrics-service",
			component:      testComponentProvisioner,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deployment := mustFindObject(t, objs, "apps/v1", "Deployment", tc.deploymentName)
			assertManagerDeploymentMetricsPort(t, deployment)

			service := mustFindObject(t, objs, "v1", "Service", tc.serviceName)
			assertManagerMetricsService(t, service, tc.component)
		})
	}
}

func TestKustomizeDefault_ProvisionerRoleDoesNotReadServiceAccounts(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		return u.GetAPIVersion() == "rbac.authorization.k8s.io/v1" &&
			u.GetKind() == "ClusterRole" &&
			u.GetName() == "openbao-operator-provisioner-role"
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one provisioner ClusterRole, got %d", len(objs))
	}

	rules, found, err := unstructured.NestedSlice(objs[0].Object, "rules")
	if err != nil || !found {
		t.Fatalf("read provisioner rules: found=%v err=%v", found, err)
	}

	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		resources, _ := ruleMap["resources"].([]any)
		for _, resource := range resources {
			if resource == "serviceaccounts" {
				t.Fatalf("provisioner ClusterRole unexpectedly grants serviceaccounts access: %#v", ruleMap)
			}
		}
	}
}

func TestKustomizeDefault_ProvisionerRoleDeletesOnlyFixedTenantGovernanceResources(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		return u.GetAPIVersion() == "rbac.authorization.k8s.io/v1" &&
			u.GetKind() == "ClusterRole" &&
			u.GetName() == "openbao-operator-provisioner-role"
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one provisioner ClusterRole, got %d", len(objs))
	}
	rules, found, err := unstructured.NestedSlice(objs[0].Object, "rules")
	if err != nil || !found {
		t.Fatalf("read provisioner rules: found=%v err=%v", found, err)
	}

	for _, tc := range []struct {
		resource string
		name     string
	}{
		{resource: "resourcequotas", name: "openbao-operator-tenant-quota"},
		{resource: "limitranges", name: "openbao-operator-tenant-limits"},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			foundScopedDelete := false
			for _, rule := range rules {
				ruleMap, ok := rule.(map[string]any)
				if !ok {
					continue
				}
				resources, _, _ := unstructured.NestedStringSlice(ruleMap, "resources")
				verbs, _, _ := unstructured.NestedStringSlice(ruleMap, "verbs")
				if !containsString(resources, tc.resource) || !containsString(verbs, "delete") {
					continue
				}

				resourceNames, _, _ := unstructured.NestedStringSlice(ruleMap, "resourceNames")
				if len(resourceNames) != 1 || resourceNames[0] != tc.name {
					t.Fatalf("provisioner %s delete must be scoped to %q, got %#v", tc.resource, tc.name, ruleMap)
				}
				for _, wantVerb := range []string{"get", "patch", "delete"} {
					if !containsString(verbs, wantVerb) {
						t.Fatalf("provisioner %s rule missing %q: %#v", tc.resource, wantVerb, ruleMap)
					}
				}
				for _, forbiddenVerb := range []string{"create", "list", "update", "watch"} {
					if containsString(verbs, forbiddenVerb) {
						t.Fatalf("provisioner %s scoped rule must not grant %q: %#v", tc.resource, forbiddenVerb, ruleMap)
					}
				}
				foundScopedDelete = true
			}
			if !foundScopedDelete {
				t.Fatalf("provisioner ClusterRole missing resourceNames-scoped delete for %s", tc.resource)
			}
		})
	}
}

func TestKustomizeDefault_ControllerRoleReadsGatewayAPIByGetOnly(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "default"))
	objs := parseYAMLToUnstructured(t, yamlBytes, func(u *unstructured.Unstructured) bool {
		return u.GetAPIVersion() == "rbac.authorization.k8s.io/v1" &&
			u.GetKind() == "ClusterRole" &&
			u.GetName() == "openbao-operator-controller-openbaocluster-role"
	})

	if len(objs) != 1 {
		t.Fatalf("expected exactly one controller ClusterRole, got %d", len(objs))
	}

	rules, found, err := unstructured.NestedSlice(objs[0].Object, "rules")
	if err != nil || !found {
		t.Fatalf("read controller rules: found=%v err=%v", found, err)
	}

	foundGatewayRule := false
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}

		resources, _ := ruleMap["resources"].([]any)
		if len(resources) != 2 {
			continue
		}

		hasGateways := false
		hasGatewayClasses := false
		for _, resource := range resources {
			switch resource {
			case "gateways":
				hasGateways = true
			case "gatewayclasses":
				hasGatewayClasses = true
			}
		}
		if !hasGateways || !hasGatewayClasses {
			continue
		}

		verbs, _ := ruleMap["verbs"].([]any)
		if len(verbs) != 1 || verbs[0] != "get" {
			t.Fatalf("controller ClusterRole must grant Gateway API reads via get only, got %#v", ruleMap)
		}

		foundGatewayRule = true
	}

	if !foundGatewayRule {
		t.Fatal("expected controller ClusterRole to include get-only access to gateways and gatewayclasses")
	}
}

func TestKustomizeSingleTenantOverlay_BakesInNamespaceScopeAndRemovesProvisioner(t *testing.T) {
	yamlBytes := kustomizeBuild(t, filepath.Join("..", "..", "config", "overlays", "single-tenant"))
	objs := parseYAMLToUnstructured(t, yamlBytes, nil)

	var controller *unstructured.Unstructured
	var singleTenantRole *unstructured.Unstructured
	var singleTenantBinding *unstructured.Unstructured
	var hasOperatorNamespace bool

	for _, obj := range objs {
		failIfProvisionerObject(t, obj)

		switch obj.GetKind() {
		case "Namespace":
			if obj.GetName() == testDefaultOperatorNS {
				hasOperatorNamespace = true
			}
		case "Deployment":
			if obj.GetName() == testControllerSAName {
				controller = obj
			}
		case "ClusterRole":
			if obj.GetName() == "openbao-operator-single-tenant" {
				singleTenantRole = obj
			}
		case "RoleBinding":
			if obj.GetName() == "openbao-operator-single-tenant" {
				singleTenantBinding = obj
			}
		}
	}

	if !hasOperatorNamespace {
		t.Fatal("single-tenant overlay did not include operator namespace")
	}
	if controller == nil {
		t.Fatal("single-tenant overlay missing controller deployment")
	}
	if singleTenantRole == nil {
		t.Fatal("single-tenant overlay missing controller ClusterRole")
	}
	if singleTenantBinding == nil {
		t.Fatal("single-tenant overlay missing target namespace rolebinding")
	}
	if singleTenantBinding.GetNamespace() != testSingleTenantTargetNS {
		t.Fatalf(
			"single-tenant rolebinding namespace = %q, want %q",
			singleTenantBinding.GetNamespace(),
			testSingleTenantTargetNS,
		)
	}

	if got := kustomizeEnvVarValue(t, controller, "WATCH_NAMESPACE"); got != testSingleTenantTargetNS {
		t.Fatalf("WATCH_NAMESPACE = %q, want %q", got, testSingleTenantTargetNS)
	}
	assertClusterRoleHasResourceRule(t, singleTenantRole, "cilium.io", "ciliumnetworkpolicies", []string{"create", "delete", "get", "patch"})
	assertClusterRoleHasResourceRule(
		t,
		singleTenantRole,
		"monitoring.coreos.com",
		"servicemonitors",
		[]string{"create", "delete", "get", "patch"},
	)
	assertClusterRoleHasResourceRule(
		t,
		singleTenantRole,
		"openbao.org",
		"openbaoclusters",
		[]string{"publishnetworking", "restore", "usecloudidentities", "usecustomexecutables", "useimagetrustroots"},
	)

	subjects, found, err := unstructured.NestedSlice(singleTenantBinding.Object, "subjects")
	if err != nil || !found || len(subjects) != 1 {
		t.Fatalf("read rolebinding subjects: found=%v len=%d err=%v", found, len(subjects), err)
	}
	subject, ok := subjects[0].(map[string]any)
	if !ok {
		t.Fatalf("rolebinding subject has unexpected type %T", subjects[0])
	}
	if got, _ := subject["name"].(string); got != testControllerSAName {
		t.Fatalf("rolebinding subject name = %q, want %q", got, testControllerSAName)
	}
	if got, _ := subject["namespace"].(string); got != testDefaultOperatorNS {
		t.Fatalf("rolebinding subject namespace = %q, want %q", got, testDefaultOperatorNS)
	}
}

func assertClusterRoleHasResourceRule(
	t *testing.T,
	role *unstructured.Unstructured,
	apiGroup string,
	resource string,
	verbs []string,
) {
	t.Helper()

	rules, found, err := unstructured.NestedSlice(role.Object, "rules")
	if err != nil || !found {
		t.Fatalf("read %s rules: found=%v err=%v", role.GetName(), found, err)
	}

	foundResource := false
	var seenVerbs []any
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		apiGroups, _ := ruleMap["apiGroups"].([]any)
		resources, _ := ruleMap["resources"].([]any)
		ruleVerbs, _ := ruleMap["verbs"].([]any)
		if !containsAny(apiGroups, apiGroup) || !containsAny(resources, resource) {
			continue
		}
		foundResource = true
		seenVerbs = ruleVerbs
		if len(ruleVerbs) != len(verbs) {
			continue
		}
		missingVerb := false
		for _, verb := range verbs {
			if !containsAny(ruleVerbs, verb) {
				missingVerb = true
				break
			}
		}
		if missingVerb {
			continue
		}
		return
	}

	if foundResource {
		t.Fatalf("%s rule for %s/%s verbs = %#v, want exactly %#v", role.GetName(), apiGroup, resource, seenVerbs, verbs)
	}
	t.Fatalf("%s missing rule for %s/%s", role.GetName(), apiGroup, resource)
}

func failIfProvisionerObject(t *testing.T, obj *unstructured.Unstructured) {
	t.Helper()

	if obj.GetLabels()["app.kubernetes.io/component"] != testComponentProvisioner {
		return
	}
	t.Fatalf("unexpected provisioner %s in single-tenant overlay: %s", strings.ToLower(obj.GetKind()), obj.GetName())
}

func kustomizeEnvVarValue(t *testing.T, obj *unstructured.Unstructured, name string) string {
	t.Helper()

	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("containers not found: %v", err)
	}

	for _, container := range containers {
		containerMap, ok := container.(map[string]any)
		if !ok || containerMap["name"] != "manager" {
			continue
		}
		envs, ok := containerMap["env"].([]any)
		if !ok {
			t.Fatalf("manager env not found")
		}
		for _, env := range envs {
			envMap, ok := env.(map[string]any)
			if !ok {
				continue
			}
			if envMap["name"] == name {
				value, ok := envMap["value"].(string)
				if !ok {
					t.Fatalf("env %s has no string value", name)
				}
				return value
			}
		}
	}

	t.Fatalf("env %s not found", name)
	return ""
}

func assertManagerDeploymentMetricsPort(t *testing.T, obj *unstructured.Unstructured) {
	t.Helper()

	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		t.Fatalf("deployment %s containers not found: found=%v err=%v", obj.GetName(), found, err)
	}

	manager, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("deployment %s container has unexpected type %T", obj.GetName(), containers[0])
	}
	args, _ := manager["args"].([]any)
	if !containsAny(args, "--metrics-bind-address=:8443") {
		t.Fatalf("deployment %s missing metrics bind-address arg: %#v", obj.GetName(), args)
	}

	ports, _ := manager["ports"].([]any)
	for _, port := range ports {
		portMap, ok := port.(map[string]any)
		if !ok {
			continue
		}
		if portMap["name"] == "https" && numericYAMLValue(portMap["containerPort"]) == 8443 {
			return
		}
	}
	t.Fatalf("deployment %s missing https container port 8443: %#v", obj.GetName(), ports)
}

func assertManagerMetricsService(t *testing.T, obj *unstructured.Unstructured, component string) {
	t.Helper()

	selector, found, err := unstructured.NestedStringMap(obj.Object, "spec", "selector")
	if err != nil || !found {
		t.Fatalf("service %s selector not found: found=%v err=%v", obj.GetName(), found, err)
	}
	if selector["app.kubernetes.io/component"] != component {
		t.Fatalf("service %s component selector = %#v, want %q", obj.GetName(), selector, component)
	}

	ports, found, err := unstructured.NestedSlice(obj.Object, "spec", "ports")
	if err != nil || !found || len(ports) != 1 {
		t.Fatalf("service %s ports = %#v found=%v err=%v", obj.GetName(), ports, found, err)
	}
	port, ok := ports[0].(map[string]any)
	if !ok {
		t.Fatalf("service %s port has unexpected type %T", obj.GetName(), ports[0])
	}
	if port["name"] != "https" ||
		numericYAMLValue(port["port"]) != 8443 ||
		numericYAMLValue(port["targetPort"]) != 8443 {
		t.Fatalf("service %s metrics port = %#v, want https:8443", obj.GetName(), port)
	}
}

func numericYAMLValue(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func containsAny(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func kustomizeProjectedTokenAudience(t *testing.T, obj *unstructured.Unstructured, volumeName string) string {
	t.Helper()

	volumes, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "volumes")
	if err != nil || !found {
		t.Fatalf("volumes not found: %v", err)
	}

	for _, volume := range volumes {
		volumeMap, ok := volume.(map[string]any)
		if !ok || volumeMap["name"] != volumeName {
			continue
		}
		projected, ok := volumeMap["projected"].(map[string]any)
		if !ok {
			t.Fatalf("volume %s is not projected", volumeName)
		}
		sources, ok := projected["sources"].([]any)
		if !ok || len(sources) == 0 {
			t.Fatalf("volume %s has no projected sources", volumeName)
		}
		first, ok := sources[0].(map[string]any)
		if !ok {
			t.Fatalf("volume %s source has unexpected type %T", volumeName, sources[0])
		}
		token, ok := first["serviceAccountToken"].(map[string]any)
		if !ok {
			t.Fatalf("volume %s first source is not a serviceAccountToken", volumeName)
		}
		audience, ok := token["audience"].(string)
		if !ok {
			t.Fatalf("volume %s serviceAccountToken.audience missing", volumeName)
		}
		return audience
	}

	t.Fatalf("volume %s not found", volumeName)
	return ""
}

func isClusterScopedManifestObject(gvk schema.GroupVersionKind) bool {
	if gvk.Group == testRBACGroup && (gvk.Kind == testKindClusterRole || gvk.Kind == testKindClusterRoleBinding) {
		return true
	}
	if gvk.Group == testAdmissionRegistrationGroup &&
		(gvk.Kind == testKindVAP || gvk.Kind == testKindVAPBinding) {
		return true
	}
	if gvk.Group == "apiextensions.k8s.io" && gvk.Kind == "CustomResourceDefinition" {
		return true
	}
	return false
}
