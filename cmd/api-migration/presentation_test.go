package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

func TestHelpNeedsNoStateOrKubernetes(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"help"}, {"help", "plan"}, {"plan", "help"},
		{"plan", "--help"}, {"migrate", "-h"}, {"commit", "--help"},
		{"rollback", "--help"}, {"status", "--help"}, {"convert", "--help"}, {"version", "-h"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			require.NoError(t, run(t.Context(), args, &stdout, &stderr))
			require.Contains(t, stdout.String(), "usage: api-migration")
			require.Empty(t, stderr.String())
		})
	}
}

func TestInvalidPlanConfigDoesNotCreateState(t *testing.T) {
	valid, err := json.Marshal(validConfig())
	require.NoError(t, err)
	for _, contents := range []string{"{", `{}`, `{"unknown":true}`, string(valid) + " trailing data", string(valid) + " {}"} {
		t.Run(contents, func(t *testing.T) {
			root := t.TempDir()
			config, state := filepath.Join(root, "config.json"), filepath.Join(root, "new-state")
			require.NoError(t, os.WriteFile(config, []byte(contents), 0o600))
			err := run(t.Context(), []string{"plan", "--state", state, "--config", config}, io.Discard, io.Discard)
			require.Error(t, err)
			_, err = os.Stat(state)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestPlanRefusesToClearExistingArtifacts(t *testing.T) {
	root := t.TempDir()
	config, state := filepath.Join(root, "config.json"), filepath.Join(root, "state")
	data, err := json.Marshal(validConfig())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(config, data, 0o600))
	require.NoError(t, os.Mkdir(state, 0o700))
	artifact := filepath.Join(state, "failed-plan-snapshot")
	require.NoError(t, os.WriteFile(artifact, []byte("preserve this artifact"), 0o600))
	err = run(t.Context(), []string{"plan", "--state", state, "--config", config}, io.Discard, io.Discard)
	require.ErrorContains(t, err, "preserve the existing artifacts")
	contents, err := os.ReadFile(artifact)
	require.NoError(t, err)
	require.Equal(t, "preserve this artifact", string(contents))
}

func TestCommandErrorsNameTheRequiredAction(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"unknown"}, "unknown command"},
		{[]string{"migrate"}, "requires --state DIR"},
		{[]string{"status", "--state", "unused", "extra"}, "unexpected argument"},
		{[]string{"status", "--state", "unused", "--output", "yaml"}, "--output must be text or json"},
	} {
		err := run(t.Context(), test.args, io.Discard, io.Discard)
		require.ErrorContains(t, err, test.want)
	}
}

func TestCommandLinePreservesShellArguments(t *testing.T) {
	args := []string{"api-migration", "commit", "--state", "/private/a b/'quoted'/$HOME/`id`", "", "$(false)", "line\nbreak"}
	line := commandLine(args...)
	output, err := exec.CommandContext(t.Context(), "sh", "-c", "printf '%s\\0' "+line).Output()
	require.NoError(t, err)
	require.Equal(t, strings.Join(args, "\x00")+"\x00", string(output))
	require.Equal(t, "api-migration commit --state /private/state", commandLine("api-migration", "commit", "--state", "/private/state"))
}

func TestSummaryJSONNeverEncodesPrivatePlanFields(t *testing.T) {
	p := &Procedure{state: &State{dir: "/private/migration state"}, plan: &Plan{
		Phase: phaseVerified, Config: Config{Context: "production", SourceVersion: sourceRelease,
			Clusters: []ClusterConfig{{OpenBao: &OpenBaoConfig{TokenFile: "private-token-path"}}}},
		Helm: HelmPlan{SourceValues: map[string]any{"password": "private-source-password"},
			DestinationOverlay: map[string]any{"password": "private-overlay-password"}},
		Operator: OperatorPlan{Version: sourceRelease}, DestinationVersion: "0.7.0",
		Progress:         Progress{Command: "migrate", Completed: []string{"verify"}},
		LastVerification: &VerificationRecord{CheckedAt: time.Now().UTC(), Step: "verify", APIVersion: primitives.PlanDestinationAPIVersion},
		Clusters:         []ClusterPlan{{Namespace: "data", Name: "bao", SpecDigest: "private-spec-digest"}},
	}}
	summary := p.baseSummary()
	summary.NextActions = p.nextActions(summary.RollbackWindow)
	var output bytes.Buffer
	require.NoError(t, renderSummary(&output, "json", summary))
	for _, forbidden := range []string{"private-source-password", "private-overlay-password", "private-token-path", "private-spec-digest", "sourceValues", "destinationOverlay", "config"} {
		require.NotContains(t, output.String(), forbidden)
	}
	var decoded migrationSummary
	require.NoError(t, json.Unmarshal(output.Bytes(), &decoded))
	require.Equal(t, "production", decoded.Context)
	require.Equal(t, "0.7.0", decoded.DestinationRelease)
	require.Equal(t, "verify", decoded.LastCompletedStep)
	require.Contains(t, decoded.NextActions[0].Command, "--state '/private/migration state'")
	require.Contains(t, decoded.VerificationScope, "saved evidence only")
	output.Reset()
	require.NoError(t, renderSummary(&output, "text", summary))
	require.Contains(t, output.String(), "Last successful verification (recorded)")
	require.Contains(t, output.String(), "Live state: not observed; run status")
	require.NotContains(t, output.String(), "Rollback window", "a recorded summary does not report the unobserved window")
}

func TestRollbackWindowUsesEverySourceIdentityAndDeletionState(t *testing.T) {
	intact := objectStatus{Cluster: "data/bao", Source: "source", ExpectedSource: "source", SourceObserved: true}
	for _, test := range []struct {
		name, want string
		change     func(*objectStatus)
	}{
		{"intact", "open", func(*objectStatus) {}},
		{"retired", "closed", func(s *objectStatus) { s.Source = "" }},
		{"replaced", "closed", func(s *objectStatus) { s.Source = "new-source" }},
		{"deleting", "closed", func(s *objectStatus) { s.SourceDeleting = true }},
		{"read failed", "unknown", func(s *objectStatus) { s.SourceObserved = false }},
		{"expected UID unknown", "unknown", func(s *objectStatus) { s.ExpectedSource = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := intact
			test.change(&entry)
			require.Equal(t, test.want, observedRollbackWindow([]objectStatus{entry}).State)
		})
	}
	require.Equal(t, "unknown", observedRollbackWindow().State)
	missingRecord := objectStatus{Cluster: "data/restore", SourceObserved: true, ExpectedSource: "record"}
	require.Equal(t, "closed", observedRollbackWindow([]objectStatus{intact}, []objectStatus{missingRecord}).State)
}

func inventoryIdentityFixture(t *testing.T, kind string, destinationUID types.UID) (*Procedure, *unstructured.Unstructured) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, version := range []string{primitives.PlanSourceAPIVersion, primitives.PlanDestinationAPIVersion} {
		for _, resource := range []string{clusterKind, restoreKind, tenantKind} {
			gvk := schema.FromAPIVersionAndKind(version, resource)
			scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(resource+"List"), &unstructured.UnstructuredList{})
		}
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(schema.FromAPIVersionAndKind(primitives.PlanSourceAPIVersion, kind))
	object.SetNamespace("data")
	object.SetName("bao")
	object.SetUID("planned-source")
	p := &Procedure{kube: &Kube{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(object).Build()},
		state: &State{dir: t.TempDir()}, log: io.Discard,
		plan: &Plan{Phase: phaseCommitting, Operator: OperatorPlan{Mode: tenancyMulti}}}
	if kind == clusterKind {
		p.plan.Clusters = []ClusterPlan{{Namespace: "data", Name: "bao", SourceUID: "planned-source", DestinationUID: destinationUID}}
	} else {
		record := RecordPlan{Kind: kind, Namespace: "data", Name: "bao", SourceUID: "planned-source", DestinationUID: destinationUID}
		if kind == restoreKind {
			p.plan.Restores = []RecordPlan{record}
		} else {
			p.plan.Tenants = []RecordPlan{record}
		}
	}
	return p, object
}

func TestInactiveInventoryRejectsRecreatedSourceIdentity(t *testing.T) {
	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		t.Run(kind, func(t *testing.T) {
			p, original := inventoryIdentityFixture(t, kind, "destination")
			require.NoError(t, p.kube.client.Delete(t.Context(), original))
			require.NoError(t, p.inventoryMatches(t.Context(), primitives.PlanSourceAPIVersion, true), "retired sources may be absent")
			replacement := original.DeepCopy()
			replacement.SetResourceVersion("")
			replacement.SetUID("recreated-by-shared-bundle")
			require.NoError(t, p.kube.client.Create(t.Context(), replacement))
			require.ErrorContains(t, p.inventoryMatches(t.Context(), primitives.PlanSourceAPIVersion, true), "was replaced since planning")
			_, err := p.verify(t.Context(), primitives.PlanDestinationAPIVersion)
			require.ErrorContains(t, err, "expected UID planned-source", "the final destination verification must reject an inactive replacement before custody discovery")
			observed, err := p.kube.getObject(t.Context(), primitives.PlanSourceAPIVersion, kind, "data", "bao")
			require.NoError(t, err)
			require.Equal(t, types.UID("recreated-by-shared-bundle"), observed.GetUID(), "the unexpected object is never deleted or adopted")
		})
	}
}

func TestInventoryAllowsAnUnrecordedDestinationUIDAfterLostResponse(t *testing.T) {
	p, source := inventoryIdentityFixture(t, clusterKind, "")
	destination := source.DeepCopy()
	destination.SetAPIVersion(primitives.PlanDestinationAPIVersion)
	destination.SetUID("accepted-import")
	destination.SetResourceVersion("")
	destination.SetAnnotations(map[string]string{primitives.GateSourceUIDAnnotation: "planned-source"})
	require.NoError(t, p.kube.client.Create(t.Context(), destination))
	require.NoError(t, p.inventoryMatches(t.Context(), primitives.PlanDestinationAPIVersion, false))
	resolved, err := p.destinationIdentity(t.Context(), &p.plan.Clusters[0])
	require.NoError(t, err)
	require.Equal(t, destination.GetUID(), resolved.GetUID())
	require.Equal(t, destination.GetUID(), p.plan.Clusters[0].DestinationUID)
	destination.SetUID("another-import")
	p.kube.client = fake.NewClientBuilder().WithScheme(p.kube.client.Scheme()).WithObjects(destination).Build()
	require.ErrorContains(t, p.inventoryMatches(t.Context(), primitives.PlanDestinationAPIVersion, false), "was replaced since planning")
	_, err = p.destinationIdentity(t.Context(), &p.plan.Clusters[0])
	require.ErrorContains(t, err, "was not created by this migration")
	// An unknown UID does not bypass the independent import-marker guard.
	p.plan.Clusters[0].DestinationUID = ""
	destination.SetAnnotations(nil)
	p.kube.client = fake.NewClientBuilder().WithScheme(p.kube.client.Scheme()).WithObjects(destination).Build()
	_, err = p.destinationIdentity(t.Context(), &p.plan.Clusters[0])
	require.ErrorContains(t, err, "was not created by this migration")
}

func TestRollbackRefusalRecommendsSameStateCommit(t *testing.T) {
	p, source := inventoryIdentityFixture(t, clusterKind, "destination")
	p.state.dir = "/private/migration state"
	require.NoError(t, p.kube.client.Delete(t.Context(), source))
	err := p.rollbackPossible(t.Context())
	require.ErrorContains(t, err, "was retired")
	require.ErrorContains(t, err, "Resume commit with the same state: api-migration commit --state '/private/migration state'")
	require.NotContains(t, err.Error(), "restore from")
	require.Equal(t, phaseCommitting, p.plan.Phase)
}

func TestUnexpectedSourceLossDoesNotRecommendAnUnavailableCommit(t *testing.T) {
	for _, phase := range []string{phasePlanned, phaseMigrating, phaseRollingBack} {
		t.Run(phase, func(t *testing.T) {
			p, source := inventoryIdentityFixture(t, clusterKind, "")
			p.plan.Phase = phase
			require.NoError(t, p.kube.client.Delete(t.Context(), source))
			err := p.rollbackPossible(t.Context())
			require.ErrorContains(t, err, "inspect the source, destination, and workload ownership")
			require.NotContains(t, err.Error(), "api-migration commit")
			actions := p.nextActions(rollbackWindow{State: "closed"})
			require.Len(t, actions, 1)
			require.Contains(t, actions[0].Command, "api-migration status")
			require.Equal(t, phase, p.plan.Phase)
		})
	}
}

func TestInterruptedSummaryKeepsProgressAndDoesNotObserveKubernetes(t *testing.T) {
	state, err := openState(filepath.Join(t.TempDir(), "state"), true)
	require.NoError(t, err)
	defer state.Close()
	p := &Procedure{state: state, log: io.Discard, stopAfter: "verify", plan: &Plan{Phase: phaseVerified}}
	// A completed step skips its action but preserves the exact parser line.
	p.plan.Progress = Progress{Command: "reverify", Completed: []string{"verify"}}
	var log bytes.Buffer
	p.log = &log
	require.ErrorIs(t, p.step(context.Background(), "verify", func() (string, error) {
		t.Fatal("a completed step must not run again")
		return "", nil
	}), errStop)
	require.Equal(t, "==> verify\n    already done\n", log.String())
	var output bytes.Buffer
	require.NoError(t, p.recordedSummary(&output))
	require.Contains(t, output.String(), "Last completed step: verify (reverify)")
	require.Contains(t, output.String(), "Live state: not observed; run status")
	require.Contains(t, output.String(), "api-migration commit --state "+state.dir)
}

func TestRollbackWindowRecognizesDeletingSourceFromLiveObject(t *testing.T) {
	p, source := inventoryIdentityFixture(t, clusterKind, "destination")
	now := metav1.Now()
	source.SetFinalizers([]string{"test/keep-deleting"})
	source.SetDeletionTimestamp(&now)
	p.kube.client = fake.NewClientBuilder().WithScheme(p.kube.client.Scheme()).WithObjects(source).Build()
	summary, err := p.observeSummary(t.Context())
	require.Error(t, err, "the missing operator observation is reported instead of assuming health")
	require.Nil(t, summary.Operator)
	require.Equal(t, "closed", summary.RollbackWindow.State)
	require.True(t, summary.Clusters[0].SourceDeleting)
	require.NotEmpty(t, summary.ObservationErrors)
	var output bytes.Buffer
	require.Error(t, p.Status(t.Context(), &output, "json"))
	require.True(t, json.Valid(output.Bytes()), "partial live status still has a complete JSON result")
}

func TestVerificationMetadataRecordsOnlySuccessfulChecks(t *testing.T) {
	state, err := openState(filepath.Join(t.TempDir(), "state"), true)
	require.NoError(t, err)
	defer state.Close()
	p := &Procedure{state: state, log: io.Discard, plan: &Plan{Version: planVersion,
		Progress: Progress{Command: "commit"}}}
	err = p.step(t.Context(), "verify", func() (string, error) { return "", context.Canceled })
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, p.plan.LastVerification)
	require.Empty(t, p.plan.Progress.Completed)
	require.NoError(t, p.step(t.Context(), "verify", func() (string, error) { return "verified", nil }))
	saved, err := state.Load()
	require.NoError(t, err)
	require.NotNil(t, saved.LastVerification)
	require.Equal(t, primitives.PlanDestinationAPIVersion, saved.LastVerification.APIVersion)
	require.Equal(t, "verify", saved.LastVerification.Step)
	require.False(t, saved.LastVerification.CheckedAt.IsZero())
	p.begin("rollback")
	require.NoError(t, p.step(t.Context(), "verify-source", func() (string, error) { return "verified", nil }))
	saved, err = state.Load()
	require.NoError(t, err)
	require.Equal(t, primitives.PlanSourceAPIVersion, saved.LastVerification.APIVersion)
}

func TestSummaryShowsEvidenceWithoutReplacingUnknownChecks(t *testing.T) {
	p := &Procedure{state: &State{dir: "/private/state"}, program: "/path with spaces/api-migration-0.7.0",
		plan: &Plan{Phase: phasePlanned, Clusters: []ClusterPlan{
			{Name: "healthy", Snapshot: "snapshot.raft"},
			{Name: "paused", Paused: true, LoginSkipped: "paused before initialization"},
		}}}
	summary := p.baseSummary()
	summary.NextActions = p.nextActions(summary.RollbackWindow)
	var text bytes.Buffer
	require.NoError(t, renderSummary(&text, "text", summary))
	require.Contains(t, text.String(), "1/2 snapshots recorded; 1 paused Clusters; 1 controller logins skipped")
	require.Contains(t, text.String(), "Last successful verification: none recorded")
	require.Contains(t, text.String(), "'/path with spaces/api-migration-0.7.0' migrate --state /private/state")
	require.Equal(t, unknownValue, summary.RollbackWindow.State, "saved metadata alone cannot establish the rollback window")
}
