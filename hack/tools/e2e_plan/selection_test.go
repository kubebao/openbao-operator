package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
)

func repoCases(t *testing.T) []catalogCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "e2e", "catalog", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []catalogCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestCISelectionUsesRecordedRouting(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	binary := filepath.Join(t.TempDir(), "ci_checks")
	cmd := exec.Command("go", "build", "-mod=vendor", "-o", binary, "./hack/tools/ci_checks")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CI planner: %v\n%s", err, output)
	}
	all := []string{"core", "controller-jwt", "security", "backup-restore", "restore-tests", "upgrade-rolling",
		"upgrade-bluegreen-core", "upgrade-bluegreen-verification", "upgrade-bluegreen-recovery",
		"upgrade-gateway", "hardened", "hsm"}
	upgrades := all[5:10]
	tests := []struct {
		name, event, head string
		labels, routes    []string
		lanes             []string
		full, pentest     bool
		blocked           bool
	}{
		{name: "source PR", lanes: all[:3], routes: []string{"e2e"}},
		{name: "backup path", lanes: all[:5], routes: []string{"e2e", "e2e_backup"}},
		{
			name:   "upgrade path",
			lanes:  append(append([]string{}, all[:3]...), upgrades...),
			routes: []string{"e2e", "e2e_upgrade"},
		},
		{name: "backup planner path", lanes: all[3:5], routes: []string{"e2e_backup"}},
		{name: "backup label", lanes: all[3:5], labels: []string{"backup"}},
		{name: "upgrade label", lanes: upgrades, labels: []string{"upgrades"}},
		{name: "security label", lanes: []string{"hardened"}, labels: []string{"security"}, pentest: true},
		{
			name:    "pentest with source and label",
			lanes:   []string{"core", "controller-jwt", "security", "hardened"},
			labels:  []string{"controller"},
			routes:  []string{"e2e"},
			pentest: true,
		},
		{
			name: "hardened path", lanes: []string{"core", "controller-jwt", "security", "hardened"},
			routes: []string{"e2e", "e2e_hardened"},
		},
		{name: "seal path", lanes: []string{"hsm"}, routes: []string{"e2e_seal"}},
		{name: "full label", lanes: all, labels: []string{"ci:full-e2e"}, full: true, pentest: true},
		{name: "main push", event: "push", lanes: all, full: true, pentest: true},
		{
			name:    "manual full",
			event:   "workflow_dispatch",
			lanes:   all,
			routes:  []string{"manual_e2e_requested", "manual_full_e2e"},
			full:    true,
			pentest: true,
		},
		{
			name:   "manual backup",
			event:  "workflow_dispatch",
			lanes:  all[3:5],
			routes: []string{"manual_e2e_requested", "manual_backup_e2e"},
		},
		{
			name:   "manual upgrade",
			event:  "workflow_dispatch",
			lanes:  upgrades,
			routes: []string{"manual_e2e_requested", "manual_upgrade_e2e"},
		},
		{
			name:    "manual hardened",
			event:   "workflow_dispatch",
			lanes:   []string{"hardened"},
			routes:  []string{"manual_e2e_requested", "manual_hardened_e2e"},
			pentest: true,
		},
		{
			name:   "manual seal",
			event:  "workflow_dispatch",
			lanes:  []string{"hsm"},
			routes: []string{"manual_e2e_requested", "manual_seal_e2e"},
		},
		{
			name:   "combined manual",
			event:  "workflow_dispatch",
			lanes:  []string{"backup-restore", "restore-tests", "hsm"},
			routes: []string{"manual_e2e_requested", "manual_backup_e2e", "manual_seal_e2e"},
		},
		{
			name:    "fork full request",
			head:    "fork/repo",
			labels:  []string{"ci:full-e2e"},
			routes:  []string{"e2e"},
			blocked: true,
		},
		{name: "docs only", routes: []string{"docs"}, blocked: true},
		{name: "operator upgrade only", routes: []string{"operator_upgrade_e2e"}, blocked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			routing := map[string]string{}
			for _, key := range strings.Fields(`manual_e2e_requested manual_full_e2e manual_backup_e2e
				manual_upgrade_e2e manual_hardened_e2e manual_seal_e2e chart workflow toolchain docs
				go_quality api_contract go_toolchain go_tests config_compat security_scan security_image
				fuzz e2e operator_upgrade_e2e e2e_backup e2e_upgrade e2e_hardened e2e_seal`) {
				routing[key] = "false"
			}
			for _, key := range test.routes {
				routing[key] = "true"
			}
			event := test.event
			if event == "" {
				event = "pull_request"
			}
			head := test.head
			if head == "" {
				head = "owner/repo"
			}
			labels := []map[string]string{}
			for _, label := range test.labels {
				labels = append(labels, map[string]string{"name": label})
			}
			writeFixtureJSON(t, filepath.Join(dir, "event.json"), map[string]any{
				"pull_request": map[string]any{
					"head": map[string]any{"repo": map[string]string{"full_name": head}}, "labels": labels,
				},
			})
			writeFixtureJSON(t, filepath.Join(dir, "routing.json"), routing)
			path := filepath.Join(dir, "plan.json")
			cmd := exec.Command(binary, "plan", "--event-name", event, "--ref", "refs/heads/main",
				"--repository", "owner/repo", "--sha", "tested-sha", "--event", filepath.Join(dir, "event.json"),
				"--routing", filepath.Join(dir, "routing.json"), "--plan", path)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("record CI plan: %v\n%s", err, output)
			}
			request, err := loadCISelection(path, "tested-sha")
			if test.blocked {
				if err == nil {
					t.Fatal("unselected or untrusted run can launch E2E")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if request.Full != test.full || request.IncludePentest != test.pentest {
				t.Fatalf("selection policy = %+v", request)
			}
			m := loadRepoManifest(t)
			matrix, err := buildGithubMatrix(m)
			if err != nil {
				t.Fatal(err)
			}
			cases := repoCases(t)
			planned, err := selectScenarios(matrix, m, cases, request)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, row := range planned.Include {
				got = append(got, row.ID)
			}
			assertLegacyCoverage(t, matrix, planned, cases, test.full, test.pentest)
			if !slices.Equal(got, test.lanes) {
				t.Fatalf("planned lanes = %v, want %v", got, test.lanes)
			}
		})
	}
}

func assertLegacyCoverage(t *testing.T, matrix, planned githubMatrix, cases []catalogCase, full, pentest bool) {
	t.Helper()
	for _, row := range planned.Include {
		// Each planned row uses the same full/PR predicate as the old
		// runner, with only the declared strategy-switch overlap removed.
		var raw matrixRow
		for _, candidate := range matrix.Include {
			if candidate.ID == row.ID {
				raw = candidate
			}
		}
		legacyFilter := raw.LabelFilter
		if !full {
			if raw.PRLabelFilter != "" {
				legacyFilter = raw.PRLabelFilter
			}
			legacyFilter = "(" + legacyFilter + ") && !lower-layer-covered"
			if raw.ExcludePentestOnDefault == "true" && !pentest {
				legacyFilter = "(" + legacyFilter + ") && !pentest"
			}
		}
		if row.LabelFilter != legacyFilter {
			t.Fatalf("lane %s effective filter changed: %s, want %s", row.ID, row.LabelFilter, legacyFilter)
		}
		legacyFilter = strings.ReplaceAll(legacyFilter, " && !case:idle-upgrade-strategy-switch", "")
		filter, err := types.ParseLabelFilter(legacyFilter)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, tc := range cases {
			if filter(tc.Labels) && (row.ID != "upgrade-rolling" || tc.ID != "idle-upgrade-strategy-switch") {
				want = append(want, tc.ID)
			}
		}
		slices.Sort(want)
		if !slices.Equal(row.ExpectedCaseIDs, want) {
			t.Fatalf("lane %s changed scenario coverage", row.ID)
		}
	}
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProfilesPreserveQualificationContexts(t *testing.T) {
	m, cases := loadRepoManifest(t), repoCases(t)
	if err := validateScenarioOwnership(m, cases); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"ci-full", "daily", "weekly-full", "release-gate"} {
		for _, rotation := range []int{0, 1} {
			t.Run(profile+string(rune('0'+rotation)), func(t *testing.T) {
				var matrix githubMatrix
				var err error
				if profile == "ci-full" {
					matrix, err = buildGithubMatrix(m)
				} else {
					matrix, err = buildGithubNightlyMatrix(m, profile, nightlyFilters{CompatibilityRotationIndex: rotation})
				}
				if err != nil {
					t.Fatal(err)
				}
				planned, err := selectScenarios(matrix, m, cases, nil)
				if err != nil {
					t.Fatal(err)
				}
				legacyContexts, plannedContexts := map[string]bool{}, map[string]bool{}
				for _, row := range matrix.Include {
					legacyFilter := strings.ReplaceAll(row.LabelFilter, " && !case:idle-upgrade-strategy-switch", "")
					filter, err := types.ParseLabelFilter(legacyFilter)
					if err != nil {
						t.Fatal(err)
					}
					for _, tc := range cases {
						if filter(tc.Labels) {
							legacyContexts[qualificationContext(row, tc.ID)] = true
						}
					}
				}
				for _, row := range planned.Include {
					for _, id := range row.ExpectedCaseIDs {
						plannedContexts[qualificationContext(row, id)] = true
					}
				}
				if !reflect.DeepEqual(legacyContexts, plannedContexts) {
					t.Fatal("qualification contexts changed after removing duplicate executions")
				}
			})
		}
	}
}

func TestWaitOverlapKeepsDedicatedJWTAndParallelRestoreWitnesses(t *testing.T) {
	m, cases := loadRepoManifest(t), repoCases(t)
	matrix, err := buildGithubMatrix(m)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := selectScenarios(matrix, m, cases, nil)
	if err != nil {
		t.Fatal(err)
	}
	jwtOwners := 0
	for _, row := range planned.Include {
		if slices.Contains(row.ExpectedCaseIDs, "controller-jwt-renewal-migration-recovery") {
			jwtOwners++
			if row.ID != "controller-jwt" || row.ParallelNodes != 1 || len(row.ExpectedCaseIDs) != 1 {
				t.Fatalf("JWT process lifetime witness shares its lane: %+v", row)
			}
		}
		if row.ID == "restore-tests" {
			want := []string{"restore-minimal-static", "restore-minimal-scheduled", "restore-minimal-retain",
				"restore-minimal-missing-source", "restore-minimal-digest", "restore-minimal-identity", "restore-minimal-cancel"}
			if row.ParallelNodes != 2 || len(row.ExpectedCaseIDs) != len(want) {
				t.Fatalf("restore witnesses no longer overlap with full case ownership: %+v", row)
			}
			for _, id := range want {
				if !slices.Contains(row.ExpectedCaseIDs, id) {
					t.Fatalf("restore deadline or lifecycle witness %s is absent", id)
				}
			}
		}
	}
	if jwtOwners != 1 {
		t.Fatalf("JWT witness has %d owners, want one", jwtOwners)
	}
}

func qualificationContext(row matrixRow, id string) string {
	return strings.Join([]string{row.Kubernetes, row.OpenBaoImage, row.Profile, row.Coverage, id}, "/")
}

func TestScenarioOwnershipRejectsDrift(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*manifest, *[]catalogCase)
		want string
	}{
		{"duplicate ID", func(_ *manifest, c *[]catalogCase) { *c = append(*c, (*c)[0]) }, "unique stable"},
		{"missing ID", func(_ *manifest, c *[]catalogCase) { (*c)[0].CaseLabel = "" }, "unique stable"},
		{
			"multiple labels",
			func(_ *manifest, c *[]catalogCase) { (*c)[0].Labels = append((*c)[0].Labels, "case:other") },
			"inconsistent",
		},
		{"focused", func(_ *manifest, c *[]catalogCase) { (*c)[0].Focused = true }, "focused or pending"},
		{"pending", func(_ *manifest, c *[]catalogCase) { (*c)[0].Pending = true }, "focused or pending"},
		{"owner missing", func(m *manifest, _ *[]catalogCase) { m.Suites = nil }, "no suite owner"},
		{
			"owner duplicated",
			func(m *manifest, _ *[]catalogCase) { m.Suites = append(m.Suites, m.Suites[0]) },
			"owned by both",
		},
		{"overlapping selectors", func(m *manifest, _ *[]catalogCase) {
			for i := range m.CILanes {
				if m.CILanes[i].ID == "upgrade-rolling" {
					m.CILanes[i].LabelFilter = strings.ReplaceAll(
						m.CILanes[i].LabelFilter, " && !case:idle-upgrade-strategy-switch", "",
					)
				}
			}
		}, "undeclared lane"},
		{"orphan", func(m *manifest, _ *[]catalogCase) {
			m.CILanes[0].LabelFilter = "nonexistent-label"
			m.CILanes[0].PRLabelFilter = ""
		}, "absent from all"},
		{"disabled automatic owner", func(m *manifest, _ *[]catalogCase) {
			enabled := false
			m.CILanes[0].IncludeInPRMatrix = &enabled
		}, "full automatic CI matrix"},
		{"manual automatic owner", func(m *manifest, _ *[]catalogCase) {
			m.CILanes[0].PRScope = "manual"
		}, "full automatic CI matrix"},
		{"PR owner drift", func(m *manifest, _ *[]catalogCase) {
			m.CILanes[0].PRLabelFilter = m.CILanes[1].LabelFilter
		}, "outside its full owner"},
		{"invalid selector", func(m *manifest, _ *[]catalogCase) { m.CILanes[0].LabelFilter = "a && (" }, "labelFilter"},
		{
			"invalid PR selector",
			func(m *manifest, _ *[]catalogCase) { m.CILanes[0].PRLabelFilter = "a && (" },
			"prLabelFilter",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, cases := loadRepoManifest(t), repoCases(t)
			test.edit(&m, &cases)
			if err := validateScenarioOwnership(m, cases); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ownership error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestPlanningRejectsExcludedAutomaticOwners(t *testing.T) {
	m := loadRepoManifest(t)
	enabled := false
	m.CILanes[0].IncludeInPRMatrix = &enabled
	matrix, err := buildGithubMatrix(m)
	if err != nil {
		t.Fatal(err)
	}
	opts := options{CatalogPath: filepath.Join("..", "..", "..", "test", "e2e", "catalog", "cases.json")}
	if err := planScenarios(&matrix, m, opts); err == nil || !strings.Contains(err.Error(), "full automatic CI matrix") {
		t.Fatalf("disabled automatic lane must block planning: %v", err)
	}
}

func TestSelectionRejectsEmptyAndRepeatedContexts(t *testing.T) {
	m, cases := loadRepoManifest(t), repoCases(t)
	matrix, err := buildGithubMatrix(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selectScenarios(matrix, m, cases, &ciSelection{Scopes: map[string]bool{}}); err == nil {
		t.Fatal("empty selection accepted")
	}
	matrix.Include[0].LabelFilter = m.CILanes[1].LabelFilter
	if _, err := selectScenarios(matrix, m, cases, nil); err == nil ||
		!strings.Contains(err.Error(), "outside its full owner") {
		t.Fatalf("nightly override ownership error = %v", err)
	}
	matrix.Include[0].LabelFilter = "nonexistent-label"
	if _, err := selectScenarios(matrix, m, cases, nil); err == nil {
		t.Fatal("empty selected lane accepted")
	}
	matrix.Include = []matrixRow{matrixRowFromLane(m.Versions, m.Parallelism, m.CILanes[0])}
	matrix.Include = append(matrix.Include, matrix.Include[0])
	if _, err := selectScenarios(matrix, m, cases, nil); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("repeated context error = %v", err)
	}
}

func TestLoadCISelectionRejectsInvalidPlans(t *testing.T) {
	valid := map[string]any{"version": 1, "sha": "sha", "checks": map[string]bool{"e2e": true, "e2e-matrix": true},
		"e2eSelection": ciSelection{Scopes: map[string]bool{
			"always": true, "backup": false, "upgrade": false, "hardened": false, "seal": false,
		}},
	}
	for _, test := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong SHA", func(p map[string]any) { p["sha"] = "wrong" }},
		{"wrong version", func(p map[string]any) { p["version"] = 2 }},
		{"missing selection", func(p map[string]any) { delete(p, "e2eSelection") }},
		{"missing full flag", func(p map[string]any) {
			p["e2eSelection"] = map[string]any{"scopes": map[string]bool{}, "includePentest": false}
		}},
		{"missing pentest flag", func(p map[string]any) {
			p["e2eSelection"] = map[string]any{"scopes": map[string]bool{}, "full": false}
		}},
		{"missing scopes", func(p map[string]any) { p["e2eSelection"] = ciSelection{} }},
		{"unselected matrix", func(p map[string]any) { p["checks"] = map[string]bool{"e2e": true} }},
		{"inconsistent full", func(p map[string]any) {
			p["e2eSelection"] = ciSelection{Full: true, Scopes: map[string]bool{"always": false}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := map[string]any{}
			for k, v := range valid {
				p[k] = v
			}
			test.edit(p)
			path := filepath.Join(t.TempDir(), "plan.json")
			writeFixtureJSON(t, path, p)
			if _, err := loadCISelection(path, "sha"); err == nil {
				t.Fatal("invalid CI plan accepted")
			}
		})
	}
}
