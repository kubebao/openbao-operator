package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubebao/openbao-operator/hack/tools/openbao/internal/configschema"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	"github.com/stretchr/testify/require"
)

func TestCheckResultRejectsIgnoredSettingsAndWrongFailures(t *testing.T) {
	parsed := struct {
		Reload  time.Duration
		Plugins []string
	}{Reload: 10 * time.Second}
	tests := []struct {
		name        string
		fixture     preparedCase
		diagnostics []string
		wantError   string
	}{
		{name: "effective duration",
			fixture: preparedCase{Assertions: map[string]string{"Reload": "10s"}}},
		{name: "ignored plugin",
			fixture:   preparedCase{Assertions: map[string]string{"Plugins.#": "1"}},
			wantError: "got \"0\""},
		{name: "missing field",
			fixture:   preparedCase{Assertions: map[string]string{"Missing": "true"}},
			wantError: "field is missing"},
		{name: "validation diagnostic",
			diagnostics: []string{"unknown field tls_auto_reload"},
			wantError:   "validation failure"},
		{name: "expected rejection",
			fixture:     preparedCase{WantFailure: true, ErrorContains: []string{"tls_auto_reload"}},
			diagnostics: []string{"unknown field tls_auto_reload"}},
		{name: "negative accepted",
			fixture:   preparedCase{WantFailure: true, ErrorContains: []string{"tls_auto_reload"}},
			wantError: "passed without detecting"},
		{name: "unrelated rejection",
			fixture:     preparedCase{WantFailure: true, ErrorContains: []string{"tls_auto_reload"}},
			diagnostics: []string{"invalid TTL"},
			wantError:   "unrelated failure"},
		{name: "mixed rejection",
			fixture:     preparedCase{WantFailure: true, ErrorContains: []string{"tls_auto_reload"}},
			diagnostics: []string{"tls_auto_reload unknown", "invalid TTL"},
			wantError:   "unrelated failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkResult(tt.fixture, parsed, tt.diagnostics)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

func TestParsedValueHandlesPromotedFieldsMapsAndNil(t *testing.T) {
	type Embedded struct {
		Values   map[string]string
		Promoted bool
	}
	parsed := struct {
		Embedded
		Values map[string]string
		Names  []string
		Nil    *string
		hidden string
	}{Embedded: Embedded{Values: map[string]string{"non_voter": "false"}, Promoted: true},
		Values: map[string]string{"non_voter": "true"}, Names: []string{"first"}}
	for path, expected := range map[string]string{
		"Values.non_voter": "true", "Names.0": "first", "Names.#": "1", "Promoted": "true",
	} {
		got, err := parsedValue(parsed, path)
		require.NoError(t, err)
		require.Equal(t, expected, got)
	}
	for _, path := range []string{"Names.-1", "Names.2", "Nil.foo", "Values.absent", "Names.#.extra", "hidden"} {
		_, err := parsedValue(parsed, path)
		require.Error(t, err)
	}
}

func TestSerializedJSONAssertionsCompareHeaderValues(t *testing.T) {
	const expected = `{"Authorization":["Bearer token"],"X-Audit":["one","two"]}`
	for _, tt := range []struct {
		name, actual string
		matches      bool
	}{
		{"different key order", `{"X-Audit":["one","two"],"Authorization":["Bearer token"]}`, true},
		{"changed header", `{"Authorization":["Bearer other"],"X-Audit":["one","two"]}`, false},
		{"missing header", `{"Authorization":["Bearer token"]}`, false},
		{"invalid JSON", `{invalid}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.matches, sameAssertionValue(tt.actual, expected))
		})
	}
}

func TestFixtureInventoryCoversEveryDeclaredOperatorPath(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	m, err := loadManifest(root)
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(root, "internal/adapter/config/testdata/*.hcl"))
	require.NoError(t, err)
	require.Len(t, files, len(m.Fixtures))
	covered := configschema.Keys{}
	for _, file := range files {
		_, ok := m.Fixtures[filepath.Base(file)]
		require.True(t, ok, "unlisted %s", file)
		paths, _, err := fixturePaths(file)
		require.NoError(t, err)
		for path := range paths {
			covered[path] = true
		}
	}
	operator, err := configschema.Operator(root)
	require.NoError(t, err)
	for _, path := range operator.Sorted() {
		require.True(t, covered[path], "uncovered %s", path)
	}
}

func TestVersionBoundaryRetainsUnsupportedExpectations(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	file := filepath.Join(root, "internal/adapter/config/testdata/render_hcl_tls_native_reload_metrics.hcl")
	spec := caseSpec{MinVersion: "2.7.0", Unsupported: []string{"tls_auto_reload"}}
	for _, version := range []string{"2.6.4", "2.7.0-rc.1", "v2.7.0+build.1", "2.7.1"} {
		current, err := platformsemver.Parse(version)
		require.NoError(t, err)
		prepared, _, err := prepareFixture(file, spec, current, configschema.Keys{})
		require.NoError(t, err)
		want := version == "2.6.4" || version == "2.7.0-rc.1"
		require.Equal(t, want, prepared.WantFailure)
		require.Equal(t, "2", prepared.Assertions["Listeners.#"])
	}
}

func TestRaftTypoIsRejectedBeyondParserValidation(t *testing.T) {
	problems := schemaProblems(configschema.Keys{"storage.retry_join_as_non_voterr": true},
		configschema.Keys{"storage.retry_join_as_non_voter": true})
	require.Equal(t, []string{"unsupported config path storage.retry_join_as_non_voterr"}, problems)
	test := preparedCase{WantFailure: true, ErrorContains: []string{"retry_join_as_non_voterr"}, Problems: problems}
	err := checkResult(test, nil, nil)
	require.NoError(t, err)
}

func TestManifestRequiresPositiveCoverageForEachFixture(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hack/ci/openbao-config-compat")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	m := manifest{SchemaVersion: 1, Versions: []string{"2.6.4", "2.7.1"},
		VersionRoles:  map[string]string{"2.6.4": "migration-compatibility", "2.7.1": "current-release"},
		Fixtures:      map[string]caseSpec{"future.hcl": {MinVersion: "2.8.0"}},
		NegativeCases: []negativeCase{{File: "negative.hcl", Contains: []string{"unknown field"}}}}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600))
	_, err = loadManifest(root)
	require.ErrorContains(t, err, "future.hcl has no positive case")
}
