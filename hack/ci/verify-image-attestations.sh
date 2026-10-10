#!/usr/bin/env bash

set -euo pipefail

: "${REPO:?REPO is required (owner/repo)}"
: "${VERSION:?VERSION is required}"
: "${MANAGER_IMAGE:?MANAGER_IMAGE is required}"
: "${MANAGER_DIGEST:?MANAGER_DIGEST is required}"
: "${CONFIG_INIT_IMAGE:?CONFIG_INIT_IMAGE is required}"
: "${CONFIG_INIT_DIGEST:?CONFIG_INIT_DIGEST is required}"
: "${BACKUP_EXECUTOR_IMAGE:?BACKUP_EXECUTOR_IMAGE is required}"
: "${BACKUP_EXECUTOR_DIGEST:?BACKUP_EXECUTOR_DIGEST is required}"
: "${UPGRADE_EXECUTOR_IMAGE:?UPGRADE_EXECUTOR_IMAGE is required}"
: "${UPGRADE_EXECUTOR_DIGEST:?UPGRADE_EXECUTOR_DIGEST is required}"

SIGNER_WORKFLOW="${SIGNER_WORKFLOW:-${REPO}/.github/workflows/reusable-build.yml}"
SOURCE_REF="${SOURCE_REF:-refs/tags/${VERSION}}"
CERT_OIDC_ISSUER="${CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-10}"
RETRY_SECONDS="${RETRY_SECONDS:-6}"

verify_one() {
  local image_ref="$1"
  local attempts=0

  while (( attempts < MAX_ATTEMPTS )); do
    attempts=$((attempts + 1))
    if gh attestation verify "oci://${image_ref}" \
      --bundle-from-oci \
      --repo "${REPO}" \
      --signer-workflow "${SIGNER_WORKFLOW}" \
      --source-ref "${SOURCE_REF}" \
      --cert-oidc-issuer "${CERT_OIDC_ISSUER}" \
      --deny-self-hosted-runners >/dev/null; then
      echo "Verified attestation: ${image_ref}"
      return 0
    fi

    if (( attempts >= MAX_ATTEMPTS )); then
      echo "Failed to verify attestation after ${MAX_ATTEMPTS} attempts: ${image_ref}" >&2
      return 1
    fi
    sleep "${RETRY_SECONDS}"
  done
}

verify_one "${MANAGER_IMAGE}@${MANAGER_DIGEST}"
verify_one "${CONFIG_INIT_IMAGE}@${CONFIG_INIT_DIGEST}"
verify_one "${BACKUP_EXECUTOR_IMAGE}@${BACKUP_EXECUTOR_DIGEST}"
verify_one "${UPGRADE_EXECUTOR_IMAGE}@${UPGRADE_EXECUTOR_DIGEST}"
