#!/usr/bin/env bash
set -euo pipefail

bundle_dir="${1:?binary output directory is required}"
source_sha="${2:?tested source SHA is required}"
if [[ "$(git rev-parse HEAD)" != "${source_sha}" ]]; then
  echo "E2E binary source SHA does not match the checkout." >&2
  exit 1
fi
if [[ -n "$(GIT_OPTIONAL_LOCKS=0 git status --porcelain=v1)" ]]; then
  echo "Build E2E binaries from a clean checkout so the source SHA identifies the compiled code." >&2
  exit 1
fi
mkdir -p "${bundle_dir}"
bundle_dir="$(cd "${bundle_dir}" && pwd)"
rm -f "${bundle_dir}/metadata.json"

export GOFLAGS="${GOFLAGS:--mod=vendor}"
go test -c -tags=e2e -o "${bundle_dir}/e2e.test" ./test/e2e
go build -o "${bundle_dir}/ginkgo" github.com/onsi/ginkgo/v2/ginkgo
go build -o "${bundle_dir}/e2e_report" ./hack/tools/e2e_report

E2E_BUILD_GOOS="$(go env GOOS)" E2E_BUILD_GOARCH="$(go env GOARCH)" \
E2E_BUILD_GOVERSION="$(go env GOVERSION)" E2E_BUILD_CGO="$(go env CGO_ENABLED)" \
  python3 hack/ci/e2e-binaries.py record "${bundle_dir}" "${source_sha}"
