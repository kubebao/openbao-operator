package openbaocluster

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type scaleDownResumeClusterChange struct {
	name     string
	setup    func(*scaleDownResumeFixture)
	mutate   func(*openbaov1alpha1.OpenBaoCluster)
	metadata bool
	allowed  bool
}

func scaleDownResumeClusterChanges() []scaleDownResumeClusterChange {
	withLock := func(f *scaleDownResumeFixture) {
		acquired := metav1.NewTime(time.Unix(100, 0))
		f.cluster.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationUpgrade, Holder: "upgrade", AcquiredAt: &acquired,
		}
	}
	withBlueGreen := func(f *scaleDownResumeFixture) {
		f.cluster.Status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyBlueGreen
		f.cluster.Status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{
			Phase: openbaov1alpha1.PhaseIdle, OperationID: "operation-1", BlueReplicas: f.cluster.Spec.Replicas,
		}
	}
	return []scaleDownResumeClusterChange{
		{name: "status observations", allowed: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.ReadyReplicas++ }},
		{name: "current version observation", allowed: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.CurrentVersion = c.Spec.Version }},
		{name: "lock renewal", setup: withLock, allowed: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.OperationLock.Message = "progress"
			renewed := metav1.Now()
			c.Status.OperationLock.RenewedAt = &renewed
		}},
		{name: "initialized reset", mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.Initialized = false }},
		{name: "active voter revision", setup: withBlueGreen, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.BlueGreen.BlueRevision = "other" }},
		{name: "blue-green phase", setup: withBlueGreen, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing }},
		{name: "blue-green attempt", setup: withBlueGreen, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.BlueGreen.OperationID = "operation-2" }},
		{name: "legacy voter revision", setup: withBlueGreen, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.BlueGreen.BlueControllerRevision = "replacement-controller"
		}},
		{name: "reader drain cancelled", setup: func(f *scaleDownResumeFixture) {
			withBlueGreen(f)
			f.cluster.Status.BlueGreen.Phase = openbaov1alpha1.PhaseSyncing
			if f.readReplica {
				f.cluster.Spec.ReadReplicas.Replicas = 1
			}
		}, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.BlueGreen.Phase = openbaov1alpha1.PhaseRestoringReadReplicas
		}},
		{name: "lock holder", setup: withLock, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.OperationLock.Holder = "other-controller" }},
		{name: "lock acquisition", setup: withLock, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			acquired := metav1.NewTime(time.Unix(200, 0))
			c.Status.OperationLock.AcquiredAt = &acquired
		}},
		{name: "restore lock", mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.OperationLock = &openbaov1alpha1.OperationLockStatus{Operation: openbaov1alpha1.ClusterOperationRestore, Holder: "restore"}
		}},
		{name: "rolling attempt", mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.Upgrade = &openbaov1alpha1.UpgradeProgress{FromVersion: "2.5.5", TargetVersion: c.Spec.Version, CurrentPartition: 3}
		}},
		{name: "rolling execution ID", setup: func(f *scaleDownResumeFixture) {
			started := metav1.NewTime(time.Unix(100, 0))
			f.cluster.Status.Upgrade = &openbaov1alpha1.UpgradeProgress{
				ExecutionID: "attempt-1", FromVersion: "2.5.5", TargetVersion: f.cluster.Spec.Version,
				CurrentPartition: 3, StartedAt: &started,
			}
		}, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.Upgrade.ExecutionID = "attempt-2"
		}},
		{name: "break glass", mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Status.BreakGlass = &openbaov1alpha1.BreakGlassStatus{Active: true}
		}},
		{name: "restore hold", metadata: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Annotations = map[string]string{constants.AnnotationRestoreHold: "held"}
		}},
		{name: "restore origin", metadata: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) {
			c.Annotations = map[string]string{constants.AnnotationRestoreOrigin: "restore"}
		}},
		{name: "spec retarget", metadata: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.Replicas++; c.Generation++ }},
		{name: "pause", metadata: true, mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.Paused = true; c.Generation++ }},
		{name: "downgrade", mutate: func(c *openbaov1alpha1.OpenBaoCluster) { c.Status.CurrentVersion = "2.7.0" }},
	}
}

func TestScaleDownResumeClusterDecision(t *testing.T) {
	for _, readReplica := range []bool{false, true} {
		for _, during := range []bool{false, true} {
			for _, change := range scaleDownResumeClusterChanges() {
				name := change.name + "/before"
				if during {
					name = change.name + "/during"
				}
				if readReplica {
					name += "/reader"
				}
				t.Run(name, func(t *testing.T) {
					readers := int32(-1)
					if readReplica {
						readers = 1
					}
					f := newScaleDownResumeFixture(t, readReplica, readers)
					if change.setup != nil {
						change.setup(f)
					}
					c := f.client(t, interceptor.Funcs{})
					update := func() {
						current := &openbaov1alpha1.OpenBaoCluster{}
						require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(f.cluster), current))
						change.mutate(current)
						if change.metadata {
							require.NoError(t, c.Update(t.Context(), current))
						} else {
							require.NoError(t, c.Status().Update(t.Context(), current))
						}
					}
					if during {
						f.membership.read = func(call int) (*portopenbao.RaftConfigurationResponse, error) {
							if call == 2 {
								update()
								change.mutate(f.cluster) // Test that the first live read remains an independent checkpoint.
							}
							return f.membership.config, nil
						}
					} else {
						update()
					}
					r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: c, APIReader: c}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
					resumed, err := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, readReplica)
					if change.allowed {
						require.NoError(t, err)
						require.True(t, resumed)
					} else {
						require.Error(t, err)
						require.False(t, resumed)
					}
					stored := &appsv1.StatefulSet{}
					require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(f.sts), stored))
					want := *f.sts.Spec.Replicas
					if change.allowed {
						want--
					}
					require.Equal(t, want, *stored.Spec.Replicas)
					require.Equal(t, f.sts.Spec.Template, stored.Spec.Template)
				})
			}
		}
	}
}
