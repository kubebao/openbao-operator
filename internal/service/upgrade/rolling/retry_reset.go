package rolling

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

const voterRetryHoldValue = "true"

type retryResetContext struct {
	Cluster     types.UID `json:"clusterUID"`
	StatefulSet types.UID `json:"statefulSetUID"`
	Revision    string    `json:"revision"`
	Image       string    `json:"image"`
	Attempt     string    `json:"attempt"`
	StartedAt   string    `json:"startedAt"`
	Request     string    `json:"request"`
}

type retryResetPod struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

type retryResetPlan struct {
	Version  int               `json:"version"`
	Context  retryResetContext `json:"context"`
	Pods     []retryResetPod   `json:"pods"`
	Complete bool              `json:"complete,omitempty"`
}

// resetRolloutPodsForRetry persists old Pod identities before resetting any Pod.
// A replacement UID observes the previous deletion, even if its response or the
// retry acknowledgement was lost. Each remaining deletion gets a fresh Raft gate.
func (m *Manager) resetRolloutPodsForRetry(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return nil
	}
	sts := &appsv1.StatefulSet{}
	if err := m.jobReader().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: upgrade.StableVoterStatefulSetName(cluster)}, sts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	held := sts.Annotations[constants.AnnotationVoterRetryHold] != ""
	if err := resourceownership.RequireOwnerProof("prepare rolling retry resets", sts, cluster); err != nil {
		return err
	}
	previous, found, err := readRetryResetPlan(sts)
	if err != nil {
		return err
	}
	if found && previous.Context == retryResetIdentity(cluster, sts) && previous.Complete {
		if err := validateRetryResetPlan(sts, previous); err != nil {
			return err
		}
		if !retryTemplateCurrent(sts, cluster) {
			return retryResetWaiting("StatefulSet template has not observed the rendered retry target")
		}
		return nil // Retry acknowledgement was lost; do not repeat completed repairs.
	}
	if cluster.Spec.Replicas > 1 {
		if err := m.holdVotersForRetry(ctx, sts); err != nil {
			return err
		}
		held = true
	}
	if !retryTemplateCurrent(sts, cluster) {
		return retryResetWaiting("StatefulSet template has not observed the rendered retry target")
	}
	plan, err := m.prepareRetryResetPlan(ctx, cluster, sts, held)
	if err != nil {
		return err
	}
	for i, reset := range plan.Pods {
		pod := &corev1.Pod{}
		err := m.jobReader().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: reset.Name}, pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if pod.UID != reset.UID {
			continue
		}
		if retryPodMatchesTemplate(pod, sts) {
			continue
		}
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "StatefulSet" || owner.UID != sts.UID {
			return retryResetWaiting("retry Pod does not match the StatefulSet owner")
		}
		if pod.DeletionTimestamp != nil {
			return retryResetWaiting("waiting for a previously requested Pod deletion")
		}
		if err := m.checkRaftVoterRollout(ctx, logger, cluster, reset.Name, true); err != nil {
			return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("hold rolling retry reset: %w", err))
		}
		if err := m.client.Delete(ctx, pod, client.Preconditions{UID: &reset.UID}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		logger.Info("Reset voter Pod for rolling retry", "pod", reset.Name, "uid", reset.UID)
		if i < len(plan.Pods)-1 {
			return retryResetWaiting("waiting for the reset voter before another replacement")
		}
	}
	if held {
		return m.finishVoterRetryRepairs(ctx, logger, cluster, sts, plan)
	}
	return nil
}

func retryTemplateCurrent(sts *appsv1.StatefulSet, cluster *openbaov1alpha1.OpenBaoCluster) bool {
	resolved := strings.TrimSpace(sts.Annotations[constants.AnnotationResolvedMainImage])
	return statefulSetTemplateCurrent(sts, cluster) && resolved != "" && strings.TrimSpace(baoContainerImage(sts.Spec.Template.Spec.Containers)) == resolved
}

func (m *Manager) holdVotersForRetry(ctx context.Context, sts *appsv1.StatefulSet) error {
	if sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType && sts.Annotations[constants.AnnotationVoterRetryHold] != "" {
		return nil
	}
	updated := sts.DeepCopy()
	updated.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[constants.AnnotationVoterRetryHold] = voterRetryHoldValue
	if err := m.client.Patch(ctx, updated, client.MergeFromWithOptions(sts, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
		return err
	}
	*sts = *updated.DeepCopy()
	return nil
}

func retryResetIdentity(cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet) retryResetContext {
	identity := retryResetContext{Cluster: cluster.UID, StatefulSet: sts.UID, Revision: sts.Status.UpdateRevision,
		Image: baoContainerImage(sts.Spec.Template.Spec.Containers), Attempt: cluster.Status.Upgrade.ExecutionID, Request: upgrade.RetryRequestValue(cluster)}
	if started := cluster.Status.Upgrade.StartedAt; started != nil {
		identity.StartedAt = started.UTC().Format(time.RFC3339Nano)
	}
	return identity
}

func readRetryResetPlan(sts *appsv1.StatefulSet) (retryResetPlan, bool, error) {
	var plan retryResetPlan
	data := sts.Annotations[constants.AnnotationVoterRetryReset]
	if data == "" {
		return plan, false, nil
	}
	if err := json.Unmarshal([]byte(data), &plan); err != nil || plan.Version != 1 {
		return plan, true, retryResetWaiting("invalid rolling retry reset checkpoint")
	}
	return plan, true, nil
}

func retryResetWaiting(reason string) error {
	return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("%s", reason))
}

func (m *Manager) prepareRetryResetPlan(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet, held bool) (retryResetPlan, error) {
	identity := retryResetIdentity(cluster, sts)
	previous, found, err := readRetryResetPlan(sts)
	if err != nil {
		return previous, err
	}
	if found {
		if previous.Context == identity {
			if err := validateRetryResetPlan(sts, previous); err != nil {
				return previous, err
			}
			return previous, nil
		}
	}
	plan := retryResetPlan{Version: 1, Context: identity}
	ordinals := retryResetOrdinals(cluster)
	if held {
		ordinals = nil
		if sts.Spec.Replicas != nil {
			for n := int32(0); n < *sts.Spec.Replicas; n++ {
				ordinals = append(ordinals, n)
			}
		}
	}
	sort.Slice(ordinals, func(i, j int) bool { return ordinals[i] > ordinals[j] })
	partition := int32(0)
	if update := sts.Spec.UpdateStrategy.RollingUpdate; update != nil && update.Partition != nil {
		partition = *update.Partition
	}
	for _, ordinal := range ordinals {
		if !held && ordinal < partition {
			continue
		} // Not released: normal advancement owns this replacement.
		pod := &corev1.Pod{}
		name := upgrade.StableVoterPodName(cluster, ordinal)
		if err := m.jobReader().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return plan, err
		}
		if retryPodMatchesTemplate(pod, sts) {
			continue
		}
		plan.Pods = append(plan.Pods, retryResetPod{Name: name, UID: pod.UID})
	}
	if err := validateRetryResetPlan(sts, plan); err != nil {
		return plan, err
	}
	if len(plan.Pods) == 0 && !held {
		return plan, nil
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	updated := sts.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[constants.AnnotationVoterRetryReset] = string(data)
	if err := m.client.Patch(ctx, updated, client.MergeFromWithOptions(sts, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout)); err != nil {
		return plan, err
	}
	return plan, nil
}

func retryPodMatchesTemplate(pod *corev1.Pod, sts *appsv1.StatefulSet) bool {
	return isPodReady(pod) && pod.Labels[appsv1.StatefulSetRevisionLabel] == sts.Status.UpdateRevision &&
		baoContainerImage(pod.Spec.Containers) == baoContainerImage(sts.Spec.Template.Spec.Containers)
}

func validateRetryResetPlan(sts *appsv1.StatefulSet, plan retryResetPlan) error {
	allowed := map[string]bool{}
	if sts.Spec.Replicas != nil {
		for n := int32(0); n < *sts.Spec.Replicas; n++ {
			allowed[fmt.Sprintf("%s-%d", sts.Name, n)] = true
		}
	}
	seen := map[string]bool{}
	seenUIDs := map[types.UID]bool{}
	for _, pod := range plan.Pods {
		if !allowed[pod.Name] || pod.UID == "" || seen[pod.Name] || seenUIDs[pod.UID] {
			return retryResetWaiting("retry reset checkpoint has invalid voter identities")
		}
		seen[pod.Name] = true
		seenUIDs[pod.UID] = true
	}
	return nil
}

// Convert only after every voter has the requested revision and fresh Raft health.
// RollingUpdate at partition zero then changes no Pod and lets Kubernetes converge
// its revision counters before the existing managed finalization checks run.
func (m *Manager) finishVoterRetryRepairs(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, sts *appsv1.StatefulSet, plan retryResetPlan) error {
	if err := m.checkRaftVoterRollout(ctx, logger, cluster, "", false); err != nil {
		return operatorerrors.WrapTransientKubernetesAPI(fmt.Errorf("wait for repaired voter templates: %w", err))
	}
	live := &appsv1.StatefulSet{}
	if err := m.jobReader().Get(ctx, client.ObjectKeyFromObject(sts), live); err != nil {
		return err
	}
	if live.UID != sts.UID || live.Generation != sts.Generation || live.Status.UpdateRevision != sts.Status.UpdateRevision {
		return rollingAttemptChanged()
	}
	updated := live.DeepCopy()
	partition := int32(0)
	updated.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}}
	delete(updated.Annotations, constants.AnnotationVoterRetryHold)
	plan.Complete = true
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	updated.Annotations[constants.AnnotationVoterRetryReset] = string(data)
	return m.client.Patch(ctx, updated, client.MergeFromWithOptions(live, client.MergeFromWithOptimisticLock{}), client.FieldOwner(constants.FieldOwnerVoterRollout))
}
