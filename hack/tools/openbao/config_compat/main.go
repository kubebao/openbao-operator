package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/kubebao/openbao-operator/hack/tools/openbao/internal/configschema"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
)

type caseSpec struct {
	MinVersion  string            `json:"min_version,omitempty"`
	MaxVersion  string            `json:"max_version,omitempty"`
	Unsupported []string          `json:"unsupported_contains,omitempty"`
	Assertions  map[string]string `json:"assertions,omitempty"`
}

type negativeCase struct {
	File     string   `json:"file"`
	Contains []string `json:"contains"`
}

type manifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Versions      []string            `json:"versions"`
	VersionRoles  map[string]string   `json:"version_roles"`
	Fixtures      map[string]caseSpec `json:"fixtures"`
	NegativeCases []negativeCase      `json:"negative_cases"`
}

func main() {
	var root, version, moduleDir, output, schemaOutput string
	var commit string
	var listVersions, validateVersion bool
	flag.StringVar(&root, "root", ".", "Operator repository root")
	flag.StringVar(&version, "version", "", "OpenBao semantic version under test")
	flag.StringVar(&moduleDir, "upstream-module-dir", "", "Materialized upstream module")
	flag.StringVar(&output, "output", "", "Prepared parser matrix JSON")
	flag.StringVar(&schemaOutput, "schema-output", "", "Qualified upstream schema paths")
	flag.StringVar(&commit, "upstream-commit", "", "Release image's upstream Git commit")
	flag.BoolVar(&listVersions, "list-versions", false, "Print the default version matrix")
	flag.BoolVar(&validateVersion, "validate-version", false, "Validate a version before resolving its image")
	flag.Parse()
	if validateVersion {
		if _, err := platformsemver.Parse(version); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := prepare(root, version, moduleDir, output, schemaOutput, commit, listVersions); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadManifest(root string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(filepath.Join(root, "hack", "ci", "openbao-config-compat", "manifest.json"))
	if err != nil {
		return m, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, err
	}
	if m.SchemaVersion != 1 || len(m.Versions) == 0 || len(m.Fixtures) == 0 || len(m.NegativeCases) == 0 {
		return m, fmt.Errorf("manifest requires schema_version 1 and nonempty versions, fixtures, and negative_cases")
	}
	for _, version := range m.Versions {
		if _, err := platformsemver.Parse(version); err != nil {
			return m, err
		}
		if m.VersionRoles[version] == "" {
			return m, fmt.Errorf("version %s has no compatibility role", version)
		}
	}
	for name, spec := range m.Fixtures {
		accepted := false
		for _, version := range m.Versions {
			current, err := platformsemver.Parse(version)
			if err != nil {
				return m, err
			}
			allowed, err := versionInRange(spec, current)
			if err != nil {
				return m, fmt.Errorf("fixture %s: %w", name, err)
			}
			accepted = accepted || allowed
		}
		if !accepted {
			return m, fmt.Errorf("fixture %s has no positive case in the default version matrix", name)
		}
	}
	return m, nil
}

func prepare(root, version, moduleDir, output, schemaOutput, commit string, listVersions bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	m, err := loadManifest(root)
	if err != nil {
		return err
	}
	if listVersions {
		fmt.Println(strings.Join(m.Versions, " "))
		return nil
	}
	current, err := platformsemver.Parse(version)
	if err != nil {
		return err
	}
	if moduleDir == "" || output == "" || schemaOutput == "" {
		return fmt.Errorf("-upstream-module-dir, -output, and -schema-output are required")
	}
	keys, err := configschema.Upstream(moduleDir)
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(root, "internal", "adapter", "config", "testdata", "*.hcl"))
	if err != nil {
		return err
	}
	if len(files) != len(m.Fixtures) {
		return fmt.Errorf("manifest has %d fixtures but testdata has %d HCL files", len(m.Fixtures), len(files))
	}
	role := m.VersionRoles[version]
	if role == "" {
		role = "custom-version"
	}
	matrix := preparedMatrix{Version: version, Commit: commit, Role: role}
	covered := configschema.Keys{}
	for _, file := range files {
		spec, ok := m.Fixtures[filepath.Base(file)]
		if !ok {
			return fmt.Errorf("unlisted HCL fixture %s", file)
		}
		test, paths, err := prepareFixture(file, spec, current, keys)
		if err != nil {
			return err
		}
		matrix.Cases = append(matrix.Cases, test)
		for path := range paths {
			covered[path] = true
		}
	}
	operatorKeys, err := configschema.Operator(root)
	if err != nil {
		return err
	}
	var missing []string
	for path := range operatorKeys {
		if !covered[path] {
			missing = append(missing, path)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		return fmt.Errorf("operator paths missing from compatibility fixtures: %s", strings.Join(missing, ", "))
	}
	for _, negative := range m.NegativeCases {
		if len(negative.Contains) == 0 {
			return fmt.Errorf("negative case %s needs an expected failure", negative.File)
		}
		file := filepath.Join(root, negative.File)
		paths, _, err := fixturePaths(file)
		if err != nil {
			return err
		}
		matrix.Cases = append(matrix.Cases, preparedCase{File: file, WantFailure: true,
			ErrorContains: negative.Contains, Problems: schemaProblems(paths, keys)})
	}
	data, err := json.MarshalIndent(matrix, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(schemaOutput, []byte(strings.Join(keys.Sorted(), "\n")+"\n"), 0o600)
}

func prepareFixture(
	file string, spec caseSpec, current *platformsemver.Version, keys configschema.Keys,
) (preparedCase, configschema.Keys, error) {
	paths, body, err := fixturePaths(file)
	if err != nil {
		return preparedCase{}, nil, err
	}
	test := preparedCase{File: file, Assertions: map[string]string{}, ErrorContains: spec.Unsupported,
		Problems: schemaProblems(paths, keys)}
	allowed, err := versionInRange(spec, current)
	if err != nil {
		return test, nil, err
	}
	test.WantFailure = !allowed
	if test.WantFailure && len(test.ErrorContains) == 0 {
		return test, nil, fmt.Errorf("%s needs unsupported_contains", file)
	}
	counts := map[string]int{}
	fields := map[string]string{
		"listener": "Listeners", "seal": "Seals", "audit": "Audits",
		"plugin": "Plugins", "initialize": "Initialization",
	}
	for _, block := range body.Blocks() {
		if field := fields[block.Type()]; field != "" {
			counts[field]++
		}
	}
	for field, count := range counts {
		test.Assertions[field+".#"] = fmt.Sprint(count)
	}
	for path, value := range spec.Assertions {
		test.Assertions[path] = value
	}
	if current.Major() > 2 || (current.Major() == 2 && current.Minor() >= 7) {
		test.Problems = append(test.Problems, removedSealProblems(body)...)
	}
	return test, paths, nil
}

func versionInRange(spec caseSpec, current *platformsemver.Version) (bool, error) {
	for _, bound := range []struct {
		value   string
		minimum bool
	}{{spec.MinVersion, true}, {spec.MaxVersion, false}} {
		if bound.value == "" {
			continue
		}
		v, err := platformsemver.Parse(bound.value)
		if err != nil {
			return false, err
		}
		if (bound.minimum && current.Compare(v) < 0) || (!bound.minimum && current.Compare(v) >= 0) {
			return false, nil
		}
	}
	return true, nil
}

func removedSealProblems(body *hclwrite.Body) []string {
	var problems []string
	for _, block := range body.Blocks() {
		if block.Type() != "seal" || len(block.Labels()) != 1 {
			continue
		}
		seal := block.Labels()[0]
		if !slices.Contains([]string{"awskms", "azurekeyvault", "gcpckms", "ocikms", "pkcs11"}, seal) {
			continue
		}
		found := false
		for _, plugin := range body.Blocks() {
			if plugin.Type() == "plugin" && slices.Equal(plugin.Labels(), []string{"kms", seal}) {
				found = true
			}
		}
		if !found {
			problems = append(problems, "seal "+seal+" requires an external KMS plugin")
		}
	}
	return problems
}

func fixturePaths(file string) (configschema.Keys, *hclwrite.Body, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	f, diagnostics := hclwrite.ParseConfig(data, file, hcl.InitialPos)
	if diagnostics.HasErrors() {
		return nil, nil, fmt.Errorf("parse fixture %s: %s", file, diagnostics.Error())
	}
	keys := configschema.Keys{}
	collectPaths(keys, "", f.Body())
	return keys, f.Body(), nil
}

func collectPaths(keys configschema.Keys, prefix string, body *hclwrite.Body) {
	for name := range body.Attributes() {
		keys[prefix+name] = true
	}
	for _, block := range body.Blocks() {
		keys[prefix+block.Type()] = true
		collectPaths(keys, prefix+block.Type()+".", block.Body())
	}
}

func schemaProblems(paths, upstream configschema.Keys) []string {
	var problems []string
	for _, path := range paths.Sorted() {
		if !upstream[path] && !configschema.Opaque(path) {
			problems = append(problems, "unsupported config path "+path)
		}
	}
	return problems
}
