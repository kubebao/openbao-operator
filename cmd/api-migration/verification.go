package main

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

// These observations exclude resource versions: status refreshes and unrelated
// metadata writes do not invalidate evidence collected against the same workload.
type workloadObservation struct {
	Sets []statefulSetObservation
	Pods map[string]podObservation
}

type statefulSetObservation struct {
	Name, Template, CurrentRevision, UpdateRevision string
	ClusterGeneration                               string
	UID                                             types.UID
	Generation                                      int64
}

type podObservation struct {
	UID             types.UID
	Ready, Deleting bool
}

// verifyStableWorkloads fences the entire pass, including later Clusters and
// record checks. A changed Cluster repeats its custody, storage, login and Raft
// checks after its workload settles; unchanged Clusters keep their evidence.
func (p *Procedure) verifyStableWorkloads(ctx context.Context, apiVersion string) (string, error) {
	verified := map[string]workloadObservation{}
	details := map[string]string{}
	var result string
	err := p.kube.wait(ctx, "verification against settled workloads", func(ctx context.Context) (bool, error) {
		if err := p.waitServing(ctx, apiVersion); err != nil {
			return false, err
		}
		before, err := p.waitWorkloads(ctx, apiVersion)
		if err != nil {
			return false, err
		}
		failures := map[string]error{}
		clusters, err := p.forEachCluster(func(cluster *ClusterPlan) (string, error) {
			key := cluster.Namespace + "/" + cluster.Name
			if previous, ok := verified[key]; ok && reflect.DeepEqual(previous, before[key]) {
				return details[key], nil
			}
			detail, err := p.verifyCluster(ctx, apiVersion, cluster)
			if err == nil {
				verified[key], details[key] = before[key], detail
			} else {
				failures[key] = err
			}
			return detail, nil
		})
		if err != nil {
			return false, err
		}
		restores, err := p.verifyRecords(ctx, p.plan.Restores, apiVersion)
		if err != nil {
			return false, err
		}
		tenants, err := p.verifyRecords(ctx, p.plan.Tenants, apiVersion)
		if err != nil {
			return false, err
		}
		after, err := p.waitWorkloads(ctx, apiVersion)
		if err != nil {
			return false, err
		}
		for _, cluster := range p.plan.Clusters {
			key := cluster.Namespace + "/" + cluster.Name
			if err := failures[key]; err != nil && reflect.DeepEqual(before[key], after[key]) {
				return false, fmt.Errorf("%s: %w", key, err)
			}
		}
		result = joinDetails(clusters, restores, tenants)
		return reflect.DeepEqual(before, after), nil
	})
	return result, err
}

func (p *Procedure) waitWorkloads(ctx context.Context, apiVersion string) (map[string]workloadObservation, error) {
	observed := map[string]workloadObservation{}
	err := p.kube.wait(ctx, "rendered StatefulSet rollouts to settle", func(ctx context.Context) (bool, error) {
		for _, cluster := range p.plan.Clusters {
			observation, settled, err := p.observeVerifiedWorkload(ctx, apiVersion, cluster)
			if err != nil || !settled {
				return false, err
			}
			observed[cluster.Namespace+"/"+cluster.Name] = observation
		}
		return true, nil
	})
	return observed, err
}

func (p *Procedure) observeVerifiedWorkload(ctx context.Context, apiVersion string,
	cluster ClusterPlan,
) (workloadObservation, bool, error) {
	observation := workloadObservation{Pods: map[string]podObservation{}}
	sets := &appsv1.StatefulSetList{}
	if err := p.kube.client.List(ctx, sets, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
		return observation, false, err
	}
	required, generation, err := p.requiredWorkloadRenders(ctx, apiVersion, cluster)
	if err != nil {
		return observation, false, err
	}
	owners := map[types.UID]int32{}
	for i := range sets.Items {
		set := &sets.Items[i]
		if !statefulSetSettled(set, !cluster.Paused) {
			return observation, false, nil
		}
		if required[set.Name] {
			if set.Annotations[constants.AnnotationClusterGeneration] != generation {
				return observation, false, nil
			}
			delete(required, set.Name)
		}
		owners[set.UID] = 1
		if set.Spec.Replicas != nil {
			owners[set.UID] = *set.Spec.Replicas
		}
		observation.Sets = append(observation.Sets, statefulSetObservation{Name: set.Name, UID: set.UID,
			Generation: set.Generation, Template: digestJSON(set.Spec.Template),
			ClusterGeneration: set.Annotations[constants.AnnotationClusterGeneration],
			CurrentRevision:   set.Status.CurrentRevision, UpdateRevision: set.Status.UpdateRevision})
	}
	if len(required) != 0 {
		return observation, false, nil
	}
	slices.SortFunc(observation.Sets, func(a, b statefulSetObservation) int { return cmp.Compare(a.Name, b.Name) })
	pods := &corev1.PodList{}
	if err := p.kube.client.List(ctx, pods, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
		return observation, false, err
	}
	counts := map[types.UID]int32{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != statefulSetKind {
			continue
		}
		if _, managed := owners[owner.UID]; managed {
			counts[owner.UID]++
			observation.Pods[pod.Name] = podObservation{UID: pod.UID, Ready: podReady(pod), Deleting: !pod.DeletionTimestamp.IsZero()}
			if !cluster.Paused && !podReady(pod) {
				return observation, false, nil
			}
		}
	}
	if !cluster.Paused && !reflect.DeepEqual(counts, nonzeroReplicaCounts(owners)) {
		return observation, false, nil
	}
	if cluster.Paused {
		ready, err := readyPods(ctx, p.kube, cluster)
		if err != nil {
			return observation, false, err
		}
		for _, name := range cluster.ReadyPods {
			if !slices.Contains(ready, name) {
				return observation, false, nil
			}
		}
	}
	return observation, true, nil
}

// Paused workloads do not render. Older saved plans remain rollback-capable,
// but cannot prove that recreation invalidated a historical render marker.
func (p *Procedure) requiredWorkloadRenders(ctx context.Context, apiVersion string,
	cluster ClusterPlan,
) (map[string]bool, string, error) {
	required := map[string]bool{}
	if cluster.Paused || p.plan.Version < planVersion {
		return required, "", nil
	}
	return workloadRenderTargets(ctx, p.kube, apiVersion, cluster)
}

func nonzeroReplicaCounts(counts map[types.UID]int32) map[types.UID]int32 {
	result := map[types.UID]int32{}
	for uid, count := range counts {
		if count != 0 {
			result[uid] = count
		}
	}
	return result
}

func workloadRenderTargets(ctx context.Context, k *Kube, apiVersion string,
	cluster ClusterPlan,
) (map[string]bool, string, error) {
	required := map[string]bool{}
	if cluster.Paused {
		return required, "", nil
	}
	object, err := k.getCluster(ctx, apiVersion, cluster.Namespace, cluster.Name)
	if err != nil {
		return nil, "", err
	}
	if object == nil {
		return nil, "", fmt.Errorf("%s Cluster %s/%s is missing", apiVersion, cluster.Namespace, cluster.Name)
	}
	typed := &api.OpenBaoCluster{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, typed); err != nil {
		return nil, "", fmt.Errorf("decode Cluster for workload render: %w", err)
	}
	required[cluster.voterStatefulSet()] = true
	if typed.Spec.ReadReplicas != nil {
		required[resourceidentity.ReadReplicaStatefulSetName(typed)] = true
	}
	return required, strconv.FormatInt(object.GetGeneration(), 10), nil
}
