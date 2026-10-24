---
title: Configure unseal
description: Choose the unseal trust root and meet the exact workload-identity, Secret, file, or HSM contract.
eyebrow: Configure · Trust root
weight: 3
verifiedBy:
  - api/v1alpha1/openbaocluster_unseal_types.go
  - api/v1alpha1/openbaocluster_configuration_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/service/bootstrap/config.go
  - internal/service/bootstrap/unseal_validation_cloud.go
  - internal/service/bootstrap/unseal_validation_secret_refs.go
  - internal/service/bootstrap/unseal_validation_transit.go
  - internal/service/workload/seal_wiring.go
  - internal/port/openbao/tls_contract.go
  - internal/adapter/config/render_seal.go
---

Choose an unseal provider whose root of trust survives loss or compromise of the Kubernetes cluster. `Hardened`
requires a non-static provider. `Development` can use the operator-managed static key.

## Choose the trust root

| Type | Use it when | Primary credential path |
| --- | --- | --- |
| `awskms`, `gcpckms`, `azurekeyvault` | The cloud platform provides the external key service | Prefer workload identity; use a Secret only when needed |
| `transit` | A separate OpenBao cluster owns the wrapping key | Namespace-local Secret for the token and optional mTLS files |
| `ocikms` | OCI KMS is the external trust source | Principal identity or an OCI SDK config Secret |
| `kmip` | An enterprise key manager exposes KMIP | Secret-mounted client certificate, key, and optional CA |
| `kms` | An OpenBao 2.6+ KMS plugin owns the wrapping operation | Declared KMS plugin; Hardened does not permit plugin configuration entries |
| `pkcs11` | An HSM and vendor library are available in the OpenBao image | Secret-backed PIN and runtime environment or configuration files |
| `static` | The cluster is disposable and uses `Development` | Operator-generated immutable Kubernetes Secret |

Supported types are `static`, `awskms`, `gcpckms`, `azurekeyvault`, `transit`, `kmip`, `kms`, `ocikms`, and `pkcs11`.

## Apply the universal Secret contract

When `spec.unseal.credentialsSecretRef` is set:

- the Secret must exist in the same namespace as the `OpenBaoCluster`;
- the user applying the cluster must be authorized to `get` that Secret;
- the name cannot match operator-managed system Secret suffixes such as `-unseal-key`, `-root-token`, `-tls-ca`, or
  `-tls-server`;
- every referenced file under `/etc/bao/seal-creds` maps to a Secret key with the same filename;
- missing, empty, invalid JSON, invalid PEM, or mismatched certificate material is rejected where the provider contract
  can validate it.

Use workload identity instead of long-lived cloud keys when the provider and platform support it. Inline credential
fields remain API-supported for `Development`, but they are stored in the `OpenBaoCluster` and therefore in etcd.

{{< command label="reference" title="Map mounted paths to Secret keys" >}}
spec:
  unseal:
    credentialsSecretRef:
      name: unseal-creds
    transit:
      tlsCACert: /etc/bao/seal-creds/ca.crt
      tlsClientCert: /etc/bao/seal-creds/client.crt
      tlsClientKey: /etc/bao/seal-creds/client.key
{{< /command >}}

This fragment requires `unseal-creds` keys named `ca.crt`, `client.crt`, and `client.key`.

## Keep Hardened credentials out of the custom resource

Hardened admission rejects these fields when they contain values:

| Provider | Rejected field | Use instead |
| --- | --- | --- |
| AWS KMS | `spec.unseal.awskms.secretKey` and `sessionToken` | Workload identity or `credentialsSecretRef` keys `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` |
| Azure Key Vault | `spec.unseal.azureKeyVault.clientSecret` | Managed identity, workload identity, or the `AZURE_CLIENT_SECRET` key in `credentialsSecretRef` |
| Transit | `spec.unseal.transit.token` | The `token` key in `credentialsSecretRef` |
| PKCS#11 | `spec.unseal.pkcs11.pin` | The `BAO_HSM_PIN` key in `credentialsSecretRef` |
| KMS plugin | Any non-empty `spec.unseal.kms.config` map | Omit the map; use plugin defaults, external identity, or Secret files under `/etc/bao/seal-creds` |

{{< callout type="warning" title="Migrate existing Hardened objects before upgrade" >}}
Create the replacement Secret or workload identity before you install the new admission policy. Submit one
`OpenBaoCluster` update that adds the replacement and removes each prohibited inline field. The API server rejects
later updates while any prohibited field remains in the submitted object.
{{< /callout >}}

## Meet the provider credential contract

| Provider | Secret keys or mounted-file requirements |
| --- | --- |
| AWS KMS | `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`; `AWS_SESSION_TOKEN` is optional. Omit the Secret for IRSA or the standard AWS credential chain. |
| GCP Cloud KMS | A valid JSON key, normally `credentials.json`, when not using Workload Identity or Application Default Credentials. |
| Azure Key Vault | `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, and `AZURE_CLIENT_SECRET`; optional environment and resource keys are also wired. Omit the Secret for managed identity or Azure Workload Identity. |
| Transit | Hardened requires the `token` Secret key. Development can use an inline token. Add files referenced by `tlsCACert`, `tlsClientCert`, and `tlsClientKey`; the client certificate and key must be set together. |
| OCI KMS API key | `config` with a `[DEFAULT]` profile and the key file named by `key_file`. Both files must be under `/etc/bao/seal-creds`. |
| KMIP | Files named by `clientCert`, `clientKey`, and optional `caCert`. The client certificate and key must match. |
| KMS plugin | All Secret keys are mounted under `/etc/bao/seal-creds`. Hardened requires an empty `spec.unseal.kms.config`; the plugin must use defaults, external identity, or fixed Secret-file paths. |
| PKCS#11 | `BAO_HSM_PIN` when `pin` is omitted, plus every key referenced by `runtime.env` and `runtime.fileEnv`. |

## Configure a cloud KMS

Use a workload identity on the OpenBao ServiceAccount and omit the credential Secret. This AWS fragment shows the
shape; GCP and Azure use their platform-specific ServiceAccount annotations or Pod labels.

{{< command label="configure" title="Use AWS KMS with workload identity" >}}
spec:
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: "arn:aws:iam::<account>:role/<openbao-kms-role>"
  unseal:
    type: awskms
    awskms:
      region: eu-west-1
      kmsKeyID: "arn:aws:kms:eu-west-1:<account>:key/<key-id>"
{{< /command >}}

Grant only the KMS operations required by the provider. Confirm the workload identity can use the key before relying on
it for a production bootstrap. Lifecycle Jobs have separate ServiceAccounts and do not inherit the main Pod's cloud
identity automatically.

## Configure transit

Transit addresses must be absolute HTTPS URLs. The operator rejects userinfo, query strings, fragments, localhost,
loopback, link-local, unspecified, scoped-IP, and ambiguous numeric-host forms.

{{< command label="configure" title="Use transit with Secret-backed credentials" >}}
spec:
  unseal:
    type: transit
    credentialsSecretRef:
      name: transit-unseal
    transit:
      address: "https://transit.example.com:8200"
      keyName: openbao-unseal
      mountPath: transit
      tlsCACert: /etc/bao/seal-creds/ca.crt
{{< /command >}}

The Secret must contain `token` and `ca.crt`. Use an orphan or periodic token with only the transit encrypt and decrypt
permissions. Set both `tlsClientCert` and `tlsClientKey` when using client-certificate authentication.

## OpenBao 2.7 seal plugins

OpenBao 2.7 requires a matching `spec.plugins` KMS declaration for the typed
AWS, Azure, GCP, OCI, and PKCS#11 seal settings. The operator blocks these
configurations without a matching plugin. Follow the [2.7 migration procedure]({{< relref "/docs/operate/openbao-270" >}})
before upgrading an existing cluster.

## Configure a plugin-backed KMS seal

OpenBao 2.6.0 and later can use a plugin catalog entry with `type: kms` as the seal implementation. The unseal
configuration must reference the declared plugin by name. `Development` can pass plugin attributes through
`spec.unseal.kms.config`. The next fragment uses that Development-only path:

{{< command label="configure" title="Use a KMS seal plugin in Development" >}}
spec:
  profile: Development
  version: "2.6.2"
  configuration:
    plugin:
      autoDownload: true
      downloadBehavior: fail
  plugins:
    - type: kms
      name: corp-kms
      image: registry.example.com/openbao-kms-corp
      version: v0.1.0
      binaryName: openbao-kms-corp
      sha256sum: "<64-character-sha256>"
  unseal:
    type: kms
    credentialsSecretRef:
      name: corp-kms-runtime
    kms:
      pluginName: corp-kms
      config:
        endpoint: "https://kms-broker.example.com:8443"
        ca_file: "/etc/bao/seal-creds/ca.crt"
{{< /command >}}

Do not use this `kms.config` fragment with `Hardened`. A KMS plugin used with `Hardened` must work without entries in
that map. It can use plugin defaults, external identity, or fixed files mounted from `credentialsSecretRef` under
`/etc/bao/seal-creds`.

In `Development`, config keys must be valid HCL identifiers. The values are stored in the `OpenBaoCluster`. Use paths
to mounted Secret files for sensitive material. The operator verifies the plugin reference and renders string
attributes, but the plugin owns the meaning of its config. With a command-based plugin, the binary must already exist
in the OpenBao image before startup.

## Configure KMIP

Create and activate the wrapping key in the KMIP system before the OpenBao cluster starts. Grant the client identity
encrypt and decrypt operations. Issue a client certificate with `clientAuth` extended key usage and ensure the server
certificate matches `serverName`.

{{< command label="configure" title="Use KMIP with mTLS files" >}}
spec:
  unseal:
    type: kmip
    credentialsSecretRef:
      name: kmip-client
    kmip:
      endpoint: kmip.example.com:5696
      kmsKeyID: "1"
      clientCert: /etc/bao/seal-creds/client.crt
      clientKey: /etc/bao/seal-creds/client.key
      caCert: /etc/bao/seal-creds/ca.crt
      serverName: kmip.example.com
      encryptAlg: AES_GCM
{{< /command >}}

The Secret must contain `client.crt`, `client.key`, and `ca.crt`. Set `tls12Ciphers` only when the appliance requires a
specific TLS 1.2 suite.

## Configure PKCS#11

Prepare the HSM and runtime before creating the cluster:

- build an OpenBao image with HSM support, the vendor PKCS#11 module, and dependent libraries;
- create the wrapping key through vendor tooling;
- choose `slot` or `tokenLabel`, but not both;
- choose a mechanism compatible with the HSM object type;
- store the PIN and vendor runtime material in `credentialsSecretRef`;
- use `runtime.libraryPath`, `runtime.env`, and `runtime.fileEnv` instead of a custom wrapper script.

{{< command label="configure" title="Wire a PKCS#11 runtime" >}}
spec:
  image: registry.example.com/openbao-hsm-vendor:2.6.2
  unseal:
    type: pkcs11
    credentialsSecretRef:
      name: pkcs11-runtime
    pkcs11:
      lib: /usr/local/lib/libpkcs11.so
      tokenLabel: OpenBao
      keyLabel: bao-root-key-aes
      mechanism: AES_GCM
      runtime:
        libraryPath: /usr/local/lib
        env:
          - name: CRYPTOSERVER
            secretKey: cryptoserver
        fileEnv:
          - name: CS_PKCS11_R3_CFG
            secretKey: cs_pkcs11_R3.cfg
{{< /command >}}

The Secret must contain `BAO_HSM_PIN`, `cryptoserver`, and `cs_pkcs11_R3.cfg`. Only `fileEnv` keys are mounted as files.
The wrapper fails early when the library is missing or points to a directory.

## Protect the static key

When unseal is omitted or set to `static`, the operator creates `<cluster-name>-unseal-key` with data key `key`. The
Secret is immutable and carries owner proof for the `OpenBaoCluster`.

Do not pre-create, replace, patch, or delete that Secret. An unowned pre-existing Secret is rejected, and a replacement
key cannot decrypt existing data. Back up and retain the operator-owned Secret according to the disposable cluster's
recovery requirements.

## Account for private ACME trust

When `spec.configuration.acmeCARoot` points under `/etc/bao/seal-creds`, the unseal credentials Secret must also contain
`pki-ca.crt`. The operator and helper clients use that fixed filename to trust the private ACME issuer during probes and
day-2 operations.

## Verify unseal readiness

{{< command label="verify" title="Inspect unseal and identity conditions" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'

kubectl -n <namespace> get pods \
  -l openbao.org/cluster=<name>
{{< /command >}}

Fix the first provider, Secret, identity, TLS, or network prerequisite reported by the operator. Do not bypass a failed
unseal validation by weakening the profile.

Continue with [storage](../storage/).
