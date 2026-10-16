#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
export SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export VERSION=edge-aaaaaaaaaaaa
export CHART_VERSION=0.0.0-edge.123.1.gaaaaaaaaaaaa
export SOURCE_DATE_EPOCH=1788912000
export MANAGER_IMAGE=ghcr.io/kubebao/openbao-operator
export CONFIG_INIT_IMAGE=ghcr.io/kubebao/openbao-init
export BACKUP_EXECUTOR_IMAGE=ghcr.io/kubebao/openbao-backup
export UPGRADE_EXECUTOR_IMAGE=ghcr.io/kubebao/openbao-upgrade
export MANAGER_DIGEST="sha256:$(printf '%064d' 1)"
export CONFIG_INIT_DIGEST="sha256:$(printf '%064d' 2)"
export BACKUP_EXECUTOR_DIGEST="sha256:$(printf '%064d' 3)"
export UPGRADE_EXECUTOR_DIGEST="sha256:$(printf '%064d' 4)"

GOFLAGS=-mod=vendor go test ./hack/tools/edge_chart
GOFLAGS=-mod=vendor go build -o "${work_dir}/prepare" ./hack/tools/edge_chart
for pass in primary rebuild; do
  mkdir -p "${work_dir}/${pass}"
  cp -R charts/openbao-operator "${work_dir}/${pass}/openbao-operator"
  CHART_DIR="${work_dir}/${pass}/openbao-operator" "${work_dir}/prepare"
  python3 - "${work_dir}/${pass}/openbao-operator" <<'PY'
import os
import sys
from pathlib import Path
for path in Path(sys.argv[1]).rglob('*'):
    os.utime(path, (int(os.environ['SOURCE_DATE_EPOCH']),) * 2)
PY
  helm lint "${work_dir}/${pass}/openbao-operator"
  helm package "${work_dir}/${pass}/openbao-operator" --destination "${work_dir}/${pass}"
done
package="${work_dir}/primary/openbao-operator-${CHART_VERSION}.tgz"
cmp "${package}" "${work_dir}/rebuild/openbao-operator-${CHART_VERSION}.tgz"
for mode in multi single; do
  helm template openbao-operator "${package}" --namespace openbao-operator-system \
    --include-crds --set "tenancy.mode=${mode}" \
    --set 'controller.extraEnv[0].name=EXTRA_SETTING' --set-string 'controller.extraEnv[0].value=retained' > "${work_dir}/render.yaml"
  for prefix in MANAGER CONFIG_INIT BACKUP_EXECUTOR UPGRADE_EXECUTOR; do
    image_var="${prefix}_IMAGE"
    digest_var="${prefix}_DIGEST"
    grep -Fq "${!image_var}@${!digest_var}" "${work_dir}/render.yaml"
  done
  grep -q 'name: EXTRA_SETTING' "${work_dir}/render.yaml"
  grep -q 'name: OPERATOR_VERSION' "${work_dir}/render.yaml"
  grep -q 'name: openbaoclusters.openbao.org' "${work_dir}/render.yaml"
done
# Older releases have no helperImages object in their saved chart defaults.
helm template openbao-operator charts/openbao-operator --namespace openbao-operator-system \
  --set helperImages=null > "${work_dir}/legacy-values.yaml"
if grep -q 'name: OPERATOR_INIT_IMAGE' "${work_dir}/legacy-values.yaml"; then
  echo 'legacy helper defaults unexpectedly acquired an override' >&2
  exit 1
fi
bash hack/ci/test-publish-edge-chart.sh "${package}"
echo 'Edge chart packaging and publication tests passed.'
