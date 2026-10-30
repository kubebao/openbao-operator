#!/usr/bin/env bash
# Require a human environment approval for this immutable workflow run.
set -euo pipefail

: "${REPO:?REPO is required}"
: "${RUN_ID:?RUN_ID is required}"
: "${ENVIRONMENT_NAME:?ENVIRONMENT_NAME is required}"
REQUIRED_APPROVER="${REQUIRED_APPROVER:-dc-tec}"

[[ "${RUN_ID}" =~ ^[0-9]+$ ]] || { echo "RUN_ID must be numeric" >&2; exit 1; }
case "${ENVIRONMENT_NAME}" in
  release-candidate|release-publish) ;;
  *) echo "unknown release approval environment" >&2; exit 1 ;;
esac

# A workflow can create an unprotected environment automatically. Its job
# succeeding is therefore insufficient: require the actual review history.
reviews="$(gh api "repos/${REPO}/actions/runs/${RUN_ID}/approvals")"
if ! jq -e --arg environment "${ENVIRONMENT_NAME}" --arg approver "${REQUIRED_APPROVER}" '
  any(.[]; .state == "approved" and .user.login == $approver and .user.type == "User"
      and any(.environments[]; .name == $environment))
' <<<"${reviews}" >/dev/null; then
  echo "${ENVIRONMENT_NAME} requires an approval from ${REQUIRED_APPROVER} for run ${RUN_ID}" >&2
  exit 1
fi
echo "${ENVIRONMENT_NAME} approved by ${REQUIRED_APPROVER} for run ${RUN_ID}."
