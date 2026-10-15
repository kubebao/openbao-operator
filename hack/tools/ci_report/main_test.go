package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRun() workflowRun {
	return workflowRun{ID: 1, Attempt: 2, HeadSHA: "head", Event: "push", CreatedAt: time.Unix(100, 0).UTC()}
}

func testJob(id int64, name, conclusion string, start, end int64) actionJob {
	s, e := time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
	return actionJob{ID: id, Status: "completed", actionStep: actionStep{
		Name: name, Conclusion: conclusion, StartedAt: &s, FinishedAt: &e,
	}}
}

func TestSummarizeOverlappingJobsAndSkippedRows(t *testing.T) {
	t.Parallel()
	jobs := []actionJob{
		testJob(1, "E2E (Core)", "success", 100, 220),
		testJob(2, "Unit Tests", "failure", 100, 160),
		testJob(3, "E2E (Unused)", "skipped", 100, 220),
		testJob(4, "CI Required", "failure", 220, 230),
		testJob(5, "Build + Verify Edge Candidate", "success", 230, 290),
		testJob(6, measurementJob, "", 290, 999),
	}
	jobs[0].Steps = []actionStep{testJob(7, "Run E2E Tests", "success", 110, 210).actionStep}
	s, err := summarize(testRun(), jobs, routingRecord{SHA: "tested", Checks: map[string]bool{"e2e": true}})
	if err != nil {
		t.Fatal(err)
	}
	if s.ElapsedSeconds != 190 || s.RequiredSeconds == nil || *s.RequiredSeconds != 130 {
		t.Fatalf("elapsed=%v, required=%v", s.ElapsedSeconds, s.RequiredSeconds)
	}
	if math.Abs(s.RunnerMinutes-250.0/60) > 1e-9 || s.CategoryMinutes["e2e"] != 2 || s.CategoryMinutes["edge"] != 1 {
		t.Fatalf("runner totals = %v, categories = %v", s.RunnerMinutes, s.CategoryMinutes)
	}
	if len(s.Jobs) != 5 || s.TestedSHA != "tested" || !s.CompleteSnapshot {
		t.Fatalf("summary = %+v", s)
	}
	if !strings.Contains(markdown(s), "excludes this measurement job") {
		t.Fatal("measurement scope omitted")
	}
}

func TestSummarizeRejectsInvalidAndMarksPartialSnapshots(t *testing.T) {
	t.Parallel()
	job := testJob(1, "Unit Tests", "success", 100, 160)
	if _, err := summarize(testRun(), []actionJob{job, job}, routingRecord{}); err == nil {
		t.Fatal("duplicate job accepted")
	}
	if _, err := summarize(testRun(), []actionJob{testJob(1, "bad", "success", 200, 100)}, routingRecord{}); err == nil {
		t.Fatal("negative duration accepted")
	}
	job.Status = "in_progress"
	job.FinishedAt = nil
	job.Conclusion = ""
	s, err := summarize(testRun(), []actionJob{job}, routingRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if s.CompleteSnapshot || s.RunnerMinutes != 0 {
		t.Fatalf("partial snapshot = %+v", s)
	}
	if _, err := summarize(workflowRun{}, []actionJob{job}, routingRecord{}); err == nil {
		t.Fatal("empty run accepted")
	}
}

func TestFailedJobRerunExcludesInheritedExecutionTime(t *testing.T) {
	t.Parallel()
	workflow := testRun()
	workflow.StartedAt = time.Unix(200, 0).UTC()
	workflow.CreatedAt = time.Unix(202, 0).UTC()
	jobs := []actionJob{
		testJob(1, "E2E (Core)", "success", 100, 180),
		testJob(2, "CI Required", "success", 205, 225),
	}
	s, err := summarize(workflow, jobs, routingRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if s.RunnerMinutes != 20.0/60 || s.CategoryMinutes["e2e"] != 0 || s.ElapsedSeconds != 25 {
		t.Fatalf("inherited timing counted: %+v", s)
	}
	if s.RequiredSeconds == nil || *s.RequiredSeconds != 25 {
		t.Fatal("incorrect attempt-relative gate time")
	}
	if !s.Jobs[1].Reused || s.Jobs[1].DurationSeconds != 80 {
		t.Fatal("inherited result was not retained")
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReportCLIIncludesAllSpecsAcrossLanes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runPath, jobsPath := filepath.Join(dir, "run.json"), filepath.Join(dir, "jobs.json")
	writeJSONFixture(t, runPath, testRun())
	writeJSONFixture(t, jobsPath, map[string]any{"jobs": []actionJob{testJob(1, "CI Required", "success", 100, 160)}})
	reportsDir := filepath.Join(dir, "reports")
	for _, lane := range []string{"core", "rolling"} {
		laneDir := filepath.Join(reportsDir, lane)
		if err := os.MkdirAll(laneDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeJSONFixture(t, filepath.Join(laneDir, "summary.json"), laneReport{Lane: lane, Selected: 2, Passed: 2})
		writeJSONFixture(t, filepath.Join(laneDir, "ginkgo.json"), []any{map[string]any{"SpecReports": []any{
			map[string]any{"LeafNodeText": "switches", "ContainerHierarchyTexts": []string{"upgrade"},
				"LeafNodeLabels": []string{"case:strategy-switch"}, "State": "passed", "RunTime": int64(2 * time.Minute)},
			map[string]any{"LeafNodeText": "checks", "State": "passed", "RunTime": int64(time.Minute)},
			map[string]any{"LeafNodeText": "filtered", "State": "skipped", "RunTime": 0},
			map[string]any{"LeafNodeText": "", "State": "passed", "RunTime": int64(time.Minute)},
		}}})
	}
	jsonPath, mdPath := filepath.Join(dir, "summary.json"), filepath.Join(dir, "summary.md")
	if err := run([]string{"--run", runPath, "--jobs", jobsPath, "--e2e-dir", reportsDir,
		"--json-out", jsonPath, "--markdown-out", mdPath}); err != nil {
		t.Fatal(err)
	}
	var s summary
	if err := readJSON(jsonPath, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Lanes) != 2 || len(s.Specs) != 4 {
		t.Fatalf("lanes=%d specs=%d", len(s.Lanes), len(s.Specs))
	}
	if s.Specs[0].CaseID != "strategy-switch" || s.Specs[0].DurationSeconds != 120 {
		t.Fatalf("spec = %+v", s.Specs[0])
	}
	if s.Run.Attempt != 2 || s.TestedSHA != "head" {
		t.Fatalf("identity = %+v", s.Run)
	}
}

func TestMissingReportsAreNotQualification(t *testing.T) {
	t.Parallel()
	s := summary{}
	if err := loadReports(filepath.Join(t.TempDir(), "absent"), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Lanes) != 0 || !strings.Contains(markdown(s), "Missing reports do not prove qualification") {
		t.Fatal("missing-report boundary omitted")
	}
}

func TestSharedCompilationCountsAsE2ECost(t *testing.T) {
	t.Parallel()
	result, err := summarize(testRun(), []actionJob{
		testJob(1, "Plan E2E Matrix", "success", 100, 220),
	}, routingRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if result.CategoryMinutes["e2e"] != 2 || result.RunnerMinutes != 2 {
		t.Fatalf("shared compilation is missing from E2E cost: %+v", result)
	}
}

func TestForkExecutionCountsAsE2ECost(t *testing.T) {
	t.Parallel()
	result, err := summarize(testRun(), []actionJob{
		testJob(1, "Fork E2E (Core)", "success", 100, 220),
		testJob(2, "Fork Operator Upgrade E2E", "success", 100, 160),
	}, routingRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if result.CategoryMinutes["e2e"] != 3 || result.RunnerMinutes != 3 {
		t.Fatalf("fork execution is missing from E2E cost: %+v", result)
	}
}
