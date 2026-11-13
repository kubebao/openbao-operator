# Pinned release-please CLI

The exact-version release wrapper uses release-please 17.11.2 on Node.js 24 in CI. Its package requires Node.js 22
or newer. Install the locked dependencies with `npm ci --ignore-scripts --prefix hack/ci/release-please`.
Run `node hack/ci/release-please/test-dependencies.cjs` to check file discovery through the real consumer with a
local repository-tree fixture. The exact-version workflow runs this check after installation.

The scoped override selects minimatch 11.0.1 for `@google-automations/git-file-utils` to address
[GHSA-g3hv-75wg-m249](https://github.com/isaacs/minimatch/security/advisories/GHSA-g3hv-75wg-m249).
The upstream parent still requests minimatch 10.x. Keep the release-please version and wrapper compatibility pin
aligned. Remove the override when the parent accepts a fixed version and the locked dependency tree remains clear
of the advisory.
