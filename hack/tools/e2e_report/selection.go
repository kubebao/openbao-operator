package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/onsi/ginkgo/v2/types"
)

func validateSelection(out *summary, reports []types.Report, opts options) error {
	data, err := os.ReadFile(opts.ExpectedCasesPath)
	if err != nil {
		return fmt.Errorf("read expected cases: %w", err)
	}
	if err := json.Unmarshal(data, &out.ExpectedCaseIDs); err != nil {
		return fmt.Errorf("decode expected cases: %w", err)
	}
	if len(out.ExpectedCaseIDs) == 0 {
		return fmt.Errorf("expected cases must not be empty")
	}
	expected := map[string]bool{}
	for _, id := range out.ExpectedCaseIDs {
		if strings.TrimSpace(id) == "" || expected[id] {
			return fmt.Errorf("expected case ID %q is empty or duplicated", id)
		}
		expected[id] = true
	}
	if strings.TrimSpace(opts.Selector) == "" {
		return fmt.Errorf("--selector is required with --expected-cases")
	}
	filter, err := types.ParseLabelFilter(opts.Selector)
	if err != nil {
		return fmt.Errorf("parse planned selector: %w", err)
	}
	if !out.SuiteSucceeded {
		out.SelectionErrors = append(out.SelectionErrors, "Ginkgo suite did not succeed")
	}
	if out.SpecsThatWillRun != len(expected) {
		out.SelectionErrors = append(out.SelectionErrors,
			fmt.Sprintf("Ginkgo selected %d scenarios; plan requires %d", out.SpecsThatWillRun, len(expected)))
	}
	actual := map[string]int{}
	for _, report := range reports {
		if report.SuiteConfig.DryRun {
			out.SelectionErrors = append(out.SelectionErrors, "dry-run reports do not qualify scenarios")
		}
		if report.SuiteConfig.LabelFilter != opts.Selector {
			out.SelectionErrors = append(out.SelectionErrors, "Ginkgo selector differs from the planned selector")
		}
		for _, spec := range report.SpecReports {
			if isSuiteNode(spec) {
				continue
			}
			// Ginkgo also reports specs excluded by the label filter. Selected
			// specs skipped before execution still count as missing coverage.
			if !filter(spec.Labels()) && !includeInRuntimeAggregates(spec) && !isSelectedSkip(spec) {
				continue
			}
			id, err := reportCaseID(spec.Labels())
			if err != nil {
				out.SelectionErrors = append(out.SelectionErrors, fmt.Sprintf("%s: %v", spec.FullText(), err))
				continue
			}
			actual[id]++
			if !expected[id] {
				out.SelectionErrors = append(out.SelectionErrors, "unplanned scenario: "+id)
			} else if spec.State != types.SpecStatePassed {
				out.SelectionErrors = append(out.SelectionErrors, fmt.Sprintf("%s: %s", id, spec.State))
			} else {
				out.PassedCaseIDs = append(out.PassedCaseIDs, id)
			}
		}
	}
	for id := range expected {
		if actual[id] == 0 {
			out.SelectionErrors = append(out.SelectionErrors, "missing scenario: "+id)
		}
	}
	for id, count := range actual {
		if count > 1 {
			out.SelectionErrors = append(out.SelectionErrors, fmt.Sprintf("scenario %s appears %d times", id, count))
		}
	}
	slices.Sort(out.ExpectedCaseIDs)
	out.PassedCaseIDs = uniqueStrings(out.PassedCaseIDs)
	out.SelectionErrors = uniqueStrings(out.SelectionErrors)
	return nil
}

func reportCaseID(labels []string) (string, error) {
	var ids []string
	for _, label := range labels {
		if strings.HasPrefix(label, "case:") {
			ids = append(ids, strings.TrimPrefix(label, "case:"))
		}
	}
	if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
		return "", fmt.Errorf("selected scenario requires exactly one case: label")
	}
	return ids[0], nil
}
