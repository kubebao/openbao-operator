# Documentation standard

The OpenBao Operator documentation describes supported behavior for people who install, configure, secure, and operate
OpenBao on Kubernetes. Write for the reader's task and verify product claims against the applicable code or release.

This standard follows Google's guidance for [clear technical writing](https://developers.google.com/tech-writing/one),
[procedures](https://developers.google.com/style/procedures), [headings](https://developers.google.com/style/headings),
[voice](https://developers.google.com/style/voice), [person](https://developers.google.com/style/person), and
[prescriptive language](https://developers.google.com/style/prescriptive-documentation).

## Organize documentation by task

Use the existing information architecture:

- `Get started` helps readers choose a deployment model, install the operator, and create a cluster.
- `Configure` owns cluster configuration and integration tasks.
- `Operate` owns production readiness, maintenance, backup, upgrade, recovery, restore, and decommissioning.
- `Security` explains trust, authority, admission, workload, supply-chain, and tenant boundaries.
- `Reference` records exact API, compatibility, lifecycle, status, and limitation contracts.
- `Architecture` explains durable component and lifecycle boundaries.
- `Project`, `Releases`, and `Contribute` remain outside the operator task flow.

Add a page only when it gives a task or contract a clear home. Prefer extending an existing page when a new page would
repeat prerequisites, warnings, or explanations already owned by that task.

## Write directly

- State the outcome before background or rationale.
- Use active voice, second person, present tense, and direct verbs.
- Use sentence case for headings.
- Start task headings with a base-form verb. Use noun phrases for concept and reference headings.
- Keep one idea in each sentence and one topic in each paragraph.
- Put prerequisites before the procedure.
- Present one recommended path first. Put genuine alternatives in a separate section.
- Use `must` for requirements, `can` for permission or capability, and `might` for possibility.
- Remove slogans, repeated navigation, commentary about the page, and implementation history that does not affect the
  current contract.

## Write complete procedures

Use numbered steps for ordered work. Start each step with an imperative and keep one primary action in each step.
Introduce commands with the action and expected effect. Explain placeholders before readers copy the command, and
state the observable result afterward.

Examples must be internally complete for the task they claim to perform. Label partial policy, manifest, or
configuration fragments as fragments. Do not present schema validation, reconciliation success, or one status field as
proof that authentication, authorization, recovery, or an external integration works.

Use explicit placeholders such as `<namespace>` and `<cluster>`. Keep shell examples safe to paste after substitution.
Avoid commands that overwrite operator-owned resources, expose credentials, or imply that a destructive operation is
reversible.

## Preserve operational boundaries

Keep requirements and warnings that protect security, data, availability, or access. A shorter page must not hide:

- authority and ownership boundaries;
- trust roots, credentials, or secret custody;
- destructive or irreversible effects;
- compatibility and version constraints;
- required network paths and external dependencies;
- backup, restore, and rollback prerequisites; or
- the difference between API acceptance, controller observation, and end-to-end readiness.

Use a warning callout only when ignoring it can cause material harm. Use a note for scope, ownership, limitations, and
other context. Do not use callouts as decoration.

## Verify product claims

Behavioral pages use the `verifiedBy` frontmatter field to identify the narrowest authoritative repository surfaces.
Choose evidence in this order:

1. API fields and defaults: Go API types, generated CRDs, and admission policy.
2. Helm behavior: values, schema, templates, and chart tests.
3. Runtime behavior: controllers, application services, unit tests, integration tests, and end-to-end tests.
4. Commands and examples: current samples, release artifacts, workflow configuration, and rendered output.
5. Compatibility and release claims: the validation matrix, release tag, and release automation.

Do not copy a claim merely because an older page contained it. When code and documentation disagree, document the
supported runtime behavior or resolve the product contract before publication. Track unresolved product work in the
issue or pull-request workflow.

Use concise frontmatter:

```yaml
---
title: Configure an integration
description: State the result and the important boundary in one sentence.
eyebrow: Configure
weight: 10
verifiedBy:
  - api/v1alpha1/example_types.go
  - internal/service/example
---
```

Use `aliases` only for intentional route continuity. Hugo does not render `verifiedBy`; it exists for review and
maintenance.

## Maintain version lines

The unprefixed route is the current stable minor line, `/next/` tracks unreleased `main`, and supported older minor
lines retain an explicit prefix. Patch releases update their minor line instead of creating another documentation
tree.

Apply a behavior change only to the lines that contain it:

- update `website/content-versions/next/` for unreleased behavior on `main`;
- update `website/content/` when preparing the next stable minor line;
- update an existing stable line for supported patch behavior; and
- keep release history in release notes instead of preserving obsolete instructions in task pages.

Pin generated API content through `website/data/version_lines.yaml`. Never generate a stable reference from a newer
schema or copy a `next` claim into stable documentation without release evidence.

## Use the smallest useful presentation

Prefer Markdown, short tables, and ordinary links. Use Hugo shortcodes only when their semantics improve scanning:

- `command` for a named command sequence;
- `callout` for a warning or bounded note; and
- `decision-table` for a genuine comparison across consistent dimensions.

Use relative links or Hugo `relref` links for internal content. Link to the most authoritative external source. Keep
link text descriptive and avoid duplicating navigation in page prose.

Layouts and components must work without JavaScript for primary content and navigation. Preserve keyboard access,
visible focus, meaningful labels, sufficient contrast, responsive tables and code blocks, and unique element IDs.

Add documentation pages to `data/navigation.yaml`. Use nested `items` for child pages, such as individual API resources.
The sidebar resolves entries against the selected documentation version and omits pages that do not exist in that
version. `make docs-build` checks navigation coverage and rejects sidebar links that cross documentation versions.

## Keep reference deployments executable

Environment-specific deployment recipes belong in a separate executable reference repository with pinned versions,
manifests, automated conformance checks, and retained evidence. The manual owns reusable product contracts and must not
describe an environment recipe as validated without reproducible evidence.

## Definition of done

A documentation change is complete when:

- the page has one clear task or contract;
- behavioral claims cite current `verifiedBy` evidence for the applicable version line;
- examples are complete, safe, and aligned with the API and runtime;
- security, data, availability, access, and compatibility boundaries remain explicit;
- headings, navigation, links, anchors, search, and responsive layout work in the rendered site;
- generated reference content is synchronized; and
- `make docs-build` plus the relevant code, chart, or workflow checks pass.
