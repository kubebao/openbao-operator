package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

const defaultInventoryPath = "api/stability/v1alpha1.yaml"

var classificationOrder = []string{
	"beta-stable",
	"additive-unfrozen",
	"likely-move",
	"deprecated",
	"stable-automation",
	"informational",
}

var allowedClassifications = stringSet(classificationOrder...)

var allowedSpecClassifications = stringSet(
	"beta-stable",
	"additive-unfrozen",
	"likely-move",
	"deprecated",
)

var allowedStatusClassifications = stringSet(
	"stable-automation",
	"informational",
	"deprecated",
)

var allowedMutability = stringSet(
	"mutable",
	"immutable",
	"grow-only",
	"transition-guarded",
	"request-token",
	"operator-owned",
)

var allowedMigration = stringSet("required-if-changed", "none")

var allowedModuleInteractions = stringSet("none", "optional-module-hook")

var allowedEnforcementLayers = stringSet(
	"crd-schema",
	"admission-policy",
	"controller",
	"restore-controller",
	"provisioner",
	"status-writer",
)

type options struct {
	InventoryPath string
	Format        string
	Update        bool
}

type inventory struct {
	SchemaVersion  int      `yaml:"schemaVersion"`
	Release        string   `yaml:"release"`
	Baseline       string   `yaml:"baseline"`
	DecisionStatus string   `yaml:"decisionStatus"`
	Snapshot       string   `yaml:"snapshot"`
	Notes          []string `yaml:"notes,omitempty"`
	// GroupTransitions is enforced by hack/tools/crd_compatibility.
	GroupTransitions []groupTransition   `yaml:"groupTransitions,omitempty"`
	Resources        []resourceInventory `yaml:"resources"`
}

type groupTransition struct {
	From      string `yaml:"from"`
	To        string `yaml:"to"`
	Release   string `yaml:"release"`
	Migration string `yaml:"migration"`
}

type resourceInventory struct {
	Kind     string           `yaml:"kind"`
	CRD      string           `yaml:"crd"`
	Version  string           `yaml:"version"`
	Defaults resourceDefaults `yaml:"defaults"`
	Notes    []string         `yaml:"notes,omitempty"`
	Rules    []inventoryRule  `yaml:"rules"`
}

type resourceDefaults struct {
	Spec   policyDefaults `yaml:"spec"`
	Status policyDefaults `yaml:"status"`
}

type policyDefaults struct {
	Mutability        string   `yaml:"mutability"`
	Migration         string   `yaml:"migration"`
	ModuleInteraction string   `yaml:"moduleInteraction"`
	Enforcement       []string `yaml:"enforcement"`
}

type inventoryRule struct {
	Path              string   `yaml:"path"`
	Classification    string   `yaml:"classification,omitempty"`
	Owner             string   `yaml:"owner,omitempty"`
	Mutability        string   `yaml:"mutability,omitempty"`
	Omission          string   `yaml:"omission,omitempty"`
	StableValues      []string `yaml:"stableValues,omitempty"`
	Migration         string   `yaml:"migration,omitempty"`
	ModuleInteraction string   `yaml:"moduleInteraction,omitempty"`
	Enforcement       []string `yaml:"enforcement,omitempty"`
	Rationale         string   `yaml:"rationale,omitempty"`
}

type schemaField struct {
	Path        string
	Type        string
	Required    bool
	Default     string
	Validation  string
	Description string
	SchemaRoot  bool
}

type canonicalValidationRule struct {
	Rule              string `json:"rule"`
	Message           string `json:"message,omitempty"`
	MessageExpression string `json:"messageExpression,omitempty"`
	FieldPath         string `json:"fieldPath,omitempty"`
	Reason            string `json:"reason,omitempty"`
	OptionalOldSelf   string `json:"optionalOldSelf,omitempty"`
}

type effectivePolicy struct {
	Classification    string
	Owner             string
	Mutability        string
	Omission          string
	StableValues      []string
	Migration         string
	ModuleInteraction string
	Enforcement       []string
	Rationale         string
	RulePath          string
}

type resolvedField struct {
	Resource string
	Field    schemaField
	Policy   effectivePolicy
}

type resourceReport struct {
	Kind   string
	Fields []resolvedField
	Counts map[string]int
}

type inventoryReport struct {
	Release        string
	Baseline       string
	DecisionStatus string
	Snapshot       string
	Resources      []resourceReport
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		fail(2, err)
	}

	report, err := buildReport(opts.InventoryPath)
	if err != nil {
		fail(1, err)
	}
	if opts.Update {
		if err := writeSnapshot(report.Snapshot, report); err != nil {
			fail(1, err)
		}
	} else if err := verifySnapshot(report.Snapshot, report); err != nil {
		fail(1, err)
	}

	switch opts.Format {
	case "summary":
		err = writeSummary(os.Stdout, report)
	case "markdown":
		err = writeMarkdown(os.Stdout, report)
	default:
		err = fmt.Errorf("unsupported format %q", opts.Format)
	}
	if err != nil {
		fail(1, fmt.Errorf("write %s report: %w", opts.Format, err))
	}
}

func parseOptions() (options, error) {
	var opts options
	flag.StringVar(&opts.InventoryPath, "inventory", defaultInventoryPath, "API stability inventory path")
	flag.StringVar(&opts.Format, "format", "summary", "Output format: summary or markdown")
	flag.BoolVar(&opts.Update, "update", false, "Update the resolved API stability snapshot")
	flag.Parse()

	if strings.TrimSpace(opts.InventoryPath) == "" {
		return options{}, fmt.Errorf("inventory path is required")
	}
	if opts.Format != "summary" && opts.Format != "markdown" {
		return options{}, fmt.Errorf("format must be summary or markdown")
	}
	return opts, nil
}

func fail(code int, err error) {
	fmt.Fprintf(os.Stderr, "api_inventory: %v\n", err)
	os.Exit(code)
}

func buildReport(path string) (inventoryReport, error) {
	inv, err := loadInventory(path)
	if err != nil {
		return inventoryReport{}, err
	}
	if err := validateInventoryHeader(inv); err != nil {
		return inventoryReport{}, err
	}

	report := inventoryReport{
		Release:        inv.Release,
		Baseline:       inv.Baseline,
		DecisionStatus: inv.DecisionStatus,
		Snapshot:       inv.Snapshot,
	}
	var validationErrors []string
	seenKinds := make(map[string]bool)
	for _, resource := range inv.Resources {
		if seenKinds[resource.Kind] {
			validationErrors = append(validationErrors, fmt.Sprintf("resource kind %s is declared more than once", resource.Kind))
			continue
		}
		seenKinds[resource.Kind] = true

		fields, err := loadSchemaFields(resource)
		if err != nil {
			validationErrors = append(validationErrors, err.Error())
			continue
		}
		resourceReport, errs := resolveResource(resource, fields)
		validationErrors = append(validationErrors, errs...)
		report.Resources = append(report.Resources, resourceReport)
	}
	if len(inv.Resources) == 0 {
		validationErrors = append(validationErrors, "resources must not be empty")
	}
	if len(validationErrors) > 0 {
		sort.Strings(validationErrors)
		return inventoryReport{}, fmt.Errorf(
			"inventory validation failed:\n- %s",
			strings.Join(validationErrors, "\n- "),
		)
	}

	sort.Slice(report.Resources, func(i, j int) bool {
		return report.Resources[i].Kind < report.Resources[j].Kind
	})
	return report, nil
}

func loadInventory(path string) (inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return inventory{}, fmt.Errorf("read inventory %s: %w", path, err)
	}

	var inv inventory
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&inv); err != nil {
		return inventory{}, fmt.Errorf("parse inventory %s: %w", path, err)
	}
	return inv, nil
}

func validateInventoryHeader(inv inventory) error {
	var errs []string
	if inv.SchemaVersion != 1 {
		errs = append(errs, fmt.Sprintf("schemaVersion must be 1, got %d", inv.SchemaVersion))
	}
	if strings.TrimSpace(inv.Release) == "" {
		errs = append(errs, "release is required")
	}
	if strings.TrimSpace(inv.Baseline) == "" {
		errs = append(errs, "baseline is required")
	}
	if inv.DecisionStatus != "proposed" && inv.DecisionStatus != "approved" {
		errs = append(errs, "decisionStatus must be proposed or approved")
	}
	if strings.TrimSpace(inv.Snapshot) == "" {
		errs = append(errs, "snapshot is required")
	}
	if len(errs) > 0 {
		return fmt.Errorf("inventory header validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func loadSchemaFields(resource resourceInventory) ([]schemaField, error) {
	data, err := os.ReadFile(resource.CRD)
	if err != nil {
		return nil, fmt.Errorf("%s: read CRD %s: %w", resource.Kind, resource.CRD, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := sigsyaml.Unmarshal(data, &crd); err != nil {
		return nil, fmt.Errorf("%s: parse CRD %s: %w", resource.Kind, resource.CRD, err)
	}
	if crd.Spec.Names.Kind != resource.Kind {
		return nil, fmt.Errorf(
			"%s: CRD %s declares kind %s",
			resource.Kind,
			resource.CRD,
			crd.Spec.Names.Kind,
		)
	}

	for _, version := range crd.Spec.Versions {
		if version.Name != resource.Version {
			continue
		}
		if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
			return nil, fmt.Errorf("%s: version %s has no OpenAPI schema", resource.Kind, resource.Version)
		}

		root := version.Schema.OpenAPIV3Schema
		var fields []schemaField
		rootRequired := stringSet(root.Required...)
		for _, prefix := range []string{"spec", "status"} {
			schema, ok := root.Properties[prefix]
			if !ok {
				return nil, fmt.Errorf("%s: version %s has no %s schema", resource.Kind, resource.Version, prefix)
			}
			fields = append(fields, newSchemaField(prefix, schema, rootRequired[prefix], true))
			fields = append(fields, collectSchemaFields(prefix, schema)...)
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
		return fields, nil
	}

	return nil, fmt.Errorf("%s: CRD %s has no version %s", resource.Kind, resource.CRD, resource.Version)
}

func collectSchemaFields(prefix string, schema apiextensionsv1.JSONSchemaProps) []schemaField {
	required := stringSet(schema.Required...)
	propertyNames := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		propertyNames = append(propertyNames, name)
	}
	sort.Strings(propertyNames)

	fields := make([]schemaField, 0, len(propertyNames))
	for _, name := range propertyNames {
		child := schema.Properties[name]
		path := prefix + "." + name
		fields = append(fields, collectSchemaNode(path, child, required[name])...)
	}

	if schema.Items != nil && schema.Items.Schema != nil {
		fields = append(fields, collectSchemaNode(prefix+"[]", *schema.Items.Schema, false)...)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		fields = append(fields, collectSchemaNode(prefix+".*", *schema.AdditionalProperties.Schema, false)...)
	}
	for index, composed := range schema.AllOf {
		path := fmt.Sprintf("%s.allOf[%d]", prefix, index)
		fields = append(fields, collectSchemaNode(path, composed, false)...)
	}
	for index, composed := range schema.AnyOf {
		path := fmt.Sprintf("%s.anyOf[%d]", prefix, index)
		fields = append(fields, collectSchemaNode(path, composed, false)...)
	}
	for index, composed := range schema.OneOf {
		path := fmt.Sprintf("%s.oneOf[%d]", prefix, index)
		fields = append(fields, collectSchemaNode(path, composed, false)...)
	}
	if schema.Not != nil {
		fields = append(fields, collectSchemaNode(prefix+".not", *schema.Not, false)...)
	}
	return fields
}

func collectSchemaNode(path string, schema apiextensionsv1.JSONSchemaProps, required bool) []schemaField {
	children := collectSchemaFields(path, schema)
	fields := make([]schemaField, 0, 1+len(children))
	fields = append(fields, newSchemaField(path, schema, required, false))
	return append(fields, children...)
}

func newSchemaField(path string, schema apiextensionsv1.JSONSchemaProps, required, schemaRoot bool) schemaField {
	return schemaField{
		Path:        path,
		Type:        schemaType(schema),
		Required:    required,
		Default:     schemaDefault(schema),
		Validation:  schemaValidation(schema),
		Description: schema.Description,
		SchemaRoot:  schemaRoot,
	}
}

func schemaType(schema apiextensionsv1.JSONSchemaProps) string {
	if schema.Type == "array" && schema.Items != nil && schema.Items.Schema != nil {
		itemType := schemaType(*schema.Items.Schema)
		if itemType != "" {
			return "array<" + itemType + ">"
		}
	}
	if schema.Type != "" {
		return schema.Type
	}
	if len(schema.AllOf) > 0 {
		return "allOf"
	}
	if len(schema.AnyOf) > 0 {
		return "anyOf"
	}
	if len(schema.OneOf) > 0 {
		return "oneOf"
	}
	return "unknown"
}

func schemaDefault(schema apiextensionsv1.JSONSchemaProps) string {
	if schema.Default == nil {
		return "-"
	}
	value := strings.TrimSpace(string(schema.Default.Raw))
	if value == "" {
		return "null"
	}
	return value
}

func schemaValidation(schema apiextensionsv1.JSONSchemaProps) string {
	var constraints []string
	if schema.Format != "" {
		constraints = append(constraints, "format="+schema.Format)
	}
	if schema.Pattern != "" {
		constraints = append(constraints, "pattern="+schema.Pattern)
	}
	if schema.Minimum != nil {
		constraints = append(constraints, "minimum="+formatFloat(*schema.Minimum))
	}
	if schema.ExclusiveMinimum {
		constraints = append(constraints, "exclusiveMinimum")
	}
	if schema.Maximum != nil {
		constraints = append(constraints, "maximum="+formatFloat(*schema.Maximum))
	}
	if schema.ExclusiveMaximum {
		constraints = append(constraints, "exclusiveMaximum")
	}
	if schema.MultipleOf != nil {
		constraints = append(constraints, "multipleOf="+formatFloat(*schema.MultipleOf))
	}
	if schema.MinLength != nil {
		constraints = append(constraints, fmt.Sprintf("minLength=%d", *schema.MinLength))
	}
	if schema.MaxLength != nil {
		constraints = append(constraints, fmt.Sprintf("maxLength=%d", *schema.MaxLength))
	}
	if schema.MinItems != nil {
		constraints = append(constraints, fmt.Sprintf("minItems=%d", *schema.MinItems))
	}
	if schema.MaxItems != nil {
		constraints = append(constraints, fmt.Sprintf("maxItems=%d", *schema.MaxItems))
	}
	if schema.UniqueItems {
		constraints = append(constraints, "uniqueItems")
	}
	if schema.MinProperties != nil {
		constraints = append(constraints, fmt.Sprintf("minProperties=%d", *schema.MinProperties))
	}
	if schema.MaxProperties != nil {
		constraints = append(constraints, fmt.Sprintf("maxProperties=%d", *schema.MaxProperties))
	}
	if len(schema.Enum) > 0 {
		values := make([]string, 0, len(schema.Enum))
		for _, value := range schema.Enum {
			values = append(values, strings.TrimSpace(string(value.Raw)))
		}
		constraints = append(constraints, "enum="+strings.Join(values, ","))
	}
	if len(schema.XValidations) > 0 {
		constraints = append(constraints, "cel="+canonicalCELValidation(schema.XValidations))
	}
	if schema.Nullable {
		constraints = append(constraints, "nullable")
	}
	if schema.AdditionalProperties != nil {
		constraints = append(
			constraints,
			"additionalProperties="+strconv.FormatBool(schema.AdditionalProperties.Allows),
		)
	}
	if schema.XPreserveUnknownFields != nil && *schema.XPreserveUnknownFields {
		constraints = append(constraints, "x-kubernetes-preserve-unknown-fields")
	}
	if schema.XEmbeddedResource {
		constraints = append(constraints, "x-kubernetes-embedded-resource")
	}
	if schema.XIntOrString {
		constraints = append(constraints, "x-kubernetes-int-or-string")
	}
	if schema.XListType != nil {
		constraints = append(constraints, "x-kubernetes-list-type="+*schema.XListType)
	}
	if len(schema.XListMapKeys) > 0 {
		keys := append([]string(nil), schema.XListMapKeys...)
		sort.Strings(keys)
		constraints = append(constraints, "x-kubernetes-list-map-keys="+strings.Join(keys, ","))
	}
	if schema.XMapType != nil {
		constraints = append(constraints, "x-kubernetes-map-type="+*schema.XMapType)
	}
	if len(constraints) == 0 {
		return "-"
	}
	return strings.Join(constraints, "; ")
}

func canonicalCELValidation(rules apiextensionsv1.ValidationRules) string {
	encoded := make([]string, 0, len(rules))
	for _, rule := range rules {
		item := canonicalValidationRule{
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
		data, _ := json.Marshal(item)
		encoded = append(encoded, string(data))
	}
	sort.Strings(encoded)
	return "[" + strings.Join(encoded, ",") + "]"
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func resolveResource(resource resourceInventory, fields []schemaField) (resourceReport, []string) {
	report := resourceReport{Kind: resource.Kind, Counts: make(map[string]int)}
	var errs []string

	if strings.TrimSpace(resource.Kind) == "" {
		errs = append(errs, "resource kind is required")
	}
	if strings.TrimSpace(resource.CRD) == "" {
		errs = append(errs, fmt.Sprintf("%s: crd path is required", resource.Kind))
	}
	if strings.TrimSpace(resource.Version) == "" {
		errs = append(errs, fmt.Sprintf("%s: version is required", resource.Kind))
	}

	rulesByPath := make(map[string]inventoryRule)
	for _, rule := range resource.Rules {
		if strings.TrimSpace(rule.Path) == "" {
			errs = append(errs, fmt.Sprintf("%s: rule path is required", resource.Kind))
			continue
		}
		if _, exists := rulesByPath[rule.Path]; exists {
			errs = append(errs, fmt.Sprintf("%s: duplicate rule for %s", resource.Kind, rule.Path))
			continue
		}
		rulesByPath[rule.Path] = rule

		stableValues := make(map[string]bool, len(rule.StableValues))
		for _, value := range rule.StableValues {
			if strings.TrimSpace(value) == "" {
				errs = append(errs, fmt.Sprintf("%s %s: stableValues must not contain empty values", resource.Kind, rule.Path))
				continue
			}
			if stableValues[value] {
				errs = append(errs, fmt.Sprintf("%s %s: duplicate stable value %q", resource.Kind, rule.Path, value))
				continue
			}
			stableValues[value] = true
		}
	}

	matchedRules := make(map[string]bool)
	for _, field := range fields {
		if field.SchemaRoot {
			report.Fields = append(report.Fields, resolvedField{
				Resource: resource.Kind,
				Field:    field,
				Policy: effectivePolicy{
					Classification: "schema-root",
					Owner:          "schema",
					Mutability:     "-",
					Enforcement:    []string{"crd-schema"},
					RulePath:       "schema-root",
				},
			})
			continue
		}
		policy, matched := resolvePolicy(resource, field.Path)
		for _, path := range matched {
			matchedRules[path] = true
		}
		fieldErrors := validateResolvedField(resource.Kind, field, policy, rulesByPath)
		errs = append(errs, fieldErrors...)
		report.Fields = append(report.Fields, resolvedField{
			Resource: resource.Kind,
			Field:    field,
			Policy:   policy,
		})
		if policy.Classification != "" {
			report.Counts[policy.Classification]++
		}
	}

	for _, rule := range resource.Rules {
		if !matchedRules[rule.Path] {
			errs = append(errs, fmt.Sprintf("%s: rule %s does not match a schema field", resource.Kind, rule.Path))
		}
	}
	return report, errs
}

func resolvePolicy(resource resourceInventory, path string) (effectivePolicy, []string) {
	defaults := resource.Defaults.Spec
	if strings.HasPrefix(path, "status.") {
		defaults = resource.Defaults.Status
	}
	policy := effectivePolicy{
		Mutability:        defaults.Mutability,
		Migration:         defaults.Migration,
		ModuleInteraction: defaults.ModuleInteraction,
		Enforcement:       defaults.Enforcement,
	}

	var matching []inventoryRule
	for _, rule := range resource.Rules {
		if ruleMatchesPath(rule.Path, path) {
			matching = append(matching, rule)
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		return len(matching[i].Path) < len(matching[j].Path)
	})

	matched := make([]string, 0, len(matching))
	for _, rule := range matching {
		matched = append(matched, rule.Path)
		if rule.Classification != "" {
			policy.Classification = rule.Classification
		}
		if rule.Owner != "" {
			policy.Owner = rule.Owner
		}
		if rule.Mutability != "" {
			policy.Mutability = rule.Mutability
		}
		if rule.Path == path {
			policy.Omission = rule.Omission
			policy.StableValues = append([]string(nil), rule.StableValues...)
			sort.Strings(policy.StableValues)
		}
		if rule.Migration != "" {
			policy.Migration = rule.Migration
		}
		if rule.ModuleInteraction != "" {
			policy.ModuleInteraction = rule.ModuleInteraction
		}
		if len(rule.Enforcement) > 0 {
			policy.Enforcement = rule.Enforcement
		}
		if rule.Rationale != "" {
			policy.Rationale = rule.Rationale
		}
		policy.RulePath = rule.Path
	}
	return policy, matched
}

func ruleMatchesPath(rulePath, fieldPath string) bool {
	if rulePath == fieldPath {
		return true
	}
	if !strings.HasPrefix(fieldPath, rulePath) || len(fieldPath) == len(rulePath) {
		return false
	}
	next := fieldPath[len(rulePath)]
	return next == '.' || next == '['
}

func validateResolvedField(
	kind string,
	field schemaField,
	policy effectivePolicy,
	rulesByPath map[string]inventoryRule,
) []string {
	var errs []string
	prefix := kind + " " + field.Path

	if isTopLevelField(field.Path) {
		if _, ok := rulesByPath[field.Path]; !ok {
			errs = append(errs, fmt.Sprintf("%s: top-level field requires an explicit rule", prefix))
		}
	}
	if policy.Classification == "" {
		errs = append(errs, fmt.Sprintf("%s: classification is required", prefix))
	} else if !allowedClassifications[policy.Classification] {
		errs = append(errs, fmt.Sprintf("%s: classification %q is not recognized", prefix, policy.Classification))
	} else if strings.HasPrefix(field.Path, "spec.") && !allowedSpecClassifications[policy.Classification] {
		errs = append(errs, fmt.Sprintf("%s: classification %q is not valid for spec", prefix, policy.Classification))
	} else if strings.HasPrefix(field.Path, "status.") && !allowedStatusClassifications[policy.Classification] {
		errs = append(errs, fmt.Sprintf("%s: classification %q is not valid for status", prefix, policy.Classification))
	}
	if strings.TrimSpace(policy.Owner) == "" {
		errs = append(errs, fmt.Sprintf("%s: owner is required", prefix))
	}
	if strings.TrimSpace(policy.Mutability) == "" {
		errs = append(errs, fmt.Sprintf("%s: mutability is required", prefix))
	} else if !allowedMutability[policy.Mutability] {
		errs = append(errs, fmt.Sprintf("%s: mutability %q is not recognized", prefix, policy.Mutability))
	}
	if strings.TrimSpace(policy.Migration) == "" {
		errs = append(errs, fmt.Sprintf("%s: migration policy is required", prefix))
	} else if !allowedMigration[policy.Migration] {
		errs = append(errs, fmt.Sprintf("%s: migration policy %q is not recognized", prefix, policy.Migration))
	}
	if strings.TrimSpace(policy.ModuleInteraction) == "" {
		errs = append(errs, fmt.Sprintf("%s: module interaction is required", prefix))
	} else if !allowedModuleInteractions[policy.ModuleInteraction] {
		errs = append(errs, fmt.Sprintf(
			"%s: module interaction %q is not recognized",
			prefix,
			policy.ModuleInteraction,
		))
	}
	if len(policy.Enforcement) == 0 {
		errs = append(errs, fmt.Sprintf("%s: at least one enforcement layer is required", prefix))
	} else {
		for _, layer := range policy.Enforcement {
			if !allowedEnforcementLayers[layer] {
				errs = append(errs, fmt.Sprintf("%s: enforcement layer %q is not recognized", prefix, layer))
			}
		}
	}
	if len(policy.StableValues) > 0 && policy.Classification != "stable-automation" {
		errs = append(errs, fmt.Sprintf("%s: stableValues require stable-automation classification", prefix))
	}
	if strings.Contains(field.Description, "Deprecated:") {
		rule, explicit := rulesByPath[field.Path]
		if !explicit || rule.Classification != "deprecated" {
			errs = append(errs, fmt.Sprintf("%s: schema deprecation requires an explicit deprecated rule", prefix))
		}
	}
	return errs
}

func isTopLevelField(path string) bool {
	return strings.Count(path, ".") == 1 && !strings.Contains(path, "[]")
}

func writeSummary(w io.Writer, report inventoryReport) error {
	if _, err := fmt.Fprintf(
		w,
		"API stability inventory: pass (release %s, baseline %s, decisions %s)\n",
		report.Release,
		report.Baseline,
		report.DecisionStatus,
	); err != nil {
		return err
	}
	for _, resource := range report.Resources {
		if _, err := fmt.Fprintf(w, "%s: %d schema nodes\n", resource.Kind, len(resource.Fields)); err != nil {
			return err
		}
		for _, classification := range classificationOrder {
			if count := resource.Counts[classification]; count > 0 {
				if _, err := fmt.Fprintf(w, "  %-20s %d\n", classification, count); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeSnapshot(path string, report inventoryReport) error {
	var output bytes.Buffer
	if err := renderSnapshot(&output, report); err != nil {
		return fmt.Errorf("render snapshot: %w", err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write snapshot %s: %w", path, err)
	}
	return nil
}

func verifySnapshot(path string, report inventoryReport) error {
	actual, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", path, err)
	}

	var expected bytes.Buffer
	if err := renderSnapshot(&expected, report); err != nil {
		return fmt.Errorf("render snapshot: %w", err)
	}
	if !bytes.Equal(actual, expected.Bytes()) {
		return fmt.Errorf(
			"snapshot %s is out of date; run `make update-api-stability-inventory` and review the resolved contract diff",
			path,
		)
	}
	return nil
}

func renderSnapshot(w io.Writer, report inventoryReport) error {
	if _, err := fmt.Fprintln(
		w,
		"resource\tpath\ttype\trequired\tdefault\tvalidation\tclassification\towner\tmutability\tenforcement\tmoduleInteraction\tmigration\trule\tpurpose\tdecision",
	); err != nil {
		return err
	}
	for _, resource := range report.Resources {
		for _, field := range resource.Fields {
			values := []string{
				field.Resource,
				field.Field.Path,
				field.Field.Type,
				strconv.FormatBool(field.Field.Required),
				field.Field.Default,
				field.Field.Validation,
				field.Policy.Classification,
				field.Policy.Owner,
				field.Policy.Mutability,
				strings.Join(field.Policy.Enforcement, ","),
				field.Policy.ModuleInteraction,
				field.Policy.Migration,
				field.Policy.RulePath,
				descriptionSummary(field.Field.Description),
				decisionSummary(field.Policy),
			}
			for index := range values {
				values[index] = sanitizeTSV(values[index])
			}
			if _, err := fmt.Fprintln(w, strings.Join(values, "\t")); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizeTSV(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(value))
	if value == "" {
		return "-"
	}
	return value
}

func writeMarkdown(w io.Writer, report inventoryReport) error {
	if _, err := fmt.Fprintln(w, "# API stability inventory"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		w,
		"\nRelease: `%s`  \nBaseline: `%s`  \nDecision status: `%s`\n",
		report.Release,
		report.Baseline,
		report.DecisionStatus,
	); err != nil {
		return err
	}

	for _, resource := range report.Resources {
		if _, err := fmt.Fprintf(w, "\n## %s\n\n", resource.Kind); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w,
			"| Path | Type | Omission/default | Validation | Stable values | Enforcement | Classification | Owner | Mutability | Module | Migration | Rule | Purpose | Decision |",
		); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w,
			"| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |",
		); err != nil {
			return err
		}
		for _, field := range resource.Fields {
			if _, err := fmt.Fprintf(
				w,
				"| `%s` | `%s` | %s | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s | %s |\n",
				escapeMarkdown(field.Field.Path),
				escapeMarkdown(field.Field.Type),
				escapeMarkdown(omissionSummary(field.Field, field.Policy)),
				escapeMarkdown(field.Field.Validation),
				escapeMarkdown(strings.Join(field.Policy.StableValues, ", ")),
				escapeMarkdown(strings.Join(field.Policy.Enforcement, ", ")),
				escapeMarkdown(field.Policy.Classification),
				escapeMarkdown(field.Policy.Owner),
				escapeMarkdown(field.Policy.Mutability),
				escapeMarkdown(field.Policy.ModuleInteraction),
				escapeMarkdown(field.Policy.Migration),
				escapeMarkdown(field.Policy.RulePath),
				escapeMarkdown(descriptionSummary(field.Field.Description)),
				escapeMarkdown(field.Policy.Rationale),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func omissionSummary(field schemaField, policy effectivePolicy) string {
	structural := "optional"
	if field.Required {
		structural = "required"
	} else if field.Default != "-" {
		structural = "optional; default=" + field.Default
	}
	if policy.Omission == "" {
		return structural
	}
	return structural + "; " + policy.Omission
}

func decisionSummary(policy effectivePolicy) string {
	var decisions []string
	if policy.Omission != "" {
		decisions = append(decisions, "omission: "+policy.Omission)
	}
	if len(policy.StableValues) > 0 {
		decisions = append(decisions, "stable values: "+strings.Join(policy.StableValues, ","))
	}
	if policy.Rationale != "" {
		decisions = append(decisions, policy.Rationale)
	}
	return strings.Join(decisions, "; ")
}

func descriptionSummary(description string) string {
	normalized := strings.Join(strings.Fields(description), " ")
	if normalized == "" {
		return "-"
	}
	if index := strings.Index(normalized, ". "); index >= 0 {
		return normalized[:index+1]
	}
	return normalized
}

func escapeMarkdown(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

func stringSet(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}
