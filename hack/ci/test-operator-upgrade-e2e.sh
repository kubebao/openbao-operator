#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT_DIR}/hack/ci/operator-upgrade-e2e.sh"
# shellcheck source=../../test/fixtures/operator-upgrade/versions.env
source "${ROOT_DIR}/test/fixtures/operator-upgrade/versions.env"
digest="sha256:$(printf '%064d' 0)"

target="${OPERATOR_UPGRADE_TARGET_RELEASE:-$(jq -er '."."' "${ROOT_DIR}/.release-please-manifest.json")}"
expected="Operator upgrade E2E harness verified: ${OPERATOR_UPGRADE_BASELINE} -> ${target}-e2e (baseline chart: ${OPERATOR_UPGRADE_BASELINE_CHART}; API migration fixtures: ${OPERATOR_UPGRADE_MIGRATION_BASELINE} -> ${OPERATOR_UPGRADE_MIGRATION_TARGET}; API group migration from ${OPERATOR_UPGRADE_API_MIGRATION_SOURCE})"
actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true bash "${HARNESS}")"
if [[ "${actual}" != "${expected}" ]]; then
  echo "operator upgrade defaults do not qualify the latest stable source: ${actual}" >&2
  exit 1
fi

actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
  OPERATOR_UPGRADE_E2E_TARGET_RELEASE=9.8.7-rc.1 \
  OPERATOR_UPGRADE_E2E_TARGET_VERSION=9.8.7-rc.1 \
  bash "${HARNESS}")"
expected="Operator upgrade E2E harness verified: ${OPERATOR_UPGRADE_BASELINE} -> 9.8.7-rc.1 (baseline chart: ${OPERATOR_UPGRADE_BASELINE_CHART}; API migration fixtures: ${OPERATOR_UPGRADE_MIGRATION_BASELINE} -> ${OPERATOR_UPGRADE_MIGRATION_TARGET}; API group migration from ${OPERATOR_UPGRADE_API_MIGRATION_SOURCE})"
if [[ "${actual}" != "${expected}" ]]; then
  echo "operator upgrade release-candidate override was not applied: ${actual}" >&2
  exit 1
fi

actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true OPERATOR_UPGRADE_E2E_API_MIGRATION_SOURCE= bash "${HARNESS}")"
if [[ "${actual}" != *"; in-place upgrade)" ]]; then
  echo "operator upgrade in-place override was not applied: ${actual}" >&2
  exit 1
fi
if OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true OPERATOR_UPGRADE_E2E_API_MIGRATION_SOURCE=0.6 \
  bash "${HARNESS}" >/dev/null 2>&1; then
  echo "operator upgrade harness accepted an invalid API migration source" >&2
  exit 1
fi
if OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true OPERATOR_UPGRADE_E2E_API_MIGRATION_SOURCE=0.5.3 \
  bash "${HARNESS}" >/dev/null 2>&1; then
  echo "operator upgrade harness accepted an API migration source outside its pinned baseline" >&2
  exit 1
fi

actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
  OPERATOR_UPGRADE_E2E_BASELINE_CHART=oci://ghcr.io/dc-tec/charts/openbao-operator \
  bash "${HARNESS}")"
if [[ "${actual}" != *"baseline chart: oci://ghcr.io/dc-tec/charts/openbao-operator;"* ]]; then
  echo "operator upgrade historical chart override was not applied: ${actual}" >&2
  exit 1
fi

for owner in dc-tec kubebao; do
  OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
    OPERATOR_UPGRADE_E2E_HARDENED_INIT_IMAGE="ghcr.io/${owner}/openbao-init@${digest}" \
    bash "${HARNESS}" >/dev/null
done

for image in \
  ghcr.io/kubebao/openbao-init:0.5.3 \
  "ghcr.io/untrusted/openbao-init@${digest}" \
  "ghcr.io/kubebao/openbao-backup@${digest}" \
  ghcr.io/kubebao/openbao-init@sha256:1234; do
  if OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
    OPERATOR_UPGRADE_E2E_HARDENED_INIT_IMAGE="${image}" \
    bash "${HARNESS}" >/dev/null 2>&1; then
    echo "operator upgrade harness accepted invalid helper: ${image}" >&2
    exit 1
  fi
done

echo "Operator upgrade helper validation passed."
