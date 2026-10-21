package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	defaultBaselinePath = "api/stability/baselines/0.5.2.json"
	defaultCurrentDir   = "config/crd/bases"
	defaultTarget       = "next"
	impactBreaking      = "breaking"
	impactCompatible    = "compatible"
	impactReview        = "review"
	validationRelaxed   = "validation-relaxed"
	validationTightened = "validation-tightened"
	trueValue           = "true"
	currentAPIVersion   = "v1alpha1"
	openBaoClusterKind  = "OpenBaoCluster"
	openBaoClusterCRD   = "openbaoclusters.openbao.org"
)

type options struct {
	BaselinePath   string
	CurrentDir     string
	Format         string
	Mode           string
	Target         string
	WriteBaseline  string
	BaselineBundle string
	Release        string
}

type snapshot struct {
	SchemaVersion int          `json:"schemaVersion"`
	Release       string       `json:"release"`
	Source        string       `json:"source"`
	SourceSHA256  string       `json:"sourceSHA256"`
	Nodes         []schemaNode `json:"nodes"`
}

type schemaNode struct {
	CRD         string            `json:"crd"`
	Kind        string            `json:"kind"`
	Version     string            `json:"version"`
	Path        string            `json:"path"`
	Type        string            `json:"type,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Default     string            `json:"default,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
	Constraints map[string]string `json:"constraints,omitempty"`
	CEL         []celRule         `json:"cel,omitempty"`
}

type celRule struct {
	Rule              string `json:"rule"`
	Message           string `json:"message,omitempty"`
	MessageExpression string `json:"messageExpression,omitempty"`
	FieldPath         string `json:"fieldPath,omitempty"`
	Reason            string `json:"reason,omitempty"`
	OptionalOldSelf   string `json:"optionalOldSelf,omitempty"`
}

type change struct {
	Impact         string `json:"impact"`
	Classification string `json:"classification"`
	Kind           string `json:"kind"`
	Version        string `json:"version"`
	Path           string `json:"path"`
	Detail         string `json:"detail"`
}

type report struct {
	Baseline string   `json:"baseline"`
	Target   string   `json:"target"`
	Mode     string   `json:"mode"`
	Changes  []change `json:"changes"`
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		fail(2, err)
	}

	if opts.WriteBaseline != "" {
		if err := generateBaseline(opts); err != nil {
			fail(1, err)
		}
		return
	}

	report, err := buildReport(opts.BaselinePath, opts.CurrentDir, opts.Target, opts.Mode)
	if err != nil {
		fail(1, err)
	}
	if err := writeReport(os.Stdout, report, opts.Format); err != nil {
		fail(1, err)
	}
	if opts.Mode == "enforce" && hasBlockingChanges(report.Changes) {
		fail(1, fmt.Errorf("CRD compatibility enforcement rejected breaking or review-required changes"))
	}
}

func parseOptions() (options, error) {
	var opts options
	flag.StringVar(&opts.BaselinePath, "baseline", defaultBaselinePath, "normalized baseline snapshot")
	flag.StringVar(&opts.CurrentDir, "current-dir", defaultCurrentDir, "directory containing current generated CRDs")
	flag.StringVar(&opts.Format, "format", "markdown", "output format: markdown or json")
	flag.StringVar(&opts.Mode, "mode", "enforce", "compatibility mode: report or enforce")
	flag.StringVar(&opts.Target, "target", defaultTarget, "target release label")
	flag.StringVar(&opts.WriteBaseline, "write-baseline", "", "write a normalized baseline snapshot to this path")
	flag.StringVar(&opts.BaselineBundle, "baseline-bundle", "", "released CRD YAML bundle used with --write-baseline")
	flag.StringVar(&opts.Release, "release", "", "release label used with --write-baseline")
	flag.Parse()

	if opts.Format != "markdown" && opts.Format != "json" {
		return options{}, fmt.Errorf("format must be markdown or json")
	}
	if opts.Mode != "report" && opts.Mode != "enforce" {
		return options{}, fmt.Errorf("mode must be report or enforce")
	}
	if opts.WriteBaseline != "" && (opts.BaselineBundle == "" || opts.Release == "") {
		return options{}, fmt.Errorf("--baseline-bundle and --release are required with --write-baseline")
	}
	return opts, nil
}

func fail(code int, err error) {
	fmt.Fprintf(os.Stderr, "crd_compatibility: %v\n", err)
	os.Exit(code)
}

func generateBaseline(opts options) error {
	nodes, err := loadSchemaNodes([]string{opts.BaselineBundle})
	if err != nil {
		return err
	}
	data, err := os.ReadFile(opts.BaselineBundle)
	if err != nil {
		return fmt.Errorf("read baseline bundle %s: %w", opts.BaselineBundle, err)
	}
	digest := sha256.Sum256(data)
	baseline := snapshot{
		SchemaVersion: 1,
		Release:       opts.Release,
		Source:        filepath.Base(opts.BaselineBundle),
		SourceSHA256:  hex.EncodeToString(digest[:]),
		Nodes:         nodes,
	}
	encoded, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(opts.WriteBaseline), 0o755); err != nil {
		return fmt.Errorf("create baseline directory: %w", err)
	}
	if err := os.WriteFile(opts.WriteBaseline, encoded, 0o644); err != nil {
		return fmt.Errorf("write baseline %s: %w", opts.WriteBaseline, err)
	}
	fmt.Printf(
		"Wrote %s CRD baseline with %d schema nodes to %s (sha256:%s)\n",
		opts.Release,
		len(nodes),
		opts.WriteBaseline,
		baseline.SourceSHA256,
	)
	return nil
}

func buildReport(baselinePath, currentDir, target, mode string) (report, error) {
	baseline, err := loadSnapshot(baselinePath)
	if err != nil {
		return report{}, err
	}
	paths, err := filepath.Glob(filepath.Join(currentDir, "*.yaml"))
	if err != nil {
		return report{}, fmt.Errorf("list current CRDs: %w", err)
	}
	if len(paths) == 0 {
		return report{}, fmt.Errorf("no current CRD YAML files found in %s", currentDir)
	}
	current, err := loadSchemaNodes(paths)
	if err != nil {
		return report{}, err
	}
	return report{
		Baseline: baseline.Release,
		Target:   target,
		Mode:     mode,
		Changes:  compareSnapshots(baseline.Nodes, current),
	}, nil
}

func loadSnapshot(path string) (snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, fmt.Errorf("read baseline %s: %w", path, err)
	}
	var result snapshot
	if err := json.Unmarshal(data, &result); err != nil {
		return snapshot{}, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	if result.SchemaVersion != 1 || result.Release == "" || len(result.Nodes) == 0 {
		return snapshot{}, fmt.Errorf("baseline %s has an invalid header or no schema nodes", path)
	}
	return result, nil
}

func loadSchemaNodes(paths []string) ([]schemaNode, error) {
	var nodes []schemaNode
	seenCRDs := make(map[string]bool)
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open CRD source %s: %w", path, err)
		}
		decoder := utilyaml.NewYAMLOrJSONDecoder(file, 4096)
		for {
			var raw map[string]any
			err := decoder.Decode(&raw)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
			if raw == nil || raw["kind"] != "CustomResourceDefinition" {
				continue
			}
			data, err := json.Marshal(raw)
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("normalize CRD from %s: %w", path, err)
			}
			var crd apiextensionsv1.CustomResourceDefinition
			if err := json.Unmarshal(data, &crd); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("parse CRD from %s: %w", path, err)
			}
			if crd.Name == "" || seenCRDs[crd.Name] {
				continue
			}
			seenCRDs[crd.Name] = true
			nodes = append(nodes, collectCRDNodes(&crd)...)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close CRD source %s: %w", path, err)
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no CustomResourceDefinition objects found")
	}
	sortNodes(nodes)
	return nodes, nil
}

func collectCRDNodes(crd *apiextensionsv1.CustomResourceDefinition) []schemaNode {
	var nodes []schemaNode
	for _, version := range crd.Spec.Versions {
		if !version.Served || version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			continue
		}
		root := version.Schema.OpenAPIV3Schema
		for _, rootName := range []string{"spec", "status"} {
			schema, ok := root.Properties[rootName]
			if !ok {
				continue
			}
			collectNode(&nodes, crd.Name, crd.Spec.Names.Kind, version.Name, rootName, schema, contains(root.Required, rootName))
		}
	}
	return nodes
}

func collectNode(
	nodes *[]schemaNode,
	crd, kind, version, path string,
	schema apiextensionsv1.JSONSchemaProps,
	required bool,
) {
	*nodes = append(*nodes, schemaNode{
		CRD:         crd,
		Kind:        kind,
		Version:     version,
		Path:        path,
		Type:        schema.Type,
		Required:    required,
		Default:     canonicalJSON(schema.Default),
		Enum:        canonicalEnum(schema.Enum),
		Constraints: constraints(schema),
		CEL:         canonicalCEL(schema.XValidations),
	})

	propertyNames := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		propertyNames = append(propertyNames, name)
	}
	sort.Strings(propertyNames)
	for _, name := range propertyNames {
		child := schema.Properties[name]
		collectNode(nodes, crd, kind, version, path+"."+name, child, contains(schema.Required, name))
	}
	if schema.Type == "array" && schema.Items != nil && schema.Items.Schema != nil {
		collectNode(nodes, crd, kind, version, path+"[]", *schema.Items.Schema, false)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		collectNode(nodes, crd, kind, version, path+".*", *schema.AdditionalProperties.Schema, false)
	}
	for index, composed := range schema.AllOf {
		collectNode(nodes, crd, kind, version, fmt.Sprintf("%s.allOf[%d]", path, index), composed, false)
	}
	for index, composed := range schema.AnyOf {
		collectNode(nodes, crd, kind, version, fmt.Sprintf("%s.anyOf[%d]", path, index), composed, false)
	}
	for index, composed := range schema.OneOf {
		collectNode(nodes, crd, kind, version, fmt.Sprintf("%s.oneOf[%d]", path, index), composed, false)
	}
	if schema.Not != nil {
		collectNode(nodes, crd, kind, version, path+".not", *schema.Not, false)
	}
}

func canonicalJSON(value *apiextensionsv1.JSON) string {
	if value == nil || len(value.Raw) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(value.Raw, &decoded); err != nil {
		return string(value.Raw)
	}
	data, err := json.Marshal(decoded)
	if err != nil {
		return string(value.Raw)
	}
	return string(data)
}

func canonicalEnum(values []apiextensionsv1.JSON) []string {
	result := make([]string, 0, len(values))
	for i := range values {
		result = append(result, canonicalJSON(&values[i]))
	}
	sort.Strings(result)
	return result
}

func constraints(schema apiextensionsv1.JSONSchemaProps) map[string]string {
	result := make(map[string]string)
	putString(result, "format", schema.Format)
	putString(result, "pattern", schema.Pattern)
	putFloat(result, "minimum", schema.Minimum)
	putFloat(result, "maximum", schema.Maximum)
	putFloat(result, "multipleOf", schema.MultipleOf)
	putInt(result, "minLength", schema.MinLength)
	putInt(result, "maxLength", schema.MaxLength)
	putInt(result, "minItems", schema.MinItems)
	putInt(result, "maxItems", schema.MaxItems)
	putInt(result, "minProperties", schema.MinProperties)
	putInt(result, "maxProperties", schema.MaxProperties)
	if schema.UniqueItems {
		result["uniqueItems"] = trueValue
	}
	if schema.ExclusiveMinimum {
		result["exclusiveMinimum"] = trueValue
	}
	if schema.ExclusiveMaximum {
		result["exclusiveMaximum"] = trueValue
	}
	if schema.Nullable {
		result["nullable"] = trueValue
	}
	if schema.XPreserveUnknownFields != nil && *schema.XPreserveUnknownFields {
		result["preserveUnknownFields"] = trueValue
	}
	if schema.XEmbeddedResource {
		result["embeddedResource"] = trueValue
	}
	if schema.XIntOrString {
		result["intOrString"] = trueValue
	}
	if schema.XListType != nil {
		result["listType"] = *schema.XListType
	}
	if schema.XMapType != nil {
		result["mapType"] = *schema.XMapType
	}
	if len(schema.XListMapKeys) > 0 {
		keys := append([]string(nil), schema.XListMapKeys...)
		sort.Strings(keys)
		result["listMapKeys"] = strings.Join(keys, ",")
	}
	if schema.AdditionalProperties != nil {
		result["additionalProperties"] = strconv.FormatBool(schema.AdditionalProperties.Allows)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func canonicalCEL(rules apiextensionsv1.ValidationRules) []celRule {
	if len(rules) == 0 {
		return nil
	}
	result := make([]celRule, 0, len(rules))
	for _, rule := range rules {
		item := celRule{
			Rule:              rule.Rule,
			Message:           rule.Message,
			MessageExpression: rule.MessageExpression,
			FieldPath:         rule.FieldPath,
		}
		if rule.Reason != nil {
			item.Reason = string(*rule.Reason)
		}
		if rule.OptionalOldSelf != nil {
			item.OptionalOldSelf = strconv.FormatBool(*rule.OptionalOldSelf)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		left, _ := json.Marshal(result[i])
		right, _ := json.Marshal(result[j])
		return string(left) < string(right)
	})
	return result
}

func compareSnapshots(oldNodes, newNodes []schemaNode) []change {
	oldByKey := nodeMap(oldNodes)
	newByKey := nodeMap(newNodes)
	changes := make([]change, 0, len(oldNodes)+len(newNodes))

	for key, oldNode := range oldByKey {
		newNode, ok := newByKey[key]
		if !ok {
			changes = append(changes, newChange(
				impactBreaking,
				"field-removed",
				oldNode,
				"field is absent from the current schema",
			))
			continue
		}
		changes = append(changes, compareNode(oldNode, newNode)...)
	}
	for key, newNode := range newByKey {
		if _, ok := oldByKey[key]; ok {
			continue
		}
		impact := impactCompatible
		classification := "field-added-optional"
		if newNode.Required {
			if addedUnderOptionalAncestor(newNode, oldByKey, newByKey) {
				classification = "field-added-under-optional-parent"
			} else {
				impact = impactBreaking
				classification = "field-added-required"
			}
		}
		changes = append(changes, newChange(
			impact,
			classification,
			newNode,
			"field is new in the current schema",
		))
	}
	sortChanges(changes)
	return changes
}

func compareNode(oldNode, newNode schemaNode) []change {
	var changes []change
	if oldNode.Type != newNode.Type {
		changes = append(changes, newChange(
			impactBreaking,
			"type-changed",
			newNode,
			fmt.Sprintf("type changed from %q to %q", oldNode.Type, newNode.Type),
		))
	}
	if !oldNode.Required && newNode.Required {
		changes = append(changes, newChange(
			impactBreaking,
			"requiredness-added",
			newNode,
			"field changed from optional to required",
		))
	}
	if oldNode.Required && !newNode.Required {
		changes = append(changes, newChange(
			impactCompatible,
			"requiredness-removed",
			newNode,
			"field changed from required to optional",
		))
	}
	if oldNode.Default != newNode.Default {
		changes = append(changes, newChange(
			impactReview,
			"default-changed",
			newNode,
			fmt.Sprintf("default changed from %s to %s", display(oldNode.Default), display(newNode.Default)),
		))
	}
	changes = append(changes, compareEnum(oldNode, newNode)...)
	changes = append(changes, compareConstraints(oldNode, newNode)...)
	if !equalJSON(oldNode.CEL, newNode.CEL) {
		oldRules := celRuleSet(oldNode.CEL)
		newRules := celRuleSet(newNode.CEL)
		added := setDifference(newRules, oldRules)
		removed := setDifference(oldRules, newRules)
		switch {
		case isTLSRotationPeriodDiagnosticFix(newNode, added, removed):
			changes = append(changes, newChange(
				impactCompatible,
				"cel-diagnostic-fix",
				newNode,
				"rotationPeriod presence guard preserves validation outcomes and corrects the missing-field error",
			))
		case isFreshRestoreValidation(newNode, added, removed):
			changes = append(changes, newChange(
				impactCompatible,
				"cel-optional-feature-validation",
				newNode,
				"fresh-target rules are guarded by optional fields absent from the released restore API",
			))
		case isOptionalS3TargetValidation(newNode, added, removed):
			changes = append(changes, newChange(
				impactCompatible,
				"cel-optional-feature-validation",
				newNode,
				"S3 discriminator validation is guarded by an optional field absent from the released storage API",
			))
		case isOptionalAzureWorkloadIdentityValidation(newNode, added, removed):
			changes = append(changes, newChange(
				impactCompatible,
				"cel-optional-feature-validation",
				newNode,
				"Azure Key Vault workload identity rules accept requests without the new optional workloadIdentity field",
			))
		case isOptionalServiceIPFamilyValidation(newNode, added, removed):
			changes = append(changes, newChange(
				impactCompatible,
				"cel-optional-feature-validation",
				newNode,
				"Service IP family rules accept requests without the new optional family fields",
			))
		case len(added) > 0 && len(removed) == 0:
			changes = append(changes, newChange(
				impactReview,
				validationTightened,
				newNode,
				fmt.Sprintf("added %d CEL validation rule(s)", len(added)),
			))
		case len(removed) > 0 && len(added) == 0:
			changes = append(changes, newChange(
				impactCompatible,
				validationRelaxed,
				newNode,
				fmt.Sprintf("removed %d CEL validation rule(s)", len(removed)),
			))
		default:
			changes = append(changes, newChange(
				impactReview,
				"cel-changed",
				newNode,
				"CEL validation rules changed and require semantic review",
			))
		}
	}
	return changes
}

// isOptionalServiceIPFamilyValidation recognizes only the optional-field guards
// verified by TestCRD_ServiceIPFamilies. Released Service configurations omit both
// fields and satisfy both rules. Rule text, metadata, resource, and paths are pinned.
func isOptionalServiceIPFamilyValidation(node schemaNode, added, removed []string) bool {
	if node.CRD != openBaoClusterCRD || node.Kind != openBaoClusterKind ||
		node.Version != currentAPIVersion || (node.Path != "spec.service" && node.Path != "spec.readReplicas.service") ||
		len(added) != 2 || len(removed) != 0 {
		return false
	}
	allowed := celRuleSet([]celRule{
		{
			Rule:    "!has(self.ipFamilies) || size(self.ipFamilies) != 2 || self.ipFamilies[0] != self.ipFamilies[1]",
			Message: "ipFamilies must not contain duplicate families",
		},
		{
			Rule: "!has(self.ipFamilies) || size(self.ipFamilies) != 2 || " +
				"(has(self.ipFamilyPolicy) && self.ipFamilyPolicy != 'SingleStack')",
			Message: "Two IP families require PreferDualStack or RequireDualStack",
		},
	})
	return allowed[added[0]] && allowed[added[1]]
}

// isOptionalAzureWorkloadIdentityValidation recognizes only the guards verified by
// TestCRD_AzureKeyVaultWorkloadIdentity. Each rule starts with
// !has(self.workloadIdentity), so released Azure Key Vault seal configurations,
// which cannot carry that field, satisfy all of them.
func isOptionalAzureWorkloadIdentityValidation(node schemaNode, added, removed []string) bool {
	if node.CRD != openBaoClusterCRD || node.Kind != openBaoClusterKind ||
		node.Version != currentAPIVersion || node.Path != "spec.unseal.azureKeyVault" ||
		len(added) != 3 || len(removed) != 0 {
		return false
	}
	allowed := celRuleSet([]celRule{
		{
			Rule:    "!has(self.workloadIdentity) || (has(self.tenantID) && size(self.tenantID) > 0)",
			Message: "spec.unseal.azureKeyVault.workloadIdentity requires tenantID",
		},
		{
			Rule: "!has(self.workloadIdentity) || ((!has(self.clientID) || size(self.clientID) == 0) && " +
				"(!has(self.clientSecret) || size(self.clientSecret) == 0))",
			Message: "spec.unseal.azureKeyVault.workloadIdentity excludes clientID and clientSecret",
		},
		{
			Rule:    "!has(self.workloadIdentity) || !has(self.authMethod) || self.authMethod == 'workload_identity'",
			Message: "spec.unseal.azureKeyVault.workloadIdentity requires authMethod workload_identity or unset",
		},
	})
	for _, rule := range added {
		if !allowed[rule] {
			return false
		}
	}
	return true
}

// isOptionalS3TargetValidation recognizes only the reviewed discriminator rule
// for the new optional S3 block. Released targets without that block remain valid.
func isOptionalS3TargetValidation(node schemaNode, added, removed []string) bool {
	if node.Version != currentAPIVersion || len(added) != 1 || len(removed) != 0 {
		return false
	}
	clusterTarget := node.CRD == openBaoClusterCRD && node.Kind == openBaoClusterKind &&
		node.Path == "spec.backup.target"
	restoreTarget := node.CRD == "openbaorestores.openbao.org" && node.Kind == "OpenBaoRestore" &&
		node.Path == "spec.source.target"
	if !clusterTarget && !restoreTarget {
		return false
	}
	allowed := celRuleSet([]celRule{{
		Rule:    "self.provider == 's3' || !has(self.s3)",
		Message: "backup target s3 options are only supported when provider is s3",
	}})
	return allowed[added[0]]
}

// isTLSRotationPeriodDiagnosticFix recognizes only the presence guard verified by
// TestCRD_OpenBaoCluster_TLSRotationPeriod. Missing fields are rejected both before
// and after the change; other TLS modes and supplied values retain their behavior.
func isTLSRotationPeriodDiagnosticFix(node schemaNode, added, removed []string) bool {
	if node.CRD != openBaoClusterCRD || node.Kind != openBaoClusterKind ||
		node.Version != currentAPIVersion || node.Path != "spec" || len(added) != 1 || len(removed) != 1 {
		return false
	}

	before := celRule{
		Rule:    "self.tls.mode != 'OperatorManaged' || size(self.tls.rotationPeriod) > 0",
		Message: "spec.tls.rotationPeriod is required when spec.tls.mode is OperatorManaged",
	}
	after := before
	after.Rule = "self.tls.mode != 'OperatorManaged' || " +
		"(has(self.tls.rotationPeriod) && size(self.tls.rotationPeriod) > 0)"
	return celRuleSet([]celRule{before})[removed[0]] && celRuleSet([]celRule{after})[added[0]]
}

// isFreshRestoreValidation recognizes the reviewed rules for optional restore
// fields introduced after 0.5.0. Each rule accepts a request without those fields.
// TestCRD_FreshRestoreOptions verifies legacy requests and the new constraints.
func isFreshRestoreValidation(node schemaNode, added, removed []string) bool {
	if node.CRD != "openbaorestores.openbao.org" || node.Kind != "OpenBaoRestore" ||
		node.Version != currentAPIVersion || node.Path != "spec" || len(added) == 0 || len(removed) != 0 {
		return false
	}

	allowed := celRuleSet([]celRule{
		{
			Rule: "!has(self.clusterTemplate) || (has(self.targetLifecycle) && self.force && !has(self.tokenSecretRef) && " +
				"!has(self.jwtAuthRole) && (!has(self.overrideOperationLock) || !self.overrideOperationLock) && " +
				"has(self.source.expectedClusterID) && size(self.source.expectedClusterID) > 0 && " +
				"has(self.source.expectedVersion) && self.source.expectedVersion == self.clusterTemplate.version)",
			Message: "fresh targets require a lifecycle, matching source identity/version, " +
				"force, and generated JWT authentication",
		},
		{
			Rule:    "has(self.clusterTemplate) || !has(self.targetLifecycle)",
			Message: "targetLifecycle requires clusterTemplate",
		},
		{
			Rule: "!has(self.cleanupAfterSeconds) || " +
				"(has(self.targetLifecycle) && self.targetLifecycle" +
				" == 'Disposable')",
			Message: "cleanupAfterSeconds requires a disposable fresh target",
		},
	})
	for _, rule := range added {
		if !allowed[rule] {
			return false
		}
	}
	return true
}

func compareEnum(oldNode, newNode schemaNode) []change {
	switch {
	case len(oldNode.Enum) == 0 && len(newNode.Enum) > 0:
		return []change{newChange(
			impactBreaking,
			"enum-constraint-added",
			newNode,
			"field changed from unconstrained to allowed values: "+strings.Join(newNode.Enum, ", "),
		)}
	case len(oldNode.Enum) > 0 && len(newNode.Enum) == 0:
		return []change{newChange(
			impactCompatible,
			"enum-constraint-removed",
			newNode,
			"field changed from enum to unconstrained; previously allowed values: "+strings.Join(oldNode.Enum, ", "),
		)}
	}

	var changes []change
	if removed := difference(oldNode.Enum, newNode.Enum); len(removed) > 0 {
		changes = append(changes, newChange(
			impactBreaking,
			"enum-values-removed",
			newNode,
			"removed values: "+strings.Join(removed, ", "),
		))
	}
	if added := difference(newNode.Enum, oldNode.Enum); len(added) > 0 {
		changes = append(changes, newChange(
			impactCompatible,
			"enum-values-added",
			newNode,
			"added values: "+strings.Join(added, ", "),
		))
	}
	return changes
}

func compareConstraints(oldNode, newNode schemaNode) []change {
	keys := map[string]bool{}
	for key := range oldNode.Constraints {
		keys[key] = true
	}
	for key := range newNode.Constraints {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	changes := make([]change, 0, len(ordered))
	for _, key := range ordered {
		oldValue := oldNode.Constraints[key]
		newValue := newNode.Constraints[key]
		if oldValue == newValue {
			continue
		}
		impact, classification := classifyConstraintChange(key, oldValue, newValue)
		changes = append(changes, newChange(
			impact,
			classification,
			newNode,
			fmt.Sprintf("%s changed from %s to %s", key, display(oldValue), display(newValue)),
		))
	}
	return changes
}

func classifyConstraintChange(key, oldValue, newValue string) (string, string) {
	switch key {
	case "nullable", "preserveUnknownFields":
		if newValue == trueValue {
			return impactCompatible, validationRelaxed
		}
		return impactReview, validationTightened
	case "uniqueItems":
		if newValue == trueValue {
			return impactReview, validationTightened
		}
		return impactCompatible, validationRelaxed
	case "listMapKeys", "listType", "mapType":
		return impactReview, "validation-changed"
	}
	if newValue == "" {
		return impactCompatible, validationRelaxed
	}
	if oldValue == "" {
		return impactReview, validationTightened
	}
	oldNumber, oldErr := strconv.ParseFloat(oldValue, 64)
	newNumber, newErr := strconv.ParseFloat(newValue, 64)
	if oldErr == nil && newErr == nil {
		switch key {
		case "minimum", "minLength", "minItems", "minProperties":
			if newNumber > oldNumber {
				return impactReview, validationTightened
			}
			return impactCompatible, validationRelaxed
		case "maximum", "maxLength", "maxItems", "maxProperties":
			if newNumber < oldNumber {
				return impactReview, validationTightened
			}
			return impactCompatible, validationRelaxed
		}
	}
	return impactReview, "validation-changed"
}

func newChange(impact, classification string, node schemaNode, detail string) change {
	return change{
		Impact:         impact,
		Classification: classification,
		Kind:           node.Kind,
		Version:        node.Version,
		Path:           node.Path,
		Detail:         detail,
	}
}

func writeReport(writer io.Writer, result report, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	counts := map[string]int{impactBreaking: 0, impactReview: 0, impactCompatible: 0}
	for _, item := range result.Changes {
		counts[item.Impact]++
	}
	modeLabel := "enforced"
	if result.Mode == "report" {
		modeLabel = "report-only"
	}
	if _, err := fmt.Fprintf(
		writer,
		"CRD compatibility: %s -> %s (%s)\n",
		result.Baseline,
		result.Target,
		modeLabel,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		writer,
		"Summary: %d breaking, %d review, %d compatible\n",
		counts[impactBreaking],
		counts[impactReview],
		counts[impactCompatible],
	); err != nil {
		return err
	}
	if len(result.Changes) == 0 {
		_, err := fmt.Fprintln(writer, "No schema changes detected.")
		return err
	}
	header := "\n| Impact | Classification | Resource | Path | Detail |\n" +
		"| --- | --- | --- | --- | --- |"
	if _, err := fmt.Fprintln(writer, header); err != nil {
		return err
	}
	for _, item := range result.Changes {
		if _, err := fmt.Fprintf(
			writer,
			"| %s | %s | %s/%s | `%s` | %s |\n",
			item.Impact,
			item.Classification,
			item.Kind,
			item.Version,
			item.Path,
			strings.ReplaceAll(item.Detail, "|", "\\|"),
		); err != nil {
			return err
		}
	}
	return nil
}

func hasBlockingChanges(changes []change) bool {
	for _, item := range changes {
		if item.Impact == impactBreaking || item.Impact == impactReview {
			return true
		}
	}
	return false
}

func nodeMap(nodes []schemaNode) map[string]schemaNode {
	result := make(map[string]schemaNode, len(nodes))
	for _, node := range nodes {
		result[nodeKey(node, node.Path)] = node
	}
	return result
}

func addedUnderOptionalAncestor(node schemaNode, oldByKey, newByKey map[string]schemaNode) bool {
	for path := parentPath(node.Path); path != ""; path = parentPath(path) {
		key := nodeKey(node, path)
		if _, existed := oldByKey[key]; existed {
			return false
		}
		if ancestor, exists := newByKey[key]; exists && !ancestor.Required {
			return true
		}
	}
	return false
}

func nodeKey(node schemaNode, path string) string {
	return node.CRD + "\x00" + node.Version + "\x00" + path
}

func parentPath(path string) string {
	if strings.HasSuffix(path, "[]") {
		return strings.TrimSuffix(path, "[]")
	}
	if separator := strings.LastIndex(path, "."); separator >= 0 {
		return path[:separator]
	}
	return ""
}

func sortNodes(nodes []schemaNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].CRD != nodes[j].CRD {
			return nodes[i].CRD < nodes[j].CRD
		}
		if nodes[i].Version != nodes[j].Version {
			return nodes[i].Version < nodes[j].Version
		}
		return nodes[i].Path < nodes[j].Path
	})
}

func sortChanges(changes []change) {
	impactOrder := map[string]int{"breaking": 0, "review": 1, "compatible": 2}
	sort.Slice(changes, func(i, j int) bool {
		if impactOrder[changes[i].Impact] != impactOrder[changes[j].Impact] {
			return impactOrder[changes[i].Impact] < impactOrder[changes[j].Impact]
		}
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Classification < changes[j].Classification
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func difference(left, right []string) []string {
	rightSet := make(map[string]bool, len(right))
	for _, value := range right {
		rightSet[value] = true
	}
	var result []string
	for _, value := range left {
		if !rightSet[value] {
			result = append(result, value)
		}
	}
	return result
}

func celRuleSet(rules []celRule) map[string]bool {
	result := make(map[string]bool, len(rules))
	for _, rule := range rules {
		data, _ := json.Marshal(rule)
		result[string(data)] = true
	}
	return result
}

func setDifference(left, right map[string]bool) []string {
	var result []string
	for value := range left {
		if !right[value] {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func equalJSON(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func putString(values map[string]string, key, value string) {
	if value != "" {
		values[key] = value
	}
}

func putFloat(values map[string]string, key string, value *float64) {
	if value != nil {
		values[key] = strconv.FormatFloat(*value, 'g', -1, 64)
	}
}

func putInt(values map[string]string, key string, value *int64) {
	if value != nil {
		values[key] = strconv.FormatInt(*value, 10)
	}
}

func display(value string) string {
	if value == "" {
		return "<unset>"
	}
	return strconv.Quote(value)
}
