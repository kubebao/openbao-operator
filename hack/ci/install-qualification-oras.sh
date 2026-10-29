#!/usr/bin/env bash
set -euo pipefail

[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["orasVersion"])' "${root}/hack/ci/release-qualification-inputs.json")"
expected="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["orasLinuxAMD64SHA256"])' "${root}/hack/ci/release-qualification-inputs.json")"
[[ "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && "${expected}" =~ ^[0-9a-f]{64}$ ]]
directory="$(mktemp -d "${RUNNER_TEMP:?}/qualification-oras.XXXXXX")"
curl --fail --silent --show-error --location --retry 3 \
  "https://github.com/oras-project/oras/releases/download/v${version}/oras_${version}_linux_amd64.tar.gz" \
  --output "${directory}/oras.tar.gz"
printf '%s  %s\n' "${expected}" "${directory}/oras.tar.gz" | sha256sum --check --status
tar -xzf "${directory}/oras.tar.gz" -C "${directory}" oras
chmod +x "${directory}/oras"
echo "${directory}" >>"${GITHUB_PATH:?}"
