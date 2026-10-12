package perf

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestAvailabilityProbeRetainsStatusAndTiming(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 429, 472, 473, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v1/namespaces/perf/services/https:cluster-public:8200/proxy/v1/sys/health", r.URL.Path)
				assert.Equal(t, "true", r.URL.Query().Get("standbyok"))
				w.WriteHeader(status)
				_, _ = w.Write([]byte(strings.Repeat("x", 8192)))
			}))
			defer server.Close()
			native := &nativeScenarioContext{cfg: &rest.Config{Host: server.URL}, namespace: "perf"}
			probe := native.probeClusterAvailability(t.Context(), "cluster")
			require.Equal(t, status, probe.HTTPStatus)
			require.Equal(t, status != 503, probe.Available)
			require.Empty(t, probe.Error)
			require.False(t, probe.At.IsZero())
			require.Greater(t, probe.DurationSeconds, 0.0)
			if status == 503 {
				require.Len(t, probe.ResponseBody, 4096)
			} else {
				require.Empty(t, probe.ResponseBody)
			}
		})
	}
}

func TestAvailabilityProbeRetainsTransportError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	native := &nativeScenarioContext{cfg: &rest.Config{Host: server.URL}, namespace: "perf"}
	probe := native.probeClusterAvailability(t.Context(), "cluster")
	require.False(t, probe.Available)
	require.Zero(t, probe.HTTPStatus)
	require.NotEmpty(t, probe.Error)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	probe = native.probeClusterAvailability(ctx, "cluster")
	require.False(t, probe.Available)
	require.Contains(t, probe.Error, "context canceled")
}

func TestFailedAvailabilityProbeCapturesRoutingStateAndWritesArtifact(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, discoveryv1.AddToScheme(scheme))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-0", Namespace: "perf", UID: "pod-uid",
			Labels: map[string]string{constants.LabelOpenBaoCluster: "cluster"}},
		Status: corev1.PodStatus{PodIP: "10.0.0.1", Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()},
		}},
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-public-abc", Namespace: "perf",
			Labels: map[string]string{discoveryv1.LabelServiceName: "cluster-public"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}}},
	}
	native := &nativeScenarioContext{
		namespace: "perf", runID: "sample-id", scenario: Scenario{Name: "rolling-upgrade"},
		opts:   Config{ArtifactDir: t.TempDir()},
		client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, slice).Build(),
	}
	probe := availabilityProbe{At: time.Now().UTC(), HTTPStatus: 503}
	native.observeFailedAvailabilityProbe(t.Context(), "cluster", &probe)
	require.Empty(t, probe.ObservationErrors)
	require.Len(t, probe.Pods, 1)
	require.Equal(t, pod.UID, probe.Pods[0].UID)
	require.True(t, probe.Pods[0].Ready)
	require.Len(t, probe.EndpointSlices, 1)
	require.Equal(t, []string{"10.0.0.1"}, probe.EndpointSlices[0].Endpoints[0].Addresses)

	f, err := native.createAvailabilityProbeLog()
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(f).Encode(probe))
	require.NoError(t, f.Close())
	result := native.result(nil, nil)
	firstPath := result.Artifacts["availabilityProbes"]
	data, err := os.ReadFile(firstPath)
	require.NoError(t, err)
	var stored availabilityProbe
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Equal(t, probe.HTTPStatus, stored.HTTPStatus)
	require.Equal(t, probe.Pods, stored.Pods)
	next, err := native.createAvailabilityProbeLog()
	require.NoError(t, err)
	require.NoError(t, next.Close())
	require.NotEqual(t, firstPath, native.artifacts["availabilityProbes"], "a reused run ID must not overwrite a sample")
	preserved, err := os.ReadFile(firstPath)
	require.NoError(t, err)
	require.Equal(t, data, preserved)

	native.client = fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("API observation unavailable")
		},
	}).Build()
	probe.Error = "original probe error"
	native.observeFailedAvailabilityProbe(t.Context(), "cluster", &probe)
	require.Equal(t, "original probe error", probe.Error)
	require.Len(t, probe.ObservationErrors, 2)
	require.Equal(t, 503, probe.HTTPStatus)
	require.True(t, strings.HasSuffix(result.Artifacts["availabilityProbes"], ".jsonl"))
}
