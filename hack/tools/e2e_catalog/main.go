package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultGinkgoPath = "bin/ginkgo"
	defaultInputDir   = "test/e2e"
	defaultOutputDir  = "test/e2e/catalog"
)

type options struct {
	GinkgoPath string
	InputDir   string
	OutputDir  string
}

type outlineNode struct {
	Name    string        `json:"name"`
	Text    string        `json:"text"`
	Spec    bool          `json:"spec"`
	Focused bool          `json:"focused"`
	Pending bool          `json:"pending"`
	Labels  []string      `json:"labels"`
	Nodes   []outlineNode `json:"nodes"`
}

type testCase struct {
	ID           string   `json:"id"`
	GeneratedID  string   `json:"generatedId"`
	CaseLabel    string   `json:"caseLabel,omitempty"`
	Coverage     []string `json:"coverage,omitempty"`
	File         string   `json:"file"`
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Path         []string `json:"path"`
	Labels       []string `json:"labels"`
	DomainLabels []string `json:"domainLabels,omitempty"`
	Steps        []string `json:"steps"`
	Focused      bool     `json:"focused"`
	Pending      bool     `json:"pending"`
	SuiteFile    string   `json:"suiteFile"`
	SuiteTitle   string   `json:"suiteTitle"`
}

type suiteCatalog struct {
	SourceFile string
	OutputFile string
	Title      string
	Labels     []string
	Cases      []testCase
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_catalog: %v\n", err)
		os.Exit(2)
	}

	cases, err := collectCases(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_catalog: %v\n", err)
		os.Exit(1)
	}

	suites := groupSuites(cases)
	if err := writeCatalog(opts.OutputDir, cases, suites); err != nil {
		fmt.Fprintf(os.Stderr, "e2e_catalog: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Wrote %d cases to %s\n", len(cases), opts.OutputDir)
}

func parseOptions() (options, error) {
	var opts options

	flag.StringVar(&opts.GinkgoPath, "ginkgo", defaultGinkgoPath, "path to the ginkgo binary")
	flag.StringVar(&opts.InputDir, "input-dir", defaultInputDir, "directory containing *_test.go E2E files")
	flag.StringVar(&opts.OutputDir, "output-dir", defaultOutputDir, "directory for generated catalog output")
	flag.Parse()

	if strings.TrimSpace(opts.GinkgoPath) == "" {
		return options{}, fmt.Errorf("ginkgo path is required")
	}
	if strings.TrimSpace(opts.InputDir) == "" {
		return options{}, fmt.Errorf("input directory is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return options{}, fmt.Errorf("output directory is required")
	}
	return opts, nil
}

func collectCases(opts options) ([]testCase, error) {
	pattern := filepath.Join(opts.InputDir, "*_test.go")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *_test.go files found under %s", opts.InputDir)
	}
	sort.Strings(files)

	var cases []testCase
	for _, file := range files {
		ginkgoFile, err := importsGinkgo(file)
		if err != nil {
			return nil, err
		}
		if !ginkgoFile {
			continue
		}
		outline, err := outlineFile(opts.GinkgoPath, file)
		if err != nil {
			return nil, err
		}
		cases = append(cases, flattenSpecs(outline, file, nil, nil)...)
	}

	sort.Slice(cases, func(i, j int) bool {
		if cases[i].File != cases[j].File {
			return cases[i].File < cases[j].File
		}
		return strings.Join(cases[i].Path, "\x00") < strings.Join(cases[j].Path, "\x00")
	})

	return cases, nil
}

// Regular Go tests exercise fixture contracts without registering E2E specs.
func importsGinkgo(path string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return false, fmt.Errorf("parse test imports %s: %w", path, err)
	}
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			return false, err
		}
		if path == "github.com/onsi/ginkgo/v2" {
			return true, nil
		}
	}
	return false, nil
}

func outlineFile(ginkgoPath, filePath string) ([]outlineNode, error) {
	// The catalog calls the validated Ginkgo binary discovered for local repo tooling.
	// #nosec G204 -- controlled tool invocation.
	// nosemgrep
	cmd := exec.Command(ginkgoPath, "outline", "--format=json", filePath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("outline %s: %s", filePath, msg)
	}

	var nodes []outlineNode
	if err := json.Unmarshal(stdout.Bytes(), &nodes); err != nil {
		return nil, fmt.Errorf("parse outline json for %s: %w", filePath, err)
	}
	return nodes, nil
}

func flattenSpecs(nodes []outlineNode, filePath string, parents, inheritedLabels []string) []testCase {
	var cases []testCase

	for _, node := range nodes {
		nodeText := strings.TrimSpace(node.Text)
		labels := mergeLabels(inheritedLabels, node.Labels)

		if node.Spec {
			path := append(append([]string{}, parents...), nodeText)
			generatedID := buildCaseID(filePath, path)
			caseLabel := firstPrefixedLabel(labels, "case:")
			coverage := prefixedLabels(labels, "covers:")
			domainLabels := filterLabels(labels, "case:", "covers:")
			id := generatedID
			if caseLabel != "" {
				id = caseLabel
			}
			cases = append(cases, testCase{
				ID:           id,
				GeneratedID:  generatedID,
				CaseLabel:    caseLabel,
				Coverage:     coverage,
				File:         filepath.ToSlash(filePath),
				Type:         node.Name,
				Name:         nodeText,
				Path:         path,
				Labels:       labels,
				DomainLabels: domainLabels,
				Steps:        collectSteps(node),
				Focused:      node.Focused,
				Pending:      node.Pending,
			})
			continue
		}

		nextParents := parents
		if node.Name != "By" && nodeText != "" {
			nextParents = append(append([]string{}, parents...), nodeText)
		}
		cases = append(cases, flattenSpecs(node.Nodes, filePath, nextParents, labels)...)
	}

	return cases
}

func mergeLabels(parent, current []string) []string {
	merged := make([]string, 0, len(parent)+len(current))
	for _, label := range append(append([]string{}, parent...), current...) {
		label = strings.TrimSpace(label)
		if label == "" || contains(merged, label) {
			continue
		}
		merged = append(merged, label)
	}
	return merged
}

func collectSteps(node outlineNode) []string {
	var steps []string
	for _, child := range node.Nodes {
		if child.Name == "By" {
			if text, ok := normalizeStep(child.Text); ok {
				steps = append(steps, text)
			}
		}
		steps = append(steps, collectSteps(child)...)
	}
	return steps
}

func normalizeStep(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", false
	}
	if strings.EqualFold(text, "undefined") {
		return "", false
	}
	return text, true
}

func groupSuites(cases []testCase) []suiteCatalog {
	byFile := map[string][]testCase{}
	for _, tc := range cases {
		byFile[tc.File] = append(byFile[tc.File], tc)
	}

	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)

	suites := make([]suiteCatalog, 0, len(files))
	for _, file := range files {
		fileCases := byFile[file]
		title := suiteTitle(fileCases)
		suiteName := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)) + ".md"
		outputFile := filepath.ToSlash(filepath.Join("suites", suiteName))
		labels := aggregateLabels(fileCases)
		for i := range fileCases {
			fileCases[i].SuiteFile = outputFile
			fileCases[i].SuiteTitle = title
		}
		suites = append(suites, suiteCatalog{
			SourceFile: file,
			OutputFile: outputFile,
			Title:      title,
			Labels:     labels,
			Cases:      fileCases,
		})
	}
	return suites
}

func suiteTitle(cases []testCase) string {
	if len(cases) == 0 {
		return ""
	}
	first := cases[0].Path
	if len(first) > 0 && strings.TrimSpace(first[0]) != "" {
		return first[0]
	}
	return strings.TrimSuffix(filepath.Base(cases[0].File), filepath.Ext(cases[0].File))
}

func aggregateLabels(cases []testCase) []string {
	var labels []string
	for _, tc := range cases {
		labels = mergeLabels(labels, tc.DomainLabels)
	}
	return labels
}

func writeCatalog(outputDir string, cases []testCase, suites []suiteCatalog) error {
	outputDir = filepath.Clean(outputDir)
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("reset output directory %s: %w", outputDir, err)
	}
	suitesDir := filepath.Join(outputDir, "suites")
	if err := os.MkdirAll(suitesDir, 0o755); err != nil {
		return fmt.Errorf("create output directories: %w", err)
	}

	jsonPath := filepath.Join(outputDir, "cases.json")
	data, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cases json: %w", err)
	}
	if err := os.WriteFile(jsonPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", jsonPath, err)
	}

	readmePath := filepath.Join(outputDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(renderIndexMarkdown(cases, suites)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", readmePath, err)
	}

	for _, suite := range suites {
		path := filepath.Join(outputDir, filepath.FromSlash(suite.OutputFile))
		if err := os.WriteFile(path, []byte(renderSuiteMarkdown(suite)), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	return nil
}

func renderIndexMarkdown(cases []testCase, suites []suiteCatalog) string {
	var b strings.Builder
	b.WriteString("# E2E Case Catalog\n\n")
	b.WriteString("Generated from `ginkgo outline` for the files under `test/e2e/`.\n\n")
	b.WriteString("Notes:\n")
	b.WriteString(
		"- Suite and spec inventory comes from `ginkgo outline`; " +
			"`case:` and `covers:` labels are the stable tracking fields.\n",
	)
	b.WriteString(
		"- `steps` are optional recorded checkpoints derived from literal `By(...)` text that `ginkgo outline` can see.\n",
	)
	b.WriteString("- Missing checkpoints do not imply missing coverage.\n")
	b.WriteString(
		"- `case:` labels become the primary catalog IDs when present; otherwise a generated fallback ID is used.\n",
	)
	b.WriteString("- Use `cases.json` for automation; use the suite pages for human review.\n\n")
	b.WriteString("## Summary\n\n")
	fmt.Fprintf(&b, "- Files: `%d`\n", len(suites))
	fmt.Fprintf(&b, "- Specs: `%d`\n", len(cases))
	fmt.Fprintf(&b, "- Explicit case IDs: `%d`\n", countTracked(cases))
	fmt.Fprintf(&b, "- Coverage tags: `%d`\n\n", len(coverageCounts(cases)))
	b.WriteString("## Suites\n\n")
	b.WriteString("| Suite | Cases | Tracked | Pending | Labels | Source |\n")
	b.WriteString("| --- | ---: | ---: | ---: | --- | --- |\n")
	for _, suite := range suites {
		fmt.Fprintf(
			&b,
			"| [%s](%s) | %d | %d | %d | %s | `%s` |\n",
			escapeTable(suite.Title),
			suite.OutputFile,
			len(suite.Cases),
			countTracked(suite.Cases),
			countPending(suite.Cases),
			labelsCell(suite.Labels),
			suite.SourceFile,
		)
	}
	b.WriteString("\n## Coverage Tags\n\n")
	b.WriteString("| Coverage | Cases |\n")
	b.WriteString("| --- | ---: |\n")
	for _, row := range sortedCoverageRows(cases) {
		fmt.Fprintf(&b, "| `%s` | %d |\n", row.Name, row.Count)
	}
	return b.String()
}

func renderSuiteMarkdown(suite suiteCatalog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", suite.Title)
	fmt.Fprintf(&b, "Source: `%s`\n\n", suite.SourceFile)
	b.WriteString(
		"Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.\n\n",
	)
	b.WriteString("## Cases\n\n")
	b.WriteString("| Case ID | Spec | State | Covers | Labels |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, tc := range suite.Cases {
		fmt.Fprintf(
			&b,
			"| `%s` | %s | %s | %s | %s |\n",
			tc.ID,
			escapeTable(tc.Name),
			caseState(tc),
			labelsCell(tc.Coverage),
			labelsCell(tc.DomainLabels),
		)
	}

	for _, tc := range suite.Cases {
		fmt.Fprintf(&b, "\n## `%s`\n\n", tc.ID)
		fmt.Fprintf(&b, "Path: `%s`\n\n", strings.Join(tc.Path, " > "))
		fmt.Fprintf(&b, "State: `%s`\n\n", caseState(tc))
		if tc.GeneratedID != "" && tc.GeneratedID != tc.ID {
			fmt.Fprintf(&b, "Generated fallback ID: `%s`\n\n", tc.GeneratedID)
		}
		fmt.Fprintf(&b, "Covers: %s\n\n", labelsList(tc.Coverage))
		fmt.Fprintf(&b, "Labels: %s\n\n", labelsList(tc.DomainLabels))
		if len(tc.Steps) > 0 {
			b.WriteString("Recorded checkpoints:\n")
			for _, step := range tc.Steps {
				fmt.Fprintf(&b, "- %s\n", step)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func labelsCell(labels []string) string {
	if len(labels) == 0 {
		return "_none_"
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf("`%s`", label))
	}
	return strings.Join(parts, ", ")
}

func labelsList(labels []string) string {
	if len(labels) == 0 {
		return "_none_"
	}
	return strings.Join(wrapLabels(labels), ", ")
}

func buildCaseID(filePath string, path []string) string {
	fileSlug := normalizeSlug([]string{strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))})
	fileSlug = strings.TrimSuffix(fileSlug, "-test")
	specSlug := ""
	if len(path) > 0 {
		specSlug = shortenSlug(normalizeSlug([]string{path[len(path)-1]}), 6)
	}
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(filepath.ToSlash(filePath)))
	_, _ = sum.Write([]byte{0})
	_, _ = sum.Write([]byte(strings.Join(path, "\x00")))

	switch {
	case fileSlug == "" && specSlug == "":
		return fmt.Sprintf("case-%08x", sum.Sum32())
	case specSlug == "":
		return fmt.Sprintf("%s-%08x", fileSlug, sum.Sum32())
	default:
		return fmt.Sprintf("%s-%s-%08x", fileSlug, specSlug, sum.Sum32())
	}
}

func countPending(cases []testCase) int {
	total := 0
	for _, tc := range cases {
		if tc.Pending {
			total++
		}
	}
	return total
}

func countTracked(cases []testCase) int {
	total := 0
	for _, tc := range cases {
		if tc.CaseLabel != "" {
			total++
		}
	}
	return total
}

func caseState(tc testCase) string {
	switch {
	case tc.Pending:
		return "pending"
	case tc.Focused:
		return "focused"
	default:
		return "active"
	}
}

func wrapLabels(labels []string) []string {
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf("`%s`", label))
	}
	return parts
}

func escapeTable(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return value
}

type coverageRow struct {
	Name  string
	Count int
}

func coverageCounts(cases []testCase) map[string]int {
	counts := map[string]int{}
	for _, tc := range cases {
		for _, coverage := range tc.Coverage {
			counts[coverage]++
		}
	}
	return counts
}

func sortedCoverageRows(cases []testCase) []coverageRow {
	counts := coverageCounts(cases)
	rows := make([]coverageRow, 0, len(counts))
	for name, count := range counts {
		rows = append(rows, coverageRow{Name: name, Count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func firstPrefixedLabel(labels []string, prefix string) string {
	for _, label := range labels {
		if value, ok := trimLabelPrefix(label, prefix); ok {
			return value
		}
	}
	return ""
}

func prefixedLabels(labels []string, prefix string) []string {
	var values []string
	for _, label := range labels {
		if value, ok := trimLabelPrefix(label, prefix); ok && !contains(values, value) {
			values = append(values, value)
		}
	}
	return values
}

func filterLabels(labels []string, prefixes ...string) []string {
	var filtered []string
	for _, label := range labels {
		skip := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(label, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, label)
		}
	}
	return filtered
}

func trimLabelPrefix(label, prefix string) (string, bool) {
	if !strings.HasPrefix(label, prefix) {
		return "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(label, prefix))
	if value == "" {
		return "", false
	}
	return value, true
}

func normalizeSlug(parts []string) string {
	var b strings.Builder
	writeDash := false
	for _, part := range parts {
		for _, r := range strings.ToLower(strings.TrimSpace(part)) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
				writeDash = true
			case writeDash:
				b.WriteRune('-')
				writeDash = false
			}
		}
		if writeDash {
			b.WriteRune('-')
			writeDash = false
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortenSlug(slug string, maxParts int) string {
	if maxParts <= 0 || slug == "" {
		return slug
	}
	parts := strings.Split(slug, "-")
	if len(parts) <= maxParts {
		return slug
	}
	return strings.Join(parts[:maxParts], "-")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
