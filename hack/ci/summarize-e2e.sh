#!/usr/bin/env bash
set -euo pipefail

mkdir -p "${E2E_REPORT_DIR:?report directory is required}"
expected_cases="${E2E_REPORT_DIR}/expected-cases.json"
printf '%s\n' "${EXPECTED_CASE_IDS:?expected scenario IDs are required}" > "${expected_cases}"
if [[ ! -s "${E2E_JSON_REPORT:-}" ]]; then
  printf '## E2E Report Summary\n\nNo Ginkgo JSON report was produced for %s.\n' \
    "${E2E_LANE_NAME:?lane name is required}" > "${E2E_SUMMARY_REPORT}"
  cat "${E2E_SUMMARY_REPORT}" >> "${GITHUB_STEP_SUMMARY}"
  exit 1
fi

report_status=0
"${E2E_BINARY_DIR:?binary directory is required}/e2e_report" \
  --json-report "${E2E_JSON_REPORT}" \
  --manifest test/e2e/suites.yaml \
  --fail-on-selected-skips \
  --expected-cases "${expected_cases}" \
  --markdown-out "${E2E_SUMMARY_REPORT}" \
  --json-out "${E2E_SUMMARY_JSON}" \
  --lane "${E2E_LANE_NAME}" \
  --selector "${E2E_LABEL_FILTER}" \
  --kubernetes-version "${KIND_NODE_IMAGE}" \
  --openbao-version "${E2E_OPENBAO_IMAGE}" || report_status=$?
if [[ -s "${E2E_SUMMARY_REPORT}" ]]; then
  cat "${E2E_SUMMARY_REPORT}" >> "${GITHUB_STEP_SUMMARY}"
fi
exit "${report_status}"
