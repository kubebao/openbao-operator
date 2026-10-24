---
title: Upgrade a cluster
description: Select an upgrade strategy, create a recovery point, request a version change, and verify the rollout.
eyebrow: Operate · Change management
weight: 3
verifiedBy:
  - api/v1alpha1/openbaocluster_operations_types.go
  - api/v1alpha1/openbaocluster_status_types.go
  - config/policy/openbao-validate-openbaocluster.yaml
  - internal/service/upgrade/strategy_transition.go
  - internal/service/upgrade/version.go
  - internal/service/upgrade/rolling
  - internal/service/upgrade/bluegreen
  - internal/service/upgrade/bluegreen/version_compatibility_test.go
---

Use `RollingUpdate` unless you need parallel validation or a manual traffic cutover. Both strategies require a healthy
cluster, a supported target version, and a working upgrade identity.

## Choose the strategy

| Strategy | Use it when | Failure boundary |
| --- | --- | --- |
| `RollingUpdate` | You want the default, lower-capacity rollout | The operator updates one Pod at a time, coordinates leader step-down, and holds a failed rollout for an explicit retry |
| `BlueGreen` | You need a parallel Green revision, validation hook, or manual promotion | The operator joins Green as non-voters, promotes it, cuts over, and can abort or roll back according to phase |

{{< callout type="warning" title="Use rolling for a pre-2.6 to 2.6-or-newer transition" >}}
The operator rejects blue-green upgrades from an OpenBao version before 2.6 to version 2.6 or newer. The mixed-version
request-forwarding path cannot be qualified safely. Switch an idle cluster to `RollingUpdate` first.
{{< /callout >}}

Check the [compatibility matrix](../../reference/compatibility/) and the upstream release notes before choosing the
target.

## Prepare the rollout

{{< checklist title="Preflight" >}}
- `Available=True`, `Degraded` is not true, and all declared voter replicas are Ready.
- `status.currentVersion` matches the current `spec.version`.
- No backup, restore, resize, restart, upgrade, Green revision, or break-glass recovery is active.
- A recent snapshot exists and its restore path has been rehearsed.
- The upgrade JWT role has the health, step-down, and Raft capabilities required by the selected strategy.
- The target version and any explicit image are allowed by the compatibility and image-verification policies.
{{< /checklist >}}

Enable a mandatory recovery point for the next change:

{{< command label="configure" title="Require a pre-upgrade snapshot" >}}
spec:
  upgrade:
    preUpgradeSnapshot: true
{{< /command >}}

This requires a valid `spec.backup`. The operator acquires an operation lock so an upgrade, backup, and restore do not
perform conflicting long-running work.

## Change strategies on an existing cluster

Change only the strategy, then wait for the operator to accept it. Do not combine the strategy transition with a
version, image, replica, storage, or restart change.

{{< command label="apply" title="Switch to rolling update" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{
  "spec": {
    "upgrade": {
      "strategy": "RollingUpdate"
    }
  }
}'
{{< /command >}}

{{< command label="verify" title="Wait for strategy acceptance" >}}
kubectl -n <namespace> get openbaocluster <name> \
  -o jsonpath='{.status.acceptedUpgradeStrategy}{"\n"}'
{{< /command >}}

Before switching to `BlueGreen`, configure `spec.upgrade.jwtAuthRole` or use the default
`openbao-operator-upgrade` role created by self-init OIDC bootstrap. A role created for a rolling-only cluster might
need its policy expanded before the switch; self-init requests are not replayed later.

## Request the version change

After the strategy is accepted, change `spec.version` in a separate request:

{{< command label="apply" title="Request the target version" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p '{
  "spec": {
    "version": "<target-version>"
  }
}'
{{< /command >}}

The version must be semantic version syntax. The operator blocks downgrades below `status.currentVersion`. If you set
`spec.image`, keep a semantic tag aligned with `spec.version`; digest-pinned images still use `spec.version` as the
upgrade intent.

## Control a blue-green promotion

Set `spec.upgrade.blueGreen.autoPromote: false` before the upgrade begins to hold a healthy Green revision in
`Syncing`. Approve it by changing the one-shot request value:

{{< command label="apply" title="Approve a held promotion" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p "{
  \"spec\": {
    \"upgrade\": {
      \"requests\": {
        \"promote\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
      }
    }
  }
}"
{{< /command >}}

`autoPromote` is snapshotted when an upgrade starts. Changing it during an in-flight upgrade affects only the next
upgrade.

When a BlueGreen validation hook is configured, the operator verifies its image through
`spec.operatorImageVerification` before creating the Job and pins a successful result by digest. `Block` stops the
upgrade when verification fails; `Warn` keeps the original reference and is not permitted by the Hardened profile.
The Job disables ServiceAccount token automounting, runs non-root with a read-only root filesystem, drops all
capabilities, uses RuntimeDefault seccomp, inherits `spec.imagePullSecrets`, and receives bounded resources.

## Recover a held rolling failure

Fix the cause recorded in `status.upgrade.failure`, then change `spec.upgrade.requests.retry`:

{{< command label="apply" title="Retry a rolling upgrade" >}}
kubectl -n <namespace> patch openbaocluster <name> --type merge -p "{
  \"spec\": {
    \"upgrade\": {
      \"requests\": {
        \"retry\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
      }
    }
  }
}"
{{< /command >}}

Each request is edge-triggered: use a new non-empty value. For blue-green rollback repair failures, use
[Recover a failed rollback](../recover-failed-rollback/) instead of retrying blindly.

A blue-green upgrade can only roll back before the `Cleanup` phase, because `Cleanup` removes Blue peers from Raft.
From `Cleanup` onward, the operator refuses `spec.upgrade.requests.rollback` and automatic rollback with a
`RollbackRefused` warning event. It retries the current phase, and applies any spec change made during that time
after the current upgrade completes. The operator records Green's target image, version, and replica count and Blue's
original replica count in `status.blueGreen`. Changes to `spec.replicas` do not change the active upgrade's cleanup
checks or rollback quorum. When resuming an older operation without these fields, the operator recovers its image and
replica counts from the existing StatefulSets; it stops if those inputs cannot be recovered.

## Verify the result

{{< command label="verify" title="Watch upgrade state" >}}
kubectl -n <namespace> get openbaocluster <name> -w
kubectl -n <namespace> get pods -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> get events --sort-by=.lastTimestamp
{{< /command >}}

Finish only when `status.currentVersion` equals `spec.version`, `status.phase=Running`, `Available=True`, all declared
replicas are Ready, and Raft membership matches the intended topology.
