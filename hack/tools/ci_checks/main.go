// ci_checks records job expectations and validates the completed CI dependency graph.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
)

const (
	planVersion           = 1
	eventPullRequest      = "pull_request"
	eventPush             = "push"
	eventWorkflowDispatch = "workflow_dispatch"
	routingTrue           = "true"
	routingFalse          = "false"
	changesJob            = "changes"
)

type checkPlan struct {
	Version int             `json:"version"`
	SHA     string          `json:"sha"`
	Event   string          `json:"event"`
	Checks  map[string]bool `json:"checks"`
	Reason  string          `json:"e2eExclusionReason,omitempty"`
	E2E     e2eSelection    `json:"e2eSelection"`
}

// e2eSelection records lane routing once, before the matrix is expanded.
type e2eSelection struct {
	Full           bool            `json:"full"`
	Scopes         map[string]bool `json:"scopes"`
	IncludePentest bool            `json:"includePentest"`
}

type eventPayload struct {
	PullRequest struct {
		Head struct {
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"pull_request"`
}

type jobResult struct {
	Result string `json:"result"`
}

var routingKeys = []string{
	"manual_e2e_requested", "manual_full_e2e", "manual_backup_e2e", "manual_upgrade_e2e",
	"manual_hardened_e2e", "manual_seal_e2e", "chart", "workflow", "toolchain", "docs",
	"go_quality", "api_contract", "go_toolchain", "go_tests", "config_compat", "security_scan",
	"security_image", "fuzz", "e2e", "operator_upgrade_e2e", "e2e_backup", "e2e_upgrade",
	"e2e_hardened", "e2e_seal",
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ci_checks: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected plan or check command")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	planPath := flags.String("plan", "", "job expectation JSON path")
	sha := flags.String("sha", "", "tested source SHA")
	eventName := flags.String("event-name", "", "GitHub event name")
	eventPath := flags.String("event", "", "GitHub event JSON path")
	ref := flags.String("ref", "", "GitHub ref")
	repository := flags.String("repository", "", "GitHub repository name")
	routingPath := flags.String("routing", "", "change-detection outputs JSON path")
	needsPath := flags.String("needs", "", "completed GitHub needs JSON path")
	outputPath := flags.String("github-output", "", "GitHub step output path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *planPath == "" || *sha == "" || flags.NArg() != 0 {
		return errors.New("--plan and --sha are required; positional arguments are not supported")
	}
	switch args[0] {
	case "plan":
		var event eventPayload
		var routing map[string]string
		if err := readJSON(*eventPath, &event); err != nil {
			return err
		}
		if err := readJSON(*routingPath, &routing); err != nil {
			return err
		}
		plan, err := buildPlan(*eventName, *ref, *repository, *sha, event, routing)
		if err != nil {
			return err
		}
		if err := writeJSON(*planPath, plan); err != nil {
			return err
		}
		if *outputPath == "" {
			return nil
		}
		checks, err := json.Marshal(plan.Checks)
		if err != nil {
			return fmt.Errorf("encode check expectations: %w", err)
		}
		f, err := os.OpenFile(*outputPath, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open GitHub output: %w", err)
		}
		_, writeErr := fmt.Fprintf(f, "expected_checks=%s\n", checks)
		return errors.Join(writeErr, f.Close())
	case "check":
		var plan checkPlan
		var results map[string]jobResult
		if err := readJSON(*planPath, &plan); err != nil {
			return err
		}
		if err := readJSON(*needsPath, &results); err != nil {
			return err
		}
		if err := validateResults(plan, *sha, results); err != nil {
			return err
		}
		fmt.Println("Required CI graph matches the recorded job expectations.")
		return nil
	default:
		return fmt.Errorf("unsupported command %q", args[0])
	}
}

func buildPlan(
	eventName, ref, repository, sha string,
	event eventPayload, routing map[string]string,
) (checkPlan, error) {
	if eventName != eventPullRequest && eventName != eventPush && eventName != eventWorkflowDispatch {
		return checkPlan{}, fmt.Errorf("unsupported event %q", eventName)
	}
	if repository == "" || sha == "" {
		return checkPlan{}, errors.New("repository and source SHA are required")
	}
	if eventName == eventPullRequest && event.PullRequest.Head.Repo.FullName == "" {
		return checkPlan{}, errors.New("pull request head repository is missing")
	}
	routes, err := parseRouting(eventName, routing)
	if err != nil {
		return checkPlan{}, err
	}
	mainPush := eventName == eventPush && ref == "refs/heads/main"
	selection := buildE2ESelection(eventName, mainPush, event, routes)
	trustedImages := eventName != eventPullRequest || event.PullRequest.Head.Repo.FullName == repository
	e2e := false
	for _, selected := range selection.Scopes {
		e2e = e2e || (trustedImages && selected)
	}
	build := e2e || (trustedImages && routes["operator_upgrade_e2e"])
	checks := map[string]bool{
		changesJob:        true,
		"api-contract":    mainPush || eventName == eventWorkflowDispatch || routes["api_contract"],
		"helm-e2e-smoke":  routes["chart"],
		"build-artifacts": build, "build-e2e-images": build, "operator-upgrade-e2e": build,
		"e2e-matrix": e2e, "e2e": e2e,
	}
	for job, route := range map[string]string{
		"security": "security_scan", "dependency-licenses": "security_scan", "security-image": "security_image",
		"workflow-lint": "workflow", "lint": "go_quality", "verify-fmt": "go_quality", "tidy-check": "go_quality",
		"go-toolchain-sync": "go_toolchain", "verify-vendor": "go_quality", "verify-generated": "go_quality",
		"test-unit": "go_tests", "test-integration": "go_tests", "fuzz": "fuzz",
		"openbao-config-compat": "config_compat", "docs": "docs", "helm": "chart",
	} {
		checks[job] = mainPush || routes[route]
	}
	plan := checkPlan{Version: planVersion, SHA: sha, Event: eventName, Checks: checks}
	plan.E2E = selection
	if !trustedImages {
		plan.Reason = "Fork pull requests do not publish E2E images; " +
			"cluster qualification requires a maintainer or local run."
	} else if !e2e {
		plan.Reason = "Current path, label, and manual-input policy does not select the Ginkgo E2E matrix."
	}
	return plan, nil
}

func buildE2ESelection(eventName string, mainPush bool, event eventPayload, routes map[string]bool) e2eSelection {
	labels := map[string]bool{}
	if eventName == eventPullRequest {
		for _, label := range event.PullRequest.Labels {
			labels[label.Name] = true
		}
	}
	full := mainPush || routes["manual_full_e2e"] || labels["ci:full-e2e"]
	hardened := routes["manual_hardened_e2e"] || labels["security"] || labels["provisioner"] ||
		labels["admission"] || labels["controller"]
	return e2eSelection{
		Full: full,
		Scopes: map[string]bool{
			"always":   full || routes["e2e"],
			"backup":   full || routes["manual_backup_e2e"] || labels["backup"] || routes["e2e_backup"],
			"upgrade":  full || routes["manual_upgrade_e2e"] || labels["upgrades"] || routes["e2e_upgrade"],
			"hardened": full || hardened || routes["e2e_hardened"],
			"seal":     full || routes["manual_seal_e2e"] || routes["e2e_seal"],
		},
		IncludePentest: full || hardened,
	}
}

func parseRouting(eventName string, routing map[string]string) (map[string]bool, error) {
	routes := make(map[string]bool, len(routingKeys))
	for _, key := range routingKeys {
		value := routing[key]
		if value != routingTrue && value != routingFalse {
			return nil, fmt.Errorf("routing output %s = %q, want true or false", key, value)
		}
		routes[key] = value == routingTrue
	}
	manual := routes["manual_full_e2e"] || routes["manual_backup_e2e"] || routes["manual_upgrade_e2e"] ||
		routes["manual_hardened_e2e"] || routes["manual_seal_e2e"]
	if routes["manual_e2e_requested"] != manual || (manual && eventName != eventWorkflowDispatch) {
		return nil, errors.New("manual E2E outputs are inconsistent with the event or requested suites")
	}
	return routes, nil
}

func validateResults(plan checkPlan, sha string, results map[string]jobResult) error {
	if plan.Version != planVersion || plan.SHA != sha || !plan.Checks[changesJob] {
		return errors.New("missing, incompatible, or source-mismatched job expectations")
	}
	var failures []string
	for job, required := range plan.Checks {
		result, exists := results[job]
		if !exists {
			failures = append(failures, job+": missing result")
		} else if result.Result != "success" && (required || result.Result != "skipped") {
			failures = append(failures, fmt.Sprintf("%s: %s (required=%t)", job, result.Result, required))
		}
	}
	for job := range results {
		if _, exists := plan.Checks[job]; !exists {
			failures = append(failures, job+": missing expectation")
		}
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		return fmt.Errorf("required CI graph failures:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
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

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
