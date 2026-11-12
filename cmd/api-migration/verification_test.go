package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

const verificationRevision = "destination-revision"

func verificationFixture(t *testing.T, namespaces ...string) (*Procedure, client.WithWatch) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	for _, group := range []string{primitives.PlanSourceAPIVersion, primitives.PlanDestinationAPIVersion} {
		for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
			gvk := schema.FromAPIVersionAndKind(group, kind)
			scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(kind+"List"), &unstructured.UnstructuredList{})
		}
	}
	for _, gvk := range networkKinds[1:] {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	p := &Procedure{plan: &Plan{Version: planVersion, Operator: OperatorPlan{Mode: tenancyMulti},
		Config: Config{OperatorNamespace: "operator"}, Deployments: []DeploymentPlan{{Name: "controller", Controller: true}}},
		state: &State{dir: t.TempDir()}, log: io.Discard}
	objects := make([]client.Object, 0, 1+8*len(namespaces))
	objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "operator"},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: managerContainer}}}}},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1, Replicas: 1}})
	for _, namespace := range namespaces {
		root := readyCluster()
		root.SetAPIVersion(primitives.PlanDestinationAPIVersion)
		root.SetKind(clusterKind)
		root.SetNamespace(namespace)
		root.SetUID(types.UID(namespace + "-cluster"))
		owner := primitives.TransferIdentity{APIVersion: root.GetAPIVersion(), Kind: clusterKind,
			Namespace: namespace, Name: root.GetName(), UID: root.GetUID()}
		set := statefulSetFixture(owner, string(owner.UID))
		set.Namespace, set.UID = namespace, types.UID(namespace+"-set")
		set.ManagedFields = nil
		set.Annotations[constants.AnnotationClusterGeneration] = "2"
		set.Spec.UpdateStrategy = partitionedStrategy(0)
		set.Status = appsv1.StatefulSetStatus{ObservedGeneration: set.Generation, Replicas: 3,
			ReadyReplicas: 3, UpdatedReplicas: 3, CurrentRevision: "old", UpdateRevision: "old"}
		cluster := ClusterPlan{Namespace: namespace, NamespaceUID: types.UID(namespace + "-namespace"), Name: "bao",
			DestinationUID: root.GetUID(), Pods: map[string]types.UID{}, LoginSkipped: "authentication is outside this orchestration fixture"}
		for ordinal := range 3 {
			name := fmt.Sprintf("bao-%d", ordinal)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
				UID: types.UID(namespace + "-" + name), Labels: set.Labels,
				Annotations: map[string]string{constants.AnnotationOpenBaoOwnerUID: string(owner.UID)},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: statefulSetKind,
					Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}},
				Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			cluster.Pods[name] = pod.UID
			objects = append(objects, pod)
		}
		claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-bao-0", Namespace: namespace,
			UID: types.UID(namespace + "-claim"), Annotations: set.Annotations, OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: statefulSetKind, Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}},
			Spec:   corev1.PersistentVolumeClaimSpec{VolumeName: namespace + "-volume"},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
		volume := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName, UID: types.UID(namespace + "-volume")},
			Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{UID: claim.UID}}}
		cluster.Claims = []ClaimBinding{{Name: claim.Name, UID: claim.UID, Volume: volume.Name, VolumeID: volume.UID}}
		objects = append(objects, root, set, claim, volume,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: cluster.NamespaceUID}})
		p.plan.Clusters = append(p.plan.Clusters, cluster)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	p.kube = &Kube{client: c, metadata: c, migrator: c, timeout: 10 * time.Second}
	return p, c
}

func renderVerificationWorkload(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	set := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: "bao"}, set))
	set.Generation++
	set.Annotations[constants.AnnotationClusterGeneration] = "2"
	set.Spec.Template.Annotations["render"] = "destination-template"
	require.NoError(t, c.Update(t.Context(), set))
	set.Status.ObservedGeneration = set.Generation
	set.Status.ReadyReplicas, set.Status.UpdatedReplicas, set.Status.Replicas = 3, 3, 3
	set.Status.CurrentRevision, set.Status.UpdateRevision = verificationRevision, verificationRevision
	require.NoError(t, c.Status().Update(t.Context(), set))
	pods := &corev1.PodList{}
	require.NoError(t, c.List(t.Context(), pods, client.InNamespace(namespace)))
	for i := range pods.Items {
		pod := pods.Items[i].DeepCopy()
		require.NoError(t, c.Delete(t.Context(), pod))
		pod.UID += "-destination"
		pod.ResourceVersion = ""
		require.NoError(t, c.Create(t.Context(), pod))
	}
}

func TestVerificationWaitsForRenderAfterClusterAvailability(t *testing.T) {
	p, base := verificationFixture(t, "team-a")
	set := &appsv1.StatefulSet{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "bao"}, set))
	delete(set.Annotations, constants.AnnotationClusterGeneration)
	require.NoError(t, base.Update(t.Context(), set))
	root, err := p.kube.getCluster(t.Context(), primitives.PlanDestinationAPIVersion, "team-a", "bao")
	require.NoError(t, err)
	require.True(t, clusterReady(root), "fresh availability must not substitute for a workload render")
	reads, checks := 0, 0
	p.kube.client = interceptor.NewClient(base, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*appsv1.StatefulSetList); ok {
				reads++
				if reads == 2 {
					renderVerificationWorkload(t, base, "team-a")
				}
			}
			return c.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				checks++
				require.GreaterOrEqual(t, reads, 2, "slow verification cannot precede the destination render")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	detail, err := p.verify(t.Context(), primitives.PlanDestinationAPIVersion)
	require.NoError(t, err)
	require.Equal(t, 1, checks)
	require.Contains(t, detail, "3 data Pod(s) replaced")
}

func TestVerificationRepeatsChangedClusterAfterLaterClusterChecks(t *testing.T) {
	p, base := verificationFixture(t, "team-a", "team-b")
	checks := map[string]int{}
	rolling, pendingReads := false, 0
	p.kube.client = interceptor.NewClient(base, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*appsv1.StatefulSetList); ok && rolling && pendingReads == 0 {
				pendingReads++
				// Capture an incomplete rollout, then allow it to converge. The
				// fence must reject that captured state before checking it again.
				err := c.List(ctx, list, opts...)
				renderVerificationWorkload(t, base, "team-a")
				return err
			}
			return c.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				checks[key.Namespace]++
				if key.Namespace == "team-b" && checks[key.Namespace] == 1 {
					set := &appsv1.StatefulSet{}
					require.NoError(t, base.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "bao"}, set))
					set.Generation++
					require.NoError(t, base.Update(ctx, set))
					set.Status.UpdateRevision, set.Status.UpdatedReplicas = verificationRevision, 1
					require.NoError(t, base.Status().Update(ctx, set))
					rolling = true
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	detail, err := p.verify(t.Context(), primitives.PlanDestinationAPIVersion)
	require.NoError(t, err)
	require.Equal(t, 1, pendingReads, "verification must observe and wait for the late rollout")
	require.Equal(t, map[string]int{"team-a": 2, "team-b": 1}, checks,
		"the global fence must repeat A when it changes during B's verification")
	require.Contains(t, detail, "3 data Pod(s) replaced")
}

func TestVerificationRequiresCurrentRenderAndRespectsStrategies(t *testing.T) {
	for _, strategy := range []appsv1.StatefulSetUpdateStrategy{
		{Type: appsv1.OnDeleteStatefulSetStrategyType}, partitionedStrategy(3), partitionedStrategy(1),
	} {
		p, base := verificationFixture(t, "tenant")
		set := &appsv1.StatefulSet{}
		require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: "tenant", Name: "bao"}, set))
		set.Spec.UpdateStrategy = strategy
		require.NoError(t, base.Update(t.Context(), set))
		set.Status.UpdateRevision, set.Status.UpdatedReplicas = verificationRevision, 2
		require.NoError(t, base.Status().Update(t.Context(), set))
		_, settled, err := p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, p.plan.Clusters[0])
		require.NoError(t, err)
		require.True(t, settled, "a fresh render does not require replacing deliberately retained old Pods")
		delete(set.Annotations, constants.AnnotationClusterGeneration)
		require.NoError(t, base.Update(t.Context(), set))
		_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, p.plan.Clusters[0])
		require.NoError(t, err)
		require.False(t, settled)
		p.plan.Clusters[0].Paused = true
		_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, p.plan.Clusters[0])
		require.NoError(t, err)
		require.True(t, settled, "paused workloads never restore the renderer marker")
	}
}

func TestVerificationRepeatsReadFailureOnlyAfterWorkloadChanges(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			p, base := verificationFixture(t, "tenant")
			checks := 0
			failure := errors.New("verification read failed")
			p.kube.client = interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
						checks++
						if checks == 1 {
							if changed {
								renderVerificationWorkload(t, base, "tenant")
							}
							return failure
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			_, err := p.verify(t.Context(), primitives.PlanDestinationAPIVersion)
			if changed {
				require.NoError(t, err)
				require.Equal(t, 2, checks)
			} else {
				require.ErrorIs(t, err, failure, "an unchanged workload must not hide a verification failure")
				require.Equal(t, 1, checks)
			}
		})
	}
}

func TestOldPlanRollbackDoesNotRequireUnrecordedRenderProvenance(t *testing.T) {
	p, base := verificationFixture(t, "tenant")
	p.plan.Version = 2
	set := &appsv1.StatefulSet{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: "tenant", Name: "bao"}, set))
	delete(set.Annotations, constants.AnnotationClusterGeneration)
	require.NoError(t, base.Update(t.Context(), set))
	_, settled, err := p.observeVerifiedWorkload(t.Context(), primitives.PlanSourceAPIVersion, p.plan.Clusters[0])
	require.NoError(t, err)
	require.True(t, settled, "legacy rollback retains its settlement checks without claiming fresh-render provenance")
	require.ErrorContains(t, p.validateForwardPlan(), "exact original migration binary")
}

func TestRecordedRecreationRequiresDifferentUIDAndSameOwner(t *testing.T) {
	p, base := verificationFixture(t, "tenant")
	set := &appsv1.StatefulSet{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: "tenant", Name: "bao"}, set))
	path := filepath.Join(p.state.dir, "saved.json")
	require.False(t, recordedStatefulSetRecreation(path, set))
	require.NoError(t, writePrivateJSON(path, set))
	require.False(t, recordedStatefulSetRecreation(path, set), "an existing matching claim template is not recreation evidence")
	current := set.DeepCopy()
	current.UID += "-recreated"
	require.True(t, recordedStatefulSetRecreation(path, current))
	current.OwnerReferences[0].UID = "another-owner"
	require.False(t, recordedStatefulSetRecreation(path, current))
}

func TestRenderBarrierIncludesDeclaredReadPoolButExemptsInactiveBlueGreen(t *testing.T) {
	p, base := verificationFixture(t, "tenant")
	cluster := &p.plan.Clusters[0]
	root, err := p.kube.getCluster(t.Context(), primitives.PlanDestinationAPIVersion, cluster.Namespace, cluster.Name)
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedMap(root.Object, map[string]any{"replicas": int64(0)}, "spec", "readReplicas"))
	require.NoError(t, base.Update(t.Context(), root))
	active := &appsv1.StatefulSet{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, active))
	inactive := active.DeepCopy()
	inactive.Name, inactive.UID, inactive.ResourceVersion = "bao-inactive", "inactive", ""
	inactive.Spec.Replicas = ptr.To(int32(0))
	inactive.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	inactive.Status.Replicas, inactive.Status.ReadyReplicas, inactive.Status.UpdatedReplicas = 0, 0, 0
	delete(inactive.Annotations, constants.AnnotationClusterGeneration)
	require.NoError(t, base.Create(t.Context(), inactive))
	_, settled, err := p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, *cluster)
	require.NoError(t, err)
	require.False(t, settled, "even a declared zero-replica read pool must have been rendered")
	read := inactive.DeepCopy()
	read.Name = resourceidentity.ReadReplicaStatefulSetName(&api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: cluster.Name}})
	read.UID, read.ResourceVersion = "read-set", ""
	require.NoError(t, base.Create(t.Context(), read))
	_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, *cluster)
	require.NoError(t, err)
	require.False(t, settled, "a settled read pool without a fresh render is insufficient")
	read.Annotations[constants.AnnotationClusterGeneration] = "2"
	require.NoError(t, base.Update(t.Context(), read))
	_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, *cluster)
	require.NoError(t, err)
	require.True(t, settled, "inactive BlueGreen sets need not regain the renderer marker")
}

func TestActiveWorkloadWithoutClaimProvenanceStillEstablishesRenderBarrier(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("paused=%t", paused), func(t *testing.T) {
			p, base := verificationFixture(t, "tenant")
			cluster := p.plan.Clusters[0]
			cluster.Paused = paused
			set := &appsv1.StatefulSet{}
			require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, set))
			set.Spec.Selector = &metav1.LabelSelector{MatchLabels: set.Labels}
			delete(set.Spec.VolumeClaimTemplates[0].Annotations, constants.AnnotationOpenBaoOwnerUID)
			require.NoError(t, base.Update(t.Context(), set))
			original := set.DeepCopy()
			p.kube.migrator = interceptor.NewClient(base, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					pods, err := setPods(ctx, p.kube, set)
					require.NoError(t, err)
					for i := range pods {
						pods[i].OwnerReferences = nil
						require.NoError(t, base.Update(ctx, &pods[i]))
					}
					return c.Delete(ctx, obj, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					obj.SetUID("recreated-set")
					if err := c.Create(ctx, obj, opts...); err != nil {
						return err
					}
					pods, err := setPods(ctx, p.kube, set)
					require.NoError(t, err)
					for i := range pods {
						pods[i].OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: statefulSetKind,
							Name: set.Name, UID: obj.GetUID(), Controller: ptr.To(true)}}
						require.NoError(t, base.Update(ctx, &pods[i]))
					}
					created := obj.(*appsv1.StatefulSet)
					created.Status = original.Status
					return base.Status().Update(ctx, created)
				},
			})
			count, err := recreateStatefulSets(t.Context(), p.kube, p.state, cluster, cluster.destination())
			require.NoError(t, err)
			if paused {
				require.Zero(t, count, "paused workloads keep the existing recreation exemption")
				return
			}
			require.Equal(t, 1, count, "an active workload must invalidate its copied renderer marker")
			current := &appsv1.StatefulSet{}
			require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(set), current))
			require.NotContains(t, current.Annotations, constants.AnnotationClusterGeneration)
			require.Equal(t, original.Spec, current.Spec, "missing claim annotations must not alter the Pod template or strategy")
			count, err = recreateStatefulSets(t.Context(), p.kube, p.state, cluster, cluster.destination())
			require.NoError(t, err)
			require.Zero(t, count, "resume reuses recorded recreation provenance without another orphan/delete")
		})
	}
}

func TestRenderMarkerInvalidationKeepsHandoffAdmissionBoundaries(t *testing.T) {
	destination := primitives.TransferIdentity{APIVersion: testDestAPI, Kind: clusterKind,
		Namespace: "openbao", Name: "bao", UID: testDestUID}
	saved := statefulSetFixture(destination, string(testSourceUID))
	saved.Annotations[constants.AnnotationClusterGeneration] = "1"
	created, err := recreatedStatefulSet(saved, destination)
	require.NoError(t, err)
	policy := openedLockPolicy(t)
	kind := metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: statefulSetKind}
	require.True(t, evaluateHandoff(t, policy, handoffCase{operation: "CREATE", kind: kind, new: created}))
	before := saved.DeepCopy()
	before.OwnerReferences[0].APIVersion, before.OwnerReferences[0].UID = testSourceAPI, testSourceUID
	before.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(testSourceUID)
	after := saved.DeepCopy()
	delete(after.Annotations, constants.AnnotationClusterGeneration)
	require.False(t, evaluateHandoff(t, policy, handoffCase{operation: "UPDATE", kind: kind, old: before, new: after}),
		"ownership-transfer UPDATE must still reject renderer-marker changes")
}

func TestRenderBarrierUsesPinnedBlueGreenVoter(t *testing.T) {
	p, base := verificationFixture(t, "tenant")
	cluster := &p.plan.Clusters[0]
	active := &appsv1.StatefulSet{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, active))
	require.NoError(t, base.Delete(t.Context(), active))
	active.Name, active.ResourceVersion = "bao-blue-revision", ""
	cluster.VoterStatefulSet = active.Name
	require.NoError(t, base.Create(t.Context(), active))
	observation, settled, err := p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, *cluster)
	require.NoError(t, err)
	require.True(t, settled, "the active revision can have a suffix independent of Cluster name")
	require.Equal(t, active.Name, observation.Sets[0].Name)
}

func TestWorkloadFenceIgnoresMetadataRefreshButTracksPodReadiness(t *testing.T) {
	p, base := verificationFixture(t, "tenant")
	cluster := p.plan.Clusters[0]
	before, settled, err := p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, cluster)
	require.NoError(t, err)
	require.True(t, settled)
	set := &appsv1.StatefulSet{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, set))
	set.Annotations["example.org/refresh"] = "observed"
	require.NoError(t, base.Update(t.Context(), set))
	after, settled, err := p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, cluster)
	require.NoError(t, err)
	require.True(t, settled)
	require.True(t, reflect.DeepEqual(before, after), "resource-version-only churn must not repeat slow checks")
	pod := &corev1.Pod{}
	require.NoError(t, base.Get(t.Context(), client.ObjectKey{Namespace: cluster.Namespace, Name: "bao-0"}, pod))
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	require.NoError(t, base.Status().Update(t.Context(), pod))
	_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, cluster)
	require.NoError(t, err)
	require.False(t, settled, "stale StatefulSet readiness cannot hide an unavailable data Pod")
	cluster.Paused, cluster.ReadyPods = true, []string{"bao-1", "bao-2"}
	_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, cluster)
	require.NoError(t, err)
	require.True(t, settled, "paused Clusters require only the Pods that were ready at plan")
	cluster.ReadyPods = append(cluster.ReadyPods, "bao-0")
	_, settled, err = p.observeVerifiedWorkload(t.Context(), primitives.PlanDestinationAPIVersion, cluster)
	require.NoError(t, err)
	require.False(t, settled)
}
