//go:build apimigration_e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The recovery shape qualifies recovery in an installation with two Clusters:
// responses lost after the API server applied a write, interruptions inside
// commit's source retirement and rollback's reverse ownership transfer, and a
// stop at every commit and rollback step. Once commit deletes a source,
// rollback must refuse and commit must still finish.

const e2eSecond = "bao-second"

var errLostResponse = errors.New("simulated lost response; the API server applied the write")

// lossyClient applies each write, then reports the response as lost when lose
// selects it.
type lossyClient struct {
	client.Client
	lose func(verb string, object client.Object) bool
}

func (c lossyClient) result(verb string, object client.Object, err error) error {
	if err == nil && c.lose(verb, object) {
		return fmt.Errorf("%s %s %s: %w", verb, kindOf(object), object.GetName(), errLostResponse)
	}
	return err
}

func (c lossyClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	return c.result("create", object, c.Client.Create(ctx, object, opts...))
}

func (c lossyClient) Update(ctx context.Context, object client.Object, opts ...client.UpdateOption) error {
	return c.result("update", object, c.Client.Update(ctx, object, opts...))
}

func (c lossyClient) Patch(ctx context.Context, object client.Object, patch client.Patch,
	opts ...client.PatchOption,
) error {
	return c.result("patch", object, c.Client.Patch(ctx, object, patch, opts...))
}

func (c lossyClient) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	return c.result("delete", object, c.Client.Delete(ctx, object, opts...))
}

// kindOf names an object's kind; typed objects carry no TypeMeta.
func kindOf(object client.Object) string {
	if kind := object.GetObjectKind().GroupVersionKind().Kind; kind != "" {
		return kind
	}
	return reflect.TypeOf(object).Elem().Name()
}

// loseOnce selects the first write with verb on kind, or on any kind.
func loseOnce(verb, kind string) func(string, client.Object) bool {
	var lost atomic.Bool
	return func(v string, object client.Object) bool {
		return v == verb && (kind == "" || kindOf(object) == kind) && lost.CompareAndSwap(false, true)
	}
}

// runLosing runs a command whose selected write loses its response, and
// requires it to fail with that error.
func (f *e2eFixture) runLosing(command, state string, lose func(string, client.Object) bool) {
	f.t.Helper()
	wrapWriter = func(c client.Client) client.Client { return lossyClient{Client: c, lose: lose} }
	defer func() { wrapWriter = func(c client.Client) client.Client { return c } }()
	require.ErrorIs(f.t, f.run(command, state), errLostResponse, "%s must report the lost response", command)
}

// stopAt runs a command until seam stops it the first time it runs.
func (f *e2eFixture) stopAt(seam *func() error, command, state string) {
	f.t.Helper()
	var stopped atomic.Bool
	*seam = func() error {
		if stopped.CompareAndSwap(false, true) {
			return errStop
		}
		return nil
	}
	defer func() { *seam = func() error { return nil } }()
	require.ErrorIs(f.t, f.run(command, state), errStop, "%s must stop at the seam", command)
}

// stopAfterEach runs command again after each named step, so every run
// resumes where the previous one stopped.
func (f *e2eFixture) stopAfterEach(command, state string, steps ...string) {
	f.t.Helper()
	for _, step := range steps {
		require.ErrorIs(f.t, f.run(command, state, "--stop-after", step), errStop, "%s --stop-after %s", command, step)
	}
}

// createSecondCluster adds an initialized Cluster next to the first one.
func (f *e2eFixture) createSecondCluster() {
	f.t.Helper()
	data, _ := json.Marshal(map[string]any{"apiVersion": primitivesSourceGroup + "/v1alpha1", "kind": "OpenBaoCluster",
		"metadata": map[string]any{"name": e2eSecond, "namespace": e2eDataNS},
		"spec":     f.cluster(primitivesSourceGroup).Object["spec"]})
	f.kubectl(data, "apply", "-f", "-")
	f.eventually("the second Cluster to initialize", 10*time.Minute, func() bool {
		return clusterReady(f.clusterNamed(primitivesSourceGroup, e2eSecond))
	})
}

// assertSecond checks the second Cluster survived in the active group.
func (f *e2eFixture) assertSecond(group, proof string) {
	f.t.Helper()
	other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
	require.Nil(f.t, f.clusterNamed(other, e2eSecond), "the %s second Cluster must be retired", other)
	f.assertReconciles(group, e2eSecond, proof)
}

func (f *e2eFixture) recover() {
	f.t.Helper()
	f.createSecondCluster()

	state := f.newState("rollback")
	f.plan(state)
	// The API server recreates a StatefulSet, but the response is lost.
	f.runLosing("migrate", state, loseOnce("create", "StatefulSet"))
	f.migrate(state)

	// Roll back with a stop at every step, two of them inside the reverse
	// ownership transfer: one between two moves, one after a lost response.
	f.stopAfterEach("rollback", state, "stop-operators", "migrator", "open-handoff", "finish-statefulsets")
	f.stopAt(&afterOwnershipMoved, "rollback", state)
	f.runLosing("rollback", state, loseOnce("patch", ""))
	f.stopAfterEach("rollback", state, "transfer-back", "statefulsets-back", "close-handoff", "restore-source",
		"retire-destination", "verify-source")
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "rollback")
	f.assertSecond(primitivesSourceGroup, "rollback")

	// Commit with a stop at every step and two inside source retirement.
	state = f.newState("migrate")
	f.plan(state)
	f.migrate(state)
	f.stopAfterEach("commit", state, "verify")
	// A controller adds a finalizer to the later Cluster after planning.
	// Retirement must inspect the entire batch before deleting the first one.
	second := f.clusterNamed(primitivesSourceGroup, e2eSecond)
	originalFinalizers := second.GetFinalizers()
	setFinalizers := func(finalizers []string) {
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"finalizers": finalizers}})
		require.NoError(f.t, err)
		f.kubectl(nil, "patch", "openbaoclusters."+primitivesSourceGroup, e2eSecond, "-n", e2eDataNS,
			"--type=merge", "-p", string(patch))
	}
	setFinalizers(append(append([]string{}, originalFinalizers...), "example.org/protection"))
	require.ErrorContains(f.t, f.run("commit", state), "unknown finalizer")
	for _, name := range []string{e2eCluster, e2eSecond} {
		source := f.clusterNamed(primitivesSourceGroup, name)
		require.NotNil(f.t, source)
		require.Nil(f.t, source.GetDeletionTimestamp(), "a later finalizer must leave every source intact")
	}
	setFinalizers(originalFinalizers)
	f.stopAt(&afterSourceRetired, "commit", state)
	require.ErrorContains(f.t, f.run("rollback", state), "was retired",
		"rollback needs every source; after the first deletion only commit can finish")
	f.runLosing("commit", state, loseOnce("delete", clusterKind))
	f.stopAfterEach("commit", state, "retire-source", "release-hold", "final-verify")
	f.procedure("commit", state)
	f.assertSurvivor("kubebao.org", "commit")
	f.assertSecond("kubebao.org", "commit")
}
