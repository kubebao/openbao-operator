---
title: Secrets and credentials
description: Understand which Kubernetes Secrets the operator creates or reads, who can access them, and what survives deletion.
eyebrow: Security · Fundamentals
weight: 3
verifiedBy:
  - internal/adapter/cluster/rbac.go
  - internal/app/openbaocluster/deletionops/handler.go
  - internal/service/bootstrap/config.go
  - internal/service/certs/manager.go
  - internal/service/init/manager_initialize.go
  - internal/service/init/manager_root_token.go
  - internal/service/provisioner/manager_secret_rbac.go
---

Kubernetes Secrets support bootstrap, TLS, external unseal, registry access, and lifecycle workflows. They are part of
the security boundary, not a substitute for OpenBao. Enable Kubernetes storage encryption, restrict Secret access,
and keep credentials out of manifests, logs, events, and status.

## Know the generated secrets

| Secret | Created when | Lifecycle |
| --- | --- | --- |
| `<cluster>-unseal-key` | Static unseal is selected or defaulted | Immutable; operator-owned; contains a 32-byte static seal key |
| `<cluster>-root-token` | Standard initialization is used | Immutable; written only after a dry-run storage preflight |
| `<cluster>-tls-ca` and `<cluster>-tls-server` | TLS is `OperatorManaged` | Issued and rotated by the operator |

Self-init revokes its bootstrap root token and does not create `<cluster>-root-token`. A non-static unseal provider
moves the unseal root of trust to that provider; the Kubernetes credentials used to reach it still need their own
lifecycle and access review. Hardened clusters require both self-init and a non-static unseal provider.

Use [Initialize the cluster](../../configure/initialize/), [Configure unseal](../../configure/unseal/), and
[Expose OpenBao](../../configure/expose/) for those workflows.

## Treat referenced secrets as external dependencies

The cluster and restore APIs can reference same-namespace Secrets for unseal credentials, backup and restore tokens
or object storage, image verification, Ingress TLS, and monitoring authentication. The controller reads only the
material its workflow needs; other references are consumed by kubelets, lifecycle Jobs, the edge, or the monitoring
system. The operator does not rotate user-provided credentials. Assign an owner, rotation procedure, expiry signal,
and recovery procedure to each reference.

`spec.imagePullSecrets` is a kubelet reference and requires the CR author to have `use` or `get`. Verification pull
Secrets are read by the controller and require `get`. See [Use private registries](../../configure/air-gapped/) for the
complete split.

## Understand access in multi-tenant mode

The base tenant Role has no Secret permissions. The provisioner derives separate, name-scoped reader and writer Roles
from the active `OpenBaoCluster` and `OpenBaoRestore` resources. Reader permissions are removed when no resource
references those Secret names.

Kubernetes RBAC cannot restrict Secret `create` by `resourceNames`; the writer Role therefore has collection-scoped
create while get, update, patch, and delete remain name-scoped. Admission restricts the controller to recognized,
operator-owned Secret names and provenance. Ordinary cluster-editor access must not include broad Secret reads.

## Plan deletion and recovery

`deletionPolicy: Retain` removes only the deleting `OpenBaoCluster` owner reference from the generated unseal-key and
root-token Secrets before cluster cleanup. It preserves unrelated owner references and metadata. The policy does not
retain every generated or referenced Secret. TLS Secrets and user-managed credential Secrets follow their own owners
and policies.

{{< callout type="warning" title="Retention preserves sensitive bootstrap material" >}}
Retaining an unseal key or root token improves recoverability but leaves a high-value credential in Kubernetes.
Inventory and remove retained material deliberately after recovery or decommissioning; do not assume cluster deletion
made it harmless.
{{< /callout >}}

{{< checklist title="Secret review" >}}
- confirm self-init means no root-token Secret is expected
- list every Secret referenced by the cluster, restore objects, monitoring, and registry verification
- test the effective controller and tenant-user permissions with `kubectl auth can-i`
- verify external credential rotation does not require editing generated workloads
- record which unseal and root-token Secrets must survive a retained deletion
{{< /checklist >}}
