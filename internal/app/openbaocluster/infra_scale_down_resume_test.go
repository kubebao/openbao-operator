package openbaocluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type scaleDownMembershipStub struct {
	config      *portopenbao.RaftConfigurationResponse
	health      *portopenbao.RaftAutopilotStateResponse
	read        func(int) (*portopenbao.RaftConfigurationResponse, error)
	readHealth  func(int) (*portopenbao.RaftAutopilotStateResponse, error)
	healthCalls int
	calls       int
	err         error
}

func (s *scaleDownMembershipStub) ReadRaftConfiguration(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftConfigurationResponse, error) {
	s.calls++
	if s.read != nil {
		return s.read(s.calls)
	}
	return s.config, s.err
}

func (s *scaleDownMembershipStub) ReadRaftAutopilotState(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster) (*portopenbao.RaftAutopilotStateResponse, error) {
	s.healthCalls++
	if s.readHealth != nil {
		return s.readHealth(s.healthCalls)
	}
	return s.health, s.err
}

type scaleDownResumeFixture struct {
	cluster     *openbaov1alpha1.OpenBaoCluster
	sts         *appsv1.StatefulSet
	objects     []client.Object
	membership  *scaleDownMembershipStub
	desired     int32
	readReplica bool
}

func newScaleDownResumeFixture(t *testing.T, readReplica bool, readers int32) *scaleDownResumeFixture {
	t.Helper()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "resume", Namespace: "default", UID: "cluster-uid", ResourceVersion: "10"},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 1, Image: "openbao:new", Version: "2.6.4"},
		Status:     openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
	}
	if readers >= 0 {
		cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{Replicas: readers}
	}
	f := &scaleDownResumeFixture{cluster: cluster, desired: 1, readReplica: readReplica,
		membership: &scaleDownMembershipStub{
			config: &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{Index: 24}},
			health: &portopenbao.RaftAutopilotStateResponse{Servers: map[string]portopenbao.RaftAutopilotServerState{}},
		},
	}
	f.objects = append(f.objects, cluster)
	voters := f.addWorkload(t, cluster.Name, 3, true, !readReplica)
	f.sts = voters
	if readReplica {
		cluster.Spec.Replicas = 3
		f.desired = 0
		f.sts = f.addWorkload(t, resourceidentity.ReadReplicaStatefulSetName(cluster), readers, false, true)
		cluster.Spec.ReadReplicas.Replicas = 0
	} else if readers >= 0 {
		f.addWorkload(t, resourceidentity.ReadReplicaStatefulSetName(cluster), readers, false, false)
	}
	return f
}

func (f *scaleDownResumeFixture) addWorkload(t *testing.T, name string, replicas int32, voter, removing bool) *appsv1.StatefulSet {
	t.Helper()
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.cluster.Namespace, UID: types.UID(name + "-uid"),
			ResourceVersion: "20", Generation: 2,
			Annotations:     map[string]string{constants.AnnotationOpenBaoOwnerUID: string(f.cluster.UID)},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(f.cluster, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(replicas), ServiceName: f.cluster.Name,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "openbao", Image: "openbao:old"}}}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: replicas, CurrentReplicas: replicas,
			ReadyReplicas: replicas, CurrentRevision: "revision", UpdateRevision: "revision"},
	}
	f.objects = append(f.objects, sts)
	retained := replicas
	if removing {
		sts.Status.ReadyReplicas--
		retained--
	}
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		podName := fmt.Sprintf("%s-%d", name, ordinal)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: f.cluster.Namespace,
			Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: sts.Status.CurrentRevision},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}
		if ordinal >= retained {
			pod.Status.Conditions[0].Status = corev1.ConditionFalse
		} else {
			address := fmt.Sprintf("%s.%s.%s.svc:8201", podName, f.cluster.Name, f.cluster.Namespace)
			leader := voter && ordinal == 0
			f.membership.config.Config.Servers = append(f.membership.config.Config.Servers,
				portopenbao.RaftServer{NodeID: podName, Address: address, Voter: voter, Leader: leader})
			f.membership.health.Servers[podName] = portopenbao.RaftAutopilotServerState{ID: podName, Address: address, Healthy: true}
			if leader {
				f.membership.health.Leader = podName
			}
		}
		f.objects = append(f.objects, pod)
	}
	return sts
}

func (f *scaleDownResumeFixture) client(t *testing.T, hooks interceptor.Funcs) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.objects...).
		WithStatusSubresource(&appsv1.StatefulSet{}, &corev1.Pod{}, &openbaov1alpha1.OpenBaoCluster{}).
		WithInterceptorFuncs(hooks).Build()
}

func TestResumeRemovedPeerScaleDown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		readReplica bool
		readers     int32
	}{
		{"voter without read pool", false, -1},
		{"voter with read pool", false, 1},
		{"voter with empty read pool", false, 0},
		{"read replica to one", true, 2},
		{"read replica to zero", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScaleDownResumeFixture(t, tc.readReplica, tc.readers)
			patches := 0
			c := f.client(t, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				payload, err := patch.Data(obj)
				require.NoError(t, err)
				var actual map[string]interface{}
				require.NoError(t, json.Unmarshal(payload, &actual))
				require.Equal(t, map[string]interface{}{
					"metadata": map[string]interface{}{"uid": string(f.sts.UID), "resourceVersion": f.sts.ResourceVersion},
					"spec":     map[string]interface{}{"replicas": float64(*f.sts.Spec.Replicas - 1)},
				}, actual, "recovery must not apply a new image, config, or template")
				return c.Patch(ctx, obj, patch, opts...)
			}})
			r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: c, APIReader: c}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
			resumed, err := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, tc.readReplica)
			require.NoError(t, err)
			require.True(t, resumed)
			require.Equal(t, 1, patches)
			updated := &appsv1.StatefulSet{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(f.sts), updated))
			require.Equal(t, *f.sts.Spec.Replicas-1, *updated.Spec.Replicas)
			require.Equal(t, f.sts.Spec.Template, updated.Spec.Template)
			require.Equal(t, 2, f.membership.calls)
		})
	}
}

func TestScaleDownResumeRejectsUncertainState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*scaleDownResumeFixture)
	}{
		{"membership unavailable", func(f *scaleDownResumeFixture) { f.membership.err = errors.New("offline") }},
		{"membership absent", func(f *scaleDownResumeFixture) { f.membership.config = nil }},
		{"empty membership", func(f *scaleDownResumeFixture) { f.membership.config.Config.Servers = nil }},
		{"foreign workload", func(f *scaleDownResumeFixture) { f.sts.OwnerReferences[0].UID = "foreign" }},
		{"missing provenance", func(f *scaleDownResumeFixture) { f.sts.Annotations = nil }},
		{"unobserved workload", func(f *scaleDownResumeFixture) { f.sts.Status.ObservedGeneration-- }},
		{"mixed workload revisions", func(f *scaleDownResumeFixture) { f.sts.Status.UpdateRevision = "new" }},
		{"retained Pod unready", func(f *scaleDownResumeFixture) {
			f.objects[2].(*corev1.Pod).Status.Conditions[0].Status = corev1.ConditionFalse
		}},
		{"retained Pod foreign", func(f *scaleDownResumeFixture) { f.objects[2].(*corev1.Pod).OwnerReferences[0].UID = "foreign" }},
		{"retained Pod stale", func(f *scaleDownResumeFixture) {
			f.objects[2].(*corev1.Pod).Labels[appsv1.StatefulSetRevisionLabel] = "old"
		}},
		{"victim still configured", func(f *scaleDownResumeFixture) {
			f.membership.config.Config.Servers = append(f.membership.config.Config.Servers, portopenbao.RaftServer{NodeID: "resume-2", Address: "resume-2.resume.default.svc:8201", Voter: true})
		}},
		{"unexpected member", func(f *scaleDownResumeFixture) {
			f.membership.config.Config.Servers[1].Address = "other.resume.default.svc:8201"
		}},
		{"foreign namespace", func(f *scaleDownResumeFixture) {
			f.membership.config.Config.Servers[1].Address = "resume-1.resume.foreign.svc:8201"
		}},
		{"short hostname", func(f *scaleDownResumeFixture) {
			f.membership.config.Config.Servers[1].Address = "resume-1:8201"
			health := f.membership.health.Servers["resume-1"]
			health.Address = "resume-1:8201"
			f.membership.health.Servers["resume-1"] = health
		}},
		{"wrong role", func(f *scaleDownResumeFixture) { f.membership.config.Config.Servers[1].Voter = false }},
		{"duplicate ID", func(f *scaleDownResumeFixture) { f.membership.config.Config.Servers[1].NodeID = "resume-0" }},
		{"duplicate address", func(f *scaleDownResumeFixture) {
			f.membership.config.Config.Servers[1].Address = f.membership.config.Config.Servers[0].Address
		}},
		{"missing retained member", func(f *scaleDownResumeFixture) {
			f.membership.config.Config.Servers = f.membership.config.Config.Servers[:1]
		}},
		{"missing leader", func(f *scaleDownResumeFixture) { f.membership.config.Config.Servers[0].Leader = false }},
		{"conflicting leader", func(f *scaleDownResumeFixture) { f.membership.health.Leader = "resume-1" }},
		{"missing native health", func(f *scaleDownResumeFixture) { delete(f.membership.health.Servers, "resume-1") }},
		{"unhealthy retained member", func(f *scaleDownResumeFixture) {
			h := f.membership.health.Servers["resume-1"]
			h.Healthy = false
			f.membership.health.Servers["resume-1"] = h
		}},
		{"health address mismatch", func(f *scaleDownResumeFixture) {
			h := f.membership.health.Servers["resume-1"]
			h.Address = "other.resume.default.svc:8201"
			f.membership.health.Servers["resume-1"] = h
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScaleDownResumeFixture(t, false, -1)
			tc.mutate(f)
			c := f.client(t, interceptor.Funcs{})
			r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: c, APIReader: c}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
			resumed, err := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, false)
			require.Error(t, err)
			require.False(t, resumed)
			actual := &appsv1.StatefulSet{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(f.sts), actual))
			require.Equal(t, *f.sts.Spec.Replicas, *actual.Spec.Replicas)
		})
	}
}

func TestScaleDownResumeMembershipChangesBeforePatch(t *testing.T) {
	for _, change := range []string{"configuration index", "victim reappears", "cluster retarget", "workload conflict"} {
		t.Run(change, func(t *testing.T) {
			f := newScaleDownResumeFixture(t, false, -1)
			patches := 0
			c := f.client(t, interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				patches++
				return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, f.sts.Name, errors.New("changed"))
			}})
			f.membership.read = func(call int) (*portopenbao.RaftConfigurationResponse, error) {
				if call == 2 {
					copyConfig := *f.membership.config
					switch change {
					case "configuration index":
						copyConfig.Config.Index++
						return &copyConfig, nil
					case "victim reappears":
						copyConfig.Config.Servers = append(copyConfig.Config.Servers, portopenbao.RaftServer{NodeID: "resume-2", Address: "resume-2.resume.default.svc:8201", Voter: true})
						return &copyConfig, nil
					case "cluster retarget":
						current := &openbaov1alpha1.OpenBaoCluster{}
						require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(f.cluster), current))
						current.Spec.Replicas = 3
						require.NoError(t, c.Update(t.Context(), current))
					}
				}
				return f.membership.config, nil
			}
			r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: c, APIReader: c}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
			resumed, err := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, false)
			require.Error(t, err)
			require.False(t, resumed)
			if change == "workload conflict" {
				require.Equal(t, 1, patches)
				require.True(t, apierrors.IsConflict(err))
			} else {
				require.Zero(t, patches)
			}
		})
	}
}

func TestScaleDownResumeRaftHost(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{"resume-0.resume.default.svc:8201", "resume-0.resume.default.svc"},
		{"https://resume-0.resume.default.svc:8201", "resume-0.resume.default.svc"},
		{"https://RESUME-0.resume.default.svc.cluster.local.:8201", "resume-0.resume.default.svc"},
		{"resume-0:8200", ""}, {"https://user@resume-0:8201", ""}, {"https://resume-0:8201/path", ""}, {"https://resume-0:8201?x=y", ""},
	} {
		t.Run(tc.address, func(t *testing.T) {
			got, err := scaleDownResumeRaftHost(tc.address)
			if tc.want == "" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}
