package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"syscall"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Printf("Error: %v", err)
		os.Exit(processExitCode(err))
	}
}

func processExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exitErr.ExitCode()
	}
	return 1
}
