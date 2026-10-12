---
title: Test a change
description: Choose the lowest-cost test layer that proves the changed behavior, then expand with risk.
eyebrow: Contribute
weight: 3
verifiedBy:
  - Makefile
  - test/e2e/suites.yaml
  - hack/perf/v2/scenarios.yaml
---

Start at the lowest-cost layer that can prove the contract. Move upward when the behavior depends on Kubernetes API semantics, controller wiring, or a real workload.

| Change | First useful layer | What it proves |
| --- | --- | --- |
| Pure Go logic, parsers, renderers, helpers | Unit tests | Deterministic in-process behavior |
| Builders, manifests, patches, fake-client contracts | Unit and focused package tests | Emitted resource shape without API-server semantics |
| Reconciliation, finalizers, status, admission, defaulting | EnvTest integration | Real API-server behavior |
| Networking, storage, upgrades, backup, restore, workload startup | Kind E2E | Controller and workload behavior in a cluster |
| Platform compatibility, disaster recovery, performance | Scheduled or focused environment validation | Evidence for environment-specific assumptions |

The controller-runtime fake client is not a replacement for the API server. Use EnvTest when the test depends on validation, defaulting, subresources, watches, cache wiring, `Generation`, or `ResourceVersion`.

{{< command label="verify" title="Run the baseline before review" >}}
devenv test
devenv tasks run operator:bootstrap
devenv tasks run operator:doctor
devenv tasks run operator:ci-core
{{< /command >}}

## Run specialized lanes

- Use `make fuzz` for parsers, renderers, auth, or normalization code. Set `FUZZTIME` or `FUZZ_TARGET_FILTER` for a focused run.
- Use `make verify-perf` for changes that may affect reconcile cost, convergence, startup, or lifecycle timing.
- Use `make test-e2e-existing` for OpenShift and other platform-specific checks that Kind cannot reproduce.
- Use `make test-e2e-operator-upgrade` when controllers, CRDs, Helm, profiles, or migrations must preserve resources made by the prior stable operator.
- Use the manual HSM lane for KMIP and PKCS#11 paths that require test-only provider fixtures.

E2E suites are declared in `test/e2e/suites.yaml`. Update the suite owner, risk tier, isolation class, labels, coverage tags, CI lane, nightly policy, and parallelism whenever an E2E spec changes ownership or scope. Then run `make verify-e2e-manifest`.

Set `E2E_FAIL_ON_EMPTY=true` with label filters. Write Ginkgo JSON and JUnit reports so a failed lane retains selected specs, failure details, and slow-test evidence.

```sh
make test-e2e-ci \
  E2E_LABEL_FILTER='lifecycle && !openshift' \
  E2E_JUNIT_REPORT=artifacts/e2e-reports/local/junit.xml \
  E2E_JSON_REPORT=artifacts/e2e-reports/local/ginkgo.json \
  E2E_FAIL_ON_EMPTY=true
```

Continue with [CI routing]({{< relref "/contribute/ci.md" >}}).

## Diagnose performance findings

Inspect the scenario artifacts before changing a performance baseline. Warmup scenario errors remain failures in the summary,
even though warmup measurements do not enter the performance comparison. A scenario with no measured samples cannot pass.

The rolling-upgrade scenario writes an `availabilityProbes` JSONL artifact. Each probe records its timestamp,
duration, HTTP status or transport error, and availability result. Failed HTTP responses retain up to 4 KiB of health
or proxy response data. Failed probes also capture Pod readiness,
deletion state, and public Service EndpointSlices. Errors while collecting this state remain separate from the probe result.
The zero-failure availability gate remains in effect.

OpenBao Services select server Pods with `openbao.org/component=openbaocluster`.
Helper Job Pods use other component values and do not serve OpenBao endpoints.

The restore scenario owns its source cluster and snapshot fixture. After observing a successful submission receipt,
the harness acknowledges administrator `Resume` and waits for managed Pod replacement and hold release.
`restore_total_seconds` measures the interval from the restore request to recovery release.
The restore can remain `Unknown`: this measurement does not confirm snapshot application.
The scenario records submission acceptance, recovery acknowledgement, and recovery release as separate phases.

## Policy approval and repair

Run `make test-policy-reconciliation-openbao` with Docker available. The test starts a disposable OpenBao dev server
on loopback and exercises the production policy client and reconciliation manager. It checks exact-content authorization,
rejected extra parameters and legacy paths, deleted-policy repair, unchanged-policy writes, switching upgrade strategies
under one approval, independent backup readiness, and retry cooldown after revocation.
The Unit Tests CI job runs this check against OpenBao 2.6.4 and 2.7.1.
The local command defaults to 2.6.4. Set `POLICY_TEST_OPENBAO_IMAGE` to test another OpenBao image.

Run `make build-policy-approvals` to generate both release approval files in `dist/`. The generator uses the same policy
definitions as bootstrap and runtime reconciliation. For a specific manifest, run
`go run ./hack/tools/operator_policy_approval --cluster cluster.yaml`. Operators use the release assets instead of
building these tools.

## Controller JWT isolation

Run `make test-controller-jwt-openbao` with Docker available. The test starts an EnvTest API server and two disposable
OpenBao servers on loopback. It verifies Pod-bound token issuance, the controller's ServiceAccount permission boundary,
mode migration, and rejected cross-target and shared JWT replay with both inline and standard authentication.
The Envtest Integration CI job runs this check against OpenBao 2.6.4 and 2.7.1. The local command defaults to 2.6.4;
set `OPENBAO_JWT_TEST_IMAGE` to select another image. The test removes its containers when it finishes.

The `controller-jwt` E2E scenario runs the deployed controller against self-initialized OpenBao Pods. It verifies the
generated controller role, Raft Autopilot repair, and approved policy repair after the original ten-minute JWT expires,
with the same controller Pod and process. It also migrates an initialized Shared cluster, restores its pre-migration Raft
snapshot, and repairs the restored audience restrictions through an independent administrator login.

```sh
make test-e2e-ci \
  E2E_LABEL_FILTER=controller-jwt \
  E2E_TIMEOUT=35m \
  E2E_FAIL_ON_EMPTY=true \
  E2E_JUNIT_REPORT=dist/test/controller-jwt/junit.xml \
  E2E_JSON_REPORT=dist/test/controller-jwt/ginkgo.json
```

This serial scenario uses the default inline transport and takes at least eleven minutes to cross the real JWT lifetime.
Allow up to 25 minutes for setup, reconciliation, and recovery. The snapshot operation uses the Raft API on the same CR;
object storage, the OpenBaoRestore Job, and recovery into a different CR UID require their own E2E coverage.
When using `test-e2e-existing` with an isolated test cluster, set both `E2E_OPENBAO_VERSION` and `E2E_OPENBAO_IMAGE`,
and configure its API-server egress addresses through `E2E_API_SERVER_CIDR` and `E2E_API_SERVER_ENDPOINT_IPS`.
