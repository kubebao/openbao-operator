package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/onsi/ginkgo/v2/types"
)

const manualPolicy = "manual"

type scenarioSuite struct {
	ID    string   `yaml:"id"`
	Files []string `yaml:"files"`
	CI    struct {
		Lanes       []string `yaml:"lanes"`
		PullRequest string   `yaml:"pullRequest"`
	} `yaml:"ci"`
}

type catalogCase struct {
	ID        string   `json:"id"`
	CaseLabel string   `json:"caseLabel"`
	File      string   `json:"file"`
	Labels    []string `json:"labels"`
	Focused   bool     `json:"focused"`
	Pending   bool     `json:"pending"`
}

// ciSelection is the e2eSelection contract written by ci_checks. The planner
// consumes the recorded scopes instead of evaluating event policy again.
type ciSelection struct {
	Full           bool            `json:"full"`
	Scopes         map[string]bool `json:"scopes"`
	IncludePentest bool            `json:"includePentest"`
}

func planScenarios(matrix *githubMatrix, m manifest, opts options) error {
	data, err := os.ReadFile(opts.CatalogPath)
	if err != nil {
		return fmt.Errorf("read scenario catalog: %w", err)
	}
	var cases []catalogCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return fmt.Errorf("decode scenario catalog: %w", err)
	}
	if err := validateScenarioOwnership(m, cases); err != nil {
		return err
	}
	var selection *ciSelection
	if opts.CIPlanPath != "" {
		if opts.Format != "github-matrix" {
			return fmt.Errorf("--ci-plan requires --format github-matrix")
		}
		selection, err = loadCISelection(opts.CIPlanPath, opts.SHA)
		if err != nil {
			return err
		}
	}
	*matrix, err = selectScenarios(*matrix, m, cases, selection)
	return err
}

func loadCISelection(path, sha string) (*ciSelection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CI plan: %w", err)
	}
	var plan struct {
		Version   int             `json:"version"`
		SHA       string          `json:"sha"`
		Checks    map[string]bool `json:"checks"`
		Selection *struct {
			Full           *bool           `json:"full"`
			Scopes         map[string]bool `json:"scopes"`
			IncludePentest *bool           `json:"includePentest"`
		} `json:"e2eSelection"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("decode CI plan: %w", err)
	}
	if sha == "" || plan.Version != 1 || plan.SHA != sha || plan.Selection == nil ||
		plan.Selection.Full == nil || plan.Selection.IncludePentest == nil ||
		!plan.Checks["e2e"] || !plan.Checks["e2e-matrix"] {
		return nil, fmt.Errorf("missing, incompatible, source-mismatched, or unselected E2E expectations")
	}
	selection := &ciSelection{
		Full: *plan.Selection.Full, Scopes: plan.Selection.Scopes, IncludePentest: *plan.Selection.IncludePentest,
	}
	for _, scope := range []string{"always", "backup", "upgrade", "hardened", "seal"} {
		selected, exists := selection.Scopes[scope]
		if !exists || (selection.Full && !selected) {
			return nil, fmt.Errorf("missing or inconsistent E2E scope %q", scope)
		}
	}
	if len(selection.Scopes) != 5 || (selection.Full && !selection.IncludePentest) {
		return nil, fmt.Errorf("inconsistent E2E selection policy")
	}
	return selection, nil
}

// Validate the full lane selectors against suite ownership, independently of
// this run's routing. A selector change cannot silently orphan a scenario.
func validateScenarioOwnership(m manifest, cases []catalogCase) error {
	if len(cases) == 0 {
		return fmt.Errorf("scenario catalog is empty")
	}
	owners := map[string]scenarioSuite{}
	for _, suite := range m.Suites {
		for _, file := range suite.Files {
			if previous, exists := owners[file]; exists {
				return fmt.Errorf("%s is owned by both %s and %s", file, previous.ID, suite.ID)
			}
			owners[file] = suite
		}
	}
	filters := map[string]types.LabelFilter{}
	prFilters := map[string]types.LabelFilter{}
	for _, lane := range m.CILanes {
		filter, err := types.ParseLabelFilter(lane.LabelFilter)
		if err != nil {
			return fmt.Errorf("lane %s labelFilter: %w", lane.ID, err)
		}
		filters[lane.ID] = filter
		if lane.PRLabelFilter != "" {
			prFilter, err := types.ParseLabelFilter(lane.PRLabelFilter)
			if err != nil {
				return fmt.Errorf("lane %s prLabelFilter: %w", lane.ID, err)
			}
			prFilters[lane.ID] = prFilter
		}
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if err := validateCatalogCase(tc, seen); err != nil {
			return err
		}
		suite, exists := owners[tc.File]
		if !exists {
			return fmt.Errorf("scenario %s has no suite owner", tc.ID)
		}
		var selected []string
		automaticOwner := false
		for _, lane := range m.CILanes {
			if prFilter := prFilters[lane.ID]; prFilter != nil && prFilter(tc.Labels) && !filters[lane.ID](tc.Labels) {
				return fmt.Errorf("lane %s PR selector includes scenario %s outside its full owner", lane.ID, tc.ID)
			}
			if !filters[lane.ID](tc.Labels) {
				continue
			}
			if !slices.Contains(suite.CI.Lanes, lane.ID) {
				return fmt.Errorf("scenario %s selects undeclared lane %s (suite %s)", tc.ID, lane.ID, suite.ID)
			}
			selected = append(selected, lane.ID)
			automaticOwner = automaticOwner || (includeInPRMatrix(lane) && lane.PRScope != manualPolicy)
		}
		if len(selected) > 1 {
			return fmt.Errorf("scenario %s has multiple lane owners: %s", tc.ID, strings.Join(selected, ", "))
		}
		if len(selected) == 0 && suite.CI.PullRequest != manualPolicy {
			return fmt.Errorf("scenario %s is absent from all full lane selectors", tc.ID)
		}
		if !automaticOwner && suite.CI.PullRequest != manualPolicy {
			return fmt.Errorf("scenario %s is absent from the full automatic CI matrix", tc.ID)
		}
	}
	return nil
}

func validateCatalogCase(tc catalogCase, seen map[string]bool) error {
	if tc.ID == "" || tc.ID != tc.CaseLabel || !idPattern.MatchString(tc.ID) || seen[tc.ID] {
		return fmt.Errorf("scenario %q requires a unique stable case ID", tc.ID)
	}
	seen[tc.ID] = true
	count := 0
	for _, label := range tc.Labels {
		if strings.HasPrefix(label, "case:") {
			count++
			if label != "case:"+tc.ID {
				return fmt.Errorf("scenario %s has inconsistent case labels", tc.ID)
			}
		}
	}
	if count != 1 || tc.Focused || tc.Pending {
		return fmt.Errorf("scenario %s requires one case label and must not be focused or pending", tc.ID)
	}
	return nil
}

func selectScenarios(
	matrix githubMatrix, m manifest, cases []catalogCase, selection *ciSelection,
) (githubMatrix, error) {
	fullFilters := map[string]types.LabelFilter{}
	for _, lane := range m.CILanes {
		filter, err := types.ParseLabelFilter(lane.LabelFilter)
		if err != nil {
			return githubMatrix{}, fmt.Errorf("lane %s full selector: %w", lane.ID, err)
		}
		fullFilters[lane.ID] = filter
	}
	planned := githubMatrix{Include: []matrixRow{}}
	seen := map[string]string{}
	for _, row := range matrix.Include {
		if selection != nil {
			if !selection.Scopes[row.PRScope] {
				continue
			}
			if !selection.Full {
				if row.PRLabelFilter != "" {
					row.LabelFilter = row.PRLabelFilter
				}
				row.LabelFilter = "(" + row.LabelFilter + ") && !lower-layer-covered"
				if row.ExcludePentestOnDefault == "true" && !selection.IncludePentest {
					row.LabelFilter = "(" + row.LabelFilter + ") && !pentest"
				}
			}
		}
		filter, err := types.ParseLabelFilter(row.LabelFilter)
		if err != nil {
			return githubMatrix{}, fmt.Errorf("lane %s effective selector: %w", row.ID, err)
		}
		fullFilter := fullFilters[row.ID]
		if fullFilter == nil {
			return githubMatrix{}, fmt.Errorf("selected lane %s has no full selector", row.ID)
		}
		for _, tc := range cases {
			if !filter(tc.Labels) {
				continue
			}
			if !fullFilter(tc.Labels) {
				return githubMatrix{}, fmt.Errorf("lane %s selects scenario %s outside its full owner", row.ID, tc.ID)
			}
			key := strings.Join([]string{row.Kubernetes, row.OpenBaoImage, tc.ID}, "\x00")
			if previous, exists := seen[key]; exists {
				return githubMatrix{}, fmt.Errorf("scenario %s repeats in %s and %s on Kubernetes %s / %s",
					tc.ID, previous, row.ID, row.Kubernetes, row.OpenBaoImage)
			}
			seen[key] = row.ID
			row.ExpectedCaseIDs = append(row.ExpectedCaseIDs, tc.ID)
		}
		if len(row.ExpectedCaseIDs) == 0 {
			return githubMatrix{}, fmt.Errorf("selected lane %s has no scenarios: %s", row.ID, row.LabelFilter)
		}
		slices.Sort(row.ExpectedCaseIDs)
		planned.Include = append(planned.Include, row)
	}
	if len(planned.Include) == 0 {
		return githubMatrix{}, fmt.Errorf("E2E selection produced no lanes")
	}
	return planned, nil
}
