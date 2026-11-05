#!/usr/bin/env bash
set -euo pipefail

# Build the api-migration release or commit-bound edge binaries. The same source, Go toolchain, and
# version always produce the same bytes, so a tag rebuild can be compared with
# the qualified release candidate.

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <output-dir> <version>" >&2
  exit 1
fi

output_dir="$1"
version="$2"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "${version}" == edge-* ]]; then
  source_commit="$(git -C "${repo_root}" rev-parse HEAD)"
  if [[ "${version}" != "edge-${source_commit:0:12}" ]]; then
    echo "edge version must match source commit edge-${source_commit:0:12}: ${version}" >&2
    exit 1
  fi
elif ! [[ "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]]; then
  echo "version must be a semantic version or edge-<source commit>: ${version}" >&2
  exit 1
fi

mkdir -p "${output_dir}"
output_dir="$(cd "${output_dir}" && pwd)"
cd "${repo_root}"

# A reused output directory must not retain the unsupported Intel Mac payload.
rm -f -- "${output_dir}/api-migration-darwin-amd64"

# Administrators run the command where they run kubectl and helm.
for platform in linux/amd64 linux/arm64 darwin/arm64; do
  os="${platform%/*}"
  arch="${platform#*/}"
  # Pin the microarchitecture levels so the environment cannot change the code.
  CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" GOAMD64=v1 GOARM64=v8.0 \
    go build -mod=vendor -trimpath -buildvcs=false \
    -ldflags="-s -w -buildid= -X main.version=${version}" \
    -o "${output_dir}/api-migration-${os}-${arch}" ./cmd/api-migration
done
