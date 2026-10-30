#!/usr/bin/env bash
# Read changed file paths from stdin and reject any path a release PR may not change.
#
# A release PR must not change the code, CI, or tests that qualify it. Release
# metadata and documentation content may change; everything else lands in a
# separate reviewed PR first.

set -euo pipefail

allowed='^(\.release-please-manifest\.json|CHANGELOG\.md|charts/openbao-operator/Chart\.yaml|release-notes/[^/]+\.mdx?|website/(content|content-versions|data)/.+|website/hugo\.toml|website/README\.md)$'

count=0
status=0
while IFS= read -r path; do
  [[ -n "${path}" ]] || continue
  count=$((count + 1))
  if [[ ! "${path}" =~ ${allowed} ]]; then
    echo "release PR changes a path outside release metadata and documentation content: ${path}" >&2
    status=1
  fi
done

if (( count == 0 )); then
  echo "release PR file list is empty" >&2
  exit 1
fi

if (( status != 0 )); then
  echo "Land code, CI, test, and docs tooling changes in a separate PR before the release PR." >&2
fi
exit "${status}"
