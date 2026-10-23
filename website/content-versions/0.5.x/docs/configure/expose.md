---
title: Expose OpenBao
description: Choose Gateway API, Ingress, or a direct Service and keep certificate, DNS, and publication ownership explicit.
eyebrow: Configure · Service boundary
weight: 6
verifiedBy:
  - api/v1alpha1/openbaocluster_networking_types.go
  - api/v1alpha1/openbaocluster_workload_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/platform/openbaotls/validation.go
  - internal/service/networking/ingress.go
  - internal/service/networking/service_revision.go
  - internal/service/networking/services.go
---

Choose one client entry path, decide where TLS terminates, and assign ownership for the public hostname and
certificate. The operator creates Kubernetes networking resources; it does not create public DNS records or configure
the external data plane.

## Choose the entry path

| Path | Use it when | Operator-managed resource |
| --- | --- | --- |
| Gateway API | Gateway API is the platform edge and you need explicit listener and route ownership | `HTTPRoute` or `TLSRoute`, a client Service, and optionally `BackendTLSPolicy` |
| Ingress | An existing ingress controller is the established HTTP edge | `Ingress` and a client Service |
| Direct Service | A cloud load balancer or private L4 boundary is sufficient | A `LoadBalancer`, `NodePort`, or `ClusterIP` Service |

Gateway API is the most explicit model for new shared platforms. Use [the Gateway guide](../gateway/) after choosing
that path. Ingress remains useful where it is already the platform standard. A direct Service has the fewest moving
parts, but leaves perimeter policy to the load balancer and network. Configure one managed edge path unless you have a
deliberate reason to publish both Gateway and Ingress; admission does not make them mutually exclusive.

The main client Service is `<cluster>-public`. With RollingUpdate, it can select voters and configured steady read
replicas; OpenBao serves eligible reads locally and forwards writes to the active leader. BlueGreen revision scoping
currently excludes steady read replicas from this Service. The operator does not create a second Gateway or Ingress
route for the opt-in dedicated read-replica Service.

## Choose where TLS terminates

| Pattern | Use it when | Required trust decision |
| --- | --- | --- |
| Passthrough | OpenBao should present the server certificate to clients | The edge forwards TLS without decrypting it |
| Edge termination with backend TLS | HTTP-aware edge policy or centralized certificate presentation is required | The edge certificate and the edge-to-OpenBao certificate are separate trust relationships |

Prefer passthrough unless the edge must inspect HTTP. ACME requires OpenBao to remain the TLS endpoint; a managed
Gateway therefore needs `tlsPassthrough: true`, and the Gateway hostname must also appear in `tls.acme.domains`.
Hardened clusters require `tls.mode: External` or `ACME` and reject TLS disablement.

`OperatorManaged` is useful for development and internal evaluation. It makes the operator the certificate authority,
so it is not an accepted Hardened production mode.

## Configure the selected path

{{< command label="configure" title="Use Gateway API passthrough" >}}
spec:
  gateway:
    enabled: true
    listenerName: openbao
    tlsPassthrough: true
    hostname: bao.example.com
    gatewayRef:
      name: main-gateway
      namespace: gateway-system
  network:
    trustedIngressPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: gateway-system
        podSelector:
          matchLabels:
            app.kubernetes.io/name: traefik
{{< /command >}}

This creates a `TLSRoute`; the referenced Gateway and a compatible TLS passthrough listener must already exist. A
cross-namespace Gateway must allow Routes from the `OpenBaoCluster` namespace.

{{< command label="configure" title="Use a managed Ingress" >}}
spec:
  ingress:
    enabled: true
    className: nginx
    host: bao.example.com
    annotations:
      nginx.ingress.kubernetes.io/backend-protocol: HTTPS
  network:
    trustedIngressPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: ingress-nginx
{{< /command >}}

The Ingress presents `spec.ingress.tlsSecretName`, or `<cluster>-tls-server` when the field is empty. That Secret choice
does not rename the certificate Secrets mounted by OpenBao. A custom Secret reference requires `use` or `get` on that
Secret. Controller-specific backend TLS annotations remain your responsibility. Readiness defaults to a published
load-balancer address; use `readinessMode: Created` only for controllers that intentionally do not publish one.

{{< command label="configure" title="Use a direct load-balancer Service" >}}
spec:
  service:
    type: LoadBalancer
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-type: nlb
{{< /command >}}

Provider annotations are passed through to the generated Service, which exposes port 8200. Confirm whether the
resulting load balancer is public or private; the operator cannot infer that boundary from the annotation. There is no
dedicated integration-readiness condition for a direct Service.

## Supply external certificates

With `tls.mode: External`, create these same-namespace Secrets before expecting the cluster to become ready:

| Secret | Required keys |
| --- | --- |
| `<cluster>-tls-ca` | `ca.crt` |
| `<cluster>-tls-server` | `tls.crt`, `tls.key` |

The server certificate must chain to `ca.crt` and cover `openbao-cluster-<cluster>.local`. It must also cover each
enabled Ingress or Gateway hostname and every entry in `spec.tls.extraSANs`. Values that parse as IP addresses must be
IP SANs; all other values must be DNS SANs. The operator validates this contract but does not issue or rotate External
certificates.

{{< callout type="warning" title="DNS remains outside the operator" >}}
Create the public DNS record with your DNS controller, GitOps platform, or provider tooling. Point it at the address
published by the Gateway, Ingress, or load balancer and test the name from the client network. Setting `hostname` or
`host` on an `OpenBaoCluster` does not publish DNS.
{{< /callout >}}

## Preserve delegated authority

An identity that can edit an `OpenBaoCluster` still needs `publishnetworking` on that cluster to enable managed
Gateway or Ingress exposure, create a non-`ClusterIP` Service, or add networking annotations. Gateway attachment also
requires `use` on the referenced Gateway. Selecting an Ingress class requires `use` on that `IngressClass`.

After applying the change, require `TLSReady=True`, inspect the generated Service and route, and check
`GatewayIntegrationReady` or `IngressIntegrationReady`. For Gateway API, `GatewayIntegrationReady=True` includes the
current Route's `Accepted=True` and `ResolvedRefs=True` parent status. Finish with `bao status` or an HTTPS request that
uses the intended CA and hostname from the real client path.
