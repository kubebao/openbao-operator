#!/usr/bin/env bash
set -euo pipefail

: "${KIND_CLUSTER:?cluster name is required}"
: "${KUBECONFIG:?use the dedicated lab kubeconfig}"
network="${KIND_CLUSTER}-network"
expected="${E2E_NETWORK_OWNER:-}"
if [[ -z "${expected}" && -f "${KUBECONFIG}.owner" ]]; then
  expected="$(cat "${KUBECONFIG}.owner")"
fi
cluster="$(docker network inspect "${network}" --format '{{ index .Labels "openbao.test.cluster" }}' 2>/dev/null || true)"
owner="$(docker network inspect "${network}" --format '{{ index .Labels "openbao.test.owner" }}' 2>/dev/null || true)"
if [[ -z "${expected}" || "${cluster}" != "${KIND_CLUSTER}" || "${owner}" != "${expected}" ]]; then
  echo "No task-owned network found for ${KIND_CLUSTER}; leaving existing resources unchanged."
  exit 0
fi
kind delete cluster --name "${KIND_CLUSTER}"
docker network rm "${network}"
rm -f "${KUBECONFIG}.owner"
