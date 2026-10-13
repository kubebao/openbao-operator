#!/usr/bin/env bash
set -euo pipefail

root="$(pwd -P)"
nodes="${E2E_PARALLEL_NODES:-1}"
# Ginkgo requires serial execution for dry runs.
[[ "${E2E_DRY_RUN:-false}" != "true" ]] || nodes=1
args=(-v "--output-dir=${root}" "--timeout=${E2E_TIMEOUT:-1h}" "--procs=${nodes}")
[[ -z "${E2E_FOCUS:-}" ]] || args+=("--focus=${E2E_FOCUS}")
[[ -z "${E2E_LABEL_FILTER:-}" ]] || args+=("--label-filter=${E2E_LABEL_FILTER}")
[[ "${E2E_TRACE:-false}" != "true" ]] || args+=(--trace)
[[ "${E2E_NO_COLOR:-false}" != "true" ]] || args+=(--no-color)
[[ "${E2E_FAIL_ON_EMPTY:-false}" != "true" ]] || args+=(--fail-on-empty)
[[ "${E2E_KEEP_GOING:-false}" != "true" ]] || args+=(--keep-going)
[[ "${E2E_DRY_RUN:-false}" != "true" ]] || args+=(--dry-run)
[[ -z "${E2E_POLL_PROGRESS_AFTER:-}" ]] || args+=("--poll-progress-after=${E2E_POLL_PROGRESS_AFTER}")

for report in E2E_JUNIT_REPORT E2E_JSON_REPORT E2E_GOJSON_REPORT; do
  path="${!report:-}"
  [[ -n "${path}" ]] || continue
  # Ginkgo namespaces intermediate reports and merges them under output-dir.
  # Absolute report names would be joined to the suite directory twice.
  if [[ "${path}" == /* ]]; then
    path="$(python3 -c 'import os,sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))' "${path}" "${root}")"
  fi
  mkdir -p "${root}/$(dirname "${path}")"
  case "${report}" in
    E2E_JUNIT_REPORT) args+=("--junit-report=${path}") ;;
    E2E_JSON_REPORT) args+=("--json-report=${path}") ;;
    E2E_GOJSON_REPORT) args+=("--gojson-report=${path}") ;;
  esac
done

if [[ -n "${E2E_BINARY_DIR:-}" ]]; then
  bundle="$(cd "${E2E_BINARY_DIR}" && pwd)"
  python3 hack/ci/e2e-binaries.py verify "${bundle}" "${E2E_SOURCE_SHA:?tested source SHA is required}"
  chmod +x "${bundle}/ginkgo" "${bundle}/e2e_report"
  # Ginkgo starts each worker in the binary's directory. Keep the suite's
  # existing test/e2e working directory for relative manifests and Make calls.
  install -m 0755 "${bundle}/e2e.test" test/e2e/e2e-precompiled.test
  exec "${bundle}/ginkgo" "${args[@]}" "${root}/test/e2e/e2e-precompiled.test"
fi
exec "${GINKGO:-bin/ginkgo}" "${args[@]}" --tags=e2e ./test/e2e/
