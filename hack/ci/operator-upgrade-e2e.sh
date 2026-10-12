#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/test/fixtures/operator-upgrade"
MIGRATION_FIXTURE_DIR="${ROOT_DIR}/test/fixtures/api-migration"
# The API stability inventory retains its original contract baseline. Runtime
# upgrade qualification follows the previous stable operator release instead.
# shellcheck source=../../test/fixtures/operator-upgrade/versions.env
source "${FIXTURE_DIR}/versions.env"

FROM_VERSION="${OPERATOR_UPGRADE_E2E_FROM_VERSION:-${OPERATOR_UPGRADE_BASELINE}}"
MIGRATION_FROM_VERSION="${OPERATOR_UPGRADE_MIGRATION_BASELINE}"
TARGET_RELEASE="${OPERATOR_UPGRADE_E2E_TARGET_RELEASE:-${OPERATOR_UPGRADE_TARGET_RELEASE}}"
TARGET_VERSION="${OPERATOR_UPGRADE_E2E_TARGET_VERSION:-${TARGET_RELEASE}-e2e}"
VERIFY_ONLY="${OPERATOR_UPGRADE_E2E_VERIFY_ONLY:-false}"

KIND_BIN="${KIND:-kind}"
KUBECTL_BIN="${KUBECTL:-kubectl}"
HELM_BIN="${HELM:-helm}"
HELM_INSTALL_BIN="${HELM_INSTALL:-${HELM_BIN}}"
DOCKER_BIN="${DOCKER:-docker}"
CLUSTER_NAME="${OPERATOR_UPGRADE_E2E_KIND_CLUSTER:-openbao-operator-upgrade-e2e}"
KUBERNETES_VERSION="${OPERATOR_UPGRADE_E2E_KUBERNETES_VERSION:-}"
if [[ -z "${KUBERNETES_VERSION}" ]]; then
  KUBERNETES_VERSION="$(
    sed -nE 's/^[[:space:]]*primary:[[:space:]]*"?([^"[:space:]]+)"?.*$/\1/p' \
      "${ROOT_DIR}/test/e2e/suites.yaml" | head -n1
  )"
fi
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v${KUBERNETES_VERSION}}"
KEEP_CLUSTER="${OPERATOR_UPGRADE_E2E_KEEP_CLUSTER:-false}"
REGISTRY_NAME="${OPERATOR_UPGRADE_E2E_REGISTRY_NAME:-${CLUSTER_NAME}-registry}"
REGISTRY_IMAGE="${OPERATOR_UPGRADE_E2E_REGISTRY_IMAGE:-registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373}"

MANAGER_SOURCE="${OPERATOR_UPGRADE_E2E_MANAGER_SOURCE:-}"
INIT_SOURCE="${OPERATOR_UPGRADE_E2E_INIT_SOURCE:-}"
LOCAL_MANAGER_IMAGE="operator-upgrade.local/openbao-operator:${TARGET_VERSION}"
LOCAL_INIT_IMAGE="operator-upgrade.local/openbao-init:${TARGET_VERSION}"

OPERATOR_NAMESPACE="openbao-operator-system"
TENANT_NAMESPACE="openbao-upgrade-e2e"
CLUSTER_RESOURCE="openbaocluster/operator-upgrade"
PVC_RESOURCE="persistentvolumeclaim/data-operator-upgrade-0"
TRANSIT_CLUSTER_RESOURCE="openbaocluster/operator-upgrade-transit"
HARDENED_CLUSTER_RESOURCE="openbaocluster/operator-upgrade-hardened"
# The source release and its signed helper stay at their historical coordinates.
HARDENED_INIT_IMAGE_REPOSITORY="ghcr.io/dc-tec/openbao-init"
DEFAULT_HARDENED_INIT_IMAGE="${OPERATOR_UPGRADE_BASELINE_INIT_IMAGE}"
HARDENED_INIT_IMAGE="${OPERATOR_UPGRADE_E2E_HARDENED_INIT_IMAGE:-${DEFAULT_HARDENED_INIT_IMAGE}}"

require_command() {
  local command_name="$1"
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "${command_name} is required" >&2
    exit 1
  fi
}

verify_hardened_init_image() {
  local baseline_ref="${HARDENED_INIT_IMAGE_REPOSITORY}:${FROM_VERSION}"
  local baseline_digest=""
  local expected_image=""

  if [[ -n "${OPERATOR_UPGRADE_E2E_HARDENED_INIT_IMAGE:-}" ]]; then
    if ! "${DOCKER_BIN}" buildx imagetools inspect "${HARDENED_INIT_IMAGE}" >/dev/null; then
      echo "hardened upgrade helper does not resolve: ${HARDENED_INIT_IMAGE}" >&2
      return 1
    fi
    echo "Verified overridden Hardened upgrade helper: ${HARDENED_INIT_IMAGE}"
    return 0
  fi

  if ! baseline_digest="$(
    "${DOCKER_BIN}" buildx imagetools inspect "${baseline_ref}" \
      --format '{{json .Manifest.Digest}}' | tr -d '"'
  )"; then
    echo "failed to resolve baseline Hardened upgrade helper: ${baseline_ref}" >&2
    return 1
  fi
  if ! [[ "${baseline_digest}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "baseline Hardened upgrade helper returned an invalid digest: ${baseline_digest}" >&2
    return 1
  fi

  expected_image="${HARDENED_INIT_IMAGE_REPOSITORY}@${baseline_digest}"
  if [[ "${DEFAULT_HARDENED_INIT_IMAGE}" != "${expected_image}" ]]; then
    echo "default Hardened upgrade helper does not match ${baseline_ref}" >&2
    echo "expected ${expected_image}, got ${DEFAULT_HARDENED_INIT_IMAGE}" >&2
    return 1
  fi

  echo "Verified baseline Hardened upgrade helper: ${DEFAULT_HARDENED_INIT_IMAGE}"
}

assert_equal() {
  local description="$1"
  local expected="$2"
  local actual="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "${description}: expected ${expected}, got ${actual}" >&2
    return 1
  fi
}

wait_for_statefulset_init_image() {
  local statefulset_name="$1"
  local expected_image="$2"
  local deadline=$((SECONDS + 600))
  local actual_image=""

  while ((SECONDS < deadline)); do
    actual_image="$(
      "${KUBECTL_BIN}" get "statefulset/${statefulset_name}" -n "${TENANT_NAMESPACE}" -o json 2>/dev/null | \
        jq -r '.spec.template.spec.initContainers[]? | select(.name == "bao-config-init") | .image' || true
    )"
    if [[ "${actual_image}" == "${expected_image}" ]]; then
      return 0
    fi
    sleep 5
  done

  echo "StatefulSet ${statefulset_name} init image did not converge to ${expected_image}; got ${actual_image}" >&2
  return 1
}

pvc_uid_set() {
  local cluster_name="$1"
  local replica_count="$2"
  local index
  local uid
  local uid_lines=""

  for ((index = 0; index < replica_count; index++)); do
    uid="$(
      "${KUBECTL_BIN}" get "persistentvolumeclaim/data-${cluster_name}-${index}" \
        -n "${TENANT_NAMESPACE}" \
        -o jsonpath='{.metadata.uid}'
    )"
    uid_lines+="${uid}"$'\n'
  done

  jq -Rsc 'split("\n") | map(select(length > 0)) | sort' <<<"${uid_lines}"
}

assert_resource_absent() {
  local resource="$1"

  if "${KUBECTL_BIN}" get "${resource}" -n "${TENANT_NAMESPACE}" >/dev/null 2>&1; then
    echo "Unexpected resource exists: ${TENANT_NAMESPACE}/${resource}" >&2
    return 1
  fi
}

render_hardened_fixture() {
  awk \
    -v default_image="${DEFAULT_HARDENED_INIT_IMAGE}" \
    -v selected_image="${HARDENED_INIT_IMAGE}" '
      $0 == "    image: \"" default_image "\"" {
        $0 = "    image: \"" selected_image "\""
      }
      { print }
    ' "${FIXTURE_DIR}/hardened-cluster.yaml"
}

discover_api_server_endpoint_ips() {
  "${KUBECTL_BIN}" get endpointslices.discovery.k8s.io \
    -n default \
    -l kubernetes.io/service-name=kubernetes \
    -o json | \
    jq -ce '[
      .items[].endpoints[]? |
      select(.conditions.ready != false) |
      .addresses[]?
    ] | unique | select(length > 0)'
}

inject_api_server_endpoint_ips() {
  local endpoint_ips="$1"

  "${KUBECTL_BIN}" create --dry-run=client -f - -o json | \
    jq --argjson endpoint_ips "${endpoint_ips}" \
      '.spec.network.apiServerEndpointIPs = $endpoint_ips'
}

wait_for_hardened_cluster() {
  "${KUBECTL_BIN}" wait "${HARDENED_CLUSTER_RESOURCE}" \
    -n "${TENANT_NAMESPACE}" \
    --for=condition=Available \
    --timeout=12m >/dev/null
  "${KUBECTL_BIN}" wait "${HARDENED_CLUSTER_RESOURCE}" \
    -n "${TENANT_NAMESPACE}" \
    --for=jsonpath='{.status.selfInitialized}'=true \
    --timeout=12m >/dev/null
  "${KUBECTL_BIN}" wait "${HARDENED_CLUSTER_RESOURCE}" \
    -n "${TENANT_NAMESPACE}" \
    --for=condition=ProductionReady \
    --timeout=12m >/dev/null
  "${KUBECTL_BIN}" rollout status statefulset/operator-upgrade-hardened \
    -n "${TENANT_NAMESPACE}" \
    --timeout=12m >/dev/null
}

create_transit_credentials_secret() {
  local token
  local ca_cert

  token="$(
    "${KUBECTL_BIN}" get secret/operator-upgrade-transit-root-token \
      -n "${TENANT_NAMESPACE}" \
      -o json | jq -er '.data.token'
  )"
  ca_cert="$(
    "${KUBECTL_BIN}" get secret/operator-upgrade-transit-tls-ca \
      -n "${TENANT_NAMESPACE}" \
      -o json | jq -er '.data["ca.crt"]'
  )"

  jq -n \
    --arg namespace "${TENANT_NAMESPACE}" \
    --arg token "${token}" \
    --arg ca_cert "${ca_cert}" \
    '{
      apiVersion: "v1",
      kind: "Secret",
      metadata: {
        name: "operator-upgrade-transit-credentials",
        namespace: $namespace
      },
      type: "Opaque",
      data: {
        token: $token,
        "ca.crt": $ca_cert
      }
    }' | "${KUBECTL_BIN}" apply -f - >/dev/null
}

create_external_tls_secrets() {
  local ca_cert
  local ca_key
  local server_cert
  local server_key

  ca_cert="$(
    "${KUBECTL_BIN}" get secret/operator-upgrade-hardened-ca-material \
      -n "${TENANT_NAMESPACE}" \
      -o json | jq -er '.data["tls.crt"]'
  )"
  ca_key="$(
    "${KUBECTL_BIN}" get secret/operator-upgrade-hardened-ca-material \
      -n "${TENANT_NAMESPACE}" \
      -o json | jq -er '.data["tls.key"]'
  )"
  server_cert="$(
    "${KUBECTL_BIN}" get secret/operator-upgrade-hardened-server-material \
      -n "${TENANT_NAMESPACE}" \
      -o json | jq -er '.data["tls.crt"]'
  )"
  server_key="$(
    "${KUBECTL_BIN}" get secret/operator-upgrade-hardened-server-material \
      -n "${TENANT_NAMESPACE}" \
      -o json | jq -er '.data["tls.key"]'
  )"

  jq -n \
    --arg namespace "${TENANT_NAMESPACE}" \
    --arg ca_cert "${ca_cert}" \
    --arg ca_key "${ca_key}" \
    '{
      apiVersion: "v1",
      kind: "Secret",
      metadata: {
        name: "operator-upgrade-hardened-tls-ca",
        namespace: $namespace
      },
      type: "Opaque",
      data: {
        "ca.crt": $ca_cert,
        "ca.key": $ca_key
      }
    }' | "${KUBECTL_BIN}" apply -f - >/dev/null

  jq -n \
    --arg namespace "${TENANT_NAMESPACE}" \
    --arg ca_cert "${ca_cert}" \
    --arg server_cert "${server_cert}" \
    --arg server_key "${server_key}" \
    '{
      apiVersion: "v1",
      kind: "Secret",
      metadata: {
        name: "operator-upgrade-hardened-tls-server",
        namespace: $namespace
      },
      type: "kubernetes.io/tls",
      data: {
        "ca.crt": $ca_cert,
        "tls.crt": $server_cert,
        "tls.key": $server_key
      }
    }' | "${KUBECTL_BIN}" apply -f - >/dev/null
}

verify_harness() {
  local required_files=(
    "${FIXTURE_DIR}/versions.env"
    "${ROOT_DIR}/release-notes/${TARGET_RELEASE}.md"
    "${MIGRATION_FIXTURE_DIR}/${MIGRATION_FROM_VERSION}-openbaocluster.yaml"
    "${MIGRATION_FIXTURE_DIR}/${TARGET_RELEASE}-openbaocluster.yaml"
    "${FIXTURE_DIR}/tenant.yaml"
    "${FIXTURE_DIR}/cluster.yaml"
    "${FIXTURE_DIR}/seed-data-job.yaml"
    "${FIXTURE_DIR}/read-data-job.yaml"
    "${FIXTURE_DIR}/transit-cluster.yaml"
    "${FIXTURE_DIR}/transit-config-job.yaml"
    "${FIXTURE_DIR}/hardened-tls.yaml"
    "${FIXTURE_DIR}/hardened-cluster.yaml"
    "${FIXTURE_DIR}/hardened-seed-data-job.yaml"
    "${FIXTURE_DIR}/hardened-read-data-job.yaml"
    "${FIXTURE_DIR}/post-upgrade-tenant.yaml"
  )

  if [[ -z "${FROM_VERSION}" || -z "${TARGET_RELEASE}" ]]; then
    echo "operator upgrade baseline and target release must be set" >&2
    exit 1
  fi
  if ! [[ "${FROM_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "operator upgrade baseline must be a stable semver: ${FROM_VERSION}" >&2
    exit 1
  fi
  if ! [[ "${TARGET_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
    echo "operator upgrade target must be a semver-compatible image tag: ${TARGET_VERSION}" >&2
    exit 1
  fi
  if ! [[ "${HARDENED_INIT_IMAGE}" =~ ^ghcr\.io/dc-tec/openbao-init@sha256:[0-9a-f]{64}$ ]]; then
    echo "hardened upgrade helper must be an immutable ghcr.io/dc-tec/openbao-init digest: ${HARDENED_INIT_IMAGE}" >&2
    exit 1
  fi
  for path in "${required_files[@]}"; do
    if [[ ! -f "${path}" ]]; then
      echo "operator upgrade E2E input is missing: ${path}" >&2
      exit 1
    fi
  done
  if ! grep -Fq "### Upgrade from ${MIGRATION_FROM_VERSION}" "${ROOT_DIR}/release-notes/${TARGET_RELEASE}.md"; then
    echo "release notes must document the ${MIGRATION_FROM_VERSION} API migration path" >&2
    exit 1
  fi
  if ! grep -Fq "image: \"${DEFAULT_HARDENED_INIT_IMAGE}\"" "${FIXTURE_DIR}/hardened-cluster.yaml"; then
    echo "hardened upgrade fixture must use the pinned default helper ${DEFAULT_HARDENED_INIT_IMAGE}" >&2
    exit 1
  fi

  echo "Operator upgrade E2E harness verified: ${FROM_VERSION} -> ${TARGET_VERSION} (API migration fixtures: ${MIGRATION_FROM_VERSION} -> ${TARGET_RELEASE})"
}

verify_harness
if [[ "${VERIFY_ONLY}" == "true" ]]; then
  exit 0
fi

for command_name in "${KIND_BIN}" "${KUBECTL_BIN}" "${HELM_INSTALL_BIN}" "${HELM_BIN}" "${DOCKER_BIN}" jq python3; do
  require_command "${command_name}"
done
verify_hardened_init_image

WORK_DIR="$(mktemp -d)"
KUBECONFIG_PATH="${WORK_DIR}/kubeconfig"
CANDIDATE_CHART="${WORK_DIR}/openbao-operator"
CLUSTER_CREATED=false
REGISTRY_CREATED=false

collect_diagnostics() {
  echo "Collecting operator upgrade diagnostics..." >&2
  "${KUBECTL_BIN}" get pods -A -o wide >&2 || true
  "${KUBECTL_BIN}" get openbaotenants -A -o yaml >&2 || true
  "${KUBECTL_BIN}" get openbaoclusters -A -o yaml >&2 || true
  "${KUBECTL_BIN}" get certificates,issuers -A -o wide >&2 || true
  "${KUBECTL_BIN}" get jobs -A -o wide >&2 || true
  "${KUBECTL_BIN}" get events -A --sort-by=.lastTimestamp >&2 || true
  for job_name in \
    operator-upgrade-seed \
    operator-upgrade-read \
    operator-upgrade-transit-config \
    operator-upgrade-hardened-seed \
    operator-upgrade-hardened-read; do
    "${KUBECTL_BIN}" logs -n "${TENANT_NAMESPACE}" "job/${job_name}" >&2 || true
  done
  "${KUBECTL_BIN}" logs -n "${OPERATOR_NAMESPACE}" -l app.kubernetes.io/name=openbao-operator \
    --all-containers --prefix --tail=200 >&2 || true
}

cleanup() {
  local status="$?"
  if [[ "${status}" -ne 0 && "${CLUSTER_CREATED}" == "true" ]]; then
    collect_diagnostics
  fi
  if [[ "${CLUSTER_CREATED}" == "true" ]]; then
    if [[ "${KEEP_CLUSTER}" == "true" ]]; then
      echo "Keeping Kind cluster ${CLUSTER_NAME}; kubeconfig was ${KUBECONFIG_PATH}" >&2
    else
      "${KIND_BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
    fi
  fi
  if [[ "${REGISTRY_CREATED}" == "true" ]]; then
    if [[ "${KEEP_CLUSTER}" == "true" ]]; then
      echo "Keeping candidate image registry ${REGISTRY_NAME}" >&2
    else
      "${DOCKER_BIN}" rm --force "${REGISTRY_NAME}" >/dev/null 2>&1 || true
    fi
  fi
  if [[ "${KEEP_CLUSTER}" != "true" ]]; then
    rm -rf -- "${WORK_DIR}"
  fi
  return "${status}"
}
trap cleanup EXIT

if "${KIND_BIN}" get clusters | grep -qx "${CLUSTER_NAME}"; then
  echo "Kind cluster ${CLUSTER_NAME} already exists; choose another OPERATOR_UPGRADE_E2E_KIND_CLUSTER" >&2
  exit 1
fi
if "${DOCKER_BIN}" container inspect "${REGISTRY_NAME}" >/dev/null 2>&1; then
  echo "Docker container ${REGISTRY_NAME} already exists; choose another OPERATOR_UPGRADE_E2E_REGISTRY_NAME" >&2
  exit 1
fi

echo "Creating Kind ${KUBERNETES_VERSION} cluster ${CLUSTER_NAME}..." >&2
"${KIND_BIN}" create cluster \
  --name "${CLUSTER_NAME}" \
  --image "${KIND_NODE_IMAGE}" \
  --kubeconfig "${KUBECONFIG_PATH}"
CLUSTER_CREATED=true
export KUBECONFIG="${KUBECONFIG_PATH}"

echo "Starting candidate image registry ${REGISTRY_NAME}..." >&2
"${DOCKER_BIN}" run \
  --detach \
  --name "${REGISTRY_NAME}" \
  --publish 127.0.0.1::5000 \
  "${REGISTRY_IMAGE}" >/dev/null
REGISTRY_CREATED=true
"${DOCKER_BIN}" network connect kind "${REGISTRY_NAME}"
registry_endpoint="$("${DOCKER_BIN}" port "${REGISTRY_NAME}" 5000/tcp | head -n1)"
registry_port="${registry_endpoint##*:}"
if ! [[ "${registry_port}" =~ ^[0-9]+$ ]]; then
  echo "failed to resolve the candidate registry port: ${registry_endpoint}" >&2
  exit 1
fi

while IFS= read -r node_name; do
  if [[ -z "${node_name}" ]]; then
    continue
  fi
  "${DOCKER_BIN}" exec "${node_name}" mkdir -p /etc/containerd/certs.d/operator-upgrade.local
  "${DOCKER_BIN}" exec --interactive "${node_name}" \
    tee /etc/containerd/certs.d/operator-upgrade.local/hosts.toml >/dev/null <<EOF
server = "https://operator-upgrade.local"

[host."http://${REGISTRY_NAME}:5000"]
  capabilities = ["pull", "resolve"]
EOF
done < <("${KIND_BIN}" get nodes --name "${CLUSTER_NAME}")

api_server_endpoint_ips="$(discover_api_server_endpoint_ips)"
echo "Kubernetes API server endpoint IPs: ${api_server_endpoint_ips}" >&2

prepare_candidate_image() {
  local source_image="$1"
  local local_image="$2"
  local build_target="$3"
  local registry_image="127.0.0.1:${registry_port}/${local_image#*/}"

  if [[ -n "${source_image}" ]]; then
    "${DOCKER_BIN}" pull "${source_image}"
    "${DOCKER_BIN}" tag "${source_image}" "${local_image}"
  else
    make "${build_target}" IMG="${local_image}"
  fi
  "${DOCKER_BIN}" tag "${local_image}" "${registry_image}"
  "${DOCKER_BIN}" push "${registry_image}"
}

cd "${ROOT_DIR}"
prepare_candidate_image "${MANAGER_SOURCE}" "${LOCAL_MANAGER_IMAGE}" docker-build
prepare_candidate_image "${INIT_SOURCE}" "${LOCAL_INIT_IMAGE}" docker-build-init

echo "Installing cert-manager..." >&2
"${KUBECTL_BIN}" apply -f \
  https://github.com/cert-manager/cert-manager/releases/download/v1.19.1/cert-manager.yaml >/dev/null
"${KUBECTL_BIN}" wait deployment \
  -n cert-manager \
  -l app.kubernetes.io/instance=cert-manager \
  --for=condition=Available \
  --timeout=5m >/dev/null

echo "Installing released operator ${FROM_VERSION}..." >&2
echo "Install client: $("${HELM_INSTALL_BIN}" version --short)" >&2
"${HELM_INSTALL_BIN}" install openbao-operator oci://ghcr.io/dc-tec/charts/openbao-operator \
  --version "${FROM_VERSION}" \
  --namespace "${OPERATOR_NAMESPACE}" \
  --create-namespace \
  --wait \
  --timeout 5m >/dev/null

"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/tenant.yaml" >/dev/null
"${KUBECTL_BIN}" wait "openbaotenant/openbao-upgrade-e2e" \
  -n "${OPERATOR_NAMESPACE}" \
  --for=jsonpath='{.status.provisioned}'=true \
  --timeout=5m >/dev/null

inject_api_server_endpoint_ips "${api_server_endpoint_ips}" < "${FIXTURE_DIR}/cluster.yaml" | \
  "${KUBECTL_BIN}" apply -f - >/dev/null
"${KUBECTL_BIN}" wait "${CLUSTER_RESOURCE}" \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Available \
  --timeout=10m >/dev/null
"${KUBECTL_BIN}" wait "secret/operator-upgrade-root-token" \
  -n "${TENANT_NAMESPACE}" \
  --for=create \
  --timeout=2m >/dev/null
"${KUBECTL_BIN}" rollout status "statefulset/operator-upgrade" \
  -n "${TENANT_NAMESPACE}" \
  --timeout=5m >/dev/null

cluster_uid="$("${KUBECTL_BIN}" get "${CLUSTER_RESOURCE}" -n "${TENANT_NAMESPACE}" -o jsonpath='{.metadata.uid}')"
statefulset_uid="$(
  "${KUBECTL_BIN}" get statefulset/operator-upgrade \
    -n "${TENANT_NAMESPACE}" \
    -o jsonpath='{.metadata.uid}'
)"
pvc_uid="$("${KUBECTL_BIN}" get "${PVC_RESOURCE}" -n "${TENANT_NAMESPACE}" -o jsonpath='{.metadata.uid}')"

echo "Seeding OpenBao data before the operator handoff..." >&2
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/seed-data-job.yaml" >/dev/null
"${KUBECTL_BIN}" wait job/operator-upgrade-seed \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Complete \
  --timeout=5m >/dev/null

echo "Provisioning the Transit unseal backend for the Hardened profile..." >&2
inject_api_server_endpoint_ips "${api_server_endpoint_ips}" < "${FIXTURE_DIR}/transit-cluster.yaml" | \
  "${KUBECTL_BIN}" apply -f - >/dev/null
"${KUBECTL_BIN}" wait "${TRANSIT_CLUSTER_RESOURCE}" \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Available \
  --timeout=10m >/dev/null
"${KUBECTL_BIN}" wait secret/operator-upgrade-transit-root-token \
  -n "${TENANT_NAMESPACE}" \
  --for=create \
  --timeout=2m >/dev/null
"${KUBECTL_BIN}" rollout status statefulset/operator-upgrade-transit \
  -n "${TENANT_NAMESPACE}" \
  --timeout=5m >/dev/null
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/transit-config-job.yaml" >/dev/null
"${KUBECTL_BIN}" wait job/operator-upgrade-transit-config \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Complete \
  --timeout=5m >/dev/null
create_transit_credentials_secret

echo "Issuing External TLS material for the Hardened profile..." >&2
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/hardened-tls.yaml" >/dev/null
"${KUBECTL_BIN}" wait certificate/operator-upgrade-hardened-ca \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Ready \
  --timeout=3m >/dev/null
"${KUBECTL_BIN}" wait issuer/operator-upgrade-hardened-ca \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Ready \
  --timeout=3m >/dev/null
"${KUBECTL_BIN}" wait certificate/operator-upgrade-hardened-server \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Ready \
  --timeout=3m >/dev/null
create_external_tls_secrets

echo "Provisioning a signed, self-initialized Hardened cluster..." >&2
render_hardened_fixture | \
  inject_api_server_endpoint_ips "${api_server_endpoint_ips}" | \
  "${KUBECTL_BIN}" apply -f - >/dev/null
wait_for_statefulset_init_image operator-upgrade-hardened "${HARDENED_INIT_IMAGE}"
wait_for_hardened_cluster
assert_resource_absent secret/operator-upgrade-hardened-root-token
assert_resource_absent secret/operator-upgrade-hardened-unseal-key

hardened_cluster_uid="$(
  "${KUBECTL_BIN}" get "${HARDENED_CLUSTER_RESOURCE}" \
    -n "${TENANT_NAMESPACE}" \
    -o jsonpath='{.metadata.uid}'
)"
hardened_statefulset_uid="$(
  "${KUBECTL_BIN}" get statefulset/operator-upgrade-hardened \
    -n "${TENANT_NAMESPACE}" \
    -o jsonpath='{.metadata.uid}'
)"
hardened_pvc_uids="$(pvc_uid_set operator-upgrade-hardened 3)"

echo "Seeding Hardened-profile data through self-initialized JWT auth..." >&2
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/hardened-seed-data-job.yaml" >/dev/null
"${KUBECTL_BIN}" wait job/operator-upgrade-hardened-seed \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Complete \
  --timeout=5m >/dev/null

render_migration_fixture() {
  local fixture="$1"
  awk -v target_namespace="${TENANT_NAMESPACE}" '
    $0 == "  namespace: openbao" { $0 = "  namespace: " target_namespace }
    { print }
    $0 == "spec:" { print "  paused: true" }
  ' "${fixture}"
}

echo "Migrating stored API fields while ${FROM_VERSION} still owns the cluster..." >&2
render_migration_fixture "${MIGRATION_FIXTURE_DIR}/${MIGRATION_FROM_VERSION}-openbaocluster.yaml" | \
  "${KUBECTL_BIN}" apply -f - >/dev/null
migration_uid="$(
  "${KUBECTL_BIN}" get openbaocluster/api-stability-migration \
    -n "${TENANT_NAMESPACE}" \
    -o jsonpath='{.metadata.uid}'
)"
render_migration_fixture "${MIGRATION_FIXTURE_DIR}/${TARGET_RELEASE}-openbaocluster.yaml" | \
  "${KUBECTL_BIN}" apply -f - >/dev/null

migration_json="$("${KUBECTL_BIN}" get openbaocluster/api-stability-migration -n "${TENANT_NAMESPACE}" -o json)"
jq -e '
  .spec.paused == true and
  .spec.tls.acme.domains == ["bao.example.com"] and
  (.spec.tls.acme | has("domain") | not) and
  .spec.runtime.restartAt == "2026-08-01T12:00:00Z" and
  (.spec.maintenance | has("restartAt") | not) and
  (.spec.upgrade | has("tokenSecretRef") | not)
' <<<"${migration_json}" >/dev/null

echo "Applying candidate CRDs before the Helm upgrade..." >&2
make build-crds >/dev/null
"${KUBECTL_BIN}" apply -f "${ROOT_DIR}/dist/crds.yaml" >/dev/null
"${KUBECTL_BIN}" wait \
  --for=condition=Established \
  crd/openbaoclusters.openbao.org \
  crd/openbaotenants.openbao.org \
  crd/openbaorestores.openbao.org \
  --timeout=2m >/dev/null

cp -R "${ROOT_DIR}/charts/openbao-operator" "${CANDIDATE_CHART}"
python3 - "${CANDIDATE_CHART}/Chart.yaml" "${TARGET_VERSION}" <<'PY'
import re
import sys
from pathlib import Path

chart_path = Path(sys.argv[1])
version = sys.argv[2]
contents = chart_path.read_text(encoding="utf-8")
contents = re.sub(r"(?m)^version:.*$", f"version: {version}", contents, count=1)
contents = re.sub(r"(?m)^appVersion:.*$", f"appVersion: {version}", contents, count=1)
chart_path.write_text(contents, encoding="utf-8")
PY

echo "Upgrading the operator to candidate ${TARGET_VERSION}..." >&2
echo "Upgrade client: $("${HELM_BIN}" version --short)" >&2
"${HELM_BIN}" upgrade openbao-operator "${CANDIDATE_CHART}" \
  --namespace "${OPERATOR_NAMESPACE}" \
  --set-string image.repository=operator-upgrade.local/openbao-operator \
  --set-string "image.tag=${TARGET_VERSION}" \
  --set-string "operatorVersion=${TARGET_VERSION}" \
  --set-string 'controller.extraEnv[0].name=OPERATOR_INIT_IMAGE_REPOSITORY' \
  --set-string 'controller.extraEnv[0].value=operator-upgrade.local/openbao-init' \
  --wait \
  --timeout 5m >/dev/null

"${KUBECTL_BIN}" rollout status deployment/openbao-operator-controller \
  -n "${OPERATOR_NAMESPACE}" \
  --timeout=5m >/dev/null
"${KUBECTL_BIN}" rollout status deployment/openbao-operator-provisioner \
  -n "${OPERATOR_NAMESPACE}" \
  --timeout=5m >/dev/null

deployments_json="$(
  "${KUBECTL_BIN}" get deployment \
    -n "${OPERATOR_NAMESPACE}" \
    -l app.kubernetes.io/name=openbao-operator \
    -o json
)"
jq -e --arg image "${LOCAL_MANAGER_IMAGE}" --arg version "${TARGET_VERSION}" '
  .items as $items |
  ($items | length) == 2 and
  all($items[];
    all(.spec.template.spec.containers[];
      .image == $image and any(.env[]; .name == "OPERATOR_VERSION" and .value == $version)))
' <<<"${deployments_json}" >/dev/null

wait_for_statefulset_init_image operator-upgrade "${LOCAL_INIT_IMAGE}"
"${KUBECTL_BIN}" rollout status statefulset/operator-upgrade \
  -n "${TENANT_NAMESPACE}" \
  --timeout=10m >/dev/null
"${KUBECTL_BIN}" wait "${CLUSTER_RESOURCE}" \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Available \
  --timeout=10m >/dev/null

wait_for_statefulset_init_image operator-upgrade-transit "${LOCAL_INIT_IMAGE}"
"${KUBECTL_BIN}" rollout status statefulset/operator-upgrade-transit \
  -n "${TENANT_NAMESPACE}" \
  --timeout=10m >/dev/null
"${KUBECTL_BIN}" wait "${TRANSIT_CLUSTER_RESOURCE}" \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Available \
  --timeout=10m >/dev/null

echo "Reapplying the Hardened contract through candidate admission..." >&2
render_hardened_fixture | "${KUBECTL_BIN}" apply -f - >/dev/null
wait_for_statefulset_init_image operator-upgrade-hardened "${HARDENED_INIT_IMAGE}"
wait_for_hardened_cluster

assert_equal "OpenBaoCluster UID changed during the operator upgrade" "${cluster_uid}" \
  "$("${KUBECTL_BIN}" get "${CLUSTER_RESOURCE}" -n "${TENANT_NAMESPACE}" -o jsonpath='{.metadata.uid}')"
assert_equal "StatefulSet UID changed during the operator upgrade" "${statefulset_uid}" \
  "$("${KUBECTL_BIN}" get statefulset/operator-upgrade -n "${TENANT_NAMESPACE}" -o jsonpath='{.metadata.uid}')"
assert_equal "PVC UID changed during the operator upgrade" "${pvc_uid}" \
  "$("${KUBECTL_BIN}" get "${PVC_RESOURCE}" -n "${TENANT_NAMESPACE}" -o jsonpath='{.metadata.uid}')"
assert_equal "Migration fixture UID changed during the operator upgrade" "${migration_uid}" \
  "$(
    "${KUBECTL_BIN}" get openbaocluster/api-stability-migration \
      -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.metadata.uid}'
  )"
assert_equal "Hardened OpenBaoCluster UID changed during the operator upgrade" "${hardened_cluster_uid}" \
  "$(
    "${KUBECTL_BIN}" get "${HARDENED_CLUSTER_RESOURCE}" \
      -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.metadata.uid}'
  )"
assert_equal "Hardened StatefulSet UID changed during the operator upgrade" "${hardened_statefulset_uid}" \
  "$(
    "${KUBECTL_BIN}" get statefulset/operator-upgrade-hardened \
      -n "${TENANT_NAMESPACE}" \
      -o jsonpath='{.metadata.uid}'
  )"
assert_equal "Hardened PVC UIDs changed during the operator upgrade" "${hardened_pvc_uids}" \
  "$(pvc_uid_set operator-upgrade-hardened 3)"
assert_resource_absent secret/operator-upgrade-hardened-root-token
assert_resource_absent secret/operator-upgrade-hardened-unseal-key

statefulset_json="$("${KUBECTL_BIN}" get statefulset/operator-upgrade -n "${TENANT_NAMESPACE}" -o json)"
jq -e --arg image "${LOCAL_INIT_IMAGE}" '
  any(.spec.template.spec.initContainers[]; .name == "bao-config-init" and .image == $image)
' <<<"${statefulset_json}" >/dev/null

hardened_statefulset_json="$(
  "${KUBECTL_BIN}" get statefulset/operator-upgrade-hardened \
    -n "${TENANT_NAMESPACE}" \
    -o json
)"
jq -e --arg image "${HARDENED_INIT_IMAGE}" '
  .spec.replicas == 3 and
  any(.spec.template.spec.initContainers[]; .name == "bao-config-init" and .image == $image)
' <<<"${hardened_statefulset_json}" >/dev/null

migration_json="$("${KUBECTL_BIN}" get openbaocluster/api-stability-migration -n "${TENANT_NAMESPACE}" -o json)"
jq -e '
  .spec.paused == true and
  .spec.tls.acme.domains == ["bao.example.com"] and
  (.spec.tls.acme | has("domain") | not) and
  .spec.runtime.restartAt == "2026-08-01T12:00:00Z" and
  (.spec.maintenance | has("restartAt") | not) and
  (.spec.upgrade | has("tokenSecretRef") | not)
' <<<"${migration_json}" >/dev/null

echo "Reading the pre-upgrade data through the reconciled workload..." >&2
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/read-data-job.yaml" >/dev/null
"${KUBECTL_BIN}" wait job/operator-upgrade-read \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Complete \
  --timeout=5m >/dev/null
assert_equal "OpenBao data changed during the operator upgrade" "preserved" \
  "$("${KUBECTL_BIN}" logs -n "${TENANT_NAMESPACE}" job/operator-upgrade-read | tr -d '\r\n')"

echo "Reading the Hardened pre-upgrade data through JWT auth and verified TLS..." >&2
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/hardened-read-data-job.yaml" >/dev/null
"${KUBECTL_BIN}" wait job/operator-upgrade-hardened-read \
  -n "${TENANT_NAMESPACE}" \
  --for=condition=Complete \
  --timeout=5m >/dev/null
assert_equal "Hardened OpenBao data changed during the operator upgrade" "preserved" \
  "$("${KUBECTL_BIN}" logs -n "${TENANT_NAMESPACE}" job/operator-upgrade-hardened-read | tr -d '\r\n')"

echo "Exercising the upgraded provisioner with a new tenant..." >&2
"${KUBECTL_BIN}" apply -f "${FIXTURE_DIR}/post-upgrade-tenant.yaml" >/dev/null
"${KUBECTL_BIN}" wait openbaotenant/openbao-upgrade-post \
  -n "${OPERATOR_NAMESPACE}" \
  --for=jsonpath='{.status.provisioned}'=true \
  --timeout=5m >/dev/null

echo "Operator upgrade E2E succeeded: ${FROM_VERSION} -> ${TARGET_VERSION}" >&2
