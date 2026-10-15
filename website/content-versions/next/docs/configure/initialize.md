---
title: Initialize the cluster
description: Define self-init requests, usable human access, operator JWT authentication, and recovery-key custody.
eyebrow: Configure · Bootstrap
weight: 2
verifiedBy:
  - api/v1alpha1/openbaocluster_selfinit_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/adapter/config/selfinit_render.go
  - internal/adapter/config/selfinit_gohcl.go
  - internal/controller/openbaocluster/status_user_access_bootstrap.go
  - internal/service/init/manager_reconcile.go
  - internal/port/auth/operator_jwt.go
---

Use self-init to apply one-time bootstrap requests and revoke the bootstrap root token. Before enabling it, define a
complete human login path and the recovery procedure for that path.

Self-initialization request `headers` and `when` require OpenBao 2.6.0 or newer. The operator rejects these
fields for older versions because those servers can ignore them, including a condition that prevents a request.

## Choose the initialization path

| Path | Use it when | Root-token behavior |
| --- | --- | --- |
| Self-init | Required for `Hardened`; preferred when bootstrap state is declarative | OpenBao revokes the bootstrap root token after the requests complete; the operator does not create a root-token Secret |
| Standard init | Disposable Development clusters and controlled compatibility work | The operator can store the root token in an immutable Kubernetes Secret |

Self-init is a one-time bootstrap mechanism. It does not continuously reconcile OpenBao auth methods, policies,
secret engines, audit devices, or upstream workflow definitions.

{{< callout type="warning" title="A non-empty request list does not prove access" >}}
Admission checks that `selfInit.requests` is non-empty. It does not prove that a user, role, credential, policy binding,
or network path works. If the requests do not create usable access, recovery requires an established generate-root
procedure or cluster recreation.
{{< /callout >}}

## Define access before self-init

Bootstrap these surfaces together:

1. Choose the human authentication method and external identity owner.
2. Add requests that enable the auth method, configure it, create the policy, and bind a role or user to that policy.
3. Add any secret engines and audit devices required at first login.
4. Enable `selfInit.oidc` if the operator and lifecycle Jobs will authenticate with projected Kubernetes JWTs.
5. Configure initial recovery keys when a non-static auto-unseal cluster needs a generate-root recovery path.

`selfInit.oidc` is only for operator lifecycle access. It does not create a human login path.

This fragment shows the structured request forms. It is not a complete access configuration because every identity
provider has different issuer, claim, redirect, group, and role requirements.

{{< command label="configure" title="Build the self-init request list" >}}
spec:
  selfInit:
    enabled: true
    oidc:
      enabled: true
    requests:
      - name: enable-human-auth
        operation: update
        path: sys/auth/<human-auth-mount>
        authMethod:
          type: <jwt-or-kubernetes-or-other-supported-type>

      - name: create-platform-policy
        operation: update
        path: sys/policies/acl/platform-operator
        policy:
          policy: |
            <least-privilege-policy>

      - name: configure-human-auth
        operation: update
        path: auth/<human-auth-mount>/config
        data:
          <provider-specific-non-secret-configuration>

      - name: bind-human-role
        operation: update
        path: auth/<human-auth-mount>/role/<role-name>
        data:
          <provider-specific-role-and-policy-binding>
{{< /command >}}

Replace every placeholder. Test the same issuer, claims, group mapping, redirect URIs, and policy binding in a disposable
cluster before using the manifest for production.

{{< callout type="warning" title="Do not store credentials in request data" >}}
The `OpenBaoCluster` and its self-init request payloads are stored in Kubernetes etcd. Do not put passwords, tokens,
private keys, or unseal material in `data` or other request fields.
{{< /callout >}}

## Use the structured request fields

Each request has a unique `name`, an `operation`, and an OpenBao API `path`. Use at most 64 requests; request names
must be unique and paths can contain at most 256 characters.

| Field | Use it for |
| --- | --- |
| `authMethod` | Enable an auth mount through `sys/auth/*` |
| `policy` | Create or update an ACL policy through `sys/policies/*` |
| `secretEngine` | Enable a mount through `sys/mounts/*` |
| `auditDevice` | Enable an audit device through `sys/audit/*` |
| `data` | Send an object payload when no structured field exists |
| `dataFrom` | Supply top-level string fields from same-namespace Secret keys during first initialization |
| `headers` | Add request-specific HTTP headers as `map[string][]string` |
| `when` | Execute conditionally from a boolean or OpenBao profile value object |
| `allowFailure` | Continue initialization after an optional request fails |

Supported operations are `create`, `read`, `update`, `patch`, `delete`, and `list`.

Use `allowFailure` only for genuinely optional state. An authentication, policy, audit, or recovery request that is
part of the access contract must fail the bootstrap when it cannot be applied.

### Supply Secret inputs

OpenBao 2.6.0 and later can read bootstrap values from files. Use `dataFrom` to
project selected Secret keys into a raw self-init request:

```yaml
spec:
  selfInit:
    enabled: true
    requests:
      - name: enable-human-oidc
        operation: update
        path: sys/auth/human
        authMethod:
          type: oidc
      - name: configure-human-oidc
        operation: update
        path: auth/human/config
        data:
          oidc_discovery_url: https://login.microsoftonline.com/<tenant-id>/v2.0
          oidc_client_id: <application-id>
          default_role: platform-admin
        dataFrom:
          oidc_client_secret:
            secretKeyRef:
              name: openbao-entra-oidc
              key: client-secret
```

This fragment enables and configures the auth mount. Add the role, redirect URIs,
claim mappings, and policies required for your human login path.

Create the Secret in the `OpenBaoCluster` namespace. The identity that submits
the cluster must be authorized to `get` each referenced Secret. References to
operator system Secrets are rejected. The controller does not read Secret
values or receive additional Secret read permissions for these inputs.

Each `dataFrom` entry supplies one string without trimming whitespace or parsing
JSON. Field names must match `^[A-Za-z_][A-Za-z0-9_-]*$`. A field cannot occur in
both `data` and `dataFrom`; the operator rejects overlaps before creating the
workload. Use at most 64 inputs per request. Structured request fields and paths
under `sys/auth/`, `sys/audit/`, `sys/mounts/`, and `sys/policies/` do not support
`dataFrom`.

All requests together can reference at most two distinct Secrets. Multiple keys
and requests can share either Secret. This limit keeps name-scoped authorization
checks within Kubernetes' CEL evaluation budget.

Declare the mappings and enable self-init when you create the cluster. Keep
self-init enabled until initialization completes. The mappings, including their
request names, field names, Secret names, and keys, are immutable after cluster
creation. To repair a missing input, create or update the referenced Secret and
key. This preserves the file projections in the existing StatefulSet template.

The init container checks that every selected file is readable before OpenBao
starts initialization. Missing Secrets or keys block startup, including inputs
on requests with `when: false` or `allowFailure: true`. Keep the Secret unchanged
until bootstrap completes. OpenBao reads the values from read-only files; the
values do not enter the generated ConfigMaps or rendered configuration.

After initialization, the operator removes these projections from future Pod
templates. Completed bootstrap configuration also permits startup from a stale
Pod template when the Secret is absent. The operator does not delete the source
Secret. You can delete it after verifying successful bootstrap and human login.
Cluster updates that preserve the completed bootstrap request list do not require
read access to its bootstrap Secrets.

Updating the Secret after initialization does not rotate credentials in OpenBao.
Use an authenticated OpenBao configuration change for rotation. Deleting the
Kubernetes Secret does not revoke the credential at the identity provider.
OpenBao TRACE logging includes sensitive request and response data; use normal
log levels when bootstrapping credentials.

### Gate profile-engine requests

OpenBao 2.6.0 and later evaluate self-init through the profile engine. Use `headers` and `when` only with versions that
support those request fields:

{{< command label="configure" title="Gate an optional self-init request" >}}
- name: configure-optional-system
  operation: update
  path: sys/config/example
  headers:
    X-Bootstrap-Source:
      - operator
  when:
    eval_source: cel
    eval_type: bool
    expression: "true"
  data:
    enabled: true
{{< /command >}}

`when` can also be a JSON boolean. Admission rejects enabled requests that use either control with a `spec.version`
older than 2.6.0. These controls remain one-time bootstrap inputs; they do not make the operator an
owner of OpenBao workflow definitions.

## Configure operator JWT authentication

Enable operator OIDC bootstrap inside the complete self-init block:

{{< command label="configure" title="Add operator OIDC bootstrap" >}}
spec:
  selfInit:
    enabled: true
    oidc:
      enabled: true
      # Optional compatibility overrides:
      # issuer: "https://<kubernetes-issuer>"
      # audience: "openbao-internal"
    requests:
      - <complete-human-access-request-set>
{{< /command >}}

The operator creates lifecycle roles for controller, backup, upgrade, and restore work as needed. Keep these values
aligned:

- The operator ServiceAccount must be allowed to read the Kubernetes OIDC discovery and JWKS non-resource URLs.
- `selfInit.oidc.audience`, when set, must match the installation-scoped `OPENBAO_JWT_AUDIENCE`. It cannot define a
  per-cluster audience. Use `spec.controllerJWTMode: Target` for target-specific controller credentials.
- Manually managed roles must bind the rendered controller and Job ServiceAccount identities, not guessed defaults.

Review [operator authentication](../../get-started/operator-authentication/) and
[operator authorization](../../get-started/operator-authorization/) before replacing any generated role.

## Create initial recovery keys

Use `spec.recoveryKeys.initial` only with self-init and a non-static unseal provider. Set `threshold` no higher than
`shares`, and provide exactly one recipient for every share.

{{< command label="configure" title="Declare initial recovery-key custody" >}}
spec:
  recoveryKeys:
    initial:
      shares: 3
      threshold: 2
      recipients:
        - name: platform-custodian
          fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"
          pgpPublicKey: "<base64-encoded-binary-openpgp-public-key>"
        - name: security-custodian
          fingerprint: "89ABCDEF0123456789ABCDEF0123456789ABCDEF"
          pgpPublicKey: "<base64-encoded-binary-openpgp-public-key>"
        - name: recovery-custodian
          fingerprint: "FEDCBA9876543210FEDCBA9876543210FEDCBA98"
          pgpPublicKey: "<base64-encoded-binary-openpgp-public-key>"
{{< /command >}}

The operator renders an authenticated `sys/rotate/recovery/init` request with `backup=true`. It does not distribute
encrypted shares, store decrypted shares, escrow key material, or run generate-root ceremonies.

Verify every fingerprint out of band. Retrieve the encrypted backup through an approved access path, confirm each
custodian can decrypt their share, record custody evidence, and remove the temporary backup from OpenBao.

## Verify bootstrap and access

1. Wait for the operator to observe self-initialization.

   {{< command label="verify" title="Check self-init status" >}}
   kubectl -n <namespace> get openbaocluster <name> \
     -o jsonpath='{.status.selfInitialized}{"\n"}'
   {{< /command >}}

2. Inspect `UserAccessBootstrap`, `ProductionReady`, and the other status conditions.

   {{< command label="inspect" title="Inspect bootstrap conditions" >}}
   kubectl -n <namespace> get openbaocluster <name> \
     -o jsonpath='{range .status.conditions[*]}{.type}={.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'
   {{< /command >}}

3. Sign in through the human authentication method with a non-bootstrap identity.
4. Read one allowed path and confirm one disallowed path is denied.
5. If operator OIDC is enabled, run a lifecycle authentication check before depending on backup or upgrade Jobs.
6. Complete and record the recovery-share custody test.

`status.selfInitialized: true` means the operator observed an initialized and unsealed cluster. The user-access
condition is a best-effort recognition signal. Neither one performs a real human login or authorization test.

Continue with [unseal](../unseal/).
