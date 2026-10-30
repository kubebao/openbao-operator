#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FILES_CHECK="${ROOT_DIR}/hack/ci/check-release-pr-files.sh"
RUN_FINDER="${ROOT_DIR}/hack/ci/find-release-candidate-run.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
mkdir -p "${tmp_dir}/bin"

fail() {
  echo "release candidate test failed: $*" >&2
  exit 1
}

release_metadata=$'.release-please-manifest.json\nCHANGELOG.md\ncharts/openbao-operator/Chart.yaml'

bash "${FILES_CHECK}" <<<"${release_metadata}" >/dev/null 2>&1 || fail "release metadata was rejected"
bash "${FILES_CHECK}" <<<"${release_metadata}"$'\nrelease-notes/0.7.0.md\nrelease-notes/0.7.0-rc.1.mdx\nwebsite/content/releases/0.7.0.md\nwebsite/content-versions/0.6.x/docs/_index.md\nwebsite/data/version_lines.yaml\nwebsite/hugo.toml\nwebsite/README.md' \
  >/dev/null 2>&1 || fail "release notes and documentation content were rejected"

for path in \
  .github/workflows/release.yml \
  .github/actions/docs-build/action.yml \
  .github/CODEOWNERS \
  hack/ci/run-e2e.sh \
  hack/docs/check-published-stable.sh \
  website/scripts/sync-api-reference.sh \
  test/e2e/e2e_suite_test.go \
  test/fixtures/operator-upgrade/versions.env \
  charts/openbao-operator/values.yaml \
  release-notes/drafts/0.7.0.md \
  Makefile \
  go.mod \
  Dockerfile; do
  if bash "${FILES_CHECK}" <<<"${release_metadata}"$'\n'"${path}" >/dev/null 2>&1; then
    fail "release PR path was accepted: ${path}"
  fi
done
if bash "${FILES_CHECK}" </dev/null >/dev/null 2>&1; then
  fail "empty release PR file list was accepted"
fi

cat > "${tmp_dir}/bin/gh" <<'PY'
#!/usr/bin/env python3
import json
import os
import sys

case = os.environ["TEST_CASE"]
sha = os.environ["HEAD_SHA"]
repo = os.environ["REPO"]
endpoint = next(arg for arg in sys.argv[1:] if arg.startswith("repos/"))


def run(run_id, **overrides):
    data = {
        "id": run_id,
        "path": ".github/workflows/release-candidate.yml",
        "head_sha": sha,
        "event": "pull_request",
        "conclusion": "success",
        "head_repository": {"full_name": repo},
    }
    data.update(overrides)
    return data


def jobs(conclusion):
    return [
        {"name": "Prepare", "conclusion": "success"},
        {"name": "Qualified Release Candidate", "conclusion": conclusion},
    ]


if "/actions/workflows/release-candidate.yml/runs?" in endpoint:
    assert f"head_sha={sha}" in endpoint and "event=pull_request" in endpoint and "status=success" in endpoint
    runs = {
        "qualified": [run(101)],
        "newest_unqualified": [run(102), run(101)],
        "skipped_only": [run(102)],
        "failed_job": [run(103)],
        "none": [],
        "wrong_path": [run(101, path=".github/workflows/ci.yml")],
        "fork": [run(101, head_repository={"full_name": "fork/openbao-operator"})],
        "wrong_sha": [run(101, head_sha="0" * 40)],
        "wrong_event": [run(101, event="workflow_dispatch")],
        "not_success": [run(101, conclusion="failure")],
        "paginated": [run(104)],
    }[case]
    print(json.dumps({"workflow_runs": runs}))
elif endpoint.startswith(f"repos/{repo}/actions/runs/") and "/jobs?filter=latest" in endpoint:
    assert "--paginate" in sys.argv
    run_id = int(endpoint.split("/")[5])
    if run_id == 101:
        print(json.dumps({"jobs": jobs("success")}))
    elif run_id == 102:
        print(json.dumps({"jobs": jobs("skipped")}))
    elif run_id == 103:
        print(json.dumps({"jobs": jobs("failure")}))
    elif run_id == 104:
        print(json.dumps({"jobs": [{"name": "Prepare", "conclusion": "success"}]}))
        print(json.dumps({"jobs": [{"name": "Qualified Release Candidate", "conclusion": "success"}]}))
    else:
        raise SystemExit(f"unexpected run: {run_id}")
else:
    raise SystemExit(f"unexpected API endpoint: {endpoint}")
PY
chmod +x "${tmp_dir}/bin/gh"

export PATH="${tmp_dir}/bin:${PATH}"
export REPO=kubebao/openbao-operator
export HEAD_SHA=1111111111111111111111111111111111111111

expect_run() {
  local expected="$1"
  local actual
  actual="$(bash "${RUN_FINDER}" 2>"${tmp_dir}/stderr")" || fail "${TEST_CASE}: no run found: $(cat "${tmp_dir}/stderr")"
  [[ "${actual}" == "${expected}" ]] || fail "${TEST_CASE}: expected run ${expected}, got ${actual}"
}

TEST_CASE=qualified expect_run 101
TEST_CASE=newest_unqualified expect_run 101
TEST_CASE=paginated expect_run 104

for TEST_CASE in skipped_only failed_job none wrong_path fork wrong_sha wrong_event not_success; do
  export TEST_CASE
  if bash "${RUN_FINDER}" >/dev/null 2>&1; then
    fail "unqualified candidate accepted: ${TEST_CASE}"
  fi
done

if TEST_CASE=qualified HEAD_SHA=1111111 bash "${RUN_FINDER}" >/dev/null 2>&1; then
  fail "abbreviated head SHA accepted"
fi

echo "release candidate checks passed"
