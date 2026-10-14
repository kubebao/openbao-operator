package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScriptCleansTemporaryStateAndPreservesParserFailure(t *testing.T) {
	for _, status := range []int{0, 17} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			root, err := filepath.Abs("../../../..")
			require.NoError(t, err)
			bin, temp, reports := t.TempDir(), t.TempDir(), t.TempDir()
			writeExecutable(t, filepath.Join(bin, "docker"),
				"#!/usr/bin/env bash\necho 'OpenBao v2.4.4 (aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)'\n")
			writeExecutable(t, filepath.Join(bin, "git"), "#!/usr/bin/env bash\nexit 0\n")
			prepare := filepath.Join(bin, "prepare-stub")
			writeExecutable(t, prepare, fakePrepare)
			writeExecutable(t, filepath.Join(bin, "go"), fakeGo)
			cmd := exec.Command("bash", filepath.Join(root, "hack/ci/openbao-config-compat.sh"))
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+temp, "COMPAT_REPORT_DIR="+reports, "REPORT_SCHEMA_DRIFT=true",
				"PREPARE_STUB="+prepare, "PARSER_STATUS="+strconv.Itoa(status), "GITHUB_STEP_SUMMARY=")
			output, err := cmd.CombinedOutput()
			if status == 0 {
				require.NoError(t, err, string(output))
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, string(output))
				require.Equal(t, status, exit.ExitCode(), string(output))
			}
			entries, err := os.ReadDir(temp)
			require.NoError(t, err)
			require.Empty(t, entries, "parser and schema scratch directories must both be removed")
		})
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o700))
}

const fakePrepare = `#!/usr/bin/env bash
set -euo pipefail
output=""
schema=""
while (($#)); do
  case "$1" in
    -list-versions) echo 2.4.4; exit 0 ;;
    -validate-version) exit 0 ;;
    -output) output="$2"; shift 2 ;;
    -schema-output) schema="$2"; shift 2 ;;
    *) shift 2 ;;
  esac
done
if [[ -n "$output" ]]; then echo '{}' > "$output"; fi
if [[ -n "$schema" ]]; then echo 'storage.path' > "$schema"; fi
`

const fakeGo = `#!/usr/bin/env bash
set -euo pipefail
case "$1:$2" in
  build:-o) cp "$PREPARE_STUB" "$3" ;;
  mod:init|mod:tidy) exit 0 ;;
  mod:download) echo '{"Origin":{"Hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}' ;;
  get:*) exit 0 ;;
  list:-m) echo /unused/upstream ;;
  run:.) echo 'test parser summary'; echo '{}' > "$4"; exit "$PARSER_STATUS" ;;
  *) echo "unexpected fake go invocation" >&2; exit 1 ;;
esac
`
