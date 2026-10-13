package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
)

const (
	compatibilitySmokeCoverage = "compatibility-smoke"
	matrixBoolTrue             = "true"
	matrixBoolFalse            = "false"
)

func testVersionPolicy() versionPolicy {
	return versionPolicy{
		OpenBao: openBaoVersionPolicy{
			DefaultImage: "ghcr.io/openbao/openbao:2.6.2",
		},
		StorageEmulators: storageEmulatorVersionPolicy{
			RustFSImage:  "docker.io/rustfs/rustfs@sha256:test-rustfs",
			FakeGCSImage: "docker.io/fsouza/fake-gcs-server@sha256:test-gcs",
			AzuriteImage: "mcr.microsoft.com/azure-storage/azurite@sha256:test-azurite",
		},
		Kubernetes: kubernetesVersionPolicy{
			Primary:       "1.36.1",
			Compatibility: []string{"1.34.3", "1.35.1"},
			ReleaseGate:   []string{"1.34.3", "1.35.1", "1.36.1"},
			NextCandidate: "1.37.0",
		},
	}
}

func testParallelismPolicy() parallelismPolicy {
	return parallelismPolicy{
		DefaultNodes: 1,
		MaxNodes:     4,
	}
}

func TestBuildGithubMatrixPreservesLaneConfiguration(t *testing.T) {
	t.Parallel()

	matrix, err := buildGithubMatrix(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:                       "backup-restore",
				Name:                     "Backup & Restore",
				LabelFilter:              "((dr || restore) && !openshift)",
				PRLabelFilter:            "((dr && e2e-anchor) && !openshift)",
				PRScope:                  "backup",
				TimeoutMinutes:           45,
				E2ETimeout:               "40m",
				InstallCSIHostPath:       true,
				LoadBackupExecutorImage:  true,
				LoadUpgradeExecutorImage: false,
				PreloadStorageEmulators:  []string{"rustfs", "fake-gcs", "azurite"},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildGithubMatrix() error = %v", err)
	}
	if got := len(matrix.Include); got != 1 {
		t.Fatalf("matrix rows = %d, want 1", got)
	}

	row := matrix.Include[0]
	if row.ID != "backup-restore" {
		t.Fatalf("id = %q, want backup-restore", row.ID)
	}
	if row.OpenBaoImage != "ghcr.io/openbao/openbao:2.6.2" {
		t.Fatalf("openbao image = %q, want central default", row.OpenBaoImage)
	}
	if row.KindNodeImage != "kindest/node:v1.36.1" {
		t.Fatalf("kind node image = %q, want central primary", row.KindNodeImage)
	}
	if row.PRLabelFilter != "((dr && e2e-anchor) && !openshift)" {
		t.Fatalf("pr label filter = %q, want PR-optimized filter", row.PRLabelFilter)
	}
	if row.LoadBackupExecutorImage != matrixBoolTrue {
		t.Fatalf("load backup image = %q, want true", row.LoadBackupExecutorImage)
	}
	if row.LoadUpgradeExecutorImage != matrixBoolFalse {
		t.Fatalf("load upgrade image = %q, want false", row.LoadUpgradeExecutorImage)
	}
	if row.InstallCSIHostPath != matrixBoolTrue {
		t.Fatalf("install csi hostpath = %q, want true", row.InstallCSIHostPath)
	}
	if row.PreloadStorageEmulators != matrixBoolTrue {
		t.Fatalf("preload storage emulators = %q, want true", row.PreloadStorageEmulators)
	}
	if row.PreloadRustFSImage != matrixBoolTrue ||
		row.PreloadFakeGCSImage != matrixBoolTrue ||
		row.PreloadAzuriteImage != matrixBoolTrue {
		t.Fatalf(
			"storage emulator image preload flags = %q/%q/%q, want all true",
			row.PreloadRustFSImage,
			row.PreloadFakeGCSImage,
			row.PreloadAzuriteImage,
		)
	}
	if row.RustFSImage != "docker.io/rustfs/rustfs@sha256:test-rustfs" {
		t.Fatalf("rustfs image = %q, want central storage emulator image", row.RustFSImage)
	}
	if row.FakeGCSImage != "docker.io/fsouza/fake-gcs-server@sha256:test-gcs" {
		t.Fatalf("fake gcs image = %q, want central storage emulator image", row.FakeGCSImage)
	}
	if row.AzuriteImage != "mcr.microsoft.com/azure-storage/azurite@sha256:test-azurite" {
		t.Fatalf("azurite image = %q, want central storage emulator image", row.AzuriteImage)
	}
	if row.TimeoutMinutes != 45 {
		t.Fatalf("timeout minutes = %d, want 45", row.TimeoutMinutes)
	}
	if row.ParallelNodes != 1 {
		t.Fatalf("parallel nodes = %d, want default 1", row.ParallelNodes)
	}
}

func TestBuildGithubMatrixRejectsInvalidLane(t *testing.T) {
	t.Parallel()

	_, err := buildGithubMatrix(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:             "bad lane",
				Name:           "Bad Lane",
				LabelFilter:    "lifecycle",
				PRScope:        "sometimes",
				TimeoutMinutes: 0,
				E2ETimeout:     "",
			},
		},
	})
	if err == nil {
		t.Fatalf("buildGithubMatrix() error = nil, want validation failure")
	}
	if !strings.Contains(err.Error(), "lowercase slug") || !strings.Contains(err.Error(), "prScope") {
		t.Fatalf("error = %q, want slug and prScope messages", err)
	}
}

func TestBuildGithubMatrixSkipsNonPRMatrixLane(t *testing.T) {
	t.Parallel()

	include := false
	matrix, err := buildGithubMatrix(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:                "platform-openshift",
				Name:              "OpenShift Platform",
				LabelFilter:       "openshift",
				PRScope:           "manual",
				TimeoutMinutes:    45,
				E2ETimeout:        "40m",
				IncludeInPRMatrix: &include,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildGithubMatrix() error = %v", err)
	}
	if got := len(matrix.Include); got != 0 {
		t.Fatalf("matrix rows = %d, want 0", got)
	}
}

func TestBuildGithubNightlyMatrixExpandsLaneSetsAndRows(t *testing.T) {
	t.Parallel()

	matrix, err := buildGithubNightlyMatrix(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:             "core",
				Name:           "Core Lifecycle & Manager",
				LabelFilter:    "lifecycle",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
				ParallelNodes:  2,
			},
			{
				ID:             "security",
				Name:           "Security & Tenants",
				LabelFilter:    "security",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
			},
		},
		Nightly: nightlyConfig{
			Profiles: []nightlyProfile{
				{
					ID: "daily",
					LaneSets: []nightlyLaneSet{
						{
							Coverage:   "full",
							Kubernetes: []string{"@primary"},
							Lanes:      []string{"core", "security"},
						},
					},
					Rows: []nightlyRowConfig{
						{
							Lane:        "core",
							Kubernetes:  "@compatibility",
							Coverage:    "compatibility-smoke",
							LabelFilter: "lifecycle && smoke",
						},
					},
				},
			},
		},
	}, "daily", nightlyFilters{})
	if err != nil {
		t.Fatalf("buildGithubNightlyMatrix() error = %v", err)
	}
	if got := len(matrix.Include); got != 4 {
		t.Fatalf("matrix rows = %d, want 4", got)
	}
	if matrix.Include[0].KindNodeImage != "kindest/node:v1.36.1" {
		t.Fatalf("kind node image = %q, want primary image", matrix.Include[0].KindNodeImage)
	}
	if matrix.Include[0].ParallelNodes != 2 {
		t.Fatalf("core parallel nodes = %d, want lane override 2", matrix.Include[0].ParallelNodes)
	}
	if matrix.Include[1].ParallelNodes != 1 {
		t.Fatalf("security parallel nodes = %d, want default 1", matrix.Include[1].ParallelNodes)
	}
	if matrix.Include[2].LabelFilter != "lifecycle && smoke" {
		t.Fatalf("smoke label filter = %q, want override", matrix.Include[2].LabelFilter)
	}
	if matrix.Include[2].KindNodeImage != "kindest/node:v1.34.3" {
		t.Fatalf("smoke kind node image = %q, want compatibility image", matrix.Include[2].KindNodeImage)
	}
	if matrix.Include[2].Coverage != compatibilitySmokeCoverage {
		t.Fatalf("coverage = %q, want %s", matrix.Include[2].Coverage, compatibilitySmokeCoverage)
	}
	if matrix.Include[3].KindNodeImage != "kindest/node:v1.35.1" {
		t.Fatalf("second smoke kind node image = %q, want compatibility image", matrix.Include[3].KindNodeImage)
	}
	if matrix.Include[3].LabelFilter != "lifecycle && smoke" {
		t.Fatalf("second smoke label filter = %q, want override", matrix.Include[3].LabelFilter)
	}
	if matrix.Include[3].Coverage != compatibilitySmokeCoverage {
		t.Fatalf("second smoke coverage = %q, want %s", matrix.Include[3].Coverage, compatibilitySmokeCoverage)
	}
}

func TestBuildGithubNightlyMatrixRotatesCompatibilityVersions(t *testing.T) {
	t.Parallel()

	m := manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:             "core",
				Name:           "Core",
				LabelFilter:    "lifecycle",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
			},
		},
		Nightly: nightlyConfig{
			Profiles: []nightlyProfile{
				{
					ID: "weekly-full",
					LaneSets: []nightlyLaneSet{
						{
							Coverage:            "full",
							Kubernetes:          []string{"@primary", "@compatibility"},
							RotateCompatibility: true,
							Lanes:               []string{"core"},
						},
					},
				},
			},
		},
	}

	matrix, err := buildGithubNightlyMatrix(m, "weekly-full", nightlyFilters{
		CompatibilityRotationIndex: 1,
	})
	if err != nil {
		t.Fatalf("buildGithubNightlyMatrix() error = %v", err)
	}
	if got := len(matrix.Include); got != 2 {
		t.Fatalf("matrix rows = %d, want 2", got)
	}
	if matrix.Include[0].Kubernetes != "1.36.1" || matrix.Include[1].Kubernetes != "1.35.1" {
		t.Fatalf(
			"rotated Kubernetes versions = %q, %q; want 1.36.1, 1.35.1",
			matrix.Include[0].Kubernetes,
			matrix.Include[1].Kubernetes,
		)
	}

	filtered, err := buildGithubNightlyMatrix(m, "weekly-full", nightlyFilters{
		Kubernetes:                 "1.34.3",
		CompatibilityRotationIndex: 1,
	})
	if err != nil {
		t.Fatalf("buildGithubNightlyMatrix() with explicit Kubernetes filter error = %v", err)
	}
	if got := len(filtered.Include); got != 1 {
		t.Fatalf("filtered matrix rows = %d, want 1", got)
	}
	if filtered.Include[0].Kubernetes != "1.34.3" {
		t.Fatalf("filtered Kubernetes version = %q, want 1.34.3", filtered.Include[0].Kubernetes)
	}
}

func TestBuildGithubNightlyMatrixFiltersRows(t *testing.T) {
	t.Parallel()

	m := manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:             "core",
				Name:           "Core",
				LabelFilter:    "lifecycle",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
			},
			{
				ID:             "security",
				Name:           "Security",
				LabelFilter:    "security",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
			},
		},
		Nightly: nightlyConfig{
			Profiles: []nightlyProfile{
				{
					ID: "release-gate",
					LaneSets: []nightlyLaneSet{
						{
							Coverage:   "release-gate",
							Kubernetes: []string{"@releaseGate"},
							Lanes:      []string{"core", "security"},
						},
					},
				},
			},
		},
	}

	matrix, err := buildGithubNightlyMatrix(m, "release-gate", nightlyFilters{
		Lane:       "security",
		Kubernetes: "1.35.1",
	})
	if err != nil {
		t.Fatalf("buildGithubNightlyMatrix() error = %v", err)
	}
	if got := len(matrix.Include); got != 1 {
		t.Fatalf("matrix rows = %d, want 1", got)
	}
	row := matrix.Include[0]
	if row.ID != "security" || row.Kubernetes != "1.35.1" || row.Coverage != "release-gate" {
		t.Fatalf("filtered row = %#v, want security release-gate on 1.35.1", row)
	}

	_, err = buildGithubNightlyMatrix(m, "release-gate", nightlyFilters{Kubernetes: "1.37.0"})
	if err == nil {
		t.Fatalf("buildGithubNightlyMatrix() error = nil, want empty filter failure")
	}
	if !strings.Contains(err.Error(), "produced no rows") {
		t.Fatalf("error = %q, want empty filter message", err)
	}
}

func TestBuildGithubNightlyMatrixRejectsUnknownProfile(t *testing.T) {
	t.Parallel()

	_, err := buildGithubNightlyMatrix(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:             "core",
				Name:           "Core",
				LabelFilter:    "lifecycle",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
			},
		},
		Nightly: nightlyConfig{
			Profiles: []nightlyProfile{
				{ID: "daily"},
			},
		},
	}, "weekly-full", nightlyFilters{})
	if err == nil {
		t.Fatalf("buildGithubNightlyMatrix() error = nil, want missing profile")
	}
	if !strings.Contains(err.Error(), `profile "weekly-full"`) {
		t.Fatalf("error = %q, want missing profile message", err)
	}
}

func TestRepoManifestNightlyRoutingUsesCanonicalLaneFilters(t *testing.T) {
	t.Parallel()

	m := loadRepoManifest(t)
	lanes, errs := validateLanes(m)
	if len(errs) > 0 {
		t.Fatalf("validate lanes: %s", strings.Join(errs, "; "))
	}
	securityLane, ok := lanes["security"]
	if !ok {
		t.Fatal("security lane is not defined")
	}
	if !strings.Contains(securityLane.LabelFilter, "tenant && !lifecycle") {
		t.Fatalf("security lane label filter = %q, want tenant lifecycle exclusion", securityLane.LabelFilter)
	}

	compatVersion := firstVersion(t, m.Versions.Kubernetes.Compatibility, "compatibility")
	dailyCompat := singleNightlyRow(t, m, "daily", nightlyFilters{
		Lane:       "security",
		Kubernetes: compatVersion,
	})
	if dailyCompat.Coverage != compatibilitySmokeCoverage {
		t.Fatalf("daily security coverage = %q, want %s", dailyCompat.Coverage, compatibilitySmokeCoverage)
	}
	if strings.Contains(dailyCompat.LabelFilter, "|| tenant ||") {
		t.Fatalf(
			"daily security compatibility label filter = %q, must not select all tenant lifecycle specs",
			dailyCompat.LabelFilter,
		)
	}
	if !strings.Contains(dailyCompat.LabelFilter, "tenant && !lifecycle") {
		t.Fatalf("daily security compatibility label filter = %q, want tenant lifecycle exclusion", dailyCompat.LabelFilter)
	}

	coreCompat := singleNightlyRow(t, m, "daily", nightlyFilters{
		Lane:       "core",
		Kubernetes: compatVersion,
	})
	if coreCompat.Coverage != compatibilitySmokeCoverage {
		t.Fatalf("daily core coverage = %q, want %s", coreCompat.Coverage, compatibilitySmokeCoverage)
	}
	if !strings.Contains(coreCompat.LabelFilter, "lifecycle && smoke") ||
		!strings.Contains(coreCompat.LabelFilter, "manager && smoke") {
		t.Fatalf(
			"daily core compatibility label filter = %q, want smoke-gated lifecycle and manager routing",
			coreCompat.LabelFilter,
		)
	}

	releaseGateVersion := firstVersion(t, m.Versions.Kubernetes.ReleaseGate, "releaseGate")
	assertProfileUsesCanonicalLaneFilters(t, m, lanes, "weekly-full", "full", releaseGateVersion)
	assertProfileUsesCanonicalLaneFilters(t, m, lanes, "release-gate", "release-gate", releaseGateVersion)
}

func TestRepoManifestKubernetesMatrixPolicy(t *testing.T) {
	t.Parallel()

	m := loadRepoManifest(t)

	ciMatrix, err := buildGithubMatrix(m)
	if err != nil {
		t.Fatalf("build CI matrix: %v", err)
	}
	assertKubernetesRowCounts(t, ciMatrix, map[string]int{"1.36.1": 11})

	dailyMatrix, err := buildGithubNightlyMatrix(m, "daily", nightlyFilters{})
	if err != nil {
		t.Fatalf("build daily matrix: %v", err)
	}
	assertKubernetesRowCounts(t, dailyMatrix, map[string]int{
		"1.34.3": 2,
		"1.35.1": 2,
		"1.36.1": 10,
	})

	weeklyFirst, err := buildGithubNightlyMatrix(m, "weekly-full", nightlyFilters{
		CompatibilityRotationIndex: 0,
	})
	if err != nil {
		t.Fatalf("build first weekly rotation: %v", err)
	}
	assertKubernetesRowCounts(t, weeklyFirst, map[string]int{
		"1.34.3": 10,
		"1.36.1": 10,
	})

	weeklySecond, err := buildGithubNightlyMatrix(m, "weekly-full", nightlyFilters{
		CompatibilityRotationIndex: 1,
	})
	if err != nil {
		t.Fatalf("build second weekly rotation: %v", err)
	}
	assertKubernetesRowCounts(t, weeklySecond, map[string]int{
		"1.35.1": 10,
		"1.36.1": 10,
	})

	releaseMatrix, err := buildGithubNightlyMatrix(m, "release-gate", nightlyFilters{})
	if err != nil {
		t.Fatalf("build release matrix: %v", err)
	}
	assertKubernetesRowCounts(t, releaseMatrix, map[string]int{
		"1.34.3": 10,
		"1.35.1": 10,
		"1.36.1": 10,
	})
}

func TestRepoManifestRestoreLanesSelectSeparateCatalogCases(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "e2e", "catalog", "cases.json"))
	if err != nil {
		t.Fatalf("read E2E catalog: %v", err)
	}

	var cases []struct {
		ID     string   `json:"id"`
		File   string   `json:"file"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("decode E2E catalog: %v", err)
	}

	lanes, errs := validateLanes(loadRepoManifest(t))
	if len(errs) > 0 {
		t.Fatalf("validate lanes: %s", strings.Join(errs, "; "))
	}

	for _, laneID := range []string{"backup-restore", "restore-tests"} {
		t.Run(laneID, func(t *testing.T) {
			lane, ok := lanes[laneID]
			if !ok {
				t.Fatalf("missing lane %q", laneID)
			}
			wantFile := "test/e2e/backup_restore_test.go"
			if laneID == "restore-tests" {
				wantFile = "test/e2e/restore_minimal_test.go"
			}

			for _, mode := range []string{"full", "pull-request"} {
				t.Run(mode, func(t *testing.T) {
					selector := lane.LabelFilter
					if mode == "pull-request" && lane.PRLabelFilter != "" {
						selector = lane.PRLabelFilter
					}
					matches, err := types.ParseLabelFilter(selector)
					if err != nil {
						t.Fatalf("parse selector: %v", err)
					}

					selected := 0
					for _, tc := range cases {
						want := tc.File == wantFile
						if got := matches(tc.Labels); got != want {
							t.Errorf("case %q selected = %t, want %t", tc.ID, got, want)
						}
						if want {
							selected++
						}
					}
					if selected == 0 {
						t.Fatal("catalog has no expected cases for this lane")
					}
				})
			}
		})
	}
}

func TestGithubMatrixJSONShape(t *testing.T) {
	t.Parallel()

	matrix, err := buildGithubMatrix(manifest{
		Version:     1,
		Versions:    testVersionPolicy(),
		Parallelism: testParallelismPolicy(),
		CILanes: []ciLaneConfig{
			{
				ID:             "core",
				Name:           "Core Lifecycle & Manager",
				LabelFilter:    "lifecycle",
				PRScope:        "always",
				TimeoutMinutes: 45,
				E2ETimeout:     "40m",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildGithubMatrix() error = %v", err)
	}

	data, err := json.Marshal(matrix)
	if err != nil {
		t.Fatalf("marshal matrix: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, `"include"`) {
		t.Fatalf("matrix json missing include: %s", out)
	}
	if !strings.Contains(out, `"label_filter":"lifecycle"`) {
		t.Fatalf("matrix json missing label filter: %s", out)
	}
	if !strings.Contains(out, `"parallel_nodes":1`) {
		t.Fatalf("matrix json missing parallel nodes: %s", out)
	}
	if !strings.Contains(out, `"install_csi_hostpath":"false"`) {
		t.Fatalf("matrix json missing csi hostpath install flag: %s", out)
	}
	if !strings.Contains(out, `"preload_storage_emulators":"false"`) {
		t.Fatalf("matrix json missing storage emulator preload flag: %s", out)
	}
	if !strings.Contains(out, `"preload_rustfs_image":"false"`) {
		t.Fatalf("matrix json missing rustfs preload flag: %s", out)
	}
	if !strings.Contains(out, `"rustfs_image":"docker.io/rustfs/rustfs@sha256:test-rustfs"`) {
		t.Fatalf("matrix json missing rustfs image: %s", out)
	}
}

func loadRepoManifest(t *testing.T) manifest {
	t.Helper()

	m, err := loadManifest(filepath.Join("..", "..", "..", "test", "e2e", "suites.yaml"))
	if err != nil {
		t.Fatalf("load repo manifest: %v", err)
	}
	return m
}

func TestRepoManifestIncludesSealLaneInPRMatrix(t *testing.T) {
	t.Parallel()

	matrix, err := buildGithubMatrix(loadRepoManifest(t))
	if err != nil {
		t.Fatalf("buildGithubMatrix() error = %v", err)
	}

	for _, row := range matrix.Include {
		if row.ID != "hsm" {
			continue
		}
		if row.PRScope != "seal" {
			t.Fatalf("hsm PR scope = %q, want seal", row.PRScope)
		}
		if row.ParallelNodes != 1 {
			t.Fatalf("hsm parallel nodes = %d, want 1", row.ParallelNodes)
		}
		if row.OpenBaoImage != "openbao-softhsm:dev" {
			t.Fatalf("hsm local OpenBao image = %q, want openbao-softhsm:dev", row.OpenBaoImage)
		}
		return
	}

	t.Fatal("PR matrix does not include the hsm lane")
}

func TestSealChangePathPatterns(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "hack", "ci", "e2e-seal-paths.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seal change paths: %v", err)
	}

	lines := strings.Split(string(data), "\n")
	patterns := make([]*regexp.Regexp, 0, len(lines))
	for lineNumber, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pattern, err := regexp.Compile(line)
		if err != nil {
			t.Fatalf("compile seal change path line %d: %v", lineNumber+1, err)
		}
		patterns = append(patterns, pattern)
	}
	if len(patterns) == 0 {
		t.Fatal("seal change path patterns are empty")
	}

	tests := []struct {
		path string
		want bool
	}{
		{path: "api/v1alpha1/openbaocluster_unseal_types.go", want: true},
		{path: "config/policy/openbao-validate-openbaocluster.yaml", want: true},
		{path: "internal/adapter/config/render_seal.go", want: true},
		{path: "internal/service/bootstrap/unseal_validation_hsm.go", want: true},
		{path: "internal/service/workload/seal_wiring.go", want: true},
		{path: "internal/port/openbao/pkcs11.go", want: true},
		{path: "test/e2e/images/openbao-softhsm/Dockerfile", want: true},
		{path: "test/e2e/images/pykmip-server/Dockerfile", want: true},
		{path: "test/e2e/Cluster_Unseal_KMIP_test.go", want: true},
		{path: "test/e2e/e2e_suite_test.go", want: true},
		{path: "hack/tools/e2e_plan/main.go", want: true},
		{path: ".github/actions/prepare-e2e-lane/action.yml", want: true},
		{path: ".github/workflows/ci.yml", want: true},
		{path: "website/content/docs/configure/unseal.md", want: false},
		{path: "internal/service/backup/manager.go", want: false},
		{path: "api/v1alpha1/openbaocluster_networking_types.go", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := false
			for _, pattern := range patterns {
				if pattern.MatchString(tt.path) {
					got = true
					break
				}
			}
			if got != tt.want {
				t.Fatalf("seal change path match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLifecycleChangePathPatterns(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "hack", "ci", "e2e-lifecycle-paths.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lifecycle change paths: %v", err)
	}

	lines := strings.Split(string(data), "\n")
	patterns := make([]*regexp.Regexp, 0, len(lines))
	for lineNumber, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pattern, err := regexp.Compile(line)
		if err != nil {
			t.Fatalf("compile lifecycle change path line %d: %v", lineNumber+1, err)
		}
		patterns = append(patterns, pattern)
	}
	if len(patterns) == 0 {
		t.Fatal("lifecycle change path patterns are empty")
	}

	tests := []struct {
		path string
		want bool
	}{
		{path: "internal/app/openbaocluster/adminops/reconcile.go", want: true},
		{path: "internal/platform/statusapply/openbaocluster_operationlock.go", want: true},
		{path: "internal/service/opslifecycle/lock.go", want: true},
		{path: "internal/service/backup/manager.go", want: false},
		{path: "internal/service/upgrade/rolling/manager.go", want: false},
		{path: "website/content/docs/architecture/operations.md", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := false
			for _, pattern := range patterns {
				if pattern.MatchString(tt.path) {
					got = true
					break
				}
			}
			if got != tt.want {
				t.Fatalf("lifecycle change path match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCIWorkflowWiresSealLaneFixtures(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(data)

	required := []string{
		"e2e_seal: ${{ steps.diff.outputs.e2e_seal }}",
		"changed_matches_file 'hack/ci/e2e-seal-paths.txt'",
		"BUILD_SEAL_FIXTURES:",
		"Build SoftHSM-enabled OpenBao fixture image",
		"Build PyKMIP fixture image",
		"openbao_softhsm_ref:",
		"pykmip_server_ref:",
		"kmip-server-image:",
		"openbao-image:",
		"./.github/actions/run-e2e-lane",
	}
	for _, value := range required {
		if !strings.Contains(workflow, value) {
			t.Errorf("CI workflow is missing seal-lane contract %q", value)
		}
	}
}

func TestCIWorkflowRoutesLifecycleChangesToBackupAndUpgrade(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}

	const lifecycleMatcher = "changed_matches_file 'hack/ci/e2e-lifecycle-paths.txt'"
	if count := strings.Count(string(data), lifecycleMatcher); count != 2 {
		t.Fatalf("CI lifecycle matcher count = %d, want 2 for backup and upgrade", count)
	}
}

func TestCIWorkflowRoutesRestoreTestAndPlanChangesToBackup(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}

	matcher := regexp.MustCompile(`if changed_matches '([^']+)'[^\n]*\n\s*echo "e2e_backup=true"`)
	match := matcher.FindSubmatch(data)
	if len(match) != 2 {
		t.Fatal("cannot find backup path matcher in CI workflow")
	}
	paths, err := regexp.Compile(string(match[1]))
	if err != nil {
		t.Fatalf("compile backup path matcher: %v", err)
	}

	for _, path := range []string{
		"test/e2e/suites.yaml",
		"test/e2e/restore_minimal_test.go",
		"test/e2e/backup_restore_test.go",
		"hack/tools/e2e_plan/main.go",
		"hack/tools/e2e_plan/main_test.go",
		"hack/tools/e2e_manifest/main.go",
	} {
		if !paths.MatchString(path) {
			t.Errorf("%q does not route to backup/restore E2E lanes", path)
		}
	}
}

func TestNightlyWorkflowWiresCompatibilityRotation(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", ".github", "workflows", "nightly.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read nightly workflow: %v", err)
	}
	workflow := string(data)

	required := []string{
		`week_number="$(date -u +%V)"`,
		`compatibility_rotation_index="$((10#${week_number}))"`,
		`--compatibility-rotation-index "${E2E_COMPATIBILITY_ROTATION_INDEX}"`,
	}
	for _, value := range required {
		if !strings.Contains(workflow, value) {
			t.Errorf("nightly workflow is missing compatibility rotation contract %q", value)
		}
	}
}

func singleNightlyRow(t *testing.T, m manifest, profile string, filters nightlyFilters) matrixRow {
	t.Helper()

	matrix, err := buildGithubNightlyMatrix(m, profile, filters)
	if err != nil {
		t.Fatalf("build %s nightly matrix: %v", profile, err)
	}
	if got := len(matrix.Include); got != 1 {
		t.Fatalf("%s nightly matrix rows = %d, want 1", profile, got)
	}
	return matrix.Include[0]
}

func assertProfileUsesCanonicalLaneFilters(
	t *testing.T,
	m manifest,
	lanes map[string]ciLaneConfig,
	profile string,
	coverage string,
	kubernetes string,
) {
	t.Helper()

	matrix, err := buildGithubNightlyMatrix(m, profile, nightlyFilters{Kubernetes: kubernetes})
	if err != nil {
		t.Fatalf("build %s nightly matrix: %v", profile, err)
	}
	if len(matrix.Include) == 0 {
		t.Fatalf("%s nightly matrix produced no rows", profile)
	}
	for _, row := range matrix.Include {
		if row.Coverage != coverage {
			t.Fatalf("%s row %q coverage = %q, want %q", profile, row.ID, row.Coverage, coverage)
		}
		lane, ok := lanes[row.ID]
		if !ok {
			t.Fatalf("%s row references unknown lane %q", profile, row.ID)
		}
		if row.LabelFilter != lane.LabelFilter {
			t.Fatalf(
				"%s row %q label filter = %q, want canonical lane filter %q",
				profile,
				row.ID,
				row.LabelFilter,
				lane.LabelFilter,
			)
		}
	}
}

func firstVersion(t *testing.T, versions []string, field string) string {
	t.Helper()

	if len(versions) == 0 {
		t.Fatalf("versions.kubernetes.%s must not be empty", field)
	}
	return versions[0]
}

func assertKubernetesRowCounts(t *testing.T, matrix githubMatrix, want map[string]int) {
	t.Helper()

	got := make(map[string]int)
	for _, row := range matrix.Include {
		got[row.Kubernetes]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Kubernetes row counts = %v, want %v", got, want)
	}
}
