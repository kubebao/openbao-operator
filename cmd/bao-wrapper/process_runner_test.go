package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	wrapperHelperEnv = "BAO_WRAPPER_PROCESS_TEST"
	ignoreHelperMode = "ignore"
)

func TestWrapperForwardsShutdownSignals(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			ready, marker := filepath.Join(dir, "ready"), filepath.Join(dir, "cleanup")
			cmd, done, output := startWrapperHelper(t, "child", ready, marker)
			waitForChildReady(t, ready)
			require.NoError(t, cmd.Process.Signal(sig))
			select {
			case err := <-done:
				require.NoError(t, err, "wrapper output: %s", output)
			case <-time.After(5 * time.Second):
				t.Fatal("wrapper did not wait for graceful child termination")
			}
			cleanup, err := os.ReadFile(marker)
			require.NoError(t, err, "child must finish its signal handler")
			require.Equal(t, sig.String(), string(cleanup))
		})
	}
}

func TestWrapperPreservesChildExitCode(t *testing.T) {
	for _, test := range []struct {
		mode string
		code int
	}{
		{mode: "exit", code: 42},
		{mode: "killed", code: 137},
	} {
		t.Run(test.mode, func(t *testing.T) {
			_, done, output := startWrapperHelper(t, test.mode)
			select {
			case err := <-done:
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, "wrapper output: %s", output)
				require.Equal(t, test.code, exitErr.ExitCode())
			case <-time.After(5 * time.Second):
				t.Fatal("wrapper did not return the child's failure")
			}
		})
	}
}

func TestRunChildCancellation(t *testing.T) {
	for _, mode := range []string{"child", ignoreHelperMode} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			ready, marker := filepath.Join(dir, "ready"), filepath.Join(dir, "cleanup")
			cmd := exec.Command(os.Args[0], "-test.run=^TestWrapperHelperProcess$", "--", mode, ready, marker)
			cmd.Env = append(os.Environ(), wrapperHelperEnv+"=1", "GORACE=atexit_sleep_ms=0")
			done := make(chan error, 1)
			const gracePeriod = 500 * time.Millisecond
			// Enable the watcher to exercise its cancellation and join on shutdown.
			go func() { done <- runChild(ctx, cmd, ready, time.Second, gracePeriod) }()
			t.Cleanup(func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			})
			waitForChildReady(t, ready)
			started := time.Now()
			cancel()
			select {
			case err := <-done:
				if mode == ignoreHelperMode {
					require.Error(t, err)
					require.Equal(t, 137, processExitCode(err))
					require.GreaterOrEqual(t, time.Since(started), gracePeriod)
					_, err = os.Stat(marker)
					require.ErrorIs(t, err, os.ErrNotExist)
				} else {
					require.NoError(t, err)
					cleanup, readErr := os.ReadFile(marker)
					require.NoError(t, readErr)
					require.Equal(t, syscall.SIGTERM.String(), string(cleanup))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("context cancellation did not terminate the child")
			}
		})
	}
}

func TestRunChildDoesNotStartWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("/does-not-exist")
	err := runChild(ctx, cmd, "", time.Second, shutdownGracePeriod)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, cmd.Process)
}

func startWrapperHelper(t *testing.T, childArgs ...string) (*exec.Cmd, <-chan error, *bytes.Buffer) {
	t.Helper()
	args := make([]string, 0, 7+len(childArgs))
	args = append(args, "-test.run=^TestWrapperHelperProcess$", "--", "wrapper", "--", os.Args[0],
		"-test.run=^TestWrapperHelperProcess$", "--")
	args = append(args, childArgs...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), wrapperHelperEnv+"=1", "GORACE=atexit_sleep_ms=0")
	output := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = output, output
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("wrapper helper did not exit after test cleanup")
		}
	})
	return cmd, done, output
}

func waitForChildReady(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "child did not install its signal handler")
}

// TestWrapperHelperProcess runs the production main and an actual child in
// separate processes. Its signal handler delays exit to expose premature kills.
func TestWrapperHelperProcess(t *testing.T) {
	if os.Getenv(wrapperHelperEnv) != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	switch args[0] {
	case "wrapper":
		flag.CommandLine = flag.NewFlagSet("bao-wrapper", flag.ExitOnError)
		os.Args = append([]string{"bao-wrapper"}, args[1:]...)
		main()
		os.Exit(0)
	case "exit":
		os.Exit(42)
	case "killed":
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
	case "child", ignoreHelperMode:
		signals := make(chan os.Signal, 1)
		if args[0] == ignoreHelperMode {
			signal.Ignore(syscall.SIGTERM, os.Interrupt)
		} else {
			signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
		}
		require.NoError(t, os.WriteFile(args[1], []byte("ready"), 0o600))
		if args[0] == ignoreHelperMode {
			<-time.After(time.Hour)
			os.Exit(2)
		}
		sig := <-signals
		<-time.After(50 * time.Millisecond)
		require.NoError(t, os.WriteFile(args[2], []byte(sig.String()), 0o600))
		os.Exit(0)
	}
}
