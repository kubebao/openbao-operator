package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/hack/tools/internal/configschema"
	"github.com/kubebao/openbao-operator/internal/adapter/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func isHexSHA40(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isSafeDockerTag(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func main() {
	var openbaoImageTag string
	var openbaoGitSHA string
	var upstreamModDir string
	var maxList int
	var verbose bool

	const openbaoImageTagUsage = "OpenBao Docker image tag used to resolve an upstream git SHA " +
		"(e.g. 2.6.4)"
	const openbaoGitSHAUsage = "OpenBao git SHA (40 hex) to use instead of resolving " +
		"from Docker image"

	flag.StringVar(&openbaoImageTag, "openbao-image-tag", "", openbaoImageTagUsage)
	flag.StringVar(&openbaoGitSHA, "openbao-git-sha", "", openbaoGitSHAUsage)
	flag.StringVar(&upstreamModDir, "upstream-module-dir", "", "Use an already materialized upstream module")
	flag.IntVar(&maxList, "max-list", 60, "Max number of keys to print per section (0 = unlimited)")
	flag.BoolVar(&verbose, "v", false, "Verbose output (prints extra keys and sets)")
	flag.Parse()

	if maxList < 0 {
		fmt.Fprintln(os.Stderr, "error: -max-list must be >= 0")
		os.Exit(2)
	}

	if openbaoImageTag == "" {
		data, err := os.ReadFile("hack/ci/openbao-config-compat/manifest.json")
		var matrix struct {
			Versions []string `json:"versions"`
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &matrix); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(matrix.Versions) == 0 {
			fmt.Fprintln(os.Stderr, "empty version matrix")
			os.Exit(1)
		}
		openbaoImageTag = matrix.Versions[len(matrix.Versions)-1]
	}
	upstreamSHA := strings.TrimSpace(openbaoGitSHA)
	if upstreamSHA == "" && upstreamModDir == "" {
		if !isSafeDockerTag(openbaoImageTag) {
			fmt.Fprintf(os.Stderr, "error: invalid -openbao-image-tag %q\n", openbaoImageTag)
			os.Exit(2)
		}
		sha, err := resolveOpenBaoSHAFromDocker(openbaoImageTag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: resolve upstream SHA from docker image %q: %v\n", openbaoImageTag, err)
			os.Exit(1)
		}
		upstreamSHA = sha
	}
	if upstreamSHA != "" && !isHexSHA40(upstreamSHA) {
		fmt.Fprintf(os.Stderr, "error: invalid -openbao-git-sha %q (expected 40 hex)\n", upstreamSHA)
		os.Exit(2)
	}

	var err error
	if upstreamModDir == "" {
		upstreamModDir, err = materializeUpstreamModuleDir(upstreamSHA)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	upstreamSet, err := configschema.Upstream(upstreamModDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	operatorSet, err := configschema.Operator(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	upstreamKeys := make(map[string]struct{}, len(upstreamSet))
	for key := range upstreamSet {
		upstreamKeys[key] = struct{}{}
	}
	operatorSchemaKeys := make(map[string]struct{}, len(operatorSet))
	for key := range operatorSet {
		operatorSchemaKeys[key] = struct{}{}
	}
	defaultGeneratedKeys, err := extractDefaultGeneratedKeySet()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: extract default generated key set: %v\n", err)
		os.Exit(1)
	}

	missingTyped := setDifference(upstreamKeys, operatorSchemaKeys)
	missingDefault := setDifference(upstreamKeys, defaultGeneratedKeys)
	operatorExtras := setDifference(operatorSchemaKeys, upstreamKeys)
	for key := range operatorExtras {
		if configschema.Opaque(key) {
			delete(operatorExtras, key)
		}
	}

	fmt.Printf("==> OpenBao config schema inventory\n")
	fmt.Printf("    Upstream: openbao/openbao:%s (%s)\n", openbaoImageTag, upstreamSHA)
	fmt.Printf("\n")

	fmt.Printf("Scope: parser tags, stanza roots, profile fields, Raft map reads, and retry_join JSON fields.\n")
	fmt.Printf("Seal options and audit/request payloads require provider or API qualification.\n")
	fmt.Printf("Upstream keys (extractable): %d\n", len(upstreamKeys))
	fmt.Printf("Operator explicit keys:      %d\n", len(operatorSchemaKeys))
	fmt.Printf("Operator default output paths:       %d\n", len(defaultGeneratedKeys))
	fmt.Printf("\n")

	fmt.Printf("Upstream paths absent from operator typed declarations: %d\n", len(missingTyped))
	printKeyList("Absent (typed declarations)", missingTyped, maxList)
	fmt.Printf("\n")

	fmt.Printf("Upstream paths not emitted by default: %d\n", len(missingDefault))
	printKeyList("Absent (default output)", missingDefault, maxList)
	fmt.Printf("\n")

	fmt.Printf("Operator declarations absent from this upstream inventory: %d\n", len(operatorExtras))
	printKeyList("Version-specific or unextracted operator paths", operatorExtras, maxList)

	if verbose {
		fmt.Printf("\n-- Upstream keys (full)\n")
		printKeyList("Upstream keys", upstreamKeys, 0)
		fmt.Printf("\n-- Operator schema keys (full)\n")
		printKeyList("Operator schema keys", operatorSchemaKeys, 0)
		fmt.Printf("\n-- Operator default keys (full)\n")
		printKeyList("Operator default keys", defaultGeneratedKeys, 0)
	}

	writeGitHubSummary(
		openbaoImageTag,
		upstreamSHA,
		upstreamKeys,
		operatorSchemaKeys,
		defaultGeneratedKeys,
		missingTyped,
		missingDefault,
		operatorExtras,
		maxList,
	)
}

func extractDefaultGeneratedKeySet() (map[string]struct{}, error) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "schema-report",
			Namespace: "default",
		},
	}
	infra := config.InfrastructureDetails{
		HeadlessServiceName: cluster.Name,
		Namespace:           cluster.Namespace,
		APIPort:             8200,
		ClusterPort:         8201,
	}

	hclBytes, err := config.RenderHCL(cluster, infra)
	if err != nil {
		return nil, err
	}
	parsed, diags := hclwrite.ParseConfig(hclBytes, "config.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse generated HCL: %s", diags.Error())
	}

	keys := map[string]struct{}{}
	collectKeysFromHCLBody(keys, "", parsed.Body())
	return keys, nil
}

func findUpstreamServerDir(upstreamModDir string) (string, error) {
	return findUpstreamPackageDir([]string{
		filepath.Join(upstreamModDir, "internal", "command", "server"),
		filepath.Join(upstreamModDir, "command", "server"),
	})
}

func findUpstreamConfigDir(upstreamModDir string) (string, error) {
	return findUpstreamPackageDir([]string{
		filepath.Join(upstreamModDir, "internal", "helper", "configutil"),
		filepath.Join(upstreamModDir, "helper", "configutil"),
		filepath.Join(upstreamModDir, "internalshared", "configutil"),
	})
}

func findUpstreamPackageDir(candidates []string) (string, error) {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("none of the upstream package directories exist: %s", strings.Join(candidates, ", "))
}

func collectKeysFromHCLBody(out map[string]struct{}, prefix string, body *hclwrite.Body) {
	for name := range body.Attributes() {
		out[join(prefix, name)] = struct{}{}
	}
	for _, block := range body.Blocks() {
		blockName := block.Type()
		out[join(prefix, blockName)] = struct{}{}
		collectKeysFromHCLBody(out, join(prefix, blockName), block.Body())
	}
}

func join(prefix, name string) string {
	prefix = strings.Trim(prefix, ".")
	name = strings.Trim(name, ".")
	if prefix == "" {
		return name
	}
	if name == "" {
		return prefix
	}
	return prefix + "." + name
}

func setDifference(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for k := range a {
		if _, ok := b[k]; !ok {
			out[k] = struct{}{}
		}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func printKeyList(title string, keys map[string]struct{}, max int) {
	list := sortedKeys(keys)
	if max > 0 && len(list) > max {
		list = list[:max]
	}
	if len(list) == 0 {
		fmt.Printf("%s: (none)\n", title)
		return
	}
	fmt.Printf("%s:\n", title)
	for _, k := range list {
		fmt.Printf("  - %s\n", k)
	}
}

func resolveOpenBaoSHAFromDocker(imageTag string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker not found in PATH")
	}
	// #nosec G204 -- imageTag is validated by isSafeDockerTag; this is a developer tool.
	out, err := exec.Command("docker", "run", "--rm", "openbao/openbao:"+imageTag, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	line := strings.TrimSpace(string(out))
	open := strings.IndexByte(line, '(')
	close := strings.IndexByte(line, ')')
	if open < 0 || close < 0 || close <= open+1 {
		return "", fmt.Errorf("unexpected version output: %q", line)
	}
	sha := line[open+1 : close]
	if !isHexSHA40(sha) {
		return "", fmt.Errorf("unexpected git sha %q from output %q", sha, line)
	}
	return sha, nil
}

func materializeUpstreamModuleDir(sha string) (string, error) {
	tmpdir, err := os.MkdirTemp("", "openbao-schema-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err := os.RemoveAll(tmpdir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove temp dir %s: %v\n", tmpdir, err)
		}
	}()

	mainModule := filepath.Join(tmpdir, "mod")
	if err := os.MkdirAll(mainModule, 0o750); err != nil {
		return "", err
	}

	cmd := exec.Command("go", "mod", "init", "tmp.example/openbao-schema")
	cmd.Dir = mainModule
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go mod init: %v: %s", err, strings.TrimSpace(string(out)))
	}

	var failures []string
	for _, modulePath := range []string{"github.com/openbao/openbao/v2", "github.com/openbao/openbao"} {
		// #nosec G204 -- sha is validated (40 hex); this is a developer tool.
		cmd = exec.Command("go", "mod", "download", "-json", modulePath+"@"+sha)
		cmd.Dir = mainModule
		out, err := cmd.Output()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v: %s", modulePath, err, strings.TrimSpace(string(out))))
			continue
		}
		var mod struct {
			Dir string `json:"Dir"`
		}
		if err := json.Unmarshal(out, &mod); err != nil {
			return "", fmt.Errorf("decode go mod download output: %w", err)
		}
		if strings.TrimSpace(mod.Dir) == "" {
			return "", fmt.Errorf("go mod download did not return .Dir for %s", modulePath)
		}
		return mod.Dir, nil
	}
	return "", fmt.Errorf("download upstream module: %s", strings.Join(failures, "; "))
}

func writeGitHubSummary(
	openbaoImageTag, upstreamSHA string,
	upstreamKeys, operatorSchemaKeys, defaultKeys, missingTyped, missingDefault, extras map[string]struct{},
	maxList int,
) {
	path := strings.TrimSpace(os.Getenv("GITHUB_STEP_SUMMARY"))
	if path == "" {
		return
	}
	path = filepath.Clean(path)

	var buf bytes.Buffer
	buf.WriteString("### OpenBao operator schema inventory\n\n")
	fmt.Fprintf(&buf, "- Upstream: `openbao/openbao:%s` (`%s`)\n", openbaoImageTag, upstreamSHA)
	fmt.Fprintf(&buf, "- Upstream extractable keys: `%d`\n", len(upstreamKeys))
	fmt.Fprintf(&buf, "- Operator explicit keys: `%d`\n", len(operatorSchemaKeys))
	fmt.Fprintf(&buf, "- Operator default output paths: `%d`\n", len(defaultKeys))
	fmt.Fprintf(&buf, "- Missing vs typed schema: `%d`\n", len(missingTyped))
	fmt.Fprintf(&buf, "- Missing vs default generation: `%d`\n", len(missingDefault))
	fmt.Fprintf(&buf, "- Operator extras vs upstream: `%d`\n", len(extras))

	writeList := func(title string, set map[string]struct{}) {
		buf.WriteString("\n")
		buf.WriteString("#### " + title + "\n\n")
		list := sortedKeys(set)
		if maxList > 0 && len(list) > maxList {
			list = list[:maxList]
		}
		if len(list) == 0 {
			buf.WriteString("_None_\n")
			return
		}
		for _, k := range list {
			buf.WriteString("- `" + k + "`\n")
		}
	}

	writeList("Absent (typed declarations)", missingTyped)
	writeList("Absent (default output)", missingDefault)
	writeList("Extra (operator vs upstream)", extras)

	// #nosec G304 -- path is provided by GitHub Actions via env var; this is CI tooling.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close %s: %v\n", path, err)
		}
	}()
	if _, err := f.Write(buf.Bytes()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", path, err)
	}
}
