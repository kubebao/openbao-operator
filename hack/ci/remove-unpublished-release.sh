#!/usr/bin/env bash
set -euo pipefail

: "${REPO:?REPO is required}"
: "${VERSION:?VERSION is required}"
: "${EXPECTED_TAG_OBJECT_SHA:?EXPECTED_TAG_OBJECT_SHA is required}"
: "${EXPECTED_COMMIT_SHA:?EXPECTED_COMMIT_SHA is required}"
: "${FAILED_RUN_ID:?FAILED_RUN_ID is required}"
: "${GH_READ_TOKEN:?GH_READ_TOKEN is required}"
: "${GH_WRITE_TOKEN:?GH_WRITE_TOKEN is required}"

[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]
[[ "${EXPECTED_TAG_OBJECT_SHA}" =~ ^[0-9a-f]{40}$ ]]
[[ "${EXPECTED_COMMIT_SHA}" =~ ^[0-9a-f]{40}$ ]]
[[ "${FAILED_RUN_ID}" =~ ^[0-9]+$ ]]
git merge-base --is-ancestor "${EXPECTED_COMMIT_SHA}" HEAD

gh_read() { GH_TOKEN="${GH_READ_TOKEN}" gh "$@"; }
gh_write() { GH_TOKEN="${GH_WRITE_TOKEN}" gh "$@"; }

run="$(gh_read api "repos/${REPO}/actions/runs/${FAILED_RUN_ID}")"
jq -e --arg sha "${EXPECTED_COMMIT_SHA}" --arg version "${VERSION}" '
  .head_sha == $sha and .head_branch == $version and .event == "push" and
  .path == ".github/workflows/release.yml" and .status == "completed" and
  (.conclusion == "failure" or .conclusion == "cancelled" or .conclusion == "timed_out")
' <<<"${run}" >/dev/null

jobs="$(gh_read api "repos/${REPO}/actions/runs/${FAILED_RUN_ID}/jobs?filter=all&per_page=100" --paginate | jq -s '[.[].jobs[]]')"
jq -e '
  any(.[]; .name == "Promote + Publish Release") and
  all(.[] | select(.name == "Promote + Publish Release");
    .status == "completed" and
    (.conclusion == "skipped" or .conclusion == "cancelled") and
    (.steps | type) == "array" and
    (.steps | length) == 0)
' <<<"${jobs}" >/dev/null

ref="$(gh_read api "repos/${REPO}/git/ref/tags/${VERSION}")"
jq -e --arg sha "${EXPECTED_TAG_OBJECT_SHA}" '
  .object.type == "tag" and .object.sha == $sha
' <<<"${ref}" >/dev/null
tag="$(gh_read api "repos/${REPO}/git/tags/${EXPECTED_TAG_OBJECT_SHA}")"
jq -e --arg sha "${EXPECTED_COMMIT_SHA}" --arg version "${VERSION}" '
  .tag == $version and .object.type == "commit" and .object.sha == $sha and
  .verification.verified == true
' <<<"${tag}" >/dev/null

releases="$(gh_write api "repos/${REPO}/releases" --paginate | jq -s --arg version "${VERSION}" '[.[][] | select(.tag_name == $version)]')"
jq -e '
  length <= 1 and all(.[]; .draft == true and .published_at == null and (.assets | length) == 0)
' <<<"${releases}" >/dev/null

# The draft can already be absent if a previous attempt deleted it before the
# tag deletion failed. All source and publication checks still apply.
release_id="$(jq -r '.[0].id // empty' <<<"${releases}")"
if [[ -n "${release_id}" ]]; then
  gh_write api --method DELETE "repos/${REPO}/releases/${release_id}"
fi
gh_write api --method DELETE "repos/${REPO}/git/refs/tags/${VERSION}"
echo "Removed unpublished release ${VERSION} at ${EXPECTED_COMMIT_SHA}."
