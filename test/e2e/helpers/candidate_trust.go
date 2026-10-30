package helpers

import (
	"fmt"
	"regexp"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

var candidatePRNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

// ApplyCandidateHelperTrust opts a Hardened test fixture into one PR's signed
// helper images. An empty PR leaves normal release trust unchanged.
func ApplyCandidateHelperTrust(cluster *openbaov1alpha1.OpenBaoCluster, pr string) error {
	if pr == "" {
		return nil
	}
	if !candidatePRNumber.MatchString(pr) {
		return fmt.Errorf("candidate helper trust requires a positive PR number")
	}
	if cluster == nil || cluster.Spec.Profile != openbaov1alpha1.ProfileHardened {
		return fmt.Errorf("candidate helper trust requires a Hardened fixture")
	}
	if cluster.Spec.OperatorImageVerification != nil {
		return fmt.Errorf("candidate helper trust must not replace an existing verification policy")
	}
	cluster.Spec.OperatorImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled:       true,
		FailurePolicy: "Block",
		Issuer:        "https://token.actions.githubusercontent.com",
		Subject: "https://github.com/kubebao/openbao-operator/.github/workflows/reusable-build.yml@refs/pull/" +
			pr + "/merge",
	}
	return nil
}
