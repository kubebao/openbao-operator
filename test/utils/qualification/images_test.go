package qualification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func selectImageMap(t *testing.T, images map[string]string) {
	t.Helper()
	data, err := json.Marshal(images)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "images.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	t.Setenv(imageMapFileEnv, path)
	t.Setenv(imageMapHashEnv, hex.EncodeToString(hash[:]))
}

func TestExternalImagePreservesNormalReferences(t *testing.T) {
	t.Setenv(imageMapFileEnv, "")
	t.Setenv(imageMapHashEnv, "")
	for _, image := range []string{"openbao/openbao:2.6.5", "ghcr.io/openbao/openbao:2.6.5", "fixture:local"} {
		resolved, err := ExternalImage(image)
		if err != nil || resolved != image {
			t.Fatalf("ordinary test image changed: %q, %v", resolved, err)
		}
	}
}

func TestExternalImageRequiresFrozenCoordinatesAndRejectsNewReferences(t *testing.T) {
	declared := "openbao/openbao:2.6.5"
	pinned := declared + "@sha256:" + strings.Repeat("a", 64)
	selectImageMap(t, map[string]string{declared: pinned})
	for _, reference := range []string{declared, pinned} {
		resolved, err := ExternalImage(reference)
		if err != nil || resolved != pinned {
			t.Fatalf("frozen reference did not resolve: %q, %v", resolved, err)
		}
	}
	if _, err := ExternalImage("openbao/openbao:2.7.2"); err == nil {
		t.Fatal("new image bypassed the frozen input map")
	}
}

func TestExternalImageRejectsIncompleteChangedOrSubstitutedMaps(t *testing.T) {
	declared := "openbao/openbao:2.6.5"
	for _, resolved := range []string{declared, "other/image:2.6.5@sha256:" + strings.Repeat("a", 64)} {
		selectImageMap(t, map[string]string{declared: resolved})
		if _, err := ExternalImage(declared); err == nil {
			t.Fatal("mutable or substituted coordinates were accepted")
		}
	}
	selectImageMap(t, map[string]string{declared: declared + "@sha256:" + strings.Repeat("a", 64)})
	t.Setenv(imageMapHashEnv, strings.Repeat("b", 64))
	if _, err := ExternalImage(declared); err == nil {
		t.Fatal("changed map was accepted")
	}
	t.Setenv(imageMapHashEnv, "")
	if _, err := ExternalImage(declared); err == nil {
		t.Fatal("incomplete map selection was accepted")
	}
}
