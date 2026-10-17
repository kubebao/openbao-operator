package openbaocluster

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	nativeResumeHealthAlias     = "port mutates second health"
	nativeResumeReplacementID   = "replacement-native-id"
	nativeResumeIndexSuffix     = "/index=nonzero"
	nativeResumeAlias           = "port mutates first response"
	nativeResumeZero            = "matching zero index"
	nativeResumeNormalized      = "normalized reordered configuration"
	nativeResumeIndexChanged    = "nonzero index changed"
	nativeResumeIndexLost       = "nonzero index unavailable"
	nativeResumeIndexAppeared   = "zero index becomes nonzero"
	nativeResumeIdentityChanged = "zero index member identity changed"
	nativeResumeAddressChanged  = "zero index member address changed"
	nativeResumeRoleChanged     = "zero index member role changed"
	nativeResumeLeaderChanged   = "zero index leader changed"
	nativeResumeHealthError     = "second health observation fails"
	nativeResumeUnhealthy       = "second health observation unhealthy"
	nativeResumeTwoToOne        = "voter two to one requires manual recovery"
	nativeResumeOneVoterReader  = "reader with one retained voter"
)

func scaleDownResumeNativeCases(readReplica bool) []string {
	cases := []string{nativeResumeZero, nativeResumeNormalized, nativeResumeIndexChanged, nativeResumeIndexLost,
		nativeResumeIndexAppeared, nativeResumeIdentityChanged, nativeResumeAddressChanged, nativeResumeRoleChanged,
		nativeResumeLeaderChanged, nativeResumeHealthError, nativeResumeUnhealthy, nativeResumeAlias, nativeResumeHealthAlias}
	for _, change := range []string{nativeResumeZero, nativeResumeNormalized, nativeResumeIdentityChanged, nativeResumeAddressChanged, nativeResumeRoleChanged, nativeResumeLeaderChanged} {
		cases = append(cases, change+nativeResumeIndexSuffix)
	}
	if readReplica {
		return append(cases, nativeResumeOneVoterReader)
	}
	return append(cases, nativeResumeTwoToOne)
}

func scaleDownResumeNativeAllowed(change string) bool {
	change = strings.TrimSuffix(change, nativeResumeIndexSuffix)
	return change == nativeResumeZero || change == nativeResumeNormalized || change == nativeResumeOneVoterReader
}

func newScaleDownResumeNativeFixture(t *testing.T, readReplica bool, change string) *scaleDownResumeFixture {
	t.Helper()
	indexPresent := strings.HasSuffix(change, nativeResumeIndexSuffix)
	change = strings.TrimSuffix(change, nativeResumeIndexSuffix)
	f := newScaleDownResumeFixture(t, readReplica, 2)
	if change == nativeResumeTwoToOne || change == nativeResumeOneVoterReader {
		resizeScaleDownResumeNativeVoters(t, f, readReplica)
	}
	f.membership.config.Config.Index = 0
	if indexPresent || change == nativeResumeAlias || change == nativeResumeHealthAlias || change == nativeResumeIndexChanged || change == nativeResumeIndexLost || change == nativeResumeHealthError || change == nativeResumeUnhealthy || change == nativeResumeTwoToOne {
		f.membership.config.Config.Index = 24
	}
	setScaleDownResumeNativeConfigurationReads(t, f, change)
	setScaleDownResumeNativeHealthReads(t, f, change)
	return f
}

func setScaleDownResumeNativeConfigurationReads(t *testing.T, f *scaleDownResumeFixture, change string) {
	t.Helper()
	f.membership.read = func(call int) (*portopenbao.RaftConfigurationResponse, error) {
		if call == 2 && change == nativeResumeHealthAlias {
			id := f.membership.config.Config.Servers[0].NodeID
			member := f.membership.health.Servers[id]
			member.Healthy = true
			f.membership.health.Servers[id] = member
		}
		if change == nativeResumeAlias {
			return f.membership.config, nil
		}
		copyConfig := *f.membership.config
		copyConfig.Config.Servers = slices.Clone(copyConfig.Config.Servers)
		if call == 2 {
			switch change {
			case nativeResumeNormalized:
				slices.Reverse(copyConfig.Config.Servers)
				for i := range copyConfig.Config.Servers {
					copyConfig.Config.Servers[i].Address = "https://" + copyConfig.Config.Servers[i].Address
				}
			case nativeResumeIndexChanged, nativeResumeIndexAppeared:
				copyConfig.Config.Index++
			case nativeResumeIndexLost:
				copyConfig.Config.Index = 0
			case nativeResumeIdentityChanged:
				copyConfig.Config.Servers[1].NodeID = nativeResumeReplacementID
			case nativeResumeAddressChanged:
				copyConfig.Config.Servers[1].Address = copyConfig.Config.Servers[0].Address
			case nativeResumeRoleChanged:
				copyConfig.Config.Servers[1].Voter = !copyConfig.Config.Servers[1].Voter
			case nativeResumeLeaderChanged:
				copyConfig.Config.Servers[0].Leader = false
				copyConfig.Config.Servers[1].Leader = true
			}
		}
		return &copyConfig, nil
	}
}

func setScaleDownResumeNativeHealthReads(t *testing.T, f *scaleDownResumeFixture, change string) {
	t.Helper()
	f.membership.readHealth = func(call int) (*portopenbao.RaftAutopilotStateResponse, error) {
		if call == 2 && change == nativeResumeHealthAlias {
			id := f.membership.config.Config.Servers[0].NodeID
			member := f.membership.health.Servers[id]
			member.Healthy = false
			f.membership.health.Servers[id] = member
			return f.membership.health, nil
		}
		health := *f.membership.health
		health.Servers = maps.Clone(health.Servers)
		if change == nativeResumeAlias {
			oldID := f.membership.config.Config.Servers[1].NodeID
			if call == 1 {
				member := health.Servers[oldID]
				member.ID = nativeResumeReplacementID
				health.Servers[member.ID] = member
				f.membership.health.Servers[member.ID] = member
				f.membership.config.Config.Servers[1].NodeID = member.ID
			}
		}
		if call == 2 {
			switch change {
			case nativeResumeIdentityChanged:
				oldID := f.membership.config.Config.Servers[1].NodeID
				member := health.Servers[oldID]
				delete(health.Servers, oldID)
				member.ID = nativeResumeReplacementID
				health.Servers[member.ID] = member
			case nativeResumeLeaderChanged:
				health.Leader = f.membership.config.Config.Servers[1].NodeID
			case nativeResumeHealthError:
				return nil, errors.New("second authenticated health read unavailable")
			case nativeResumeUnhealthy:
				id := f.membership.config.Config.Servers[0].NodeID
				member := health.Servers[id]
				member.Healthy = false
				health.Servers[id] = member
			}
		}
		return &health, nil
	}
}

func resizeScaleDownResumeNativeVoters(t *testing.T, f *scaleDownResumeFixture, readReplica bool) {
	t.Helper()
	population := int32(2)
	if readReplica {
		population = 1
		f.cluster.Spec.Replicas = 1
	}
	keep := map[string]bool{}
	retained := population
	if !readReplica {
		retained--
	}
	for ordinal := int32(0); ordinal < population; ordinal++ {
		keep[fmt.Sprintf("%s-%d", f.cluster.Name, ordinal)] = true
	}
	f.objects = slices.DeleteFunc(f.objects, func(obj client.Object) bool {
		if sts, ok := obj.(*appsv1.StatefulSet); ok && sts.Name == f.cluster.Name {
			sts.Spec.Replicas = &population
			sts.Status.Replicas, sts.Status.CurrentReplicas, sts.Status.ReadyReplicas = population, population, retained
		}
		if pod, ok := obj.(*corev1.Pod); ok && pod.OwnerReferences[0].Name == f.cluster.Name {
			return !keep[pod.Name]
		}
		return false
	})
	if !readReplica {
		scaleDownResumeVictim(t, f).Status.Conditions[0].Status = corev1.ConditionFalse
		delete(keep, fmt.Sprintf("%s-%d", f.cluster.Name, population-1))
	}
	f.membership.config.Config.Servers = slices.DeleteFunc(f.membership.config.Config.Servers, func(server portopenbao.RaftServer) bool {
		if server.Voter && !keep[server.NodeID] {
			delete(f.membership.health.Servers, server.NodeID)
			return true
		}
		return false
	})
}

func TestScaleDownResumeNativeObservations(t *testing.T) {
	for _, readReplica := range []bool{false, true} {
		for _, terminating := range []bool{false, true} {
			for _, change := range scaleDownResumeNativeCases(readReplica) {
				t.Run(fmt.Sprintf("%s/reader=%t/terminating=%t", change, readReplica, terminating), func(t *testing.T) {
					f := newScaleDownResumeNativeFixture(t, readReplica, change)
					if terminating {
						victim := scaleDownResumeVictim(t, f)
						victim.UID = "native-victim-uid"
						victim.Finalizers = []string{"test.openbao.org/native-hold"}
						now := metav1.Now()
						victim.DeletionTimestamp = &now
						f.sts.Status.CurrentReplicas--
					}
					c := f.client(t, interceptor.Funcs{})
					r := &infraReconciler{deps: InfraDependencies{Kubernetes: InfraKubernetesRuntime{Client: c, APIReader: c}, ScaleDown: InfraScaleDownRuntime{Membership: f.membership}}}
					resumed, err := r.resumeRemovedPeerScaleDown(t.Context(), logr.Discard(), f.cluster, f.sts, f.desired, readReplica)
					assertScaleDownResumeNativeResult(t, c, f, change, resumed, err)
				})
			}
		}
	}
}

func assertScaleDownResumeNativeResult(t *testing.T, c client.Client, f *scaleDownResumeFixture, change string, resumed bool, err error) {
	t.Helper()
	want := *f.sts.Spec.Replicas
	if scaleDownResumeNativeAllowed(change) {
		require.NoError(t, err)
		require.True(t, resumed)
		require.Equal(t, 2, f.membership.calls)
		require.Equal(t, 2, f.membership.healthCalls)
		want--
	} else {
		require.Error(t, err)
		require.False(t, resumed)
	}
	stored := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(f.sts), stored))
	require.Equal(t, want, *stored.Spec.Replicas)
	require.Equal(t, f.sts.Spec.Template, stored.Spec.Template)
}
