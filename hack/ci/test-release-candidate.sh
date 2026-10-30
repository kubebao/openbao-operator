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
resolve_case = os.environ.get("RESOLVE_CASE", "")
sha = os.environ.get("HEAD_SHA", "")
tag_sha = os.environ.get("TAG_SHA", "")
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
elif endpoint == f"repos/{repo}/commits/{tag_sha}/pulls":
    prs = [{"number": 7, "merged_at": "2026-10-30T00:00:00Z", "merge_commit_sha": tag_sha}]
    if resolve_case == "no_pr":
        prs = []
    if resolve_case == "unmerged":
        prs[0]["merged_at"] = None
    if resolve_case == "duplicate_pr":
        prs.append({**prs[0], "number": 8})
    print(json.dumps(prs))
elif endpoint == f"repos/{repo}/pulls/7":
    head = os.environ["PR_BASE_SHA"] if resolve_case == "moved_head" else os.environ["PR_HEAD_SHA"]
    head_repo = "fork/openbao-operator" if resolve_case == "fork_head" else repo
    print(json.dumps({"head": {"sha": head, "repo": {"full_name": head_repo}}}))
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

# Resolve a tagged squash commit to its qualified PR head with real Git objects.
RESOLVER="${ROOT_DIR}/hack/ci/resolve-release-candidate.sh"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
repo_dir="${tmp_dir}/repo"
git init -q -b main "${repo_dir}"
git init -q --bare "${tmp_dir}/origin.git"
(
  cd "${repo_dir}"
  git remote add origin "${tmp_dir}/origin.git"
  echo base > file
  git add file
  GIT_COMMITTER_DATE=@1690000000 git commit -q -m base
  git checkout -q -b release
  echo release > file
  GIT_COMMITTER_DATE=@1700000000 git commit -q -am "chore(main): release 0.7.0"
  git checkout -q main
  git merge -q --squash release >/dev/null
  GIT_COMMITTER_DATE=@1700000100 git commit -q -m "chore(main): release 0.7.0 (#7)"
  git tag squash
  echo unqualified > file
  git commit -q -am unqualified
  git tag unqualified
  git push -q origin main release:refs/pull/7/head
)
PR_BASE_SHA="$(git -C "${repo_dir}" rev-parse main~2)"
PR_HEAD_SHA="$(git -C "${repo_dir}" rev-parse release)"
squash_sha="$(git -C "${repo_dir}" rev-parse squash)"
unqualified_sha="$(git -C "${repo_dir}" rev-parse unqualified)"
export PR_BASE_SHA PR_HEAD_SHA
unset HEAD_SHA

(
  cd "${repo_dir}"
  outputs="$(TEST_CASE=qualified RESOLVE_CASE=ok TAG_SHA="${squash_sha}" GITHUB_OUTPUT=/dev/stdout bash "${RESOLVER}" 2>/dev/null)" ||
    fail "qualified squash commit was rejected"
  expected="$(printf 'pr_number=7\nhead_sha=%s\ncandidate_run_id=101\nsource_date_epoch=1700000000' "${PR_HEAD_SHA}")"
  [[ "${outputs}" == "${expected}" ]] || fail "unexpected resolver outputs: ${outputs}"

  for case_args in \
    "qualified no_pr ${squash_sha} expected exactly one PR merged" \
    "qualified unmerged ${squash_sha} expected exactly one PR merged" \
    "qualified duplicate_pr ${squash_sha} expected exactly one PR merged" \
    "qualified fork_head ${squash_sha} head is from fork/openbao-operator" \
    "qualified moved_head ${squash_sha} does not match PR head" \
    "qualified ok ${unqualified_sha} differs from PR #7 head" \
    "none ok ${squash_sha} no successful 'Qualified Release Candidate' job"; do
    read -r test_case resolve_case tag_sha expected_error <<<"${case_args}"
    if TEST_CASE="${test_case}" RESOLVE_CASE="${resolve_case}" TAG_SHA="${tag_sha}" GITHUB_OUTPUT=/dev/null \
      bash "${RESOLVER}" >/dev/null 2>"${tmp_dir}/resolver.err"; then
      fail "resolver accepted ${case_args}"
    fi
    grep -Fq -- "${expected_error}" "${tmp_dir}/resolver.err" ||
      fail "resolver rejected ${resolve_case} for another reason: $(cat "${tmp_dir}/resolver.err")"
  done
)

echo "release candidate checks passed"
