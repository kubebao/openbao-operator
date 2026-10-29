#!/usr/bin/env bash
set -euo pipefail

: "${SOURCE_DIR:?}"
: "${IMAGE_DIR:?}"
: "${OUTPUT_DIR:?}"
: "${SYFT_BIN:?}"
: "${RELEASE_VERSION:?}"
: "${SOURCE_DATE_EPOCH:?}"
: "${RELEASE_HELM_VERSION:?}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="${RELEASE_VERSION}"
epoch="${SOURCE_DATE_EPOCH}"
helm_version="${RELEASE_HELM_VERSION}"

if [[ -e "${SOURCE_DIR}/dist" ]]; then
  echo "Packaging requires a fresh source checkout without dist/." >&2
  exit 1
fi
mkdir -p "${OUTPUT_DIR}"

current_helm="$(helm version --short)"
if [[ "${current_helm}" != "${helm_version}" && "${current_helm}" != "${helm_version}"+* ]]; then
  echo "Helm differs from frozen inputs." >&2
  exit 1
fi

cd "${SOURCE_DIR}"
export GOFLAGS=-mod=vendor
make kustomize controller-gen
manager_digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["digest"])' "${IMAGE_DIR}/operator.json")"
make build-installer build-crds build-policy-approvals \
  IMG="ghcr.io/kubebao/openbao-operator@${manager_digest}" OPERATOR_VERSION="${version}"
for name in install.yaml crds.yaml operator-policy-approval.hcl operator-policy-approval-with-backup.hcl; do
  cp "dist/${name}" "${OUTPUT_DIR}/${name}"
done

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
cp -a charts/openbao-operator "${work_dir}/openbao-operator"
CHART_DIR="${work_dir}/openbao-operator" CHART_VERSION="${version}" OWNER=kubebao PRESERVE_CHANGE_METADATA=true \
  bash "${root}/hack/ci/prepare-release-chart.sh"
python3 - "${work_dir}/openbao-operator" "${epoch}" <<'PY'
import os
from pathlib import Path
import sys

chart, epoch = Path(sys.argv[1]), int(sys.argv[2])
for path in [*chart.rglob("*"), chart]:
    os.utime(path, (epoch, epoch), follow_symlinks=False)
PY
helm lint "${work_dir}/openbao-operator"
helm package "${work_dir}/openbao-operator" -d "${OUTPUT_DIR}"

export SYFT_CHECK_FOR_APP_UPDATE=false
for image in operator init backup upgrade; do
  digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["digest"])' "${IMAGE_DIR}/${image}.json")"
  "${SYFT_BIN}" scan "oci-archive:${IMAGE_DIR}/${image}.tar" --platform linux/amd64 \
    --source-name "ghcr.io/kubebao/openbao-${image}" --source-version "${digest}" \
    -o "spdx-json=${OUTPUT_DIR}/sbom-openbao-${image}.spdx.json"
done
SOURCE_DATE_EPOCH="${epoch}" bash "${root}/hack/ci/normalize-spdx-json.sh" "${OUTPUT_DIR}"/sbom-*.spdx.json
(
  cd "${OUTPUT_DIR}"
  sha256sum install.yaml crds.yaml operator-policy-approval.hcl operator-policy-approval-with-backup.hcl \
    sbom-openbao-backup.spdx.json sbom-openbao-init.spdx.json sbom-openbao-operator.spdx.json sbom-openbao-upgrade.spdx.json > checksums.txt
)
