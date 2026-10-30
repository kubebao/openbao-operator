#!/usr/bin/env bash
# Print the newest Release Candidate run that fully qualified one exact PR head.

set -euo pipefail

: "${REPO:?REPO is required (owner/repo)}"
: "${HEAD_SHA:?HEAD_SHA is required}"

WORKFLOW_PATH=".github/workflows/release-candidate.yml"
QUALIFIED_JOB="Qualified Release Candidate"

if [[ ! "${HEAD_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "HEAD_SHA must be a full lowercase commit SHA" >&2
  exit 1
fi

# The run API reports the PR head as head_sha. Only same-repository runs of the
# reviewed workflow path count; the newest run is returned first.
run_ids="$(
  gh api "repos/${REPO}/actions/workflows/release-candidate.yml/runs?head_sha=${HEAD_SHA}&event=pull_request&status=success&per_page=100" |
    jq -r \
      --arg path "${WORKFLOW_PATH}" \
      --arg repo "${REPO}" \
      --arg sha "${HEAD_SHA}" \
      '.workflow_runs[]
       | select(.path == $path and .head_sha == $sha and .event == "pull_request"
                and .conclusion == "success" and .head_repository.full_name == $repo)
       | .id'
)"

for run_id in ${run_ids}; do
  # Non-candidate runs succeed with every job skipped. Require the final job,
  # which only runs after every qualification job of the latest attempt passed.
  qualified="$(
    gh api --paginate "repos/${REPO}/actions/runs/${run_id}/jobs?filter=latest&per_page=100" |
      jq -s -r --arg name "${QUALIFIED_JOB}" \
        '[.[].jobs[] | select(.name == $name and .conclusion == "success")] | length'
  )"
  if (( qualified > 0 )); then
    printf '%s\n' "${run_id}"
    exit 0
  fi
done

echo "no successful '${QUALIFIED_JOB}' job found for ${HEAD_SHA} in ${WORKFLOW_PATH}" >&2
echo "Add the release:candidate label to the PR and wait for the Release Candidate workflow to pass." >&2
exit 1
