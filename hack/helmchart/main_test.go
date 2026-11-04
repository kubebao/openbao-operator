package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	syaml "sigs.k8s.io/yaml"
)

func TestTransformPolicyToHelm_RewritesProvisionerServiceAccountVariable(t *testing.T) {
	input := `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: openbao-restrict-provisioner-rbac
spec:
  variables:
    - name: provisioner_serviceaccount_name
      expression: >-
        'openbao-operator-provisioner'
`

	got := transformPolicyToHelm(input)
	want := `'{{ include "openbao-operator.provisionerServiceAccountName" . }}'`
	if !strings.Contains(got, want) {
		t.Fatalf("transformed policy missing provisioner helper expression %q:\n%s", want, got)
	}
	if strings.Contains(got, `'openbao-operator-provisioner'`) {
		t.Fatalf("transformed policy still contains stale provisioner ServiceAccount name:\n%s", got)
	}
}

func TestTransformRBACToHelm_DeduplicatesCommonLabels(t *testing.T) {
	input := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: openbao-operator-controller
  labels:
    app.kubernetes.io/name: openbao-operator
    app.kubernetes.io/component: controller
    app.kubernetes.io/managed-by: kustomize
rules:
  - apiGroups:
      - kubebao.org
    resources:
      - openbaoclusters
    verbs:
      - get
`

	got := transformRBACToHelm(input, "controller", false)
	if !strings.Contains(got, `{{- include "openbao-operator.labels" . | nindent 4 }}`) {
		t.Fatalf("transformed RBAC missing Helm labels helper:\n%s", got)
	}
	if strings.Contains(got, "app.kubernetes.io/name: openbao-operator") {
		t.Fatalf("transformed RBAC still contains duplicate app name label:\n%s", got)
	}
	if strings.Contains(got, "app.kubernetes.io/managed-by: kustomize") {
		t.Fatalf("transformed RBAC still contains duplicate managed-by label:\n%s", got)
	}
	if !strings.Contains(got, "app.kubernetes.io/component: controller") {
		t.Fatalf("transformed RBAC dropped component label:\n%s", got)
	}
}

func TestSyncAggregatedRBAC_IncludesDangerousControlDelegationRoles(t *testing.T) {
	inputDir := t.TempDir()
	outputDir := t.TempDir()

	writeRole := func(filename, name string, verbs ...string) {
		t.Helper()

		var builder strings.Builder
		builder.WriteString("apiVersion: rbac.authorization.k8s.io/v1\n")
		builder.WriteString("kind: ClusterRole\n")
		builder.WriteString("metadata:\n")
		builder.WriteString("  name: " + name + "\n")
		builder.WriteString("rules:\n")
		builder.WriteString("  - apiGroups:\n")
		builder.WriteString("      - kubebao.org\n")
		builder.WriteString("    resources:\n")
		builder.WriteString("      - openbaoclusters\n")
		builder.WriteString("    verbs:\n")
		for _, verb := range verbs {
			builder.WriteString("      - " + verb + "\n")
		}

		if err := os.WriteFile(filepath.Join(inputDir, filename), []byte(builder.String()), 0o600); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
	}

	writeRole("openbaocluster_admin_role.yaml", "openbaocluster-admin-role", "*")
	writeRole("openbaocluster_editor_role.yaml", "openbaocluster-editor-role", "create", "update")
	writeRole(
		"openbaocluster_cloud_identity_role.yaml",
		"openbaocluster-cloud-identity-role",
		"get",
		"usecloudidentities",
	)
	writeRole(
		"openbaocluster_helper_image_role.yaml",
		"openbaocluster-helper-image-role",
		"get",
		"usecustomexecutables",
		"usehelperimages",
	)
	writeRole(
		"openbaocluster_image_trust_roots_role.yaml",
		"openbaocluster-image-trust-roots-role",
		"get",
		"useimagetrustroots",
	)
	writeRole(
		"openbaocluster_network_publication_role.yaml",
		"openbaocluster-network-publication-role",
		"get",
		"publishnetworking",
	)
	writeRole(
		"openbaocluster_restore_role.yaml",
		"openbaocluster-restore-role",
		"get",
		"restore",
	)
	writeRole("openbaocluster_viewer_role.yaml", "openbaocluster-viewer-role", "get", "list")
	writeRole("openbaotenant_editor_role.yaml", "openbaotenant-editor-role", "create", "update")

	if err := syncAggregatedRBAC(options{rbacInputDir: inputDir, rbacOutputDir: outputDir}); err != nil {
		t.Fatalf("syncAggregatedRBAC() failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outputDir, "aggregated-clusterroles.yaml"))
	if err != nil {
		t.Fatalf("read generated aggregated roles: %v", err)
	}
	output := string(got)
	for _, want := range []string{
		`{{ include "openbao-operator.fullname" . }}-openbaocluster-helper-image`,
		`{{ include "openbao-operator.fullname" . }}-openbaocluster-image-trust-roots`,
		`{{ include "openbao-operator.fullname" . }}-openbaocluster-cloud-identity`,
		`{{ include "openbao-operator.fullname" . }}-openbaocluster-network-publication`,
		"usecustomexecutables",
		`{{ include "openbao-operator.fullname" . }}-openbaocluster-restore`,
		"usehelperimages",
		"useimagetrustroots",
		"usecloudidentities",
		"publishnetworking",
		"restore",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("generated aggregated RBAC missing %q:\n%s", want, output)
		}
	}
}

func TestAddNamespacePodSecurityLabelRBACMode_ConditionsNamespaceMutationVerbs(t *testing.T) {
	input := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: openbao-operator-provisioner
rules:
  - apiGroups:
      - ""
    resources:
      - namespaces
    verbs:
      - get
      - update
      - patch
`

	got, err := addNamespacePodSecurityLabelRBACMode(input)
	if err != nil {
		t.Fatalf("addNamespacePodSecurityLabelRBACMode() failed: %v", err)
	}
	if !strings.Contains(got, `{{ if eq .Values.tenancy.namespacePodSecurityLabels.mode "enforce" }}`) {
		t.Fatalf("transformed RBAC missing namespace Pod Security label mode conditional:\n%s", got)
	}
	if !strings.Contains(got, "      - get\n{{ if") {
		t.Fatalf("transformed RBAC should leave get outside the conditional:\n%s", got)
	}
}

func TestAddNamespacePodSecurityLabelPolicyMode_AddsExternalDenyRule(t *testing.T) {
	input := `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: openbao-restrict-provisioner-namespace-mutations
spec:
  validations:
    # Rule 1: Only enforce Pod Security Standards labels (restricted), and do not change anything else.
    - expression: >-
        !variables.is_provisioner
      message: "The Provisioner may only enforce Pod Security Standards labels (restricted) on Namespaces."
`

	got, err := addNamespacePodSecurityLabelPolicyMode(input)
	if err != nil {
		t.Fatalf("addNamespacePodSecurityLabelPolicyMode() failed: %v", err)
	}
	for _, want := range []string{
		`{{ if eq .Values.tenancy.namespacePodSecurityLabels.mode "external" }}`,
		"The Provisioner may not mutate Namespaces when tenant namespace Pod Security labels are externally managed.",
		`{{ else }}`,
		"The Provisioner may only enforce Pod Security Standards labels (restricted) on Namespaces.",
		`{{ end }}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transformed policy missing %q:\n%s", want, got)
		}
	}
}

func TestHelmTemplateRendersNetworkPublicationAuthority(t *testing.T) {
	rendered := string(renderChart(t, "--set", "admissionPolicies.enabled=true"))
	for _, want := range []string{
		"network_publication_authorized",
		"has_network_publication_controls",
		`check("publishnetworking")`,
		"openbaocluster-network-publication",
		"publishnetworking",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered multi-tenant chart missing %q", want)
		}
	}

	singleTenant := string(renderChart(
		t,
		"--set",
		"admissionPolicies.enabled=true",
		"--set",
		"tenancy.mode=single",
		"--set",
		"tenancy.targetNamespace=openbao-system",
	))
	if !strings.Contains(singleTenant, "publishnetworking") {
		t.Fatalf("rendered single-tenant chart missing publishnetworking")
	}
}

func TestHelmTemplateWiresControllerTenancyMode(t *testing.T) {
	for _, tt := range []struct {
		name               string
		args               []string
		wantWatchNamespace string
	}{
		{name: "multi-tenant"},
		{
			name:               "single-tenant-target-namespace",
			args:               []string{"--set", "tenancy.mode=single", "--set", "tenancy.targetNamespace=tenant-openbao"},
			wantWatchNamespace: "tenant-openbao",
		},
		{
			name:               "single-tenant-release-namespace-default",
			args:               []string{"--set", "tenancy.mode=single"},
			wantWatchNamespace: "openbao",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered := string(renderChart(t, tt.args...))
			watchNamespace := "- name: WATCH_NAMESPACE\n              value: "

			if tt.wantWatchNamespace == "" {
				if strings.Contains(rendered, watchNamespace) {
					t.Fatal("rendered multi-tenant chart unexpectedly sets WATCH_NAMESPACE")
				}
				return
			}

			if !strings.Contains(rendered, watchNamespace+`"`+tt.wantWatchNamespace+`"`) {
				t.Fatalf("rendered single-tenant chart does not set WATCH_NAMESPACE=%q", tt.wantWatchNamespace)
			}
		})
	}
}

func TestHelmTemplateAllowsOperatorMetricsIngress(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		kind string
	}{
		{
			name: "service-monitor",
			args: []string{"--set", "metrics.serviceMonitor.enabled=true"},
			kind: "ServiceMonitor",
		},
		{
			name: "victoria-metrics",
			args: []string{"--set", "metrics.victoriaMetrics.enabled=true"},
			kind: "VMServiceScrape",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := make([]string, 0, 6+len(tt.args))
			args = append(args,
				"--set", "tenancy.mode=multi",
				"--set", "metrics.port=9443",
				"--set", "networkPolicy.metricsAllowedNamespaceLabels.metrics=monitoring",
			)
			args = append(args, tt.args...)
			rendered := string(renderChart(t, args...))

			assertMetricsNetworkPolicy(t, rendered, "test-openbao-operator-allow-metrics", "controller")
			assertMetricsNetworkPolicy(t, rendered, "test-openbao-operator-allow-provisioner-metrics", "provisioner")
			if !hasRenderedObject(rendered, tt.kind, "test-openbao-operator-provisioner-metrics") {
				t.Fatalf("rendered multi-tenant chart missing provisioner %s", tt.kind)
			}
		})
	}
}

func TestHelmTemplateOmitsProvisionerMetricsInSingleTenantMode(t *testing.T) {
	rendered := string(renderChart(
		t,
		"--set", "tenancy.mode=single",
		"--set", "metrics.port=9443",
		"--set", "networkPolicy.metricsAllowedNamespaceLabels.metrics=monitoring",
		"--set", "metrics.serviceMonitor.enabled=true",
		"--set", "metrics.victoriaMetrics.enabled=true",
	))

	assertMetricsNetworkPolicy(t, rendered, "test-openbao-operator-allow-metrics", "controller")
	if strings.Contains(rendered, "test-openbao-operator-allow-provisioner-metrics") ||
		strings.Contains(rendered, "test-openbao-operator-provisioner-metrics") {
		t.Fatal("rendered single-tenant chart unexpectedly contains provisioner metrics resources")
	}
}

func TestHelmTemplateProvisionerMemoryDefaults(t *testing.T) {
	rendered := string(renderChart(t))
	manifest, ok := findRenderedObject(rendered, "Deployment", "test-openbao-operator-provisioner")
	if !ok {
		t.Fatal("rendered chart missing provisioner Deployment")
	}

	want := `          resources:
            limits:
              cpu: 100m
              memory: 128Mi
            requests:
              cpu: 10m
              memory: 64Mi`
	if !strings.Contains(manifest, want) {
		t.Fatalf("provisioner Deployment has unexpected default resources:\n%s", manifest)
	}
}

func assertMetricsNetworkPolicy(t *testing.T, rendered, name, component string) {
	t.Helper()

	manifest, ok := findRenderedObject(rendered, "NetworkPolicy", name)
	if !ok {
		t.Fatalf("rendered chart missing NetworkPolicy %q", name)
	}
	for _, want := range []string{
		"app.kubernetes.io/name: openbao-operator\n      app.kubernetes.io/component: " + component,
		"metrics: monitoring",
		"- port: 9443\n          protocol: TCP",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("NetworkPolicy %q missing %q:\n%s", name, want, manifest)
		}
	}
}

func hasRenderedObject(rendered, kind, name string) bool {
	_, ok := findRenderedObject(rendered, kind, name)
	return ok
}

func findRenderedObject(rendered, kind, name string) (string, bool) {
	for _, document := range strings.Split(rendered, "\n---") {
		if strings.Contains(document, "\nkind: "+kind+"\n") &&
			strings.Contains(document, "\n  name: "+name+"\n") {
			return document, true
		}
	}
	return "", false
}

func TestHelmTemplateRendersStrictYAML(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "default"},
		{name: "multi", args: []string{"--set", "tenancy.mode=multi"}},
		{
			name: "external-namespace-pod-security-labels",
			args: []string{"--set", "tenancy.namespacePodSecurityLabels.mode=external"},
		},
		{name: "single", args: []string{"--set", "tenancy.mode=single", "--set", "tenancy.targetNamespace=openbao-system"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered := renderChart(t, tt.args...)
			assertStrictYAML(t, rendered)
		})
	}
}

func renderChart(t *testing.T, extraArgs ...string) []byte {
	t.Helper()

	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not available")
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file path")
	}
	chartDir := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "charts", "openbao-operator"))
	args := make([]string, 0, 6+len(extraArgs))
	args = append(args,
		"template",
		"test",
		chartDir,
		"--namespace",
		"openbao",
		"--include-crds",
	)
	args = append(args, extraArgs...)
	cmd := exec.Command("helm", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, string(output))
	}
	return output
}

func assertStrictYAML(t *testing.T, rendered []byte) {
	t.Helper()

	for i, doc := range bytes.Split(rendered, []byte("\n---")) {
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}
		var out map[string]interface{}
		if err := syaml.UnmarshalStrict(doc, &out); err != nil {
			t.Fatalf("rendered document %d failed strict YAML decoding: %v\n%s", i+1, err, string(doc))
		}
	}
}

func TestRestorePolicyBindingUsesReleaseName(t *testing.T) {
	got := transformPolicyToHelm("policyName: openbao-protect-restore-execution")
	want := `policyName: {{ include "openbao-operator.fullname" . }}-openbao-protect-restore-execution`
	if got != want {
		t.Fatalf("restore binding = %q, want %q", got, want)
	}
}
