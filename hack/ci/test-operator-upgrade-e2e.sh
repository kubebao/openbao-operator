#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT_DIR}/hack/ci/operator-upgrade-e2e.sh"
digest="sha256:$(printf '%064d' 0)"

actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true bash "${HARNESS}")"
if [[ "${actual}" != *"baseline chart: oci://ghcr.io/kubebao/charts/openbao-operator;"* ]]; then
  echo "operator upgrade defaults do not select the published baseline chart: ${actual}" >&2
  exit 1
fi

actual="$(OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
  OPERATOR_UPGRADE_E2E_BASELINE_CHART=oci://ghcr.io/dc-tec/charts/openbao-operator \
  bash "${HARNESS}")"
if [[ "${actual}" != *"baseline chart: oci://ghcr.io/dc-tec/charts/openbao-operator;"* ]]; then
  echo "operator upgrade historical chart override was not applied: ${actual}" >&2
  exit 1
fi

for chart in \
  oci://ghcr.io/untrusted/charts/openbao-operator \
  oci://ghcr.io/kubebao/charts/openbao-backup \
  oci://ghcr.io/kubebao/charts/openbao-operator:0.5.2 \
  oci://ghcr.io/kubebao/charts/openbao-operator/extra \
  https://ghcr.io/kubebao/charts/openbao-operator; do
  if OPERATOR_UPGRADE_E2E_VERIFY_ONLY=true \
    OPERATOR_UPGRADE_E2E_BASELINE_CHART="${chart}" \
    bash "${HARNESS}" >/dev/null 2>&1; then
    echo "operator upgrade harness accepted invalid chart: ${chart}" >&2
    exit 1
  fi
done

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

echo "Operator upgrade helper and chart validation passed."
