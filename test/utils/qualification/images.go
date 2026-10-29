package qualification

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	imageMapFileEnv = "E2E_QUALIFICATION_IMAGE_MAP_FILE"
	imageMapHashEnv = "E2E_QUALIFICATION_IMAGE_MAP_SHA256"
)

// ExternalImage resolves a declared test image through the frozen qualification input map.
// Normal tests retain their original reference when the map is not selected.
func ExternalImage(reference string) (string, error) {
	path, expected := os.Getenv(imageMapFileEnv), os.Getenv(imageMapHashEnv)
	if path == "" && expected == "" {
		return reference, nil
	}
	if path == "" || expected == "" {
		return "", fmt.Errorf("qualification image map and SHA256 are required together")
	}
	data, err := readBounded(path)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != expected {
		return "", fmt.Errorf("qualification image map SHA256 differs")
	}
	var images map[string]string
	if err := decode(data, &images); err != nil {
		return "", err
	}
	if len(images) == 0 {
		return "", fmt.Errorf("qualification image map is empty")
	}
	pinned := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for declared, resolved := range images {
		coordinates, selectedDigest, separated := strings.Cut(resolved, "@sha256:")
		if !separated || coordinates == "" || !pinned.MatchString(selectedDigest) ||
			(resolved != declared && coordinates != declared) {
			return "", fmt.Errorf("qualification image map must retain declared coordinates and add an immutable digest")
		}
	}
	if resolved, exists := images[reference]; exists {
		return resolved, nil
	}
	for _, resolved := range images {
		if reference == resolved {
			return reference, nil
		}
	}
	return "", fmt.Errorf("test image is absent from the frozen qualification input map")
}

// MustExternalImage rejects an incomplete qualification fixture before a workload starts.
func MustExternalImage(reference string) string {
	image, err := ExternalImage(reference)
	if err != nil {
		panic(fmt.Sprintf("qualification external image: %v", err))
	}
	return image
}
