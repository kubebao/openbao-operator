#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT_DIR}/hack/ci/operator-upgrade-e2e.sh"
digest="sha256:$(printf '%064d' 0)"

expected="Operator upgrade E2E harness verified: 0.5.2 -> 0.6.0-e2e (baseline chart: oci://ghcr.io/kubebao/charts/openbao-operator; API migration fixtures: 0.5.0 -> 0.5.1)"
actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true bash "${HARNESS}")"
if [[ "${actual}" != "${expected}" ]]; then
  echo "operator upgrade defaults do not qualify the latest stable source: ${actual}" >&2
  exit 1
fi

actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
  OPERATOR_UPGRADE_E2E_TARGET_RELEASE=0.6.0-rc.1 \
  OPERATOR_UPGRADE_E2E_TARGET_VERSION=0.6.0-rc.1 \
  bash "${HARNESS}")"
expected="Operator upgrade E2E harness verified: 0.5.2 -> 0.6.0-rc.1 (baseline chart: oci://ghcr.io/kubebao/charts/openbao-operator; API migration fixtures: 0.5.0 -> 0.5.1)"
if [[ "${actual}" != "${expected}" ]]; then
  echo "operator upgrade release-candidate override was not applied: ${actual}" >&2
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
  ghcr.io/kubebao/openbao-init:0.5.2 \
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
