package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRestoreImageAvailability(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		readyAfter, attempts int
		wantSuccess          bool
	}{
		{name: "already-published", readyAfter: 1, attempts: 1, wantSuccess: true},
		{name: "publication-delay", readyAfter: 3, attempts: 3, wantSuccess: true},
		{name: "unavailable", readyAfter: 99, attempts: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			counter := filepath.Join(dir, "attempts")
			if err := os.WriteFile(counter, []byte("0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mock := `#!/bin/sh
test "$1" = pull && test "$2" = openbao/openbao:2.7.2 || exit 42
read -r attempts < "$IMAGE_WAIT_COUNTER"
attempts=$((attempts + 1))
printf '%s\n' "$attempts" > "$IMAGE_WAIT_COUNTER"
test "$attempts" -ge "$IMAGE_READY_AFTER"
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(mock), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", filepath.Join("..", "..", "..", "hack", "ci", "wait-openbao-image.sh"),
				"openbao/openbao:2.7.2", "3", "0")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"IMAGE_WAIT_COUNTER="+counter, "IMAGE_READY_AFTER="+strconv.Itoa(tc.readyAfter))
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("success=%t, want %t: %v\n%s", err == nil, tc.wantSuccess, err, output)
			}
			data, err := os.ReadFile(counter)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(data)) != strconv.Itoa(tc.attempts) {
				t.Fatalf("attempts=%q, want %d", data, tc.attempts)
			}
			if !tc.wantSuccess && !strings.Contains(string(output), "tests have not started") {
				t.Fatalf("image unavailability must be identified as preparation failure:\n%s", output)
			}
		})
	}
}
