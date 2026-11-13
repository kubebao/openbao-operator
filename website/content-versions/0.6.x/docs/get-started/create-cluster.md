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
| `Development` | Local evaluation, CI, and disposable environments | Permits operator-managed TLS and static auto-unseal; requires Target and self-init OIDC |
| `Hardened` | Production | Requires External or ACME TLS, non-static unseal, self-init, and at least three replicas |

Declare `spec.profile`, `spec.controllerJWTMode: Target`, and self-init OIDC for every new cluster. Both profiles use
the same controller authentication requirement. The evaluation manifest creates a separate, read-only client role;
self-init revokes its bootstrap root token. See [controller JWT authentication](../../operate/controller-jwt-mode/).

Changing the word `Development` to `Hardened` does not complete the production contract.

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
     controllerJWTMode: Target
     selfInit:
       enabled: true
       oidc:
         enabled: true
       requests:
         - name: create-evaluation-policy
           operation: update
           path: sys/policies/acl/dev-evaluation
           policy:
             policy: 'path "sys/health" { capabilities = ["read"] }'
         - name: create-evaluation-role
           operation: update
           path: auth/jwt-operator/role/dev-evaluation
           data:
             role_type: jwt
             user_claim: sub
             bound_audiences: [openbao-evaluation]
             bound_subject: system:serviceaccount:openbao-demo:dev-cluster-client
             token_policies: [dev-evaluation]
             token_ttl: 10m
     tls:
       enabled: true
       mode: OperatorManaged
       rotationPeriod: "720h"
     storage:
       size: "10Gi"
     deletionPolicy: Retain
   ---
   apiVersion: v1
   kind: ServiceAccount
   metadata:
     name: dev-cluster-client
     namespace: openbao-demo
   {{< /command >}}

   Replace both `openbao-demo` namespace fields and the role's `bound_subject` together when choosing another authorized namespace. Version 2.6.x is the primary
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
   kubectl -n openbao-demo create token dev-cluster-client \
     --audience=openbao-evaluation --duration=10m | \
     kubectl -n openbao-demo exec -i -c openbao dev-cluster-0 -- sh -ec '
       IFS= read -r jwt
       export BAO_ADDR=https://127.0.0.1:8200
       export BAO_CACERT=/etc/bao/tls/ca.crt
       export BAO_TLS_SERVER_NAME=openbao-cluster-dev-cluster.local
       BAO_TOKEN="$(printf "%s" "$jwt" | bao write -field=token auth/jwt-operator/login role=dev-evaluation jwt=-)"
       export BAO_TOKEN
       bao token capabilities sys/health
     '
   {{< /command >}}

   The command must print `read`. This check proves authenticated API access through the Pod-local endpoint. The check does
   not place credentials in the exec command or expose OpenBao outside the cluster. Use
   [Expose OpenBao](../../configure/expose/) when the evaluation requires a client entry path.

{{< callout type="warning" title="Development stores sensitive material in Kubernetes Secrets" >}}
The default static auto-unseal key is stored in a Kubernetes Secret. Self-init revokes the bootstrap root token. Protect etcd encryption, RBAC,
logs, support bundles, and backups even for evaluation. Do not reuse this cluster for production.
{{< /callout >}}

## Prepare a Hardened cluster

A complete production manifest must define all of these contracts before the first reconcile:

1. Set `profile: Hardened`, `controllerJWTMode: Target`, and at least three replicas. Enable self-init OIDC.
2. Configure `tls.mode: External` or `ACME` and verify the issuer, Secret, domain, and termination boundary.
3. Configure a non-static unseal provider and its workload identity or Secret references.
4. Enable self-init and provide a non-empty request list.
5. Enable self-init OIDC for controller authentication.
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
