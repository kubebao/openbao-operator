package bluegreen

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	"github.com/dc-tec/openbao-operator/internal/service/upgrade"
)

const imageVerificationFailurePolicyWarn = "Warn"

type failurePolicyTestCase struct {
	name     string
	cluster  *openbaov1alpha1.OpenBaoCluster
	expected string
}

func buildFailurePolicyCases(
	assign func(*openbaov1alpha1.OpenBaoClusterSpec, *openbaov1alpha1.ImageVerificationConfig),
) []failurePolicyTestCase {
	return []failurePolicyTestCase{
		{
			name:     "defaults to block when config is nil",
			cluster:  &openbaov1alpha1.OpenBaoCluster{},
			expected: constants.ImageVerificationFailurePolicyBlock,
		},
		{
			name: "defaults to block when policy is empty",
			cluster: func() *openbaov1alpha1.OpenBaoCluster {
				cluster := &openbaov1alpha1.OpenBaoCluster{}
				assign(&cluster.Spec, &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       true,
					FailurePolicy: "",
				})
				return cluster
			}(),
			expected: constants.ImageVerificationFailurePolicyBlock,
		},
		{
			name: "returns configured policy",
			cluster: func() *openbaov1alpha1.OpenBaoCluster {
				cluster := &openbaov1alpha1.OpenBaoCluster{}
				assign(&cluster.Spec, &openbaov1alpha1.ImageVerificationConfig{
					Enabled:       true,
					FailurePolicy: imageVerificationFailurePolicyWarn,
				})
				return cluster
			}(),
			expected: imageVerificationFailurePolicyWarn,
		},
	}
}

func runFailurePolicyTests(
	t *testing.T,
	name string,
	policy func(*openbaov1alpha1.OpenBaoCluster) string,
	tests []failurePolicyTestCase,
) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := policy(tt.cluster); got != tt.expected {
				t.Fatalf("%s() = %q, want %q", name, got, tt.expected)
			}
		})
	}
}

func TestImageVerificationFailurePolicy_TableDriven(t *testing.T) {
	t.Parallel()

	runFailurePolicyTests(t, "imageVerificationFailurePolicy", imageVerificationFailurePolicy, buildFailurePolicyCases(
		func(spec *openbaov1alpha1.OpenBaoClusterSpec, cfg *openbaov1alpha1.ImageVerificationConfig) {
			spec.ImageVerification = cfg
		},
	))
}

func TestOperatorImageVerificationFailurePolicy_TableDriven(t *testing.T) {
	t.Parallel()

	runFailurePolicyTests(t, "OperatorImageVerificationFailurePolicy", upgrade.OperatorImageVerificationFailurePolicy, buildFailurePolicyCases(
		func(spec *openbaov1alpha1.OpenBaoClusterSpec, cfg *openbaov1alpha1.ImageVerificationConfig) {
			spec.OperatorImageVerification = cfg
		},
	))
}

func TestResolveInitContainerImage_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		cluster  *openbaov1alpha1.OpenBaoCluster
		version  string
		expected string
	}{
		{
			name:     "returns default operator-managed image when init container config is nil",
			cluster:  &openbaov1alpha1.OpenBaoCluster{},
			version:  "1.2.3",
			expected: "ghcr.io/kubebao/openbao-init:1.2.3",
		},
		{
			name: "returns configured image",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Image: "ghcr.io/kubebao/openbao-init:edge",
					},
				},
			},
			expected: "ghcr.io/kubebao/openbao-init:edge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.version != "" {
				t.Setenv(constants.EnvOperatorVersion, tt.version)
			}
			got, err := resolveInitContainerImage(tt.cluster)
			if err != nil {
				t.Fatalf("resolveInitContainerImage() error = %v", err)
			}
			if got != tt.expected {
				t.Fatalf("resolveInitContainerImage() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestVerifyImageDigest_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		cluster        *openbaov1alpha1.OpenBaoCluster
		imageRef       string
		wantDigest     string
		wantErrSubstr  string
		expectNilError bool
	}{
		{
			name: "skips when verification is disabled",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile: openbaov1alpha1.ProfileDevelopment,
				},
			},
			imageRef:       "ghcr.io/openbao/openbao:2.5.0",
			wantDigest:     "",
			expectNilError: true,
		},
		{
			name: "skips when image reference is empty",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					ImageVerification: &openbaov1alpha1.ImageVerificationConfig{
						Enabled:       true,
						FailurePolicy: constants.ImageVerificationFailurePolicyBlock,
					},
				},
			},
			imageRef:       "",
			wantDigest:     "",
			expectNilError: true,
		},
		{
			name: "warn policy continues on verifier failure",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					ImageVerification: &openbaov1alpha1.ImageVerificationConfig{
						Enabled:       true,
						FailurePolicy: imageVerificationFailurePolicyWarn,
					},
				},
			},
			imageRef:       "ghcr.io/openbao/openbao:2.5.0",
			wantDigest:     "",
			expectNilError: true,
		},
		{
			name: "block policy fails on verifier failure",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					ImageVerification: &openbaov1alpha1.ImageVerificationConfig{
						Enabled:       true,
						FailurePolicy: constants.ImageVerificationFailurePolicyBlock,
					},
				},
			},
			imageRef:      "ghcr.io/openbao/openbao:2.5.0",
			wantDigest:    "",
			wantErrSubstr: "verify main image",
		},
	}

	mgr := &Manager{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotDigest, err := mgr.verifyImageDigest(
				context.Background(),
				logr.Discard(),
				tt.cluster,
				tt.imageRef,
				constants.ReasonImageVerificationFailed,
				"verify main image",
			)
			if gotDigest != tt.wantDigest {
				t.Fatalf("verifyImageDigest() digest = %q, want %q", gotDigest, tt.wantDigest)
			}

			if tt.expectNilError {
				if err != nil {
					t.Fatalf("verifyImageDigest() unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("verifyImageDigest() error = nil, want substring %q", tt.wantErrSubstr)
			}
			if tt.wantErrSubstr != "" && !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("verifyImageDigest() error = %q, want substring %q", err.Error(), tt.wantErrSubstr)
			}
		})
	}
}

func TestVerifyOperatorImageDigest_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		cluster        *openbaov1alpha1.OpenBaoCluster
		imageRef       string
		wantDigest     string
		wantErrSubstr  string
		expectNilError bool
	}{
		{
			name: "skips when operator verification is disabled",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile: openbaov1alpha1.ProfileDevelopment,
				},
			},
			imageRef:       "ghcr.io/kubebao/openbao-init:edge",
			wantDigest:     "",
			expectNilError: true,
		},
		{
			name: "warn policy continues on verifier failure",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					OperatorImageVerification: &openbaov1alpha1.ImageVerificationConfig{
						Enabled:       true,
						FailurePolicy: imageVerificationFailurePolicyWarn,
					},
				},
			},
			imageRef:       "ghcr.io/kubebao/openbao-init:edge",
			wantDigest:     "",
			expectNilError: true,
		},
		{
			name: "block policy fails on verifier failure",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					OperatorImageVerification: &openbaov1alpha1.ImageVerificationConfig{
						Enabled:       true,
						FailurePolicy: constants.ImageVerificationFailurePolicyBlock,
					},
				},
			},
			imageRef:      "ghcr.io/kubebao/openbao-init:edge",
			wantDigest:    "",
			wantErrSubstr: "verify operator image",
		},
	}

	mgr := &Manager{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotDigest, err := mgr.verifyOperatorImageDigest(
				context.Background(),
				logr.Discard(),
				tt.cluster,
				tt.imageRef,
				constants.ReasonInitContainerImageVerificationFailed,
				"verify operator image",
			)
			if gotDigest != tt.wantDigest {
				t.Fatalf("verifyOperatorImageDigest() digest = %q, want %q", gotDigest, tt.wantDigest)
			}

			if tt.expectNilError {
				if err != nil {
					t.Fatalf("verifyOperatorImageDigest() unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("verifyOperatorImageDigest() error = nil, want substring %q", tt.wantErrSubstr)
			}
			if tt.wantErrSubstr != "" && !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("verifyOperatorImageDigest() error = %q, want substring %q", err.Error(), tt.wantErrSubstr)
			}
		})
	}
}
