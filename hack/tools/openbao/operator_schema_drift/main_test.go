package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindUpstreamConfigDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
	}{
		{name: "2.7 internal layout", dir: filepath.Join("internal", "helper", "configutil")},
		{name: "current layout", dir: filepath.Join("helper", "configutil")},
		{name: "legacy layout", dir: filepath.Join("internalshared", "configutil")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			want := filepath.Join(root, tt.dir)
			if err := os.MkdirAll(want, 0o750); err != nil {
				t.Fatalf("create config directory: %v", err)
			}

			got, err := findUpstreamConfigDir(root)
			if err != nil {
				t.Fatalf("find config directory: %v", err)
			}
			if got != want {
				t.Fatalf("config directory = %q, want %q", got, want)
			}
		})
	}
}

func TestFindUpstreamConfigDirPrefersCurrentLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	current := filepath.Join(root, "helper", "configutil")
	legacy := filepath.Join(root, "internalshared", "configutil")
	for _, dir := range []string{current, legacy} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("create config directory: %v", err)
		}
	}

	got, err := findUpstreamConfigDir(root)
	if err != nil {
		t.Fatalf("find config directory: %v", err)
	}
	if got != current {
		t.Fatalf("config directory = %q, want current layout %q", got, current)
	}
}

func TestFindUpstreamConfigDirRejectsUnknownLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	_, err := findUpstreamConfigDir(root)
	if err == nil {
		t.Fatal("expected missing config directory error")
	}
	if !strings.Contains(err.Error(), "helper/configutil") ||
		!strings.Contains(err.Error(), "internalshared/configutil") {
		t.Fatalf("error %q does not name the supported layouts", err)
	}
}

func TestFindUpstreamServerDir(t *testing.T) {
	for _, dir := range []string{"command/server", "internal/command/server"} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			want := filepath.Join(root, dir)
			if err := os.MkdirAll(want, 0o750); err != nil {
				t.Fatal(err)
			}
			got, err := findUpstreamServerDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
