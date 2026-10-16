package openbao

import (
	"sync"

	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// ClientManager owns shared rate limits, circuit breakers, and JWT token caches.
type ClientManager struct {
	mu     sync.RWMutex
	states map[string]*clientState

	// defaults holds the default configuration for smart client state.
	defaults portopenbao.ClientConfig
}

// NewClientManager creates a new ClientManager with the given default limits.
// The defaults are applied when creating client state for new clusters.
func NewClientManager(defaults portopenbao.ClientConfig) *ClientManager {
	return &ClientManager{
		states:   make(map[string]*clientState),
		defaults: defaults,
	}
}

// FactoryFor returns a ClientFactory bound to the given clusterKey.
// The factory shares client state with other factories for the same clusterKey.
//
// The clusterKey must identify one cluster lifetime, such as its Kubernetes UID.
// An empty key disables state sharing. The caCert is the PEM-encoded CA certificate
// for TLS verification. tlsServerName optionally overrides the hostname used
// for TLS verification when clients connect through a different Service name.
func (m *ClientManager) FactoryFor(clusterKey string, caCert []byte, tlsServerName ...string) *ClientFactory {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	if m.states == nil {
		m.mu.Unlock()
		return nil
	}
	state := m.getOrCreateState(clusterKey)
	m.mu.Unlock()

	template := m.defaults
	template.ClusterKey = clusterKey
	template.CACert = caCert
	if len(tlsServerName) > 0 {
		template.TLSServerName = tlsServerName[0]
	}

	return newClientFactoryWithState(template, state)
}

// getOrCreateState requires m.mu to be locked.
func (m *ClientManager) getOrCreateState(clusterKey string) *clientState {
	if clusterKey == "" {
		return nil
	}
	if state, ok := m.states[clusterKey]; ok {
		return state
	}
	state := newClientState(m.defaults)
	m.states[clusterKey] = state
	return state
}

// ClusterKeys returns a snapshot of the cluster lifetimes retained by the manager.
func (m *ClientManager) ClusterKeys() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.states))
	for key := range m.states {
		keys = append(keys, key)
	}
	return keys
}

// Forget releases the manager's reference to a deleted cluster's state.
// Existing factories and clients remain usable until their callers release them.
func (m *ClientManager) Forget(clusterKey string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, clusterKey)
}

// Close releases all retained state and prevents new factory creation.
// Existing factories and clients remain usable until their callers release them.
func (m *ClientManager) Close() {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.states = nil
}
