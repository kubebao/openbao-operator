package openbaocluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

// handleInterruptedScaleDown returns true when recovery completed or must wait.
func (r *infraReconciler) handleInterruptedScaleDown(
	ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster,
	observed *appsv1.StatefulSet, desired int32, readReplica bool,
) bool {
	resumed, err := r.resumeRemovedPeerScaleDown(ctx, logger, cluster, observed, desired, readReplica)
	if err != nil {
		logger.Info("Interrupted scale down remains blocked", "statefulset", observed.Name, "readReplica", readReplica, "reason", err.Error())
	}
	return resumed || err != nil
}

// resumeRemovedPeerScaleDown only finishes a replica decrement whose victim is
// already absent. It never removes a peer or applies a new workload template.
func (r *infraReconciler) resumeRemovedPeerScaleDown(
	ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster,
	observed *appsv1.StatefulSet, desired int32, readReplica bool,
) (bool, error) {
	if !cluster.Status.Initialized || observed.Spec.Replicas == nil || desired >= *observed.Spec.Replicas ||
		statefulSetSettledAtReplicas(observed, *observed.Spec.Replicas) {
		return false, nil
	}
	if r.deps.ScaleDown.Membership == nil {
		return false, fmt.Errorf("authenticated membership observation is required to resume scale down")
	}
	reader := r.deps.Kubernetes.APIReader
	if reader == nil {
		reader = r.deps.Kubernetes.Client
	}
	currentCluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(cluster), currentCluster); err != nil {
		return false, fmt.Errorf("read current cluster before scale-down resume: %w", err)
	}
	if err := r.validateScaleDownResumeCluster(cluster, currentCluster, observed.Name, desired, readReplica); err != nil {
		return false, err
	}
	cluster = currentCluster
	sts := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(observed), sts); err != nil {
		return false, fmt.Errorf("read current workload before scale-down resume: %w", err)
	}
	if sts.UID != observed.UID || sts.ResourceVersion != observed.ResourceVersion {
		return false, fmt.Errorf("workload changed before scale-down resume")
	}
	terminatingVictim, err := scaleDownResumeTargetVictim(ctx, reader, cluster, sts)
	if err != nil {
		return false, err
	}
	next := *sts.Spec.Replicas - 1
	retained, err := scaleDownResumeRetainedPods(ctx, reader, cluster, sts, next, readReplica)
	if err != nil {
		return false, err
	}
	// Raft permits one pending configuration change. Preserve a majority of
	// the pre-decrement voter population even if the latest observed removal
	// is not committed. This fence applies to every interrupted voter resume.
	if !readReplica && next < *sts.Spec.Replicas/2+1 {
		return false, fmt.Errorf("interrupted voter scale down requires a retained pre-decrement majority; manual recovery is required")
	}
	victim := fmt.Sprintf("%s-%d", sts.Name, *sts.Spec.Replicas-1)
	if err := r.confirmScaleDownResumeMembership(ctx, logger, cluster, victim, retained); err != nil {
		return false, err
	}
	confirmedCluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(cluster), confirmedCluster); err != nil {
		return false, fmt.Errorf("confirm cluster identity before scale-down resume: %w", err)
	}
	if err := r.validateScaleDownResumeCluster(cluster, confirmedCluster, sts.Name, desired, readReplica); err != nil {
		return false, err
	}
	if terminatingVictim != nil {
		confirmedVictim, err := readScaleDownResumeTerminatingVictim(ctx, reader, sts)
		if err != nil {
			return false, err
		}
		if confirmedVictim.UID != terminatingVictim.UID {
			return false, fmt.Errorf("terminating scale-down victim changed during observation")
		}
	}
	if err := patchResumedReplicaCount(ctx, r.deps.Kubernetes.Client, sts, next); err != nil {
		return false, err
	}
	logger.Info("Finished interrupted scale-down decrement", "statefulset", sts.Name, "victim", victim, "replicas", next)
	return true, nil
}

func scaleDownResumeRetainedPods(
	ctx context.Context, reader client.Reader, cluster *openbaov1alpha1.OpenBaoCluster,
	sts *appsv1.StatefulSet, next int32, readReplica bool,
) (map[string]bool, error) {
	voterSTS := sts
	if readReplica {
		if sts.Name != resourceidentity.ReadReplicaStatefulSetName(cluster) {
			return nil, fmt.Errorf("read-replica resume requires the current read-replica workload")
		}
		voterSTS = &appsv1.StatefulSet{}
		key := client.ObjectKey{Namespace: cluster.Namespace, Name: upgrade.StableVoterStatefulSetName(cluster)}
		if err := reader.Get(ctx, key, voterSTS); err != nil {
			return nil, fmt.Errorf("read voter workload for scale-down resume: %w", err)
		}
		if err := validateScaleDownResumeWorkload(cluster, voterSTS); err != nil {
			return nil, err
		}
		if !statefulSetSettledAtReplicas(voterSTS, *voterSTS.Spec.Replicas) {
			return nil, fmt.Errorf("voter workload is not settled for read-replica scale-down resume")
		}
	} else if sts.Name != upgrade.StableVoterStatefulSetName(cluster) || next < 1 {
		return nil, fmt.Errorf("voter resume requires the active workload and at least one retained voter")
	}
	retained := make(map[string]bool)
	voters := *voterSTS.Spec.Replicas
	if !readReplica {
		voters = next
	}
	if err := readyScaleDownResumePods(ctx, reader, voterSTS, voters, true, retained); err != nil {
		return nil, err
	}
	if readReplica {
		if err := readyScaleDownResumePods(ctx, reader, sts, next, false, retained); err != nil {
			return nil, err
		}
	} else if cluster.Spec.ReadReplicas != nil {
		readSTS := &appsv1.StatefulSet{}
		key := client.ObjectKey{Namespace: cluster.Namespace, Name: resourceidentity.ReadReplicaStatefulSetName(cluster)}
		if err := reader.Get(ctx, key, readSTS); err != nil {
			if !apierrors.IsNotFound(err) || cluster.Spec.ReadReplicas.Replicas > 0 {
				return nil, fmt.Errorf("read retained read-replica workload: %w", err)
			}
		} else {
			if err := validateScaleDownResumeWorkload(cluster, readSTS); err != nil {
				return nil, err
			}
			if err := readyScaleDownResumePods(ctx, reader, readSTS, *readSTS.Spec.Replicas, false, retained); err != nil {
				return nil, err
			}
		}
	}
	return retained, nil
}

func validateScaleDownResumeWorkload(cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet) error {
	if err := validateScaleDownResumeWorkloadIdentity(cluster, sts); err != nil {
		return err
	}
	if sts.Status.CurrentReplicas != *sts.Spec.Replicas {
		return fmt.Errorf("workload current replica count is not settled for scale-down resume")
	}
	return nil
}

// Only the target can have one fewer current replica. StatefulSet status omits
// terminating Pods from CurrentReplicas but retains them in Replicas. Pin the
// exact highest ordinal so another missing or stale Pod cannot explain the gap.
func scaleDownResumeTargetVictim(
	ctx context.Context, reader client.Reader, cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet,
) (*corev1.Pod, error) {
	if err := validateScaleDownResumeWorkloadIdentity(cluster, sts); err != nil {
		return nil, err
	}
	if sts.Status.CurrentReplicas == *sts.Spec.Replicas {
		return nil, nil
	}
	if *sts.Spec.Replicas < 1 || sts.Status.CurrentReplicas != *sts.Spec.Replicas-1 {
		return nil, fmt.Errorf("workload current replica deficit is not one terminating scale-down victim")
	}
	return readScaleDownResumeTerminatingVictim(ctx, reader, sts)
}

func readScaleDownResumeTerminatingVictim(ctx context.Context, reader client.Reader, sts *appsv1.StatefulSet) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	key := client.ObjectKey{Namespace: sts.Namespace, Name: fmt.Sprintf("%s-%d", sts.Name, *sts.Spec.Replicas-1)}
	if err := reader.Get(ctx, key, pod); err != nil {
		return nil, fmt.Errorf("read terminating scale-down victim: %w", err)
	}
	owner := metav1.GetControllerOfNoCopy(pod)
	if pod.UID == "" || pod.DeletionTimestamp.IsZero() || owner == nil ||
		owner.UID != sts.UID || owner.Name != sts.Name || owner.Kind != "StatefulSet" ||
		owner.APIVersion != appsv1.SchemeGroupVersion.String() ||
		pod.Labels[appsv1.StatefulSetRevisionLabel] != sts.Status.CurrentRevision {
		return nil, fmt.Errorf("scale-down victim lacks current terminating identity, ownership or revision")
	}
	return pod, nil
}

func validateScaleDownResumeWorkloadIdentity(cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet) error {
	if err := resourceownership.RequireManagedControllerOwnerProof("resume scale down", sts, cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")); err != nil {
		return err
	}
	if sts.UID == "" || sts.ResourceVersion == "" || !sts.DeletionTimestamp.IsZero() ||
		sts.Spec.Replicas == nil || *sts.Spec.Replicas < 0 ||
		sts.Spec.ServiceName != resourceidentity.HeadlessServiceName(cluster) ||
		sts.Status.ObservedGeneration < sts.Generation || (*sts.Spec.Replicas > 0 && sts.Status.CurrentRevision == "") ||
		sts.Status.CurrentRevision != sts.Status.UpdateRevision ||
		sts.Status.Replicas != *sts.Spec.Replicas {
		return fmt.Errorf("workload identity or observed revision is not current for scale-down resume")
	}
	return nil
}

func readyScaleDownResumePods(
	ctx context.Context, reader client.Reader, sts *appsv1.StatefulSet, replicas int32,
	voter bool, retained map[string]bool,
) error {
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		pod := &corev1.Pod{}
		key := client.ObjectKey{Namespace: sts.Namespace, Name: fmt.Sprintf("%s-%d", sts.Name, ordinal)}
		if err := reader.Get(ctx, key, pod); err != nil {
			return fmt.Errorf("read retained Pod %s before scale-down resume: %w", key.Name, err)
		}
		owner := metav1.GetControllerOfNoCopy(pod)
		if owner == nil || owner.UID != sts.UID || owner.Kind != "StatefulSet" ||
			owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Name != sts.Name ||
			!pod.DeletionTimestamp.IsZero() || !isPodReady(pod) ||
			pod.Labels[appsv1.StatefulSetRevisionLabel] != sts.Status.CurrentRevision {
			return fmt.Errorf("retained Pod %s lacks current ownership, revision or readiness", pod.Name)
		}
		retained[pod.Name] = voter
	}
	return nil
}

func validateScaleDownResumeMembership(
	cluster *openbaov1alpha1.OpenBaoCluster, config *portopenbao.RaftConfigurationResponse,
	autopilot *portopenbao.RaftAutopilotStateResponse, victim string, retained map[string]bool,
) error {
	if config == nil || len(config.Config.Servers) == 0 || autopilot == nil ||
		autopilot.Leader == "" || len(retained) == 0 {
		return fmt.Errorf("authoritative membership and retained member health are required for scale-down resume")
	}
	seenIDs, seenHosts, found := map[string]bool{}, map[string]bool{}, map[string]bool{}
	leaders := 0
	for _, server := range config.Config.Servers {
		host, err := scaleDownResumeRaftHost(server.Address)
		if err != nil || server.NodeID == "" || seenIDs[server.NodeID] || seenHosts[host] {
			return fmt.Errorf("ambiguous Raft member identity prevents scale-down resume")
		}
		seenIDs[server.NodeID], seenHosts[host] = true, true
		suffix := "." + resourceidentity.HeadlessServiceName(cluster) + "." + cluster.Namespace + ".svc"
		if !strings.HasSuffix(host, suffix) {
			return fmt.Errorf("observed Raft member address is outside the current headless Service")
		}
		podName := strings.TrimSuffix(host, suffix)
		if server.NodeID == victim || podName == victim {
			return fmt.Errorf("scale-down victim %s is still present in Raft membership", victim)
		}
		voter, expected := retained[podName]
		if !expected {
			return fmt.Errorf("observed Raft member %s does not match the retained topology", server.NodeID)
		}
		if found[podName] || server.Voter != voter {
			return fmt.Errorf("observed Raft role or address conflicts for retained Pod %s", podName)
		}
		if expectedVoter, named := retained[server.NodeID]; named && (server.NodeID != podName || expectedVoter != voter) {
			return fmt.Errorf("observed Raft node ID conflicts with the retained Pod address")
		}
		if err := validateScaleDownResumeMemberHealth(server, host, voter, autopilot); err != nil {
			return err
		}
		if server.Leader {
			leaders++
		}
		found[podName] = true
	}
	if leaders != 1 || len(found) != len(retained) {
		return fmt.Errorf("retained Raft topology or leader is incomplete for scale-down resume")
	}
	return nil
}

func validateScaleDownResumeMemberHealth(
	server portopenbao.RaftServer, host string, voter bool, autopilot *portopenbao.RaftAutopilotStateResponse,
) error {
	health, ok := autopilot.Servers[server.NodeID]
	healthHost, err := scaleDownResumeRaftHost(health.Address)
	if !ok || health.ID != server.NodeID || !health.Healthy || err != nil || healthHost != host ||
		(server.Leader && (!voter || autopilot.Leader != server.NodeID)) {
		return fmt.Errorf("retained Raft member %s lacks current health or leadership proof", server.NodeID)
	}
	return nil
}

func scaleDownResumeRaftHost(address string) (string, error) {
	if strings.Contains(address, "://") {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" ||
			parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return "", fmt.Errorf("invalid Raft member address")
		}
		address = parsed.Host
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "8201" || host == "" {
		return "", fmt.Errorf("invalid Raft member address")
	}
	return strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(host), "."), ".cluster.local"), nil
}

func patchResumedReplicaCount(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, replicas int32) error {
	// UID is immutable, and resourceVersion makes this replica-only merge patch
	// conditional on the exact observed workload. No template fields are applied.
	var patch struct {
		Metadata struct {
			UID             types.UID `json:"uid"`
			ResourceVersion string    `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Replicas int32 `json:"replicas"`
		} `json:"spec"`
	}
	patch.Metadata.UID, patch.Metadata.ResourceVersion = sts.UID, sts.ResourceVersion
	patch.Spec.Replicas = replicas
	payload, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode resumed replica count: %w", err)
	}
	if err := c.Patch(ctx, sts.DeepCopy(), client.RawPatch(types.MergePatchType, payload)); err != nil {
		return fmt.Errorf("finish interrupted replica decrement: %w", err)
	}
	return nil
}
