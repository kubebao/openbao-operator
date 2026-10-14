package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

const (
	// Exit codes
	exitSuccess           = 0
	exitConfigError       = 1
	exitAuthError         = 2
	exitLeaderDiscovery   = 3
	exitSnapshotError     = 4
	exitStorageError      = 5
	exitVerificationError = 6
)

func main() {
	ctx := context.Background()

	// Check executor mode
	mode := os.Getenv("EXECUTOR_MODE")
	var err error

	switch mode {
	case "retention":
		err = runRetention(ctx)
	case "restore":
		err = runRestore(ctx)
	case "backup", "":
		// Default to backup mode for backward compatibility
		err = run(ctx)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown EXECUTOR_MODE: %s (expected 'backup', 'restore', or 'retention')\n", mode)
		os.Exit(exitConfigError)
	}

	if err != nil {
		prefix := "bao-backup"
		if mode == "restore" {
			prefix = "bao-restore"
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s error: %v\n", prefix, err)
		os.Exit(exitCodeForError(err))
	}
	os.Exit(exitSuccess)
}

func exitCodeForError(err error) int {
	if err == nil {
		return exitSuccess
	}

	switch {
	case errors.Is(err, errConfigCategory):
		return exitConfigError
	case errors.Is(err, errAuthCategory):
		return exitAuthError
	case errors.Is(err, errLeaderCategory):
		return exitLeaderDiscovery
	case errors.Is(err, errSnapshotCategory):
		return exitSnapshotError
	case errors.Is(err, errStorageCategory):
		return exitStorageError
	case errors.Is(err, errVerificationCategory):
		return exitVerificationError
	default:
		return exitConfigError
	}
}
