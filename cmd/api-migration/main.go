// Command api-migration moves an OpenBao Operator installation
// from openbao.org/v1alpha1 to kubebao.org/v1alpha1 without rebuilding its
// OpenBao clusters. See README.md for the procedure and its scope.
//
// Every step observes the installation and only does what is still missing, so
// re-running a command after an interruption continues where it stopped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	commandPlan     = "plan"
	commandMigrate  = "migrate"
	commandCommit   = "commit"
	commandRollback = "rollback"
	commandStatus   = "status"
	commandHelpName = "help"
)

// version is the operator release or edge version this binary belongs to, and the
// only destination it migrates to. Packaged builds set it with -X main.version; a
// development build migrates to whatever destination it is given.
var version = developmentVersion

const developmentVersion = "dev"

// sourceRelease is the only source release this command migrates from. Its
// handoff messages, lock policy, and qualification are pinned to that release.
const sourceRelease = "0.6.1"

// destinationRelease requires the destination to be the release this binary
// belongs to: its handoff, imports, and checks are qualified against that one.
func destinationRelease(release string) error {
	if version == developmentVersion || release == version {
		return nil
	}
	return fmt.Errorf("this api-migration binary migrates to release %s, but the destination is release %q; "+
		"use the api-migration binary of release %q", version, release, release)
}

const usage = `usage: api-migration <command> [flags]

commands:
  plan      inspect the installation, take Raft snapshots, and write DIR/plan.json (requires --config)
  migrate   hand the installation over to kubebao.org and verify it; operations stay held
  commit    trust only the destination, delete the source Clusters, and release the hold
  rollback  hand the installation back to openbao.org (before commit deletes the source)
  status    print the phase and the Clusters observed in both groups
  convert   rewrite OpenBao resources in manifest files to kubebao.org (convert [--write] FILE...)
  version   print the release this binary belongs to
  help      print help, or use help COMMAND for command flags

Plan and status print text by default; use --output json for a public summary.
The private plan.json can contain sensitive Helm values. Keep DIR private.

Exit status 3 means --stop-after stopped the command. Exit status 4 means a
manifest installation must apply an operator state first: the message names
the file that describes it.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	program, err := os.Executable()
	if err != nil {
		program = os.Args[0]
	}
	err = runWithProgram(ctx, os.Args[1:], os.Stdout, os.Stderr, program)
	var checkpoint *checkpointError
	switch {
	case errors.As(err, &checkpoint):
		// A manifest installation waits for the administrator to apply a state.
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	case errors.Is(err, errStop):
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	case err != nil:
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runWithProgram(ctx, args, stdout, stderr, "api-migration")
}

func runWithProgram(ctx context.Context, args []string, stdout, stderr io.Writer, program string) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("a command is required")
	}
	command := args[0]
	if handled, err := commandHelp(args, stdout); handled {
		return err
	}
	switch command {
	case "convert":
		return runConvert(args[1:], stdout, stderr)
	case "version":
		if len(args) != 1 {
			return errors.New("version accepts no arguments; run api-migration help version")
		}
		_, err := fmt.Fprintln(stdout, version)
		return err
	case commandPlan, commandMigrate, commandCommit, commandRollback, commandStatus:
	default:
		return fmt.Errorf("unknown command %q; run api-migration help", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateDir := flags.String("state", "", "private state directory for this migration")
	configPath := flags.String("config", "", "JSON configuration (plan only)")
	timeout := flags.Duration("timeout", 10*time.Minute, "maximum time to wait for one condition")
	stopAfter := flags.String("stop-after", "", "exit after the named step completes (interruption testing)")
	output := flags.String("output", "text", "summary format: text or json (plan and status only)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; run api-migration help %s", flags.Arg(0), command)
	}
	if *stateDir == "" {
		return fmt.Errorf("%s requires --state DIR; run api-migration help %s", command, command)
	}
	if err := validateOutput(command, *output); err != nil {
		return err
	}
	if (command == commandPlan) != (*configPath != "") {
		return errors.New("--config is required for plan and not accepted otherwise")
	}

	plan := &Plan{Version: planVersion, CreatedAt: time.Now().UTC()}
	var err error
	if command == commandPlan {
		if plan.Config, err = loadConfig(*configPath); err != nil {
			return err
		}
		if version != developmentVersion {
			plan.DestinationVersion = version
		}
	}
	state, err := openState(*stateDir, command == commandPlan)
	if err != nil {
		return err
	}
	defer state.Close()
	if command != commandPlan {
		if plan, err = state.Load(); err != nil {
			return err
		}
		if err := destinationRelease(plan.DestinationVersion); err != nil {
			return fmt.Errorf("saved plan: %w", err)
		}
	}
	kube, err := newKube(plan.Config.Kubeconfig, plan.Config.Context, plan.Config.migratorPrincipal(), *timeout)
	if err != nil {
		return err
	}
	procedure := &Procedure{state: state, plan: plan, kube: kube, stopAfter: *stopAfter, log: stderr, program: program,
		bao: map[string]*OpenBao{},
		helm: Helm{Kubeconfig: plan.Config.Kubeconfig, Context: plan.Config.Context,
			Namespace: plan.Config.helmNamespace(), TargetNamespace: plan.Config.OperatorNamespace, Release: plan.Config.Release}}
	defer procedure.Close()
	if command != commandPlan {
		if err := procedure.checkScope(ctx); err != nil {
			return err
		}
	}
	switch command {
	case commandPlan:
		if err := procedure.Plan(ctx); err != nil {
			return fmt.Errorf("%w; planning artifacts are preserved in %s. After resolving the failure, plan with a new, empty --state directory", err, state.dir)
		}
		return procedure.Summary(ctx, stdout, *output)
	case commandMigrate:
		err = procedure.Migrate(ctx)
	case commandCommit:
		err = procedure.Commit(ctx)
	case commandRollback:
		err = procedure.Rollback(ctx)
	case commandStatus:
		return procedure.Status(ctx, stdout, *output)
	}
	if err == nil {
		return procedure.Summary(ctx, stdout, "text")
	}
	// Do not start more Kubernetes reads after an interruption or a failed write.
	_ = procedure.recordedSummary(stderr)
	return err
}

func validateOutput(command, output string) error {
	if output != "text" && output != "json" {
		return errors.New("--output must be text or json")
	}
	if command != commandPlan && command != commandStatus && output != "text" {
		return errors.New("--output json is accepted only by plan and status")
	}
	return nil
}

// checkScope refuses to act on a different Kubernetes cluster than the plan's.
func (p *Procedure) checkScope(ctx context.Context) error {
	uid, err := p.kube.namespaceUID(ctx, "kube-system")
	if err != nil {
		return err
	}
	if uid != p.plan.KubeSystemUID {
		return errors.New("the kube context points to a different Kubernetes cluster than the plan")
	}
	return nil
}
