package main

import (
	"fmt"
	"io"
	"slices"
)

// commandHelp handles help before loading configuration, state, or Kubernetes clients.
func commandHelp(args []string, out io.Writer) (bool, error) {
	command := args[0]
	requested := command == commandHelpName || command == "--help" || command == "-h"
	if command == commandHelpName && len(args) > 1 {
		command = args[1]
	} else if requested {
		command = ""
	}
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		requested = requested || arg == "--help" || arg == "-h"
	}
	requested = requested || len(args) == 2 && args[1] == commandHelpName
	if !requested {
		return false, nil
	}
	if command == "" {
		_, err := fmt.Fprint(out, usage)
		return true, err
	}
	if !slices.Contains([]string{commandPlan, commandMigrate, commandCommit, commandRollback, commandStatus, "convert", "version"}, command) {
		return true, fmt.Errorf("unknown command %q; run api-migration help", command)
	}
	if command == "convert" {
		_, err := fmt.Fprintln(out, "usage: api-migration convert [--write] FILE...\n\nList converted resource counts. --write rewrites each file in place.")
		return true, err
	}
	if command == "version" {
		_, err := fmt.Fprintln(out, "usage: api-migration version\n\nPrint the release this binary belongs to.")
		return true, err
	}
	_, err := fmt.Fprintf(out, "usage: api-migration %s --state DIR [flags]\n\n", command)
	if err != nil {
		return true, err
	}
	_, _ = fmt.Fprintln(out, "  --state DIR          private migration state directory (required)\n  --timeout DURATION   maximum wait for one condition (default 10m)")
	if command == commandPlan {
		_, _ = fmt.Fprintln(out, "  --config FILE        JSON configuration (required); DIR must be new and empty")
	}
	if command == commandPlan || command == commandStatus {
		_, _ = fmt.Fprintln(out, "  --output text|json   public summary format (default text)")
	} else {
		_, _ = fmt.Fprintln(out, "  --stop-after STEP    exit with status 3 after STEP; resume with the same --state")
	}
	_, err = fmt.Fprintln(out, "  --help, -h           print this help\n\nKeep the state directory and its plan.json private. Resume interrupted commands with the same --state.")
	return true, err
}
