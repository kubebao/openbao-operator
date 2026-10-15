#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VALIDATOR="${ROOT_DIR}/hack/ci/validate-release-as-request.sh"
RELEASE_AS_RESOLVER="${ROOT_DIR}/hack/ci/resolve-release-as-override.sh"
CHART_PREPARER="${ROOT_DIR}/hack/ci/prepare-release-chart.sh"
SPDX_NORMALIZER="${ROOT_DIR}/hack/ci/normalize-spdx-json.sh"
POST_RELEASE_VERIFIER="${ROOT_DIR}/hack/ci/verify-post-release.sh"
RELEASE_WORKFLOW="${ROOT_DIR}/.github/workflows/release.yml"
RELEASE_PLEASE_WORKFLOW="${ROOT_DIR}/.github/workflows/release-please.yml"
RELEASE_PLEASE_PACKAGE="${ROOT_DIR}/hack/ci/release-please/package.json"
RELEASE_PLEASE_LOCK="${ROOT_DIR}/hack/ci/release-please/package-lock.json"
RELEASE_PLEASE_EXACT_RUNNER="${ROOT_DIR}/hack/ci/run-release-please-exact.js"
RELEASE_PR_GATE_WORKFLOW="${ROOT_DIR}/.github/workflows/release-pr-gate.yml"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

fail() {
  echo "release automation test failed: $*" >&2
  exit 1
}

expect_valid_request() {
  local target_branch="$1"
  local version="$2"
  local expected_branch="$3"
  local actual_branch

  actual_branch="$(
    env -u GITHUB_OUTPUT \
      TARGET_BRANCH="${target_branch}" \
      VERSION="${version}" \
      bash "${VALIDATOR}"
  )"
  [[ "${actual_branch}" == "${expected_branch}" ]] || {
    fail "expected marker '${expected_branch}', got '${actual_branch}'"
  }
}

expect_invalid_request() {
  local target_branch="$1"
  local version="$2"

  if TARGET_BRANCH="${target_branch}" VERSION="${version}" bash "${VALIDATOR}" >/dev/null 2>&1; then
    fail "expected target='${target_branch}' version='${version}' to be rejected"
  fi
}

resolve_release_as() {
  local event_name="$1"
  local target_branch="$2"
  local dispatch_release_as="$3"
  local commit_subject="$4"
  local commit_is_empty="$5"

  env -u GITHUB_OUTPUT \
    EVENT_NAME="${event_name}" \
    TARGET_BRANCH="${target_branch}" \
    DISPATCH_RELEASE_AS="${dispatch_release_as}" \
    COMMIT_SUBJECT="${commit_subject}" \
    COMMIT_IS_EMPTY="${commit_is_empty}" \
    bash "${RELEASE_AS_RESOLVER}"
}

expect_invalid_release_as_resolution() {
  if resolve_release_as "$@" >/dev/null 2>&1; then
    fail "expected Release-As resolution to fail for '$*'"
  fi
}

write_chart_fixture() {
  local chart_file="$1"

  cat > "${chart_file}" <<'EOF'
apiVersion: v2
name: openbao-operator
version: 0.5.0
annotations:
  artifacthub.io/prerelease: "false"
  artifacthub.io/containsSecurityUpdates: 'false'
  artifacthub.io/changes: |
    - kind: changed
      description: "stale"
  artifacthub.io/images: |
    - name: openbao-operator
      image: ghcr.io/example/openbao-operator:stale
EOF
}

assert_contains() {
  local file="$1"
  local expected="$2"

  grep -Fq -- "${expected}" "${file}" || fail "expected '${expected}' in ${file}"
}

assert_not_contains() {
  local file="$1"
  local unexpected="$2"

  if grep -Fq -- "${unexpected}" "${file}"; then
    fail "did not expect '${unexpected}' in ${file}"
  fi
}

assert_ordered() {
  local file="$1"
  local first="$2"
  local second="$3"
  local first_line
  local second_line

  first_line="$(grep -nF -- "${first}" "${file}" | head -n 1 | cut -d: -f1)"
  second_line="$(grep -nF -- "${second}" "${file}" | head -n 1 | cut -d: -f1)"

  [[ -n "${first_line}" ]] || fail "expected '${first}' in ${file}"
  [[ -n "${second_line}" ]] || fail "expected '${second}' in ${file}"
  (( first_line < second_line )) || fail "expected '${first}' before '${second}' in ${file}"
}

bash -n \
  "${VALIDATOR}" \
  "${RELEASE_AS_RESOLVER}" \
  "${CHART_PREPARER}" \
  "${SPDX_NORMALIZER}" \
  "${POST_RELEASE_VERIFIER}"

assert_contains "${RELEASE_PLEASE_WORKFLOW}" "Resolve Release-As override"
assert_contains "${RELEASE_PLEASE_WORKFLOW}" "bash hack/ci/resolve-release-as-override.sh"
assert_contains "${RELEASE_PLEASE_WORKFLOW}" "Run release-please action (normal manifest PR)"
assert_contains "${RELEASE_PLEASE_WORKFLOW}" "Run release-please wrapper (exact-version manifest PR)"
assert_contains "${RELEASE_PLEASE_PACKAGE}" '"release-please": "17.11.2"'
assert_contains "${RELEASE_PLEASE_WORKFLOW}" "node hack/ci/run-release-please-exact.js"
assert_contains "${RELEASE_PLEASE_EXACT_RUNNER}" "PINNED_RELEASE_PLEASE_VERSION = '17.11.2'"
assert_contains "${RELEASE_PLEASE_WORKFLOW}" "exact-version release PR does not match requested version"
assert_contains "${RELEASE_PLEASE_WORKFLOW}" "release-please did not create the requested"
assert_not_contains "${RELEASE_PLEASE_WORKFLOW}" 'release-as: ${{ steps.release-as.outputs.release_as }}'
node "${RELEASE_PLEASE_EXACT_RUNNER}" --self-test
release_please_lock_version="$(jq -er '.packages["node_modules/release-please"].version' "${RELEASE_PLEASE_LOCK}")"
[[ "${release_please_lock_version}" == "17.11.2" ]] || fail "release-please lockfile version is not pinned to 17.11.2"
release_please_lock_integrity="$(jq -er '.packages["node_modules/release-please"].integrity' "${RELEASE_PLEASE_LOCK}")"
[[ "${release_please_lock_integrity}" == "sha512-we0+NgyHjvczxrilD6aujnRNuASd65Jem0dY5evevxSZvz8BnKnB/QRN3UzGA229fcV09lKCYbqeE9HgkXzGmA==" ]] || {
  fail "release-please lockfile integrity does not match 17.11.2"
}

assert_contains "${RELEASE_WORKFLOW}" "Setup Helm 3 compatibility client"
assert_contains "${RELEASE_WORKFLOW}" 'HELM: ${{ steps.helm4.outputs.helm-path }}'
assert_contains "${RELEASE_WORKFLOW}" 'HELM_INSTALL: ${{ steps.helm3.outputs.helm-path }}'
assert_contains "${RELEASE_WORKFLOW}" "  cleanup-release-state:"
assert_contains "${RELEASE_WORKFLOW}" "    name: Clear Release Pending State"
assert_contains "${RELEASE_WORKFLOW}" "    needs: [prepare, promote]"
assert_contains "${RELEASE_WORKFLOW}" "      pull-requests: write"
assert_contains "${RELEASE_WORKFLOW}" "bash hack/ci/clear-release-please-pending-label.sh"
assert_contains "${RELEASE_WORKFLOW}" "    needs: [prepare, promote, cleanup-release-state]"
preserve_change_metadata_count="$(grep -Fc 'PRESERVE_CHANGE_METADATA=true' "${RELEASE_WORKFLOW}")"
[[ "${preserve_change_metadata_count}" == "2" ]] || {
  fail "release packaging must preserve reviewed chart change metadata in both builds"
}

release_security_images_job="${tmp_dir}/release-security-images.yml"
awk '
  /^  security-images:/ { capture = 1 }
  /^  e2e-matrix:/ { capture = 0 }
  capture
' "${RELEASE_WORKFLOW}" > "${release_security_images_job}"
assert_ordered \
  "${release_security_images_job}" \
  "- name: Checkout" \
  "- name: Install Trivy (safe pinned release)"

assert_contains "${RELEASE_PR_GATE_WORKFLOW}" "pull_request_review:"
assert_contains "${RELEASE_PR_GATE_WORKFLOW}" "      - dismissed"
assert_contains "${RELEASE_PR_GATE_WORKFLOW}" 'head_sha="$(gh api'
assert_contains "${RELEASE_PR_GATE_WORKFLOW}" 'review_state="$(jq -r'
assert_contains "${RELEASE_PR_GATE_WORKFLOW}" 'review_commit="$(jq -r'
assert_not_contains "${RELEASE_PR_GATE_WORKFLOW}" "    paths:"

assert_contains "${POST_RELEASE_VERIFIER}" 'if [[ "${VERSION}" == *-* ]]; then'
assert_contains "${POST_RELEASE_VERIFIER}" 'if [[ "${is_prerelease}" != "${expected_prerelease}" ]]; then'
assert_contains "${POST_RELEASE_VERIFIER}" "github_release_prerelease_flag_verified: true"
assert_contains "${POST_RELEASE_VERIFIER}" "verifying published release-asset checksums"
assert_contains "${POST_RELEASE_VERIFIER}" "verifying published image tags and release signatures"
assert_contains "${POST_RELEASE_VERIFIER}" "hack/ci/verify-image-attestations.sh"
assert_contains "${POST_RELEASE_VERIFIER}" 'helm show chart "${expected_chart_dir}"'
assert_contains "${POST_RELEASE_VERIFIER}" 'helm show chart "${chart_dir}/openbao-operator"'
assert_contains "${POST_RELEASE_VERIFIER}" "published_chart_metadata_matches_tag: true"
assert_contains "${POST_RELEASE_VERIFIER}" "helm_chart_attestation_verified: true"
assert_not_contains "${ROOT_DIR}/.github/workflows/post-release-verification.yml" 'ref: ${{ github.event.workflow_run.head_sha || github.event.inputs.tag }}'
assert_contains "${ROOT_DIR}/.github/workflows/post-release-verification.yml" 'git show "${TAG_HEAD_SHA}:charts/openbao-operator/Chart.yaml"'
assert_contains "${ROOT_DIR}/.github/workflows/post-release-verification.yml" "EXPECTED_CHART_FILE=dist/reviewed-Chart.yaml"
assert_contains "${ROOT_DIR}/.github/workflows/post-release-verification.yml" "Setup Helm"

python3 "${ROOT_DIR}/hack/ci/test-post-release.py"

spdx_fixture="${tmp_dir}/normalizer.spdx.json"
cat > "${spdx_fixture}" <<'EOF'
{
  "SPDXID": "SPDXRef-DOCUMENT",
  "spdxVersion": "SPDX-2.3",
  "creationInfo": {
    "created": "2040-01-01T00:00:00Z",
    "creators": ["Tool: release-automation-test"]
  },
  "dataLicense": "CC0-1.0",
  "documentNamespace": "https://example.test/original",
  "files": [
    {
      "SPDXID": "SPDXRef-File-A",
      "checksums": [{"algorithm": "SHA256", "checksumValue": "aa"}],
      "fileName": "a"
    }
  ],
  "name": "normalizer-test",
  "packages": [],
  "relationships": []
}
EOF
SOURCE_DATE_EPOCH=1700000000 bash "${SPDX_NORMALIZER}" "${spdx_fixture}"
python3 - "${spdx_fixture}" <<'PY'
import json
import sys
from pathlib import Path

document = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
checksum = document["files"][0]["checksums"][0]
if set(checksum) != {"algorithm", "checksumValue"}:
    raise SystemExit(f"normalizer added fields to checksum object: {sorted(checksum)}")
creation_info = document["creationInfo"]
unexpected = {
    "packages",
    "relationships",
    "files",
    "annotations",
    "hasExtractedLicensingInfos",
}.intersection(creation_info)
if unexpected:
    raise SystemExit(f"normalizer added fields to creationInfo: {sorted(unexpected)}")
PY

expect_valid_request "main" "0.5.0-rc.1" "automation/release-as-main-0.5.0-rc.1"
expect_valid_request "release-0.5" "0.5.1" "automation/release-as-release-0.5-0.5.1"
expect_invalid_request "release-as-main-0.5.0" "0.5.0"
expect_invalid_request "release-0.5.1" "0.5.1"
expect_invalid_request "release-0.5" "0.6.0"
expect_invalid_request "main" "0.5"
expect_invalid_request "main" "00.5.0"
expect_invalid_request "main" "0.5.0-rc.01"

release_as="$(
  resolve_release_as \
    "push" \
    "main" \
    "" \
    "chore(main): request release 0.5.0-rc.1 (#619)" \
    "true"
)"
[[ "${release_as}" == "0.5.0-rc.1" ]] || fail "failed to recover the squash-safe Release-As version"

release_as="$(
  resolve_release_as \
    "workflow_dispatch" \
    "release-0.5" \
    "0.5.1" \
    "" \
    "false"
)"
[[ "${release_as}" == "0.5.1" ]] || fail "failed to preserve the dispatch Release-As version"

release_as="$(resolve_release_as "push" "main" "" "fix(release): normal change (#620)" "false")"
[[ -z "${release_as}" ]] || fail "normal pushes must not set a Release-As override"

release_as="$(resolve_release_as "workflow_dispatch" "main" "" "" "false")"
[[ -z "${release_as}" ]] || fail "an empty dispatch override must stay empty"

expect_invalid_release_as_resolution \
  "push" \
  "main" \
  "" \
  "chore(main): request release 0.5.0-rc.1 (#619)" \
  "false"
expect_invalid_release_as_resolution \
  "push" \
  "main" \
  "" \
  "chore(release-0.5): request release 0.5.1 (#619)" \
  "true"
expect_invalid_release_as_resolution \
  "push" \
  "main" \
  "" \
  "chore(main): request release 0.5.0-rc.01 (#619)" \
  "true"

marker_branch="$(env -u GITHUB_OUTPUT TARGET_BRANCH=main VERSION=0.5.0-rc.1 bash "${VALIDATOR}")"
if [[ "${marker_branch}" == release-* ]]; then
  fail "marker branch '${marker_branch}' overlaps the release branch namespace"
fi

for workflow in release-please.yml release-tag.yml; do
  if grep -Fq -- "- 'release-*'" "${ROOT_DIR}/.github/workflows/${workflow}"; then
    fail "${workflow} uses the broad release-* branch trigger"
  fi
done

output_file="${tmp_dir}/github-output"
GITHUB_OUTPUT="${output_file}" TARGET_BRANCH=main VERSION=0.5.0-rc.1 bash "${VALIDATOR}"
assert_contains "${output_file}" "marker_branch=automation/release-as-main-0.5.0-rc.1"

changelog_file="${tmp_dir}/CHANGELOG.md"
cat > "${changelog_file}" <<'EOF'
# Changelog

## [0.5.0](https://example.test/0.5.0) (2026-08-04)

### Bug Fixes

* **release:** finalize stable metadata
* **backup:** add immutable backup evidence

## [0.5.0-rc.2](https://example.test/0.5.0-rc.2) (2026-08-03)

### Features

* **backup:** add immutable backup evidence
* **controller:** add rc2-only reconciliation guard

## [0.5.0-rc.1](https://example.test/0.5.0-rc.1) (2026-08-02)

### Bug Fixes

* **deps:** resolve security vulnerabilities in example dependencies

### Features

* **backup:** add immutable backup evidence

## [0.4.0](https://example.test/0.4.0) (2026-07-01)

### Features

* **legacy:** older release entry
EOF

rc_chart_dir="${tmp_dir}/rc-chart"
mkdir -p "${rc_chart_dir}"
write_chart_fixture "${rc_chart_dir}/Chart.yaml"
CHART_DIR="${rc_chart_dir}" \
  CHANGELOG_FILE="${changelog_file}" \
  CHART_VERSION="0.5.0-rc.1" \
  OWNER="kubebao" \
  bash "${CHART_PREPARER}"

assert_contains "${rc_chart_dir}/Chart.yaml" 'artifacthub.io/prerelease: "true"'
assert_contains "${rc_chart_dir}/Chart.yaml" "artifacthub.io/containsSecurityUpdates: 'true'"
assert_contains "${rc_chart_dir}/Chart.yaml" "kind: security"
assert_contains "${rc_chart_dir}/Chart.yaml" "deps: resolve security vulnerabilities in example dependencies"
assert_contains "${rc_chart_dir}/Chart.yaml" "backup: add immutable backup evidence"
assert_not_contains "${rc_chart_dir}/Chart.yaml" "release: finalize stable metadata"
assert_not_contains "${rc_chart_dir}/Chart.yaml" "controller: add rc2-only reconciliation guard"

stable_chart_dir="${tmp_dir}/stable-chart"
mkdir -p "${stable_chart_dir}"
write_chart_fixture "${stable_chart_dir}/Chart.yaml"
CHART_DIR="${stable_chart_dir}" \
  CHANGELOG_FILE="${changelog_file}" \
  CHART_VERSION="0.5.0" \
  OWNER="kubebao" \
  bash "${CHART_PREPARER}"

stable_chart="${stable_chart_dir}/Chart.yaml"
assert_contains "${stable_chart}" 'artifacthub.io/prerelease: "false"'
assert_contains "${stable_chart}" "artifacthub.io/containsSecurityUpdates: 'true'"
assert_contains "${stable_chart}" "release: finalize stable metadata"
assert_contains "${stable_chart}" "backup: add immutable backup evidence"
assert_contains "${stable_chart}" "controller: add rc2-only reconciliation guard"
assert_contains "${stable_chart}" "deps: resolve security vulnerabilities in example dependencies"
assert_not_contains "${stable_chart}" "legacy: older release entry"

backup_change_count="$(grep -Fc "backup: add immutable backup evidence" "${stable_chart}")"
[[ "${backup_change_count}" == "1" ]] || fail "expected rolled-up changes to be deduplicated"

preserved_chart_dir="${tmp_dir}/preserved-chart"
mkdir -p "${preserved_chart_dir}"
write_chart_fixture "${preserved_chart_dir}/Chart.yaml"
sed -E -i.bak \
  -e "s/artifacthub\.io\/containsSecurityUpdates: 'false'/artifacthub.io\/containsSecurityUpdates: 'true'/" \
  -e 's/description: "stale"/description: "reviewed security metadata"/' \
  "${preserved_chart_dir}/Chart.yaml"
rm -f "${preserved_chart_dir}/Chart.yaml.bak"
PRESERVE_CHANGE_METADATA=true \
  CHART_DIR="${preserved_chart_dir}" \
  CHANGELOG_FILE="${changelog_file}" \
  CHART_VERSION="0.5.0-rc.2" \
  OWNER="kubebao" \
  bash "${CHART_PREPARER}"

preserved_chart="${preserved_chart_dir}/Chart.yaml"
assert_contains "${preserved_chart}" 'artifacthub.io/prerelease: "true"'
assert_contains "${preserved_chart}" "artifacthub.io/containsSecurityUpdates: 'true'"
assert_contains "${preserved_chart}" 'description: "reviewed security metadata"'
assert_contains "${preserved_chart}" "image: ghcr.io/kubebao/openbao-operator:0.5.0-rc.2"
assert_not_contains "${preserved_chart}" "controller: add rc2-only reconciliation guard"

if PRESERVE_CHANGE_METADATA=invalid \
  CHART_DIR="${preserved_chart_dir}" \
  CHANGELOG_FILE="${changelog_file}" \
  CHART_VERSION="0.5.0-rc.2" \
  OWNER="kubebao" \
  bash "${CHART_PREPARER}" >/dev/null 2>&1; then
  fail "invalid PRESERVE_CHANGE_METADATA value was accepted"
fi

echo "release automation tests passed"
