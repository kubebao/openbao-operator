package blobstore

import "testing"

func TestEffectiveS3Region(t *testing.T) {
	for _, tc := range []struct{ name, target, secret, want string }{
		{"secret override", "us-east-1", " eu-west-1\n", "eu-west-1"},
		{"target fallback", "eu-central-1", "", "eu-central-1"},
		{"blank secret", "eu-central-1", " \n", "eu-central-1"},
		{"default", "", "", "us-east-1"},
		{"blank target", " \n", "", "us-east-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveS3Region(tc.target, tc.secret); got != tc.want {
				t.Fatalf("EffectiveS3Region() = %q, want %q", got, tc.want)
			}
		})
	}
}
