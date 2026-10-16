package openbao

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

func TestClientManager_FactoryFor_ReturnsSameStateForSameCluster(t *testing.T) {
	t.Parallel()

	mgr := NewClientManager(ClientConfig{
		RateLimitQPS:                   100,
		RateLimitBurst:                 100,
		CircuitBreakerFailureThreshold: 5,
		CircuitBreakerOpenDuration:     10 * time.Second,
	})
	defer mgr.Close()

	clusterKey := "ns/cluster1"
	caCert := []byte{} // Empty for this test

	f1 := mgr.FactoryFor(clusterKey, caCert)
	f2 := mgr.FactoryFor(clusterKey, caCert)

	if f1.clientState != f2.clientState {
		t.Errorf("expected same clientState for same clusterKey, got different pointers")
	}
}

func TestClientManager_FactoryFor_DifferentStateForDifferentClusters(t *testing.T) {
	t.Parallel()

	mgr := NewClientManager(ClientConfig{
		RateLimitQPS:   100,
		RateLimitBurst: 100,
	})
	defer mgr.Close()

	f1 := mgr.FactoryFor("ns/cluster1", nil)
	f2 := mgr.FactoryFor("ns/cluster2", nil)

	if f1.clientState == f2.clientState {
		t.Errorf("expected different clientState for different clusterKeys, got same pointer")
	}
}

func TestClientManager_FactoryFor_AppliesTLSServerNameOverride(t *testing.T) {
	t.Parallel()

	mgr := NewClientManager(ClientConfig{})
	defer mgr.Close()

	factory := mgr.FactoryFor("ns/cluster", nil, "acme.example.com")
	if factory == nil {
		t.Fatal("expected factory")
	}
	if factory.template.TLSServerName != "acme.example.com" {
		t.Fatalf("TLSServerName=%q, want acme.example.com", factory.template.TLSServerName)
	}
}

func TestClientManager_Close(t *testing.T) {
	t.Parallel()

	mgr := NewClientManager(ClientConfig{})

	_ = mgr.FactoryFor("ns/cluster1", nil)
	_ = mgr.FactoryFor("ns/cluster2", nil)

	mgr.Close()

	if got := clientManagerStateCount(t, mgr); got != 0 {
		t.Errorf("client state count=%d after Close, expected 0", got)
	}
}

func TestClientManager_NilReceiver(t *testing.T) {
	t.Parallel()

	var mgr *ClientManager

	// These should not panic
	if f := mgr.FactoryFor("ns/cluster", nil); f != nil {
		t.Errorf("expected nil factory for nil manager")
	}

	mgr.Close()
	mgr.Forget("cluster-uid")
	require.Empty(t, mgr.ClusterKeys())
}

func clientManagerStateCount(t *testing.T, mgr *ClientManager) int {
	t.Helper()
	if mgr == nil {
		return 0
	}
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return len(mgr.states)
}

func TestClientManager_CircuitBreakerIsolation(t *testing.T) {
	t.Parallel()

	// Create two separate ClientManagers
	mgr1 := NewClientManager(ClientConfig{
		RateLimitQPS:                   1000,
		RateLimitBurst:                 1000,
		CircuitBreakerFailureThreshold: 2,
		CircuitBreakerOpenDuration:     30 * time.Second,
	})
	defer mgr1.Close()

	mgr2 := NewClientManager(ClientConfig{
		RateLimitQPS:                   1000,
		RateLimitBurst:                 1000,
		CircuitBreakerFailureThreshold: 2,
		CircuitBreakerOpenDuration:     30 * time.Second,
	})
	defer mgr2.Close()

	var requests int32
	handlerErrors := newHTTPHandlerErrors(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != apiPathSysStepDown {
			handlerErrors.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	// Use the same clusterKey with different managers
	clusterKey := "test/cluster"

	f1 := mgr1.FactoryFor(clusterKey, nil)
	c1, err := f1.NewWithToken(server.URL, "token")
	if err != nil {
		t.Fatalf("NewWithToken() error: %v", err)
	}

	// Trip the circuit breaker on manager 1's client
	_ = c1.StepDown(context.Background())
	_ = c1.StepDown(context.Background())

	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests before circuit open, got %d", got)
	}

	// Circuit on mgr1 should be open
	err = c1.StepDown(context.Background())
	if err == nil || !operatorerrors.IsTransientRemoteOverloaded(err) {
		t.Fatalf("expected circuit breaker to block request, got %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected circuit breaker to block without new request; got %d requests", got)
	}

	// But manager 2's client should have its own circuit (not tripped)
	f2 := mgr2.FactoryFor(clusterKey, nil)
	c2, err := f2.NewWithToken(server.URL, "token")
	if err != nil {
		t.Fatalf("NewWithToken() error: %v", err)
	}

	// This should hit the server (different manager, different state)
	_ = c2.StepDown(context.Background())
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("expected 3 requests (manager 2 not blocked), got %d", got)
	}
}

func TestClientManager_FactoryCreatesWorkingClients(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPathSysHealth {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"standby":false}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	mgr := NewClientManager(ClientConfig{})
	defer mgr.Close()

	factory := mgr.FactoryFor("test/cluster", nil)
	client, err := factory.New(server.URL)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}

	if !health.Initialized {
		t.Errorf("expected Initialized=true")
	}
	if health.Sealed {
		t.Errorf("expected Sealed=false")
	}
}

func TestClientManager_StateLifecycle(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		key  string
	}{
		{name: "cluster UID", key: "cluster-uid"},
		{name: "empty key", key: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewClientManager(ClientConfig{})
			t.Cleanup(mgr.Close)
			first := mgr.FactoryFor(tt.key, nil)
			require.NotNil(t, first)
			if tt.key == "" {
				require.Nil(t, first.clientState)
				require.Empty(t, mgr.ClusterKeys())
			} else {
				require.ElementsMatch(t, []string{tt.key}, mgr.ClusterKeys())
			}

			mgr.Forget(tt.key)
			require.Empty(t, mgr.ClusterKeys())
			second := mgr.FactoryFor(tt.key, nil)
			require.NotNil(t, second)
			if tt.key != "" {
				require.NotSame(t, first.clientState, second.clientState)
				require.NotSame(t, first.jwtTokens, second.jwtTokens)
			}
			// Forget does not invalidate a factory that is still in use.
			_, err := first.New("https://openbao.example:8200")
			require.NoError(t, err)

			mgr.Close()
			mgr.Close()
			require.Empty(t, mgr.ClusterKeys())
			require.Nil(t, mgr.FactoryFor(tt.key, nil))
		})
	}
}

func TestClientManager_ConcurrentLifecycle(t *testing.T) {
	t.Parallel()
	mgr := NewClientManager(ClientConfig{})
	var workers, ready sync.WaitGroup
	ready.Add(16)
	start := make(chan struct{})
	for range 16 {
		workers.Go(func() {
			factory := mgr.FactoryFor("cluster-uid", nil)
			assert.NotNil(t, factory)
			ready.Done()
			<-start
			for range 100 {
				factory := mgr.FactoryFor("cluster-uid", nil)
				if factory != nil {
					_, err := factory.New("https://openbao.example:8200")
					assert.NoError(t, err)
				}
				_ = mgr.ClusterKeys()
				mgr.Forget("cluster-uid")
			}
		})
	}
	ready.Wait()
	close(start)
	mgr.Close()
	workers.Wait()
	require.Empty(t, mgr.ClusterKeys())
	require.Nil(t, mgr.FactoryFor("cluster-uid", nil))
}

func TestClientManager_ClusterLifetimeIsolation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		nextKey      string
		forget       bool
		wantRequests int32
		wantToken    string
	}{
		{name: "same UID shares state", nextKey: "old-uid", wantRequests: 1, wantToken: "token-1"},
		{name: "new UID starts fresh", nextKey: "new-uid", wantRequests: 2, wantToken: "token-2"},
		{name: "forgotten UID starts fresh", nextKey: "old-uid", forget: true, wantRequests: 2, wantToken: "token-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logins, requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == apiPathAuthJWTLogin {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"auth":{"client_token":"token-%d","ttl":3600}}`, logins.Add(1))
					return
				}
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			mgr := NewClientManager(ClientConfig{
				RateLimitQPS: 1000, RateLimitBurst: 1000,
				CircuitBreakerFailureThreshold: 1, CircuitBreakerOpenDuration: time.Minute,
			})
			t.Cleanup(mgr.Close)
			first := mgr.FactoryFor("old-uid", nil)
			token, err := first.LoginJWT(t.Context(), server.URL, "operator", "jwt")
			require.NoError(t, err)
			require.Equal(t, "token-1", token)
			oldClient, err := first.NewWithToken(server.URL, token)
			require.NoError(t, err)
			require.Error(t, oldClient.StepDown(t.Context()))
			if tt.forget {
				mgr.Forget("old-uid")
			}
			second := mgr.FactoryFor(tt.nextKey, nil)
			token, err = second.LoginJWT(t.Context(), server.URL, "operator", "jwt")
			require.NoError(t, err)
			require.Equal(t, tt.wantToken, token)
			nextClient, err := second.NewWithToken(server.URL, token)
			require.NoError(t, err)
			require.Error(t, nextClient.StepDown(t.Context()))
			require.Equal(t, tt.wantRequests, requests.Load())
			// The old factory retains its open circuit even after manager cleanup.
			require.Error(t, oldClient.StepDown(t.Context()))
			require.Equal(t, tt.wantRequests, requests.Load())
		})
	}
}
