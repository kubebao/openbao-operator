package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogDistinguishesFixtureContractsFromGinkgoSpecs(t *testing.T) {
	for _, test := range []struct {
		name, source string
		want         bool
	}{
		{"regular Go test", "package e2e\nimport \"testing\"\nfunc TestFixture(t *testing.T) {}", false},
		{"dot import", "package e2e\nimport . \"github.com/onsi/ginkgo/v2\"", true},
		{"named import", "package e2e\nimport ginkgo \"github.com/onsi/ginkgo/v2\"", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture_test.go")
			if err := os.WriteFile(path, []byte(test.source), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := importsGinkgo(path)
			if err != nil || got != test.want {
				t.Fatalf("importsGinkgo() = %t, %v; want %t", got, err, test.want)
			}
		})
	}
	// A directory containing only fixture contracts must not launch Ginkgo.
	dir := t.TempDir()
	source := []byte("package e2e\nimport \"testing\"")
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	cases, err := collectCases(options{InputDir: dir, GinkgoPath: "ginkgo-must-not-run"})
	if err != nil || len(cases) != 0 {
		t.Fatalf("fixture-only catalog = %v, %v", cases, err)
	}
}

func TestFlattenSpecsInheritsLabelsAndSteps(t *testing.T) {
	t.Parallel()

	nodes := []outlineNode{
		{
			Name:   "Describe",
			Text:   "Upgrade Strategies",
			Labels: []string{"upgrade", "slow"},
			Nodes: []outlineNode{
				{
					Name:   "Context",
					Text:   "Blue/Green Drift",
					Labels: []string{"bluegreen"},
					Nodes: []outlineNode{
						{
							Name: "It",
							Text: "abandons an outdated green revision",
							Spec: true,
							Labels: []string{
								"case:bluegreen-target-drift-restart",
								"covers:target-revision-drift",
								"covers:stale-green-cleanup",
							},
							Nodes: []outlineNode{
								{Name: "By", Text: "starting a blue/green upgrade"},
								{Name: "By", Text: "verifying the stale green workload is cleaned up"},
							},
						},
					},
				},
			},
		},
	}

	cases := flattenSpecs(nodes, "test/e2e/Upgrade_Target_Drift_test.go", nil, nil)
	if len(cases) != 1 {
		t.Fatalf("expected 1 case, got %d", len(cases))
	}

	tc := cases[0]
	if tc.ID == "" {
		t.Fatal("expected case id to be populated")
	}
	if got, want := tc.ID, "bluegreen-target-drift-restart"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
	if got, want := tc.GeneratedID, "upgrade-target-drift-abandons-an-outdated-green-revision-19f1f2eb"; got != want {
		t.Fatalf("generated id = %q, want %q", got, want)
	}
	if got, want := tc.CaseLabel, "bluegreen-target-drift-restart"; got != want {
		t.Fatalf("case label = %q, want %q", got, want)
	}
	if got, want := strings.Join(tc.Coverage, ","), "target-revision-drift,stale-green-cleanup"; got != want {
		t.Fatalf("coverage = %q, want %q", got, want)
	}
	if got, want := strings.Join(tc.Path, " > "),
		"Upgrade Strategies > Blue/Green Drift > abandons an outdated green revision"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if got, want := strings.Join(tc.Labels, ","),
		"upgrade,slow,bluegreen,case:bluegreen-target-drift-restart,"+
			"covers:target-revision-drift,covers:stale-green-cleanup"; got != want {
		t.Fatalf("raw labels = %q, want %q", got, want)
	}
	if got, want := strings.Join(tc.DomainLabels, ","), "upgrade,slow,bluegreen"; got != want {
		t.Fatalf("domain labels = %q, want %q", got, want)
	}
	if got, want := len(tc.Steps), 2; got != want {
		t.Fatalf("steps len = %d, want %d", got, want)
	}
}

func TestCollectStepsIgnoresUndefinedAndEmptyText(t *testing.T) {
	t.Parallel()

	steps := collectSteps(outlineNode{
		Name: "It",
		Text: "example",
		Nodes: []outlineNode{
			{Name: "By", Text: "first checkpoint"},
			{Name: "By", Text: "undefined"},
			{Name: "By", Text: "  "},
			{
				Name: "Context",
				Text: "nested",
				Nodes: []outlineNode{
					{Name: "By", Text: "second checkpoint"},
				},
			},
		},
	})

	if got, want := strings.Join(steps, ","), "first checkpoint,second checkpoint"; got != want {
		t.Fatalf("steps = %q, want %q", got, want)
	}
}

func TestRenderIndexMarkdownIncludesSuiteRows(t *testing.T) {
	t.Parallel()

	suites := []suiteCatalog{
		{
			SourceFile: "test/e2e/Manager_Resilience_test.go",
			OutputFile: "suites/Manager_Resilience_test.md",
			Title:      "Manager Resilience",
			Labels:     []string{"manager", "cluster"},
			Cases: []testCase{
				{
					ID:           "manager-restart-idempotent-reconcile",
					CaseLabel:    "manager-restart-idempotent-reconcile",
					Name:         "recovers idempotently",
					DomainLabels: []string{"manager", "cluster"},
					Coverage:     []string{"controller-restart", "idempotent-reconcile"},
					Pending:      true,
				},
			},
		},
	}

	out := renderIndexMarkdown(suites[0].Cases, suites)
	if !strings.Contains(out, "[Manager Resilience](suites/Manager_Resilience_test.md)") {
		t.Fatalf("index markdown missing suite link:\n%s", out)
	}
	if !strings.Contains(out, "`test/e2e/Manager_Resilience_test.go`") {
		t.Fatalf("index markdown missing source file:\n%s", out)
	}
	if !strings.Contains(out, "| [Manager Resilience](suites/Manager_Resilience_test.md) | 1 | 1 | 1 |") {
		t.Fatalf("index markdown missing tracked/pending counts:\n%s", out)
	}
	if !strings.Contains(out, "`steps` are optional recorded checkpoints derived from literal `By(...)` text") {
		t.Fatalf("index markdown missing checkpoints note:\n%s", out)
	}
	if !strings.Contains(out, "Missing checkpoints do not imply missing coverage.") {
		t.Fatalf("index markdown missing missing-coverage note:\n%s", out)
	}
	if !strings.Contains(out, "- Explicit case IDs: `1`") {
		t.Fatalf("index markdown missing explicit case summary:\n%s", out)
	}
	if !strings.Contains(out, "| `controller-restart` | 1 |") {
		t.Fatalf("index markdown missing coverage summary:\n%s", out)
	}
}

func TestRenderSuiteMarkdownShowsMissingSteps(t *testing.T) {
	t.Parallel()

	suite := suiteCatalog{
		SourceFile: "test/e2e/anti_tamper_policy_test.go",
		OutputFile: "suites/anti_tamper_policy_test.md",
		Title:      "Security: Anti-Tamper Policy",
		Cases: []testCase{
			{
				ID:           "anti-tamper-configmap-delete-blocked",
				CaseLabel:    "anti-tamper-configmap-delete-blocked",
				GeneratedID:  "anti-tamper-policy-configmap-protection-fallback",
				Name:         "prevents deletion of the managed ConfigMap",
				Path:         []string{"Security: Anti-Tamper Policy", "prevents deletion of the managed ConfigMap"},
				DomainLabels: []string{"security", "tamper", "cluster", "slow"},
				Coverage:     []string{"anti-tamper", "configmap-protection"},
			},
		},
	}

	out := renderSuiteMarkdown(suite)
	if !strings.Contains(
		out,
		"| `anti-tamper-configmap-delete-blocked` | prevents deletion of the managed ConfigMap | active | "+
			"`anti-tamper`, `configmap-protection` | `security`, `tamper`, `cluster`, `slow` |",
	) {
		t.Fatalf("suite markdown missing case table row:\n%s", out)
	}
	if !strings.Contains(out, "State: `active`") {
		t.Fatalf("suite markdown missing case state:\n%s", out)
	}
	if !strings.Contains(out, "Generated fallback ID: `anti-tamper-policy-configmap-protection-fallback`") {
		t.Fatalf("suite markdown missing fallback id:\n%s", out)
	}
	if !strings.Contains(out, "Covers: `anti-tamper`, `configmap-protection`") {
		t.Fatalf("suite markdown missing coverage list:\n%s", out)
	}
	if strings.Contains(out, "none recorded via `By(...)`") {
		t.Fatalf("suite markdown should not call out missing checkpoints:\n%s", out)
	}
	if strings.Contains(out, "Recorded checkpoints:") {
		t.Fatalf("suite markdown should omit empty checkpoints section:\n%s", out)
	}
}

func TestRenderSuiteMarkdownShowsRecordedCheckpoints(t *testing.T) {
	t.Parallel()

	suite := suiteCatalog{
		SourceFile: "test/e2e/Manager_Resilience_test.go",
		OutputFile: "suites/Manager_Resilience_test.md",
		Title:      "Manager Resilience",
		Cases: []testCase{
			{
				ID:           "manager-restart-idempotent-reconcile",
				Name:         "recovers idempotently",
				Path:         []string{"Manager Resilience", "recovers idempotently"},
				DomainLabels: []string{"manager", "cluster"},
				Coverage:     []string{"controller-restart", "idempotent-reconcile"},
				Steps:        []string{"restarting the controller", "verifying steady state"},
			},
		},
	}

	out := renderSuiteMarkdown(suite)
	if !strings.Contains(out, "Recorded checkpoints:") {
		t.Fatalf("suite markdown missing checkpoints heading:\n%s", out)
	}
	if !strings.Contains(out, "- restarting the controller") || !strings.Contains(out, "- verifying steady state") {
		t.Fatalf("suite markdown missing checkpoints:\n%s", out)
	}
}
