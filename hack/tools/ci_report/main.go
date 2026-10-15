// ci_report combines one workflow attempt's job timings and Ginkgo reports.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const measurementJob = "CI Measurement (advisory)"

type workflowRun struct {
	ID        int64     `json:"id"`
	Attempt   int       `json:"run_attempt"`
	Event     string    `json:"event"`
	HeadSHA   string    `json:"head_sha"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"run_started_at"`
	URL       string    `json:"html_url"`
}

type actionStep struct {
	Name       string     `json:"name"`
	Conclusion string     `json:"conclusion"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"completed_at"`
}

type actionJob struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	actionStep
	Steps []actionStep `json:"steps"`
}

type timing struct {
	Name            string   `json:"name"`
	Conclusion      string   `json:"conclusion"`
	DurationSeconds float64  `json:"durationSeconds"`
	Category        string   `json:"category,omitempty"`
	Reused          bool     `json:"reusedFromPreviousAttempt,omitempty"`
	Steps           []timing `json:"steps,omitempty"`
}

type specTiming struct {
	CaseID          string  `json:"caseId,omitempty"`
	Name            string  `json:"name"`
	Lane            string  `json:"lane"`
	State           string  `json:"state"`
	DurationSeconds float64 `json:"durationSeconds"`
}

type laneReport struct {
	Lane            string  `json:"lane"`
	Selected        int     `json:"specsThatWillRun"`
	Passed          int     `json:"passed"`
	Failed          int     `json:"failed"`
	Skipped         int     `json:"skipped"`
	DurationSeconds float64 `json:"runTimeSeconds"`
}

type ginkgoReport struct {
	Specs []struct {
		Text       string        `json:"LeafNodeText"`
		Hierarchy  []string      `json:"ContainerHierarchyTexts"`
		Labels     [][]string    `json:"ContainerHierarchyLabels"`
		LeafLabels []string      `json:"LeafNodeLabels"`
		State      string        `json:"State"`
		RunTime    time.Duration `json:"RunTime"`
	} `json:"SpecReports"`
}

type routingRecord struct {
	SHA    string          `json:"sha"`
	Checks map[string]bool `json:"checks"`
	Reason string          `json:"e2eExclusionReason,omitempty"`
}

type summary struct {
	Version          int                `json:"version"`
	Run              workflowRun        `json:"run"`
	TestedSHA        string             `json:"testedSHA"`
	CompleteSnapshot bool               `json:"completeJobSnapshot"`
	ElapsedSeconds   float64            `json:"observedElapsedSeconds"`
	RequiredSeconds  *float64           `json:"ciRequiredSeconds,omitempty"`
	RunnerMinutes    float64            `json:"runnerMinutes"`
	CategoryMinutes  map[string]float64 `json:"categoryRunnerMinutes"`
	ExpectedChecks   map[string]bool    `json:"expectedChecks,omitempty"`
	E2EReason        string             `json:"e2eExclusionReason,omitempty"`
	Jobs             []timing           `json:"jobs"`
	Lanes            []laneReport       `json:"lanes"`
	Specs            []specTiming       `json:"specs"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ci_report: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("ci_report", flag.ContinueOnError)
	runPath := flags.String("run", "", "GitHub run JSON path")
	jobsPath := flags.String("jobs", "", "paginated jobs JSON with a jobs array")
	planPath := flags.String("plan", "", "optional job expectation JSON path")
	reportsDir := flags.String("e2e-dir", "", "downloaded E2E report directory")
	jsonPath := flags.String("json-out", "", "machine-readable summary path")
	markdownPath := flags.String("markdown-out", "", "Markdown summary path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runPath == "" || *jobsPath == "" || *jsonPath == "" || *markdownPath == "" || flags.NArg() != 0 {
		return errors.New("--run, --jobs, --json-out, and --markdown-out are required")
	}
	var workflow workflowRun
	var jobs struct {
		Jobs []actionJob `json:"jobs"`
	}
	var plan routingRecord
	if err := readJSON(*runPath, &workflow); err != nil {
		return err
	}
	if err := readJSON(*jobsPath, &jobs); err != nil {
		return err
	}
	if *planPath != "" {
		if err := readJSON(*planPath, &plan); err != nil {
			return err
		}
	}
	s, err := summarize(workflow, jobs.Jobs, plan)
	if err != nil {
		return err
	}
	if err := loadReports(*reportsDir, &s); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode summary: %w", err)
	}
	if err := os.WriteFile(*jsonPath, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.WriteFile(*markdownPath, []byte(markdown(s)), 0600)
}

func summarize(run workflowRun, jobs []actionJob, plan routingRecord) (summary, error) {
	if run.ID == 0 || run.Attempt < 1 || run.CreatedAt.IsZero() || len(jobs) == 0 {
		return summary{}, errors.New("run identity, creation time, attempt, and jobs are required")
	}
	s := summary{Version: 1, Run: run, TestedSHA: plan.SHA, CompleteSnapshot: true,
		CategoryMinutes: map[string]float64{}, ExpectedChecks: plan.Checks, E2EReason: plan.Reason,
		Jobs: []timing{}, Lanes: []laneReport{}, Specs: []specTiming{}}
	if s.TestedSHA == "" {
		s.TestedSHA = run.HeadSHA
	}
	seen := map[int64]bool{}
	startedAt := run.StartedAt
	if startedAt.IsZero() {
		startedAt = run.CreatedAt
	}
	for _, job := range jobs {
		if job.ID == 0 || seen[job.ID] {
			return summary{}, fmt.Errorf("missing or duplicate job ID %d", job.ID)
		}
		seen[job.ID] = true
		if job.Name == measurementJob {
			continue
		}
		if job.Status != "completed" {
			s.CompleteSnapshot = false
		}
		seconds, err := duration(job.actionStep)
		if err != nil {
			return summary{}, err
		}
		item := timing{Name: job.Name, Conclusion: job.Conclusion, DurationSeconds: seconds, Category: category(job.Name)}
		item.Reused = job.StartedAt != nil && job.StartedAt.Before(startedAt)
		for _, step := range job.Steps {
			stepSeconds, err := duration(step)
			if err != nil {
				return summary{}, err
			}
			item.Steps = append(item.Steps, timing{Name: step.Name, Conclusion: step.Conclusion, DurationSeconds: stepSeconds})
		}
		s.Jobs = append(s.Jobs, item)
		// The run-attempt endpoint includes successful jobs inherited from earlier attempts.
		if item.Reused {
			continue
		}
		s.RunnerMinutes += seconds
		s.CategoryMinutes[item.Category] += seconds
		if job.FinishedAt != nil && job.Conclusion != "skipped" {
			elapsed := job.FinishedAt.Sub(startedAt).Seconds()
			if elapsed < 0 {
				return summary{}, fmt.Errorf("job %s finishes before the attempt starts", job.Name)
			}
			s.ElapsedSeconds = max(s.ElapsedSeconds, elapsed)
			if job.Name == "CI Required" {
				s.RequiredSeconds = &elapsed
			}
		}
	}
	s.RunnerMinutes /= 60
	for name, seconds := range s.CategoryMinutes {
		s.CategoryMinutes[name] = seconds / 60
	}
	slices.SortFunc(s.Jobs, func(a, b timing) int { return strings.Compare(a.Name, b.Name) })
	return s, nil
}

func duration(step actionStep) (float64, error) {
	if step.Conclusion == "skipped" || step.StartedAt == nil || step.FinishedAt == nil {
		return 0, nil
	}
	seconds := step.FinishedAt.Sub(*step.StartedAt).Seconds()
	if seconds < 0 {
		return 0, fmt.Errorf("negative duration for %s", step.Name)
	}
	return seconds, nil
}

func category(name string) string {
	if strings.HasPrefix(name, "E2E (") || strings.HasPrefix(name, "Fork E2E (") ||
		name == "Operator Upgrade E2E" || name == "Fork Operator Upgrade E2E" ||
		name == "Plan E2E Matrix" || name == "Generate Release E2E Matrix" {
		return "e2e"
	}
	if strings.Contains(name, "Edge") {
		return "edge"
	}
	return "other"
}

func loadReports(dir string, s *summary) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "summary.json" {
			return nil
		}
		var lane laneReport
		if err := readJSON(path, &lane); err != nil {
			return err
		}
		if lane.Lane == "" {
			return fmt.Errorf("missing lane in %s", path)
		}
		s.Lanes = append(s.Lanes, lane)
		var reports []ginkgoReport
		ginkgoPath := filepath.Join(filepath.Dir(path), "ginkgo.json")
		if err := readJSON(ginkgoPath, &reports); err != nil {
			return err
		}
		for _, report := range reports {
			for _, spec := range report.Specs {
				if spec.Text == "" || spec.RunTime <= 0 {
					continue
				}
				item := specTiming{Name: strings.Join(append(spec.Hierarchy, spec.Text), " / "),
					Lane: lane.Lane, State: spec.State, DurationSeconds: spec.RunTime.Seconds()}
				for _, labels := range append(spec.Labels, spec.LeafLabels) {
					for _, label := range labels {
						if strings.HasPrefix(label, "case:") {
							item.CaseID = strings.TrimPrefix(label, "case:")
						}
					}
				}
				s.Specs = append(s.Specs, item)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("load E2E reports: %w", err)
	}
	slices.SortFunc(s.Lanes, func(a, b laneReport) int { return strings.Compare(a.Lane, b.Lane) })
	slices.SortFunc(s.Specs, func(a, b specTiming) int {
		if a.DurationSeconds > b.DurationSeconds {
			return -1
		}
		if a.DurationSeconds < b.DurationSeconds {
			return 1
		}
		return strings.Compare(a.Lane+a.Name, b.Lane+b.Name)
	})
	return nil
}

func markdown(s summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## CI measurements\n\nRun [%d](%s), attempt %d, event `%s`, tested SHA `%s`.\n\n",
		s.Run.ID, s.Run.URL, s.Run.Attempt, s.Run.Event, s.TestedSHA)
	b.WriteString("The snapshot excludes this measurement job. " +
		"Runner-minutes sum job execution time; concurrent jobs overlap.\n\n")
	b.WriteString("Reused job results are shown, but their execution time is excluded from this attempt.\n\n")
	fmt.Fprintf(&b, "| Measure | Value |\n| --- | ---: |\n| Observed elapsed | %.2f min |\n", s.ElapsedSeconds/60)
	if s.RequiredSeconds != nil {
		fmt.Fprintf(&b, "| Time to CI Required | %.2f min |\n", *s.RequiredSeconds/60)
	}
	fmt.Fprintf(&b, "| Runner consumption | %.2f min |\n| E2E runner consumption | %.2f min |\n",
		s.RunnerMinutes, s.CategoryMinutes["e2e"])
	fmt.Fprintf(&b, "| Edge qualification consumption | %.2f min |\n| Complete job snapshot | %t |\n\n",
		s.CategoryMinutes["edge"], s.CompleteSnapshot)
	if s.E2EReason != "" {
		fmt.Fprintf(&b, "E2E exclusion: %s\n\n", s.E2EReason)
	}
	b.WriteString("### Job timings\n\n| Job | Result | Minutes | Reused |\n| --- | --- | ---: | --- |\n")
	for _, job := range s.Jobs {
		fmt.Fprintf(&b, "| %s | %s | %.2f | %t |\n", escape(job.Name), job.Conclusion,
			job.DurationSeconds/60, job.Reused)
	}
	b.WriteString("\n### E2E lanes\n\n| Lane | Selected | Passed | Failed | Suite minutes |\n" +
		"| --- | ---: | ---: | ---: | ---: |\n")
	for _, lane := range s.Lanes {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.2f |\n",
			escape(lane.Lane), lane.Selected, lane.Passed, lane.Failed, lane.DurationSeconds/60)
	}
	b.WriteString("\n### Slowest specifications\n\n| Lane | Case ID | Result | Minutes | Specification |\n" +
		"| --- | --- | --- | ---: | --- |\n")
	for _, spec := range s.Specs[:min(15, len(s.Specs))] {
		fmt.Fprintf(&b, "| %s | %s | %s | %.2f | %s |\n", escape(spec.Lane), escape(spec.CaseID),
			spec.State, spec.DurationSeconds/60, escape(spec.Name))
	}
	b.WriteString("\nThe JSON artifact contains all measured specifications and job-step timings. " +
		"Missing reports do not prove qualification.\n")
	return b.String()
}

func escape(value string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(value)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
