---
title: Upgrade OpenBao to 2.7
description: Prepare external seal and application plugins before upgrading OpenBao to 2.7.
weight: 35
---

OpenBao 2.7 removes several built-in seal, authentication, and secrets plugins.
Prepare their replacements before changing `spec.version` and `spec.image`.
Validate the exact image, plugin version, platform, and upgrade strategy in staging.

## Prepare seal plugins

OpenBao 2.7 requires external KMS plugins for `awskms`, `azurekeyvault`, `gcpckms`,
`ocikms`, and `pkcs11`. The operator retains these typed `spec.unseal` settings,
including credential Secret and workload identity wiring. Add a `spec.plugins`
entry with `type: kms` and a `name` equal to `spec.unseal.type`.

The operator blocks workload preparation and upgrade execution when a removed
built-in seal has no matching plugin declaration. This check verifies the declared
configuration; it does not verify that the plugin can contact the provider or
unwrap the existing root key.

The `static`, `transit`, and `kmip` seals remain built in. They do not require a
plugin declaration.

1. Back up the cluster and verify the restore procedure with the existing seal.
2. Select and pin the replacement plugin. Keep the existing seal provider, key,
   and configuration. Grant the manifest author permission to configure custom
   executables, as required by the operator's admission policy.
3. Install the plugin on OpenBao 2.6 first. Declare `version`, `binaryName`, and
   the plugin binary's `sha256sum` while using 2.6. For OCI downloads, set
   `spec.configuration.plugin.autoDownload: true`. For command-based plugins,
   include the executable under `/openbao/plugins` in the OpenBao image.
4. Configure registry and provider egress. Automatic OCI downloads occur inside
   the OpenBao container. Kubernetes `imagePullSecrets` do not configure that
   downloader. For a private registry, use a supported image that supplies the
   downloader credentials, or include the plugin binary in the image.
5. Replace Pods in staging and verify that they unseal with the existing data.
   Downloaded plugins use an `emptyDir` cache and must be available again when a
   Pod is replaced. Include registry unavailability in the recovery assessment.
6. Change both `spec.version` and `spec.image` to the selected 2.7 release. Observe
   the upgrade conditions, quorum, authentication, and reads and writes.
7. Take and restore a new snapshot in an isolated staging cluster. Verify plugin
   availability and unseal after Pod replacement there as well.

Do not disable an existing authentication or secrets mount to migrate its plugin.
Disabling a mount deletes its data.

## Declare plugins on OpenBao 2.7

For OCI plugins, 2.7 can infer the version from the image tag and the binary name
from image metadata. A manifest digest can replace the binary checksum:

```yaml
spec:
  version: "2.7.0"
  configuration:
    plugin:
      autoDownload: true
      autoRegister: true
      downloadBehavior: fail
  plugins:
    - type: kms
      name: awskms
      image: ghcr.io/openbao/openbao-plugin-kms-awskms:<plugin-version>@sha256:<manifest-digest>
  unseal:
    type: awskms
    awskms:
      region: eu-west-1
      kmsKeyID: <existing-key-arn>
```

Replace the placeholders with the selected plugin release, verified manifest
digest, and existing KMS key. Supply credentials through the existing workload
identity or `spec.unseal.credentialsSecretRef` configuration.

When an OCI reference has no tag, set `version`. When it has no digest, set
`sha256sum` to the checksum of the extracted binary, not the image digest.
Earlier OpenBao versions still require all three explicit plugin fields.

Command-based KMS plugins on 2.7 can omit `version`, `binaryName`, and `sha256sum`.
The command must stay within the plugin directory. Checksums remain available
when the deployment requires binary verification.

OpenBao 2.7 defaults `plugin_auto_register` to true. Set
`spec.configuration.plugin.autoRegister` when the deployment requires a fixed
registration policy across versions. KMS plugins are declared through server
configuration and do not use API-based registration.

## Migrate PKCS#11 images

The `openbao-hsm` distribution ends with OpenBao 2.6. Use the standard OpenBao
image with the PKCS#11 KMS plugin and the vendor library and runtime dependencies.
The operator's typed PKCS#11 settings continue to supply the existing runtime
files, environment variables, and credential Secret references.

PKCS#11 plugin v0.1.0 contains the implementation shipped in OpenBao 2.6.x.
Plugin v0.2.0 changes session handling and adds External Keys support. Upstream
warns that a downgrade to v0.1.0 is not possible after v0.2.0 rewraps the root key.
Qualify that plugin upgrade separately against the actual HSM.
See the [PKCS#11 v0.2.0 release notes](https://github.com/openbao/openbao-plugins/releases/tag/kms-pkcs11-v0.2.0).

## Inventory application plugins

Before upgrading, inventory authentication and secrets mounts in every namespace.
The built-in LDAP, Kerberos, and RADIUS authentication plugins and LDAP secrets
plugin are removed in 2.7. Install their external replacements on every Pod and
verify existing mounts, authentication, lease operations, and restarts in staging.
The operator does not discover these mounts from `OpenBaoCluster` configuration.

The existing pre-2.6 to 2.6-or-newer BlueGreen restriction remains in place until
specific version pairs pass lifecycle qualification. A request-forwarding fix
does not establish datastore downgrade compatibility. Use the backup and restore
procedure for recovery when an in-place downgrade is not supported.

See the [OpenBao 2.7 release notes](https://github.com/openbao/openbao/releases/tag/v2.7.0)
and [plugin upgrade guide](https://openbao.org/docs/guides/upgrade/plugins/).
