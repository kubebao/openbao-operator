package security

import (
	"context"
	"regexp"
	"testing"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/port/imageverify"
	"github.com/go-logr/logr"
)

type captureHelperVerifier struct {
	config imageverify.VerifyConfig
	called bool
}

func (v *captureHelperVerifier) Verify(_ context.Context, imageRef string, config imageverify.VerifyConfig) (string, error) {
	v.config = config
	v.called = true
	return imageRef, nil
}

func TestHelperVerificationDefaultsBindRepositoryToSigner(t *testing.T) {
	for _, owner := range []string{"kubebao", "dc-tec"} {
		for _, helper := range []string{"openbao-init", "openbao-backup", "openbao-upgrade"} {
			t.Run(owner+"/"+helper, func(t *testing.T) {
				cluster := &openbaov1alpha1.OpenBaoCluster{
					Spec: openbaov1alpha1.OpenBaoClusterSpec{Profile: openbaov1alpha1.ProfileHardened},
				}
				verifier := &captureHelperVerifier{}
				_, err := VerifyOperatorImageForCluster(context.Background(), logr.Discard(), verifier, cluster, "ghcr.io/"+owner+"/"+helper+":edge")
				if err != nil || !verifier.called {
					t.Fatalf("helper verification: called=%v, error=%v", verifier.called, err)
				}
				if verifier.config.IssuerRegExp != defaultGitHubOIDCIssuerRegExp {
					t.Fatalf("unexpected issuer: %q", verifier.config.IssuerRegExp)
				}
				subjects := regexp.MustCompile(verifier.config.SubjectRegExp)
				for _, signer := range []string{"kubebao", "dc-tec", "untrusted"} {
					for _, workflow := range []string{
						"release.yml@refs/tags/0.6.0",
						"publish-edge.yml@refs/heads/main",
						"publish-nightly.yml@refs/heads/main",
						"reusable-build.yml@refs/tags/0.5.1",
					} {
						subject := "https://github.com/" + signer + "/openbao-operator/.github/workflows/" + workflow
						if got, want := subjects.MatchString(subject), signer == owner; got != want {
							t.Errorf("subject %q: matched=%v, want=%v", subject, got, want)
						}
					}
				}
			})
		}
	}
}

func TestHelperVerificationDefaultsRejectUnrecognizedRepositories(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/untrusted/openbao-init:edge",
		"ghcr.io/kubebao/openbao-init-extra:edge",
		"ghcr.io/kubebao/openbao-operator:edge",
		"example.com/kubebao/openbao-init:edge",
	} {
		t.Run(image, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{Profile: openbaov1alpha1.ProfileHardened},
			}
			verifier := &captureHelperVerifier{}
			_, err := VerifyOperatorImageForCluster(context.Background(), logr.Discard(), verifier, cluster, image)
			if err == nil || verifier.called {
				t.Fatalf("unrecognized repository: called=%v, error=%v", verifier.called, err)
			}
		})
	}
}
