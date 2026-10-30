package helpers

import (
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCandidateHelperTrust(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	if err := ApplyCandidateHelperTrust(cluster, ""); err != nil || cluster.Spec.OperatorImageVerification != nil {
		t.Fatal("ordinary fixtures must keep production trust defaults")
	}
	for _, pr := range []string{"0", "-7", "7/merge", "7.*", "7\n", "other/repo"} {
		if err := ApplyCandidateHelperTrust(cluster, pr); err == nil {
			t.Fatalf("accepted candidate PR %q", pr)
		}
	}
	if err := ApplyCandidateHelperTrust(cluster, "7"); err != nil {
		t.Fatal(err)
	}
	policy := cluster.Spec.OperatorImageVerification
	if !policy.Enabled || policy.FailurePolicy != "Block" || policy.IgnoreTlog || policy.PublicKey != "" ||
		policy.Issuer != "https://token.actions.githubusercontent.com" || policy.SubjectRegExp != "" ||
		policy.Subject != "https://github.com/kubebao/openbao-operator/.github/workflows/reusable-build.yml@refs/pull/7/merge" {
		t.Fatalf("unexpected candidate trust: %+v", policy)
	}
	if err := ApplyCandidateHelperTrust(cluster, "8"); err == nil || cluster.Spec.OperatorImageVerification != policy {
		t.Fatal("must not replace an existing policy")
	}
	if err := ApplyCandidateHelperTrust(&openbaov1alpha1.OpenBaoCluster{}, "7"); err == nil {
		t.Fatal("must not change Development fixture verification")
	}
}
