# Documentation website

This directory contains the canonical OpenBao Operator documentation site. Hugo builds the complete stable,
planned-minor, and `next` documentation lines without a JavaScript package or browser-bundle dependency graph.

The site uses a task-oriented product structure:

- minor-line documentation organized by operator work
- native Hugo version routing with `next` for `main`
- release notes as the entry point for patch-level history
- repository-owned Hugo layouts and CSS without a third-party theme
- build-generated local search with a small browser script
- Hugo shortcodes for the few structured patterns that improve scanning
- no Node.js, package manager, package lock, browser bundle, or npm dependency graph

## Run locally

```sh
./website/scripts/sync-api-reference.sh --all --check

# The devenv shell provides the repository-pinned Hugo version.
devenv shell make docs-serve
```

Open <http://127.0.0.1:1313/openbao-operator/>.

## Build strictly

```sh
devenv shell make docs-build
```

The API sync reads each line's exact `sourceRef` from `data/version_lines.yaml` and splits its generated reference into
one Hugo page per custom resource. The 0.4.x line retains three release-specific runtime errata; later lines publish
their matching generated source directly. A first-stable line can declare a qualified `fallbackSourceRef` so pre-tag
checks generate the final-tag pages before that tag exists. The tagged build uses `sourceRef` and must produce the same
content. The generated site is written to `website/public/` and is ignored by Git.

The redirect post-processing step writes version-aware compatibility pages into that generated destination. Its
declarative policy and validation instructions live under `redirects/`.

## Version policy

- The unprefixed site is the current stable `0.5.x` line through OpenBao Operator 0.5.1.
- `/0.4.x/` retains the previous stable 0.4 minor-line contract through 0.4.2.
- `/next/` tracks unreleased behavior on `main`. It must never be presented as a stable production contract.
- `/latest/` and its retained suffix routes are compatibility redirects to the equivalent unprefixed stable pages.
  They do not track `main` and are not another editable content tree.
- Patch releases update their existing minor line. A 0.5.1 release updates `0.5.x`; it does not create a `0.5.1`
  documentation route.

Before publishing a minor release, copy the reviewed `next` contract into its stable minor line, replace the source
commit with the final release tag, regenerate the API reference, and make that line the default. Afterward, advance
`next` from `main` without changing the stable snapshot.

## Source and publication boundary

Hugo content under `content/` and `content-versions/` is the only hand-written documentation source. Generated API
source lives under `generated/`; `make api-reference` refreshes it from `api/v1alpha1`. Do not recreate a parallel
manual or edit generated pages directly.

Pages and release workflows publish the validated `website/public/` artifact while preserving the independently
published `edge/` and `nightly/` channel directories. The executable reference-deployments repository remains a
separate future project.

See [EDITORIAL.md](EDITORIAL.md) for the authoring, verification, versioning, and quality standard.
