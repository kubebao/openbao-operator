# Network qualification

The `networking` PR lane runs on an IPv6-only Kind cluster with Cilium and kube-proxy replacement. It checks three
voters and a read replica, Pod IP advertisement, DNS and literal-IP connections with certificate verification, Service
IP families, Kubernetes API access, and denied workload and helper egress. A reachable target and an unselected client
provide the negative test's control. The External TLS case supplies only the stable server-name DNS SAN.

Weekly and release qualification add IPv6 scaling, restart, rolling upgrade, and storage-provider journeys. They also run the
networking scenarios and fresh/scheduled restore targets on dual-stack clusters with each primary family. The restore
fixture selects a destination client Service primary family different from the source on dual-stack clusters. It checks
the generated request, target, and Service, then retains the existing application and cleanup checks across two scheduled
runs. These restore journeys also run on IPv6-only clusters. Matrices record `network_profile` so repeated
cases in different network environments remain distinct. Scenario ownership and expected-case validation still apply.

Use an isolated cluster and kubeconfig for a local run:

```sh
export KIND_CLUSTER=bao-network-test
export KIND_NODE_IMAGE=kindest/node:v1.36.1
export KUBECONFIG="$PWD/dist/test/network/kubeconfig"
export E2E_NETWORK_PROFILE=ipv6
export E2E_REPORT_DIR="$PWD/dist/test/network"
bash hack/ci/create-network-kind.sh
make test-e2e-ci E2E_LABEL_FILTER=networking E2E_INSTALL_CSI_HOSTPATH=false \
  E2E_PRELOAD_HARDENED_ASSETS=false E2E_FAIL_ON_EMPTY=true E2E_SKIP_CLEANUP=true \
  E2E_JSON_REPORT="$E2E_REPORT_DIR/ginkgo.json" E2E_JUNIT_REPORT="$E2E_REPORT_DIR/junit.xml"
bash hack/ci/delete-network-kind.sh
```

The profiles are `ipv4`, `ipv6`, `dual-ipv4`, and `dual-ipv6`. The setup script refuses to reuse an existing cluster or
Docker network. It records the Kind and Cilium configuration and the actual Node and API Service addresses. Cleanup
requires the network's cluster label and a matching per-run ownership token. The lab uses the Cilium version in `hack/dev/tool-versions.env` and an
administrator-owned API-server policy; it does not replace the generated Kubernetes NetworkPolicies.

Network qualification gives the provisioner a 500m CPU limit and 256 MiB memory limit for cache warmup. Production
resource configuration remains independent of this test fixture. These tests do not qualify cloud load balancers,
other CNI implementations, ACME issuance over IPv6, or every storage driver.
