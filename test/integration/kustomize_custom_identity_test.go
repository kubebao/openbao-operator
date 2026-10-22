//go:build integration
// +build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestKustomizeCustomIdentityOverlay_RewritesOperatorIdentityFields(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp(filepath.Join("..", "..", "config", "overlays"), ".tmp-kustomize-custom-identity-")
	if err != nil {
		t.Fatalf("create temp overlay dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	overlayPath := filepath.Join("..", "..", "config", "overlays", "custom-identity", "kustomization.yaml")
	kustomizationBytes, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatalf("read custom-identity overlay: %v", err)
	}
	kustomization := strings.Replace(
		string(kustomizationBytes),
		"namespace: openbao-operator-system",
		"namespace: custom-operator\nnamePrefix: demo-",
		1,
	)
	if err := os.WriteFile(filepath.Join(tmpDir, "kustomization.yaml"), []byte(kustomization), 0o600); err != nil {
		t.Fatalf("write temp kustomization: %v", err)
	}

	yamlBytes := kustomizeBuild(t, tmpDir)
	objs := parseYAMLToUnstructured(t, yamlBytes, nil)
	for _, object := range objs {
		if object.GetAPIVersion() == "admissionregistration.k8s.io/v1" && object.GetNamespace() != "" {
			t.Fatalf("admission object %s/%s must be cluster-scoped", object.GetKind(), object.GetName())
		}
	}

	tokenRole := mustFindObject(t, objs, "rbac.authorization.k8s.io/v1", "Role", "demo-openbao-operator-controller-token")
	rules, found, err := unstructured.NestedSlice(tokenRole.Object, "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("controller token role must contain one rule: %v", err)
	}
	names, found, err := unstructured.NestedStringSlice(rules[0].(map[string]interface{}), "resourceNames")
	if err != nil || !found || len(names) != 1 || names[0] != testPrefixedControllerSA {
		t.Fatalf("controller token role resourceNames=%v, want only %s: %v", names, testPrefixedControllerSA, err)
	}

	controller := mustFindObject(t, objs, "apps/v1", "Deployment", testPrefixedControllerSA)
	provisioner := mustFindObject(t, objs, "apps/v1", "Deployment", testPrefixedProvisionerSA)
	controllerPolicy := mustFindPolicy(t, objs, "demo-openbao-operator-openbao-restrict-controller-rbac")
	for _, name := range []string{"openbao-protect-restore-execution", "openbao-validate-openbaorestore"} {
		policy := mustFindPolicy(t, objs, "demo-openbao-operator-"+name)
		if got := policyVariableExpression(t, policy, "operator_namespace"); got != testQuotedCustomOperatorNS {
			t.Fatalf("%s namespace = %q", name, got)
		}
		if got := policyVariableExpression(t, policy, "controller_serviceaccount_name"); got != testQuotedPrefixedCtrlSA {
			t.Fatalf("%s controller = %q", name, got)
		}
	}

	controllerServiceAccountPolicy := mustFindPolicy(
		t,
		objs,
		"demo-openbao-operator-openbao-restrict-controller-serviceaccounts",
	)
	controllerSecretPolicy := mustFindPolicy(
		t,
		objs,
		"demo-openbao-operator-openbao-restrict-controller-secret-writes",
	)
	lockManagedPolicy := mustFindPolicy(
		t,
		objs,
		"demo-openbao-operator-openbao-lock-managed-resource-mutations",
	)
	provisionerPolicy := mustFindPolicy(t, objs, "demo-openbao-operator-openbao-restrict-provisioner-rbac")
	tenantGovernancePolicy := mustFindPolicy(
		t,
		objs,
		"demo-openbao-operator-openbao-restrict-provisioner-tenant-governance",
	)
	tenantPolicy := mustFindPolicy(t, objs, "demo-openbao-operator-openbao-validate-openbao-tenant")
	openBaoClusterPolicy := mustFindPolicy(t, objs, "demo-openbao-operator-openbao-validate-openbaocluster")

	if got := envVarValue(t, controller, "OPERATOR_SERVICE_ACCOUNT_NAME"); got != testPrefixedControllerSA {
		t.Fatalf("controller OPERATOR_SERVICE_ACCOUNT_NAME=%q, want %q", got, testPrefixedControllerSA)
	}
	if got := envVarValue(t, provisioner, "OPERATOR_SERVICE_ACCOUNT_NAME"); got != testPrefixedControllerSA {
		t.Fatalf("provisioner OPERATOR_SERVICE_ACCOUNT_NAME=%q, want %q", got, testPrefixedControllerSA)
	}

	for _, deployment := range []*unstructured.Unstructured{controller, provisioner} {
		if got := envVarValue(t, deployment, "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME"); got != testPrefixedProvisionerSA {
			t.Fatalf("%s OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME=%q, want %q", deployment.GetName(), got, testPrefixedProvisionerSA)
		}
	}

	if got := policyVariableExpression(t, controllerPolicy, "operator_namespace"); got != testQuotedCustomOperatorNS {
		t.Fatalf("controller policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		controllerPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf("controller policy controller_serviceaccount_name=%q, want %q", got, testQuotedPrefixedCtrlSA)
	}
	if got := policyVariableExpression(
		t,
		controllerServiceAccountPolicy,
		"operator_namespace",
	); got != testQuotedCustomOperatorNS {
		t.Fatalf("controller serviceaccount policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		controllerServiceAccountPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf(
			"controller serviceaccount policy controller_serviceaccount_name=%q, want %q",
			got,
			testQuotedPrefixedCtrlSA,
		)
	}
	if got := policyVariableExpression(
		t,
		controllerSecretPolicy,
		"operator_namespace",
	); got != testQuotedCustomOperatorNS {
		t.Fatalf("controller secret policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		controllerSecretPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf("controller secret policy controller_serviceaccount_name=%q, want %q", got, testQuotedPrefixedCtrlSA)
	}
	if got := policyVariableExpression(
		t,
		lockManagedPolicy,
		"provisioner_serviceaccount_name",
	); got != testQuotedPrefixedProvSA {
		t.Fatalf("lock managed policy provisioner_serviceaccount_name=%q, want %q", got, testQuotedPrefixedProvSA)
	}
	if got := policyVariableExpression(
		t,
		provisionerPolicy,
		"provisioner_serviceaccount_name",
	); got != testQuotedPrefixedProvSA {
		t.Fatalf("provisioner policy provisioner_serviceaccount_name=%q, want %q", got, testQuotedPrefixedProvSA)
	}
	if got := policyVariableExpression(
		t,
		tenantGovernancePolicy,
		"operator_namespace",
	); got != testQuotedCustomOperatorNS {
		t.Fatalf("tenant governance policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		tenantGovernancePolicy,
		"provisioner_serviceaccount_name",
	); got != testQuotedPrefixedProvSA {
		t.Fatalf(
			"tenant governance policy provisioner_serviceaccount_name=%q, want %q",
			got,
			testQuotedPrefixedProvSA,
		)
	}
	if got := policyVariableExpression(t, tenantPolicy, "operator_namespace"); got != testQuotedCustomOperatorNS {
		t.Fatalf("tenant policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		openBaoClusterPolicy,
		"operator_namespace",
	); got != testQuotedCustomOperatorNS {
		t.Fatalf("openbaocluster policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		openBaoClusterPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf("openbaocluster policy controller_serviceaccount_name=%q, want %q", got, testQuotedPrefixedCtrlSA)
	}
}

func TestKustomizeSingleTenantOverlay_CustomOperatorAndTargetNamespace(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp(filepath.Join("..", "..", "config", "overlays"), ".tmp-kustomize-single-tenant-")
	if err != nil {
		t.Fatalf("create temp overlay dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	sourceDir := filepath.Join("..", "..", "config", "overlays", "single-tenant")
	for _, name := range []string{
		"kustomization.yaml",
		"operator_namespace.yaml",
		"single_tenant_clusterrole.yaml",
		"target_namespace_config.yaml",
		"target_namespace_rolebinding.yaml",
	} {
		content, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		switch name {
		case "kustomization.yaml":
			content = []byte(strings.Replace(
				string(content),
				"namespace: openbao-operator-system",
				"namespace: custom-operator\nnamePrefix: demo-",
				1,
			))
		case "target_namespace_config.yaml":
			content = []byte(strings.Replace(
				string(content),
				"WATCH_NAMESPACE: openbao",
				"WATCH_NAMESPACE: tenant-openbao",
				1,
			))
		}
		if err := os.WriteFile(filepath.Join(tmpDir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	yamlBytes := kustomizeBuild(t, tmpDir)
	objs := parseYAMLToUnstructured(t, yamlBytes, nil)

	tokenRole := mustFindObject(t, objs, "rbac.authorization.k8s.io/v1", "Role", "demo-openbao-operator-controller-token")
	rules, found, err := unstructured.NestedSlice(tokenRole.Object, "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("controller token role must contain one rule: %v", err)
	}
	names, found, err := unstructured.NestedStringSlice(rules[0].(map[string]interface{}), "resourceNames")
	if err != nil || !found || len(names) != 1 || names[0] != testPrefixedControllerSA {
		t.Fatalf("controller token role resourceNames=%v, want only %s: %v", names, testPrefixedControllerSA, err)
	}

	controller := mustFindObject(t, objs, "apps/v1", "Deployment", testPrefixedControllerSA)
	roleBinding := mustFindRoleBinding(t, objs, "demo-openbao-operator-single-tenant")
	operatorNS := mustFindObject(t, objs, "v1", "Namespace", testCustomOperatorNS)

	if operatorNS == nil {
		t.Fatal("custom operator namespace was not rendered")
	}
	if controller.GetNamespace() != testCustomOperatorNS {
		t.Fatalf("controller namespace=%q, want %q", controller.GetNamespace(), testCustomOperatorNS)
	}
	if got := envVarValue(t, controller, "WATCH_NAMESPACE"); got != testCustomTenantTargetNS {
		t.Fatalf("WATCH_NAMESPACE=%q, want %q", got, testCustomTenantTargetNS)
	}
	if roleBinding.GetNamespace() != testCustomTenantTargetNS {
		t.Fatalf("rolebinding namespace=%q, want %q", roleBinding.GetNamespace(), testCustomTenantTargetNS)
	}

	subjects, found, err := unstructured.NestedSlice(roleBinding.Object, "subjects")
	if err != nil || !found || len(subjects) != 1 {
		t.Fatalf("read rolebinding subjects: found=%v len=%d err=%v", found, len(subjects), err)
	}
	subject, ok := subjects[0].(map[string]any)
	if !ok {
		t.Fatalf("rolebinding subject has unexpected type %T", subjects[0])
	}
	if got, _ := subject["name"].(string); got != testPrefixedControllerSA {
		t.Fatalf("rolebinding subject name=%q, want %q", got, testPrefixedControllerSA)
	}
	if got, _ := subject["namespace"].(string); got != testCustomOperatorNS {
		t.Fatalf("rolebinding subject namespace=%q, want %q", got, testCustomOperatorNS)
	}
}

func TestKustomizeSingleTenantCustomIdentityOverlay_RewritesControllerIdentityAndTargetNamespace(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp(
		filepath.Join("..", "..", "config", "overlays"),
		".tmp-kustomize-single-tenant-custom-identity-",
	)
	if err != nil {
		t.Fatalf("create temp overlay dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	sourceDir := filepath.Join("..", "..", "config", "overlays", "single-tenant-custom-identity")
	for _, name := range []string{
		"kustomization.yaml",
		"operator_namespace.yaml",
		"single_tenant_clusterrole.yaml",
		"target_namespace_config.yaml",
		"target_namespace_rolebinding.yaml",
	} {
		content, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		switch name {
		case "kustomization.yaml":
			content = []byte(strings.Replace(
				string(content),
				"namespace: openbao-operator-system",
				"namespace: custom-operator\nnamePrefix: demo-",
				1,
			))
		case "target_namespace_config.yaml":
			content = []byte(strings.Replace(
				string(content),
				"WATCH_NAMESPACE: openbao",
				"WATCH_NAMESPACE: tenant-openbao",
				1,
			))
		}
		if err := os.WriteFile(filepath.Join(tmpDir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	yamlBytes := kustomizeBuild(t, tmpDir)
	objs := parseYAMLToUnstructured(t, yamlBytes, nil)

	tokenRole := mustFindObject(t, objs, "rbac.authorization.k8s.io/v1", "Role", "demo-openbao-operator-controller-token")
	rules, found, err := unstructured.NestedSlice(tokenRole.Object, "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("controller token role must contain one rule: %v", err)
	}
	names, found, err := unstructured.NestedStringSlice(rules[0].(map[string]interface{}), "resourceNames")
	if err != nil || !found || len(names) != 1 || names[0] != testPrefixedControllerSA {
		t.Fatalf("controller token role resourceNames=%v, want only %s: %v", names, testPrefixedControllerSA, err)
	}

	controller := mustFindObject(t, objs, "apps/v1", "Deployment", testPrefixedControllerSA)
	roleBinding := mustFindRoleBinding(t, objs, "demo-openbao-operator-single-tenant")
	controllerPolicy := mustFindPolicy(t, objs, "demo-openbao-operator-openbao-restrict-controller-rbac")
	for _, name := range []string{"openbao-protect-restore-execution", "openbao-validate-openbaorestore"} {
		policy := mustFindPolicy(t, objs, "demo-openbao-operator-"+name)
		if got := policyVariableExpression(t, policy, "operator_namespace"); got != testQuotedCustomOperatorNS {
			t.Fatalf("%s namespace = %q", name, got)
		}
		if got := policyVariableExpression(t, policy, "controller_serviceaccount_name"); got != testQuotedPrefixedCtrlSA {
			t.Fatalf("%s controller = %q", name, got)
		}
	}

	controllerServiceAccountPolicy := mustFindPolicy(
		t,
		objs,
		"demo-openbao-operator-openbao-restrict-controller-serviceaccounts",
	)
	openBaoClusterPolicy := mustFindPolicy(t, objs, "demo-openbao-operator-openbao-validate-openbaocluster")
	operatorNS := mustFindObject(t, objs, "v1", "Namespace", testCustomOperatorNS)

	if operatorNS == nil {
		t.Fatal("custom operator namespace was not rendered")
	}
	if controller.GetNamespace() != testCustomOperatorNS {
		t.Fatalf("controller namespace=%q, want %q", controller.GetNamespace(), testCustomOperatorNS)
	}
	if got := envVarValue(t, controller, "WATCH_NAMESPACE"); got != testCustomTenantTargetNS {
		t.Fatalf("WATCH_NAMESPACE=%q, want %q", got, testCustomTenantTargetNS)
	}
	if roleBinding.GetNamespace() != testCustomTenantTargetNS {
		t.Fatalf("rolebinding namespace=%q, want %q", roleBinding.GetNamespace(), testCustomTenantTargetNS)
	}
	if got := policyVariableExpression(t, controllerPolicy, "operator_namespace"); got != testQuotedCustomOperatorNS {
		t.Fatalf("controller policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		controllerPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf("controller policy controller_serviceaccount_name=%q, want %q", got, testQuotedPrefixedCtrlSA)
	}
	if got := policyVariableExpression(
		t,
		controllerServiceAccountPolicy,
		"operator_namespace",
	); got != testQuotedCustomOperatorNS {
		t.Fatalf("controller serviceaccount policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		controllerServiceAccountPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf(
			"controller serviceaccount policy controller_serviceaccount_name=%q, want %q",
			got,
			testQuotedPrefixedCtrlSA,
		)
	}
	if got := policyVariableExpression(
		t,
		openBaoClusterPolicy,
		"operator_namespace",
	); got != testQuotedCustomOperatorNS {
		t.Fatalf("openbaocluster policy operator_namespace=%q, want %q", got, testQuotedCustomOperatorNS)
	}
	if got := policyVariableExpression(
		t,
		openBaoClusterPolicy,
		"controller_serviceaccount_name",
	); got != testQuotedPrefixedCtrlSA {
		t.Fatalf("openbaocluster policy controller_serviceaccount_name=%q, want %q", got, testQuotedPrefixedCtrlSA)
	}

	subjects, found, err := unstructured.NestedSlice(roleBinding.Object, "subjects")
	if err != nil || !found || len(subjects) != 1 {
		t.Fatalf("read rolebinding subjects: found=%v len=%d err=%v", found, len(subjects), err)
	}
	subject, ok := subjects[0].(map[string]any)
	if !ok {
		t.Fatalf("rolebinding subject has unexpected type %T", subjects[0])
	}
	if got, _ := subject["name"].(string); got != testPrefixedControllerSA {
		t.Fatalf("rolebinding subject name=%q, want %q", got, testPrefixedControllerSA)
	}
	if got, _ := subject["namespace"].(string); got != testCustomOperatorNS {
		t.Fatalf("rolebinding subject namespace=%q, want %q", got, testCustomOperatorNS)
	}
}

func mustFindPolicy(t *testing.T, objs []*unstructured.Unstructured, name string) *unstructured.Unstructured {
	t.Helper()

	return mustFindObject(t, objs, testAdmissionRegistrationV1, testKindVAP, name)
}

func mustFindRoleBinding(t *testing.T, objs []*unstructured.Unstructured, name string) *unstructured.Unstructured {
	t.Helper()

	return mustFindObject(t, objs, testRBACV1, testKindRoleBinding, name)
}

func mustFindObject(
	t *testing.T,
	objs []*unstructured.Unstructured,
	apiVersion string,
	kind string,
	name string,
) *unstructured.Unstructured {
	t.Helper()

	for _, obj := range objs {
		if obj.GetAPIVersion() == apiVersion && obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}

	t.Fatalf("object %s %s %s not found", apiVersion, kind, name)
	return nil
}

func envVarValue(t *testing.T, obj *unstructured.Unstructured, name string) string {
	t.Helper()

	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("containers not found: %v", err)
	}
	for _, container := range containers {
		containerMap, ok := container.(map[string]any)
		if !ok {
			continue
		}
		if containerMap["name"] != "manager" {
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
				if value, ok := envMap["value"].(string); ok {
					return value
				}
				t.Fatalf("env %s has no string value", name)
			}
		}
	}

	t.Fatalf("env %s not found", name)
	return ""
}

func policyVariableExpression(t *testing.T, obj *unstructured.Unstructured, name string) string {
	t.Helper()

	variables, found, err := unstructured.NestedSlice(obj.Object, "spec", "variables")
	if err != nil || !found {
		t.Fatalf("policy variables not found: %v", err)
	}
	for _, variable := range variables {
		variableMap, ok := variable.(map[string]any)
		if !ok {
			continue
		}
		if variableMap["name"] == name {
			if expression, ok := variableMap["expression"].(string); ok {
				return expression
			}
			t.Fatalf("variable %s has no string expression", name)
		}
	}

	t.Fatalf("policy variable %s not found", name)
	return ""
}
