#!/usr/bin/env bash
# Create an isolated, policy-enforcing Kind lab. KUBECONFIG must be task-owned.
set -euo pipefail

source hack/dev/tool-versions.env
: "${KIND_CLUSTER:?cluster name is required}"
: "${KIND_NODE_IMAGE:?Kind node image is required}"
: "${KUBECONFIG:?use a dedicated kubeconfig}"
: "${E2E_NETWORK_PROFILE:?network profile is required}"
[[ "${KIND_CLUSTER}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "Invalid cluster name" >&2; exit 1; }

ipv4=true
ipv6=true
family=dual
pod_subnets='10.244.0.0/16,fd00:10:244::/56'
service_subnets='10.96.0.0/16,fd00:10:96::/112'
case "${E2E_NETWORK_PROFILE}" in
  ipv4) family=ipv4; ipv6=false; pod_subnets='10.244.0.0/16'; service_subnets='10.96.0.0/16' ;;
  ipv6) family=ipv6; ipv4=false; pod_subnets='fd00:10:244::/56'; service_subnets='fd00:10:96::/112' ;;
  dual-ipv4) ;;
  dual-ipv6) pod_subnets='fd00:10:244::/56,10.244.0.0/16'; service_subnets='fd00:10:96::/112,10.96.0.0/16' ;;
  *) echo "Unknown network profile: ${E2E_NETWORK_PROFILE}" >&2; exit 1 ;;
esac

report_dir="${E2E_REPORT_DIR:-dist/test/network-${KIND_CLUSTER}}"
mkdir -p "${report_dir}" "$(dirname "${KUBECONFIG}")"
network="${KIND_CLUSTER}-network"
if kind get clusters | grep -qx "${KIND_CLUSTER}" || docker network inspect "${network}" >/dev/null 2>&1; then
  echo "Refusing to reuse an existing cluster or Docker network" >&2
  exit 1
fi
owner="${E2E_NETWORK_OWNER:-$(python3 -c 'import uuid; print(uuid.uuid4())')}"
printf '%s\n' "${owner}" > "${KUBECONFIG}.owner"

# Kind orders both node IPs and the API advertise address from the first Service
# subnet. Retain Kind's node-ip configuration so dual-stack nodes keep both IPs.
cat > "${report_dir}/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: ${family}
  apiServerAddress: 127.0.0.1
  disableDefaultCNI: true
  kubeProxyMode: none
  podSubnet: ${pod_subnets}
  serviceSubnet: ${service_subnets}
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF

docker network create --ipv6 --label "openbao.test.cluster=${KIND_CLUSTER}" --label "openbao.test.owner=${owner}" "${network}"
KIND_EXPERIMENTAL_DOCKER_NETWORK="${network}" kind create cluster \
  --name "${KIND_CLUSTER}" --image "${KIND_NODE_IMAGE}" \
  --config "${report_dir}/kind.yaml" --kubeconfig "${KUBECONFIG}"

address_key=IPAddress
[[ "${E2E_NETWORK_PROFILE}" == ipv6 || "${E2E_NETWORK_PROFILE}" == dual-ipv6 ]] && address_key=GlobalIPv6Address
api_host="$(docker inspect "${KIND_CLUSTER}-control-plane" \
  --format "{{(index .NetworkSettings.Networks \"${network}\").${address_key}}}")"
[[ -n "${api_host}" ]] || { echo "No API server address found" >&2; exit 1; }
cat > "${report_dir}/cilium.yaml" <<EOF
kubeProxyReplacement: true
k8sServiceHost: "${api_host}"
k8sServicePort: 6443
ipv4:
  enabled: ${ipv4}
ipv6:
  enabled: ${ipv6}
ipam:
  mode: kubernetes
routingMode: native
autoDirectNodeRoutes: true
ipv4NativeRoutingCIDR: 10.244.0.0/16
ipv6NativeRoutingCIDR: fd00:10:244::/56
operator:
  replicas: 1
EOF
helm upgrade --install cilium oci://quay.io/cilium/charts/cilium \
  --version "${CILIUM_VERSION#v}" --namespace kube-system \
  --values "${report_dir}/cilium.yaml" --wait --timeout 5m
kubectl --kubeconfig "${KUBECONFIG}" wait nodes --all --for=condition=Ready --timeout=180s

# API-server egress is managed by the operator; no administrator policy is installed.
kubectl --kubeconfig "${KUBECONFIG}" get nodes -o json > "${report_dir}/nodes.json"
kubectl --kubeconfig "${KUBECONFIG}" -n default get service kubernetes -o json > "${report_dir}/api-service.json"
