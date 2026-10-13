package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2/types"
)

func selectionReport() types.Report {
	return types.Report{
		SuiteSucceeded: true,
		SuiteConfig:    types.SuiteConfig{LabelFilter: "smoke"},
		PreRunStats:    types.PreRunStats{TotalSpecs: 2, SpecsThatWillRun: 1},
		SpecReports: types.SpecReports{
			{LeafNodeType: types.NodeTypeBeforeSuite, State: types.SpecStatePassed},
			{LeafNodeType: types.NodeTypeIt, LeafNodeText: "planned scenario",
				LeafNodeLabels: []string{"smoke", "case:planned"}, State: types.SpecStatePassed, RunTime: time.Second},
			{LeafNodeType: types.NodeTypeIt, LeafNodeText: "filtered scenario",
				LeafNodeLabels: []string{"other", "case:filtered"}, State: types.SpecStateSkipped},
		},
	}
}

func expectedCaseFile(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "expected-cases.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScenarioEvidenceMustMatchPlan(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*types.Report)
		want string
	}{
		{"passed with filtered specs and suite hooks", func(_ *types.Report) {}, ""},
		{"missing", func(r *types.Report) { r.SpecReports = r.SpecReports[:1] }, "missing scenario"},
		{"unexpected", func(r *types.Report) { r.SpecReports[2].State = types.SpecStatePassed }, "unplanned scenario"},
		{
			"unexpected selected skip",
			func(r *types.Report) { r.SpecReports[2].Failure.Message = "Skip called" },
			"unplanned scenario",
		},
		{"duplicate", func(r *types.Report) { r.SpecReports = append(r.SpecReports, r.SpecReports[1]) }, "appears 2 times"},
		{"skipped before execution", func(r *types.Report) {
			r.SpecReports[1].State, r.SpecReports[1].RunTime = types.SpecStateSkipped, 0
		}, "planned: skipped"},
		{"pending", func(r *types.Report) { r.SpecReports[1].State = types.SpecStatePending }, "planned: pending"},
		{"failed", func(r *types.Report) { r.SpecReports[1].State = types.SpecStateFailed }, "planned: failed"},
		{
			"interrupted",
			func(r *types.Report) { r.SpecReports[1].State = types.SpecStateInterrupted },
			"planned: interrupted",
		},
		{"missing ID", func(r *types.Report) { r.SpecReports[1].LeafNodeLabels = []string{"smoke"} }, "exactly one case:"},
		{"multiple IDs", func(r *types.Report) {
			r.SpecReports[1].LeafNodeLabels = append(r.SpecReports[1].LeafNodeLabels, "case:other")
		}, "exactly one case:"},
		{
			"ID changed",
			func(r *types.Report) { r.SpecReports[1].LeafNodeLabels = []string{"smoke", "case:renamed"} },
			"missing scenario",
		},
		{"selector changed", func(r *types.Report) { r.SuiteConfig.LabelFilter = "other" }, "selector differs"},
		{"selection count changed", func(r *types.Report) { r.PreRunStats.SpecsThatWillRun = 2 }, "selected 2 scenarios"},
		{"suite failure", func(r *types.Report) { r.SuiteSucceeded = false }, "suite did not succeed"},
		{"dry run", func(r *types.Report) { r.SuiteConfig.DryRun = true }, "dry-run reports"},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := selectionReport()
			test.edit(&report)
			opts := options{Selector: "smoke", ExpectedCasesPath: expectedCaseFile(t, []string{"planned"})}
			out := buildSummary([]types.Report{report}, opts)
			if err := validateSelection(&out, []types.Report{report}, opts); err != nil {
				t.Fatal(err)
			}
			if test.want == "" {
				if len(out.SelectionErrors) > 0 || !slices.Equal(out.PassedCaseIDs, []string{"planned"}) {
					t.Fatalf("validation = %+v", out)
				}
			} else if !strings.Contains(strings.Join(out.SelectionErrors, "; "), test.want) {
				t.Fatalf("errors = %v, want %s", out.SelectionErrors, test.want)
			}
		})
	}
}

func TestExpectedCasesFailClosed(t *testing.T) {
	for _, value := range []any{[]string{}, []string{""}, []string{"a", "a"}, "not-an-array"} {
		opts := options{Selector: "smoke", ExpectedCasesPath: expectedCaseFile(t, value)}
		var out summary
		if err := validateSelection(&out, nil, opts); err == nil {
			t.Fatalf("invalid expected cases accepted: %v", value)
		}
	}
	for _, selector := range []string{"", "a && ("} {
		opts := options{Selector: selector, ExpectedCasesPath: expectedCaseFile(t, []string{"a"})}
		var out summary
		if err := validateSelection(&out, nil, opts); err == nil {
			t.Fatalf("invalid selector accepted: %s", selector)
		}
	}
}
