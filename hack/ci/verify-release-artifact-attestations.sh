#!/usr/bin/env bash

set -euo pipefail

: "${REPO:?REPO is required (owner/repo)}"
: "${VERSION:?VERSION is required}"
: "${OWNER:?OWNER is required}"

CHECKSUMS_PATH="${CHECKSUMS_PATH:-dist/checksums.txt}"
SIGNER_WORKFLOW="${SIGNER_WORKFLOW:-${REPO}/.github/workflows/release.yml}"
SOURCE_REF="${SOURCE_REF:-refs/tags/${VERSION}}"
CERT_OIDC_ISSUER="${CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-10}"
RETRY_SECONDS="${RETRY_SECONDS:-6}"
VERIFY_CHART="${VERIFY_CHART:-auto}"
CHART_IMAGE="${CHART_IMAGE:-ghcr.io/${OWNER}/charts/openbao-operator}"

if [[ ! -f "${CHECKSUMS_PATH}" ]]; then
  echo "checksums file not found: ${CHECKSUMS_PATH}" >&2
  exit 1
fi

# Published releases carry this bundle; in-flight publication still uses the API.
checksums_bundle_args=()
if [[ -n "${CHECKSUMS_ATTESTATION_BUNDLE:-}" ]]; then
  if [[ ! -s "${CHECKSUMS_ATTESTATION_BUNDLE}" ]]; then
    echo "checksums attestation bundle missing or empty: ${CHECKSUMS_ATTESTATION_BUNDLE}" >&2
    exit 1
  fi
  checksums_bundle_args=(--bundle "${CHECKSUMS_ATTESTATION_BUNDLE}")
fi

if [[ "${VERIFY_CHART}" == "auto" ]]; then
  if [[ -n "${CHART_DIGEST:-}" ]]; then
    VERIFY_CHART="true"
  else
    VERIFY_CHART="false"
  fi
fi

verify_oci_subject() {
  local oci_ref="$1"
  local attempts=0

  while (( attempts < MAX_ATTEMPTS )); do
    attempts=$((attempts + 1))
    if gh attestation verify "oci://${oci_ref}" \
      --bundle-from-oci \
      --repo "${REPO}" \
      --signer-workflow "${SIGNER_WORKFLOW}" \
      --source-ref "${SOURCE_REF}" \
      --cert-oidc-issuer "${CERT_OIDC_ISSUER}" \
      --deny-self-hosted-runners >/dev/null; then
      echo "Verified attestation: ${oci_ref}"
      return 0
    fi

    if (( attempts >= MAX_ATTEMPTS )); then
      echo "Failed to verify attestation after ${MAX_ATTEMPTS} attempts: ${oci_ref}" >&2
      return 1
    fi
    sleep "${RETRY_SECONDS}"
  done
}

verify_file_subject() {
  local file_path="$1"
  local attempts=0

  while (( attempts < MAX_ATTEMPTS )); do
    attempts=$((attempts + 1))
    if gh attestation verify "${file_path}" \
      "${checksums_bundle_args[@]}" \
      --repo "${REPO}" \
      --signer-workflow "${SIGNER_WORKFLOW}" \
      --source-ref "${SOURCE_REF}" \
      --cert-oidc-issuer "${CERT_OIDC_ISSUER}" \
      --deny-self-hosted-runners >/dev/null; then
      echo "Verified attestation: ${file_path}"
      return 0
    fi

    if (( attempts >= MAX_ATTEMPTS )); then
      echo "Failed to verify attestation after ${MAX_ATTEMPTS} attempts: ${file_path}" >&2
      return 1
    fi
    sleep "${RETRY_SECONDS}"
  done
}

if [[ "${VERIFY_CHART}" == "true" ]]; then
  : "${CHART_DIGEST:?CHART_DIGEST is required when VERIFY_CHART=true}"
  verify_oci_subject "${CHART_IMAGE}@${CHART_DIGEST}"
fi
verify_file_subject "${CHECKSUMS_PATH}"
