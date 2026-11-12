package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
)

// Helm runs the helm binary against one release with an explicit context.
type Helm struct {
	Kubeconfig      string
	Context         string
	Namespace       string // Helm release storage
	TargetNamespace string // namespace used by helm template
	Release         string
}

type HelmRevision struct {
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Chart    string `json:"chart"`
}

func (h Helm) run(ctx context.Context, args ...string) ([]byte, error) {
	args = append(args, "--kubeconfig", h.Kubeconfig, "--kube-context", h.Context, "--namespace", h.Namespace)
	cmd := exec.CommandContext(ctx, "helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// Helm errors can echo values; report only the first line.
		message, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("helm %s: %w: %s", args[0], err, message)
	}
	return stdout.Bytes(), nil
}

// Version returns the version of the helm executable on PATH.
func (h Helm) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "helm", "version", "--template", "{{.Version}}").Output()
	if err != nil {
		return "", fmt.Errorf("helm version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// requireHelm4 rejects other Helm majors: the transitions use Helm 4
// server-side apply and release metadata.
func requireHelm4(version string) error {
	if !strings.HasPrefix(version, "v4.") {
		return fmt.Errorf("the helm executable on PATH is %s; a Helm installation requires Helm 4", version)
	}
	return nil
}

func (h Helm) Latest(ctx context.Context) (HelmRevision, error) {
	out, err := h.run(ctx, "history", h.Release, "--max", "1", "--output", "json")
	if err != nil {
		return HelmRevision{}, err
	}
	var history []HelmRevision
	if err := json.Unmarshal(out, &history); err != nil || len(history) != 1 {
		return HelmRevision{}, errors.New("cannot read the Helm release history")
	}
	return history[0], nil
}

// Values returns the user-supplied values of a revision (0 selects the latest).
func (h Helm) Values(ctx context.Context, revision int) (map[string]any, error) {
	args := []string{"get", "values", h.Release, "--output", "json"}
	if revision > 0 {
		args = append(args, "--revision", strconv.Itoa(revision))
	}
	out, err := h.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	if err := json.Unmarshal(out, &values); err != nil {
		return nil, errors.New("cannot decode Helm release values")
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

func (h Helm) Template(ctx context.Context, chart, valuesFile string) ([]byte, error) {
	if h.TargetNamespace != "" {
		h.Namespace = h.TargetNamespace
	}
	return h.run(ctx, "template", h.Release, chart, "--values", valuesFile)
}

// Upgrade never waits or rolls back by itself: the procedure observes the outcome.
// Conflicts are forced because the procedure scales the operator Deployments
// itself between Helm revisions; server-side apply treats that change as
// another owner of spec.replicas. Enable server-side apply even when the source
// release used client-side apply; Helm's auto mode would retain that method.
// Only fields the chart declares are affected.
func (h Helm) Upgrade(ctx context.Context, chart, valuesFile string) error {
	_, err := h.run(ctx, "upgrade", h.Release, chart, "--values", valuesFile, "--history-max", "0", "--server-side=true", "--force-conflicts")
	return err
}

func (h Helm) Rollback(ctx context.Context, revision int) error {
	// Rollback's auto mode inherits the target revision's method, which can
	// still be client-side apply after an upgrade switched the release to SSA.
	_, err := h.run(ctx, "rollback", h.Release, strconv.Itoa(revision), "--history-max", "0", "--server-side=true", "--force-conflicts")
	return err
}

// chartValues layers the destination overlay and the procedure's settings over
// the source release's user-supplied values.
func chartValues(source, overlay map[string]any, replicas map[string]int32, hold bool) map[string]any {
	values := mergeValues(deepCopyMap(source), deepCopyMap(overlay))
	setValue(values, []string{"controller", "replicas"}, replicas["controller"])
	setValue(values, []string{"provisioner", "replicas"}, replicas["provisioner"])
	setValue(values, []string{"controller", "apiMigrationHold"}, hold)
	return values
}

func mergeValues(base, overlay map[string]any) map[string]any {
	for key, value := range overlay {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := base[key].(map[string]any); ok {
				base[key] = mergeValues(existing, child)
				continue
			}
		}
		base[key] = value
	}
	return base
}

func setValue(values map[string]any, path []string, value any) {
	for _, key := range path[:len(path)-1] {
		child, ok := values[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			values[key] = child
		}
		values = child
	}
	values[path[len(path)-1]] = value
}

func deepCopyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	data, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

// sameValues compares values after a JSON round trip, as Helm stores them.
func sameValues(a, b map[string]any) bool {
	return reflect.DeepEqual(deepCopyMap(a), deepCopyMap(b))
}

// ReleaseTargetNamespace reads the namespace recorded in the existing release.
// Helm 4 upgrade/rollback retain it even when release records live elsewhere.
func (h Helm) ReleaseTargetNamespace(ctx context.Context) (string, error) {
	data, err := h.run(ctx, "get", "metadata", h.Release, "--output", "json")
	if err != nil {
		return "", err
	}
	var metadata struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Namespace == "" {
		return "", errors.New("cannot read the Helm release target namespace")
	}
	return metadata.Namespace, nil
}
