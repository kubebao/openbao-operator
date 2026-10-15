package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultManifestPath = "test/e2e/suites.yaml"
	defaultCatalogPath  = "test/e2e/catalog/cases.json"
)

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type options struct {
	ManifestPath   string
	CatalogPath    string
	RequireCaseIDs bool
}

type manifest struct {
	Version     int               `yaml:"version"`
	Versions    versionPolicy     `yaml:"versions"`
	Parallelism parallelismPolicy `yaml:"parallelism"`
	CILanes     []ciLaneConfig    `yaml:"ciLanes"`
	Nightly     nightlyPlanConfig `yaml:"nightly"`
	Suites      []manifestSuite   `yaml:"suites"`
}

type versionPolicy struct {
	OpenBao    openBaoVersionPolicy    `yaml:"openbao"`
	Kubernetes kubernetesVersionPolicy `yaml:"kubernetes"`
}

type openBaoVersionPolicy struct {
	DefaultImage string `yaml:"defaultImage"`
}

type kubernetesVersionPolicy struct {
	Primary       string   `yaml:"primary"`
	Compatibility []string `yaml:"compatibility"`
	ReleaseGate   []string `yaml:"releaseGate"`
	NextCandidate string   `yaml:"nextCandidate"`
}

type parallelismPolicy struct {
	DefaultNodes int `yaml:"defaultNodes"`
	MaxNodes     int `yaml:"maxNodes"`
}

type ciLaneConfig struct {
	NetworkProfile           string `yaml:"networkProfile"`
	ID                       string `yaml:"id"`
	Name                     string `yaml:"name"`
	LabelFilter              string `yaml:"labelFilter"`
	PRLabelFilter            string `yaml:"prLabelFilter"`
	PRScope                  string `yaml:"prScope"`
	TimeoutMinutes           int    `yaml:"timeoutMinutes"`
	E2ETimeout               string `yaml:"e2eTimeout"`
	ParallelNodes            int    `yaml:"parallelNodes"`
	IncludeInPRMatrix        *bool  `yaml:"includeInPRMatrix"`
	ExcludePentestOnDefault  bool   `yaml:"excludePentestOnDefaultPR"`
	HardenedSigned           bool   `yaml:"hardenedSigned"`
	OpenBaoImage             string `yaml:"openbaoImage"`
	HardenedInitImage        string `yaml:"hardenedInitImage"`
	HardenedUpgradeImage     string `yaml:"hardenedUpgradeExecutorImage"`
	InstallCSIHostPath       bool   `yaml:"installCSIHostPath"`
	LoadBackupExecutorImage  bool   `yaml:"loadBackupExecutorImage"`
	LoadUpgradeExecutorImage bool   `yaml:"loadUpgradeExecutorImage"`
	PreloadUpgradeImages     bool   `yaml:"preloadUpgradeImages"`
	PreloadHardenedAssets    bool   `yaml:"preloadHardenedAssets"`
}

type nightlyPlanConfig struct {
	Profiles []nightlyProfile `yaml:"profiles"`
}

type nightlyProfile struct {
	ID          string             `yaml:"id"`
	Description string             `yaml:"description"`
	LaneSets    []nightlyLaneSet   `yaml:"laneSets"`
	Rows        []nightlyRowConfig `yaml:"rows"`
}

type nightlyLaneSet struct {
	Coverage       string   `yaml:"coverage"`
	Kubernetes     []string `yaml:"kubernetes"`
	Lanes          []string `yaml:"lanes"`
	TimeoutMinutes int      `yaml:"timeoutMinutes"`
	E2ETimeout     string   `yaml:"e2eTimeout"`
}

type nightlyRowConfig struct {
	NetworkProfile string `yaml:"networkProfile"`
	Lane           string `yaml:"lane"`
	Kubernetes     string `yaml:"kubernetes"`
	Coverage       string `yaml:"coverage"`
	LabelFilter    string `yaml:"labelFilter"`
	TimeoutMinutes int    `yaml:"timeoutMinutes"`
	E2ETimeout     string `yaml:"e2eTimeout"`
	OpenBaoImage   string `yaml:"openbaoImage"`
}

type manifestSuite struct {
	ID        string       `yaml:"id"`
	Title     string       `yaml:"title"`
	Owner     string       `yaml:"owner"`
	RiskTier  string       `yaml:"riskTier"`
	Isolation string       `yaml:"isolation"`
	Files     []string     `yaml:"files"`
	Labels    []string     `yaml:"labels"`
	Coverage  []string     `yaml:"coverage"`
	Runtime   suiteRuntime `yaml:"runtime"`
	CI        suiteCI      `yaml:"ci"`
	Nightly   suiteNightly `yaml:"nightly"`
}

type suiteRuntime struct {
	Observed string `yaml:"observed"`
	Budget   string `yaml:"budget"`
}

type suiteCI struct {
	Lanes       []string `yaml:"lanes"`
	PullRequest string   `yaml:"pullRequest"`
}

type suiteNightly struct {
	Policy string `yaml:"policy"`
}

type catalogCase struct {
	ID           string   `json:"id"`
	CaseLabel    string   `json:"caseLabel"`
	File         string   `json:"file"`
	Path         []string `json:"path"`
	DomainLabels []string `json:"domainLabels"`
	Coverage     []string `json:"coverage"`
	Pending      bool     `json:"pending"`
}

type suiteFacts struct {
	File           string
	Title          string
	Labels         []string
	Coverage       []string
	Cases          int
	ExplicitCaseID int
	Pending        int
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_manifest: %v\n", err)
		os.Exit(2)
	}

	m, err := loadManifest(opts.ManifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_manifest: %v\n", err)
		os.Exit(1)
	}

	cases, err := loadCatalog(opts.CatalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e_manifest: %v\n", err)
		os.Exit(1)
	}

	facts := buildSuiteFacts(cases)
	if err := validateManifest(m, facts, opts); err != nil {
		fmt.Fprintf(os.Stderr, "e2e_manifest: %v\n", err)
		os.Exit(1)
	}

	totalCases := 0
	explicitCaseIDs := 0
	for _, fact := range facts {
		totalCases += fact.Cases
		explicitCaseIDs += fact.ExplicitCaseID
	}
	fmt.Printf(
		"E2E manifest valid: %d suites, %d files, %d cases, %d explicit case IDs\n",
		len(m.Suites),
		len(facts),
		totalCases,
		explicitCaseIDs,
	)
}

func parseOptions() (options, error) {
	var opts options
	flag.StringVar(&opts.ManifestPath, "manifest", defaultManifestPath, "E2E suite manifest path")
	flag.StringVar(&opts.CatalogPath, "catalog", defaultCatalogPath, "E2E catalog cases.json path")
	flag.BoolVar(&opts.RequireCaseIDs, "require-case-ids", false, "fail if any catalog case lacks an explicit case: label")
	flag.Parse()

	if strings.TrimSpace(opts.ManifestPath) == "" {
		return options{}, fmt.Errorf("manifest path is required")
	}
	if strings.TrimSpace(opts.CatalogPath) == "" {
		return options{}, fmt.Errorf("catalog path is required")
	}
	return opts, nil
}

func loadManifest(path string) (manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return m, nil
}

func loadCatalog(path string) ([]catalogCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read catalog %s: %w", path, err)
	}

	var cases []catalogCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("parse catalog %s: %w", path, err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("catalog %s has no cases", path)
	}
	return cases, nil
}

func buildSuiteFacts(cases []catalogCase) map[string]suiteFacts {
	byFile := map[string][]catalogCase{}
	for _, tc := range cases {
		file := filepath.ToSlash(tc.File)
		byFile[file] = append(byFile[file], tc)
	}

	facts := map[string]suiteFacts{}
	for file, fileCases := range byFile {
		fact := suiteFacts{
			File:  file,
			Title: suiteTitle(fileCases),
			Cases: len(fileCases),
		}
		labelSet := map[string]bool{}
		coverageSet := map[string]bool{}
		for _, tc := range fileCases {
			if strings.TrimSpace(tc.CaseLabel) != "" {
				fact.ExplicitCaseID++
			}
			if tc.Pending {
				fact.Pending++
			}
			for _, label := range tc.DomainLabels {
				label = strings.TrimSpace(label)
				if label != "" {
					labelSet[label] = true
				}
			}
			for _, coverage := range tc.Coverage {
				coverage = strings.TrimSpace(coverage)
				if coverage != "" {
					coverageSet[coverage] = true
				}
			}
		}
		fact.Labels = sortedKeys(labelSet)
		fact.Coverage = sortedKeys(coverageSet)
		facts[file] = fact
	}
	return facts
}

func validateManifest(m manifest, facts map[string]suiteFacts, opts options) error {
	var errs []string
	if m.Version != 1 {
		errs = append(errs, fmt.Sprintf("version = %d, want 1", m.Version))
	}
	if len(m.Suites) == 0 {
		errs = append(errs, "suites must not be empty")
	}

	ciLanes, laneErrs := validateCILanes(m.CILanes, m.Parallelism)
	errs = append(errs, laneErrs...)
	errs = append(errs, validateVersionPolicy(m.Versions)...)
	errs = append(errs, validateParallelismPolicy(m.Parallelism)...)
	errs = append(errs, validateNightlyPlan(m.Nightly, m.Versions.Kubernetes, ciLanes)...)

	seenIDs := map[string]string{}
	seenFiles := map[string]string{}
	for idx, suite := range m.Suites {
		prefix := fmt.Sprintf("suites[%d]", idx)
		errs = append(errs, validateSuite(prefix, suite, facts, ciLanes, seenIDs, seenFiles)...)
	}
	errs = append(errs, validateParallelLaneIsolation(m.Suites, ciLanes, m.Parallelism)...)

	for file := range facts {
		if seenFiles[file] == "" {
			errs = append(errs, fmt.Sprintf("catalog file %s is not owned by any manifest suite", file))
		}
	}

	if opts.RequireCaseIDs {
		for _, fact := range sortedFacts(facts) {
			if fact.ExplicitCaseID != fact.Cases {
				errs = append(errs, fmt.Sprintf("%s has %d/%d explicit case IDs", fact.File, fact.ExplicitCaseID, fact.Cases))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("manifest validation failed:\n- %s", strings.Join(errs, "\n- "))
	}
	return nil
}

func validateSuite(
	prefix string,
	suite manifestSuite,
	facts map[string]suiteFacts,
	ciLanes map[string]ciLaneConfig,
	seenIDs map[string]string,
	seenFiles map[string]string,
) []string {
	var errs []string
	if suite.ID == "" {
		errs = append(errs, fmt.Sprintf("%s.id is required", prefix))
	} else {
		if !idPattern.MatchString(suite.ID) {
			errs = append(errs, fmt.Sprintf("%s.id %q must be a lowercase slug", prefix, suite.ID))
		}
		if previous := seenIDs[suite.ID]; previous != "" {
			errs = append(errs, fmt.Sprintf("%s.id %q duplicates %s", prefix, suite.ID, previous))
		}
		seenIDs[suite.ID] = prefix
	}

	if strings.TrimSpace(suite.Title) == "" {
		errs = append(errs, fmt.Sprintf("%s.title is required", prefix))
	}
	if !allowedOwners[suite.Owner] {
		errs = append(errs, fmt.Sprintf("%s.owner %q is not recognized", prefix, suite.Owner))
	}
	if !allowedRiskTiers[suite.RiskTier] {
		errs = append(errs, fmt.Sprintf("%s.riskTier %q is not recognized", prefix, suite.RiskTier))
	}
	if !allowedIsolation[suite.Isolation] {
		errs = append(errs, fmt.Sprintf("%s.isolation %q is not recognized", prefix, suite.Isolation))
	}
	if len(suite.Files) == 0 {
		errs = append(errs, fmt.Sprintf("%s.files must not be empty", prefix))
	}
	if len(suite.Labels) == 0 {
		errs = append(errs, fmt.Sprintf("%s.labels must not be empty", prefix))
	}
	if len(suite.CI.Lanes) == 0 {
		errs = append(errs, fmt.Sprintf("%s.ci.lanes must not be empty", prefix))
	}
	for _, lane := range suite.CI.Lanes {
		if _, ok := ciLanes[lane]; !ok {
			errs = append(errs, fmt.Sprintf("%s.ci.lanes contains undefined lane %q", prefix, lane))
		}
	}
	if !allowedPullRequestPolicies[suite.CI.PullRequest] {
		errs = append(errs, fmt.Sprintf("%s.ci.pullRequest %q is not recognized", prefix, suite.CI.PullRequest))
	}
	if !allowedNightlyPolicies[suite.Nightly.Policy] {
		errs = append(errs, fmt.Sprintf("%s.nightly.policy %q is not recognized", prefix, suite.Nightly.Policy))
	}
	errs = append(errs, validateSuiteRuntime(prefix+".runtime", suite.Runtime)...)

	var suiteLabels []string
	var suiteCoverage []string
	suiteTitles := make([]string, 0, len(suite.Files))
	for _, file := range suite.Files {
		file = filepath.ToSlash(strings.TrimSpace(file))
		if file == "" {
			errs = append(errs, fmt.Sprintf("%s.files contains an empty path", prefix))
			continue
		}
		if previous := seenFiles[file]; previous != "" {
			errs = append(errs, fmt.Sprintf("%s.files contains %s already owned by %s", prefix, file, previous))
		}
		seenFiles[file] = suite.ID
		if _, err := os.Stat(file); err != nil {
			errs = append(errs, fmt.Sprintf("%s.files contains %s that cannot be read: %v", prefix, file, err))
		}
		fact, ok := facts[file]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s.files contains %s that is absent from catalog", prefix, file))
			continue
		}
		suiteLabels = append(suiteLabels, fact.Labels...)
		suiteCoverage = append(suiteCoverage, fact.Coverage...)
		suiteTitles = append(suiteTitles, fact.Title)
	}

	if len(suiteTitles) == 1 && suite.Title != suiteTitles[0] {
		errs = append(errs, fmt.Sprintf("%s.title %q does not match catalog title %q", prefix, suite.Title, suiteTitles[0]))
	}
	if got, want := normalizeList(suite.Labels), normalizeList(suiteLabels); !equalStringSlices(got, want) {
		errs = append(
			errs,
			fmt.Sprintf("%s.labels = [%s], want catalog labels [%s]", prefix, strings.Join(got, ", "), strings.Join(want, ", ")),
		)
	}
	if got, want := normalizeList(suite.Coverage), normalizeList(suiteCoverage); !equalStringSlices(got, want) {
		errs = append(
			errs,
			fmt.Sprintf(
				"%s.coverage = [%s], want catalog coverage [%s]",
				prefix,
				strings.Join(got, ", "),
				strings.Join(want, ", "),
			),
		)
	}

	return errs
}

func validateSuiteRuntime(prefix string, runtime suiteRuntime) []string {
	var errs []string
	hasRuntime := strings.TrimSpace(runtime.Observed) != "" ||
		strings.TrimSpace(runtime.Budget) != ""
	if !hasRuntime {
		return nil
	}
	if strings.TrimSpace(runtime.Observed) == "" {
		errs = append(errs, fmt.Sprintf("%s.observed is required when runtime metadata is set", prefix))
	} else if _, err := time.ParseDuration(runtime.Observed); err != nil {
		errs = append(errs, fmt.Sprintf("%s.observed %q is not a valid Go duration: %v", prefix, runtime.Observed, err))
	}
	if strings.TrimSpace(runtime.Budget) == "" {
		errs = append(errs, fmt.Sprintf("%s.budget is required when runtime metadata is set", prefix))
	} else if _, err := time.ParseDuration(runtime.Budget); err != nil {
		errs = append(errs, fmt.Sprintf("%s.budget %q is not a valid Go duration: %v", prefix, runtime.Budget, err))
	}
	return errs
}

func validateCILanes(lanes []ciLaneConfig, parallelism parallelismPolicy) (map[string]ciLaneConfig, []string) {
	var errs []string
	if len(lanes) == 0 {
		return nil, []string{"ciLanes must not be empty"}
	}

	seen := map[string]bool{}
	lanesByID := map[string]ciLaneConfig{}
	for idx, lane := range lanes {
		if !validNetworkProfile(lane.NetworkProfile) {
			errs = append(errs, fmt.Sprintf("ciLanes[%d].networkProfile %q is not recognized", idx, lane.NetworkProfile))
		}
		prefix := fmt.Sprintf("ciLanes[%d]", idx)
		if lane.ID == "" {
			errs = append(errs, fmt.Sprintf("%s.id is required", prefix))
		} else {
			if !idPattern.MatchString(lane.ID) {
				errs = append(errs, fmt.Sprintf("%s.id %q must be a lowercase slug", prefix, lane.ID))
			}
			if seen[lane.ID] {
				errs = append(errs, fmt.Sprintf("%s.id %q is duplicated", prefix, lane.ID))
			}
			if !seen[lane.ID] {
				lanesByID[lane.ID] = lane
			}
			seen[lane.ID] = true
		}
		if strings.TrimSpace(lane.Name) == "" {
			errs = append(errs, fmt.Sprintf("%s.name is required", prefix))
		}
		if strings.TrimSpace(lane.LabelFilter) == "" {
			errs = append(errs, fmt.Sprintf("%s.labelFilter is required", prefix))
		}
		if !allowedPRScopes[lane.PRScope] {
			errs = append(errs, fmt.Sprintf("%s.prScope %q is not recognized", prefix, lane.PRScope))
		}
		if lane.TimeoutMinutes <= 0 {
			errs = append(errs, fmt.Sprintf("%s.timeoutMinutes must be > 0", prefix))
		}
		if strings.TrimSpace(lane.E2ETimeout) == "" {
			errs = append(errs, fmt.Sprintf("%s.e2eTimeout is required", prefix))
		}
		errs = append(errs, validateLaneParallelism(prefix, lane, parallelism)...)
	}
	return lanesByID, errs
}

func validateVersionPolicy(policy versionPolicy) []string {
	var errs []string
	if strings.TrimSpace(policy.OpenBao.DefaultImage) == "" {
		errs = append(errs, "versions.openbao.defaultImage is required")
	}
	if strings.TrimSpace(policy.Kubernetes.Primary) == "" {
		errs = append(errs, "versions.kubernetes.primary is required")
	}
	if len(policy.Kubernetes.Compatibility) == 0 {
		errs = append(errs, "versions.kubernetes.compatibility must not be empty")
	}
	if len(policy.Kubernetes.ReleaseGate) == 0 {
		errs = append(errs, "versions.kubernetes.releaseGate must not be empty")
	}
	errs = append(errs, validateVersionList("versions.kubernetes.compatibility", policy.Kubernetes.Compatibility)...)
	errs = append(errs, validateVersionList("versions.kubernetes.releaseGate", policy.Kubernetes.ReleaseGate)...)
	return errs
}

func validateParallelismPolicy(policy parallelismPolicy) []string {
	var errs []string
	if policy.DefaultNodes <= 0 {
		errs = append(errs, "parallelism.defaultNodes must be > 0")
	}
	if policy.MaxNodes <= 0 {
		errs = append(errs, "parallelism.maxNodes must be > 0")
	}
	if policy.DefaultNodes > 0 && policy.MaxNodes > 0 && policy.DefaultNodes > policy.MaxNodes {
		errs = append(errs, "parallelism.defaultNodes must be <= parallelism.maxNodes")
	}
	return errs
}

func validateLaneParallelism(prefix string, lane ciLaneConfig, policy parallelismPolicy) []string {
	var errs []string
	if lane.ParallelNodes < 0 {
		errs = append(errs, fmt.Sprintf("%s.parallelNodes must be >= 0", prefix))
	}
	if lane.ParallelNodes > 0 && policy.MaxNodes > 0 && lane.ParallelNodes > policy.MaxNodes {
		errs = append(errs, fmt.Sprintf("%s.parallelNodes must be <= parallelism.maxNodes (%d)", prefix, policy.MaxNodes))
	}
	return errs
}

func validateParallelLaneIsolation(
	suites []manifestSuite,
	ciLanes map[string]ciLaneConfig,
	parallelism parallelismPolicy,
) []string {
	var errs []string
	for _, suite := range suites {
		for _, laneID := range suite.CI.Lanes {
			lane, ok := ciLanes[laneID]
			if !ok {
				continue
			}
			nodes := effectiveParallelNodes(parallelism, lane)
			if nodes <= 1 || allowedParallelIsolation[suite.Isolation] {
				continue
			}
			errs = append(
				errs,
				fmt.Sprintf(
					"ciLanes[%s].parallelNodes=%d requires suite %s isolation to be parallel-safe or serial; got %s",
					laneID,
					nodes,
					suite.ID,
					suite.Isolation,
				),
			)
		}
	}
	return errs
}

func effectiveParallelNodes(policy parallelismPolicy, lane ciLaneConfig) int {
	if lane.ParallelNodes > 0 {
		return lane.ParallelNodes
	}
	if policy.DefaultNodes > 0 {
		return policy.DefaultNodes
	}
	return 1
}

func validateVersionList(prefix string, versions []string) []string {
	var errs []string
	for idx, version := range versions {
		if strings.TrimSpace(version) == "" {
			errs = append(errs, fmt.Sprintf("%s[%d] must not be empty", prefix, idx))
		}
	}
	return errs
}

func validateNightlyPlan(
	plan nightlyPlanConfig,
	versions kubernetesVersionPolicy,
	ciLanes map[string]ciLaneConfig,
) []string {
	var errs []string
	if len(plan.Profiles) == 0 {
		errs = append(errs, "nightly.profiles must not be empty")
	}

	seenProfiles := map[string]bool{}
	for idx, profile := range plan.Profiles {
		prefix := fmt.Sprintf("nightly.profiles[%d]", idx)
		if profile.ID == "" {
			errs = append(errs, fmt.Sprintf("%s.id is required", prefix))
		} else {
			if !idPattern.MatchString(profile.ID) {
				errs = append(errs, fmt.Sprintf("%s.id %q must be a lowercase slug", prefix, profile.ID))
			}
			if seenProfiles[profile.ID] {
				errs = append(errs, fmt.Sprintf("%s.id %q is duplicated", prefix, profile.ID))
			}
			seenProfiles[profile.ID] = true
		}
		if len(profile.LaneSets) == 0 && len(profile.Rows) == 0 {
			errs = append(errs, fmt.Sprintf("%s must declare laneSets or rows", prefix))
		}
		for setIdx, set := range profile.LaneSets {
			setPrefix := fmt.Sprintf("%s.laneSets[%d]", prefix, setIdx)
			errs = append(errs, validateNightlyCoverage(setPrefix, set.Coverage)...)
			if len(set.Kubernetes) == 0 {
				errs = append(errs, fmt.Sprintf("%s.kubernetes must not be empty", setPrefix))
			}
			errs = append(errs, validateKubernetesRefs(setPrefix+".kubernetes", versions, set.Kubernetes)...)
			if len(set.Lanes) == 0 {
				errs = append(errs, fmt.Sprintf("%s.lanes must not be empty", setPrefix))
			}
			for _, lane := range set.Lanes {
				if _, ok := ciLanes[lane]; !ok {
					errs = append(errs, fmt.Sprintf("%s.lanes contains undefined lane %q", setPrefix, lane))
				}
			}
			if set.TimeoutMinutes < 0 {
				errs = append(errs, fmt.Sprintf("%s.timeoutMinutes must be >= 0", setPrefix))
			}
		}
		for rowIdx, row := range profile.Rows {
			rowPrefix := fmt.Sprintf("%s.rows[%d]", prefix, rowIdx)
			if !validNetworkProfile(row.NetworkProfile) {
				errs = append(errs, fmt.Sprintf("%s.networkProfile %q is not recognized", rowPrefix, row.NetworkProfile))
			}
			errs = append(errs, validateNightlyCoverage(rowPrefix, row.Coverage)...)
			if _, ok := ciLanes[row.Lane]; !ok {
				errs = append(errs, fmt.Sprintf("%s.lane %q is undefined", rowPrefix, row.Lane))
			}
			errs = append(errs, validateKubernetesRefs(rowPrefix+".kubernetes", versions, []string{row.Kubernetes})...)
			if row.TimeoutMinutes < 0 {
				errs = append(errs, fmt.Sprintf("%s.timeoutMinutes must be >= 0", rowPrefix))
			}
		}
	}
	return errs
}

func validateKubernetesRefs(prefix string, policy kubernetesVersionPolicy, refs []string) []string {
	var errs []string
	for idx, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			errs = append(errs, fmt.Sprintf("%s[%d] must not be empty", prefix, idx))
			continue
		}
		switch ref {
		case "@primary":
			if strings.TrimSpace(policy.Primary) == "" {
				errs = append(errs, fmt.Sprintf("%s[%d] references empty versions.kubernetes.primary", prefix, idx))
			}
		case "@compatibility":
			if len(normalizeVersions(policy.Compatibility)) == 0 {
				errs = append(errs, fmt.Sprintf("%s[%d] references empty versions.kubernetes.compatibility", prefix, idx))
			}
		case "@releaseGate":
			if len(normalizeVersions(policy.ReleaseGate)) == 0 {
				errs = append(errs, fmt.Sprintf("%s[%d] references empty versions.kubernetes.releaseGate", prefix, idx))
			}
		default:
			if strings.HasPrefix(ref, "@") {
				errs = append(errs, fmt.Sprintf("%s[%d] references unknown Kubernetes version set %q", prefix, idx, ref))
			}
		}
	}
	return errs
}

func normalizeVersions(versions []string) []string {
	out := make([]string, 0, len(versions))
	for _, version := range versions {
		if version = strings.TrimSpace(version); version != "" {
			out = append(out, version)
		}
	}
	return out
}

func validateNightlyCoverage(prefix, coverage string) []string {
	if strings.TrimSpace(coverage) == "" {
		return []string{fmt.Sprintf("%s.coverage is required", prefix)}
	}
	if !allowedNightlyCoverage[coverage] {
		return []string{fmt.Sprintf("%s.coverage %q is not recognized", prefix, coverage)}
	}
	return nil
}

var allowedOwners = map[string]bool{
	"backup-restore": true,
	"core":           true,
	"gitops":         true,
	"hardening":      true,
	"manager":        true,
	"platform":       true,
	"security":       true,
	"upgrade":        true,
}

var allowedRiskTiers = map[string]bool{
	"critical":    true,
	"high":        true,
	"medium":      true,
	"low":         true,
	"exploratory": true,
}

var allowedIsolation = map[string]bool{
	"parallel-safe":    true,
	"shared-cluster":   true,
	"global-mutator":   true,
	"serial":           true,
	"external-cluster": true,
	"multi-cluster":    true,
}

var allowedParallelIsolation = map[string]bool{
	"parallel-safe": true,
	"serial":        true,
}

var allowedPRScopes = map[string]bool{
	"always":   true,
	"backup":   true,
	"hardened": true,
	"manual":   true,
	"seal":     true,
	"upgrade":  true,
}

var allowedPullRequestPolicies = map[string]bool{
	"always":        true,
	"changed-paths": true,
	"manual":        true,
}

var allowedNightlyPolicies = map[string]bool{
	"external-cluster-scheduled": true,
	"primary-version-full":       true,
}

var allowedNightlyCoverage = map[string]bool{
	"network-ipv6":        true,
	"network-dual-ipv4":   true,
	"network-dual-ipv6":   true,
	"compatibility-smoke": true,
	"full":                true,
	"release-gate":        true,
}

func validNetworkProfile(profile string) bool {
	switch profile {
	case "", "ipv4", "ipv6", "dual-ipv4", "dual-ipv6":
		return true
	default:
		return false
	}
}

func suiteTitle(cases []catalogCase) string {
	for _, tc := range cases {
		if len(tc.Path) > 0 && strings.TrimSpace(tc.Path[0]) != "" {
			return strings.TrimSpace(tc.Path[0])
		}
	}
	return ""
}

func sortedFacts(facts map[string]suiteFacts) []suiteFacts {
	out := make([]suiteFacts, 0, len(facts))
	for _, fact := range facts {
		out = append(out, fact)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].File < out[j].File
	})
	return out
}

func normalizeList(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = true
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
