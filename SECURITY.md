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

## Scanner applicability assessments

Scorecard uses OSV to match dependency versions and vendored source fingerprints. The following exceptions
expire on 20 January 2027. Review the source, production dependency graph, and workflow usage before renewal.
The production `make vulncheck` gate remains enabled and has no ignored advisory IDs.

| Finding | Assessment | Policy source |
| --- | --- | --- |
| [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) | The advisory affects deprecated `golang.org/x/crypto/openpgp` packages. Rekor 1.5.4 uses the maintained ProtonMail fork. The deprecated packages are absent from vendor and the production dependency graph. | [Root OSV policy](osv-scanner.toml) |
| [CVE-2025-8556](https://osv.dev/CVE-2025-8556) | OSV fingerprints the aggregate `vendor/github.com` directory as CIRCL 1.3.3. The actual module and vendor tree use 1.6.3, after the 1.6.1 fix. The affected FourQ package is absent. | [Vendored-source OSV policy](hack/security/osv/vendor-github.toml) |
| Three Requests advisories and two Python TUF advisories | These match `requests==2.31.0` and `tuf==3.1.0` in the Go TUF dependency's upstream `requirements-test.txt`. The operator's builds, tests, images, and CI do not install or execute that manifest. | [Upstream test OSV policy](hack/security/osv/go-tuf.toml) |

Each exception names a specific advisory and applies beside its matched manifest or directory. A root policy
does not apply to child directories. First-party Python manifests and the CIRCL version in `go.mod` retain
their normal vulnerability checks. Reassess an exception if its source, use, version, or advisory scope changes.

Run `make vendor` after changing Go dependencies. It regenerates upstream vendor files and copies the two
project-owned policies from `hack/security/osv/` to their scanner discovery locations. Do not edit the copied
policies or upstream dependency files by hand. `make verify-vendor` verifies both generated dependencies and policies.
