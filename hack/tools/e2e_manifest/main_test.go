package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempE2EFile(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("package e2e\n"), 0o644); err != nil {
		t.Fatalf("write temp e2e file: %v", err)
	}
	return filepath.ToSlash(path)
}

func testCILane(id string) ciLaneConfig {
	return ciLaneConfig{
		ID:             id,
		Name:           id,
		LabelFilter:    id,
		PRScope:        "always",
		TimeoutMinutes: 45,
		E2ETimeout:     "40m",
	}
}

func testVersionPolicy() versionPolicy {
	return versionPolicy{
		OpenBao: openBaoVersionPolicy{
			DefaultImage: "ghcr.io/openbao/openbao:2.6.2",
		},
		Kubernetes: kubernetesVersionPolicy{
			Primary:       "1.35.1",
			Compatibility: []string{"1.34.3"},
			ReleaseGate:   []string{"1.34.3", "1.35.1"},
			NextCandidate: "1.36.0",
		},
	}
}

func testParallelismPolicy() parallelismPolicy {
	return parallelismPolicy{
		DefaultNodes: 1,
		MaxNodes:     4,
	}
}

func testNightlyPlan(lane string) nightlyPlanConfig {
	return nightlyPlanConfig{
		Profiles: []nightlyProfile{
			{
				ID: "daily",
				LaneSets: []nightlyLaneSet{
					{
						Coverage:   "full",
						Kubernetes: []string{"@primary"},
						Lanes:      []string{lane},
					},
				},
			},
		},
	}
}

func TestValidateManifestAcceptsCatalogMatchedSuite(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "Cluster_Lifecycle_test.go")

	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"Cluster Lifecycle", "creates a cluster"},
			CaseLabel:    "cluster-lifecycle-create",
			DomainLabels: []string{"lifecycle", "cluster"},
			Coverage:     []string{"lifecycle"},
		},
	})

	err := validateManifest(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			testCILane("core"),
		},
		Nightly: testNightlyPlan("core"),
		Suites: []manifestSuite{
			{
				ID:        "cluster-lifecycle",
				Title:     "Cluster Lifecycle",
				Owner:     "core",
				RiskTier:  "critical",
				Isolation: "shared-cluster",
				Files:     []string{file},
				Labels:    []string{"cluster", "lifecycle"},
				Coverage:  []string{"lifecycle"},
				Runtime: suiteRuntime{
					Observed: "1m",
					Budget:   "2m",
				},
				CI: suiteCI{
					Lanes:       []string{"core"},
					PullRequest: "changed-paths",
				},
				Nightly: suiteNightly{
					Policy: "primary-version-full",
				},
			},
		},
	}, facts, options{})
	if err != nil {
		t.Fatalf("validateManifest() error = %v", err)
	}
}

func TestValidateManifestRejectsInvalidRuntimeMetadata(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "Cluster_Lifecycle_test.go")

	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"Cluster Lifecycle", "creates a cluster"},
			DomainLabels: []string{"lifecycle", "cluster"},
		},
	})

	err := validateManifest(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			testCILane("core"),
		},
		Nightly: testNightlyPlan("core"),
		Suites: []manifestSuite{
			{
				ID:        "cluster-lifecycle",
				Title:     "Cluster Lifecycle",
				Owner:     "core",
				RiskTier:  "critical",
				Isolation: "shared-cluster",
				Files:     []string{file},
				Labels:    []string{"cluster", "lifecycle"},
				Runtime: suiteRuntime{
					Observed: "not-a-duration",
					Budget:   "2m",
				},
				CI: suiteCI{
					Lanes:       []string{"core"},
					PullRequest: "changed-paths",
				},
				Nightly: suiteNightly{
					Policy: "primary-version-full",
				},
			},
		},
	}, facts, options{})
	if err == nil {
		t.Fatalf("validateManifest() error = nil, want runtime validation failure")
	}
	if !strings.Contains(err.Error(), "runtime.observed") {
		t.Fatalf("error = %q, want runtime observed message", err)
	}
}

func TestValidateManifestRejectsLabelDrift(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "Security_Guardrails_test.go")

	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"Security Guardrails", "checks RBAC"},
			DomainLabels: []string{"security", "rbac"},
		},
	})

	err := validateManifest(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			testCILane("security"),
		},
		Nightly: testNightlyPlan("security"),
		Suites: []manifestSuite{
			{
				ID:        "security-guardrails",
				Title:     "Security Guardrails",
				Owner:     "security",
				RiskTier:  "critical",
				Isolation: "global-mutator",
				Files:     []string{file},
				Labels:    []string{"security"},
				CI: suiteCI{
					Lanes:       []string{"security"},
					PullRequest: "changed-paths",
				},
				Nightly: suiteNightly{
					Policy: "primary-version-full",
				},
			},
		},
	}, facts, options{})
	if err == nil {
		t.Fatalf("validateManifest() error = nil, want label drift")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Fatalf("error = %q, want labels message", err)
	}
}

func TestValidateManifestRejectsDuplicateFileOwnership(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "Operator_Manager_test.go")

	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"Manager", "starts"},
			DomainLabels: []string{"manager"},
		},
	})

	base := manifestSuite{
		Title:     "Manager",
		Owner:     "manager",
		RiskTier:  "critical",
		Isolation: "shared-cluster",
		Files:     []string{file},
		Labels:    []string{"manager"},
		CI: suiteCI{
			Lanes:       []string{"core"},
			PullRequest: "changed-paths",
		},
		Nightly: suiteNightly{
			Policy: "primary-version-full",
		},
	}
	first := base
	first.ID = "operator-manager"
	second := base
	second.ID = "operator-manager-copy"

	err := validateManifest(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			testCILane("core"),
		},
		Nightly: testNightlyPlan("core"),
		Suites:  []manifestSuite{first, second},
	}, facts, options{})
	if err == nil {
		t.Fatalf("validateManifest() error = nil, want duplicate file ownership")
	}
	if !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("error = %q, want duplicate ownership message", err)
	}
}

func TestValidateManifestCanRequireExplicitCaseIDs(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "backup_restore_test.go")

	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"DR: Storage Providers Backup & Restore", "backs up"},
			DomainLabels: []string{"dr", "backup"},
		},
	})

	err := validateManifest(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			testCILane("backup-restore"),
		},
		Nightly: testNightlyPlan("backup-restore"),
		Suites: []manifestSuite{
			{
				ID:        "backup-restore",
				Title:     "DR: Storage Providers Backup & Restore",
				Owner:     "backup-restore",
				RiskTier:  "critical",
				Isolation: "global-mutator",
				Files:     []string{file},
				Labels:    []string{"backup", "dr"},
				CI: suiteCI{
					Lanes:       []string{"backup-restore"},
					PullRequest: "changed-paths",
				},
				Nightly: suiteNightly{
					Policy: "primary-version-full",
				},
			},
		},
	}, facts, options{RequireCaseIDs: true})
	if err == nil {
		t.Fatalf("validateManifest() error = nil, want missing case IDs")
	}
	if !strings.Contains(err.Error(), "explicit case IDs") {
		t.Fatalf("error = %q, want explicit case IDs message", err)
	}
}

func TestValidateManifestRejectsInvalidNightlyLane(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "Operator_Manager_test.go")
	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"Manager", "starts"},
			DomainLabels: []string{"manager"},
		},
	})

	err := validateManifest(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			testCILane("core"),
		},
		Nightly: testNightlyPlan("missing-lane"),
		Suites: []manifestSuite{
			{
				ID:        "operator-manager",
				Title:     "Manager",
				Owner:     "manager",
				RiskTier:  "critical",
				Isolation: "shared-cluster",
				Files:     []string{file},
				Labels:    []string{"manager"},
				CI: suiteCI{
					Lanes:       []string{"core"},
					PullRequest: "changed-paths",
				},
				Nightly: suiteNightly{
					Policy: "primary-version-full",
				},
			},
		},
	}, facts, options{})
	if err == nil {
		t.Fatalf("validateManifest() error = nil, want nightly lane failure")
	}
	if !strings.Contains(err.Error(), "undefined lane") {
		t.Fatalf("error = %q, want undefined lane message", err)
	}
}

func TestValidateManifestRequiresSafeIsolationForParallelLane(t *testing.T) {
	t.Parallel()

	file := writeTempE2EFile(t, "Cluster_Lifecycle_test.go")
	facts := buildSuiteFacts([]catalogCase{
		{
			File:         file,
			Path:         []string{"Cluster Lifecycle", "creates a cluster"},
			DomainLabels: []string{"cluster", "lifecycle"},
		},
	})

	lane := testCILane("core")
	lane.ParallelNodes = 2
	suite := manifestSuite{
		ID:        "cluster-lifecycle",
		Title:     "Cluster Lifecycle",
		Owner:     "core",
		RiskTier:  "critical",
		Isolation: "shared-cluster",
		Files:     []string{file},
		Labels:    []string{"cluster", "lifecycle"},
		CI: suiteCI{
			Lanes:       []string{"core"},
			PullRequest: "changed-paths",
		},
		Nightly: suiteNightly{
			Policy: "primary-version-full",
		},
	}
	base := manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes:     []ciLaneConfig{lane},
		Nightly:     testNightlyPlan("core"),
		Suites:      []manifestSuite{suite},
	}

	err := validateManifest(base, facts, options{})
	if err == nil {
		t.Fatalf("validateManifest() error = nil, want parallel isolation failure")
	}
	if !strings.Contains(err.Error(), "requires suite cluster-lifecycle isolation to be parallel-safe or serial") {
		t.Fatalf("error = %q, want parallel isolation message", err)
	}

	base.Suites[0].Isolation = "parallel-safe"
	if err := validateManifest(base, facts, options{}); err != nil {
		t.Fatalf("validateManifest() with parallel-safe isolation error = %v", err)
	}
}

func TestLoadCatalogRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`{not-json}`), 0o644); err != nil {
		t.Fatalf("write malformed catalog: %v", err)
	}

	_, err := loadCatalog(path)
	if err == nil {
		t.Fatalf("loadCatalog() error = nil, want parse failure")
	}
	if !strings.Contains(err.Error(), "parse catalog") {
		t.Fatalf("error = %q, want parse catalog message", err)
	}
}

func TestValidateNightlyUpgradeSequence(t *testing.T) {
	for _, tc := range []struct {
		sequence string
		valid    bool
	}{
		{"2.5.5,2.6.4,2.7.1", true},
		{"2.6.4,2.7.0,2.7.1", true},
		{"", false},
		{"2.6.4,2.7.1", false},
		{"2.6.4,2.6.4,2.7.1", false},
	} {
		t.Run(tc.sequence, func(t *testing.T) {
			plan := nightlyPlanConfig{Profiles: []nightlyProfile{{
				ID: "release-gate", Rows: []nightlyRowConfig{{
					Lane: "upgrade", Kubernetes: "@primary", Coverage: "upgrade-sequence-latest",
					UpgradeSequence: tc.sequence,
				}},
			}}}
			lanes := map[string]ciLaneConfig{"upgrade": testCILane("upgrade")}
			errs := validateNightlyPlan(plan, testVersionPolicy().Kubernetes, lanes)
			if (len(errs) == 0) != tc.valid {
				t.Fatalf("errors = %v, want valid=%t", errs, tc.valid)
			}
		})
	}
}
