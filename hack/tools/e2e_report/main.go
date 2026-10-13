package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2/types"
	"gopkg.in/yaml.v3"
)

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

type options struct {
	Reports             stringList
	MarkdownOut         string
	JSONOut             string
	TopSpecs            int
	Lane                string
	Selector            string
	KubernetesVersion   string
	OpenBAOVersion      string
	ManifestPath        string
	SuiteBudgets        map[string]runtimeBudget
	FailOnSelectedSkips bool
	ExpectedCasesPath   string
}

type summary struct {
	Lane                       string          `json:"lane,omitempty"`
	Selector                   string          `json:"selector,omitempty"`
	KubernetesVersion          string          `json:"kubernetesVersion,omitempty"`
	OpenBAOVersion             string          `json:"openbaoVersion,omitempty"`
	ReportCount                int             `json:"reportCount"`
	SuiteSucceeded             bool            `json:"suiteSucceeded"`
	TotalSpecs                 int             `json:"totalSpecs"`
	SpecsThatWillRun           int             `json:"specsThatWillRun"`
	SpecReports                int             `json:"specReports"`
	Passed                     int             `json:"passed"`
	Failed                     int             `json:"failed"`
	Skipped                    int             `json:"skipped"`
	Pending                    int             `json:"pending"`
	Other                      int             `json:"other"`
	SuiteNodes                 int             `json:"suiteNodes"`
	SuiteNodePassed            int             `json:"suiteNodePassed"`
	SuiteNodeFailed            int             `json:"suiteNodeFailed"`
	SuiteNodeSkipped           int             `json:"suiteNodeSkipped"`
	SuiteNodePending           int             `json:"suiteNodePending"`
	SuiteNodeOther             int             `json:"suiteNodeOther"`
	RunTime                    string          `json:"runTime"`
	RunTimeSeconds             float64         `json:"runTimeSeconds"`
	SpecialSuiteFailureReasons []string        `json:"specialSuiteFailureReasons,omitempty"`
	SlowestSpecs               []specSummary   `json:"slowestSpecs,omitempty"`
	Failures                   []specSummary   `json:"failures,omitempty"`
	SelectedSkips              []specSummary   `json:"selectedSkips,omitempty"`
	Aggregates                 aggregates      `json:"aggregates,omitempty"`
	BudgetWarnings             []budgetWarning `json:"budgetWarnings,omitempty"`
	ExpectedCaseIDs            []string        `json:"expectedCaseIds,omitempty"`
	PassedCaseIDs              []string        `json:"passedCaseIds,omitempty"`
	SelectionErrors            []string        `json:"selectionErrors,omitempty"`
}

type specSummary struct {
	Name           string   `json:"name"`
	State          string   `json:"state"`
	RunTime        string   `json:"runTime"`
	RunTimeSeconds float64  `json:"runTimeSeconds"`
	Location       string   `json:"location,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	FailureMessage string   `json:"failureMessage,omitempty"`
}

type aggregates struct {
	ByLane  []aggregateSummary `json:"byLane,omitempty"`
	ByFile  []aggregateSummary `json:"byFile,omitempty"`
	ByLabel []aggregateSummary `json:"byLabel,omitempty"`
}

type aggregateSummary struct {
	Name           string  `json:"name"`
	Specs          int     `json:"specs"`
	Passed         int     `json:"passed"`
	Failed         int     `json:"failed"`
	Skipped        int     `json:"skipped"`
	Pending        int     `json:"pending"`
	Other          int     `json:"other"`
	RunTime        string  `json:"runTime"`
	RunTimeSeconds float64 `json:"runTimeSeconds"`
	Budget         string  `json:"budget,omitempty"`
	BudgetSeconds  float64 `json:"budgetSeconds,omitempty"`
	BudgetRatio    float64 `json:"budgetRatio,omitempty"`
	SuiteID        string  `json:"suiteId,omitempty"`
}

type budgetWarning struct {
	Scope          string  `json:"scope"`
	Name           string  `json:"name"`
	SuiteID        string  `json:"suiteId,omitempty"`
	RunTime        string  `json:"runTime"`
	RunTimeSeconds float64 `json:"runTimeSeconds"`
	Budget         string  `json:"budget"`
	BudgetSeconds  float64 `json:"budgetSeconds"`
	BudgetRatio    float64 `json:"budgetRatio"`
	Message        string  `json:"message"`
}

type runtimeBudget struct {
	SuiteID  string
	Title    string
	Observed time.Duration
	Budget   time.Duration
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_report: %v\n", err)
		os.Exit(2)
	}

	reports, err := loadReports(opts.Reports)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_report: %v\n", err)
		os.Exit(1)
	}

	if opts.ManifestPath != "" {
		budgets, err := loadManifestBudgets(opts.ManifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e_report: %v\n", err)
			os.Exit(1)
		}
		opts.SuiteBudgets = budgets
	}

	s := buildSummary(reports, opts)
	if opts.ExpectedCasesPath != "" {
		if err := validateSelection(&s, reports, opts); err != nil {
			fmt.Fprintf(os.Stderr, "e2e_report: %v\n", err)
			os.Exit(1)
		}
	}

	if opts.JSONOut != "" {
		if err := writeJSON(opts.JSONOut, s); err != nil {
			fmt.Fprintf(os.Stderr, "e2e_report: %v\n", err)
			os.Exit(1)
		}
	}

	markdown := formatMarkdown(s)
	if opts.MarkdownOut != "" {
		if err := writeText(opts.MarkdownOut, markdown); err != nil {
			fmt.Fprintf(os.Stderr, "e2e_report: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Print(markdown)
	if len(s.SelectionErrors) > 0 {
		fmt.Fprintf(os.Stderr, "e2e_report: scenario validation failed: %s\n", strings.Join(s.SelectionErrors, "; "))
		os.Exit(1)
	}
	if opts.FailOnSelectedSkips && len(s.SelectedSkips) > 0 {
		fmt.Fprintf(os.Stderr, "e2e_report: %d selected specs were skipped\n", len(s.SelectedSkips))
		os.Exit(1)
	}
}

func parseOptions() (options, error) {
	var opts options
	flag.Var(&opts.Reports, "json-report", "Ginkgo JSON report path. May be passed more than once.")
	flag.StringVar(&opts.MarkdownOut, "markdown-out", "", "optional path for Markdown summary output")
	flag.StringVar(&opts.JSONOut, "json-out", "", "optional path for machine-readable summary output")
	flag.IntVar(&opts.TopSpecs, "top", 10, "number of slowest specs to include")
	flag.StringVar(&opts.Lane, "lane", "", "CI lane name")
	flag.StringVar(&opts.Selector, "selector", "", "Ginkgo label selector")
	flag.StringVar(&opts.KubernetesVersion, "kubernetes-version", "", "Kubernetes node image or version")
	flag.StringVar(&opts.OpenBAOVersion, "openbao-version", "", "OpenBao image or version")
	flag.StringVar(&opts.ManifestPath, "manifest", "", "optional E2E suite manifest for runtime budget warnings")
	flag.StringVar(&opts.ExpectedCasesPath, "expected-cases", "", "JSON array of scenario IDs that must pass exactly once")
	flag.BoolVar(
		&opts.FailOnSelectedSkips,
		"fail-on-selected-skips",
		false,
		"exit non-zero when selected specs are skipped",
	)
	flag.Parse()

	if len(opts.Reports) == 0 {
		return options{}, fmt.Errorf("at least one --json-report path is required")
	}
	if opts.TopSpecs < 0 {
		return options{}, fmt.Errorf("--top must be >= 0")
	}
	return opts, nil
}

func loadReports(paths []string) ([]types.Report, error) {
	var reports []types.Report
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read report %s: %w", path, err)
		}

		var decoded []types.Report
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, fmt.Errorf("parse report %s: %w", path, err)
		}
		reports = append(reports, decoded...)
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no reports decoded")
	}
	return reports, nil
}

func loadManifestBudgets(path string) (map[string]runtimeBudget, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var manifest struct {
		Suites []struct {
			ID      string   `yaml:"id"`
			Title   string   `yaml:"title"`
			Files   []string `yaml:"files"`
			Runtime struct {
				Observed string `yaml:"observed"`
				Budget   string `yaml:"budget"`
			} `yaml:"runtime"`
		} `yaml:"suites"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}

	budgets := map[string]runtimeBudget{}
	for _, suite := range manifest.Suites {
		if strings.TrimSpace(suite.Runtime.Budget) == "" {
			continue
		}
		observed, err := parseOptionalDuration(suite.Runtime.Observed)
		if err != nil {
			return nil, fmt.Errorf("suite %s runtime.observed: %w", suite.ID, err)
		}
		budget, err := time.ParseDuration(suite.Runtime.Budget)
		if err != nil {
			return nil, fmt.Errorf("suite %s runtime.budget: %w", suite.ID, err)
		}
		if budget <= 0 {
			return nil, fmt.Errorf("suite %s runtime.budget must be positive", suite.ID)
		}
		for _, file := range suite.Files {
			file = normalizeReportFile(file)
			if file == "" || file == "." {
				continue
			}
			budgets[file] = runtimeBudget{
				SuiteID:  suite.ID,
				Title:    suite.Title,
				Observed: observed,
				Budget:   budget,
			}
		}
	}
	return budgets, nil
}

func parseOptionalDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	return time.ParseDuration(value)
}

func buildSummary(reports []types.Report, opts options) summary {
	out := summary{
		Lane:              opts.Lane,
		Selector:          opts.Selector,
		KubernetesVersion: opts.KubernetesVersion,
		OpenBAOVersion:    opts.OpenBAOVersion,
		ReportCount:       len(reports),
		SuiteSucceeded:    true,
	}

	var specs []specSummary
	fileAggregates := map[string]*aggregateSummary{}
	labelAggregates := map[string]*aggregateSummary{}
	laneAggregates := map[string]*aggregateSummary{}
	for _, report := range reports {
		out.SuiteSucceeded = out.SuiteSucceeded && report.SuiteSucceeded
		out.TotalSpecs += report.PreRunStats.TotalSpecs
		out.SpecsThatWillRun += report.PreRunStats.SpecsThatWillRun
		out.SpecialSuiteFailureReasons = append(out.SpecialSuiteFailureReasons, report.SpecialSuiteFailureReasons...)
		out.RunTimeSeconds += report.RunTime.Seconds()

		for _, spec := range report.SpecReports {
			out.SpecReports++
			specOut := summarizeSpec(spec)

			if isSuiteNode(spec) {
				countSuiteNodeState(&out, spec.State)
				if spec.State.Is(types.SpecStateFailureStates) {
					out.Failures = append(out.Failures, specOut)
				}
				continue
			}

			if includeInSlowestSpecs(spec) {
				specs = append(specs, specOut)
			}

			if includeInRuntimeAggregates(spec) {
				fileAggregate := namedAggregate(fileAggregates, normalizeReportFile(spec.LeafNodeLocation.FileName))
				updateAggregate(fileAggregate, spec.State, spec.RunTime)
				for _, label := range specOut.Labels {
					updateAggregate(namedAggregate(labelAggregates, label), spec.State, spec.RunTime)
				}
				if strings.TrimSpace(opts.Lane) != "" {
					updateAggregate(namedAggregate(laneAggregates, opts.Lane), spec.State, spec.RunTime)
				}
			}

			switch {
			case spec.State.Is(types.SpecStatePassed):
				out.Passed++
			case spec.State.Is(types.SpecStateFailureStates):
				out.Failed++
				out.Failures = append(out.Failures, specOut)
			case spec.State.Is(types.SpecStateSkipped):
				out.Skipped++
				if isSelectedSkip(spec) {
					out.SelectedSkips = append(out.SelectedSkips, specOut)
				}
			case spec.State.Is(types.SpecStatePending):
				out.Pending++
			default:
				out.Other++
			}
		}
	}

	out.RunTimeSeconds = roundSeconds(out.RunTimeSeconds)
	out.RunTime = formatDuration(out.RunTimeSeconds)

	sort.SliceStable(specs, func(i, j int) bool {
		return specs[i].RunTimeSeconds > specs[j].RunTimeSeconds
	})
	if opts.TopSpecs > len(specs) {
		opts.TopSpecs = len(specs)
	}
	out.SlowestSpecs = append(out.SlowestSpecs, specs[:opts.TopSpecs]...)

	sort.SliceStable(out.Failures, func(i, j int) bool {
		return out.Failures[i].Name < out.Failures[j].Name
	})
	sort.SliceStable(out.SelectedSkips, func(i, j int) bool {
		return out.SelectedSkips[i].Name < out.SelectedSkips[j].Name
	})

	out.SpecialSuiteFailureReasons = uniqueStrings(out.SpecialSuiteFailureReasons)
	out.Aggregates = aggregates{
		ByLane:  aggregateList(laneAggregates, nil, nil),
		ByFile:  aggregateList(fileAggregates, opts.SuiteBudgets, &out),
		ByLabel: aggregateList(labelAggregates, nil, nil),
	}
	sort.SliceStable(out.BudgetWarnings, func(i, j int) bool {
		return out.BudgetWarnings[i].BudgetRatio > out.BudgetWarnings[j].BudgetRatio
	})
	return out
}

func namedAggregate(aggregates map[string]*aggregateSummary, name string) *aggregateSummary {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "(unknown)"
	}
	aggregate, ok := aggregates[name]
	if !ok {
		aggregate = &aggregateSummary{Name: name}
		aggregates[name] = aggregate
	}
	return aggregate
}

func updateAggregate(aggregate *aggregateSummary, state types.SpecState, runTime time.Duration) {
	aggregate.Specs++
	aggregate.RunTimeSeconds += runTime.Seconds()
	switch {
	case state.Is(types.SpecStatePassed):
		aggregate.Passed++
	case state.Is(types.SpecStateFailureStates):
		aggregate.Failed++
	case state.Is(types.SpecStateSkipped):
		aggregate.Skipped++
	case state.Is(types.SpecStatePending):
		aggregate.Pending++
	default:
		aggregate.Other++
	}
}

func aggregateList(
	aggregates map[string]*aggregateSummary,
	budgets map[string]runtimeBudget,
	summaryOut *summary,
) []aggregateSummary {
	out := make([]aggregateSummary, 0, len(aggregates))
	for _, aggregate := range aggregates {
		item := *aggregate
		item.RunTimeSeconds = roundSeconds(item.RunTimeSeconds)
		item.RunTime = formatDuration(item.RunTimeSeconds)
		if budget, ok := budgets[item.Name]; ok {
			item.SuiteID = budget.SuiteID
			item.Budget = formatDuration(budget.Budget.Seconds())
			item.BudgetSeconds = roundSeconds(budget.Budget.Seconds())
			if item.BudgetSeconds > 0 {
				item.BudgetRatio = roundSeconds(item.RunTimeSeconds / item.BudgetSeconds)
			}
			if summaryOut != nil && item.RunTimeSeconds > item.BudgetSeconds {
				summaryOut.BudgetWarnings = append(summaryOut.BudgetWarnings, budgetWarning{
					Scope:          "file",
					Name:           item.Name,
					SuiteID:        budget.SuiteID,
					RunTime:        item.RunTime,
					RunTimeSeconds: item.RunTimeSeconds,
					Budget:         item.Budget,
					BudgetSeconds:  item.BudgetSeconds,
					BudgetRatio:    item.BudgetRatio,
					Message: fmt.Sprintf(
						"%s exceeded runtime budget %s with %s",
						item.Name,
						item.Budget,
						item.RunTime,
					),
				})
			}
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RunTimeSeconds == out[j].RunTimeSeconds {
			return out[i].Name < out[j].Name
		}
		return out[i].RunTimeSeconds > out[j].RunTimeSeconds
	})
	return out
}

func isSuiteNode(spec types.SpecReport) bool {
	return spec.LeafNodeType.Is(types.NodeTypesForSuiteLevelNodes) || strings.TrimSpace(spec.LeafNodeText) == ""
}

func includeInSlowestSpecs(spec types.SpecReport) bool {
	return includeInRuntimeAggregates(spec)
}

func includeInRuntimeAggregates(spec types.SpecReport) bool {
	if spec.State.Is(types.SpecStatePassed | types.SpecStateFailureStates) {
		return true
	}
	return spec.RunTime > 0
}

func isSelectedSkip(spec types.SpecReport) bool {
	if !spec.State.Is(types.SpecStateSkipped) {
		return false
	}
	return spec.RunTime > 0 || strings.TrimSpace(spec.Failure.Message) != ""
}

func countSuiteNodeState(out *summary, state types.SpecState) {
	out.SuiteNodes++
	switch {
	case state.Is(types.SpecStatePassed):
		out.SuiteNodePassed++
	case state.Is(types.SpecStateFailureStates):
		out.SuiteNodeFailed++
	case state.Is(types.SpecStateSkipped):
		out.SuiteNodeSkipped++
	case state.Is(types.SpecStatePending):
		out.SuiteNodePending++
	default:
		out.SuiteNodeOther++
	}
}

func summarizeSpec(spec types.SpecReport) specSummary {
	return specSummary{
		Name:           specName(spec),
		State:          spec.State.String(),
		RunTime:        formatDuration(spec.RunTime.Seconds()),
		RunTimeSeconds: roundSeconds(spec.RunTime.Seconds()),
		Location:       spec.LeafNodeLocation.String(),
		Labels:         specLabels(spec),
		FailureMessage: firstLine(spec.Failure.Message),
	}
}

func specName(spec types.SpecReport) string {
	parts := append([]string{}, spec.ContainerHierarchyTexts...)
	if strings.TrimSpace(spec.LeafNodeText) != "" {
		parts = append(parts, spec.LeafNodeText)
	}

	filtered := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, strings.TrimSpace(part))
		}
	}
	if len(filtered) == 0 {
		if spec.LeafNodeType.Is(types.NodeTypesForSuiteLevelNodes) {
			return fmt.Sprintf("(%s)", spec.LeafNodeType.String())
		}
		return "(suite node)"
	}
	return strings.Join(filtered, " ")
}

func specLabels(spec types.SpecReport) []string {
	seen := map[string]bool{}
	var labels []string
	for _, group := range spec.ContainerHierarchyLabels {
		for _, label := range group {
			if !seen[label] {
				seen[label] = true
				labels = append(labels, label)
			}
		}
	}
	for _, label := range spec.LeafNodeLabels {
		if !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	return labels
}

func normalizeReportFile(file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return "(unknown)"
	}
	if filepath.IsAbs(file) {
		if cwd, err := os.Getwd(); err == nil {
			if rel, err := filepath.Rel(cwd, file); err == nil && !strings.HasPrefix(rel, "..") {
				file = rel
			}
		}
	}
	return filepath.ToSlash(filepath.Clean(file))
}

func formatMarkdown(s summary) string {
	var b strings.Builder

	b.WriteString("## E2E Report Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| --- | --- |\n")
	writeRow(&b, "Lane", valueOrUnset(s.Lane))
	writeRow(&b, "Selector", valueOrUnset(s.Selector))
	writeRow(&b, "Kubernetes", valueOrUnset(s.KubernetesVersion))
	writeRow(&b, "OpenBao", valueOrUnset(s.OpenBAOVersion))
	writeRow(&b, "Suite succeeded", fmt.Sprintf("%t", s.SuiteSucceeded))
	if len(s.ExpectedCaseIDs) > 0 {
		writeRow(&b, "Planned scenarios", fmt.Sprint(len(s.ExpectedCaseIDs)))
		writeRow(&b, "Validated passed scenarios", fmt.Sprint(len(s.PassedCaseIDs)))
	}
	writeRow(&b, "Runtime", s.RunTime)
	writeRow(&b, "Reports", fmt.Sprintf("%d", s.ReportCount))
	writeRow(&b, "Total specs", fmt.Sprintf("%d", s.TotalSpecs))
	writeRow(&b, "Specs selected", fmt.Sprintf("%d", s.SpecsThatWillRun))
	writeRow(&b, "Spec reports", fmt.Sprintf("%d", s.SpecReports))
	writeRow(&b, "Leaf specs passed", fmt.Sprintf("%d", s.Passed))
	writeRow(&b, "Leaf specs failed", fmt.Sprintf("%d", s.Failed))
	writeRow(&b, "Leaf specs skipped", fmt.Sprintf("%d", s.Skipped))
	if len(s.SelectedSkips) > 0 {
		writeRow(&b, "Leaf specs selected-skipped", fmt.Sprintf("%d", len(s.SelectedSkips)))
	}
	writeRow(&b, "Leaf specs pending", fmt.Sprintf("%d", s.Pending))
	if s.Other > 0 {
		writeRow(&b, "Leaf specs other", fmt.Sprintf("%d", s.Other))
	}
	writeRow(&b, "Suite nodes", fmt.Sprintf("%d", s.SuiteNodes))
	writeRow(&b, "Suite nodes passed", fmt.Sprintf("%d", s.SuiteNodePassed))
	writeRow(&b, "Suite nodes failed", fmt.Sprintf("%d", s.SuiteNodeFailed))
	if s.SuiteNodeSkipped > 0 {
		writeRow(&b, "Suite nodes skipped", fmt.Sprintf("%d", s.SuiteNodeSkipped))
	}
	if s.SuiteNodePending > 0 {
		writeRow(&b, "Suite nodes pending", fmt.Sprintf("%d", s.SuiteNodePending))
	}
	if s.SuiteNodeOther > 0 {
		writeRow(&b, "Suite nodes other", fmt.Sprintf("%d", s.SuiteNodeOther))
	}
	b.WriteString("\n")

	if len(s.SelectionErrors) > 0 {
		b.WriteString("\n### Scenario validation failures\n\n")
		for _, issue := range s.SelectionErrors {
			fmt.Fprintf(&b, "- %s\n", issue)
		}
	}

	if len(s.SpecialSuiteFailureReasons) > 0 {
		b.WriteString("### Suite Failure Reasons\n\n")
		for _, reason := range s.SpecialSuiteFailureReasons {
			fmt.Fprintf(&b, "- %s\n", escapeMarkdown(reason))
		}
		b.WriteString("\n")
	}

	if len(s.BudgetWarnings) > 0 {
		b.WriteString("### Runtime Budget Warnings\n\n")
		b.WriteString("| Scope | Runtime | Budget | Ratio | Name |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, warning := range s.BudgetWarnings {
			fmt.Fprintf(
				&b,
				"| %s | %s | %s | %.2fx | %s |\n",
				escapeMarkdown(warning.Scope),
				warning.RunTime,
				warning.Budget,
				warning.BudgetRatio,
				escapeMarkdown(warning.Name),
			)
		}
		b.WriteString("\n")
	}

	b.WriteString("### Slowest Specs\n\n")
	if len(s.SlowestSpecs) == 0 {
		b.WriteString("No specs were reported.\n\n")
	} else {
		b.WriteString("| Runtime | State | Spec |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, spec := range s.SlowestSpecs {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", spec.RunTime, escapeMarkdown(spec.State), escapeMarkdown(spec.Name))
		}
		b.WriteString("\n")
	}

	writeAggregateTable(&b, "Runtime By Lane", s.Aggregates.ByLane, 0)
	writeAggregateTable(&b, "Runtime By File", s.Aggregates.ByFile, 10)
	writeAggregateTable(&b, "Runtime By Label", s.Aggregates.ByLabel, 10)

	if len(s.Failures) > 0 {
		b.WriteString("### Failures\n\n")
		b.WriteString("| State | Spec | Message |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, spec := range s.Failures {
			fmt.Fprintf(
				&b,
				"| %s | %s | %s |\n",
				escapeMarkdown(spec.State),
				escapeMarkdown(spec.Name),
				escapeMarkdown(valueOrUnset(spec.FailureMessage)),
			)
		}
		b.WriteString("\n")
	}

	if len(s.SelectedSkips) > 0 {
		b.WriteString("### Selected Skips\n\n")
		b.WriteString("| Runtime | Spec | Message |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, spec := range s.SelectedSkips {
			fmt.Fprintf(
				&b,
				"| %s | %s | %s |\n",
				spec.RunTime,
				escapeMarkdown(spec.Name),
				escapeMarkdown(spec.FailureMessage),
			)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func writeAggregateTable(b *strings.Builder, title string, aggregates []aggregateSummary, limit int) {
	if len(aggregates) == 0 {
		return
	}
	if limit > 0 && len(aggregates) > limit {
		aggregates = aggregates[:limit]
	}

	fmt.Fprintf(b, "### %s\n\n", title)
	b.WriteString("| Runtime | Specs | Passed | Failed | Skipped | Budget | Name |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | --- | --- |\n")
	for _, aggregate := range aggregates {
		budget := valueOrUnset(aggregate.Budget)
		fmt.Fprintf(
			b,
			"| %s | %d | %d | %d | %d | %s | %s |\n",
			aggregate.RunTime,
			aggregate.Specs,
			aggregate.Passed,
			aggregate.Failed,
			aggregate.Skipped,
			escapeMarkdown(budget),
			escapeMarkdown(aggregate.Name),
		)
	}
	b.WriteString("\n")
}

func writeRow(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "| %s | %s |\n", escapeMarkdown(key), escapeMarkdown(value))
}

func writeJSON(path string, s summary) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		return fmt.Errorf("create summary json dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary json: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o660); err != nil {
		return fmt.Errorf("write summary json: %w", err)
	}
	return nil
}

func writeText(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		return fmt.Errorf("create summary markdown dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o660); err != nil {
		return fmt.Errorf("write summary markdown: %w", err)
	}
	return nil
}

func formatDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	return time.Duration(seconds * float64(time.Second)).Round(time.Millisecond).String()
}

func roundSeconds(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return value[:idx]
	}
	return value
}

func valueOrUnset(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(unset)"
	}
	return value
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func escapeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return value
}
