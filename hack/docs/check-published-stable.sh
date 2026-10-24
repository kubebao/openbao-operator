#!/usr/bin/env bash
set -euo pipefail

# Release preparation changes the stable documentation tree before publication.
# Keep the existing public site until that exact stable release is published.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${1:-${ROOT_DIR}/website/hugo.toml}"
versions="${2:-${ROOT_DIR}/website/data/version_lines.yaml}"
repo="${GITHUB_REPOSITORY:-kubebao/openbao-operator}"

[[ $# -le 2 && "${repo}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || {
  echo 'Invalid arguments or GitHub repository.' >&2
  exit 1
}
line="$(sed -n 's/^defaultContentVersion = "\([^"]*\)"$/\1/p' "${config}")"
[[ "${line}" =~ ^[0-9]+\.[0-9]+\.x$ ]] || {
  echo 'The default documentation line must be a stable minor line.' >&2
  exit 1
}
version_field() {
  awk -v section="\"${line}\":" -v field="$1:" '
    $0 == section { in_section = 1; next }
    in_section && /^[^[:space:]]/ { exit }
    in_section && $1 == field {
      value = $2
      gsub(/^"|"$/, "", value)
      print value
      exit
    }
  ' "${versions}"
}
release="$(version_field release)"
source_ref="$(version_field sourceRef)"
[[ "${release}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && "${release%.*}.x" == "${line}" && "${source_ref}" == "${release}" ]] || {
  echo 'The stable documentation release and source ref must match its minor line.' >&2
  exit 1
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
published=false
if gh api "repos/${repo}/releases/tags/${release}" > "${tmp_dir}/release.json" 2> "${tmp_dir}/error"; then
  jq -e --arg release "${release}" '
    .tag_name == $release and (.draft | type == "boolean") and (.prerelease | type == "boolean")
  ' "${tmp_dir}/release.json" >/dev/null || {
    echo 'GitHub returned an invalid or mismatched release.' >&2
    exit 1
  }
  if jq -e '.draft == false and .prerelease == false' "${tmp_dir}/release.json" >/dev/null; then
    jq -e '.published_at | type == "string" and length > 0' "${tmp_dir}/release.json" >/dev/null || {
      echo 'GitHub returned a stable release without a publication timestamp.' >&2
      exit 1
    }
    published=true
  fi
elif ! jq -e '.status == "404" or .status == 404' "${tmp_dir}/release.json" >/dev/null 2>&1; then
  cat "${tmp_dir}/error" >&2
  echo 'Cannot determine whether the stable documentation release is published.' >&2
  exit 1
fi

printf 'Stable documentation release %s: published=%s\n' "${release}" "${published}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf 'published=%s\nrelease_version=%s\n' "${published}" "${release}" >> "${GITHUB_OUTPUT}"
fi
