---
title: Create the first cluster
description: Create and verify a Development cluster, or complete the full Hardened contract before production.
eyebrow: Get started · Step 4
weight: 4
verifiedBy:
  - charts/openbao-operator/Chart.yaml
  - api/v1alpha1/openbaocluster_types.go
  - api/v1alpha1/openbaocluster_unseal_types.go
  - api/v1alpha1/openbaocluster_selfinit_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/controller/openbaocluster/security_events.go
  - internal/controller/openbaocluster/status_user_access_bootstrap.go
---

Create a disposable Development cluster for evaluation. Do not create a production cluster until its entire
`Hardened` contract is complete.

## Before you begin

- Verify the controller and Provisioner are available.
- In multi-tenant mode, confirm `OpenBaoTenant.status.provisioned: true` and the tenant RoleBinding.
- In single-tenant mode, confirm that the controller's `WATCH_NAMESPACE` equals the target namespace.
- Confirm a default StorageClass exists for evaluation. Choose the StorageClass and capacity explicitly for
  production.
- Decide whether the cluster is disposable or intended for production before the first reconcile.

{{< command label="verify" title="Verify evaluation storage" >}}
kubectl get storageclass
{{< /command >}}

For evaluation, the output must identify one default StorageClass. Stop and configure storage when no default exists.

## Choose the profile

| Profile | Intended use | Security behavior |
| --- | --- | --- |
| `Development` | Local evaluation, CI, and disposable environments | Permits operator-managed TLS, static auto-unseal, and a root token Secret; reports security risk |
| `Hardened` | Production | Requires External or ACME TLS, non-static unseal, self-init, and at least three replicas |

`spec.profile` is required. Changing the word `Development` to `Hardened` does not complete the production contract.

## Create an evaluation cluster

1. Save this manifest as `cluster.yaml`.

   {{< command label="configure" title="Declare the evaluation cluster" >}}
   apiVersion: openbao.org/v1alpha1
   kind: OpenBaoCluster
   metadata:
     name: dev-cluster
     namespace: openbao-demo
   spec:
     version: "2.6.2"
     replicas: 1
     profile: Development
     tls:
       enabled: true
       mode: OperatorManaged
       rotationPeriod: "720h"
     storage:
       size: "10Gi"
     deletionPolicy: Retain
   {{< /command >}}

   Replace `openbao-demo` only with a namespace authorized by the chosen tenancy model. Version 2.6.x is the primary
   current validation line; the manifest pins the concrete version exercised by current CI.

2. Apply the manifest.

   {{< command label="apply" title="Create the cluster" >}}
   kubectl apply -f cluster.yaml
   {{< /command >}}

3. In a separate terminal, optionally watch the custom resource converge. Stop the watch with `Ctrl-C` after the
   availability condition becomes true.

   {{< command label="inspect" title="Watch cluster creation" >}}
   kubectl -n openbao-demo get openbaocluster dev-cluster -w
   {{< /command >}}

4. Wait for the availability condition.

   {{< command label="verify" title="Wait for cluster availability" >}}
   kubectl -n openbao-demo wait \
     --for=condition=Available \
     openbaocluster/dev-cluster \
     --timeout=10m
   {{< /command >}}

5. Inspect the final status and storage.

   {{< command label="verify" title="Verify status and persistent storage" >}}
   kubectl -n openbao-demo get openbaocluster dev-cluster -o yaml
   kubectl -n openbao-demo get pods,pvc,services \
     -l openbao.org/cluster=dev-cluster
   {{< /command >}}

   Confirm:

   - `status.phase` is `Running`;
   - `status.readyReplicas` equals `spec.replicas`;
   - `Available=True` and `TLSReady=True`;
   - every voter Pod is Ready and its PVC is Bound;
   - the TLS mode and storage match the declared configuration.

6. Prove that the evaluation credential can authenticate to OpenBao from inside the cluster.

   {{< command label="verify" title="Run an authenticated OpenBao check" >}}
   (
     kubectl -n openbao-demo get secret dev-cluster-root-token \
       -o jsonpath='{.data.token}' | base64 -d
     printf '\n'
   ) | kubectl -n openbao-demo exec -i -c openbao dev-cluster-0 -- \
     sh -ec '
       IFS= read -r BAO_TOKEN
       export BAO_ADDR=https://127.0.0.1:8200
       export BAO_CACERT=/etc/bao/tls/ca.crt
       export BAO_TLS_SERVER_NAME=openbao-cluster-dev-cluster.local
       export BAO_TOKEN
       bao token capabilities sys/health
     '
   {{< /command >}}

   The command must print `root`. This check proves authenticated API access through the Pod-local endpoint. It does
   not place the token in the exec command or expose OpenBao outside the cluster. Use
   [Expose OpenBao](../../configure/expose/) when the evaluation requires a client entry path.

{{< callout type="warning" title="Development stores sensitive material in Kubernetes Secrets" >}}
The default static auto-unseal key and root token are stored in Kubernetes Secrets. Protect etcd encryption, RBAC,
logs, support bundles, and backups even for evaluation. Do not reuse this cluster for production.
{{< /callout >}}

## Prepare a Hardened cluster

A complete production manifest must define all of these contracts before the first reconcile:

1. Set `profile: Hardened` and at least three replicas.
2. Configure `tls.mode: External` or `ACME` and verify the issuer, Secret, domain, and termination boundary.
3. Configure a non-static unseal provider and its workload identity or Secret references.
4. Enable self-init and provide a non-empty request list.
5. Enable operator OIDC when lifecycle Jobs will use projected JWT authentication.
6. Create at least one usable human authentication path in `selfInit.requests` before root-token revocation.
7. Set persistent storage, voter resources, and deletion policy explicitly. Size read replicas separately when used.
8. Configure and test backup identity, object storage, and restore before the first risky change.
9. Define network egress for external unseal, backup, issuer, or discovery dependencies.

Use the [configuration baseline](../../configure/) for these choices. Do not publish an incomplete `Hardened` YAML
block as if it were executable.

{{< callout type="warning" title="ProductionReady does not prove human access" >}}
`UserAccessBootstrap` is a best-effort recognition signal. `ProductionReady=True` does not prove that a human auth
method, role, identity, or network path works. Test a real human login before declaring the service ready.
{{< /callout >}}

## Troubleshoot the first reconcile

| Symptom | Check first |
| --- | --- |
| No workload resources appear | Tenant handoff RoleBinding or single-tenant `WATCH_NAMESPACE` |
| Admission rejects the profile | Required `Hardened` TLS, unseal, self-init, requests, or replica fields |
| Pods remain Pending | StorageClass, PVC events, resource availability, and placement rules |
| Pods crash or remain sealed | Generated configuration, unseal Secret or identity, TLS mounts, and events |
| `Available=False` with some Ready Pods | Desired versus ready replica count and per-Pod conditions |
| `ProductionReady=False` | The exact condition reason; do not infer it from Pod readiness alone |

Continue with [production operations](../prepare-day-2/).
