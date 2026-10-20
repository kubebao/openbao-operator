// Package main provides a tool to sync kustomize manifests to the Helm chart.
// The tool syncs CRDs, ValidatingAdmissionPolicies, and RBAC resources.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	metadataMarker = "metadata:"
	specMarker     = "spec:"
	rulesMarker    = "rules:"
	helmFullname   = `{{ include "openbao-operator.fullname" . }}`
)

var helmManagedLabelKeys = map[string]struct{}{
	"helm.sh/chart":                {},
	"app.kubernetes.io/name":       {},
	"app.kubernetes.io/instance":   {},
	"app.kubernetes.io/managed-by": {},
	"app.kubernetes.io/version":    {},
}

type options struct {
	crdInputDir     string
	crdOutputDir    string
	policyInputDir  string
	policyOutputDir string
	rbacInputDir    string
	rbacOutputDir   string
	helmChartDir    string
}

func main() {
	var opts options
	flag.StringVar(&opts.crdInputDir, "crd-in-dir", "config/crd/bases", "Path to config/crd/bases directory")
	flag.StringVar(&opts.crdOutputDir, "crd-out-dir", "charts/openbao-operator/crds", "Path to Helm chart CRDs directory")
	flag.StringVar(&opts.policyInputDir, "policy-in-dir", "config/policy", "Path to config/policy directory")
	flag.StringVar(&opts.policyOutputDir, "policy-out-dir", "charts/openbao-operator/templates/admission",
		"Path to Helm chart admission templates directory")
	flag.StringVar(&opts.rbacInputDir, "rbac-in-dir", "config/rbac", "Path to config/rbac directory")
	flag.StringVar(&opts.rbacOutputDir, "rbac-out-dir", "charts/openbao-operator/templates/rbac",
		"Path to Helm chart RBAC templates directory")
	flag.StringVar(&opts.helmChartDir, "chart-dir", "charts/openbao-operator", "Path to Helm chart root directory")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if err := syncCRDs(opts); err != nil {
		return fmt.Errorf("sync CRDs: %w", err)
	}
	if err := syncPolicies(opts); err != nil {
		return fmt.Errorf("sync policies: %w", err)
	}
	if err := syncRBAC(opts); err != nil {
		return fmt.Errorf("sync RBAC: %w", err)
	}
	return nil
}

func readFile(path string) (string, error) {
	// #nosec G304 -- path is constructed from a controlled input directory.
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func readLines(path string) ([]string, error) {
	// #nosec G304 -- path is constructed from a controlled input directory and file name.
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func leadingWhitespace(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return s[:i]
		}
	}
	return s
}

func writeFile(path, content string) error {
	// #nosec G306 -- writes non-sensitive YAML intended to be committed to the repo.
	return os.WriteFile(path, []byte(content), 0o644)
}

// syncCRDs syncs CRD YAMLs from config/crd/bases to the Helm chart.
func syncCRDs(opts options) error {
	entries, err := os.ReadDir(opts.crdInputDir)
	if err != nil {
		return fmt.Errorf("read CRD input dir %q: %w", opts.crdInputDir, err)
	}
	if err := os.MkdirAll(opts.crdOutputDir, 0o750); err != nil {
		return fmt.Errorf("create CRD output dir %q: %w", opts.crdOutputDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}

		inPath := filepath.Join(opts.crdInputDir, name)
		lines, err := readLines(inPath)
		if err != nil {
			return fmt.Errorf("read %q: %w", inPath, err)
		}

		lines, err = ensureHelmKeepAnnotation(lines)
		if err != nil {
			return fmt.Errorf("ensure Helm keep annotation for %q: %w", inPath, err)
		}

		outPath := filepath.Join(opts.crdOutputDir, name)
		if err := writeFile(outPath, strings.Join(lines, "\n")+"\n"); err != nil {
			return fmt.Errorf("write %q: %w", outPath, err)
		}
	}

	return nil
}

func ensureHelmKeepAnnotation(lines []string) ([]string, error) {
	for _, line := range lines {
		if strings.TrimSpace(line) == "helm.sh/resource-policy: keep" {
			return lines, nil
		}
	}

	for i, line := range lines {
		if strings.TrimSpace(line) != "annotations:" {
			continue
		}
		indent := leadingWhitespace(line)
		insert := indent + "  helm.sh/resource-policy: keep"
		lines = append(lines[:i+1], append([]string{insert}, lines[i+1:]...)...)
		return lines, nil
	}

	return nil, fmt.Errorf("annotations block not found")
}

// Policy file mapping from kustomize to Helm output names.
var policyFileMapping = map[string]string{
	"openbao-lock-managed-resource-mutations.yaml":                  "lock-managed-resources.yaml",
	"openbao-lock-managed-resource-mutations-binding.yaml":          "lock-managed-resources.yaml", // merged
	"openbao-enforce-managed-image-digests.yaml":                    "enforce-managed-image-digests.yaml",
	"openbao-enforce-managed-image-digests-binding.yaml":            "enforce-managed-image-digests.yaml", // merged
	"openbao-lock-controller-statefulset-mutations.yaml":            "validating-policies.yaml",
	"openbao-lock-controller-statefulset-mutations-binding.yaml":    "validating-policies.yaml", // merged
	"openbao-restrict-provisioner-rbac.yaml":                        "provisioner-rbac.yaml",
	"openbao-restrict-provisioner-rbac-binding.yaml":                "provisioner-rbac.yaml", // merged
	"openbao-restrict-provisioner-tenant-governance.yaml":           "provisioner-tenant-governance.yaml",
	"openbao-restrict-provisioner-tenant-governance-binding.yaml":   "provisioner-tenant-governance.yaml", // merged
	"openbao-validate-openbaocluster.yaml":                          "validate-openbaocluster.yaml",
	"openbao-validate-openbaocluster-binding.yaml":                  "validate-openbaocluster.yaml", // merged
	"openbao-protect-restore-execution.yaml":                        "protect-restore-execution.yaml",
	"openbao-protect-restore-execution-binding.yaml":                "protect-restore-execution.yaml",
	"openbao-validate-openbaorestore.yaml":                          "validate-openbaorestore.yaml",
	"openbao-validate-openbaorestore-binding.yaml":                  "validate-openbaorestore.yaml", // merged
	"openbao-validate-openbao-tenant.yaml":                          "validate-openbao-tenant.yaml",
	"openbao-validate-openbao-tenant-binding.yaml":                  "validate-openbao-tenant.yaml", // merged
	"openbao-restrict-controller-rbac-binding.yaml":                 "controller-rbac.yaml",         // merged
	"openbao-restrict-controller-rbac.yaml":                         "controller-rbac.yaml",
	"openbao-restrict-controller-serviceaccounts.yaml":              "controller-serviceaccounts.yaml",
	"openbao-restrict-controller-serviceaccounts-binding.yaml":      "controller-serviceaccounts.yaml", // merged
	"openbao-restrict-controller-secret-writes.yaml":                "controller-secret-writes.yaml",
	"openbao-restrict-controller-secret-writes-binding.yaml":        "controller-secret-writes.yaml", // merged
	"openbao-restrict-provisioner-namespace-mutations.yaml":         "provisioner-namespace-mutations.yaml",
	"openbao-restrict-provisioner-namespace-mutations-binding.yaml": "provisioner-namespace-mutations.yaml", // merged
}

// syncPolicies syncs ValidatingAdmissionPolicy YAMLs from config/policy to Helm templates.
func syncPolicies(opts options) error {
	entries, err := os.ReadDir(opts.policyInputDir)
	if err != nil {
		return fmt.Errorf("read policy input dir %q: %w", opts.policyInputDir, err)
	}
	if err := os.MkdirAll(opts.policyOutputDir, 0o750); err != nil {
		return fmt.Errorf("create policy output dir %q: %w", opts.policyOutputDir, err)
	}

	// Group files by output file
	outputGroups := make(map[string][]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") || name == "kustomization.yaml" {
			continue
		}
		outName, ok := policyFileMapping[name]
		if !ok {
			return fmt.Errorf("unknown policy file %q (update policyFileMapping in hack/helmchart/main.go)", name)
		}
		outputGroups[outName] = append(outputGroups[outName], name)
	}

	// Sort the output groups so policies come before bindings (binding files have -binding suffix)
	for _, files := range outputGroups {
		sort.Slice(files, func(i, j int) bool {
			// Non-binding files should come first
			iBinding := strings.Contains(files[i], "-binding")
			jBinding := strings.Contains(files[j], "-binding")
			if iBinding != jBinding {
				return !iBinding // non-binding first
			}
			return files[i] < files[j]
		})
	}

	// Process each output group (sorted for stable output).
	outNames := make([]string, 0, len(outputGroups))
	for outName := range outputGroups {
		outNames = append(outNames, outName)
	}
	sort.Strings(outNames)
	for _, outName := range outNames {
		inputFiles := outputGroups[outName]
		var parts []string
		for _, inputFile := range inputFiles {
			inPath := filepath.Join(opts.policyInputDir, inputFile)
			content, err := readFile(inPath)
			if err != nil {
				return fmt.Errorf("read %q: %w", inPath, err)
			}
			transformed := transformPolicyToHelm(content)
			if inputFile == "openbao-restrict-provisioner-namespace-mutations.yaml" {
				transformed, err = addNamespacePodSecurityLabelPolicyMode(transformed)
				if err != nil {
					return fmt.Errorf("add namespace Pod Security label mode to %q: %w", inputFile, err)
				}
			}
			if inputFile == "openbao-lock-managed-resource-mutations.yaml" {
				transformed, err = addPVCBindingSyncCallers(transformed)
				if err != nil {
					return fmt.Errorf("add PVC binding sync callers to %q: %w", inputFile, err)
				}
			}
			parts = append(parts, transformed)
		}

		// Wrap in conditional and join
		output := fmt.Sprintf("{{- if .Values.admissionPolicies.enabled }}\n%s{{- end }}\n", strings.Join(parts, "---\n"))
		outPath := filepath.Join(opts.policyOutputDir, outName)
		if err := writeFile(outPath, output); err != nil {
			return fmt.Errorf("write %q: %w", outPath, err)
		}
	}

	return nil
}

// transformPolicyToHelm transforms a kustomize policy YAML to Helm template.
func transformPolicyToHelm(content string) string {

	// Add Helm labels after metadata.name
	content = addHelmLabels(content)

	// Replace static name with Helm template
	// Pattern: name: <policy-name> -> name: {{ include "openbao-operator.fullname" . }}-<policy-name>
	namePattern := regexp.MustCompile(`(\n\s+name:\s+)([\w-]+)(\s*\n)`)
	content = namePattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := namePattern.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		prefix := parts[1]
		name := parts[2]
		suffix := parts[3]
		// Don't transform names that are already templates or nested names
		if strings.Contains(name, "{{") || strings.Contains(prefix, "policyName") {
			return match
		}
		return fmt.Sprintf("%s{{ include \"openbao-operator.fullname\" . }}-%s%s", prefix, name, suffix)
	})

	// Replace policyName references - strip existing openbao-operator- prefix if present.
	// Also supports "openbao-"-prefixed policy names (e.g. openbao-restrict-provisioner-rbac).
	policyNamePattern := regexp.MustCompile(
		"(policyName:\\s*)(openbao-operator-)?" +
			"((?:openbao-)?(?:lock-|restrict-|validate-|enforce-|protect-))" +
			"([\\w-]+)",
	)
	content = policyNamePattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := policyNamePattern.FindStringSubmatch(match)
		if len(parts) < 5 {
			return match
		}
		prefix := parts[1]
		// parts[2] is the optional "openbao-operator-" which we strip
		policyPrefix := parts[3] // (openbao-)?(lock-|restrict-|validate-|protect-)
		policySuffix := parts[4]
		return fmt.Sprintf("%s{{ include \"openbao-operator.fullname\" . }}-%s%s", prefix, policyPrefix, policySuffix)
	})

	// Replace namespace references in expressions
	content = strings.ReplaceAll(content, "openbao-operator-system", "{{ .Release.Namespace }}")

	// Replace service account references in expressions
	// Note: Helm templates inside YAML multiline strings need unescaped quotes
	content = strings.ReplaceAll(content,
		`"system:serviceaccount:{{ .Release.Namespace }}:openbao-operator-controller"`,
		`"system:serviceaccount:{{ .Release.Namespace }}:{{ include "openbao-operator.controllerServiceAccountName" . }}"`)
	content = strings.ReplaceAll(content,
		`"system:serviceaccount:{{ .Release.Namespace }}:openbao-operator-provisioner"`,
		`"system:serviceaccount:{{ .Release.Namespace }}:{{ include "openbao-operator.provisionerServiceAccountName" . }}"`)

	// Replace controller ServiceAccount name references in RoleBinding subject validation expressions.
	content = strings.ReplaceAll(content,
		`'openbao-operator-controller'`,
		`'{{ include "openbao-operator.controllerServiceAccountName" . }}'`)
	content = strings.ReplaceAll(content,
		`'openbao-operator-provisioner'`,
		`'{{ include "openbao-operator.provisionerServiceAccountName" . }}'`)

	return content
}

func addPVCBindingSyncCallers(content string) (string, error) {
	const marker = "    - name: pvc_binding_sync_callers\n      expression: >-\n        {}"
	if strings.Count(content, marker) != 1 {
		return "", fmt.Errorf("expected exactly one default PVC binding sync caller variable")
	}
	const replacement = "    - name: pvc_binding_sync_callers\n" +
		"      expression: {{ include \"openbao-operator.pvcBindingSyncCallers\" . | quote }}"
	return strings.Replace(content, marker, replacement, 1), nil
}

func addNamespacePodSecurityLabelPolicyMode(content string) (string, error) {
	const startMarker = "    # Rule 1: Only enforce Pod Security Standards labels (restricted), " +
		"and do not change anything else."
	const endMarker = "      message: " +
		`"The Provisioner may only enforce Pod Security Standards labels (restricted) on Namespaces."`

	start := strings.Index(content, startMarker)
	if start == -1 {
		return "", fmt.Errorf("namespace Pod Security label rule start marker not found")
	}
	relativeEnd := strings.Index(content[start:], endMarker)
	if relativeEnd == -1 {
		return "", fmt.Errorf("namespace Pod Security label rule end marker not found")
	}
	end := start + relativeEnd + len(endMarker)

	externalRule := `{{ if eq .Values.tenancy.namespacePodSecurityLabels.mode "external" }}
    # Rule 1: Namespace Pod Security labels are externally managed, so the Provisioner must not mutate Namespaces.
    - expression: >-
        !variables.is_provisioner
      message: >-
        The Provisioner may not mutate Namespaces when tenant namespace Pod Security labels are externally managed.
{{ else }}
`
	replacement := externalRule + content[start:end] + "\n{{ end }}"
	return content[:start] + replacement + content[end:], nil
}

// addHelmLabels adds standard Helm labels after the metadata block.
func addHelmLabels(content string) string {
	// Find metadata: block and add labels after name
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines)+2) // Pre-allocate with extra space for labels
	inMetadata := false
	labelsAdded := false

	for i, line := range lines {
		result = append(result, line)

		if strings.TrimSpace(line) == metadataMarker {
			inMetadata = true
			continue
		}

		if inMetadata && !labelsAdded && strings.HasPrefix(strings.TrimSpace(line), "name:") {
			// Add labels after name line
			indent := leadingWhitespace(line)
			result = append(result, indent+"labels:")
			result = append(result, indent+"  {{- include \"openbao-operator.labels\" . | nindent 4 }}")
			labelsAdded = true

			// If next line is spec:, we're done with metadata
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == specMarker {
				inMetadata = false
			}
		}

		if inMetadata && strings.TrimSpace(line) == specMarker {
			inMetadata = false
		}
	}

	return strings.Join(result, "\n")
}

// syncRBAC syncs RBAC resources from config/rbac to Helm templates.
// This transforms Kustomize RBAC manifests into Helm templates with proper
// namespace, name, and conditional transformations.
func syncRBAC(opts options) error {
	if err := os.MkdirAll(opts.rbacOutputDir, 0o750); err != nil {
		return fmt.Errorf("create RBAC output dir %q: %w", opts.rbacOutputDir, err)
	}

	// Sync provisioner ClusterRoles (multi-tenant mode only)
	if err := syncProvisionerRBAC(opts); err != nil {
		return fmt.Errorf("sync provisioner RBAC: %w", err)
	}

	// Sync controller ClusterRoles (always)
	if err := syncControllerRBAC(opts); err != nil {
		return fmt.Errorf("sync controller RBAC: %w", err)
	}

	// Sync single-tenant ClusterRole
	if err := syncSingleTenantRBAC(opts); err != nil {
		return fmt.Errorf("sync single-tenant RBAC: %w", err)
	}

	// Sync controller-only TokenRequest permissions
	if err := syncControllerTokenRBAC(opts); err != nil {
		return fmt.Errorf("sync controller token RBAC: %w", err)
	}

	// Sync leader election
	if err := syncLeaderElectionRBAC(opts); err != nil {
		return fmt.Errorf("sync leader election RBAC: %w", err)
	}

	// Sync aggregated ClusterRoles
	if err := syncAggregatedRBAC(opts); err != nil {
		return fmt.Errorf("sync aggregated RBAC: %w", err)
	}

	fmt.Println("RBAC sync: completed")
	return nil
}

// syncProvisionerRBAC syncs provisioner-related RBAC (multi-tenant mode).
func syncProvisionerRBAC(opts options) error {
	parts := make([]string, 0, 3)

	// 1. Provisioner minimal ClusterRole
	provisionerRole, err := readFile(filepath.Join(opts.rbacInputDir, "provisioner_minimal_role.yaml"))
	if err != nil {
		return fmt.Errorf("read provisioner_minimal_role.yaml: %w", err)
	}
	provisionerRole = transformRBACToHelm(provisionerRole, "provisioner", false)
	provisionerRole, err = addNamespacePodSecurityLabelRBACMode(provisionerRole)
	if err != nil {
		return fmt.Errorf("add namespace Pod Security label mode to provisioner RBAC: %w", err)
	}
	parts = append(parts, provisionerRole)

	// 2. Provisioner ClusterRoleBindings
	provisionerMetricsBinding := generateBinding(
		"provisioner-metrics-auth",
		"metrics-auth",
		"provisionerServiceAccountName",
		"provisioner",
	)
	parts = append(parts, provisionerMetricsBinding)

	provisionerBinding := generateBinding(
		"provisioner",
		"provisioner",
		"provisionerServiceAccountName",
		"provisioner",
	)
	parts = append(parts, provisionerBinding)

	// Wrap in multi-tenant conditional
	output := fmt.Sprintf("{{- if eq .Values.tenancy.mode \"multi\" }}\n%s{{- end }}\n", strings.Join(parts, "---\n"))
	outPath := filepath.Join(opts.rbacOutputDir, "provisioner-clusterroles.yaml")
	return writeFile(outPath, output)
}

func addNamespacePodSecurityLabelRBACMode(content string) (string, error) {
	const namespaceVerbs = `    verbs:
      - get
      - update
      - patch
`
	const namespaceVerbsWithMode = `    verbs:
      - get
{{ if eq .Values.tenancy.namespacePodSecurityLabels.mode "enforce" }}
      - update
      - patch
{{ end }}
`
	if !strings.Contains(content, namespaceVerbs) {
		return "", fmt.Errorf("namespace get/update/patch verbs block not found")
	}
	return strings.Replace(content, namespaceVerbs, namespaceVerbsWithMode, 1), nil
}

// syncControllerRBAC syncs controller-related RBAC.
func syncControllerRBAC(opts options) error {
	var parts []string

	// Metrics auth ClusterRole
	metricsAuth, err := readFile(filepath.Join(opts.rbacInputDir, "metrics_auth_role.yaml"))
	if err != nil {
		return fmt.Errorf("read metrics_auth_role.yaml: %w", err)
	}
	metricsAuth = transformRBACToHelm(metricsAuth, "metrics-auth", false)
	parts = append(parts, metricsAuth)

	// Metrics reader ClusterRole
	metricsReader, err := readFile(filepath.Join(opts.rbacInputDir, "metrics_reader_role.yaml"))
	if err != nil {
		return fmt.Errorf("read metrics_reader_role.yaml: %w", err)
	}
	metricsReader = transformRBACToHelm(metricsReader, "metrics-reader", false)
	parts = append(parts, metricsReader)

	// Controller OpenBaoCluster ClusterRole
	clusterRole, err := readFile(filepath.Join(opts.rbacInputDir, "controller_openbaocluster_clusterrole.yaml"))
	if err != nil {
		return fmt.Errorf("read controller_openbaocluster_clusterrole.yaml: %w", err)
	}
	clusterRole = transformRBACToHelm(clusterRole, "controller-openbaocluster", false)
	parts = append(parts, clusterRole)

	// Controller OpenBaoRestore ClusterRole
	restoreRole, err := readFile(filepath.Join(opts.rbacInputDir, "controller_openbaorestore_clusterrole.yaml"))
	if err != nil {
		return fmt.Errorf("read controller_openbaorestore_clusterrole.yaml: %w", err)
	}
	restoreRole = transformRBACToHelm(restoreRole, "controller-openbaorestore", false)
	parts = append(parts, restoreRole)

	// Controller ClusterRoleBindings
	metricsAuthBinding := generateBinding(
		"controller-metrics-auth",
		"metrics-auth",
		"controllerServiceAccountName",
		"controller",
	)
	parts = append(parts, metricsAuthBinding)

	clusterRoleBinding := generateBinding(
		"controller-openbaocluster",
		"controller-openbaocluster",
		"controllerServiceAccountName",
		"controller",
	)
	parts = append(parts, clusterRoleBinding)

	restoreRoleBinding := generateBinding(
		"controller-openbaorestore",
		"controller-openbaorestore",
		"controllerServiceAccountName",
		"controller",
	)
	parts = append(parts, restoreRoleBinding)

	output := strings.Join(parts, "---\n") + "\n"
	outPath := filepath.Join(opts.rbacOutputDir, "controller-clusterroles.yaml")
	return writeFile(outPath, output)
}

// syncSingleTenantRBAC syncs single-tenant mode RBAC.
func syncSingleTenantRBAC(opts options) error {
	content, err := readFile(filepath.Join(opts.rbacInputDir, "single_tenant_clusterrole.yaml"))
	if err != nil {
		return fmt.Errorf("read single_tenant_clusterrole.yaml: %w", err)
	}

	content = transformRBACToHelm(content, "single-tenant", true)

	// Add RoleBinding for single-tenant mode
	roleBinding := `---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "openbao-operator.fullname" . }}-single-tenant
  namespace: {{ .Values.tenancy.targetNamespace | default .Release.Namespace }}
  labels:
    {{- include "openbao-operator.labels" . | nindent 4 }}
    app.kubernetes.io/component: controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "openbao-operator.fullname" . }}-single-tenant
subjects:
  - kind: ServiceAccount
    name: {{ include "openbao-operator.controllerServiceAccountName" . }}
    namespace: {{ .Release.Namespace }}
`

	output := fmt.Sprintf("{{- if eq .Values.tenancy.mode \"single\" }}\n%s%s{{- end }}\n", content, roleBinding)
	outPath := filepath.Join(opts.rbacOutputDir, "single-tenant-clusterrole.yaml")
	return writeFile(outPath, output)
}

// syncControllerTokenRBAC keeps token issuance confined to the controller identity.
func syncControllerTokenRBAC(opts options) error {
	var parts []string
	for _, name := range []string{"controller_token_role.yaml", "controller_token_role_binding.yaml"} {
		content, err := readFile(filepath.Join(opts.rbacInputDir, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		content = strings.ReplaceAll(content, "name: controller-token", "name: "+helmFullname+"-controller-token")
		content = strings.ReplaceAll(content, "namespace: system", "namespace: {{ .Release.Namespace }}")
		content = strings.ReplaceAll(content, "name: controller\n",
			"name: {{ include \"openbao-operator.controllerServiceAccountName\" . }}\n")
		content = strings.ReplaceAll(content, `resourceNames: ["controller"]`,
			`resourceNames: [{{ include "openbao-operator.controllerServiceAccountName" . | quote }}]`)
		parts = append(parts, addHelmLabelsToRBAC(content))
	}
	return writeFile(filepath.Join(opts.rbacOutputDir, "controller-token.yaml"), strings.Join(parts, "---\n"))
}

// syncLeaderElectionRBAC syncs leader election RBAC.
func syncLeaderElectionRBAC(opts options) error {
	role, err := readFile(filepath.Join(opts.rbacInputDir, "leader_election_role.yaml"))
	if err != nil {
		return fmt.Errorf("read leader_election_role.yaml: %w", err)
	}

	role = transformRBACToHelm(role, "leader-election", false)
	// Leader election is a Role, not ClusterRole - fix namespace
	role = strings.Replace(role, "kind: ClusterRole", "kind: Role", 1)
	role = addNamespaceToMetadata(role)

	// Controller leader election RoleBinding
	controllerBinding := `---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "openbao-operator.fullname" . }}-controller-leader-election
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "openbao-operator.labels" . | nindent 4 }}
    app.kubernetes.io/component: controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "openbao-operator.fullname" . }}-leader-election
subjects:
  - kind: ServiceAccount
    name: {{ include "openbao-operator.controllerServiceAccountName" . }}
    namespace: {{ .Release.Namespace }}
`

	// Provisioner leader election RoleBinding (multi-tenant only)
	provisionerBinding := `{{- if eq .Values.tenancy.mode "multi" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "openbao-operator.fullname" . }}-provisioner-leader-election
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "openbao-operator.labels" . | nindent 4 }}
    app.kubernetes.io/component: provisioner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "openbao-operator.fullname" . }}-leader-election
subjects:
  - kind: ServiceAccount
    name: {{ include "openbao-operator.provisionerServiceAccountName" . }}
    namespace: {{ .Release.Namespace }}
{{- end }}
`

	output := role + controllerBinding + provisionerBinding
	outPath := filepath.Join(opts.rbacOutputDir, "leader-election.yaml")
	return writeFile(outPath, output)
}

// syncAggregatedRBAC syncs aggregated ClusterRoles.
func syncAggregatedRBAC(opts options) error {
	parts := make([]string, 0, 9) // 8 cluster roles + 1 tenant role

	// OpenBaoCluster admin/editor/viewer and delegated dangerous-control roles.
	for _, role := range []struct {
		filename   string
		nameSuffix string
	}{
		{filename: "openbaocluster_admin_role.yaml", nameSuffix: "openbaocluster-admin"},
		{filename: "openbaocluster_editor_role.yaml", nameSuffix: "openbaocluster-editor"},
		{filename: "openbaocluster_cloud_identity_role.yaml", nameSuffix: "openbaocluster-cloud-identity"},
		{filename: "openbaocluster_helper_image_role.yaml", nameSuffix: "openbaocluster-helper-image"},
		{filename: "openbaocluster_image_trust_roots_role.yaml", nameSuffix: "openbaocluster-image-trust-roots"},
		{filename: "openbaocluster_network_publication_role.yaml", nameSuffix: "openbaocluster-network-publication"},
		{filename: "openbaocluster_restore_role.yaml", nameSuffix: "openbaocluster-restore"},
		{filename: "openbaocluster_viewer_role.yaml", nameSuffix: "openbaocluster-viewer"},
	} {
		filename := role.filename
		content, err := readFile(filepath.Join(opts.rbacInputDir, filename))
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		content = transformRBACToHelm(content, role.nameSuffix, false)
		parts = append(parts, content)
	}

	// OpenBaoTenant editor role
	tenantEditor, err := readFile(filepath.Join(opts.rbacInputDir, "openbaotenant_editor_role.yaml"))
	if err != nil {
		return fmt.Errorf("read openbaotenant_editor_role.yaml: %w", err)
	}
	tenantEditor = transformRBACToHelm(tenantEditor, "openbaotenant-editor", false)
	parts = append(parts, tenantEditor)

	output := strings.Join(parts, "---\n") + "\n"
	outPath := filepath.Join(opts.rbacOutputDir, "aggregated-clusterroles.yaml")
	return writeFile(outPath, output)
}

// transformRBACToHelm applies Helm template transformations to RBAC content.
func transformRBACToHelm(content, nameSuffix string, hasGatewayAPI bool) string {
	// Remove kustomize comments and leading ---
	content = strings.TrimPrefix(content, "---\n")
	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Preserve Trivy inline ignore directives so Helm templates carry them through.
			if strings.Contains(trimmed, "trivy:ignore:") {
				filtered = append(filtered, line)
			}
			continue // Skip other comments
		}
		filtered = append(filtered, line)
	}
	content = strings.Join(filtered, "\n")

	// Replace hardcoded name with Helm template
	namePattern := regexp.MustCompile(`(\n\s*name:\s*)openbao-operator-[a-z-]+(\s*\n)`)
	replacement := fmt.Sprintf(`$1%s-%s$2`, helmFullname, nameSuffix)
	content = namePattern.ReplaceAllString(content, replacement)

	// Also handle names without openbao-operator- prefix
	simpleNamePattern := regexp.MustCompile(`(\n\s*name:\s*)([a-z][a-z0-9-]*-role)(\s*\n)`)
	simpleReplacement := fmt.Sprintf(`$1%s-%s$3`, helmFullname, nameSuffix)
	content = simpleNamePattern.ReplaceAllString(content, simpleReplacement)

	// Replace namespace references
	content = strings.ReplaceAll(content, "openbao-operator-system", "{{ .Release.Namespace }}")

	// Add Helm labels after metadata.name
	content = addHelmLabelsToRBAC(content)

	// Wrap Gateway API rules in conditional
	if hasGatewayAPI {
		content = wrapGatewayAPIRules(content)
	}

	return content
}

// addHelmLabelsToRBAC adds Helm labels to RBAC resources.
func addHelmLabelsToRBAC(content string) string {
	// Ensure metadata.labels contains Helm labels (and keep any existing labels like component hints).
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines)+4) // Extra space for Helm labels
	inMetadata := false
	inMetadataLabels := false
	metadataLabelsIndent := 0
	labelsSeen := false
	labelsInjected := false

	metadataHasLabelsAhead := func(start int) bool {
		for j := start; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == rulesMarker || t == specMarker || t == "roleRef:" || t == "subjects:" {
				return false
			}
			if t == "labels:" {
				return true
			}
		}
		return false
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if strings.TrimSpace(line) == metadataMarker {
			result = append(result, line)
			inMetadata = true
			labelsSeen = false
			labelsInjected = false
			continue
		}

		if !inMetadata {
			result = append(result, line)
			continue
		}

		trimmed := strings.TrimSpace(line)
		if inMetadataLabels && trimmed != "" && len(leadingWhitespace(line)) <= metadataLabelsIndent {
			inMetadataLabels = false
		}
		if inMetadataLabels && isHelmManagedLabelLine(line) {
			continue
		}

		// If there's an existing labels block, inject Helm labels into it as the first entry.
		if trimmed == "labels:" {
			labelsSeen = true
			inMetadataLabels = true
			metadataLabelsIndent = len(leadingWhitespace(line))
			result = append(result, line)

			childIndent := leadingWhitespace(line) + "  "
			result = append(result, childIndent+`{{- include "openbao-operator.labels" . | nindent 4 }}`)
			labelsInjected = true
			continue
		}

		// If there's no labels block, add one immediately after metadata.name.
		if strings.HasPrefix(trimmed, "name:") && !labelsSeen && !labelsInjected {
			// If a labels block exists later in metadata, don't add a new one here.
			if metadataHasLabelsAhead(i + 1) {
				result = append(result, line)
				continue
			}
			result = append(result, line)
			indent := leadingWhitespace(line)
			result = append(result, indent+"labels:")
			result = append(result, indent+`  {{- include "openbao-operator.labels" . | nindent 4 }}`)
			labelsInjected = true
			continue
		}

		if inMetadata && (strings.TrimSpace(line) == rulesMarker || strings.TrimSpace(line) == specMarker) {
			inMetadata = false
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

func isHelmManagedLabelLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	key, _, ok := strings.Cut(trimmed, ":")
	if !ok {
		return false
	}
	key = strings.Trim(key, `"'`)
	_, ok = helmManagedLabelKeys[key]
	return ok
}

// wrapGatewayAPIRules wraps Gateway API rules in a conditional.
func wrapGatewayAPIRules(content string) string {
	// Find and wrap gateway.networking.k8s.io rules
	gatewayStart := strings.Index(content, "gateway.networking.k8s.io")
	if gatewayStart == -1 {
		return content
	}

	// Find the start of this rule block (look back for "- apiGroups:")
	ruleStart := strings.LastIndex(content[:gatewayStart], "- apiGroups:")
	if ruleStart == -1 {
		return content
	}

	// Find the end of this rule block (next "- apiGroups:" or end of rules)
	remaining := content[gatewayStart:]
	ruleEnd := strings.Index(remaining, "\n  - apiGroups:")
	if ruleEnd == -1 {
		// Must find the end by looking for next top-level key
		ruleEnd = len(remaining)
		for _, marker := range []string{"\n  - apiGroups:", "\n---", "\napiVersion:"} {
			idx := strings.Index(remaining, marker)
			if idx != -1 && idx < ruleEnd {
				ruleEnd = idx
			}
		}
	}

	// Extract the rule
	beforeRule := content[:ruleStart]
	rule := content[ruleStart : gatewayStart+ruleEnd]
	afterRule := content[gatewayStart+ruleEnd:]

	// Get indentation
	indent := "  "

	// Wrap the rule
	wrapped := fmt.Sprintf("%s{{- if .Values.gatewayAPI.enabled }}\n%s%s{{- end }}\n%s",
		beforeRule, indent, strings.TrimPrefix(rule, indent), afterRule)

	return wrapped
}

// addNamespaceToMetadata adds namespace to metadata block.
func addNamespaceToMetadata(content string) string {
	// Add namespace after name in metadata
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines)+1)
	inMetadata := false

	for _, line := range lines {
		result = append(result, line)

		if strings.TrimSpace(line) == metadataMarker {
			inMetadata = true
			continue
		}

		if inMetadata && strings.HasPrefix(strings.TrimSpace(line), "name:") {
			indent := leadingWhitespace(line)
			result = append(result, indent+"namespace: {{ .Release.Namespace }}")
			inMetadata = false
		}
	}

	return strings.Join(result, "\n")
}

// generateBinding generates a ClusterRoleBinding template.
func generateBinding(name, roleName, saHelper, component string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "openbao-operator.fullname" . }}-%s
  labels:
    {{- include "openbao-operator.labels" . | nindent 4 }}
    app.kubernetes.io/component: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "openbao-operator.fullname" . }}-%s
subjects:
  - kind: ServiceAccount
    name: {{ include "openbao-operator.%s" . }}
    namespace: {{ .Release.Namespace }}
`, name, component, roleName, saHelper)
}
