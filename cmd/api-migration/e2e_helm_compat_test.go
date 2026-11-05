//go:build apimigration_e2e

package main

import (
	"encoding/json"

	"github.com/stretchr/testify/require"
)

// migrateClientSideHelm starts from a real client-side source revision, as a
// Helm 3 installation or a Helm 4 --server-side=false release would. Both the
// forward upgrade and rollback must select SSA despite that revision's method.
func (f *e2eFixture) migrateClientSideHelm() {
	f.t.Helper()
	f.cmd("helm", nil, "upgrade", e2eRelease, f.releaseChart(e2eSourceRelease),
		"--kubeconfig", f.kubeconfig, "--kube-context", f.context, "--namespace", e2eOperatorNS,
		"--reuse-values", "--server-side=false", "--wait", "--timeout", "300s")
	f.assertHelmApplyMethod("csa")
	f.waitSettled()

	state := f.newState("csa-rollback")
	f.plan(state)
	f.migrate(state)
	f.assertHelmApplyMethod("ssa")
	f.procedure("rollback", state)
	f.assertHelmApplyMethod("ssa")
	f.assertSurvivor(primitivesSourceGroup, "csa-rollback")
	f.migrateAndCommit(f.newState("csa-commit"))
	f.assertHelmApplyMethod("ssa")
}

func (f *e2eFixture) assertHelmApplyMethod(want string) {
	f.t.Helper()
	var metadata struct {
		ApplyMethod string `json:"applyMethod"`
		Status      string `json:"status"`
	}
	out := f.cmd("helm", nil, "get", "metadata", e2eRelease,
		"--kubeconfig", f.kubeconfig, "--kube-context", f.context, "--namespace", e2eOperatorNS,
		"--output", "json")
	require.NoError(f.t, json.Unmarshal([]byte(out), &metadata))
	require.Equal(f.t, "deployed", metadata.Status)
	require.Equal(f.t, want, metadata.ApplyMethod, "Helm's stored revision must use the selected apply method")
}
