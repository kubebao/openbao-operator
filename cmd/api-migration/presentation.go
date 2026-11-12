package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

const unknownValue = "unknown"

// migrationSummary allowlists public information. Never encode Plan here:
// its Helm values and saved source documents can contain credentials.
type migrationSummary struct {
	Phase              string              `json:"phase"`
	Context            string              `json:"context"`
	StateDirectory     string              `json:"stateDirectory"`
	SourceRelease      string              `json:"sourceRelease"`
	DestinationRelease string              `json:"destinationRelease,omitempty"`
	OperatorNamespace  string              `json:"operatorNamespace"`
	ReleaseNamespace   string              `json:"releaseNamespace,omitempty"`
	Counts             objectCounts        `json:"counts"`
	Release            releaseStatus       `json:"release"`
	Operator           *operatorStatus     `json:"operator,omitempty"`
	Progress           Progress            `json:"progress"`
	LastCompletedStep  string              `json:"lastCompletedStep,omitempty"`
	LastVerification   *VerificationRecord `json:"lastVerification,omitempty"`
	VerificationScope  string              `json:"verificationScope"`
	Clusters           []objectStatus      `json:"clusters"`
	Restores           []objectStatus      `json:"restores,omitempty"`
	Tenants            []objectStatus      `json:"tenants,omitempty"`
	CheckedAt          *time.Time          `json:"checkedAt,omitempty"`
	RollbackWindow     rollbackWindow      `json:"rollbackWindow"`
	NextActions        []nextAction        `json:"nextActions"`
	ObservationErrors  []string            `json:"observationErrors,omitempty"`
}

type objectCounts struct {
	Clusters     int `json:"clusters"`
	Restores     int `json:"restores"`
	Tenants      int `json:"tenants"`
	Snapshots    int `json:"snapshotsRecorded"`
	Paused       int `json:"pausedAtPlan"`
	LoginSkipped int `json:"controllerLoginsSkippedAtPlan"`
}

// releaseStatus preserves the original status release fields for both installers.
type releaseStatus struct {
	Install    string `json:"install,omitempty"`
	Revision   int    `json:"revision,omitempty"`
	Status     string `json:"status,omitempty"`
	Chart      string `json:"chart,omitempty"`
	Observed   string `json:"observed,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
}

type operatorStatus struct {
	Version string `json:"version,omitempty"`
	Group   string `json:"group,omitempty"`
	Stopped bool   `json:"stopped"`
	Running bool   `json:"running"`
	Held    *bool  `json:"held,omitempty"`
}

type objectStatus struct {
	Cluster             string    `json:"cluster"`
	Source              types.UID `json:"source,omitempty"`
	Destination         types.UID `json:"destination,omitempty"`
	ExpectedSource      types.UID `json:"expectedSource,omitempty"`
	ExpectedDestination types.UID `json:"expectedDestination,omitempty"`
	SourceObserved      bool      `json:"sourceObserved"`
	SourceDeleting      bool      `json:"sourceDeleting,omitempty"`
	DestinationObserved bool      `json:"destinationObserved"`
	DestinationDeleting bool      `json:"destinationDeleting,omitempty"`
}

type rollbackWindow struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type nextAction struct {
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
}

func (p *Procedure) baseSummary() migrationSummary {
	summary := migrationSummary{Phase: p.plan.Phase, Context: p.plan.Config.Context, StateDirectory: p.state.dir,
		SourceRelease: p.plan.Operator.Version, DestinationRelease: p.plan.DestinationVersion,
		OperatorNamespace: p.plan.Config.OperatorNamespace,
		Counts:            objectCounts{Clusters: len(p.plan.Clusters), Restores: len(p.plan.Restores), Tenants: len(p.plan.Tenants)},
		Progress:          p.plan.Progress, LastVerification: p.plan.LastVerification,
		VerificationScope: "saved evidence only; status does not repeat migration verification",
		Release:           releaseStatus{Install: p.plan.Operator.Install, Checkpoint: p.plan.Operator.Checkpoint},
		RollbackWindow:    rollbackWindow{State: unknownValue, Reason: "source identities and deletion state were not observed"}}
	for _, cluster := range p.plan.Clusters {
		if cluster.Snapshot != "" {
			summary.Counts.Snapshots++
		}
		if cluster.Paused {
			summary.Counts.Paused++
		}
		if cluster.LoginSkipped != "" {
			summary.Counts.LoginSkipped++
		}
	}
	if p.plan.Operator.Install == installHelm {
		summary.ReleaseNamespace = p.plan.Config.helmNamespace()
	}
	if summary.SourceRelease == "" {
		summary.SourceRelease = p.plan.Config.SourceVersion
	}
	if summary.DestinationRelease == "" && p.plan.Helm.DestinationChart != "" {
		// Older format-2 plans do not record the destination release separately.
		summary.DestinationRelease, _ = chartAppVersion(p.plan.Helm.DestinationChart)
	}
	if completed := p.plan.Progress.Completed; len(completed) > 0 {
		summary.LastCompletedStep = completed[len(completed)-1]
	}
	return summary
}

func (p *Procedure) observeSummary(ctx context.Context) (migrationSummary, error) {
	summary := p.baseSummary()
	now := time.Now().UTC()
	summary.CheckedAt = &now
	var problems []error
	observed, err := p.observeOperator(ctx)
	if err != nil {
		problems = append(problems, fmt.Errorf("observe operator: %w", err))
	} else {
		// Migration transitions keep the planned counts. Status also accepts
		// healthy desired counts restored after commit or rollback.
		observed.running = observed.readyAtDesired
		summary.Release.Observed = observed.String()
		summary.Operator = &operatorStatus{Version: observed.version, Group: observed.group,
			Stopped: observed.stopped, Running: observed.running}
		if summary.DestinationRelease == "" && observed.group == stateNormal.group {
			summary.DestinationRelease = observed.version
		}
		if observed.version != "" {
			held := observed.held
			summary.Operator.Held = &held
		}
	}
	if p.plan.Operator.Install == installHelm {
		latest, err := p.helm.Latest(ctx)
		if err != nil {
			problems = append(problems, fmt.Errorf("observe Helm release: %w", err))
		} else {
			summary.Release.Revision, summary.Release.Status, summary.Release.Chart = latest.Revision, latest.Status, latest.Chart
		}
	}
	observe := func(kind, namespace, name string, source, destination types.UID) objectStatus {
		entry := objectStatus{Cluster: namespace + "/" + name, ExpectedSource: source, ExpectedDestination: destination}
		for _, side := range []struct {
			apiVersion string
			uid        *types.UID
			observed   *bool
			deleting   *bool
		}{{primitives.PlanSourceAPIVersion, &entry.Source, &entry.SourceObserved, &entry.SourceDeleting},
			{primitives.PlanDestinationAPIVersion, &entry.Destination, &entry.DestinationObserved, &entry.DestinationDeleting}} {
			object, err := p.kube.getObject(ctx, side.apiVersion, kind, namespace, name)
			if err != nil {
				problems = append(problems, fmt.Errorf("observe %s %s %s: %w", side.apiVersion, kind, entry.Cluster, err))
				continue
			}
			*side.observed = true
			if object != nil {
				*side.uid, *side.deleting = object.GetUID(), object.GetDeletionTimestamp() != nil
			}
		}
		return entry
	}
	for _, cluster := range p.plan.Clusters {
		summary.Clusters = append(summary.Clusters,
			observe(clusterKind, cluster.Namespace, cluster.Name, cluster.SourceUID, cluster.DestinationUID))
	}
	for _, record := range p.plan.Restores {
		summary.Restores = append(summary.Restores,
			observe(record.Kind, record.Namespace, record.Name, record.SourceUID, record.DestinationUID))
	}
	for _, record := range p.plan.Tenants {
		summary.Tenants = append(summary.Tenants,
			observe(record.Kind, record.Namespace, record.Name, record.SourceUID, record.DestinationUID))
	}
	summary.RollbackWindow = observedRollbackWindow(summary.Clusters, summary.Restores, summary.Tenants)
	for _, problem := range problems {
		summary.ObservationErrors = append(summary.ObservationErrors, problem.Error())
	}
	summary.NextActions = p.nextActions(summary.RollbackWindow)
	return summary, errors.Join(problems...)
}

// observedRollbackWindow uses source identities, never the informational phase.
func observedRollbackWindow(sets ...[]objectStatus) rollbackWindow {
	count, unknown := 0, false
	for _, entries := range sets {
		for _, entry := range entries {
			count++
			if !entry.SourceObserved || entry.ExpectedSource == "" {
				unknown = true
				continue
			}
			if entry.Source == "" || entry.Source != entry.ExpectedSource || entry.SourceDeleting {
				return rollbackWindow{State: "closed", Reason: entry.Cluster + ": planned source is absent, replaced, or deleting"}
			}
		}
	}
	if unknown || count == 0 {
		return rollbackWindow{State: unknownValue, Reason: "not every planned source identity and deletion state could be confirmed"}
	}
	return rollbackWindow{State: "open", Reason: "every planned source UID exists and no source deletion has started; rollback checks further preconditions"}
}

func (p *Procedure) nextActions(window rollbackWindow) []nextAction {
	command := p.stateCommand
	if window.State == "closed" && p.plan.Phase != phaseCommitting && p.plan.Phase != phaseComplete && p.plan.Phase != phaseRolledBack {
		return []nextAction{{"Preserve the state and inspect the unexpected source change and workload ownership before continuing", command(commandStatus)}}
	}
	switch p.plan.Phase {
	case phasePlanned:
		return []nextAction{{"Review the private plan at " + p.state.path(planFile) + ", then start migration", command(commandMigrate)}}
	case phaseMigrating:
		return []nextAction{{"Resume migration with the same state directory", command(commandMigrate)}}
	case phaseCommitting:
		return []nextAction{{"Resume commit with the same state directory", command(commandCommit)}}
	case phaseRollingBack:
		return []nextAction{{"Resume rollback with the same state directory", command(commandRollback)}}
	case phaseVerified:
		actions := []nextAction{{"Repeat verification while operations stay held", command(commandMigrate)},
			{"Commit when ready; the first accepted source deletion closes the rollback window", command(commandCommit)}}
		if window.State == "open" {
			actions = append(actions, nextAction{"Or return to the source installation", command(commandRollback)})
		}
		return actions
	case phaseComplete:
		return []nextAction{{"Follow any GitOps handback instructions in the state directory, then check live status", command(commandStatus)}}
	case phaseRolledBack:
		return []nextAction{{"Follow any GitOps handback instructions in the state directory, then check the source installation", command(commandStatus)}}
	default:
		return []nextAction{{"Inspect the saved migration and live objects before continuing", command(commandStatus)}}
	}
}

func (p *Procedure) recordedSummary(out io.Writer) error {
	summary := p.baseSummary()
	summary.NextActions = p.nextActions(summary.RollbackWindow)
	return renderSummary(out, "text", summary)
}

// Summary reports a completed command without making its success depend on
// additional diagnostic reads. Status returns observation errors to its caller.
func (p *Procedure) Summary(ctx context.Context, out io.Writer, output string) error {
	summary, _ := p.observeSummary(ctx)
	return renderSummary(out, output, summary)
}

func (p *Procedure) Status(ctx context.Context, out io.Writer, output string) error {
	summary, err := p.observeSummary(ctx)
	return errors.Join(err, renderSummary(out, output, summary))
}

func renderSummary(out io.Writer, output string, summary migrationSummary) error {
	if output == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summary)
	}
	_, _ = fmt.Fprintf(out, "Phase: %s\nContext: %s\nState: %s\nRelease: %s -> %s\nObjects: %d Clusters, %d restores, %d Tenants\n",
		summary.Phase, summary.Context, summary.StateDirectory, known(summary.SourceRelease), known(summary.DestinationRelease),
		summary.Counts.Clusters, summary.Counts.Restores, summary.Counts.Tenants)
	_, _ = fmt.Fprintf(out, "Evidence at plan: %d/%d snapshots recorded; %d paused Clusters; %d controller logins skipped\n",
		summary.Counts.Snapshots, summary.Counts.Clusters, summary.Counts.Paused, summary.Counts.LoginSkipped)
	_, _ = fmt.Fprintln(out, "Operator namespace:", known(summary.OperatorNamespace))
	if summary.ReleaseNamespace != "" && summary.ReleaseNamespace != summary.OperatorNamespace {
		_, _ = fmt.Fprintln(out, "Helm release storage namespace:", summary.ReleaseNamespace)
	}
	if summary.Release.Revision != 0 {
		_, _ = fmt.Fprintf(out, "Helm release: revision %d, %s, %s\n", summary.Release.Revision, summary.Release.Chart, summary.Release.Status)
	}
	if summary.CheckedAt != nil {
		_, _ = fmt.Fprintf(out, "Live observations: %s (do not repeat migration verification)\n", summary.CheckedAt.Format(time.RFC3339))
	}
	switch {
	case summary.CheckedAt == nil:
		// A stopped command does not read the cluster again; status does.
		_, _ = fmt.Fprintln(out, "Live state: not observed; run status to observe the operator and the rollback window")
	case summary.Operator == nil:
		_, _ = fmt.Fprintln(out, "Operator: unknown; operations hold: unknown")
	default:
		held := unknownValue
		if summary.Operator.Held != nil {
			held = fmt.Sprint(*summary.Operator.Held)
		}
		_, _ = fmt.Fprintf(out, "Operator: release=%s, group=%s, stopped=%t, running=%t; operations hold=%s\n",
			known(summary.Operator.Version), known(summary.Operator.Group), summary.Operator.Stopped, summary.Operator.Running, held)
	}
	_, _ = fmt.Fprintf(out, "Last completed step: %s", known(summary.LastCompletedStep))
	if summary.Progress.Command != "" {
		_, _ = fmt.Fprintf(out, " (%s)", summary.Progress.Command)
	}
	_, _ = fmt.Fprintln(out)
	if check := summary.LastVerification; check != nil {
		_, _ = fmt.Fprintf(out, "Last successful verification (recorded): %s, %s at %s\n", check.Step, check.APIVersion, check.CheckedAt.Format(time.RFC3339))
	} else {
		_, _ = fmt.Fprintln(out, "Last successful verification: none recorded (older plans might lack this metadata)")
	}
	if summary.CheckedAt != nil {
		_, _ = fmt.Fprintf(out, "Rollback window: %s — %s\n", summary.RollbackWindow.State, summary.RollbackWindow.Reason)
	}
	for _, set := range []struct {
		kind    string
		entries []objectStatus
	}{{clusterKind, summary.Clusters}, {restoreKind, summary.Restores}, {tenantKind, summary.Tenants}} {
		for _, entry := range set.entries {
			_, _ = fmt.Fprintf(out, "%s %s: source=%s; destination=%s\n", set.kind, entry.Cluster,
				observedIdentity(entry.Source, entry.ExpectedSource, entry.SourceObserved, entry.SourceDeleting),
				observedIdentity(entry.Destination, entry.ExpectedDestination, entry.DestinationObserved, entry.DestinationDeleting))
		}
	}
	for _, problem := range summary.ObservationErrors {
		_, _ = fmt.Fprintln(out, "Observation failed:", problem)
	}
	for _, action := range summary.NextActions {
		_, _ = fmt.Fprintln(out, "Next:", action.Description)
		if action.Command != "" {
			if _, err := fmt.Fprintln(out, "  "+action.Command); err != nil {
				return err
			}
		}
	}
	return nil
}

func observedIdentity(uid, expected types.UID, observed, deleting bool) string {
	if !observed {
		return unknownValue
	}
	if uid == "" {
		return "absent"
	}
	if expected != "" && uid != expected {
		return "replaced (unexpected UID)"
	}
	if deleting {
		return "deleting"
	}
	if expected == "" {
		return "present (UID not recorded)"
	}
	return "present (planned UID)"
}

func known(value string) string {
	if value == "" {
		return unknownValue
	}
	return value
}
