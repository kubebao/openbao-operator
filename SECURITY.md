# Security Policy

## Reporting a Vulnerability

Please do **not** open public GitHub issues for security-sensitive reports.

Instead, use GitHub Security Advisories:

- https://github.com/kubebao/openbao-operator/security/advisories/new

If you are unable to use GitHub Security Advisories, open a minimal issue that requests a private contact channel and do not include exploit details.

## Supported Versions

Security fixes are provided for:

- The latest released version.

## Supply Chain

Release artifacts (container images and OCI Helm chart) are published to GHCR and are signed using keyless Sigstore signing via GitHub Actions OIDC.

For trusted root maintenance used by keyless verification, see `website/content/contribute/release.md`.

## Known dependency caveats

In this operator, we **pin** Sigstore trusted root material (either embedded `internal/adapter/security/trusted_root.json` or a user-provided ConfigMap) and pass it to Cosign for both keyless verification and Rekor transparency log verification. This avoids fetching/updating trusted root data via TUF at runtime. The only TUF fetch logic in this repository is `internal/adapter/security/fetch_trusted_root.go`, which is build-ignored and intended for maintainers to refresh the pinned `trusted_root.json`.
