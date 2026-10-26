# Qualify a restore between Kubernetes clusters

The `restore-cross-cluster` suite backs up a source OpenBao cluster, stops its
single Kind node, and restores into an independent recovery Kubernetes cluster.
It tests Disposable cleanup and Retain recovery with administrator repair of
restored JWT trust. The suite uses OpenBao 2.7.2 and a shared Transit unseal key.

Run this suite only against disposable test environments. It stops the source
node, installs the operator in the recovery cluster, and removes that operator
after a successful run. It does not qualify remote scheduling or production
traffic cutover.

The 2026-10-05 qualification passes both cases with Kubernetes 1.36.1 and a
Cilium 1.20.2 recovery cluster. The Retain case checks normal service registration,
30 seconds of Pod stability with zero container restarts, and a post-handoff data
read. These assertions cover a regression where `Completed` preceded a crash loop
after the transition from the restricted template to normal management.

## Prepare the clusters

Use two Kind clusters on the same Docker network. The recovery cluster must have
a CNI that enforces NetworkPolicy. The source must be a single-node cluster whose
name starts with `restore-source-`. Give the source a different service-account
issuer and non-overlapping Pod and Service address ranges. For example:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  podSubnet: 10.246.0.0/16
  serviceSubnet: 10.97.0.0/16
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: ClusterConfiguration
    apiServer:
      extraArgs:
      - name: service-account-issuer
        value: https://source.restore.invalid
      - name: service-account-jwks-uri
        value: https://kubernetes.default.svc/openid/v1/jwks
```

This example uses the kubeadm configuration format in the Kubernetes 1.36.1 Kind
image. Create the source with a separate kubeconfig:

```sh
kind create cluster --name restore-source-qualification \
  --image kindest/node:v1.36.1 --config source-kind.yaml \
  --kubeconfig "$PWD/source.kubeconfig"
```

Build the operator, init, and backup executor images from the checkout under
test. Load all three images into both clusters. Set `E2E_OPERATOR_IMAGE`,
`E2E_CONFIG_INIT_IMAGE`, and `E2E_BACKUP_EXECUTOR_IMAGE` to their tags. Install the
source operator before running the suite:

```sh
kubectl --kubeconfig "$PWD/source.kubeconfig" create namespace openbao-operator-system
KUBECONFIG="$PWD/source.kubeconfig" make install deploy IMG="$E2E_OPERATOR_IMAGE"
kubectl --kubeconfig "$PWD/source.kubeconfig" -n openbao-operator-system \
  wait deployment --all --for=condition=Available --timeout=300s
```

The source needs access to NodePorts on the recovery cluster. The suite hosts
RustFS and Transit there so both remain available while the source is stopped.
It creates a recovery namespace with default-deny NetworkPolicies and specific
DNS, API, storage, Transit, and restore-executor allowances. Configure
`E2E_API_SERVER_ENDPOINT_IPS` for the recovery API addresses. For Cilium, enable
the CIDR handling needed to allow those node addresses; see the
[restore API access profile](../../website/content-versions/next/docs/configure/network.md#restore-kubernetes-api-access-with-cilium).
The source cluster specification includes its own API endpoint addresses and
port-scoped egress rules for the two NodePorts. The fixture requires IPv4.

## Run the suite

Select the recovery cluster with `KUBECONFIG`. Set both source variables; with
neither set, this suite skips. A partial configuration fails.

```sh
export KUBECONFIG=/absolute/path/recovery.kubeconfig
export E2E_USE_EXISTING_CLUSTER=true
export E2E_CLUSTER_NAME=restore-recovery-qualification
export E2E_SKIP_CLEANUP=false
export CERT_MANAGER_INSTALL_SKIP=true
export E2E_CROSS_CLUSTER_SOURCE_KUBECONFIG="$PWD/source.kubeconfig"
export E2E_CROSS_CLUSTER_SOURCE_KIND=restore-source-qualification

go test -v -tags=e2e ./test/e2e -run '^TestE2E$' -count=1 -timeout=25m \
  -ginkgo.label-filter=restore-cross-cluster -ginkgo.fail-on-empty \
  -ginkgo.json-report="$PWD/cross-cluster.json"
```

The suite checks distinct Kubernetes namespace UIDs, JWT issuers, and JWKS
hashes before creating the source. Its report records those identities and the
snapshot's native cluster ID, version, size, and SHA-256 digest. It does not
record administrator credentials.

Expected results:

- Disposable restoration confirms the source identity, completes, and deletes
  the target cluster and data PVC while the source node is stopped.
- Retain restoration permits an independent administrator login and reads data
  from the snapshot. `Resume` keeps the hold until the administrator repairs the
  destination issuer, signing-key trust, controller audience, and lifecycle
  subjects. Management then resumes after replacing the target Pod with the
  normal configuration and Kubernetes API token mount. Service registration
  works, the Pod stays stable, and the data remains readable while the source
  node remains stopped.

The repair imports public signing keys obtained through the administrator's
authenticated Kubernetes connection. Kind rejects anonymous JWKS reads, so the
test does not configure the restored target to fetch keys from the API directly.
It verifies acceptance of the destination controller audience and rejection of
an unrelated cluster audience. Static-key rotation remains an administrator task.

The independent login and data read are test assertions. They are not an
operator-managed read check or a standing production credential.

## Clean up

The suite restarts the source node in `AfterAll`. On success it removes its
recovery fixtures through normal finalizers. On failure it keeps recovery
fixtures and the recovery operator for inspection. If the test process is
interrupted, start the source node with `docker start` before inspecting it.

After collecting evidence, delete the disposable source cluster and remove its
kubeconfig. Delete the recovery cluster only if you created it for this run.

```sh
kind delete cluster --name restore-source-qualification
rm source.kubeconfig
```

Keep kubeconfigs, local runner scripts, and private evidence out of commits.
