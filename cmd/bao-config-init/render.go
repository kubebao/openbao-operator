package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// renderConfig reads a template file, substitutes environment-driven placeholders,
// and writes the rendered configuration to the specified output path.
func renderConfig(templatePath, outputPath, hostname, podIP, selfInitPath string, selfInitSecretFiles ...string) error {
	if strings.TrimSpace(templatePath) == "" {
		return fmt.Errorf("template path is required")
	}
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("output path is required")
	}
	if strings.TrimSpace(hostname) == "" {
		return fmt.Errorf("HOSTNAME environment variable is required (must be set from pod metadata.name)")
	}

	resolvedPodIP, err := waitForPodIP(podIP)
	if err != nil {
		return err
	}

	content, err := readValidatedFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to open template file %q: %w", templatePath, err)
	}

	rendered := replacePlaceholders(string(content), hostname, resolvedPodIP)
	if selfInitPath != "" && strings.HasSuffix(hostname, "-0") {
		rendered, err = appendSelfInitConfig(rendered, selfInitPath, selfInitSecretFiles...)
		if err != nil {
			return err
		}
	}

	if err := writeRenderedConfig(outputPath, rendered); err != nil {
		return err
	}

	if strings.Contains(rendered, "${HOSTNAME}") {
		return fmt.Errorf("rendered config still contains ${HOSTNAME} placeholder - HOSTNAME expansion failed")
	}
	if strings.Contains(rendered, "${POD_IP}") {
		return fmt.Errorf("rendered config still contains ${POD_IP} placeholder - POD_IP expansion failed")
	}

	return nil
}

func waitForPodIP(podIP string) (string, error) {
	if strings.TrimSpace(podIP) != "" {
		return podIP, nil
	}

	pollCtx := context.Background()
	pollFn := func(ctx context.Context) (bool, error) {
		podIP = strings.TrimSpace(os.Getenv(envPodIP))
		return podIP != "", nil
	}
	err := wait.PollUntilContextTimeout(pollCtx, 500*time.Millisecond, 5*time.Second, true, pollFn)
	if strings.TrimSpace(podIP) != "" {
		return podIP, nil
	}
	if err != nil {
		return "", fmt.Errorf(
			"POD_IP environment variable is required but not available after waiting (must be set from pod status.podIP): %w",
			err,
		)
	}

	return "", fmt.Errorf(
		"POD_IP environment variable is required but not available after waiting " +
			"(must be set from pod status.podIP)",
	)
}

func replacePlaceholders(content, hostname, podIP string) string {
	rendered := strings.ReplaceAll(content, "$${HOSTNAME}", hostname)
	rendered = strings.ReplaceAll(rendered, "${HOSTNAME}", hostname)
	rendered = strings.ReplaceAll(rendered, "$${POD_IP}", podIP)
	rendered = strings.ReplaceAll(rendered, "${POD_IP}", podIP)
	return rendered
}

func appendSelfInitConfig(rendered, selfInitPath string, selfInitSecretFiles ...string) (string, error) {
	selfInitContent, err := readOptionalValidatedFile(selfInitPath)
	if err != nil {
		return "", fmt.Errorf("failed to open self-init config file %q: %w", selfInitPath, err)
	}
	if strings.TrimSpace(string(selfInitContent)) == strings.TrimSpace(constants.CompletedSelfInitConfig) {
		return rendered, nil
	}
	if len(selfInitContent) == 0 {
		if len(selfInitSecretFiles) > 0 {
			return "", fmt.Errorf("self-init configuration is missing while Secret inputs are required")
		}
		return rendered, nil
	}
	for _, inputPath := range selfInitSecretFiles {
		if err := validateSelfInitSecretFile(inputPath); err != nil {
			return "", err
		}
	}
	if err := validateSelfInitSecretWiring(selfInitContent, selfInitSecretFiles); err != nil {
		return "", err
	}

	return rendered + "\n\n" + string(selfInitContent), nil
}

func validateSelfInitSecretFile(inputPath string) error {
	file, err := os.Open(inputPath) // #nosec G304 -- Operator-generated path; contents are not rendered or logged.
	if err != nil {
		return fmt.Errorf("required self-init Secret input %q is not readable: %w", inputPath, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("check required self-init Secret input %q: %w", inputPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("required self-init Secret input %q must be a regular file", inputPath)
	}
	return nil
}

// validateSelfInitSecretWiring catches a ConfigMap update that reaches an old
// Pod template before its input projections change. It evaluates only literal
// HCL objects and never reads the referenced Secret contents.
func validateSelfInitSecretWiring(content []byte, files []string) error {
	if !bytes.Contains(content, []byte(constants.PathSelfInitInputs+"/")) {
		return nil
	}
	parsed, diagnostics := hclsyntax.ParseConfig(content, "self-init.hcl", hcl.InitialPos)
	if diagnostics.HasErrors() {
		return fmt.Errorf("self-init Secret input configuration is invalid")
	}
	expected := make(map[string]bool, len(files))
	for _, file := range files {
		expected[file] = true
	}
	mismatch := false
	_ = hclsyntax.VisitAll(parsed.Body.(hclsyntax.Node), func(node hclsyntax.Node) hcl.Diagnostics {
		object, ok := node.(*hclsyntax.ObjectConsExpr)
		if !ok {
			return nil
		}
		value, diags := object.Value(nil)
		if diags.HasErrors() || !value.IsWhollyKnown() || value.IsNull() {
			return nil
		}
		attributes := value.AsValueMap()
		source, hasSource := attributes["eval_source"]
		inputPath, hasPath := attributes["path"]
		if hasSource && hasPath && source.Type() == cty.String && inputPath.Type() == cty.String &&
			!source.IsNull() && !inputPath.IsNull() && source.AsString() == "file" &&
			strings.HasPrefix(inputPath.AsString(), constants.PathSelfInitInputs+"/") && !expected[inputPath.AsString()] {
			mismatch = true
		}
		return nil
	})
	if mismatch {
		return fmt.Errorf("self-init Secret input configuration does not match the Pod projections")
	}
	return nil
}

func writeRenderedConfig(outputPath, rendered string) error {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create output directory %q: %w", dir, err)
	}

	if err := os.WriteFile(outputPath, []byte(rendered), configFileMode); err != nil {
		return fmt.Errorf("failed to write rendered config to %q: %w", outputPath, err)
	}

	return nil
}

func readValidatedFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		return nil, fmt.Errorf("path %q contains path traversal", path)
	}

	f, err := os.Open(cleanPath) // #nosec G304 -- Path is validated and cleaned to prevent traversal
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	return io.ReadAll(f)
}

func readOptionalValidatedFile(path string) ([]byte, error) {
	content, err := readValidatedFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	return content, nil
}
