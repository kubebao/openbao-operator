---
title: Configure a cluster
description: Set the cluster baseline, then define its network, trust, exposure, and monitoring boundary.
eyebrow: Cluster baseline
weight: 2
hideChildren: true
verifiedBy:
  - api/v1alpha1/openbaocluster_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/platform/hardenedcontract/contract.go
---

Configure the cluster baseline before you expose OpenBao or depend on it. Start with the security profile because it
constrains every later choice.

## Configure the baseline

| Task | Outcome |
| --- | --- |
| [Choose a security profile](security-profile/) | Select the enforced `Development` or `Hardened` contract. |
| [Initialize the cluster](initialize/) | Define bootstrap requests, human access, operator access, and recovery-key custody. |
| [Configure unseal](unseal/) | Place the unseal root of trust and credentials outside the failure boundary you want to protect. |
| [Configure storage](storage/) | Choose voter and audit storage, understand expansion, and account for the current resource-control gap. |
| [Configure the server runtime](server/) | Set listener, lease, audit, plugin, and Raft Autopilot behavior. |

Complete those tasks in order for a new production cluster.

## Define the service boundary

| Task | Outcome |
| --- | --- |
| [Expose OpenBao](expose/) | Choose Gateway API, Ingress, or a direct Service and assign TLS and DNS ownership. |
| [Use Gateway API](gateway/) | Attach a compatible listener with passthrough or verified backend TLS. |
| [Configure network policy](network/) | Allow DNS, Kubernetes API, edge, monitoring, and external dependency traffic. |
| [Monitor OpenBao](monitor/) | Scrape operator and workload signals with explicit credentials, trust, and reachability. |

Treat these as one boundary: a route without NetworkPolicy reachability is unusable, and a monitoring resource without
certificate trust or credentials is not an operational signal.

## Extend the deployment

| Task | Outcome |
| --- | --- |
| [Configure read replicas](read-replicas/) | Add a non-voter pool, choose its endpoint, and understand storage and upgrade behavior. |
| [Use private registries](air-gapped/) | Mirror every runtime image and separate kubelet pull credentials from controller verification credentials. |

## Keep ownership boundaries explicit

The operator owns the generated StatefulSets, Services, ConfigMaps, Secrets, and NetworkPolicies. You own the external
systems and policies those resources depend on, including:

- KMS, HSM, transit, and credential lifecycle;
- certificate issuers, DNS, and edge routing;
- StorageClasses, encryption, capacity, and failure domains;
- human identity, policy, and recovery-key custody;
- audit collection, backup storage, restore testing, and monitoring.

Do not edit generated workload resources directly. Change the `OpenBaoCluster` or use a documented maintenance or
recovery workflow.

{{< callout type="warning" title="A valid manifest is not a production-ready service" >}}
Admission checks required fields and known unsafe combinations. It cannot prove that a human can sign in, a KMS key is
recoverable, a StorageClass meets its durability claim, or a backup can restore. Test those outcomes before go-live.
{{< /callout >}}
