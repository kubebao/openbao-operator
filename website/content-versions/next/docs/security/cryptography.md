---
title: Cryptography policy
description: Review minimum key sizes, TLS cipher policy, certificate validation, and external-provider boundaries.
eyebrow: Security · Fundamentals
weight: 8
verifiedBy:
  - internal/platform/cryptopolicy/keys.go
  - internal/platform/cryptopolicy/tls.go
  - internal/platform/kubernetestls/config.go
  - internal/platform/openbaotls/validation.go
  - internal/adapter/probe/prober.go
  - internal/adapter/security/image_verifier_key_policy.go
---

The operator applies a shared key-strength and TLS policy to its network clients, metrics endpoint, and imported
verification material. Prepare existing endpoints and certificates before upgrading if they depend on smaller RSA
keys or TLS 1.2 CBC suites.

## Use supported public keys

| Algorithm | Minimum accepted key |
| --- | --- |
| RSA | 2048-bit modulus |
| ECDSA | 224-bit curve; generated CA and server keys use P-256 |
| Ed25519 | Standard 32-byte public-key encoding |

Unsupported key types fail validation. The checks use the parsed public key, including for custom Sigstore trust
roots; a metadata label does not establish key strength.

The policy covers imported CA bundles and server certificates, transit and KMIP client-certificate material,
Kubernetes client certificates, raw and certificate-backed JWKs, image-verification keys and trust roots, and keys in
TLS peer chains. Keyless image verification also checks the signing certificate and its verified chain.

Metrics TLS checks supplied certificates when selecting them for a handshake. A rotated certificate that fails the
policy causes the handshake to fail. Replace the certificate with a supported key to restore metrics access.

## Require ephemeral AEAD suites

Operator TLS connections require TLS 1.2 or later. TLS 1.2 permits ECDHE with AES-GCM or ChaCha20-Poly1305. CBC,
static RSA key exchange, RC4, and 3DES are excluded. TLS 1.3 retains Go's cipher suites and key-exchange defaults.
This policy follows the modern cipher and RSA-key recommendations in [RFC 9325](https://www.rfc-editor.org/rfc/rfc9325.html).

The policy applies to OpenBao API clients, Kubernetes and OIDC discovery clients, registry requests, object-storage
clients and their credential HTTP requests, health probes, and the metrics server. Kubernetes clients retain
client-go's CA and client-certificate rotation.

Normal certificate-chain and hostname verification still apply. An emulator's `InsecureSkipVerify` setting does not
disable the key-size or cipher policy. There is no compatibility switch for RSA keys below 2048 bits or CBC-only
TLS 1.2 endpoints.

## Bound the ACME acquisition exception

During ACME certificate acquisition, an enabled loopback probe fallback can accept a temporary certificate without
CA or hostname authentication. It checks certificate dates and key strength. Every dial must resolve to a loopback
IP address, and probes reject HTTP redirects.

This exception does not establish the server's identity. It is limited to local health requests during acquisition;
ordinary external and operator-managed TLS probes retain normal authentication.

## Keep external-provider policy separate

OpenBao owns its server listeners and provider connections. The operator validates supplied unseal CA and
client-certificate material, but it does not select every cipher or generate the remote keys inside a KMS or HSM.
Configure those services and OpenBao's provider settings against their own supported security policies.

Provider-issued opaque credentials need an owner, rotation procedure, and revocation process. A byte-length check
cannot establish the strength of an opaque credential. See [Secrets and credentials](../secrets/) and
[Configure unseal](../../configure/unseal/) for those responsibilities.

Before upgrading:

1. If an RSA certificate or verification key uses a modulus below 2048 bits, replace it. Include CA certificates in active chains.
2. Configure each TLS 1.2 peer to support ECDHE with AES-GCM or ChaCha20-Poly1305, or enable TLS 1.3.
3. Check metrics certificate rotation and the operator's connections to storage, registries, identity endpoints, and OpenBao.
