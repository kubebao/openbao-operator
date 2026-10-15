package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func run(ctx context.Context) error {
	applyUmaskFromEnv()
	if err := runPreflightChecks(); err != nil {
		return err
	}

	var (
		watchFile string
		interval  time.Duration
	)

	flag.StringVar(&watchFile, "watch-file", "", "Path to the file to watch for changes")
	flag.DurationVar(&interval, "interval", 10*time.Second, "Polling interval")
	flag.Parse()

	cmdArgs := flag.Args()
	if len(cmdArgs) == 0 {
		return fmt.Errorf("no command specified to run")
	}

	// #nosec G204 -- This wrapper intentionally executes user-provided commands.
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnvironment(os.Environ())

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start child process: %w", err)
	}

	forwardSignals(ctx, cmd)
	startFileWatcher(ctx, cmd, watchFile, interval)

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("child process exited with code %d: %w", exitErr.ExitCode(), err)
		}
		return fmt.Errorf("child process exited with error: %w", err)
	}

	return nil
}

func applyUmaskFromEnv() {
	raw := os.Getenv("UMASK")
	if raw == "" {
		return
	}

	mask, err := parseUmask(raw)
	if err != nil {
		log.Printf("Invalid UMASK %q (%v), leaving default umask unchanged", raw, err)
		return
	}

	syscall.Umask(mask)
}

func parseUmask(raw string) (int, error) {
	mask, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("expected octal value")
	}
	if mask > 0o777 {
		return 0, fmt.Errorf("must be between 0000 and 0777")
	}
	return int(mask), nil
}

func forwardSignals(ctx context.Context, cmd *exec.Cmd) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-sigChan:
				if err := cmd.Process.Signal(sig); err != nil {
					log.Printf("Failed to forward signal %v: %v", sig, err)
				}
			}
		}
	}()
}

func startFileWatcher(ctx context.Context, cmd *exec.Cmd, watchFile string, interval time.Duration) {
	if watchFile == "" {
		return
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		watchCancel()
	}()

	go watchFileForChanges(watchCtx, watchFile, interval, func() {
		log.Printf("File %s changed. Sending SIGHUP to child process...", watchFile)
		if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
			log.Printf("Failed to signal child process: %v", err)
		}
	})
}
