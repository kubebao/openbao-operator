#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

usage() {
  cat >&2 <<'USAGE'
usage: VERSION=X.Y.Z [REPO=kubebao/openbao-operator] hack/ci/verify-post-release.sh

Verifies the post-release invariants that should hold after the Release workflow
has published a stable or prerelease release.

Environment:
  VERSION       Required release version, for example 0.3.0.
  REPO          Current GitHub repository for tags, releases, and PRs. Default: kubebao/openbao-operator.
  PUBLISHER_REPO
                Original artifact publisher for signatures, attestations, and GHCR. Default: ${REPO}.
                Set to dc-tec/openbao-operator for pre-transfer releases, including 0.5.1.
  GIT_REMOTE    Git remote used for branch/tag checks. Default: https://github.com/${REPO}.git.
  ALLOW_DRAFT   Set to 1 to allow a draft GitHub Release. Default: 0.
  EVIDENCE_OUT  Optional path where a JSON verification evidence file is written.
  EXPECTED_CHART_FILE
                 Reviewed Chart.yaml to compare with the published chart.
USAGE
}

fail() {
  echo "error: $*" >&2
  exit 1
}

info() {
  echo "==> $*"
}

require_cmd() {
  local cmd="$1"
  command -v "${cmd}" >/dev/null 2>&1 || fail "required command not found: ${cmd}"
}

VERSION="${VERSION:-${1:-}}"
REPO="${REPO:-kubebao/openbao-operator}"
PUBLISHER_REPO="${PUBLISHER_REPO:-${REPO}}"
OWNER="${PUBLISHER_REPO%%/*}"
GIT_REMOTE="${GIT_REMOTE:-https://github.com/${REPO}.git}"
ALLOW_DRAFT="${ALLOW_DRAFT:-0}"
EVIDENCE_OUT="${EVIDENCE_OUT:-}"
EXPECTED_CHART_FILE="${EXPECTED_CHART_FILE:-${ROOT_DIR}/charts/openbao-operator/Chart.yaml}"
VERIFIED_AT="${VERIFIED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
RELEASE_RUN_ID="${RELEASE_RUN_ID:-}"
VERIFICATION_RUN_ID="${GITHUB_RUN_ID:-}"
VERIFICATION_RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-${REPO}}/actions/runs/${GITHUB_RUN_ID:-}"

if [[ "${VERSION}" == "-h" || "${VERSION}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ -z "${VERSION}" ]]; then
  usage
  exit 2
fi

if ! [[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$ ]]; then
  fail "VERSION must be SemVer, got '${VERSION}'"
fi

for cmd in gh jq git docker cosign helm sha256sum cmp; do
  require_cmd "${cmd}"
done
if [[ ! -f "${EXPECTED_CHART_FILE}" ]]; then
  fail "reviewed Chart.yaml not found: ${EXPECTED_CHART_FILE}"
fi

required_assets=(
  install.yaml
  crds.yaml
  checksums.txt
  checksums.txt.bundle
  checksums.txt.sigstore.json
  checksums.intoto.jsonl
  sbom-openbao-operator.spdx.json
  sbom-openbao-init.spdx.json
  sbom-openbao-backup.spdx.json
  sbom-openbao-upgrade.spdx.json
  provenance-index.json
)

info "checking remote tag ${VERSION}"
tag_refs="$(git ls-remote --tags "${GIT_REMOTE}" "refs/tags/${VERSION}" "refs/tags/${VERSION}^{}")" ||
  fail "release tag ${VERSION} was not found on ${GIT_REMOTE}"
tag_head_sha="$(awk '$2 ~ /\^\{\}$/ {print $1; exit}' <<<"${tag_refs}")"
if [[ -z "${tag_head_sha}" ]]; then
  tag_head_sha="$(awk -v ref="refs/tags/${VERSION}" '$2 == ref {print $1; exit}' <<<"${tag_refs}")"
fi
if [[ -z "${tag_head_sha}" ]]; then
  fail "could not resolve release tag ${VERSION} target commit from ${GIT_REMOTE}"
fi

info "checking GitHub Release ${VERSION}"
release_json="$(
  gh release view "${VERSION}" \
    --repo "${REPO}" \
    --json assets,isDraft,isPrerelease,tagName,url
)"

release_tag="$(jq -r '.tagName' <<<"${release_json}")"
if [[ "${release_tag}" != "${VERSION}" ]]; then
  fail "GitHub Release tagName is '${release_tag}', expected '${VERSION}'"
fi

is_draft="$(jq -r '.isDraft' <<<"${release_json}")"
if [[ "${is_draft}" == "true" && "${ALLOW_DRAFT}" != "1" ]]; then
  fail "GitHub Release ${VERSION} is still a draft"
fi

expected_prerelease="false"
if [[ "${VERSION}" == *-* ]]; then
  expected_prerelease="true"
fi
is_prerelease="$(jq -r '.isPrerelease' <<<"${release_json}")"
if [[ "${is_prerelease}" != "${expected_prerelease}" ]]; then
  fail "GitHub Release ${VERSION} prerelease flag is '${is_prerelease}', expected '${expected_prerelease}'"
fi

mapfile -t asset_names < <(jq -r '.assets[].name' <<<"${release_json}" | LC_ALL=C sort)
missing_assets=()
for asset in "${required_assets[@]}"; do
  if ! printf '%s\n' "${asset_names[@]}" | grep -Fxq "${asset}"; then
    missing_assets+=("${asset}")
  fi
done
if (( ${#missing_assets[@]} > 0 )); then
  printf 'missing release assets:\n' >&2
  printf '  - %s\n' "${missing_assets[@]}" >&2
  exit 1
fi

release_url="$(jq -r '.url' <<<"${release_json}")"
info "release assets present: ${release_url}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

info "downloading published release assets"
gh release download "${VERSION}" \
  --repo "${REPO}" \
  --dir "${tmpdir}" \
  --clobber

info "verifying published release-asset checksums"
(
  cd "${tmpdir}"
  sha256sum -c checksums.txt
)

identity="https://github.com/${PUBLISHER_REPO}/.github/workflows/release.yml@refs/tags/${VERSION}"

info "verifying checksums signature"
cosign verify-blob \
  --new-bundle-format=true \
  --bundle "${tmpdir}/checksums.txt.bundle" \
  --certificate-identity "${identity}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${tmpdir}/checksums.txt" >/dev/null

provenance_repo="$(jq -r '.release.repository' "${tmpdir}/provenance-index.json")"
if [[ "${provenance_repo}" != "${PUBLISHER_REPO}" ]]; then
  fail "provenance repository is '${provenance_repo}', expected publisher '${PUBLISHER_REPO}'"
fi
provenance_owner="$(jq -r '.release.owner' "${tmpdir}/provenance-index.json")"
if [[ "${provenance_owner}" != "${OWNER}" ]]; then
  fail "provenance owner is '${provenance_owner}', expected '${OWNER}'"
fi

provenance_tag="$(jq -r '.release.tag' "${tmpdir}/provenance-index.json")"
if [[ "${provenance_tag}" != "${VERSION}" ]]; then
  fail "provenance-index.json release tag is '${provenance_tag}', expected '${VERSION}'"
fi

source_ref="refs/tags/${VERSION}"
provenance_source_ref="$(jq -r '.release.source_ref' "${tmpdir}/provenance-index.json")"
if [[ "${provenance_source_ref}" != "${source_ref}" ]]; then
  fail "provenance-index.json source ref is '${provenance_source_ref}', expected '${source_ref}'"
fi

checksums_digest="sha256:$(sha256sum "${tmpdir}/checksums.txt" | awk '{print $1}')"
provenance_checksums_digest="$(jq -r '.release_artifacts.checksums_txt.digest' "${tmpdir}/provenance-index.json")"
if [[ "${provenance_checksums_digest}" != "${checksums_digest}" ]]; then
  fail "provenance-index.json checksums digest does not match the published checksums.txt"
fi

image_count="$(jq '.images | length' "${tmpdir}/provenance-index.json")"
if [[ "${image_count}" != "4" ]]; then
  fail "provenance-index.json must contain exactly four release images, found ${image_count}"
fi

image_value() {
  local name="$1"
  local field="$2"
  jq -er --arg name "${name}" --arg field "${field}" \
    '[.images[] | select(.name == $name)]
     | if length == 1 then .[0][$field] else error("expected one image entry for " + $name) end' \
    "${tmpdir}/provenance-index.json"
}

manager_image="$(image_value openbao-operator ref)"
manager_digest="$(image_value openbao-operator digest)"
config_init_image="$(image_value openbao-init ref)"
config_init_digest="$(image_value openbao-init digest)"
backup_executor_image="$(image_value openbao-backup ref)"
backup_executor_digest="$(image_value openbao-backup digest)"
upgrade_executor_image="$(image_value openbao-upgrade ref)"
upgrade_executor_digest="$(image_value openbao-upgrade digest)"

expected_image_refs=(
  "openbao-operator=ghcr.io/${OWNER}/openbao-operator"
  "openbao-init=ghcr.io/${OWNER}/openbao-init"
  "openbao-backup=ghcr.io/${OWNER}/openbao-backup"
  "openbao-upgrade=ghcr.io/${OWNER}/openbao-upgrade"
)
for expected in "${expected_image_refs[@]}"; do
  image_name="${expected%%=*}"
  expected_ref="${expected#*=}"
  actual_ref="$(image_value "${image_name}" ref)"
  if [[ "${actual_ref}" != "${expected_ref}" ]]; then
    fail "provenance image ${image_name} ref is '${actual_ref}', expected '${expected_ref}'"
  fi
done

attestation_signer_workflow="$(jq -er '.identity_constraints.reusable_build_signer_workflow' "${tmpdir}/provenance-index.json")"
info "verifying published image attestations"
REPO="${PUBLISHER_REPO}" \
  VERSION="${VERSION}" \
  SOURCE_REF="${source_ref}" \
  SIGNER_WORKFLOW="${attestation_signer_workflow}" \
  MANAGER_IMAGE="${manager_image}" \
  MANAGER_DIGEST="${manager_digest}" \
  CONFIG_INIT_IMAGE="${config_init_image}" \
  CONFIG_INIT_DIGEST="${config_init_digest}" \
  BACKUP_EXECUTOR_IMAGE="${backup_executor_image}" \
  BACKUP_EXECUTOR_DIGEST="${backup_executor_digest}" \
  UPGRADE_EXECUTOR_IMAGE="${upgrade_executor_image}" \
  UPGRADE_EXECUTOR_DIGEST="${upgrade_executor_digest}" \
  bash "${ROOT_DIR}/hack/ci/verify-image-attestations.sh"

info "verifying published image tags and release signatures"
while IFS= read -r image_entry; do
  image_name="$(jq -r '.name' <<<"${image_entry}")"
  image_ref="$(jq -r '.ref' <<<"${image_entry}")"
  image_digest="$(jq -r '.digest' <<<"${image_entry}")"
  if ! [[ "${image_digest}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    fail "provenance image ${image_name} has invalid digest '${image_digest}'"
  fi

  published_digest="$(
    docker buildx imagetools inspect "${image_ref}:${VERSION}" \
      --format '{{json .Manifest.Digest}}' | tr -d '"'
  )"
  if [[ "${published_digest}" != "${image_digest}" ]]; then
    fail "published image tag ${image_ref}:${VERSION} resolves to ${published_digest}, expected ${image_digest}"
  fi

  cosign verify \
    --new-bundle-format=true \
    --certificate-identity "${identity}" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    "${image_ref}@${image_digest}" >/dev/null
done < <(jq -c '.images[]' "${tmpdir}/provenance-index.json")

chart_ref="ghcr.io/${OWNER}/charts/openbao-operator:${VERSION}"
info "checking Helm chart publication: ${chart_ref}"
chart_digest="$(
  docker buildx imagetools inspect "${chart_ref}" --format '{{json .Manifest.Digest}}' | tr -d '"'
)"
if [[ -z "${chart_digest}" || "${chart_digest}" == "null" ]]; then
  fail "could not resolve chart digest for ${chart_ref}"
fi

provenance_chart_digest="$(jq -r '.chart.digest' "${tmpdir}/provenance-index.json")"
if [[ "${provenance_chart_digest}" != "${chart_digest}" ]]; then
  fail "provenance chart digest is ${provenance_chart_digest}, published chart digest is ${chart_digest}"
fi

info "checking published Helm chart metadata"
chart_dir="${tmpdir}/chart"
expected_chart_dir="${tmpdir}/expected-chart"
mkdir -p "${chart_dir}"
mkdir -p "${expected_chart_dir}"
helm pull "oci://ghcr.io/${OWNER}/charts/openbao-operator" \
  --version "${VERSION}" \
  --untar \
  --untardir "${chart_dir}" >/dev/null
cp "${EXPECTED_CHART_FILE}" "${expected_chart_dir}/Chart.yaml"
helm show chart "${expected_chart_dir}" > "${tmpdir}/expected-chart-metadata.yaml"
helm show chart "${chart_dir}/openbao-operator" > "${tmpdir}/published-chart-metadata.yaml"
if ! cmp -s "${tmpdir}/expected-chart-metadata.yaml" "${tmpdir}/published-chart-metadata.yaml"; then
  fail "published Helm chart metadata differs from the reviewed ${VERSION} Chart.yaml"
fi

info "verifying Helm chart signature"
cosign verify \
  --new-bundle-format=true \
  --certificate-identity "${identity}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "ghcr.io/${OWNER}/charts/openbao-operator@${chart_digest}" >/dev/null

info "verifying chart and checksums attestations"
REPO="${PUBLISHER_REPO}" \
  OWNER="${OWNER}" \
  VERSION="${VERSION}" \
  SOURCE_REF="${source_ref}" \
  CHECKSUMS_PATH="${tmpdir}/checksums.txt" \
  CHECKSUMS_ATTESTATION_BUNDLE="${tmpdir}/checksums.intoto.jsonl" \
  CHART_DIGEST="${chart_digest}" \
  VERIFY_CHART=true \
  bash "${ROOT_DIR}/hack/ci/verify-release-artifact-attestations.sh"

info "checking for open release-please PRs"
open_release_prs="$(
  gh pr list \
    --repo "${REPO}" \
    --state open \
    --json number,title,headRefName,url \
    --jq '.[] | select(.headRefName | startswith("release-please--branches--")) | "#\(.number) \(.title) [\(.headRefName)] \(.url)"'
)"
if [[ -n "${open_release_prs}" ]]; then
  echo "${open_release_prs}" >&2
  fail "unexpected open release-please PRs remain"
fi

info "checking for stale release-please branches"
stale_branches="$(git ls-remote --heads "${GIT_REMOTE}" 'release-please--branches--*')"
if [[ -n "${stale_branches}" ]]; then
  echo "${stale_branches}" >&2
  fail "stale release-please branches remain"
fi

info "checking release-please pending label cleanup"
release_pr_candidates="$(
  gh pr list \
    --repo "${REPO}" \
    --state merged \
    --search "${VERSION} in:title" \
    --limit 50 \
    --json number,title,url,mergeCommit,labels
)"
release_pr_json="$(
  jq \
    --arg version "${VERSION}" \
    --arg tag_head_sha "${tag_head_sha}" \
    '[.[] | select((.title | startswith("chore(")) and (.title | endswith("): release " + $version)))]
     | if ($tag_head_sha | length) > 0 then
         (map(select((.mergeCommit.oid // "") == $tag_head_sha)) as $exact
           | if ($exact | length) == 1 then $exact else . end)
       else
         .
       end' \
    <<<"${release_pr_candidates}"
)"
release_pr_match_count="$(jq 'length' <<<"${release_pr_json}")"
if [[ "${release_pr_match_count}" != "1" ]]; then
  echo "expected exactly one merged release PR for ${VERSION}, found ${release_pr_match_count}" >&2
  jq -r '.[] | "- #\(.number) \(.title) (\(.url))"' <<<"${release_pr_candidates}" >&2 || true
  exit 1
fi

release_pr_number="$(jq -r '.[0].number' <<<"${release_pr_json}")"
release_pr_url="$(jq -r '.[0].url' <<<"${release_pr_json}")"
pending_release_labels="$(
  jq -r '.[]?.labels[]?.name | select(. == "autorelease: pending" or . == "autorelease:pending")' \
    <<<"${release_pr_json}"
)"
if [[ -n "${pending_release_labels}" ]]; then
  echo "${pending_release_labels}" >&2
  fail "release PR ${release_pr_url} still has release-please pending label"
fi

if [[ -n "${EVIDENCE_OUT}" ]]; then
  info "writing post-release verification evidence: ${EVIDENCE_OUT}"
  mkdir -p "$(dirname "${EVIDENCE_OUT}")"
  assets_file="${tmpdir}/release-assets.txt"
  printf '%s\n' "${asset_names[@]}" > "${assets_file}"
  jq -n \
    --arg schema_version "1" \
    --arg repo "${REPO}" \
    --arg publisher_repo "${PUBLISHER_REPO}" \
    --arg version "${VERSION}" \
    --arg verified_at "${VERIFIED_AT}" \
    --arg release_run_id "${RELEASE_RUN_ID}" \
    --arg verification_run_id "${VERIFICATION_RUN_ID}" \
    --arg verification_run_url "${VERIFICATION_RUN_URL}" \
    --arg release_url "${release_url}" \
    --arg release_pr_number "${release_pr_number}" \
    --arg release_pr_url "${release_pr_url}" \
    --arg identity "${identity}" \
    --arg chart_ref "${chart_ref}" \
    --arg chart_digest "${chart_digest}" \
    --slurpfile release <(printf '%s\n' "${release_json}") \
    --slurpfile provenance "${tmpdir}/provenance-index.json" \
    --rawfile assets "${assets_file}" \
    'def optional_string($value):
      if ($value | length) > 0 then $value else null end;

    {
      schema_version: $schema_version,
      repository: $repo,
      publisher_repository: $publisher_repo,
      version: $version,
      verified_at: $verified_at,
      release_run: {
        id: optional_string($release_run_id)
      },
      verification_run: {
        id: optional_string($verification_run_id),
        url: (if ($verification_run_id | length) > 0 then $verification_run_url else null end)
      },
      release: {
        url: $release_url,
        tag: $release[0].tagName,
        draft: $release[0].isDraft,
        prerelease: $release[0].isPrerelease,
        assets: ($assets | split("\n") | map(select(length > 0)))
      },
      release_pr: {
        number: ($release_pr_number | tonumber),
        url: $release_pr_url
      },
      identity_constraints: {
        certificate_identity: $identity,
        certificate_oidc_issuer: "https://token.actions.githubusercontent.com"
      },
      chart: {
        ref: $chart_ref,
        digest: $chart_digest
      },
      published_provenance: $provenance[0],
      checks: {
        remote_tag_present: true,
        github_release_published: true,
        github_release_prerelease_flag_verified: true,
        required_assets_present: true,
        release_asset_checksums_verified: true,
        checksums_signature_verified: true,
        checksums_attestation_verified: true,
        image_tags_match_provenance: true,
        image_signatures_verified: true,
        image_attestations_verified: true,
        helm_chart_published: true,
        published_chart_metadata_matches_tag: true,
        helm_chart_signature_verified: true,
        helm_chart_attestation_verified: true,
        no_open_release_please_prs: true,
        no_stale_release_please_branches: true,
        release_please_pending_label_cleared: true
      }
    }' > "${EVIDENCE_OUT}"
fi

info "post-release verification passed for ${VERSION}"
