//go:build e2e

package e2e

import (
	"fmt"

	"github.com/kubebao/openbao-operator/test/utils/qualification"
)

func qualifiedExternalImage(reference string) string {
	image, err := qualification.ExternalImage(reference)
	if err != nil {
		panic(fmt.Sprintf("qualification test image input: %v", err))
	}
	return image
}

func openBaoVersionImage(version string) string {
	return qualifiedExternalImage("openbao/openbao:" + version)
}
