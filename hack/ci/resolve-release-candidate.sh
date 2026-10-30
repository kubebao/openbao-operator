#!/usr/bin/env bash
# Resolve the qualified Release Candidate run for a tagged release commit.
#
# The tagged commit must be the squash merge of a same-repository PR whose head
# has the same tree and passed the Release Candidate workflow. That PR is the
# release PR, or a release:candidate fix PR for a branch-head retry.

set -euo pipefail

: "${REPO:?REPO is required (owner/repo)}"
: "${TAG_SHA:?TAG_SHA is required}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT="${GITHUB_OUTPUT:-/dev/stdout}"

fail() {
  echo "$*" >&2
  exit 1
}

[[ "${TAG_SHA}" =~ ^[0-9a-f]{40}$ ]] || fail "TAG_SHA must be a full lowercase commit SHA"

pr_number="$(
  gh api "repos/${REPO}/commits/${TAG_SHA}/pulls" |
    jq -r --arg sha "${TAG_SHA}" \
      '[.[] | select(.merged_at != null and .merge_commit_sha == $sha)]
       | if length == 1 then .[0].number else empty end'
)"
[[ "${pr_number}" =~ ^[0-9]+$ ]] || fail "expected exactly one PR merged as ${TAG_SHA}"

pr_json="$(gh api "repos/${REPO}/pulls/${pr_number}")"
head_sha="$(jq -r '.head.sha // ""' <<<"${pr_json}")"
head_repo="$(jq -r '.head.repo.full_name // ""' <<<"${pr_json}")"
[[ "${head_sha}" =~ ^[0-9a-f]{40}$ ]] || fail "PR #${pr_number} has no head commit"
[[ "${head_repo}" == "${REPO}" ]] || fail "PR #${pr_number} head is from ${head_repo:-a deleted repository}, expected ${REPO}"

git fetch --quiet --no-tags origin "refs/pull/${pr_number}/head"
[[ "$(git rev-parse FETCH_HEAD)" == "${head_sha}" ]] || fail "refs/pull/${pr_number}/head does not match PR head ${head_sha}"

tag_tree="$(git rev-parse "${TAG_SHA}^{tree}")"
head_tree="$(git rev-parse "${head_sha}^{tree}")"
if [[ "${tag_tree}" != "${head_tree}" ]]; then
  fail "tagged commit ${TAG_SHA} tree ${tag_tree} differs from PR #${pr_number} head ${head_sha} tree ${head_tree}"
fi

run_id="$(HEAD_SHA="${head_sha}" bash "${SCRIPT_DIR}/find-release-candidate-run.sh")"
source_date_epoch="$(git show -s --format=%ct "${head_sha}")"

echo "Tagged commit ${TAG_SHA} matches PR #${pr_number} head ${head_sha}, qualified by run ${run_id}." >&2
{
  echo "pr_number=${pr_number}"
  echo "head_sha=${head_sha}"
  echo "candidate_run_id=${run_id}"
  echo "source_date_epoch=${source_date_epoch}"
} >> "${OUTPUT}"
