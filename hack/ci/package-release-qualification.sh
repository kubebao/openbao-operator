#!/usr/bin/env bash
set -euo pipefail

: "${QUALIFICATION_PLAN:?}"
: "${QUALIFICATION_BUILD:?}"
: "${SOURCE_DIR:?}"
: "${IMAGE_DIR:?}"
: "${OUTPUT_DIR:?}"
: "${SYFT_BIN:?}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
python3 "${root}/hack/ci/qualification-artifacts.py" --plan "${QUALIFICATION_PLAN}" source --directory "${SOURCE_DIR}"
version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "${QUALIFICATION_PLAN}")"
epoch="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["sources"]["sourceDateEpoch"])' "${QUALIFICATION_PLAN}")"
helm_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["inputs"]["tools"]["versions"]["HELM_VERSION"])' "${QUALIFICATION_PLAN}")"
RELEASE_VERSION="${version}" SOURCE_DATE_EPOCH="${epoch}" RELEASE_HELM_VERSION="${helm_version}" \
  bash "${root}/hack/ci/package-canonical-release-files.sh"
python3 "${root}/hack/ci/qualification-artifacts.py" --plan "${QUALIFICATION_PLAN}" source --directory "${SOURCE_DIR}"
python3 "${root}/hack/ci/qualification-artifacts.py" --plan "${QUALIFICATION_PLAN}" record-files \
  --build "${QUALIFICATION_BUILD}" --images "${IMAGE_DIR}" --directory "${OUTPUT_DIR}" --output "${OUTPUT_DIR}/files.json"
