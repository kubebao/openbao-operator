package workload

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
)

const voterObservationLifetime = time.Minute

type voterRolloutObservation struct {
	Context     string                              `json:"context"`
	Followers   map[string]voterFollowerObservation `json:"followers"`
	LeaderIndex uint64                              `json:"leaderIndex"`
	ObservedAt  time.Time                           `json:"observedAt"`
}

type voterFollowerObservation struct {
	Index       uint64 `json:"index"`
	LastContact string `json:"lastContact"`
}

type voterPodObservation struct {
	Name       string      `json:"name"`
	UID        string      `json:"uid"`
	ReadySince metav1.Time `json:"readySince"`
}

// CheckVoterRollout checks actual voting membership, current Pod identities and
// replication progress. replacing excludes only the Pod about to be disrupted.
// The first healthy observation is persisted. A later, changed Autopilot sample
// must show that every remaining voter reached that observation's leader index.
// This prevents cached pre-restart health from immediately releasing another Pod.
func checkVoterRollout(ctx context.Context, logger logr.Logger, c client.Client, reader client.Reader, membership portopenbao.RaftMembershipReader, cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet, target string, replacing bool) error {
	replicas, err := portworkload.VoterReplicaCount(sts)
	if err != nil {
		return err
	}
	if replicas == 1 {
		return nil // A single voter already requires an outage for replacement.
	}
	if reader == nil {
		reader = c
	}
	if err := resourceownership.RequireOwnerProof("observe voter rollout", sts, cluster); err != nil {
		return err
	}
	pods, err := observeVoterPods(ctx, reader, sts, target, replacing)
	if err != nil {
		return resetVoterObservation(ctx, c, sts, err)
	}
	if membership == nil {
		return resetVoterObservation(ctx, c, sts, fmt.Errorf("authenticated Raft membership observer is unavailable"))
	}
	configuration, err := membership.ReadRaftConfiguration(ctx, logger, cluster)
	if err != nil {
		return resetVoterObservation(ctx, c, sts, err)
	}
	state, err := membership.ReadRaftAutopilotState(ctx, logger, cluster)
	if err != nil {
		return resetVoterObservation(ctx, c, sts, err)
	}
	names := make([]string, len(pods))
	for i := range pods {
		names[i] = pods[i].Name
	}
	excluded := ""
	if replacing {
		excluded = target
	}
	health, err := portopenbao.CheckVoterDisruption(configuration, state, names, excluded, portopenbao.BuildAutopilotConfig(cluster))
	if err != nil {
		return resetVoterObservation(ctx, c, sts, err)
	}
	contextKey := rolloutDigest(struct {
		StatefulSet, Revision, Template, Target, Leader string
		Replacing                                       bool
		Term, Configuration                             uint64
		Pods                                            []voterPodObservation
	}{string(sts.UID), sts.Status.UpdateRevision, sts.Annotations[constants.AnnotationVoterRolloutTemplate], target, health.LeaderID,
		replacing, health.LeaderTerm, configuration.Config.Index, pods})
	// Membership calls can take time. Re-read identities before recording or
	// accepting the observation, and bind every write to the live StatefulSet RV.
	live := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(sts), live); err != nil {
		return err
	}
	if live.UID != sts.UID || live.Generation != sts.Generation || live.Status.UpdateRevision != sts.Status.UpdateRevision {
		return fmt.Errorf("%w: StatefulSet changed during Raft observation", portworkload.ErrVoterRolloutWaiting)
	}
	confirmedPods, err := observeVoterPods(ctx, reader, live, target, replacing)
	if err != nil || rolloutDigest(confirmedPods) != rolloutDigest(pods) {
		return resetVoterObservation(ctx, c, live, fmt.Errorf("voter Pod identities or readiness changed during Raft observation"))
	}
	now := time.Now().UTC()
	current := voterRolloutObservation{Context: contextKey, Followers: make(map[string]voterFollowerObservation, len(health.Followers)), LeaderIndex: health.LeaderIndex, ObservedAt: now}
	for pod, peer := range health.Followers {
		current.Followers[pod] = voterFollowerObservation{Index: peer.LastIndex, LastContact: peer.LastContact}
	}
	var previous voterRolloutObservation
	decodeErr := json.Unmarshal([]byte(live.Annotations[constants.AnnotationVoterRolloutObservation]), &previous)
	if decodeErr != nil || previous.Context != current.Context || previous.ObservedAt.After(now) || now.Sub(previous.ObservedAt) > voterObservationLifetime {
		if err := writeVoterObservation(ctx, c, live, current); err != nil {
			return err
		}
		return fmt.Errorf("%w: awaiting a fresh Autopilot observation for the current Pods", portworkload.ErrVoterRolloutWaiting)
	}
	for pod, sample := range current.Followers {
		before, found := previous.Followers[pod]
		if !found || !hasFreshVoterContact(before, sample) {
			return fmt.Errorf("%w: Autopilot has not refreshed voter %s observation", portworkload.ErrVoterRolloutWaiting, pod)
		}
	}
	for pod, index := range health.Indices {
		if index < previous.LeaderIndex {
			return fmt.Errorf("%w: voter %s has not reached observed leader index %d", portworkload.ErrVoterRolloutWaiting, pod, previous.LeaderIndex)
		}
	}
	return nil
}

func observeVoterPods(ctx context.Context, reader client.Reader, sts *appsv1.StatefulSet, target string, replacing bool) ([]voterPodObservation, error) {
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas < 1 || sts.Status.ObservedGeneration < sts.Generation || sts.Status.UpdateRevision == "" {
		return nil, fmt.Errorf("voter StatefulSet has not observed the desired voter template")
	}
	pods := make([]voterPodObservation, 0, *sts.Spec.Replicas)
	targetOrdinal := int32(-1)
	for ordinal := int32(0); ordinal < *sts.Spec.Replicas; ordinal++ {
		if fmt.Sprintf("%s-%d", sts.Name, ordinal) == target {
			targetOrdinal = ordinal
		}
	}
	if target != "" && targetOrdinal < 0 {
		return nil, fmt.Errorf("target %s is outside the voter StatefulSet", target)
	}
	for ordinal := int32(0); ordinal < *sts.Spec.Replicas; ordinal++ {
		name := fmt.Sprintf("%s-%d", sts.Name, ordinal)
		pod := &corev1.Pod{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: name}, pod); err != nil {
			return nil, err
		}
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.UID != sts.UID || owner.Kind != "StatefulSet" || pod.UID == "" {
			return nil, fmt.Errorf("voter Pod %s does not match the StatefulSet identity", name)
		}
		observation := voterPodObservation{Name: name, UID: string(pod.UID)}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready, observation.ReadySince = true, condition.LastTransitionTime
			}
		}
		if name != target || !replacing {
			if !ready || pod.DeletionTimestamp != nil {
				return nil, fmt.Errorf("waiting for voter Pod %s readiness", name)
			}
		}
		ordered := sts.Annotations[constants.AnnotationVoterTemplateRepair] == ""
		if (target == "" || (ordered && ordinal > targetOrdinal)) && pod.Labels[appsv1.StatefulSetRevisionLabel] != sts.Status.UpdateRevision {
			return nil, fmt.Errorf("waiting for the updated revision on voter Pod %s", name)
		}
		pods = append(pods, observation)
	}
	return pods, nil
}

// Contact age grows without a heartbeat. Only an advanced replication index or
// a reduced age establishes a newer follower observation. Other changed fields,
// including a growing LastContact duration, do not establish fresh contact.
func hasFreshVoterContact(before, current voterFollowerObservation) bool {
	if current.Index > before.Index {
		return true
	}
	oldAge, oldErr := time.ParseDuration(before.LastContact)
	newAge, newErr := time.ParseDuration(current.LastContact)
	return oldErr == nil && newErr == nil && newAge >= 0 && newAge < oldAge
}

func rolloutDigest(value any) string {
	// Only fixed structs, slices and decoded API responses reach this boundary.
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func writeVoterObservation(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, observation voterRolloutObservation) error {
	data, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	updated := sts.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[constants.AnnotationVoterRolloutObservation] = string(data)
	return c.Patch(ctx, updated, client.MergeFromWithOptions(sts, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout))
}

func resetVoterObservation(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, reason error) error {
	if sts.Annotations[constants.AnnotationVoterRolloutObservation] != "" {
		updated := sts.DeepCopy()
		delete(updated.Annotations, constants.AnnotationVoterRolloutObservation)
		if err := c.Patch(ctx, updated, client.MergeFromWithOptions(sts, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: %w", portworkload.ErrVoterRolloutWaiting, reason)
}

// CheckVoterRollout implements the guard shared by ordinary and managed upgrades.
func (m *Manager) CheckVoterRollout(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet, target string, replacing bool) error {
	return checkVoterRollout(ctx, logger, m.client, m.reader, m.raftMembership, cluster, sts, target, replacing)
}
