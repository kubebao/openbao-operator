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

{{< callout type="warning" title="Do not cross the OpenBao 2.6 protocol boundary" >}}
Do not upgrade an OpenBao server from a version before 2.6 to 2.6 or newer with either `RollingUpdate` or `BlueGreen`.
Older voters cannot forward requests to an upgraded leader. Health checks can still pass while forwarding and
Autopilot health fail. Changing the strategy does not avoid this limitation.
This operator version blocks the BlueGreen crossing but does not block the unsafe rolling crossing.
Keep the current server version until a compatible upgrade path is qualified. API-group migration that retains
the server version is a separate operation.
If a crossing has already started, do not force more voter replacements or downgrade upgraded voters. Preserve
PVCs and plan recovery from the actual voter versions and quorum state.
{{< /callout >}}

Check the [compatibility matrix](../../reference/compatibility/) and the upstream release notes before choosing the
target.

## Prepare the rollout

If database startup approaches the default probe budget, configure a longer
[startup budget](../../configure/storage/#allow-more-time-for-database-startup) before the rollout. The setting
also extends the rolling-upgrade readiness deadline for the sequential voter rollout.

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
`openbao-operator-upgrade` role created by self-init OIDC bootstrap. Without policy reconciliation, a role created for
a rolling-only cluster might need its policy expanded before the switch; self-init requests are not replayed later.

With [approved policy reconciliation](../operator-policies/), enrollment approves both supported strategies. The operator
updates the built-in upgrade policy after a strategy change, without another approval. Wait for
`PolicyReconciliationReady=True` as well as strategy acceptance before requesting the version change. Custom policies
attached to a custom upgrade role remain administrator-managed.

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

## Wait for Raft catch-up between voter replacements

The Raft catch-up gate is available from operator 0.6.3.

For `RollingUpdate`, the operator checks Raft health before releasing each voter replacement. This check covers
ordinary Pod-template changes, such as startup budgets and resources, and managed version upgrades. A Ready Pod can
still be catching up with the leader.

The operator holds the StatefulSet partition until the remaining voters are Ready, have the expected voting
membership, and meet the configured Autopilot health, contact, term, and log-lag thresholds. It then requires a fresh
observation from each remaining follower: its replication index must advance or its contact age must decrease.
An increasing contact age does not prove a new heartbeat. The voters must also reach the previously observed leader
index. The check excludes read replicas from voting quorum. A controller restart preserves the hold and its
observation. Single-voter and two-voter clusters still require a write outage during replacement.

The controller needs authenticated read access to `sys/storage/raft/configuration` and
`sys/storage/raft/autopilot/state`. The default self-init OIDC controller policy provides this access. If you manage
a custom controller identity or policy, grant these reads before requesting a rollout. The upgrade helper's JWT
role does not replace the controller identity.

A `VoterRolloutWaiting` event records why an ordinary rollout is held. Managed upgrades record the reason in
`RollingPartitionRetry` events. Check these events, operator logs, Raft health, and the StatefulSet partition when
progress stops. Restore authentication or peer health before continuing. Do not lower the partition manually to
bypass the check. A managed upgrade retains its overall readiness deadline and requires a retry if that deadline
expires.

A healthy active rollout finishes before the operator applies another template, replica count, or version. The
active rollout uses its published voter count while a scale request waits. If an ordinary template leaves a voter
unready, a correction with the same version and replica count is staged under `OnDelete`. The operator repairs an
unavailable voter first, then replaces other outdated voters under the Raft gate. This also permits a later
template to replace a deferred BlueGreen-to-RollingUpdate template without recreating Pods from the old current
revision. It restores `RollingUpdate` only after all voters have the correction and fresh Raft health.

Upgrade the operator on a settled cluster before starting a rollout. The gate does not retroactively protect a
rollout already started by an older operator.

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

During retry preparation, the operator holds the voter StatefulSet under `OnDelete`. This also allows a corrected
Pod template to be staged after a failed upgrade. The retry repairs voters one at a time and waits for Raft catch-up
between replacements. Controller restarts preserve the reset progress. A Pod that recovers without a reset is kept.
After all voters have the requested template and fresh Raft health, the operator restores `RollingUpdate` and
acknowledges the retry request. A repair can replace the leader; clients must retry through a brief leadership
change. Check `status.adminOps.lastError` when retry preparation waits.

Before resetting failed Pods, the operator waits for the StatefulSet to reflect the current cluster generation
and for the StatefulSet controller to observe that template. The Bao container image must match the resolved
image recorded by workload reconciliation, including a verified digest when image verification succeeds.
If the image differs or the resolved-image annotation is missing, the retry waits for workload reconciliation.

The operator records each rolling attempt in `status.upgrade.executionID`. Controller restarts preserve this value.
A new upgrade or accepted retry uses a new value, so retained leader step-down Jobs cannot satisfy a later attempt.
Before accepting a retry or changing the target version, the operator waits for the previous step-down Job and its Pods
to terminate. Progress created by an older operator can resume with its existing Jobs only when their ownership,
action, and creation time identify the current attempt. If that evidence is missing, the operator holds the upgrade;
inspect `status.upgrade.failure`, resolve the cause, and submit a new retry request.

Each request is edge-triggered: use a new non-empty value. For blue-green rollback repair failures, use
[Recover a failed rollback](../recover-failed-rollback/) instead of retrying blindly.

A blue-green upgrade can only roll back before the `Cleanup` phase, because `Cleanup` removes Blue peers from Raft.
From `Cleanup` onward, the operator refuses `spec.upgrade.requests.rollback` and automatic rollback with a
`RollbackRefused` warning event. It retries the current phase, and applies any spec change made during that time
after the current upgrade completes. The operator records Green's target image, version, and replica count and Blue's
original replica count in `status.blueGreen`. Changes to `spec.replicas` do not change the active upgrade's cleanup
checks or rollback quorum. When resuming an older operation without these fields, the operator recovers its image and
replica counts from the existing StatefulSets; it stops if those inputs cannot be recovered.

Before deleting a retired Blue or aborted Green StatefulSet, the operator verifies its current-cluster controller
owner reference, `openbao.org/owner-uid` annotation, and Pod-template revision. An ownership or revision mismatch
stops cleanup and preserves the StatefulSet. For an unrevisioned Blue workload, the operator also checks
`status.blueGreen.blueControllerRevision` when that field is recorded.

## Verify the result

{{< command label="verify" title="Watch upgrade state" >}}
kubectl -n <namespace> get openbaocluster <name> -w
kubectl -n <namespace> get pods -l openbao.org/cluster=<name> -o wide
kubectl -n <namespace> get events --sort-by=.lastTimestamp
{{< /command >}}

Finish only when `status.currentVersion` equals `spec.version`, `status.phase=Running`, `Available=True`, all declared
replicas are Ready, and Raft membership matches the intended topology.
