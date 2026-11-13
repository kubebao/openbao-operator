---
title: Use private registries
description: Mirror operator, OpenBao, and helper images and configure pull and verification credentials for disconnected environments.
eyebrow: Configure · Deployment
weight: 10
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - api/v1alpha1/openbaocluster_configuration_types.go
  - api/v1alpha1/openbaorestore_types.go
  - charts/openbao-operator/templates/controller/deployment.yaml
  - charts/openbao-operator/templates/provisioner/deployment.yaml
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/platform/constants/images.go
  - internal/service/restore/job.go
---

A disconnected installation needs more than the operator image. Mirror every image that the controller can place in a
StatefulSet or lifecycle Job, then configure credentials separately for installation, workload pulls, and signature
verification.

## Mirror every image surface

| Surface | Default or override |
| --- | --- |
| Controller and provisioner | Helm `image.repository` and `image.tag` |
| OpenBao server | `RELATED_IMAGE_OPENBAO` plus `spec.version`, or `spec.image` |
| Config init and wrapper | `OPERATOR_INIT_IMAGE_REPOSITORY`, or `spec.initContainer.image` |
| Backup and pre-upgrade snapshot executor | `OPERATOR_BACKUP_IMAGE_REPOSITORY`, or `spec.backup.image` |
| Upgrade executor | `OPERATOR_UPGRADE_IMAGE_REPOSITORY`, or `spec.upgrade.image` |
| Restore executor | `OpenBaoRestore.spec.image`, then `OpenBaoCluster.spec.backup.image`, then the backup repository default |

The helper-image defaults use the running operator's `OPERATOR_VERSION` as their tag. Promote the controller,
provisioner, init, backup, and upgrade images as one operator release set. Restore does not have a separate repository
environment variable.

## Set installation defaults

Configure repository defaults on the controller, which resolves workload and helper images. The provisioner performs
tenant onboarding and does not need duplicate image-repository environment variables.

{{< command label="configure" title="Use mirrored operator and workload images" >}}
image:
  repository: registry.example.com/openbao/openbao-operator
  tag: "<operator-version>"
imagePullSecrets:
  - name: operator-registry-creds

controller:
  extraEnv:
    - name: RELATED_IMAGE_OPENBAO
      value: registry.example.com/openbao/openbao
    - name: OPERATOR_INIT_IMAGE_REPOSITORY
      value: registry.example.com/openbao/openbao-init
    - name: OPERATOR_BACKUP_IMAGE_REPOSITORY
      value: registry.example.com/openbao/openbao-backup
    - name: OPERATOR_UPGRADE_IMAGE_REPOSITORY
      value: registry.example.com/openbao/openbao-upgrade
{{< /command >}}

The chart-level pull Secret must exist in the operator namespace. It is used by both operator Deployments.

## Configure each cluster namespace

`spec.imagePullSecrets` is passed to the kubelet for OpenBao and operator-managed Job pulls. Verification credentials
are different: the controller reads the Secrets named under the two verification blocks while resolving signatures
and digests.

{{< command label="configure" title="Use a private registry for one cluster" >}}
spec:
  version: "2.6.2"
  imagePullSecrets:
    - name: registry-creds
  imageVerification:
    enabled: true
    failurePolicy: Block
    imagePullSecrets:
      - name: registry-creds
  operatorImageVerification:
    enabled: true
    failurePolicy: Block
    imagePullSecrets:
      - name: registry-creds
{{< /command >}}

Create `registry-creds` in the same namespace as the `OpenBaoCluster`. In multi-tenant mode, the provisioner grants
the controller name-scoped `get` access to verification pull Secrets. The CR author also needs `use` or `get` on a
Secret referenced by `spec.imagePullSecrets`, and `get` on verification pull Secrets.

Mirrored repositories are not recognized as the official repositories, even when their bytes are identical. Supply
an explicit public key or keyless issuer and subject for both verification blocks. In a fully disconnected
environment, decide deliberately whether your verification design can use transparency-log evidence; `ignoreTlog`
changes that trust contract and requires delegated image-trust-root authority on Hardened clusters.

{{< callout type="warning" title="Custom images are delegated executable authority" >}}
Setting a custom init, backup, upgrade, validation-hook, or plugin executable requires `usecustomexecutables` on the
target cluster. The older `usehelperimages` verb remains a compatibility alias. This permission is separate from
registry access and signature trust.
{{< /callout >}}

## Verify before disconnecting

{{< checklist title="Private-registry readiness" >}}
- mirror the controller, provisioner, OpenBao, init, backup, and upgrade images for one operator release
- create chart pull credentials in the operator namespace and workload pull credentials in every tenant namespace
- test controller-side signature verification against the mirror, not only a kubelet pull
- create an explicit restore image plan and test the restore workflow while registry access is available
- keep image digests and promotion records with the release evidence moved into the disconnected environment
{{< /checklist >}}

Continue with [supply-chain verification](../../security/supply-chain/) for trust and digest behavior.
