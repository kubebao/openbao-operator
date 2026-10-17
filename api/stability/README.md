# API stability inventory

`v1alpha1.yaml` records the approved 0.5.0 stability decision for every user-facing
`spec` and `status` path in the three served CRDs. It is a release-planning contract,
not a declaration that `v1alpha1` is already stable.

Its `baseline: 0.4.2` and `release: 0.5.0` preserve the approved migration's
provenance. The operator-upgrade E2E harness uses these fields to select its
release notes and migration fixtures. They do not select the schema checker's
compatibility baseline, which advances independently after a supported release.

The inventory uses inherited path rules. Every top-level field requires an explicit
rule, while nested fields inherit the closest matching rule unless a more specific
decision overrides it. The checker expands those rules against the generated CRD
schemas, including schema roots, array items, map values, composed branches, and
Kubernetes merge semantics. It rejects unclassified top-level fields and requires
every schema-deprecated field to have an explicit `deprecated` rule.

`v1alpha1-paths.tsv` is the deterministic resolved contract. It ensures nested fields
cannot appear, disappear, or change their schema facts or inherited decision without a
reviewable snapshot diff.

The resolved review table derives field purpose, requiredness, defaults, and structural
validation directly from the generated schema. The policy file adds ownership,
semantic omission behavior where it differs from the schema, mutability, stable status
values, enforcement layers, module interaction, migration posture, and the approved
stability decision. `grow-only` and `transition-guarded` distinguish constrained writes
from freely mutable fields; `request-token` identifies changed values that trigger a
one-shot operator action.

Classifications have the following meanings:

- `beta-stable`: intended to retain its name, type, omission/default semantics,
  meaning, and mutability under the approved 0.5.0 freeze
- `additive-unfrozen`: supported current surface whose beta commitment still needs
  a later decision; compatible additions may continue
- `likely-move`: known candidate for validation or shape work before beta
- `deprecated`: compatibility field with a documented replacement
- `stable-automation`: status intended for machine consumers; message prose and
  expanding reason sets remain outside this guarantee
- `informational`: status intended for observation and debugging rather than a
  durable automation contract

Run the completeness check with:

```sh
make verify-api-stability-inventory
```

After an intentional inventory or CRD change, update and review the resolved snapshot:

```sh
make update-api-stability-inventory
```

Render the fully resolved field table for review with:

```sh
go run ./hack/tools/api_inventory --format markdown
```

## CRD compatibility gate

`baselines/0.5.2.json` is the supported baseline. It records the normalized schemas
from the [released 0.5.2 CRD bundle](https://github.com/kubebao/openbao-operator/releases/download/0.5.2/crds.yaml),
whose SHA-256 digest is `d72469358ae4b6dc1264c98b61671226ddcefca1a73952ead877a6910ac2ea69`.
Keep the older baselines for historical comparisons. Runtime upgrade qualification
starts from 0.5.2; the separate 0.5.0-to-0.5.1 API migration fixtures remain unchanged.

Run the inventory and compatibility gate with:

```sh
make verify-api-contract
```

The gate rejects breaking and review-required schema changes, including field
removal, validation tightening, and changed defaults. Updating the inventory
snapshot does not authorize an incompatible schema change. The approved 0.5.0
field decisions remain in effect; this gate does not graduate `v1alpha1` to beta.

The checker recognizes the exact `OpenBaoCluster/v1alpha1` `rotationPeriod` presence-guard rewrite as compatible.
It preserves which inputs are accepted and replaces a missing-key evaluation error with the existing required-field
message. The match includes the resource, schema path, rule text, and rule metadata. Other CEL changes still require
semantic review. `TestCRD_OpenBaoCluster_TLSRotationPeriod` verifies the API-server behavior.

The checker also recognizes the exact fresh-restore validation rules. These rules
accept all requests that omit the new optional target and cleanup
fields. `TestCRD_FreshRestoreOptions` checks this boundary against the API server.
Changes to these rules still require semantic review.

The Service IP-family rules are also matched exactly, including their resource, paths, rule text, and metadata.
They accept every released main or read-replica Service configuration because those requests omit both new optional
fields. `TestCRD_ServiceIPFamilies` verifies legacy requests and the new combinations against the API server. Other
changes to Service validation remain subject to the compatibility gate.

The `API Contract` job runs for API, CRD, checker, gate-test, dependency, build-rule,
and gate-workflow changes, every push to `main`, and manual CI runs. `CI Required`
includes its result. Edge candidate builds and release image builds also depend
on this gate. Release validation verifies generated artifacts before checking
the API contract and labels the report with the release tag. Other runs use `next`.

For a diagnostic report that does not reject schema differences, run:

```sh
make report-crd-compatibility
```

`verify-api-contract` always enforces compatibility. The former
`CRD_COMPAT_MODE=report` override cannot weaken this gate. The Go checker also
defaults to enforcement; use `--mode report` for a direct diagnostic invocation.

After a supported release, download its published `crds.yaml` and generate the
next baseline from that asset. Replace `<release>` with the published tag and
`/path/to/released/crds.yaml` with the downloaded asset path:

```sh
go run ./hack/tools/crd_compatibility \
  --write-baseline api/stability/baselines/<release>.json \
  --baseline-bundle /path/to/released/crds.yaml --release <release>
```

Review the recorded digest and update the checker default. Preserve the inventory's
approved migration metadata unless a new migration plan, release notes, and upgrade
fixtures are reviewed together. Run `make verify-api-contract verify-operator-upgrade-e2e`
to check both contracts. Never replace a released baseline with schemas generated
from the current checkout. A planned incompatible change requires a reviewed
migration and compatibility-policy change before integration.
