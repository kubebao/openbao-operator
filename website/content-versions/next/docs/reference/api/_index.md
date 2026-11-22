---
title: API reference
description: Generated fields, defaults, and validation for the OpenBao Operator custom resources.
eyebrow: Reference
weight: 1
verifiedBy:
  - api/v1alpha1
  - website/generated/api-reference.md
---

Use these pages when you need the exact field name, type, default, or validation rule for an operator custom resource.

The repository generates the source reference from `api/v1alpha1`. The synchronization step pins the Next source ref
and writes one page per top-level resource so each page has a useful table of contents and unique anchors.

{{< callout type="warning" title="Apply the matching CRDs" >}}
The API reference follows the checked-out generated source on `main`, including local changes. Apply CRDs generated
from that same checkout. Use the edge channel only when the checkout is clean and `git rev-parse HEAD` matches
the `sha` in `metadata.json`. The edge publisher can briefly lag a newly merged commit. Do not combine this schema
with a stable operator release.
{{< /callout >}}
