//go:build apimigration_e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/stretchr/testify/require"
)

// migrateControllerHA rehearses the documented preparation in Helm release
// values, so rollback restores one replica and the surviving release can return
// to its normal controller replica count after commit.
func (f *e2eFixture) migrateControllerHA() {
	f.t.Helper()
	f.controllerReplicas(f.releaseChart(e2eSourceRelease), 2)
	require.ErrorContains(f.t, f.run("plan", f.newState("ha-refused"), "--config", f.configFile()), "must run one replica")
	f.controllerReplicas(f.releaseChart(e2eSourceRelease), 1)
	f.waitSettled()
	state := f.newState("ha-rollback")
	f.plan(state)
	f.migrate(state)
	f.procedure("rollback", state)
	f.assertControllerReplicaCount(1)
	f.assertSurvivor(primitivesSourceGroup, "ha-rollback")
	committed := f.newState("ha-commit")
	f.migrateAndCommit(committed)
	f.assertControllerReplicaCount(1)
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	f.controllerReplicas(filepath.Join(root, "charts/openbao-operator"), 2)
	f.assertSurvivor("kubebao.org", "ha-restored")
	var stdout, stderr bytes.Buffer
	require.NoError(f.t, run(context.Background(), []string{commandStatus, "--state", committed, "--output", "json"}, &stdout, &stderr), stderr.String())
	var summary migrationSummary
	require.NoError(f.t, json.Unmarshal(stdout.Bytes(), &summary))
	require.NotNil(f.t, summary.Operator)
	require.True(f.t, summary.Operator.Running, "status must accept healthy desired replicas restored after commit")
}

func (f *e2eFixture) controllerReplicas(chart string, replicas int) {
	f.t.Helper()
	path := filepath.Join(f.dir, "controller-replicas.json")
	require.NoError(f.t, writePrivateJSON(path, map[string]any{"controller": map[string]any{"replicas": replicas}}))
	f.cmd("helm", nil, "upgrade", e2eRelease, chart, "--kubeconfig", f.kubeconfig, "--kube-context", f.context,
		"--namespace", e2eOperatorNS, "--reuse-values", "--values", path, "--wait", "--timeout", "300s")
	f.assertControllerReplicaCount(replicas)
}

func (f *e2eFixture) assertControllerReplicaCount(replicas int) {
	f.t.Helper()
	deployment := f.getJSON("get", "deployment", "openbao-operator-controller", "-n", e2eOperatorNS)
	require.EqualValues(f.t, replicas, deployment["spec"].(map[string]any)["replicas"])
	require.EqualValues(f.t, replicas, deployment["status"].(map[string]any)["readyReplicas"])
}
