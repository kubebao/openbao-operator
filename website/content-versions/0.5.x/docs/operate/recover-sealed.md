---
title: Recover a sealed cluster
description: Isolate seal, credential, trust, and network failures without replacing recovery material.
eyebrow: Operate · Recovery
weight: 7
verifiedBy:
  - api/v1alpha1/openbaocluster_unseal_types.go
  - api/v1alpha1/openbaocluster_types.go
  - internal/service/bootstrap/config.go
  - internal/service/bootstrap/config_test.go
  - internal/service/bootstrap/unseal_validation.go
  - internal/service/workloadidentity/cloud_unseal.go
  - internal/controller/openbaocluster/status_cloud_unseal_identity.go
---

A running but sealed Pod cannot serve requests. Identify the configured seal first, then repair its existing credential,
trust, or network path. Do not create new seal material as a trial fix.

## Confirm the failure

{{< command label="inspect" title="Read seal and dependency signals" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{.message}{"\n"}{end}'
kubectl -n <namespace> get openbaocluster <name> -o yaml | yq '.spec.unseal'
kubectl -n <namespace> exec <pod-name> -- bao status
kubectl -n <namespace> logs <pod-name> -c openbao
{{< /command >}}

`OpenBaoSealed` is derived from OpenBao's service-registration label on Pods; use `bao status` to confirm. If the
cluster is unsealed but has no leader, switch to [Recover from no leader](../recover-no-leader/).

## Diagnose the selected seal

| Seal | Check first |
| --- | --- |
| `static` or omitted | Exact generated `<cluster>-unseal-key` Secret, its ownership, and its `key` data |
| `transit` | Credentials Secret, transit policy and key, HTTPS trust and SNI, DNS, and egress |
| `awskms`, `gcpckms`, `azurekeyvault`, `ocikms` | `CloudUnsealIdentityReady`, workload ServiceAccount binding, decrypt permission, DNS, and egress |
| `kmip` | Client certificate, key and CA files, server name, and endpoint reachability |
| `pkcs11` | Vendor library and dependent library paths, token or slot, PIN material, and device access |

For external seals, compare the Secret keys and mounted file paths with [Configure unseal](../../configure/unseal/).
Look for `permission denied`, `x509`, `AccessDenied`, and timeout errors before rotating any credential.

## Recover the static-seal Secret

The default static seal uses an operator-generated immutable Secret named `<cluster>-unseal-key`. Its `key` contains
32 random bytes mounted at the configured static-seal path.

{{< callout type="danger" title="Do not replace the static-seal key" >}}
A newly generated value cannot decrypt state sealed by the original value. The Secret is immutable by design. Restore
the exact original Secret bytes from your protected recovery copy; do not run `kubectl create secret` with a new key.
{{< /callout >}}

Inspect metadata and presence without printing Secret data into terminal history:

{{< command label="inspect" title="Check the static-seal Secret" >}}
kubectl -n <namespace> get secret <name>-unseal-key -o json | \
  jq '{name: .metadata.name, immutable: .immutable, keys: (.data | keys)}'
{{< /command >}}

If the Secret is missing, pause the cluster and restore the original Secret with the ownership metadata required by
the target `OpenBaoCluster`. If that material is lost, a Raft snapshot alone does not recreate the missing seal key.

## Verify recovery

After repairing the dependency, restart through `spec.runtime.restartAt` only if the process must reload mounted
material. Then verify every voter:

{{< command label="verify" title="Confirm unseal and service state" >}}
kubectl -n <namespace> get pods -l openbao.org/cluster=<name>
kubectl -n <namespace> exec <pod-name> -- bao status
kubectl -n <namespace> exec <pod-name> -- bao operator raft list-peers
{{< /command >}}

`raft list-peers` requires an authenticated OpenBao session with Raft configuration read access. Use your approved
interactive login path; do not put a privileged token in the command line or shell history.

Finish when all intended voters are unsealed and Ready, a leader exists, and `Available=True`. A manual
`bao operator unseal` prompt is not a generic escape hatch for the operator's auto-seal modes; repair the configured
seal or recover its original material.
