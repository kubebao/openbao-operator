package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2/types"
)

func writeTempGinkgoReport(t *testing.T, reports []types.Report) string {
	t.Helper()

	data, err := json.Marshal(reports)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	path := filepath.Join(t.TempDir(), "ginkgo.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	return path
}

func TestLoadReports(t *testing.T) {
	reportPath := writeTempGinkgoReport(t, []types.Report{
		{
			SuiteSucceeded: true,
			PreRunStats: types.PreRunStats{
				TotalSpecs:       2,
				SpecsThatWillRun: 1,
			},
		},
	})

	reports, err := loadReports([]string{reportPath})
	if err != nil {
		t.Fatalf("loadReports() error = %v", err)
	}
	if got := len(reports); got != 1 {
		t.Fatalf("len(reports) = %d, want 1", got)
	}
	if reports[0].PreRunStats.TotalSpecs != 2 {
		t.Fatalf("total specs = %d, want 2", reports[0].PreRunStats.TotalSpecs)
	}
}

func TestLoadReportsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ginkgo.json")
	if err := os.WriteFile(path, []byte(`{not-json}`), 0o644); err != nil {
		t.Fatalf("write malformed report: %v", err)
	}

	_, err := loadReports([]string{path})
	if err == nil {
		t.Fatalf("loadReports() error = nil, want parse failure")
	}
	if !strings.Contains(err.Error(), "parse report") {
		t.Fatalf("error = %q, want parse report message", err)
	}
}

func TestBuildSummaryCountsAndSlowestSpecs(t *testing.T) {
	reports := []types.Report{
		{
			SuiteSucceeded: true,
			PreRunStats: types.PreRunStats{
				TotalSpecs:       4,
				SpecsThatWillRun: 3,
			},
			RunTime: 5 * time.Second,
			SpecReports: types.SpecReports{
				{
					ContainerHierarchyTexts:  []string{"lifecycle"},
					ContainerHierarchyLabels: [][]string{{"lifecycle"}},
					LeafNodeText:             "creates a cluster",
					LeafNodeLabels:           []string{"case:lifecycle-create"},
					State:                    types.SpecStatePassed,
					RunTime:                  2 * time.Second,
				},
				{
					ContainerHierarchyTexts: []string{"backup"},
					LeafNodeText:            "restores a snapshot",
					State:                   types.SpecStateFailed,
					RunTime:                 3 * time.Second,
					Failure: types.Failure{
						Message: "restore failed\nwith details",
					},
				},
				{
					ContainerHierarchyTexts: []string{"openshift"},
					LeafNodeText:            "runs on openshift",
					State:                   types.SpecStateSkipped,
					RunTime:                 0,
				},
			},
		},
	}

	s := buildSummary(reports, options{
		TopSpecs:          2,
		Lane:              "Backup & Restore",
		Selector:          "restore",
		KubernetesVersion: "kindest/node:v1.35.1",
		OpenBAOVersion:    "ghcr.io/openbao/openbao:2.6.2",
	})

	if !s.SuiteSucceeded {
		t.Fatalf("suite succeeded = false, want true")
	}
	if s.Passed != 1 || s.Failed != 1 || s.Skipped != 1 {
		t.Fatalf("counts = passed %d failed %d skipped %d, want 1/1/1", s.Passed, s.Failed, s.Skipped)
	}
	if got := len(s.SlowestSpecs); got != 2 {
		t.Fatalf("slowest specs = %d, want 2", got)
	}
	if s.SlowestSpecs[0].Name != "backup restores a snapshot" {
		t.Fatalf("slowest[0] = %q, want backup spec", s.SlowestSpecs[0].Name)
	}
	if got := s.Failures[0].FailureMessage; got != "restore failed" {
		t.Fatalf("failure message = %q, want first line", got)
	}
}

func TestBuildSummarySeparatesLeafSpecsFromSuiteNodes(t *testing.T) {
	reports := []types.Report{
		{
			SuiteSucceeded: false,
			PreRunStats: types.PreRunStats{
				TotalSpecs:       1,
				SpecsThatWillRun: 1,
			},
			RunTime: 7 * time.Second,
			SpecReports: types.SpecReports{
				{
					LeafNodeType: types.NodeTypeSynchronizedBeforeSuite,
					State:        types.SpecStatePassed,
					RunTime:      2 * time.Second,
				},
				{
					ContainerHierarchyTexts: []string{"lifecycle"},
					LeafNodeType:            types.NodeTypeIt,
					LeafNodeText:            "creates a cluster",
					State:                   types.SpecStatePassed,
					RunTime:                 3 * time.Second,
				},
				{
					LeafNodeType: types.NodeTypeSynchronizedAfterSuite,
					State:        types.SpecStateFailed,
					RunTime:      time.Second,
					Failure: types.Failure{
						Message: "cleanup failed",
					},
				},
			},
		},
	}

	s := buildSummary(reports, options{TopSpecs: 10})

	if s.Passed != 1 || s.Failed != 0 || s.Skipped != 0 {
		t.Fatalf("leaf counts = passed %d failed %d skipped %d, want 1/0/0", s.Passed, s.Failed, s.Skipped)
	}
	if s.SuiteNodes != 2 || s.SuiteNodePassed != 1 || s.SuiteNodeFailed != 1 {
		t.Fatalf(
			"suite node counts = nodes %d passed %d failed %d, want 2/1/1",
			s.SuiteNodes,
			s.SuiteNodePassed,
			s.SuiteNodeFailed,
		)
	}
	if got := len(s.SlowestSpecs); got != 1 {
		t.Fatalf("slowest specs = %d, want only leaf specs", got)
	}
	if got := s.SlowestSpecs[0].Name; got != "lifecycle creates a cluster" {
		t.Fatalf("slowest leaf spec = %q, want lifecycle spec", got)
	}
	if got := len(s.Failures); got != 1 {
		t.Fatalf("failures = %d, want suite node failure", got)
	}
	if got := s.Failures[0].Name; got != "(SynchronizedAfterSuite)" {
		t.Fatalf("failure name = %q, want suite node type", got)
	}
}

func TestBuildSummaryExcludesSkippedSpecsFromSlowestSpecs(t *testing.T) {
	reports := []types.Report{
		{
			SuiteSucceeded: true,
			PreRunStats: types.PreRunStats{
				TotalSpecs:       3,
				SpecsThatWillRun: 1,
			},
			RunTime: 2 * time.Second,
			SpecReports: types.SpecReports{
				{
					ContainerHierarchyTexts: []string{"acme"},
					LeafNodeType:            types.NodeTypeIt,
					LeafNodeText:            "creates a cluster",
					State:                   types.SpecStatePassed,
					RunTime:                 2 * time.Second,
				},
				{
					ContainerHierarchyTexts: []string{"openshift"},
					LeafNodeType:            types.NodeTypeIt,
					LeafNodeText:            "runs on openshift",
					State:                   types.SpecStateSkipped,
					RunTime:                 0,
				},
				{
					ContainerHierarchyTexts: []string{"manual"},
					LeafNodeType:            types.NodeTypeIt,
					LeafNodeText:            "waits for a trigger",
					State:                   types.SpecStatePending,
					RunTime:                 0,
				},
			},
		},
	}

	s := buildSummary(reports, options{TopSpecs: 10})

	if got := len(s.SlowestSpecs); got != 1 {
		t.Fatalf("slowest specs = %d, want only executed spec", got)
	}
	if got := s.SlowestSpecs[0].Name; got != "acme creates a cluster" {
		t.Fatalf("slowest spec = %q, want acme spec", got)
	}
}

func TestBuildSummaryDetectsSelectedSkips(t *testing.T) {
	reports := []types.Report{
		{
			SuiteSucceeded: true,
			PreRunStats: types.PreRunStats{
				TotalSpecs:       2,
				SpecsThatWillRun: 1,
			},
			RunTime: 5 * time.Minute,
			SpecReports: types.SpecReports{
				{
					ContainerHierarchyTexts: []string{"backup"},
					LeafNodeType:            types.NodeTypeIt,
					LeafNodeText:            "creates a restorable S3 backup",
					State:                   types.SpecStateSkipped,
					RunTime:                 5 * time.Minute,
					Failure: types.Failure{
						Message: "RustFS deployment failed",
					},
				},
				{
					ContainerHierarchyTexts: []string{"openshift"},
					LeafNodeType:            types.NodeTypeIt,
					LeafNodeText:            "runs on openshift",
					State:                   types.SpecStateSkipped,
					RunTime:                 0,
				},
			},
		},
	}

	s := buildSummary(reports, options{TopSpecs: 10})

	if got := len(s.SelectedSkips); got != 1 {
		t.Fatalf("selected skips = %d, want 1", got)
	}
	if got := s.SelectedSkips[0].FailureMessage; got != "RustFS deployment failed" {
		t.Fatalf("selected skip message = %q, want RustFS failure", got)
	}

	markdown := formatMarkdown(s)
	if !strings.Contains(markdown, "Leaf specs selected-skipped") {
		t.Fatalf("markdown missing selected skip metric: %q", markdown)
	}
	if !strings.Contains(markdown, "### Selected Skips") {
		t.Fatalf("markdown missing selected skips section: %q", markdown)
	}
}

func TestBuildSummaryAggregatesRuntimeAndWarnsOnBudget(t *testing.T) {
	reports := []types.Report{
		{
			SuiteSucceeded: true,
			PreRunStats: types.PreRunStats{
				TotalSpecs:       2,
				SpecsThatWillRun: 2,
			},
			RunTime: 3 * time.Second,
			SpecReports: types.SpecReports{
				{
					ContainerHierarchyTexts:  []string{"acme"},
					ContainerHierarchyLabels: [][]string{{"tls"}},
					LeafNodeType:             types.NodeTypeIt,
					LeafNodeText:             "creates a cluster",
					LeafNodeLocation: types.CodeLocation{
						FileName: "test/e2e/Cluster_TLS_ACME_test.go",
					},
					LeafNodeLabels: []string{"security"},
					State:          types.SpecStatePassed,
					RunTime:        2 * time.Second,
				},
				{
					ContainerHierarchyTexts:  []string{"acme"},
					ContainerHierarchyLabels: [][]string{{"tls"}},
					LeafNodeType:             types.NodeTypeIt,
					LeafNodeText:             "validates auth",
					LeafNodeLocation: types.CodeLocation{
						FileName: "test/e2e/Cluster_TLS_ACME_test.go",
					},
					State:   types.SpecStatePassed,
					RunTime: time.Second,
				},
			},
		},
	}

	s := buildSummary(reports, options{
		TopSpecs: 10,
		Lane:     "Hardened",
		SuiteBudgets: map[string]runtimeBudget{
			"test/e2e/Cluster_TLS_ACME_test.go": {
				SuiteID: "cluster-tls-acme",
				Budget:  2 * time.Second,
			},
		},
	})

	if got := len(s.Aggregates.ByFile); got != 1 {
		t.Fatalf("file aggregates = %d, want 1", got)
	}
	if got := s.Aggregates.ByFile[0].RunTime; got != "3s" {
		t.Fatalf("file runtime = %q, want 3s", got)
	}
	if got := s.Aggregates.ByFile[0].Budget; got != "2s" {
		t.Fatalf("file budget = %q, want 2s", got)
	}
	if got := len(s.Aggregates.ByLabel); got != 2 {
		t.Fatalf("label aggregates = %d, want tls and security", got)
	}
	if got := s.Aggregates.ByLane[0].Name; got != "Hardened" {
		t.Fatalf("lane aggregate = %q, want Hardened", got)
	}
	if got := len(s.BudgetWarnings); got != 1 {
		t.Fatalf("budget warnings = %d, want 1", got)
	}
	if got := s.BudgetWarnings[0].SuiteID; got != "cluster-tls-acme" {
		t.Fatalf("warning suite id = %q, want cluster-tls-acme", got)
	}
}

func TestFormatMarkdown(t *testing.T) {
	out := formatMarkdown(summary{
		Lane:             "Core",
		SuiteSucceeded:   true,
		TotalSpecs:       2,
		SpecsThatWillRun: 2,
		Passed:           2,
		RunTime:          "1s",
		SlowestSpecs: []specSummary{
			{Name: "lifecycle creates | updates", State: "passed", RunTime: "1s"},
		},
	})

	if !strings.Contains(out, "## E2E Report Summary") {
		t.Fatalf("markdown missing title: %q", out)
	}
	if !strings.Contains(out, "lifecycle creates \\| updates") {
		t.Fatalf("markdown did not escape table pipe: %q", out)
	}
}

func TestFormatDurationRoundsToMilliseconds(t *testing.T) {
	tests := []struct {
		name    string
		seconds float64
		want    string
	}{
		{name: "negative", seconds: -1, want: "0s"},
		{name: "subsecond", seconds: 0.1234, want: "123ms"},
		{name: "multi minute", seconds: 713.39133725, want: "11m53.391s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDuration(tt.seconds); got != tt.want {
				t.Fatalf("formatDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
}

func TestPrecompiledReportLocationsRetainSuiteBudgets(t *testing.T) {
	for _, file := range []string{
		"/builder/work/operator/test/e2e/Cluster_Lifecycle_test.go",
		"github.com/kubebao/openbao-operator/test/e2e/Cluster_Lifecycle_test.go",
	} {
		if got := normalizeReportFile(file); got != "test/e2e/Cluster_Lifecycle_test.go" {
			t.Fatalf("precompiled report location = %s", got)
		}
	}
}
