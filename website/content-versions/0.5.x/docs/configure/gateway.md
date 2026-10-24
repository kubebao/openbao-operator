---
title: Use Gateway API
description: Attach OpenBao to a compatible Gateway with TLS passthrough or verified backend TLS.
eyebrow: Configure · Service boundary
weight: 7
verifiedBy:
  - go.mod
  - api/v1alpha1/openbaocluster_networking_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/service/networking/gateway_integration.go
  - internal/service/networking/preflight.go
  - internal/service/networking/http_route.go
  - internal/service/networking/tls_route.go
  - internal/service/networking/backend_tls_policy.go
---

Use this page after choosing Gateway API as the [external entry path](../expose/). The cluster must serve Gateway API
`v1` resources used by the operator. OpenBao Operator 0.5.x is built against Gateway API v1.6.1.

## Choose the route mode

| Mode | Operator creates | Gateway listener | Use it when |
| --- | --- | --- | --- |
| TLS passthrough | `TLSRoute` | `protocol: TLS`, `tls.mode: Passthrough` | OpenBao should remain the TLS endpoint; required for ACME |
| Gateway termination | `HTTPRoute` and, by default, `BackendTLSPolicy` | HTTP or HTTPS | The Gateway must apply HTTP-aware policy or present the client certificate |

Passthrough is the smaller trust model because the Gateway never holds or uses the OpenBao server private key.

## Configure passthrough

Create the Gateway and listener first:

{{< command label="apply" title="Create a TLS passthrough listener" >}}
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: main-gateway
  namespace: gateway-system
spec:
  gatewayClassName: traefik
  listeners:
    - name: openbao
      port: 443
      protocol: TLS
      tls:
        mode: Passthrough
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              openbao.org/gateway-access: "true"
{{< /command >}}

Label each `OpenBaoCluster` namespace admitted by this shared listener with `openbao.org/gateway-access=true`. Gateway
API defaults `allowedRoutes.namespaces.from` to `Same`, so a Route in another namespace will not attach unless the
listener says otherwise.

Attach the cluster to that exact listener:

{{< command label="configure" title="Create the OpenBao TLSRoute" >}}
spec:
  gateway:
    enabled: true
    listenerName: openbao
    tlsPassthrough: true
    hostname: bao.example.com
    gatewayRef:
      name: main-gateway
      namespace: gateway-system
{{< /command >}}

The operator routes ordinary TLS traffic to `<cluster>-public` on port 8200. In ACME mode it routes to the dedicated
ACME Service on port 443. Add `bao.example.com` to `spec.tls.acme.domains`; Gateway hostnames are not copied into the
ACME domain list. The selected GatewayClass must advertise `TLSRoute` support.

## Configure termination and backend trust

When `tlsPassthrough` is false or omitted, the operator creates an `HTTPRoute`. Backend TLS is enabled by default when
cluster TLS is enabled.

{{< command label="configure" title="Create an HTTPRoute with verified backend TLS" >}}
spec:
  gateway:
    enabled: true
    listenerName: openbao-https
    hostname: bao.example.com
    gatewayRef:
      name: main-gateway
      namespace: gateway-system
    backendTLS:
      enabled: true
      hostname: openbao-cluster-prod-cluster.local
{{< /command >}}

The Gateway listener certificate is configured on the Gateway, not on the `OpenBaoCluster`. For the backend hop, the
operator creates a `BackendTLSPolicy` and a `<cluster>-tls-ca` ConfigMap containing `ca.crt`. Its default verification
name is the stable internal TLS server name, `openbao-cluster-<cluster>.local`. Set `backendTLS.hostname` only when the
backend certificate deliberately uses another DNS SAN.

Hardened clusters reject `backendTLS.enabled: false`. The GatewayClass must advertise `HTTPRoute` and
`BackendTLSPolicy` support for the default terminated path.

## Allow the data plane to reach OpenBao

Route attachment and pod reachability are separate controls. Add the actual Gateway data-plane pods to
`spec.network.trustedIngressPeers`; referencing a Gateway does not allow its entire namespace through NetworkPolicy.

{{< command label="configure" title="Allow the Gateway data plane" >}}
spec:
  network:
    trustedIngressPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: gateway-system
        podSelector:
          matchLabels:
            app.kubernetes.io/name: traefik
{{< /command >}}

Use selectors that match the data-plane pods, which might not be the same pods as the Gateway controller.

## Check compatibility and readiness

The operator validates the referenced Gateway, listener compatibility, GatewayClass acceptance, advertised features,
installed API version, the Gateway `Programmed` condition, and attachment of its managed Route. For a Gateway in
another namespace, its listener must also allow Routes from the `OpenBaoCluster` namespace.

| Condition | Meaning |
| --- | --- |
| `GatewayIntegrationReady=True` | The operator verified the Gateway contract and the current Route reports `Accepted=True` and `ResolvedRefs=True` |
| `GatewayIntegrationReady=Unknown` | Capability or current Route status is not available yet; `GatewayRoutePending` identifies the attachment wait |
| `GatewayIntegrationReady=False` | A reference, listener, feature, programmed-state, Route acceptance, or Route reference check failed |

Read the condition reason before changing the manifest. A missing `status.supportedFeatures` is reported as Unknown,
not assumed compatible. `GatewayRouteNotAccepted` and `GatewayRouteReferencesUnresolved` identify explicit attachment
failures. Inspect the Route parent status for controller detail, then test the public hostname from outside the cluster.
