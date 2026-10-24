---
title: Supply-chain verification
description: Verify managed container images, pin successful results by digest, and understand the limits of in-cluster verification.
eyebrow: Security · Workload
weight: 6
verifiedBy:
  - api/v1alpha1/openbaocluster_configuration_types.go
  - config/policy/openbao-enforce-managed-image-digests.yaml
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/adapter/security/cluster_image_verification.go
  - internal/adapter/security/image_verifier.go
  - internal/adapter/security/workload_labels.go
  - internal/app/openbaocluster/infra_images.go
  - internal/service/backup/job.go
  - internal/service/restore/manager_running.go
  - internal/service/upgrade/image_verification.go
---

The controller resolves configured images to digests, verifies signatures against the selected trust policy, and uses
the verified digest in managed workloads. Keep the OpenBao server and operator helper images as separate trust
surfaces because they have different publishers and release identities.

## Configure both trust surfaces

| Configuration | Images covered |
| --- | --- |
| `spec.imageVerification` | OpenBao server images, including blue-green target images |
| `spec.operatorImageVerification` | Config init, backup, restore, upgrade executor, and BlueGreen validation-hook images |

{{< command label="configure" title="Require both image policies" >}}
spec:
  imageVerification:
    enabled: true
    failurePolicy: Block
    issuer: https://token.actions.githubusercontent.com
    subject: "<expected OpenBao release workflow identity>"
  operatorImageVerification:
    enabled: true
    failurePolicy: Block
    issuer: https://token.actions.githubusercontent.com
    subject: "<expected operator release workflow identity>"
{{< /command >}}

Use either a public key or a complete keyless issuer-and-subject pair. The operator supplies built-in keyless defaults
only for recognized official repositories. A mirror, fork, or internal image needs explicit trust configuration even
when it was copied from an official registry.

Hardened clusters enable both verification surfaces when the blocks are omitted and require `Block`. Custom public
keys, issuer or subject matchers, regular-expression matchers, or `ignoreTlog` require `useimagetrustroots` on the
cluster. Development permits `Warn` for staged adoption.

## Understand failure behavior

| Policy | Result |
| --- | --- |
| `Block` | Reconciliation stops for the affected workload and reports the verification failure |
| `Warn` | The error is logged and emitted as an Event; reconciliation continues with the original image reference |

Successful verification produces a `repo@sha256:...` reference. Hardened managed StatefulSets and Jobs receive a
digest-enforcement label, and admission rejects any container or init-container image that is not a SHA-256 digest.
This provides defense in depth against controller bypass or an accidental tag reintroduction.

`ignoreTlog: false` is the default. Setting it to true skips transparency-log verification and changes the trust
model; use it only as part of an explicit disconnected or private signing design.

## Separate pull credentials

`spec.imagePullSecrets` is used by kubelets. The `imagePullSecrets` inside each verification block is used by the
controller to resolve and verify a private image. In multi-tenant mode, the controller receives name-scoped `get`
access only to the verification Secrets referenced in active tenant resources.

See [Use private registries](../../configure/air-gapped/) before promoting images into a disconnected environment.

## Know what is not verified here

The in-cluster verifier does not verify the Helm chart, CRDs, operator installation image, GitHub Actions, or other
release artifacts. Verify those in the installation and release process.

Custom blue-green validation-hook images pass through `operatorImageVerification` before the Job is created. A
successful verification is pinned by digest; `Block` prevents Job creation on failure and `Warn` retains the original
reference. Hardened requires the blocking policy. The CR author also needs `usecustomexecutables`, and custom hook
publishers normally need explicit operator-image trust roots.

{{< checklist title="Artifact review" >}}
- verify the publisher identity for both OpenBao and operator helper images
- test verification against the actual registry or mirror and its authentication path
- require `Block` and inspect rendered digest references for production workloads
- include custom validation-hook publishers in `operatorImageVerification`
- validate plugin artifacts outside the operator's managed image-verification surfaces
- retain chart, CRD, image-digest, signature, and provenance evidence with the deployed release
{{< /checklist >}}
