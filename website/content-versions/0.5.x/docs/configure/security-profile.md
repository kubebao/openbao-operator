---
title: Choose a security profile
description: Select Development for disposable evaluation or meet the enforced Hardened contract for production.
eyebrow: Configure · Security
weight: 1
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - api/v1alpha1/openbaocluster_configuration_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/platform/hardenedcontract/catalog.go
  - internal/platform/hardenedcontract/contract.go
  - internal/port/security/image_verification.go
---

Set `spec.profile` explicitly. Use `Development` only for disposable evaluation. Use `Hardened` for production after
every required trust, identity, storage, and network dependency is ready.

## Compare the profiles

| Surface | `Development` | `Hardened` |
| --- | --- | --- |
| Intended use | Local evaluation, CI, and disposable environments | Production and production-like validation |
| Replicas | One or more | At least three voters |
| TLS | Operator-managed, External, or ACME | External or ACME, with TLS enabled |
| Unseal | Static or external | Explicit non-static provider |
| Initialization | Standard init or self-init | Self-init with a non-empty request list |
| Root token | Can be stored in an operator-managed Secret | Self-init revokes the bootstrap root token |
| Image verification | Optional; `Warn` or `Block` when enabled | Enabled for OpenBao and helper images; `Warn` and explicit disablement are rejected |
| Network and runtime escape hatches | Permitted where the API supports them | Broad or insecure forms are rejected |

`Development` is not a less complete spelling of `Hardened`. The profile changes admission, runtime readiness, image
verification, bootstrap credential handling, and Raft Autopilot defaults.

## Create a disposable evaluation cluster

This manifest is complete enough for the default operator contract when the namespace is onboarded and a default
StorageClass exists.

{{< command label="configure" title="Declare a Development cluster" >}}
apiVersion: openbao.org/v1alpha1
kind: OpenBaoCluster
metadata:
  name: dev-cluster
  namespace: openbao-demo
spec:
  version: "2.6.2"
  profile: Development
  replicas: 1
  tls:
    enabled: true
    mode: OperatorManaged
    rotationPeriod: "720h"
  storage:
    size: "10Gi"
  deletionPolicy: Retain
{{< /command >}}

The operator defaults an omitted unseal configuration to static and creates the unseal key. Standard initialization
can also create an immutable root-token Secret. Protect Kubernetes Secrets, etcd encryption, logs, support bundles,
and backups even for evaluation.

{{< callout type="warning" title="Do not promote an evaluation cluster in place" >}}
Changing only `profile: Development` to `profile: Hardened` does not establish the external trust, usable human access,
storage, recovery, and network contracts required for production. Build and test the full baseline first.
{{< /callout >}}

## Meet the Hardened baseline

Before you create a `Hardened` cluster, define all of these fields and dependencies:

1. Set `profile: Hardened`, at least three voters, a supported OpenBao version, and persistent storage.
2. Set `tls.enabled: true` and choose `tls.mode: External` or `ACME`.
3. Configure an explicit non-static unseal provider. Use workload identity or a namespace-local Secret for credentials.
4. Enable self-init and provide at least one request.
5. Create a usable human authentication method, policy, and role before self-init revokes the bootstrap credential.
6. Enable operator OIDC bootstrap when backup, upgrade, or restore Jobs will use projected JWT authentication.
7. Configure external identity and egress for unseal, issuers, object storage, and other dependencies.
8. Test a real login, recovery-key custody, backup, restore, and loss of a voter before go-live.

The exact TLS and unseal fields depend on the provider. Use [initialize the cluster](../initialize/) and
[configure unseal](../unseal/) to build those parts. Do not publish an incomplete provider-neutral manifest as an
executable production example.

## Understand enforced restrictions

Hardened admission rejects these configurations:

- TLS disablement, operator-managed TLS, and transit `tlsSkipVerify`;
- static unseal; inline transit tokens; inline AWS secret keys and session tokens; inline Azure client secrets; inline
  PKCS#11 PINs; and non-empty KMS plugin configuration maps;
- fewer than three voters;
- self-init without requests;
- disabled image verification or `failurePolicy: Warn` for OpenBao or operator helper images;
- root user or group overrides, root supplemental groups, unconfined seccomp, sysctls, and Windows pod options;
- listener TLS disablement and the dangerous runtime flags `detectDeadlocks`, `rawStorageEndpoint`,
  `introspectionEndpoint`, and `unsafeAllowAPIAuditCreation`;
- raw `network.ingressRules`, wildcard trusted peers, and egress rules without explicit peers and ports;
- backup storage without an explicit Secret, workload identity, or S3 role ARN;
- insecure backup or ServiceMonitor TLS, and Gateway backend HTTP unless TLS passthrough is enabled.

When backup or pre-upgrade snapshots are enabled, Hardened also requires non-empty egress rules. Custom image trust
roots and custom executables require their delegated API permissions.

{{< callout type="warning" title="Migrate inline unseal credentials before upgrade" >}}
The API server evaluates the full `OpenBaoCluster` on each create or update. After this admission policy is installed,
it rejects an update if the submitted Hardened object still contains a prohibited inline field. Create the replacement
Secret or workload identity first. Then submit one update that adds the replacement and removes the inline field.
{{< /callout >}}

## Use image verification defaults deliberately

When both verification blocks are omitted from a Hardened cluster, the operator enables verification for the main
OpenBao image and operator helper images. Official images use the built-in official keyless identity defaults. Unknown
or mirrored repositories need an explicit public key or keyless issuer and subject.

Use separate configuration blocks because helper-image verification does not inherit the main-image settings:

{{< command label="configure" title="Set explicit image verification" >}}
spec:
  imageVerification:
    enabled: true
    failurePolicy: Block
    issuer: "https://token.actions.githubusercontent.com"
    subject: "<expected OpenBao release workflow identity>"
  operatorImageVerification:
    enabled: true
    failurePolicy: Block
    issuer: "https://token.actions.githubusercontent.com"
    subject: "<expected operator release workflow identity>"
{{< /command >}}

Only add explicit trust identities after verifying the exact image publisher. In Hardened, warning-only verification is
not a transition mode; admission rejects it.

## Add AppArmor only when the platform supports it

Set `spec.workloadHardening.appArmorEnabled: true` to request the runtime-default AppArmor profile on OpenBao
StatefulSets and the built-in backup and upgrade Jobs. Restore and custom validation-hook Jobs do not currently
receive this setting. It remains opt-in because unsupported nodes would leave the affected workload unschedulable;
use platform policy for the remaining Job surfaces.

## Verify the effective profile

{{< command label="verify" title="Inspect profile and readiness" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.spec.profile}{"\n"}{range .status.conditions[*]}{.type}={.status}{"\t"}{.reason}{"\n"}{end}'
{{< /command >}}

Read the exact failed condition and admission message. `ProductionReady=True` is useful operator evidence, but it does
not prove that a human login or external dependency works end to end.

Continue with [initialization](../initialize/).
