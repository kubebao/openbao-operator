//go:build integration

// Package managerprobe tests manager health registration with a real API server.
package managerprobe

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/internal/platform/admission"
)

// Environment starts an isolated API server using the repository's CRDs.
func Environment(t *testing.T) *rest.Config {
	t.Helper()
	ctrl.SetLogger(logr.Discard())
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	return config
}

// RecordingCache distinguishes readiness registration from real controller source registration.
type RecordingCache struct {
	cache.Cache
	started       atomic.Bool
	mu            sync.Mutex
	before, after map[string]struct{}
}

// NewRecordingCache wraps a real cache without changing its informer behavior.
func NewRecordingCache(config *rest.Config, options cache.Options) (*RecordingCache, error) {
	inner, err := cache.New(config, options)
	if err != nil {
		return nil, err
	}
	return &RecordingCache{Cache: inner, before: map[string]struct{}{}, after: map[string]struct{}{}}, nil
}

func (c *RecordingCache) GetInformer(ctx context.Context, object client.Object, options ...cache.InformerGetOption) (cache.Informer, error) {
	c.mu.Lock()
	if c.started.Load() {
		c.after[reflect.TypeOf(object).String()] = struct{}{}
	} else {
		c.before[reflect.TypeOf(object).String()] = struct{}{}
	}
	c.mu.Unlock()
	return c.Cache.GetInformer(ctx, object, options...)
}

func (c *RecordingCache) Start(ctx context.Context) error {
	c.started.Store(true)
	return c.Cache.Start(ctx)
}

// AssertMatchesWatches compares readiness's list with the actual source registrations after warmup.
func (c *RecordingCache) AssertMatchesWatches(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.after)
	require.Equal(t, c.after, c.before, "readiness registration must exactly match the production controller watches")
}

// ProbeAddress reserves a currently available local TCP address for a manager probe listener.
func ProbeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

// Start starts a manager and registers shutdown before returning.
func Start(t *testing.T, mgr ctrl.Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("manager did not stop")
		}
	})
}

// Status fetches the manager's HTTP probe with a bounded request.
func Status(address, path string) int {
	httpClient := &http.Client{Timeout: time.Second}
	response, err := httpClient.Get("http://" + address + path)
	if err != nil {
		return 0
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

// HoldLeadership prevents the tested manager from acquiring its lease.
func HoldLeadership(t *testing.T, kubeClient client.Client, name string) {
	t.Helper()
	holder := "another-process"
	duration := int32(3600)
	now := metav1.NowMicro()
	require.NoError(t, kubeClient.Create(t.Context(), &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, AcquireTime: &now, RenewTime: &now},
	}))
}

// AdmissionLifecycle checks initial absence, periodic recovery, and loss on an idle standby process.
func AdmissionLifecycle(t *testing.T, mgr ctrl.Manager, address string) {
	t.Helper()
	require.Eventually(t, func() bool { return Status(address, "/healthz") == http.StatusOK }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, http.StatusInternalServerError, Status(address, "/readyz"))
	kubeClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	for _, dependency := range admission.DefaultDependencies() {
		for _, fixture := range []struct {
			name   string
			object client.Object
		}{
			{dependency.PolicyName, &admissionv1.ValidatingAdmissionPolicy{}},
			{dependency.BindingName, &admissionv1.ValidatingAdmissionPolicyBinding{}},
		} {
			data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", fixture.name+".yaml"))
			require.NoError(t, err)
			require.NoError(t, yaml.UnmarshalStrict(data, fixture.object))
			require.NoError(t, kubeClient.Create(t.Context(), fixture.object))
		}
	}
	require.Eventually(t, func() bool { return Status(address, "/readyz") == http.StatusOK }, 30*time.Second, 100*time.Millisecond)
	select {
	case <-mgr.Elected():
		t.Fatal("standby readiness must not require leadership")
	default:
	}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: admission.DefaultDependencies()[0].BindingName}}
	require.NoError(t, kubeClient.Delete(t.Context(), binding))
	require.Eventually(t, func() bool { return Status(address, "/readyz") == http.StatusInternalServerError }, 30*time.Second, 100*time.Millisecond)
	require.Equal(t, http.StatusOK, Status(address, "/healthz"))
}

// ListGate withholds an initial LIST or streaming watch-list while probes and discovery run.
type ListGate struct {
	Resource    string
	Observed    chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

// NewListGate creates a cancellable gate for initial cache population.
func NewListGate(resource string) *ListGate {
	return &ListGate{Resource: resource, Observed: make(chan struct{}), release: make(chan struct{})}
}

// Release permits the held request to reach the API server.
func (g *ListGate) Release() { g.releaseOnce.Do(func() { close(g.release) }) }

// Wrap returns a transport wrapper suitable for rest.Config.WrapTransport.
func (g *ListGate) Wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		initialPopulation := request.URL.Query().Get("watch") != "true" ||
			request.URL.Query().Get("sendInitialEvents") == "true"
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/"+g.Resource) && initialPopulation {
			g.once.Do(func() { close(g.Observed) })
			select {
			case <-g.release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return next.RoundTrip(request)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// WarmupObserver records completion of actual registered controllers' warmup callbacks.
type WarmupObserver struct {
	ctrl.Manager
	completed []<-chan struct{}
}

func (m *WarmupObserver) Add(runnable manager.Runnable) error {
	warmup, ok := runnable.(interface {
		Warmup(context.Context) error
		NeedLeaderElection() bool
	})
	if !ok {
		return m.Manager.Add(runnable)
	}
	done := make(chan struct{})
	m.completed = append(m.completed, done)
	return m.Manager.Add(&observedWarmup{Runnable: runnable, warmup: warmup.Warmup, needLeader: warmup.NeedLeaderElection, done: done})
}

// Wait waits for every registered warmup to complete, not merely for manager.Elected.
func (m *WarmupObserver) Wait(t *testing.T) {
	t.Helper()
	require.NotEmpty(t, m.completed)
	for _, done := range m.completed {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("registered controller warmup did not complete")
		}
	}
}

type observedWarmup struct {
	manager.Runnable
	warmup     func(context.Context) error
	needLeader func() bool
	done       chan struct{}
}

func (w *observedWarmup) NeedLeaderElection() bool { return w.needLeader() }
func (w *observedWarmup) Warmup(ctx context.Context) error {
	err := w.warmup(ctx)
	close(w.done)
	return err
}
