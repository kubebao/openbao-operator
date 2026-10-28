#!/usr/bin/env bash
set -euo pipefail

# The identity experiment uses this checksum-pinned verifier on GitHub-hosted Linux AMD64.
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]
target="${RUNNER_TEMP:?}/identity-pilot-gh"
mkdir "${target}"
curl --fail --silent --show-error --location \
  https://github.com/cli/cli/releases/download/v2.96.0/gh_2.96.0_linux_amd64.tar.gz \
  -o "${target}/gh.tar.gz"
echo "83d5c2ccad5498f58bf6368acb1ab32588cf43ab3a4b1c301bf36328b1c8bd60  ${target}/gh.tar.gz" | sha256sum --check
tar -xzf "${target}/gh.tar.gz" -C "${target}" gh_2.96.0_linux_amd64/bin/gh
echo "${target}/gh_2.96.0_linux_amd64/bin" >> "${GITHUB_PATH:?}"
