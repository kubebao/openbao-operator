#!/usr/bin/env bash
set -euo pipefail

: "${PILOT_PLAN:?PILOT_PLAN is required}"
: "${PILOT_SOURCE:?PILOT_SOURCE is required}"
: "${SOURCE_DIR:?SOURCE_DIR is required}"
: "${IMAGE_DIR:?IMAGE_DIR is required}"
: "${OUTPUT_DIR:?OUTPUT_DIR is required}"
: "${SYFT_BIN:?SYFT_BIN is required}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
python3 "${root}/hack/ci/release-pilot.py" source --plan "${PILOT_PLAN}" --source "${PILOT_SOURCE}" --source-dir "${SOURCE_DIR}"
version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "${PILOT_PLAN}")"
epoch="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["sourceDateEpoch"])' "${PILOT_PLAN}")"
helm_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["helmVersion"])' "${PILOT_PLAN}")"
RELEASE_VERSION="${version}" SOURCE_DATE_EPOCH="${epoch}" RELEASE_HELM_VERSION="${helm_version}" \
  bash "${root}/hack/ci/package-canonical-release-files.sh"
python3 "${root}/hack/ci/release-pilot.py" source --plan "${PILOT_PLAN}" --source "${PILOT_SOURCE}" --source-dir "${SOURCE_DIR}"
python3 "${root}/hack/ci/release-pilot.py" files --plan "${PILOT_PLAN}" --source "${PILOT_SOURCE}" \
  --images "${IMAGE_DIR}" --files "${OUTPUT_DIR}" --output "${OUTPUT_DIR}/files.json"
