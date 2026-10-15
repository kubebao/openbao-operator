//go:build e2e

package e2e

import (
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestE2EFixtureGatewaySelectorRequiresObservedPhase(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	for _, test := range []struct {
		name             string
		phase, target    api.BlueGreenPhase
		selector         string
		complete         bool
		wantFailure      bool
		wantMissingPhase bool
	}{
		{name: "demotion observes Blue", phase: api.PhaseDemotingBlue, target: api.PhaseDemotingBlue, selector: "blue"},
		{name: "cleanup observes Green", phase: api.PhaseCleanup, target: api.PhaseCleanup, selector: "green"},
		{name: "demotion rejects Green", phase: api.PhaseDemotingBlue, target: api.PhaseDemotingBlue, selector: "green", wantFailure: true},
		{name: "cleanup rejects Blue", phase: api.PhaseCleanup, target: api.PhaseCleanup, selector: "blue", wantFailure: true},
		{name: "earlier phase retries", phase: api.PhasePromoting, target: api.PhaseDemotingBlue, selector: "blue", wantFailure: true},
		{name: "cleanup waits for its own phase", phase: api.PhaseDemotingBlue, target: api.PhaseCleanup, selector: "green", wantFailure: true},
		{name: "initial Idle retries", phase: api.PhaseIdle, target: api.PhaseDemotingBlue, selector: "blue", wantFailure: true},
		{name: "skipped demotion fails", phase: api.PhaseCleanup, target: api.PhaseDemotingBlue, selector: "blue", wantMissingPhase: true},
		{name: "skipped cleanup fails", phase: api.PhaseRestoringReadReplicas, target: api.PhaseCleanup, selector: "green", wantMissingPhase: true},
		{name: "completed upgrade cannot satisfy cleanup", phase: api.PhaseIdle, target: api.PhaseCleanup, selector: "green", complete: true, wantMissingPhase: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cluster := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "tenant"},
				Spec: api.OpenBaoClusterSpec{Version: "2.7.1"},
				Status: api.OpenBaoClusterStatus{CurrentVersion: "2.6.4", BlueGreen: &api.BlueGreenStatus{
					Phase: test.phase, BlueRevision: "blue", GreenRevision: "green",
				}}}
			if test.complete {
				cluster.Status.CurrentVersion = cluster.Spec.Version
			}
			service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "gateway-public", Namespace: "tenant"},
				Spec: corev1.ServiceSpec{Selector: map[string]string{constants.LabelOpenBaoRevision: test.selector}}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, service).Build()
			var failures []string
			g := NewGomega(func(message string, _ ...int) { failures = append(failures, message) })
			var stopped any
			func() {
				defer func() { stopped = recover() }()
				expectBlueGreenServiceSelectorDuringPhase(g, t.Context(), c, cluster.Namespace, cluster.Name, test.target,
					func(status *api.BlueGreenStatus) string {
						if test.target == api.PhaseDemotingBlue {
							return status.BlueRevision
						}
						return status.GreenRevision
					}, "Green revision is required")
			}()
			if test.wantMissingPhase {
				require.Implements(t, (*PollingSignalError)(nil), stopped)
				require.Contains(t, fmt.Sprint(stopped), "without observing")
				require.Contains(t, fmt.Sprint(stopped), string(test.target))
				return
			}
			require.Nil(t, stopped)
			require.Equal(t, test.wantFailure, len(failures) != 0, "phase and selector assertions: %v", failures)
		})
	}
}

func TestE2EFixtureGatewayUpgradeWaitsForEstablishedSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*api.OpenBaoCluster)
	}{
		{name: "established source"},
		{name: "initialized bootstrap without observed version", change: func(c *api.OpenBaoCluster) {
			c.Status.CurrentVersion, c.Status.ReadyReplicas, c.Status.BlueGreen, c.Status.Conditions = "", 1, nil, nil
		}},
		{name: "target recorded without an upgrade", change: func(c *api.OpenBaoCluster) { c.Status.CurrentVersion = "2.5.5" }},
		{name: "source still scaling", change: func(c *api.OpenBaoCluster) { c.Status.ReadyReplicas = 1 }},
		{name: "version observed before BlueGreen initialization", change: func(c *api.OpenBaoCluster) { c.Status.BlueGreen = nil }},
		{name: "upgrade already active", change: func(c *api.OpenBaoCluster) {
			c.Status.BlueGreen.Phase, c.Status.BlueGreen.GreenRevision = api.PhaseDeployingGreen, "green"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cluster := &api.OpenBaoCluster{
				Spec: api.OpenBaoClusterSpec{Version: "2.4.4", Replicas: 3},
				Status: api.OpenBaoClusterStatus{
					Initialized: true, CurrentVersion: "2.4.4", ReadyReplicas: 3,
					BlueGreen:  &api.BlueGreenStatus{Phase: api.PhaseIdle, BlueRevision: "blue"},
					Conditions: []metav1.Condition{{Type: string(api.ConditionAvailable), Status: metav1.ConditionTrue}},
				},
			}
			if test.change != nil {
				test.change(cluster)
			}
			var failure any
			func() {
				defer func() { failure = recover() }()
				g := NewGomega(func(message string, _ ...int) { panic(message) })
				expectGatewayUpgradeSourceReady(g, cluster, "2.4.4")
			}()
			if test.change == nil {
				require.Nil(t, failure)
			} else {
				require.IsType(t, "", failure, "an incomplete source must fail an assertion")
			}
		})
	}
}
