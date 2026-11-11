package controller

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/internal/platform/entrypoint"
)

func TestParseRunConfig(t *testing.T) {
	t.Setenv("OPERATOR_PLATFORM", "")
	t.Setenv("OPENBAO_JWT_AUTH_STRATEGY", "")
	originalArgs := append([]string(nil), os.Args...)
	originalFlags := flag.CommandLine
	for _, tc := range []struct {
		name      string
		args      []string
		wantError string
	}{
		{name: "unknown flag", args: []string{"--unknown"}, wantError: "flag provided but not defined"},
		{name: "invalid boolean", args: []string{"--leader-elect=perhaps"}, wantError: "invalid boolean value"},
		{name: "invalid duration", args: []string{"--admission-startup-timeout=later"}, wantError: "invalid value"},
		{name: "invalid admission mode", args: []string{"--admission-enforcement=off"}, wantError: "invalid admission"},
		{name: "invalid platform", args: []string{"--platform=opneshift"}, wantError: "invalid target platform"},
		{name: "positional argument", args: []string{"cluster"}, wantError: "unexpected positional argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRunConfig(tc.args, io.Discard)
			require.ErrorContains(t, err, tc.wantError)
			var usageError *entrypoint.UsageError
			require.ErrorAs(t, Run(context.Background(), tc.args), &usageError)
		})
	}

	for range 2 {
		cfg, err := parseRunConfig(
			[]string{"--leader-elect", "--platform= OpenShift ", "--kubeconfig=/test/config"}, io.Discard,
		)
		require.NoError(t, err)
		require.True(t, cfg.enableLeaderElection)
		require.Equal(t, "openshift", cfg.platform)
		require.Equal(t, "/test/config", cfg.kubeconfig)
		defaults, err := parseRunConfig(nil, io.Discard)
		require.NoError(t, err)
		require.False(t, defaults.enableLeaderElection)
		require.Equal(t, "auto", defaults.platform)
		require.Empty(t, defaults.kubeconfig)
	}
	require.Equal(t, originalArgs, os.Args)
	require.Same(t, originalFlags, flag.CommandLine)
	require.Nil(t, flag.CommandLine.Lookup("platform"))
}

func TestParseRunConfigHelp(t *testing.T) {
	var output bytes.Buffer
	_, err := parseRunConfig([]string{"--help"}, &output)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "Usage of controller:")
	require.Contains(t, output.String(), "-kubeconfig")
}

func TestParseRunConfigPlatformEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, configured, environment, want string
		invalid                             bool
	}{
		{name: "default", want: "auto"},
		{name: "flag", configured: " OpenShift ", want: "openshift"},
		{name: "environment wins", configured: "kubernetes", environment: " OpenShift ", want: "openshift"},
		{name: "empty environment", configured: "kubernetes", environment: " ", want: "kubernetes"},
		{name: "invalid environment", configured: "kubernetes", environment: "opneshift", invalid: true},
		{name: "overridden flag", configured: "typo", environment: "kubernetes", want: "kubernetes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPERATOR_PLATFORM", tc.environment)
			cfg, err := parseRunConfig([]string{"--platform=" + tc.configured}, io.Discard)
			if tc.invalid {
				require.ErrorContains(t, err, "invalid target platform")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.platform)
		})
	}
}

func TestParseRunConfigAPIMigrationHold(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "default disabled"},
		{name: "enabled", args: []string{"--api-migration-hold"}, want: true},
		{name: "explicitly disabled", args: []string{"--api-migration-hold=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseRunConfig(tc.args, io.Discard)
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.apiMigrationHold)
		})
	}
	_, err := parseRunConfig([]string{"--api-migration-hold=perhaps"}, io.Discard)
	require.ErrorContains(t, err, "invalid boolean value")
}
