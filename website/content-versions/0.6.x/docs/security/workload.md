---
title: Workload security
description: Review the generated pod security contexts, token mounts, writable storage, namespace enforcement, and lifecycle Jobs.
eyebrow: Security · Workload
weight: 5
verifiedBy:
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/platform/constants/security.go
  - internal/service/backup/job_builder.go
  - internal/service/provisioner/manager_tenant.go
  - internal/service/restore/job.go
  - internal/service/upgrade/job_builder.go
  - internal/service/workload/statefulset_builder.go
  - internal/service/workload/statefulset_builder_containers.go
  - internal/service/workload/statefulset_builder_probe_volumes.go
  - internal/service/workload/statefulset_builder_spec.go
---

The operator generates OpenBao Pods and built-in lifecycle Jobs with a restricted baseline. Keep customization in the
custom resource and review any custom executable as a separate workload; do not patch generated resources to bypass
the baseline.

## Generated baseline

| Control | OpenBao workload | Built-in backup, restore, and upgrade executors |
| --- | --- | --- |
| User | Non-root; UID 100 and GID/fsGroup 1000 on standard Kubernetes | Non-root; UID/GID/fsGroup 1000 on standard Kubernetes |
| OpenShift | UID, GID, and fsGroup left to SCC | UID, GID, and fsGroup left to SCC |
| Filesystem | Read-only root with explicit data, rendered-config, temporary, and optional audit/plugin mounts | Read-only root with explicit credential and temporary mounts |
| Privileges | `allowPrivilegeEscalation: false`, all capabilities dropped | Same |
| Seccomp | `RuntimeDefault` | `RuntimeDefault` |
| Resources | Cluster-configured requests and limits | Fixed requests of 100m/128Mi and limits of 500m/512Mi |

Hardened admission rejects root IDs, `runAsNonRoot: false`, root supplemental groups, unconfined seccomp, sysctls,
and Windows pod options. Development allows more overrides but does not make them safe.

## Keep token exposure explicit

The StatefulSet disables default ServiceAccount token automounting. It projects a one-hour token and Kubernetes CA
bundle only into the OpenBao container for Kubernetes integration. The config-rendering init container does not
receive that token mount.

Lifecycle Jobs also disable default automounting. They use separate ServiceAccounts and add projected tokens only
when the selected OpenBao JWT or cloud workload-identity flow needs them. Prefer these short-lived identities over
long-lived OpenBao tokens in Secrets.

## Keep writes on declared volumes

OpenBao writes Raft data to its data PVC and temporary data to `emptyDir`. When audit file storage is enabled, every
Pod mounts the shared RWX claim under a pod-specific `subPathExpr`; collectors should mount that claim read-only.
Confirm the storage provider honors the effective `fsGroup` or pre-provision ownership for the runtime identity.

The generated StatefulSet deletes PVCs for removed ordinals on scale-down and retains remaining PVCs when the
StatefulSet is deleted. See [Configure storage](../../configure/storage/) and
[Configure read replicas](../../configure/read-replicas/) before changing replicas.

## Enforce the namespace boundary

In multi-tenant mode, the provisioner defaults tenant namespaces to Pod Security `restricted` for enforce, audit, and
warn. With external label ownership, the platform must set and maintain an equivalent boundary. Single-tenant mode
also leaves namespace policy to the platform.

`spec.workloadHardening.appArmorEnabled: true` adds `RuntimeDefault` AppArmor to StatefulSets and the built-in backup
and upgrade executors when the platform supports it.

{{< callout type="note" title="Custom validation hooks retain a separate command boundary" >}}
Blue-green validation-hook Jobs disable token automounting, run non-root with a read-only root filesystem, drop
capabilities, use RuntimeDefault seccomp, and receive fixed resource bounds. Their image is verified through
`operatorImageVerification`. They do not currently receive the optional AppArmor setting, and the supplied command
remains author-controlled; require `usecustomexecutables` and apply platform policy for that remaining boundary.
{{< /callout >}}

{{< checklist title="Runtime review" >}}
- render a voter StatefulSet, read-replica StatefulSet, and each enabled lifecycle Job
- verify the init container has no Kubernetes token mount
- test writable data, temporary, audit, and credential paths under the effective UID and fsGroup
- verify tenant namespaces enforce the intended Pod Security level
- confirm custom hooks and plugin executables meet controls the operator does not supply
{{< /checklist >}}
