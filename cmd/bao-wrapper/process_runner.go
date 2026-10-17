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

// Managed Pods use Kubernetes' default 30-second termination grace period.
// Leave five seconds for the wrapper to reap the child before kubelet escalation.
const shutdownGracePeriod = 25 * time.Second

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
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnvironment(os.Environ())

	return runChild(ctx, cmd, watchFile, interval, shutdownGracePeriod)
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

func runChild(ctx context.Context, cmd *exec.Cmd, watchFile string, interval, gracePeriod time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start child process: %w", err)
	}
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start child process: %w", err)
	}

	stopWatcher := startFileWatcher(ctx, cmd, watchFile, interval)
	defer stopWatcher()
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	var timer *time.Timer
	var deadline <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	ctxDone := ctx.Done()
	shutdown := func(sig os.Signal) {
		stopWatcher()
		if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Printf("Failed to forward signal %v: %v", sig, err)
		}
		if timer == nil {
			timer = time.NewTimer(gracePeriod)
			deadline = timer.C
		}
		ctxDone = nil
	}
	for {
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("child process exited: %w", err)
			}
			return nil
		case sig := <-sigChan:
			shutdown(sig)
		case <-ctxDone:
			shutdown(syscall.SIGTERM)
		case <-deadline:
			deadline = nil
			log.Printf("Child process did not exit within %s; sending SIGKILL", gracePeriod)
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				log.Printf("Failed to kill child process: %v", err)
			}
		}
	}
}

func startFileWatcher(ctx context.Context, cmd *exec.Cmd, watchFile string, interval time.Duration) func() {
	if watchFile == "" {
		return func() {}
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchFileForChanges(watchCtx, watchFile, interval, func() {
			log.Printf("File %s changed. Sending SIGHUP to child process...", watchFile)
			if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
				log.Printf("Failed to signal child process: %v", err)
			}
		})
	}()
	return func() {
		watchCancel()
		<-done
	}
}
