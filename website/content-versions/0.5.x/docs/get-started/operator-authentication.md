---
title: Configure operator authentication
description: Align Kubernetes identities, projected JWTs, OpenBao roles, and human access during bootstrap.
eyebrow: Get started · Supporting decision
weight: 7
verifiedBy:
  - charts/openbao-operator/templates/controller/deployment.yaml
  - charts/openbao-operator/values.yaml
  - internal/port/openbao/client_config.go
  - internal/port/auth/operator_jwt.go
  - internal/adapter/openbao/auth.go
  - internal/adapter/config/builder.go
  - api/v1alpha1/openbaocluster_selfinit_types.go
---

The controller, OpenBao Pods, and lifecycle Jobs use different identities. Keep each Kubernetes ServiceAccount aligned
with its projected token, OpenBao JWT role, and policy.

## Map the identities

| Actor | Kubernetes identity | OpenBao authentication | Boundary |
| --- | --- | --- | --- |
| Provisioner | Provisioner ServiceAccount in the operator namespace | None | Kubernetes RBAC only |
| Controller | Controller ServiceAccount in the operator namespace | Projected JWT bound to `openbao-operator` | Routine lifecycle and maintenance |
| OpenBao Pods | Per-cluster ServiceAccount | Runtime and unseal integrations | Server workload identity |
| Backup Job | Generated backup ServiceAccount | Projected JWT or backup token Secret | Snapshot read and object storage |
| Restore Job | Generated restore ServiceAccount | Projected JWT or restore token Secret | Destructive snapshot restore |
| Upgrade Job | Generated upgrade ServiceAccount | Projected JWT | Rolling or BlueGreen orchestration |

Changing JWT transport does not merge these identities. Each actor retains its own ServiceAccount, role, audience,
and policy.

## Understand the default JWT path

The controller Deployment mounts a projected one-hour ServiceAccount token. Its audience and
`OPENBAO_JWT_AUDIENCE` both default to `openbao-internal`. OpenBao validates the JWT against the bound audience and
the controller ServiceAccount subject.

The default `inline` strategy sends the JWT in the request-specific OpenBao inline-auth headers. Use `standard` only
when a proxy or intermediary cannot carry those headers; it performs a JWT login and sends the resulting OpenBao token
as `X-Vault-Token`.

{{< command label="configure" title="Use the standard JWT transport" >}}
kubectl -n openbao-operator-system set env \
  deployment/openbao-operator-controller \
  OPENBAO_JWT_AUTH_STRATEGY=standard
kubectl -n openbao-operator-system rollout status \
  deployment/openbao-operator-controller
{{< /command >}}

Leave the variable unset, or set it to `inline`, for the default path. The controller propagates the selected strategy
to JWT-backed backup, restore, and upgrade Jobs.

{{< callout type="warning" title="Redact inline-auth headers" >}}
`X-Vault-Inline-Auth-Parameter-jwt` contains the projected ServiceAccount credential. Redact it wherever you redact
`Authorization` and `X-Vault-Token`, including ingress, proxy, service-mesh, debug, and audit pipelines. Logging the
method, path, status, and duration is safe; logging complete request headers is not.
{{< /callout >}}

## Bootstrap operator and human access

`spec.selfInit.enabled: true` makes OpenBao execute the initialization requests and revoke the root token.
`spec.selfInit.oidc.enabled: true` adds the operator JWT auth method, role, and policies to that bootstrap. It does not
create human access.

{{< callout type="warning" title="Create human access before self-init revokes the root token" >}}
Add at least one human authentication method and its usable role or policy to `spec.selfInit.requests`. Without it, the
operator can maintain the cluster while people remain permanently locked out.
{{< /callout >}}

Self-init is one-shot. The operator uses the generated auth surface after initialization but does not continuously
reconcile OpenBao policies. A human administrator must apply later policy changes required by an operator upgrade.

For a planned snapshot recovery target whose generated ServiceAccount subjects differ from the source, configure
`spec.selfInit.oidc.additionalSubjects` on the source before self-initialization. Keep controller, backup, restore, and
upgrade subjects in their corresponding lists. The operator renders a separate exact allowlist for each role and does
not combine their policies. See [Restore a snapshot](../../operate/restore/#preserve-lifecycle-jwt-identities) for the
recovery sequence and the separate JWT trust requirement.

## Configure a manual controller role

Use manual JWT configuration only for a controlled bootstrap or a custom install that cannot use self-init OIDC.
Render the operator installation first, then substitute the actual namespace, ServiceAccount, and audience.

{{< command label="configure" title="Bind a custom controller identity" >}}
bao write auth/jwt-operator/role/openbao-operator \
  role_type=jwt \
  bound_audiences=openbao-internal \
  user_claim=sub \
  bound_subject=system:serviceaccount:platform-security:demo-openbao-operator-controller \
  token_policies=openbao-operator \
  token_ttl=1h \
  token_max_ttl=1h \
  token_no_default_policy=true
{{< /command >}}

The controller policy needs the steady-state maintenance paths:

{{< command label="configure" title="Define the controller policy" >}}
path "sys/health" {
  capabilities = ["read"]
}

path "sys/step-down" {
  capabilities = ["sudo", "update"]
}

path "sys/storage/raft/configuration" {
  capabilities = ["read"]
}

path "sys/storage/raft/remove-peer" {
  capabilities = ["update"]
}

path "sys/storage/raft/autopilot/configuration" {
  capabilities = ["read", "update"]
}

path "sys/storage/raft/autopilot/state" {
  capabilities = ["read"]
}
{{< /command >}}

Do not add backup, restore, or upgrade permissions to this policy. Those belong to job-specific roles.

## Verify a custom installation

Check these values together:

1. The rendered controller ServiceAccount name and operator namespace.
2. The Deployment's projected `openbao-token` volume and one-hour expiration.
3. The projected audience and `OPENBAO_JWT_AUDIENCE`.
4. The JWT role's `bound_audiences` and `bound_subject`.
5. The controller's reachability to the configured OIDC discovery or JWKS endpoint.
6. The separate ServiceAccounts and JWT roles generated for lifecycle Jobs.

## Troubleshoot authentication

| Symptom | Likely cause | Check first |
| --- | --- | --- |
| Controller receives `permission denied` immediately | Audience or bound-subject mismatch | Rendered controller identity and JWT role |
| Self-init finishes but operator auth does not settle | Discovery or JWKS cannot be reached or validated | Issuer, discovery URL, CA, and network path |
| Backup or restore auth fails while the controller works | Job-specific identity or role is missing | The Job's ServiceAccount and JWT role |
| A custom namespace or prefix breaks auth | OpenBao still trusts the default subject | All rendered identity references and `bound_subject` |
| A recovery target works for its first restore but later lifecycle Jobs fail | The restored source roles do not trust the target subjects | Source-side `additionalSubjects` and the restored `jwt-operator` roles |

If JWT login succeeds but a specific request is denied, continue with [operator authorization](../operator-authorization/).
