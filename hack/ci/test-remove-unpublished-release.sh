#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/hack/ci/remove-unpublished-release.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
mkdir -p "${tmp_dir}/bin"

cat > "${tmp_dir}/bin/gh" <<'PY'
#!/usr/bin/env python3
import json
import os
import sys

args = sys.argv[1:]
case = os.environ["TEST_CASE"]
sha = os.environ["EXPECTED_COMMIT_SHA"]
tag_sha = os.environ["EXPECTED_TAG_OBJECT_SHA"]
with open(os.environ["TEST_CALLS"], "a", encoding="utf-8") as calls:
    calls.write(" ".join(args) + "\n")
if "DELETE" in args:
    sys.exit(0)
endpoint = args[1]
if endpoint.endswith("/jobs?filter=all&per_page=100"):
    jobs = [{"name": "API Contract Gate", "status": "completed", "conclusion": "success", "steps": []}]
    promotion = {"name": "Promote + Publish Release", "status": "completed", "conclusion": "cancelled", "steps": []}
    if case == "promotion_started":
        promotion["steps"] = [{"name": "Promote tags (no rebuild)", "conclusion": "cancelled"}]
    if case == "unknown_steps":
        promotion["steps"] = None
    if case == "previous_promotion":
        jobs.append({**promotion, "conclusion": "success", "steps": [{"name": "Promote tags (no rebuild)"}]})
    if case != "missing_promotion":
        jobs.append(promotion)
    data = {"jobs": [] if case == "no_jobs" else jobs}
elif "/actions/runs/" in endpoint:
    data = {"head_sha": sha, "head_branch": "0.5.2", "event": "push", "path": ".github/workflows/release.yml", "status": "completed", "conclusion": "cancelled"}
    if case == "wrong_commit":
        data["head_sha"] = "0" * 40
    if case == "wrong_version":
        data["head_branch"] = "0.5.1"
    if case == "running":
        data["status"] = "in_progress"
    if case == "successful_run":
        data["conclusion"] = "success"
elif "/git/ref/" in endpoint:
    data = {"object": {"type": "tag", "sha": "0" * 40 if case == "wrong_tag" else tag_sha}}
elif "/git/tags/" in endpoint:
    data = {"tag": "0.5.2", "object": {"type": "commit", "sha": sha}, "verification": {"verified": case != "unsigned_tag"}}
elif endpoint.endswith("/releases"):
    data = [{"id": 17, "tag_name": "0.5.2", "draft": case != "published", "published_at": None, "assets": [{"name": "install.yaml"}] if case == "assets" else []}]
    if case == "missing_draft":
        data = []
    if case == "duplicate_release":
        data *= 2
else:
    raise SystemExit(f"unexpected API endpoint: {endpoint}")
print(json.dumps(data))
PY
chmod +x "${tmp_dir}/bin/gh"

export PATH="${tmp_dir}/bin:${PATH}"
export REPO=kubebao/openbao-operator VERSION=0.5.2 FAILED_RUN_ID=42
export EXPECTED_TAG_OBJECT_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EXPECTED_COMMIT_SHA="$(git rev-parse HEAD)"
export EXPECTED_COMMIT_SHA GH_READ_TOKEN=test-read GH_WRITE_TOKEN=test-write
export TEST_CALLS="${tmp_dir}/calls"

for TEST_CASE in wrong_commit wrong_version wrong_tag unsigned_tag running successful_run published assets duplicate_release promotion_started previous_promotion missing_promotion unknown_steps no_jobs; do
  export TEST_CASE
  : > "${TEST_CALLS}"
  if bash "${SCRIPT}" > "${tmp_dir}/output" 2>&1; then
    echo "release removal accepted invalid state: ${TEST_CASE}" >&2
    exit 1
  fi
  if grep -q DELETE "${TEST_CALLS}"; then
    echo "release removal mutated invalid state: ${TEST_CASE}" >&2
    exit 1
  fi
done

for TEST_CASE in valid missing_draft; do
  export TEST_CASE
  : > "${TEST_CALLS}"
  bash "${SCRIPT}" > "${tmp_dir}/output" 2>&1
  expected="api --method DELETE repos/${REPO}/git/refs/tags/0.5.2"
  if [[ "${TEST_CASE}" == valid ]]; then
    expected="$(printf '%s\n%s' "api --method DELETE repos/${REPO}/releases/17" "${expected}")"
  fi
  actual="$(grep DELETE "${TEST_CALLS}")"
  [[ "${actual}" == "${expected}" ]] || {
    echo "unexpected release deletion sequence: ${actual}" >&2
    exit 1
  }
done

echo "Unpublished release removal guards passed."
