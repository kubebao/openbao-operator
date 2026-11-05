package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// custody partitions the Cluster's managed resources by which root controls
// them. Discovery runs in both directions: a resource the "to" root already
// owns is a blocker for the forward scan but a transfer for the reverse one.
type custody struct {
	pending  []primitives.TransferRequest
	settled  []primitives.TransferIdentity
	blockers []primitives.DiscoveryBlocker
}

// custodyScope is what discovery needs besides the two roots: the pinned
// namespace, a Cluster's retained resources, and the restore records whose Jobs
// carry the Cluster's labels but move with their record.
type custodyScope struct {
	namespaceUID types.UID
	retained     []primitives.TransferIdentity
	foreign      []primitives.TransferIdentity
}

func (s custodyScope) options(source, destination primitives.TransferIdentity) primitives.DiscoveryOptions {
	return primitives.DiscoveryOptions{NamespaceUID: s.namespaceUID, Source: source, Destination: destination,
		RetainedResources: s.retained, ForeignRoots: s.foreign}
}

func discoverCustody(ctx context.Context, k *Kube, scope custodyScope, from, to primitives.TransferIdentity) (*custody, error) {
	forward, err := primitives.DiscoverManagedResources(ctx, k.metadata, scope.options(from, to))
	if err != nil {
		return nil, fmt.Errorf("discover resources owned by %s: %w", from.APIVersion, err)
	}
	reverse, err := primitives.DiscoverManagedResources(ctx, k.metadata, scope.options(to, from))
	if err != nil {
		return nil, fmt.Errorf("discover resources owned by %s: %w", to.APIVersion, err)
	}
	return partitionCustody(forward, reverse), nil
}

// partitionCustody merges the two discovery directions. Each direction reports
// the other side's resources, and the other side's StatefulSet descendants, as
// blockers. A blocker is real only if the other direction cannot account for
// the resource either.
func partitionCustody(forward, reverse *primitives.ManagedDiscovery) *custody {
	key := func(identity primitives.TransferIdentity) string {
		return identity.APIVersion + "/" + identity.Kind + "/" + identity.Name + "/" + string(identity.UID)
	}
	accounted := func(discovery *primitives.ManagedDiscovery) map[string]bool {
		known := map[string]bool{}
		for _, transfer := range discovery.Transfers {
			known[key(transfer.Resource)] = true
		}
		for _, descendant := range discovery.Descendants {
			known[key(descendant.Resource)] = true
		}
		return known
	}
	forwardKnown, reverseKnown := accounted(forward), accounted(reverse)
	result := &custody{pending: forward.Transfers}
	for _, transfer := range reverse.Transfers {
		result.settled = append(result.settled, transfer.Resource)
	}
	seen := map[string]bool{}
	for _, pair := range []struct {
		blockers []primitives.DiscoveryBlocker
		other    map[string]bool
	}{{forward.Blockers, reverseKnown}, {reverse.Blockers, forwardKnown}} {
		for _, blocker := range pair.blockers {
			id := key(blocker.Resource)
			if !pair.other[id] && !seen[id] {
				seen[id] = true
				result.blockers = append(result.blockers, blocker)
			}
		}
	}
	return result
}

func (c *custody) blockerError() error {
	return blockerError(c.blockers)
}

func blockerError(blockers []primitives.DiscoveryBlocker) error {
	if len(blockers) == 0 {
		return nil
	}
	data, _ := json.Marshal(blockers)
	return fmt.Errorf("managed resources without provable custody: %s", data)
}

// convergeOwnership moves every resource controlled by from to to. Each
// transfer is conditional and idempotent, so re-running after an interruption
// only finishes the remaining resources.
func convergeOwnership(ctx context.Context, k *Kube, scope custodyScope, from, to primitives.TransferIdentity) (int, error) {
	state, err := discoverCustody(ctx, k, scope, from, to)
	if err != nil {
		return 0, err
	}
	if err := state.blockerError(); err != nil {
		return 0, err
	}
	for _, transfer := range state.pending {
		if err := primitives.TransferOwnership(ctx, k.handoff, transfer); err != nil {
			return 0, fmt.Errorf("transfer %s %s: %w", transfer.Resource.Kind, transfer.Resource.Name, err)
		}
		if err := afterOwnershipMoved(); err != nil {
			return 0, err
		}
	}
	after, err := discoverCustody(ctx, k, scope, from, to)
	if err != nil {
		return 0, err
	}
	if err := after.blockerError(); err != nil {
		return 0, err
	}
	if len(after.pending) != 0 {
		return 0, fmt.Errorf("%d resources still reference %s after transfer", len(after.pending), from.APIVersion)
	}
	return len(state.pending), nil
}

// ownedStatefulSets lists the Cluster's StatefulSets under the given owner.
func ownedStatefulSets(ctx context.Context, k *Kube, owner primitives.TransferIdentity) ([]appsv1.StatefulSet, error) {
	sets := &appsv1.StatefulSetList{}
	if err := k.client.List(ctx, sets, client.InNamespace(owner.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: owner.Name}); err != nil {
		return nil, err
	}
	var owned []appsv1.StatefulSet
	for _, set := range sets.Items {
		if ref := metav1.GetControllerOf(&set); ref != nil && ref.UID == owner.UID {
			owned = append(owned, set)
		}
	}
	return owned, nil
}

func claimTemplatesOwnedBy(set *appsv1.StatefulSet, uid types.UID) bool {
	for _, template := range set.Spec.VolumeClaimTemplates {
		if value, ok := template.Annotations[constants.AnnotationOpenBaoOwnerUID]; ok && value != string(uid) {
			return false
		}
	}
	return true
}

// recreatedStatefulSet returns the exact specification with the claim
// template provenance moved to owner. Server-populated metadata is dropped.
// The Pod template is untouched, so the new StatefulSet adopts the running
// Pods and ControllerRevisions without a rollout.
func recreatedStatefulSet(saved *appsv1.StatefulSet, owner primitives.TransferIdentity) (*appsv1.StatefulSet, error) {
	ref := metav1.GetControllerOf(saved)
	if ref == nil || ref.APIVersion != owner.APIVersion || ref.Kind != owner.Kind || ref.Name != owner.Name ||
		ref.UID != owner.UID || saved.Annotations[constants.AnnotationOpenBaoOwnerUID] != string(owner.UID) {
		return nil, errors.New("statefulset ownership must be transferred before it is recreated")
	}
	set := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{
			Name: saved.Name, Namespace: saved.Namespace, Labels: saved.Labels, Annotations: saved.Annotations,
			OwnerReferences: saved.OwnerReferences,
		},
		Spec: *saved.Spec.DeepCopy(),
	}
	for i := range set.Spec.VolumeClaimTemplates {
		template := &set.Spec.VolumeClaimTemplates[i]
		if _, ok := template.Annotations[constants.AnnotationOpenBaoOwnerUID]; ok {
			template.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(owner.UID)
		}
	}
	return set, nil
}

// recreateStatefulSets moves the immutable claim-template provenance to owner by
// orphaning and recreating each StatefulSet. The manifest is saved before the
// delete, so an interruption between delete and create can be resumed.
func recreateStatefulSets(ctx context.Context, k *Kube, state *State, cluster ClusterPlan, owner primitives.TransferIdentity) (int, error) {
	dir := state.path("statefulsets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	saved, err := filepath.Glob(filepath.Join(dir, cluster.Namespace+"_*.json"))
	if err != nil {
		return 0, err
	}
	names := map[string]bool{}
	for _, path := range saved {
		set := &appsv1.StatefulSet{}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, set) != nil {
			return 0, fmt.Errorf("read saved StatefulSet %s", path)
		}
		if set.Labels[constants.LabelOpenBaoCluster] == cluster.Name {
			names[set.Name] = true
		}
	}
	sets, err := ownedStatefulSets(ctx, k, owner)
	if err != nil {
		return 0, err
	}
	for _, set := range sets {
		names[set.Name] = true
	}
	recreated := 0
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range names {
			if !yield(name) {
				return
			}
		}
	}) {
		changed, err := recreateStatefulSet(ctx, k, filepath.Join(dir, cluster.Namespace+"_"+name+".json"),
			cluster.Namespace, name, owner)
		if err != nil {
			return recreated, err
		}
		if changed {
			recreated++
		}
	}
	return recreated, nil
}

func recreateStatefulSet(ctx context.Context, k *Kube, savePath, namespace, name string,
	owner primitives.TransferIdentity,
) (bool, error) {
	current := &appsv1.StatefulSet{}
	err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, current)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return false, err
	case !current.DeletionTimestamp.IsZero():
		// An earlier run's orphan delete is still in progress.
	default:
		if claimTemplatesOwnedBy(current, owner.UID) {
			ref := metav1.GetControllerOf(current)
			if ref == nil || ref.UID != owner.UID {
				return false, fmt.Errorf("statefulset %s is not controlled by %s", name, owner.APIVersion)
			}
			// A previous run may have stopped before the Pods were adopted.
			return false, k.wait(ctx, "StatefulSet "+name+" Pod adoption", func(ctx context.Context) (bool, error) {
				return podsAdopted(ctx, k, current, savedReady(savePath))
			})
		}
		if _, err := recreatedStatefulSet(current, owner); err != nil {
			return false, fmt.Errorf("statefulset %s: %w", name, err)
		}
		if err := writePrivateJSON(savePath, current); err != nil {
			return false, fmt.Errorf("save StatefulSet %s before recreation: %w", name, err)
		}
		uid := current.UID
		orphan := metav1.DeletePropagationOrphan
		if err := k.migrator.Delete(ctx, current, client.Preconditions{UID: &uid, ResourceVersion: &current.ResourceVersion},
			client.PropagationPolicy(orphan)); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("orphan StatefulSet %s: %w", name, err)
		}
		if err := afterStatefulSetOrphaned(); err != nil {
			return false, err
		}
	}
	data, err := os.ReadFile(savePath)
	if err != nil {
		return false, fmt.Errorf("statefulset %s is absent and no saved manifest exists: %w", name, err)
	}
	saved := &appsv1.StatefulSet{}
	if err := json.Unmarshal(data, saved); err != nil {
		return false, err
	}
	if err := k.wait(ctx, "orphaned StatefulSet "+name+" removal", func(ctx context.Context) (bool, error) {
		set := &appsv1.StatefulSet{}
		err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, set)
		if apierrors.IsNotFound(err) {
			return podsReleased(ctx, k, saved)
		}
		return false, err
	}); err != nil {
		return false, err
	}
	desired, err := recreatedStatefulSet(saved, owner)
	if err != nil {
		return false, fmt.Errorf("statefulset %s: %w", name, err)
	}
	if err := k.migrator.Create(ctx, desired, client.FieldOwner(toolFieldManager)); err != nil {
		return false, fmt.Errorf("recreate StatefulSet %s: %w", name, err)
	}
	return true, k.wait(ctx, "StatefulSet "+name+" Pod adoption", func(ctx context.Context) (bool, error) {
		return podsAdopted(ctx, k, desired, saved.Status.ReadyReplicas)
	})
}

// finishStatefulSets completes a recreation that an interrupted run left
// between the orphan delete and the create. Until then the StatefulSet's Pods
// have no controller, and custody discovery refuses them, so rollback runs this
// before it moves anything. The handoff admits a StatefulSet only with its claim
// templates moved to its controller, so the StatefulSet is recreated for the
// owner it was saved with; the rollback's own steps then move it back.
func finishStatefulSets(ctx context.Context, k *Kube, state *State, cluster ClusterPlan) (int, error) {
	paths, err := filepath.Glob(filepath.Join(state.path("statefulsets"), cluster.Namespace+"_*.json"))
	if err != nil {
		return 0, err
	}
	finished := 0
	for _, path := range paths {
		saved := &appsv1.StatefulSet{}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, saved) != nil {
			return finished, fmt.Errorf("read saved StatefulSet %s", path)
		}
		if saved.Labels[constants.LabelOpenBaoCluster] != cluster.Name {
			continue
		}
		current := &appsv1.StatefulSet{}
		err = k.client.Get(ctx, client.ObjectKeyFromObject(saved), current)
		if err == nil && current.DeletionTimestamp.IsZero() {
			continue
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return finished, err
		}
		var owner primitives.TransferIdentity
		switch ref := metav1.GetControllerOf(saved); {
		case ref != nil && ref.UID == cluster.SourceUID:
			owner = cluster.source()
		case ref != nil && cluster.DestinationUID != "" && ref.UID == cluster.DestinationUID:
			owner = cluster.destination()
		default:
			return finished, fmt.Errorf("saved StatefulSet %s has no planned controller", saved.Name)
		}
		if _, err := recreateStatefulSet(ctx, k, path, saved.Namespace, saved.Name, owner); err != nil {
			return finished, err
		}
		finished++
	}
	return finished, nil
}

// savedReady is how many Pods were ready when the StatefulSet was saved. A
// missing save requires all of them.
func savedReady(savePath string) int32 {
	saved := &appsv1.StatefulSet{}
	data, err := os.ReadFile(savePath)
	if err != nil || json.Unmarshal(data, saved) != nil {
		return math.MaxInt32
	}
	return saved.Status.ReadyReplicas
}

func setPods(ctx context.Context, k *Kube, set *appsv1.StatefulSet) ([]corev1.Pod, error) {
	selector, err := metav1.LabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods := &corev1.PodList{}
	err = k.client.List(ctx, pods, client.InNamespace(set.Namespace), client.MatchingLabelsSelector{Selector: selector})
	return pods.Items, err
}

// podsReleased reports whether garbage collection removed the orphaned
// StatefulSet's controller references. Adoption requires ownerless Pods.
func podsReleased(ctx context.Context, k *Kube, saved *appsv1.StatefulSet) (bool, error) {
	pods, err := setPods(ctx, k, saved)
	if err != nil {
		return false, err
	}
	for _, pod := range pods {
		if ref := metav1.GetControllerOf(&pod); ref != nil && ref.Kind == statefulSetKind && ref.Name == saved.Name {
			return false, nil
		}
	}
	return true, nil
}

// podsAdopted requires the recreated StatefulSet to control every Pod and as
// many to be ready as before: all of a running Cluster's, and those a paused
// Cluster had ready.
func podsAdopted(ctx context.Context, k *Kube, desired *appsv1.StatefulSet, readyBefore int32) (bool, error) {
	set := &appsv1.StatefulSet{}
	if err := k.client.Get(ctx, client.ObjectKeyFromObject(desired), set); err != nil {
		return false, err
	}
	pods, err := setPods(ctx, k, set)
	if err != nil {
		return false, err
	}
	want := int32(1)
	if set.Spec.Replicas != nil {
		want = *set.Spec.Replicas
	}
	adopted := int32(0)
	for _, pod := range pods {
		if ref := metav1.GetControllerOf(&pod); ref != nil && ref.UID == set.UID {
			adopted++
		}
	}
	return adopted == want && set.Status.ReadyReplicas >= min(want, readyBefore), nil
}
