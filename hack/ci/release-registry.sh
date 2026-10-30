#!/usr/bin/env bash
# Read a GHCR manifest. Return 3 only for an authoritative MANIFEST_UNKNOWN.
# Callers must fail on every other error before attempting publication.
read_ghcr_manifest() (
  set -euo pipefail
  local reference="$1" output="$2" repository tag directory token status
  : "${GH_TOKEN:?GH_TOKEN is required}"
  : "${GITHUB_ACTOR:?GITHUB_ACTOR is required}"
  if [[ ! "${reference}" =~ ^ghcr\.io/([a-z0-9._/-]+):([a-zA-Z0-9._-]+)$ ]]; then
    echo "expected a tagged GHCR reference, got ${reference}" >&2
    return 1
  fi
  repository="${BASH_REMATCH[1]}"
  tag="${BASH_REMATCH[2]}"
  directory="$(mktemp -d)" || return 1
  trap 'rm -rf "${directory}"' EXIT
  token="$(curl --fail --silent --show-error --get \
    --user "${GITHUB_ACTOR}:${GH_TOKEN}" \
    --data-urlencode service=ghcr.io --data-urlencode "scope=repository:${repository}:pull" \
    https://ghcr.io/token | jq -er '.token | select(type == "string" and length > 0)')" || return 1
  status="$(curl --silent --show-error --output "${directory}/manifest" --write-out '%{http_code}' \
    --header "Authorization: Bearer ${token}" \
    --header 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
    "https://ghcr.io/v2/${repository}/manifests/${tag}")" || return 1
  case "${status}" in
    200)
      jq -e '.schemaVersion == 2' "${directory}/manifest" >/dev/null || return 1
      cp "${directory}/manifest" "${output}"
      ;;
    404)
      if jq -e '.errors | length > 0 and all(.[]; .code == "MANIFEST_UNKNOWN")' \
        "${directory}/manifest" >/dev/null; then
        return 3
      fi
      echo "${reference}: registry did not confirm a missing manifest" >&2
      return 1
      ;;
    *) echo "${reference}: registry lookup failed (HTTP ${status})" >&2; return 1 ;;
  esac
)
