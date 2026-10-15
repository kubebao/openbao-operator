# Upgrade Strategies: Gateway API

Source: `test/e2e/Upgrade_Gateway_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `upgrade-strategies-creates-a-tlsroute-and-reports-healthy-79be45e7` | creates a TLSRoute and reports healthy passthrough integration | active | _none_ | `upgrade`, `upgrades`, `cluster`, `slow`, `gateway`, `requires-gateway-api`, `tls-passthrough` |
| `upgrade-gateway-httproute-cutover` | keeps HTTPRoute stable and switches external Service selector at cutover | active | _none_ | `upgrade`, `upgrades`, `cluster`, `slow`, `gateway`, `requires-gateway-api`, `bluegreen`, `e2e-anchor` |

## `upgrade-strategies-creates-a-tlsroute-and-reports-healthy-79be45e7`

Path: `Upgrade Strategies: Gateway API > Gateway API > Gateway TLS Passthrough > creates a TLSRoute and reports healthy passthrough integration`

State: `active`

Generated fallback ID: `upgrade-gateway-creates-a-tlsroute-and-reports-healthy-1c3566a0`

Covers: _none_

Labels: `upgrade`, `upgrades`, `cluster`, `slow`, `gateway`, `requires-gateway-api`, `tls-passthrough`

Recorded checkpoints:
- waiting for passthrough Gateway integration status
- verifying a TLSRoute is created for passthrough access
- verifying BackendTLSPolicy is not created for passthrough mode


## `upgrade-gateway-httproute-cutover`

Path: `Upgrade Strategies: Gateway API > Gateway API > HTTPRoute Blue/Green Cutover > keeps HTTPRoute stable and switches external Service selector at cutover`

State: `active`

Generated fallback ID: `upgrade-gateway-keeps-httproute-stable-and-switches-external-954a56f0`

Covers: _none_

Labels: `upgrade`, `upgrades`, `cluster`, `slow`, `gateway`, `requires-gateway-api`, `bluegreen`, `e2e-anchor`

Recorded checkpoints:
- Capturing HTTPRoute before upgrade to verify stability
- Triggering upgrade
- Verifying external Service remains on Blue while DemotingBlue is in progress
- Waiting for Cleanup phase and verifying the external Service selector switches to Green
- Waiting for Blue/Green upgrade to complete
- Verifying legacy blue/green Services do not exist
- Verifying HTTPRoute remains stable throughout upgrade
