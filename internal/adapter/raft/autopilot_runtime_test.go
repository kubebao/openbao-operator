package raft

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestAutopilotBaseURL(t *testing.T) {
	t.Parallel()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-ns"},
		Spec:       openbaov1alpha1.OpenBaoClusterSpec{Service: &openbaov1alpha1.ServiceConfig{}},
	}
	got := autopilotBaseURL(cluster)
	want := "https://cluster-a-public.tenant-ns.svc:8200"
	if got != want {
		t.Fatalf("autopilotBaseURL()=%q, want %q", got, want)
	}
}

func TestAutopilotBaseURL_UsesHeadlessServiceWithoutExternalService(t *testing.T) {
	t.Parallel()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-ns"},
	}
	got := autopilotBaseURL(cluster)
	want := "https://cluster-a.tenant-ns.svc:8200"
	if got != want {
		t.Fatalf("autopilotBaseURL()=%q, want %q", got, want)
	}
}

func TestHandleJWTAuthError(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"}}

	tests := []struct {
		name        string
		initialized bool
		err         error
		wantPerm    bool
		wantText    string
	}{
		{
			name:        "404 maps to prerequisite missing guidance",
			initialized: false,
			err: fmt.Errorf("failed to authenticate using JWT Auth: %w", &portopenbao.APIError{
				Operation:    "JWT auth request failed",
				StatusCode:   http.StatusNotFound,
				ResponseBody: `{"errors":["no handler for route"]}`,
			}),
			wantPerm: true,
			wantText: "Enable JWT auth",
		},
		{
			name:        "400 maps to role guidance",
			initialized: false,
			err: fmt.Errorf("failed to authenticate using JWT Auth: %w", &portopenbao.APIError{
				Operation:    "JWT auth request failed",
				StatusCode:   http.StatusBadRequest,
				ResponseBody: `{"errors":["invalid JWT token"]}`,
			}),
			wantPerm: true,
			wantText: "Ensure JWT role",
		},
		{
			name:        "initialized cluster uses manual guidance",
			initialized: true,
			err: fmt.Errorf("failed to authenticate using JWT Auth: %w", &portopenbao.APIError{
				Operation:    "JWT auth request failed",
				StatusCode:   http.StatusBadRequest,
				ResponseBody: `{"errors":["invalid JWT token"]}`,
			}),
			wantPerm: true,
			wantText: "Manually configure JWT role",
		},
		{
			name:        "other error remains generic",
			initialized: false,
			err:         errors.New("connection refused"),
			wantPerm:    false,
			wantText:    "failed to create authenticated OpenBao client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cluster.DeepCopy()
			c.Status.Initialized = tt.initialized
			err := mgr.handleJWTAuthError(c, tt.err)
			if tt.wantPerm && !operatorerrors.IsPermanent(err) {
				t.Fatalf("expected permanent error, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error=%q, expected to contain %q", err.Error(), tt.wantText)
			}
		})
	}
}

func TestGetClientTrustBundle(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			TLS: openbaov1alpha1.TLSConfig{Enabled: true},
		},
	}

	t.Run("operator managed tls reads cluster ca secret", func(t *testing.T) {
		clientset := k8sfake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-a-tls-ca", Namespace: "tenant-ns"},
			Data:       map[string][]byte{"ca.crt": []byte("pem-data")},
		})
		mgr := NewManager(clientset, nil, nil)

		trust, err := mgr.getClientTrustBundle(context.Background(), cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(trust.CACert) != "pem-data" {
			t.Fatalf("ca=%q, want pem-data", string(trust.CACert))
		}
		if trust.TLSServerName != "openbao-cluster-cluster-a.local" {
			t.Fatalf("tlsServerName=%q, want openbao-cluster-cluster-a.local", trust.TLSServerName)
		}
	})

	t.Run("private acme reads pki ca from unseal credentials secret", func(t *testing.T) {
		acmeCluster := cluster.DeepCopy()
		acmeCluster.Spec.TLS = openbaov1alpha1.TLSConfig{
			Enabled: true,
			Mode:    openbaov1alpha1.TLSModeACME,
			ACME: &openbaov1alpha1.ACMEConfig{
				Domains: []string{"cluster-a-acme.tenant-ns.svc"},
			},
		}
		acmeCluster.Spec.Configuration = &openbaov1alpha1.OpenBaoConfiguration{
			ACMECARoot: "/etc/bao/seal-creds/ca.crt",
		}
		acmeCluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "seal-creds"},
		}

		clientset := k8sfake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "seal-creds", Namespace: "tenant-ns"},
			Data:       map[string][]byte{"pki-ca.crt": []byte("pki-ca-data")},
		})
		mgr := NewManager(clientset, nil, nil)

		trust, err := mgr.getClientTrustBundle(context.Background(), acmeCluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(trust.CACert) != "pki-ca-data" {
			t.Fatalf("ca=%q, want pki-ca-data", string(trust.CACert))
		}
		if trust.TLSServerName != "cluster-a-acme.tenant-ns.svc" {
			t.Fatalf("tlsServerName=%q, want ACME service name", trust.TLSServerName)
		}
	})

	t.Run("missing secret", func(t *testing.T) {
		mgr := NewManager(k8sfake.NewClientset(), nil, nil)
		_, err := mgr.getClientTrustBundle(context.Background(), cluster)
		if err == nil || !strings.Contains(err.Error(), "failed to get OpenBao trust Secret") {
			t.Fatalf("expected missing secret error, got %v", err)
		}
	})

	t.Run("missing ca key", func(t *testing.T) {
		clientset := k8sfake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-a-tls-ca", Namespace: "tenant-ns"},
		})
		mgr := NewManager(clientset, nil, nil)
		_, err := mgr.getClientTrustBundle(context.Background(), cluster)
		if err == nil || !strings.Contains(err.Error(), `missing "ca.crt" key`) {
			t.Fatalf("expected missing key error, got %v", err)
		}
	})

	t.Run("forbidden maps to transient kubernetes api", func(t *testing.T) {
		clientset := k8sfake.NewClientset()
		clientset.PrependReactor("get", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "", Resource: "secrets"}, "cluster-a-tls-ca", errors.New("forbidden"))
		})
		mgr := NewManager(clientset, nil, nil)
		_, err := mgr.getClientTrustBundle(context.Background(), cluster)
		if err == nil {
			t.Fatalf("expected forbidden error")
		}
		if !operatorerrors.IsTransientKubernetesAPI(err) {
			t.Fatalf("expected transient kubernetes api classification, got %v", err)
		}
	})
}

func TestReconcileAutopilotConfig_EarlyBranches(t *testing.T) {
	t.Parallel()

	t.Run("cluster not initialized is no-op", func(t *testing.T) {
		mgr := NewManager(k8sfake.NewClientset(), nil, nil)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3},
			Status:     openbaov1alpha1.OpenBaoClusterStatus{Initialized: false},
		}
		if err := mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster); err != nil {
			t.Fatalf("expected nil error for uninitialized cluster, got %v", err)
		}
	})

	t.Run("missing root token secret is skipped", func(t *testing.T) {
		mgr := NewManager(k8sfake.NewClientset(), nil, nil)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Replicas: 3,
				TLS:      openbaov1alpha1.TLSConfig{Enabled: true},
			},
			Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
		}
		if err := mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster); err != nil {
			t.Fatalf("expected nil error for missing root token secret, got %v", err)
		}
	})

	t.Run("root token secret without token is skipped", func(t *testing.T) {
		clientset := k8sfake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"}})
		mgr := NewManager(clientset, nil, nil)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3},
			Status:     openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
		}
		if err := mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster); err != nil {
			t.Fatalf("expected nil error when root token is empty, got %v", err)
		}
	})

	t.Run("self-init without operator jwt bootstrap is skipped", func(t *testing.T) {
		mgr := NewManager(k8sfake.NewClientset(), nil, nil)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Replicas: 3,
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: true,
				},
			},
			Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
		}
		if err := mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster); err != nil {
			t.Fatalf("expected nil error when operator JWT bootstrap is disabled, got %v", err)
		}
	})

	t.Run("missing tls secret after root token returns error", func(t *testing.T) {
		clientset := k8sfake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"},
			Data:       map[string][]byte{"token": []byte("root-token")},
		})
		mgr := NewManager(clientset, nil, nil)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			Spec:       openbaov1alpha1.OpenBaoClusterSpec{Replicas: 3},
			Status:     openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
		}
		err := mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster)
		if err == nil || !strings.Contains(err.Error(), "failed to create authenticated OpenBao client") {
			t.Fatalf("expected authenticated client creation error, got %v", err)
		}
	})
}

type fakeScaleDownClient struct {
	calls            []string
	configureCalls   []portopenbao.AutopilotConfig
	configureErr     error
	autopilotConfig  portopenbao.AutopilotConfig
	autopilotReadErr error
	raftConfig       *portopenbao.RaftConfigurationResponse
	autopilotState   *portopenbao.RaftAutopilotStateResponse
	readErr          error
	removeCalls      []string
	removeErr        error
	stepDownCalls    int
	stepDownErr      error
}

func (c *fakeScaleDownClient) ReadRaftAutopilotConfig(context.Context) (*portopenbao.AutopilotConfig, error) {
	c.calls = append(c.calls, "read-autopilot")
	return &c.autopilotConfig, c.autopilotReadErr
}

func (c *fakeScaleDownClient) ConfigureRaftAutopilot(_ context.Context, config portopenbao.AutopilotConfig) error {
	c.calls = append(c.calls, "configure")
	c.configureCalls = append(c.configureCalls, config)
	if c.configureErr == nil {
		c.autopilotConfig = config
	}
	return c.configureErr
}

func (c *fakeScaleDownClient) ReadRaftConfiguration(context.Context) (*portopenbao.RaftConfigurationResponse, error) {
	c.calls = append(c.calls, "read")
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.raftConfig, nil
}

func (c *fakeScaleDownClient) ReadRaftAutopilotState(context.Context) (*portopenbao.RaftAutopilotStateResponse, error) {
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.autopilotState, nil
}

func (c *fakeScaleDownClient) RemoveRaftPeer(_ context.Context, serverID string) error {
	c.calls = append(c.calls, "remove:"+serverID)
	c.removeCalls = append(c.removeCalls, serverID)
	return c.removeErr
}

func (c *fakeScaleDownClient) StepDownLeader(context.Context) error {
	c.calls = append(c.calls, "step-down")
	c.stepDownCalls++
	return c.stepDownErr
}

type fakeScaleDownFactory struct {
	client       Client
	newWithToken int
}

func (f *fakeScaleDownFactory) NewWithJWT(context.Context, string, string, string) (Client, error) {
	return f.client, nil
}

func (f *fakeScaleDownFactory) NewWithToken(string, string) (Client, error) {
	f.newWithToken++
	return f.client, nil
}

type fakeScaleDownFactoryProvider struct {
	factory       ClientFactory
	clusterKey    string
	caCert        []byte
	tlsServerName string
}

func (p *fakeScaleDownFactoryProvider) FactoryFor(clusterKey string, caCert []byte, tlsServerName string) ClientFactory {
	p.clusterKey = clusterKey
	p.caCert = append([]byte(nil), caCert...)
	p.tlsServerName = tlsServerName
	return p.factory
}

func TestPrepareScaleDown_RemovesFollowerAndUpdatesAutopilot(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns", UID: "cluster-uid"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Replicas: 3,
			TLS:      openbaov1alpha1.TLSConfig{Enabled: true},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
	}

	client := &fakeScaleDownClient{
		raftConfig: &portopenbao.RaftConfigurationResponse{
			Config: portopenbao.RaftConfiguration{
				Servers: []portopenbao.RaftServer{
					{NodeID: "cluster-0", Address: "https://cluster-0.cluster.ns.svc:8200", Leader: true, Voter: true},
					{NodeID: "cluster-1", Address: "https://cluster-1.cluster.ns.svc:8200", Voter: true},
					{NodeID: "cluster-2", Address: "https://cluster-2.cluster.ns.svc:8200", Voter: true},
				},
			},
		},
	}
	factory := &fakeScaleDownFactory{client: client}
	provider := &fakeScaleDownFactoryProvider{factory: factory}

	clientset := k8sfake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"},
			Data:       map[string][]byte{"token": []byte("root-token")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-tls-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": []byte("pem-data")},
		},
	)

	mgr := NewManager(clientset, provider, nil)
	err := mgr.PrepareScaleDown(context.Background(), logr.Discard(), cluster, "cluster", 3, 2)
	if err != nil {
		t.Fatalf("PrepareScaleDown() error = %v", err)
	}

	if len(client.configureCalls) != 1 {
		t.Fatalf("expected one autopilot update, got %d", len(client.configureCalls))
	}
	if got := client.configureCalls[0].MinQuorum; got != 2 {
		t.Fatalf("autopilot min_quorum = %d, want 2", got)
	}
	if client.configureCalls[0].CleanupDeadServers {
		t.Fatalf("cleanup_dead_servers = true, want false for 2 replicas")
	}
	if len(client.removeCalls) != 1 || client.removeCalls[0] != "cluster-2" {
		t.Fatalf("removeCalls = %v, want [cluster-2]", client.removeCalls)
	}
	if client.stepDownCalls != 0 {
		t.Fatalf("stepDownCalls = %d, want 0", client.stepDownCalls)
	}
	if factory.newWithToken != 1 {
		t.Fatalf("NewWithToken() calls = %d, want 1", factory.newWithToken)
	}
	if provider.clusterKey != "cluster-uid" {
		t.Fatalf("clusterKey = %q, want cluster-uid", provider.clusterKey)
	}
	if string(provider.caCert) != "pem-data" {
		t.Fatalf("caCert = %q, want pem-data", string(provider.caCert))
	}
	if provider.tlsServerName != "openbao-cluster-cluster.local" {
		t.Fatalf("tlsServerName = %q, want openbao-cluster-cluster.local", provider.tlsServerName)
	}
	if want := []string{"read-autopilot", "configure", "read", "remove:cluster-2"}; !slices.Equal(client.calls, want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
}

func TestPrepareScaleDown_StepsDownLeaderVictim(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
	}

	client := &fakeScaleDownClient{
		raftConfig: &portopenbao.RaftConfigurationResponse{
			Config: portopenbao.RaftConfiguration{
				Servers: []portopenbao.RaftServer{
					{NodeID: "cluster-0", Address: "https://cluster-0.cluster.ns.svc:8200", Voter: true},
					{NodeID: "cluster-1", Address: "https://cluster-1.cluster.ns.svc:8200", Voter: true},
					{NodeID: "cluster-2", Address: "https://cluster-2.cluster.ns.svc:8200", Leader: true, Voter: true},
				},
			},
		},
	}

	clientset := k8sfake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"},
			Data:       map[string][]byte{"token": []byte("root-token")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-tls-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": []byte("pem-data")},
		},
	)

	mgr := NewManager(clientset, &fakeScaleDownFactoryProvider{factory: &fakeScaleDownFactory{client: client}}, nil)
	err := mgr.PrepareScaleDown(context.Background(), logr.Discard(), cluster, "cluster", 3, 2)
	if err == nil || !strings.Contains(err.Error(), "waiting for leader step-down on cluster-2 to complete") {
		t.Fatalf("expected step-down wait error, got %v", err)
	}
	if client.stepDownCalls != 1 {
		t.Fatalf("stepDownCalls = %d, want 1", client.stepDownCalls)
	}
	if len(client.removeCalls) != 0 {
		t.Fatalf("removeCalls = %v, want none", client.removeCalls)
	}
	if operatorerrors.IsTransient(err) || operatorerrors.IsPermanent(err) {
		t.Fatalf("leader wait error gained a classification: %v", err)
	}
	if want := []string{"read-autopilot", "configure", "read", "step-down"}; !slices.Equal(client.calls, want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
}

func TestPrepareReadReplicaScaleDown_RemovesNonVoter(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
	}

	client := &fakeScaleDownClient{
		raftConfig: &portopenbao.RaftConfigurationResponse{
			Config: portopenbao.RaftConfiguration{
				Servers: []portopenbao.RaftServer{
					{NodeID: "cluster-0", Address: "https://cluster-0.cluster.ns.svc:8200", Leader: true, Voter: true},
					{NodeID: "cluster-read-0", Address: "https://cluster-read-0.cluster.ns.svc:8200", Voter: false},
					{NodeID: "cluster-read-1", Address: "https://cluster-read-1.cluster.ns.svc:8200", Voter: false},
				},
			},
		},
	}

	clientset := k8sfake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"},
			Data:       map[string][]byte{"token": []byte("root-token")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-tls-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": []byte("pem-data")},
		},
	)

	mgr := NewManager(clientset, &fakeScaleDownFactoryProvider{factory: &fakeScaleDownFactory{client: client}}, nil)
	err := mgr.PrepareReadReplicaScaleDown(context.Background(), logr.Discard(), cluster, "cluster-read", 2, 1)
	if err != nil {
		t.Fatalf("PrepareReadReplicaScaleDown() error = %v", err)
	}
	if len(client.removeCalls) != 1 || client.removeCalls[0] != "cluster-read-1" {
		t.Fatalf("removeCalls = %v, want [cluster-read-1]", client.removeCalls)
	}
	if len(client.configureCalls) != 0 {
		t.Fatalf("configureCalls = %v, want none", client.configureCalls)
	}
	if client.stepDownCalls != 0 {
		t.Fatalf("stepDownCalls = %d, want 0", client.stepDownCalls)
	}
	if want := []string{"read", "remove:cluster-read-1"}; !slices.Equal(client.calls, want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
}

func TestPrepareScaleDown_OperationResults(t *testing.T) {
	t.Parallel()

	failure := errors.New("injected failure")
	follower := &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{
		Servers: []portopenbao.RaftServer{{NodeID: "peer-id", Address: "cluster-2.cluster.ns.svc", Voter: true}},
	}}
	leader := &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{
		Servers: []portopenbao.RaftServer{{NodeID: "peer-id", Address: "cluster-2.cluster.ns.svc", Leader: true, Voter: true}},
	}}
	nonvoter := &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{
		Servers: []portopenbao.RaftServer{{NodeID: "peer-id", Address: "cluster-2.cluster.ns.svc"}},
	}}
	nonvoterLeader := &portopenbao.RaftConfigurationResponse{Config: portopenbao.RaftConfiguration{
		Servers: []portopenbao.RaftServer{{NodeID: "peer-id", Address: "cluster-2.cluster.ns.svc", Leader: true}},
	}}
	tests := []struct {
		name        string
		readReplica bool
		client      *fakeScaleDownClient
		wantCalls   []string
		wantError   string
		wantClass   error
		wantCause   error
	}{
		{
			name: "configure failure stops before membership read", client: &fakeScaleDownClient{configureErr: failure, raftConfig: follower},
			wantCalls: []string{"read-autopilot", "configure"}, wantError: "transient connection error: failed to configure Raft Autopilot: injected failure",
			wantClass: operatorerrors.ErrTransientConnection, wantCause: failure,
		},
		{
			name: "membership failure stops before removal", client: &fakeScaleDownClient{readErr: failure, raftConfig: follower},
			wantCalls: []string{"read-autopilot", "configure", "read"}, wantError: "failed to read Raft configuration before scale down: injected failure", wantCause: failure,
		},
		{
			name: "step-down failure never removes peer", client: &fakeScaleDownClient{raftConfig: leader, stepDownErr: failure},
			wantCalls: []string{"read-autopilot", "configure", "read", "step-down"}, wantError: "failed to step down leader cluster-2 before scale down: injected failure", wantCause: failure,
		},
		{
			name: "successful step-down waits without removal", client: &fakeScaleDownClient{raftConfig: leader},
			wantCalls: []string{"read-autopilot", "configure", "read", "step-down"}, wantError: "waiting for leader step-down on cluster-2 to complete",
		},
		{
			name: "removal failure preserves server id", client: &fakeScaleDownClient{raftConfig: follower, removeErr: failure},
			wantCalls: []string{"read-autopilot", "configure", "read", "remove:peer-id"}, wantError: `failed to remove Raft peer "peer-id" before scale down: injected failure`, wantCause: failure,
		},
		{name: "nil membership still configures autopilot", client: &fakeScaleDownClient{}, wantCalls: []string{"read-autopilot", "configure", "read"}},
		{name: "absent peer still configures autopilot", client: &fakeScaleDownClient{raftConfig: &portopenbao.RaftConfigurationResponse{}}, wantCalls: []string{"read-autopilot", "configure", "read"}},
		{
			name: "read replica membership failure", readReplica: true, client: &fakeScaleDownClient{raftConfig: nonvoter, readErr: failure},
			wantCalls: []string{"read"}, wantError: "failed to read Raft configuration before read-replica scale down: injected failure", wantCause: failure,
		},
		{
			name: "read replica removal failure", readReplica: true, client: &fakeScaleDownClient{raftConfig: nonvoter, removeErr: failure},
			wantCalls: []string{"read", "remove:peer-id"}, wantError: `failed to remove read-replica Raft peer "peer-id" before scale down: injected failure`, wantCause: failure,
		},
		{
			name: "read replica voter refusal", readReplica: true, client: &fakeScaleDownClient{raftConfig: follower},
			wantCalls: []string{"read"}, wantError: "permanent prerequisites missing: read-replica pod cluster-2 is registered as a voter; refusing read-replica scale down",
			wantClass: operatorerrors.ErrPermanentPrerequisitesMissing,
		},
		{
			name: "read replica voter leader refusal precedes step-down", readReplica: true, client: &fakeScaleDownClient{raftConfig: leader},
			wantCalls: []string{"read"}, wantError: "permanent prerequisites missing: read-replica pod cluster-2 is registered as a voter; refusing read-replica scale down",
			wantClass: operatorerrors.ErrPermanentPrerequisitesMissing,
		},
		{name: "read replica nonvoter leader is removed without step-down", readReplica: true, client: &fakeScaleDownClient{raftConfig: nonvoterLeader}, wantCalls: []string{"read", "remove:peer-id"}},
		{name: "read replica nil membership", readReplica: true, client: &fakeScaleDownClient{}, wantCalls: []string{"read"}},
		{name: "read replica absent peer", readReplica: true, client: &fakeScaleDownClient{raftConfig: &portopenbao.RaftConfigurationResponse{}}, wantCalls: []string{"read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, cluster := newMaintenanceTestManager(tt.client)
			before := cluster.DeepCopy()
			prepare := mgr.PrepareScaleDown
			if tt.readReplica {
				prepare = mgr.PrepareReadReplicaScaleDown
			}
			err := prepare(context.Background(), logr.Discard(), cluster, "cluster", 3, 2)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || err.Error() != tt.wantError {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
			if tt.wantClass != nil {
				if !errors.Is(err, tt.wantClass) {
					t.Errorf("error = %v, want classification %v", err, tt.wantClass)
				}
			} else if operatorerrors.IsTransient(err) || operatorerrors.IsPermanent(err) {
				t.Errorf("error gained a classification: %v", err)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Errorf("error = %v, want wrapped cause %v", err, tt.wantCause)
			}
			if !slices.Equal(tt.client.calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", tt.client.calls, tt.wantCalls)
			}
			if !tt.readReplica && (len(tt.client.configureCalls) != 1 || tt.client.configureCalls[0].MinQuorum != 2) {
				t.Errorf("configure calls = %+v, want desired replicas quorum 2", tt.client.configureCalls)
			}
			if !reflect.DeepEqual(cluster, before) {
				t.Fatal("scale-down preparation mutated the input cluster")
			}
		})
	}
}

func TestPrepareScaleDown_NoDownscaleSkipsValidation(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}
	for _, replicas := range []int32{2, 3} {
		if err := mgr.PrepareScaleDown(context.Background(), logr.Discard(), nil, "", 2, replicas); err != nil {
			t.Errorf("PrepareScaleDown(2, %d) = %v, want nil", replicas, err)
		}
		if err := mgr.PrepareReadReplicaScaleDown(context.Background(), logr.Discard(), nil, "", 2, replicas); err != nil {
			t.Errorf("PrepareReadReplicaScaleDown(2, %d) = %v, want nil", replicas, err)
		}
	}
}

func TestAutopilotConfiguration_ErrorClassification(t *testing.T) {
	t.Parallel()

	for _, initial := range []bool{true, false} {
		t.Run(fmt.Sprintf("initial=%t", initial), func(t *testing.T) {
			failure := errors.New("configuration rejected")
			client := &fakeScaleDownClient{configureErr: failure}
			mgr, cluster := newMaintenanceTestManager(client)
			var err error
			wantText := "failed to configure Raft Autopilot: configuration rejected"
			if initial {
				err = mgr.ConfigureAutopilot(context.Background(), logr.Discard(), cluster, "root-token")
			} else {
				err = mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster)
				wantText = "transient connection error: " + wantText
			}
			if err == nil || err.Error() != wantText || !errors.Is(err, failure) {
				t.Fatalf("error = %v, want %q wrapping %v", err, wantText, failure)
			}
			if operatorerrors.IsTransient(err) != !initial || operatorerrors.IsPermanent(err) {
				t.Errorf("unexpected error classification: %v", err)
			}
			want := []string{"configure"}
			if !initial {
				want = append([]string{"read-autopilot"}, want...)
			}
			if !slices.Equal(client.calls, want) {
				t.Errorf("calls = %v, want %v", client.calls, want)
			}
		})
	}
}

func newMaintenanceTestManager(client *fakeScaleDownClient) (*Manager, *openbaov1alpha1.OpenBaoCluster) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment, Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
	}
	clientset := k8sfake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"},
			Data:       map[string][]byte{"token": []byte("root-token")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-tls-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": []byte("pem-data")},
		},
	)
	return NewManager(clientset, &fakeScaleDownFactoryProvider{factory: &fakeScaleDownFactory{client: client}}, nil), cluster
}

func TestWrapScaleDownPermissionError_SelfInitClusterRequiresUpdatedPolicy(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC:    &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true},
			},
		},
	}

	err := mgr.wrapScaleDownPermissionError(cluster, fmt.Errorf("read raft config: %w", &portopenbao.APIError{
		Operation:    "raft configuration request failed",
		StatusCode:   http.StatusForbidden,
		ResponseBody: `{"errors":["permission denied"]}`,
	}))
	if err == nil {
		t.Fatal("expected permission error")
	}
	if !operatorerrors.IsPermanent(err) {
		t.Fatalf("expected permanent classification, got %v", err)
	}
	if !strings.Contains(err.Error(), "remove-peer") {
		t.Fatalf("expected permission guidance, got %v", err)
	}
}

func TestAutopilotReconcileReadsBeforeWriting(t *testing.T) {
	for _, tt := range []struct {
		name        string
		scaleDown   bool
		drift       bool
		readFailure bool
	}{
		{name: "steady reconciliation"},
		{name: "reconciliation repairs drift", drift: true},
		{name: "reconciliation read failure", readFailure: true},
		{name: "scale down already configured", scaleDown: true},
		{name: "scale down configuration drift", scaleDown: true, drift: true},
		{name: "scale down read failure", scaleDown: true, readFailure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeScaleDownClient{}
			mgr, cluster := newMaintenanceTestManager(c)
			target := cluster.DeepCopy()
			if tt.scaleDown {
				target.Spec.Replicas = 2
			}
			c.autopilotConfig = portopenbao.BuildAutopilotConfig(target)
			c.autopilotConfig.DeadServerLastContactThreshold = "300s"
			if tt.drift {
				c.autopilotConfig.MinQuorum++
			}
			failure := errors.New("config read failed")
			if tt.readFailure {
				c.autopilotReadErr = failure
			}
			var err error
			if tt.scaleDown {
				err = mgr.PrepareScaleDown(t.Context(), logr.Discard(), cluster, "cluster", 3, 2)
			} else {
				err = mgr.ReconcileAutopilotConfig(t.Context(), logr.Discard(), cluster)
			}
			want := []string{"read-autopilot"}
			if tt.readFailure {
				if !errors.Is(err, failure) || !operatorerrors.IsTransient(err) {
					t.Fatalf("expected transient read failure, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if tt.drift {
					want = append(want, "configure")
				}
				if tt.scaleDown {
					want = append(want, "read")
				}
			}
			if !slices.Equal(c.calls, want) {
				t.Fatalf("calls = %v, want %v", c.calls, want)
			}
			if !tt.readFailure {
				c.calls = nil
				if tt.scaleDown {
					err = mgr.PrepareScaleDown(t.Context(), logr.Discard(), cluster, "cluster", 3, 2)
				} else {
					err = mgr.ReconcileAutopilotConfig(t.Context(), logr.Discard(), cluster)
				}
				if err != nil {
					t.Fatal(err)
				}
				want = []string{"read-autopilot"}
				if tt.scaleDown {
					want = append(want, "read")
				}
				if !slices.Equal(c.calls, want) {
					t.Fatalf("repeat calls = %v, want %v", c.calls, want)
				}
			}
		})
	}
}

// A malformed Target object must not select legacy root-token authentication,
// even when admission enforcement is disabled or an old object bypassed it.
func TestTargetJWT_IssuanceFailureNeverUsesRootToken(t *testing.T) {
	t.Parallel()
	for _, selfInit := range []*openbaov1alpha1.SelfInitConfig{nil, {Enabled: true}} {
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				ControllerJWTMode: openbaov1alpha1.ControllerJWTModeTarget,
				SelfInit:          selfInit, Replicas: 3, TLS: openbaov1alpha1.TLSConfig{Enabled: true},
			},
			Status: openbaov1alpha1.OpenBaoClusterStatus{Initialized: true},
		}
		clientset := k8sfake.NewClientset(
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cluster-root-token", Namespace: "ns"},
				Data: map[string][]byte{"token": []byte("root-token")}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cluster-tls-ca", Namespace: "ns"},
				Data: map[string][]byte{"ca.crt": []byte("pem-data")}},
		)
		factory := &fakeScaleDownFactory{}
		issuanceErr := errors.New("target issuance unavailable")
		provider := controllerTokenProviderFunc(func(context.Context, *openbaov1alpha1.OpenBaoCluster) (string, error) {
			return "", issuanceErr
		})
		mgr := NewManager(clientset, &fakeScaleDownFactoryProvider{factory: factory}, provider)
		_, err := mgr.newScaleDownClient(context.Background(), cluster)
		if !errors.Is(err, issuanceErr) {
			t.Fatalf("scale-down error = %v, want issuance error", err)
		}
		err = mgr.ReconcileAutopilotConfig(context.Background(), logr.Discard(), cluster)
		if !errors.Is(err, issuanceErr) {
			t.Fatalf("Autopilot error = %v, want issuance error", err)
		}
		if factory.newWithToken != 0 {
			t.Fatal("Target authentication fell back to the root token")
		}
	}
}

type controllerTokenProviderFunc func(context.Context, *openbaov1alpha1.OpenBaoCluster) (string, error)

func (f controllerTokenProviderFunc) Token(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (string, error) {
	return f(ctx, cluster)
}

func TestNewOpenBaoClientsUseClusterUID(t *testing.T) {
	t.Parallel()
	for _, jwt := range []bool{false, true} {
		t.Run(fmt.Sprintf("JWT=%t", jwt), func(t *testing.T) {
			provider := &fakeScaleDownFactoryProvider{factory: &fakeScaleDownFactory{client: &fakeScaleDownClient{}}}
			tokens := controllerTokenProviderFunc(func(context.Context, *openbaov1alpha1.OpenBaoCluster) (string, error) {
				return "test-jwt", nil
			})
			manager := NewManager(k8sfake.NewClientset(), provider, tokens)
			for _, uid := range []types.UID{"old-uid", "new-uid"} {
				cluster := &openbaov1alpha1.OpenBaoCluster{
					ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "same-name", UID: uid},
				}
				var err error
				if jwt {
					_, err = manager.newOpenBaoClient(t.Context(), cluster)
				} else {
					_, err = manager.newOpenBaoClientWithToken(t.Context(), cluster, "test-token")
				}
				require.NoError(t, err)
				require.Equal(t, string(uid), provider.clusterKey)
			}
		})
	}
}
