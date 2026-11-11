package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type args struct {
	mode            string
	indexPath       string
	repo            string
	owner           string
	version         string
	sourceDateEpoch int64

	managerImage          string
	managerDigest         string
	configInitImage       string
	configInitDigest      string
	backupExecutorImage   string
	backupExecutorDigest  string
	upgradeExecutorImage  string
	upgradeExecutorDigest string

	// release mode
	chartDigest                 string
	chartRef                    string
	chartVersion                string
	releaseSourceRef            string
	claim                       string
	reusableBuildSignerWorkflow string
	releaseSignerWorkflow       string
	checksumsPath               string
	checksumsBundlePath         string
	installPath                 string
	crdsPath                    string
	sbomGlob                    string

	// channel mode
	channel                   string
	commit                    string
	runID                     string
	runAttempt                string
	sourceRef                 string
	attestationSignerWorkflow string
	checksumsSignerWorkflow   string
}

func main() {
	cfg, err := parseArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	var index map[string]any
	switch cfg.mode {
	case "release":
		index, err = buildReleaseIndex(cfg)
	case "channel":
		index, err = buildChannelIndex(cfg)
	default:
		err = fmt.Errorf("unsupported mode %q (expected release or channel)", cfg.mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.indexPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: create index directory: %v\n", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal index: %v\n", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if err := os.WriteFile(cfg.indexPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write index: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Wrote %s\n", cfg.indexPath)
}

func parseArgs() (args, error) {
	cfg := args{}

	flag.StringVar(&cfg.mode, "mode", "", "index mode: release|channel")
	flag.StringVar(&cfg.indexPath, "index-path", "dist/provenance-index.json", "output index path")

	flag.StringVar(&cfg.repo, "repo", "", "repository in owner/repo format")
	flag.StringVar(&cfg.owner, "owner", "", "repository owner/org")
	flag.StringVar(&cfg.version, "version", "", "release or channel version label")
	flag.Int64Var(&cfg.sourceDateEpoch, "source-date-epoch", 0, "unix timestamp used for deterministic generated_at_utc")

	flag.StringVar(&cfg.managerImage, "manager-image", "", "manager image repository")
	flag.StringVar(&cfg.managerDigest, "manager-digest", "", "manager image digest")
	flag.StringVar(&cfg.configInitImage, "config-init-image", "", "config-init image repository")
	flag.StringVar(&cfg.configInitDigest, "config-init-digest", "", "config-init image digest")
	flag.StringVar(&cfg.backupExecutorImage, "backup-executor-image", "", "backup image repository")
	flag.StringVar(&cfg.backupExecutorDigest, "backup-executor-digest", "", "backup image digest")
	flag.StringVar(&cfg.upgradeExecutorImage, "upgrade-executor-image", "", "upgrade image repository")
	flag.StringVar(&cfg.upgradeExecutorDigest, "upgrade-executor-digest", "", "upgrade image digest")

	// release mode
	flag.StringVar(&cfg.chartDigest, "chart-digest", "", "chart digest")
	flag.StringVar(&cfg.chartRef, "chart-ref", "", "chart repository (channel mode)")
	flag.StringVar(&cfg.chartVersion, "chart-version", "", "chart version (channel mode)")
	flag.StringVar(&cfg.releaseSourceRef, "release-source-ref", "", "release source ref (release mode)")
	flag.StringVar(
		&cfg.claim,
		"claim",
		"Targets SLSA Build L3 controls with additional L4-like hardening.",
		"claim text (release mode)",
	)
	flag.StringVar(
		&cfg.reusableBuildSignerWorkflow,
		"reusable-build-signer-workflow",
		"",
		"image attestation signer workflow",
	)
	flag.StringVar(&cfg.releaseSignerWorkflow, "release-signer-workflow", "", "release attestation signer workflow")
	flag.StringVar(&cfg.checksumsPath, "checksums-path", "dist/checksums.txt", "checksums path")
	flag.StringVar(&cfg.checksumsBundlePath, "checksums-bundle-path", "dist/checksums.txt.bundle", "checksums bundle path")
	flag.StringVar(&cfg.installPath, "install-path", "dist/install.yaml", "installer manifest path")
	flag.StringVar(&cfg.crdsPath, "crds-path", "dist/crds.yaml", "crds manifest path")
	flag.StringVar(&cfg.sbomGlob, "sbom-glob", "dist/sbom-*.spdx.json", "sbom glob pattern")

	// channel mode
	flag.StringVar(&cfg.channel, "channel", "", "channel name (channel mode)")
	flag.StringVar(&cfg.commit, "commit", "", "commit sha (channel mode)")
	flag.StringVar(&cfg.runID, "run-id", "", "workflow run id (channel mode)")
	flag.StringVar(&cfg.runAttempt, "run-attempt", "", "workflow run attempt (channel mode)")
	flag.StringVar(&cfg.sourceRef, "source-ref", "refs/heads/main", "source ref (channel mode)")
	flag.StringVar(
		&cfg.attestationSignerWorkflow,
		"attestation-signer-workflow",
		"",
		"image attestation signer workflow (channel mode)",
	)
	flag.StringVar(
		&cfg.checksumsSignerWorkflow,
		"checksums-signer-workflow",
		"",
		"checksums signer workflow (channel mode)",
	)

	flag.Parse()

	if cfg.mode == "" {
		return cfg, errors.New("-mode is required")
	}
	if cfg.repo == "" {
		return cfg, errors.New("-repo is required")
	}
	if cfg.owner == "" {
		return cfg, errors.New("-owner is required")
	}
	if cfg.version == "" {
		return cfg, errors.New("-version is required")
	}

	requiredImages := map[string]string{
		"-manager-image":           cfg.managerImage,
		"-manager-digest":          cfg.managerDigest,
		"-config-init-image":       cfg.configInitImage,
		"-config-init-digest":      cfg.configInitDigest,
		"-backup-executor-image":   cfg.backupExecutorImage,
		"-backup-executor-digest":  cfg.backupExecutorDigest,
		"-upgrade-executor-image":  cfg.upgradeExecutorImage,
		"-upgrade-executor-digest": cfg.upgradeExecutorDigest,
	}
	for key, value := range requiredImages {
		if value == "" {
			return cfg, fmt.Errorf("%s is required", key)
		}
	}

	switch cfg.mode {
	case "release":
		if cfg.chartDigest == "" {
			return cfg, errors.New("-chart-digest is required in release mode")
		}
		if cfg.releaseSourceRef == "" {
			cfg.releaseSourceRef = fmt.Sprintf("refs/tags/%s", cfg.version)
		}
		if cfg.reusableBuildSignerWorkflow == "" {
			cfg.reusableBuildSignerWorkflow = fmt.Sprintf("%s/.github/workflows/reusable-build.yml", cfg.repo)
		}
		if cfg.releaseSignerWorkflow == "" {
			cfg.releaseSignerWorkflow = fmt.Sprintf("%s/.github/workflows/release.yml", cfg.repo)
		}
	case "channel":
		if cfg.channel == "" {
			return cfg, errors.New("-channel is required in channel mode")
		}
		if cfg.commit == "" {
			return cfg, errors.New("-commit is required in channel mode")
		}
		if cfg.runID == "" {
			return cfg, errors.New("-run-id is required in channel mode")
		}
		if cfg.attestationSignerWorkflow == "" {
			cfg.attestationSignerWorkflow = fmt.Sprintf("%s/.github/workflows/reusable-build.yml", cfg.repo)
		}
		if cfg.checksumsSignerWorkflow == "" {
			cfg.checksumsSignerWorkflow = fmt.Sprintf("%s/.github/workflows/publish-%s.yml", cfg.repo, cfg.channel)
		}
	default:
		return cfg, fmt.Errorf("unsupported mode %q", cfg.mode)
	}

	return cfg, nil
}

func buildReleaseIndex(cfg args) (map[string]any, error) {
	sboms, err := filepath.Glob(cfg.sbomGlob)
	if err != nil {
		return nil, fmt.Errorf("glob sboms: %w", err)
	}
	sort.Strings(sboms)
	releaseFiles := make([]string, 0, 4+len(sboms))
	releaseFiles = append(releaseFiles, cfg.installPath, cfg.crdsPath, cfg.checksumsPath, cfg.checksumsBundlePath)
	releaseFiles = append(releaseFiles, sboms...)
	for _, name := range []string{"operator-policy-approval.hcl", "operator-policy-approval-with-backup.hcl",
		"api-migration-darwin-arm64", "api-migration-linux-amd64", "api-migration-linux-arm64"} {
		releaseFiles = append(releaseFiles, filepath.Join(filepath.Dir(cfg.checksumsPath), name))
	}

	checksumsSubjects, err := parseChecksumsFile(cfg.checksumsPath)
	if err != nil {
		return nil, fmt.Errorf("parse checksums file: %w", err)
	}

	artifactEntries := make([]map[string]any, 0, len(releaseFiles))
	for _, fileName := range releaseFiles {
		if !fileExists(fileName) {
			continue
		}
		fileSHA, err := sha256Hex(fileName)
		if err != nil {
			return nil, fmt.Errorf("sha256 %s: %w", fileName, err)
		}

		base := filepath.Base(fileName)
		_, included := checksumsSubjects[base]
		entry := map[string]any{
			"path":                      fileName,
			"sha256":                    fileSHA,
			"included_in_checksums_txt": included,
			"checksums_txt_sha256":      checksumsSubjects[base],
		}
		artifactEntries = append(artifactEntries, entry)
	}

	var checksumsDigest string
	if fileExists(cfg.checksumsPath) {
		hexDigest, err := sha256Hex(cfg.checksumsPath)
		if err != nil {
			return nil, fmt.Errorf("sha256 %s: %w", cfg.checksumsPath, err)
		}
		checksumsDigest = "sha256:" + hexDigest
	}

	images := buildReleaseImages(cfg)
	index := map[string]any{
		"schema_version":   "v1alpha1",
		"generated_at_utc": isoUTC(cfg.sourceDateEpoch),
		"release": map[string]any{
			"repository": cfg.repo,
			"owner":      cfg.owner,
			"tag":        cfg.version,
			"source_ref": cfg.releaseSourceRef,
			"claim":      cfg.claim,
		},
		"identity_constraints": map[string]any{
			"oidc_issuer":                    "https://token.actions.githubusercontent.com",
			"reusable_build_signer_workflow": cfg.reusableBuildSignerWorkflow,
			"release_signer_workflow":        cfg.releaseSignerWorkflow,
		},
		"images": images,
		"chart": map[string]any{
			"ref":                fmt.Sprintf("ghcr.io/%s/charts/openbao-operator", cfg.owner),
			"digest":             cfg.chartDigest,
			"oci_subject":        fmt.Sprintf("ghcr.io/%s/charts/openbao-operator@%s", cfg.owner, cfg.chartDigest),
			"attestation_api":    apiAttestationURI(cfg.repo, cfg.chartDigest),
			"signature_identity": releaseWorkflowIdentity(cfg.repo, cfg.version),
		},
		"release_artifacts": map[string]any{
			"checksums_txt": map[string]any{
				"path":                  cfg.checksumsPath,
				"digest":                checksumsDigest,
				"attestation_api":       apiAttestationURI(cfg.repo, checksumsDigest),
				"signature_bundle_path": cfg.checksumsBundlePath,
			},
			"files": artifactEntries,
		},
	}

	return index, nil
}

func buildChannelIndex(cfg args) (map[string]any, error) {
	installDigest, err := maybeSHA256WithPrefix(cfg.installPath)
	if err != nil {
		return nil, fmt.Errorf("hash install path: %w", err)
	}
	crdsDigest, err := maybeSHA256WithPrefix(cfg.crdsPath)
	if err != nil {
		return nil, fmt.Errorf("hash crds path: %w", err)
	}
	checksumsDigest, err := maybeSHA256WithPrefix(cfg.checksumsPath)
	if err != nil {
		return nil, fmt.Errorf("hash checksums path: %w", err)
	}
	checksumsBundleDigest, err := maybeSHA256WithPrefix(cfg.checksumsBundlePath)
	if err != nil {
		return nil, fmt.Errorf("hash checksums bundle path: %w", err)
	}

	images := buildChannelImages(cfg)

	var attempt any
	if cfg.runAttempt != "" {
		attempt = cfg.runAttempt
	}

	index := map[string]any{
		"schema_version":   "v1alpha1",
		"channel":          cfg.channel,
		"version":          cfg.version,
		"repository":       cfg.repo,
		"owner":            cfg.owner,
		"source_ref":       cfg.sourceRef,
		"commit":           cfg.commit,
		"generated_at_utc": isoUTC(cfg.sourceDateEpoch),
		"run": map[string]any{
			"id":      cfg.runID,
			"attempt": attempt,
		},
		"identity_constraints": map[string]any{
			"oidc_issuer":                       "https://token.actions.githubusercontent.com",
			"image_attestation_signer_workflow": cfg.attestationSignerWorkflow,
			"checksums_signer_workflow":         cfg.checksumsSignerWorkflow,
			"deny_self_hosted_runners":          true,
		},
		"images": images,
		"manifests": map[string]any{
			"install_yaml": map[string]any{
				"path":   cfg.installPath,
				"digest": installDigest,
			},
			"crds_yaml": map[string]any{
				"path":   cfg.crdsPath,
				"digest": crdsDigest,
			},
		},
		"checksums": map[string]any{
			"path":                    cfg.checksumsPath,
			"digest":                  checksumsDigest,
			"attestation_api":         apiAttestationURI(cfg.repo, checksumsDigest),
			"signature_bundle_path":   cfg.checksumsBundlePath,
			"signature_bundle_digest": checksumsBundleDigest,
		},
	}

	if cfg.chartDigest != "" {
		if cfg.chartRef == "" || cfg.chartVersion == "" {
			return nil, errors.New("chart reference and version are required with a channel chart digest")
		}
		index["chart"] = map[string]any{
			"ref":                         cfg.chartRef,
			"version":                     cfg.chartVersion,
			"digest":                      cfg.chartDigest,
			"oci_subject":                 cfg.chartRef + "@" + cfg.chartDigest,
			"attestation_api":             apiAttestationURI(cfg.repo, cfg.chartDigest),
			"attestation_signer_workflow": cfg.checksumsSignerWorkflow,
			"signing_identity":            "https://github.com/" + cfg.checksumsSignerWorkflow + "@" + cfg.sourceRef,
		}
	}
	if cfg.channel == "edge" {
		files, err := edgeMigrationArtifacts(cfg)
		if err != nil {
			return nil, err
		}
		index["api_migration"] = map[string]any{
			"version":                     cfg.version,
			"source_digest":               cfg.commit,
			"attestation_signer_workflow": cfg.repo + "/.github/workflows/reusable-channel-hardening.yml",
			"files":                       files,
		}
	}

	return index, nil
}

func edgeMigrationArtifacts(cfg args) ([]map[string]any, error) {
	source, err := hex.DecodeString(cfg.commit)
	if err != nil || len(source) != 20 || cfg.version != "edge-"+cfg.commit[:12] {
		return nil, errors.New("edge migration version must identify the source commit")
	}
	checksums, err := parseChecksumsFile(cfg.checksumsPath)
	if err != nil {
		return nil, fmt.Errorf("parse edge checksums: %w", err)
	}
	var files []map[string]any
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		name := "api-migration-" + platform
		digest, err := sha256Hex(filepath.Join(filepath.Dir(cfg.checksumsPath), name))
		if err != nil {
			return nil, fmt.Errorf("hash edge migration CLI %s: %w", platform, err)
		}
		if checksums[name] != digest {
			return nil, fmt.Errorf("edge migration CLI %s does not match checksums.txt", platform)
		}
		files = append(files, map[string]any{
			"platform":        platform,
			"path":            name,
			"digest":          "sha256:" + digest,
			"attestation_api": apiAttestationURI(cfg.repo, "sha256:"+digest),
		})
	}
	return files, nil
}

func buildReleaseImages(cfg args) []map[string]any {
	entries := imageBaseEntries(cfg)
	for _, image := range entries {
		image["signing_identity"] = releaseWorkflowIdentity(cfg.repo, cfg.version)
		image["attestation_signer_workflow"] = cfg.reusableBuildSignerWorkflow
	}
	return entries
}

func buildChannelImages(cfg args) []map[string]any {
	entries := imageBaseEntries(cfg)
	for _, image := range entries {
		image["attestation_signer_workflow"] = cfg.attestationSignerWorkflow
		image["source_ref"] = cfg.sourceRef
	}
	return entries
}

func imageBaseEntries(cfg args) []map[string]any {
	entries := []map[string]string{
		{
			"name":   "openbao-operator",
			"ref":    cfg.managerImage,
			"digest": cfg.managerDigest,
		},
		{
			"name":   "openbao-init",
			"ref":    cfg.configInitImage,
			"digest": cfg.configInitDigest,
		},
		{
			"name":   "openbao-backup",
			"ref":    cfg.backupExecutorImage,
			"digest": cfg.backupExecutorDigest,
		},
		{
			"name":   "openbao-upgrade",
			"ref":    cfg.upgradeExecutorImage,
			"digest": cfg.upgradeExecutorDigest,
		},
	}

	out := make([]map[string]any, 0, len(entries))
	for _, item := range entries {
		out = append(out, map[string]any{
			"name":            item["name"],
			"ref":             item["ref"],
			"digest":          item["digest"],
			"oci_subject":     fmt.Sprintf("%s@%s", item["ref"], item["digest"]),
			"attestation_api": apiAttestationURI(cfg.repo, item["digest"]),
		})
	}

	return out
}

func parseChecksumsFile(path string) (map[string]string, error) {
	out := map[string]string{}
	if !fileExists(path) {
		return out, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}

	return out, nil
}

func releaseWorkflowIdentity(repo, version string) string {
	return fmt.Sprintf(
		"https://github.com/%s/.github/workflows/release.yml@refs/tags/%s",
		repo,
		version,
	)
}

func maybeSHA256WithPrefix(path string) (any, error) {
	if !fileExists(path) {
		return nil, nil
	}
	hexDigest, err := sha256Hex(path)
	if err != nil {
		return nil, err
	}
	return "sha256:" + hexDigest, nil
}

func sha256Hex(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func apiAttestationURI(repo string, digest any) any {
	d, ok := digest.(string)
	if !ok || d == "" {
		return nil
	}
	clean := strings.TrimPrefix(d, "sha256:")
	return fmt.Sprintf("https://api.github.com/repos/%s/attestations/sha256:%s", repo, clean)
}

func isoUTC(epoch int64) string {
	return time.Unix(epoch, 0).UTC().Format("2006-01-02T15:04:05-07:00")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
