---
title: API reference
description: Generated fields, defaults, and validation for the OpenBao Operator custom resources.
eyebrow: Reference
weight: 1
verifiedBy:
  - api/v1alpha1 at 0.7.0
  - website/generated/api-reference.md at 0.7.0
---

Use these pages when you need the exact field name, type, default, or validation rule for an operator custom resource.

The repository generates the source reference from `api/v1alpha1`. The synchronization step pins the 0.7 release source ref
and writes one page per top-level resource so each page has a useful table of contents and unique anchors.

{{< callout type="warning" title="Apply the matching CRDs" >}}
This reference is prepared for 0.7.0; final release qualification is pending. After publication, apply the CRDs
from the matching 0.7.0 release for a new installation. Existing 0.6 installations must use the
[API group migration](../../operate/api-group-migration/). Do not combine this schema with an older operator.
{{< /callout >}}
