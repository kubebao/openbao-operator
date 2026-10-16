package raft

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/openbaotls"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	// rootTokenSecretKey is the key used to store the root token in the Secret data.
	rootTokenSecretKey = "token"
	suffixRootToken    = "-root-token"
	roleNameOperator   = "openbao-operator"
	portAPI            = 8200
)

type Client interface {
	portopenbao.AutopilotConfigurer
	ReadRaftAutopilotConfig(ctx context.Context) (*portopenbao.AutopilotConfig, error)
	ReadRaftConfiguration(ctx context.Context) (*portopenbao.RaftConfigurationResponse, error)
	ReadRaftAutopilotState(ctx context.Context) (*portopenbao.RaftAutopilotStateResponse, error)
	RemoveRaftPeer(ctx context.Context, serverID string) error
	StepDownLeader(ctx context.Context) error
}

type ClientFactory interface {
	NewWithJWT(ctx context.Context, baseURL, role, jwtToken string) (Client, error)
	NewWithToken(baseURL, token string) (Client, error)
}

type ClientFactoryProvider interface {
	FactoryFor(clusterKey string, caCert []byte, tlsServerName string) ClientFactory
}

// Manager handles Raft Autopilot configuration for OpenBao clusters.
type Manager struct {
	clientset             kubernetes.Interface
	clientFactoryProvider ClientFactoryProvider
	tokens                portauth.ControllerTokenProvider
}

// NewManager creates a new Raft Autopilot Manager.
func NewManager(clientset kubernetes.Interface, clientFactoryProvider ClientFactoryProvider, tokens portauth.ControllerTokenProvider) *Manager {
	return &Manager{
		clientset:             clientset,
		clientFactoryProvider: clientFactoryProvider,
		tokens:                tokens,
	}
}

// ReconcileAutopilotConfig reconciles Raft Autopilot configuration for an initialized cluster.
// This is called during Day 2 operations (e.g., when replicas or autopilot config changes).
// It handles authentication via root token (non-SelfInit) or JWT (SelfInit).
// JWT authentication uses the cluster's selected shared or target-specific mode.
func (m *Manager) ReconcileAutopilotConfig(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	// Only reconcile if cluster is initialized
	if !cluster.Status.Initialized {
		return nil
	}

	// Log the Profile and replicas being used for debugging
	logger.V(1).Info("Reconciling autopilot config",
		"profile", cluster.Spec.Profile,
		"replicas", cluster.Spec.Replicas,
		"cluster", cluster.Name,
	)

	// Build desired Autopilot configuration
	desiredConfig := portopenbao.BuildAutopilotConfig(cluster)

	// Log the calculated min_quorum for debugging
	logger.V(1).Info("Calculated autopilot config",
		"min_quorum", desiredConfig.MinQuorum,
		"cleanup_dead_servers", desiredConfig.CleanupDeadServers,
		"profile", cluster.Spec.Profile,
		"replicas", cluster.Spec.Replicas,
	)

	// Get authenticated client
	// For SelfInit clusters, use ClientManager which handles JWT authentication
	// For non-SelfInit clusters, use root token from Secret
	selfInitEnabled := cluster.Spec.SelfInit != nil && cluster.Spec.SelfInit.Enabled
	targetJWT := cluster.Spec.ControllerJWTMode == openbaov1alpha1.ControllerJWTModeTarget
	var client Client
	var err error

	if selfInitEnabled || targetJWT {
		if !portauth.OperatorJWTBootstrapEnabled(cluster) && !targetJWT {
			logger.V(1).Info("Skipping autopilot config reconciliation for self-init cluster without operator JWT bootstrap")
			return nil
		}

		// Use ClientManager for JWT authentication (SelfInit)
		client, err = m.newOpenBaoClient(ctx, cluster)
		if err != nil {
			return fmt.Errorf("failed to create OpenBao client for autopilot config: %w", err)
		}
	} else {
		// Use root token from Secret (non-SelfInit)
		secretName := cluster.Name + suffixRootToken
		secret, err := m.clientset.CoreV1().Secrets(cluster.Namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("Root token Secret not found; skipping autopilot config reconciliation", "secret", secretName)
				return nil
			}
			return fmt.Errorf("failed to get root token Secret %s/%s: %w", cluster.Namespace, secretName, err)
		}

		rootToken, ok := secret.Data[rootTokenSecretKey]
		if !ok || len(rootToken) == 0 {
			logger.V(1).Info("Root token Secret missing token data; skipping autopilot config reconciliation", "secret", secretName)
			return nil
		}

		client, err = m.newOpenBaoClientWithToken(ctx, cluster, string(rootToken))
		if err != nil {
			return fmt.Errorf("failed to create authenticated OpenBao client: %w", err)
		}
	}

	return m.configureAutopilotWithClient(ctx, logger, client, cluster)
}

// PrepareScaleDown stages a single safe scale-down step by reconciling the
// target autopilot configuration, stepping the victim leader down when needed,
// and removing the departing Raft peer before the StatefulSet shrinks.
func (m *Manager) PrepareScaleDown(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	statefulSetName string,
	currentReplicas int32,
	desiredReplicas int32,
) error {
	if currentReplicas <= desiredReplicas {
		return nil
	}
	if cluster == nil {
		return fmt.Errorf("cluster is required")
	}
	if strings.TrimSpace(statefulSetName) == "" {
		return fmt.Errorf("statefulset name is required")
	}

	client, err := m.newScaleDownClient(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to create authenticated OpenBao client for safe scale down: %w", err)
	}

	scaleStepCluster := cluster.DeepCopy()
	scaleStepCluster.Spec.Replicas = desiredReplicas
	if err := m.configureAutopilotWithClient(ctx, logger, client, scaleStepCluster); err != nil {
		return err
	}

	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	raftConfig, err := client.ReadRaftConfiguration(configCtx)
	if err != nil {
		return m.wrapScaleDownPermissionError(
			cluster,
			fmt.Errorf("failed to read Raft configuration before scale down: %w", err),
		)
	}

	victimPodName := fmt.Sprintf("%s-%d", statefulSetName, currentReplicas-1)
	decision := portopenbao.DecideRaftPeerRemoval(raftConfig, victimPodName)
	if decision.Action == portopenbao.RaftPeerAbsent {
		logger.Info("Victim pod already absent from Raft configuration; continuing with scale down", "victim", victimPodName)
		return nil
	}

	if decision.Action == portopenbao.RaftPeerStepDown {
		logger.Info("Victim pod is current Raft leader; stepping down before scale down", "victim", victimPodName, "node_id", decision.ServerID)
		if err := client.StepDownLeader(configCtx); err != nil {
			return fmt.Errorf("failed to step down leader %s before scale down: %w", victimPodName, err)
		}
		return fmt.Errorf("waiting for leader step-down on %s to complete", victimPodName)
	}

	logger.Info("Removing Raft peer before scale down", "victim", victimPodName, "node_id", decision.ServerID)
	if err := client.RemoveRaftPeer(configCtx, decision.ServerID); err != nil {
		return m.wrapScaleDownPermissionError(
			cluster,
			fmt.Errorf("failed to remove Raft peer %q before scale down: %w", decision.ServerID, err),
		)
	}

	return nil
}

// PrepareReadReplicaScaleDown stages a single safe steady-state read-replica
// scale-down step by removing the departing non-voter before the StatefulSet
// shrinks. Unlike voter scale-down, there is no leader step-down or autopilot
// min_quorum adjustment.
func (m *Manager) PrepareReadReplicaScaleDown(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	statefulSetName string,
	currentReplicas int32,
	desiredReplicas int32,
) error {
	if currentReplicas <= desiredReplicas {
		return nil
	}
	if cluster == nil {
		return fmt.Errorf("cluster is required")
	}
	if strings.TrimSpace(statefulSetName) == "" {
		return fmt.Errorf("statefulset name is required")
	}

	client, err := m.newScaleDownClient(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to create authenticated OpenBao client for read-replica scale down: %w", err)
	}

	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	raftConfig, err := client.ReadRaftConfiguration(configCtx)
	if err != nil {
		return m.wrapScaleDownPermissionError(
			cluster,
			fmt.Errorf("failed to read Raft configuration before read-replica scale down: %w", err),
		)
	}

	victimPodName := fmt.Sprintf("%s-%d", statefulSetName, currentReplicas-1)
	decision := portopenbao.DecideReadReplicaRemoval(raftConfig, victimPodName)
	if decision.Action == portopenbao.RaftPeerAbsent {
		logger.Info("Read-replica victim pod already absent from Raft configuration; continuing with scale down", "victim", victimPodName)
		return nil
	}
	if decision.Action == portopenbao.RaftPeerRefuseVoter {
		return operatorerrors.WrapPermanentPrerequisitesMissing(
			fmt.Errorf("read-replica pod %s is registered as a voter; refusing read-replica scale down", victimPodName),
		)
	}

	logger.Info("Removing read-replica Raft peer before scale down", "victim", victimPodName, "node_id", decision.ServerID)
	if err := client.RemoveRaftPeer(configCtx, decision.ServerID); err != nil {
		return m.wrapScaleDownPermissionError(
			cluster,
			fmt.Errorf("failed to remove read-replica Raft peer %q before scale down: %w", decision.ServerID, err),
		)
	}

	return nil
}

// ReadRaftConfiguration reads the current authenticated Raft configuration for
// status observation and topology checks.
func (m *Manager) ReadRaftConfiguration(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (*portopenbao.RaftConfigurationResponse, error) {
	if cluster == nil {
		return nil, fmt.Errorf("cluster is required")
	}

	client, err := m.newScaleDownClient(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to create authenticated OpenBao client for raft membership read: %w", err)
	}

	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	raftConfig, err := client.ReadRaftConfiguration(configCtx)
	if err != nil {
		return nil, m.wrapScaleDownPermissionError(
			cluster,
			fmt.Errorf("failed to read Raft configuration for status observation: %w", err),
		)
	}

	return raftConfig, nil
}

// ReadRaftAutopilotState reads the current authenticated Raft Autopilot state
// for status observation and topology health checks.
func (m *Manager) ReadRaftAutopilotState(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (*portopenbao.RaftAutopilotStateResponse, error) {
	if cluster == nil {
		return nil, fmt.Errorf("cluster is required")
	}

	client, err := m.newScaleDownClient(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to create authenticated OpenBao client for raft autopilot state read: %w", err)
	}

	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	autopilotState, err := client.ReadRaftAutopilotState(configCtx)
	if err != nil {
		return nil, m.wrapScaleDownPermissionError(
			cluster,
			fmt.Errorf("failed to read Raft Autopilot state for status observation: %w", err),
		)
	}

	return autopilotState, nil
}

// ConfigureAutopilot configures Raft Autopilot for automatic dead server cleanup.
// This is called after cluster initialization with the root token.
func (m *Manager) ConfigureAutopilot(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, rootToken string) error {
	// Create authenticated client with root token
	client, err := m.newOpenBaoClientWithToken(ctx, cluster, rootToken)
	if err != nil {
		return fmt.Errorf("failed to create authenticated OpenBao client: %w", err)
	}

	// Build Autopilot configuration from CRD or use defaults
	config := portopenbao.BuildAutopilotConfig(cluster)

	logger.Info("Configuring Raft Autopilot",
		"cleanup_dead_servers", config.CleanupDeadServers,
		"dead_server_last_contact_threshold", config.DeadServerLastContactThreshold,
		"min_quorum", config.MinQuorum,
	)

	autopilotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := client.ConfigureRaftAutopilot(autopilotCtx, config); err != nil {
		return fmt.Errorf("failed to configure Raft Autopilot: %w", err)
	}

	logger.Info("Raft Autopilot configured successfully")
	return nil
}

func (m *Manager) configureAutopilotWithClient(ctx context.Context, logger logr.Logger, client Client, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if client == nil {
		return fmt.Errorf("OpenBao client is required")
	}
	if cluster == nil {
		return fmt.Errorf("cluster is required")
	}

	desiredConfig := portopenbao.BuildAutopilotConfig(cluster)

	logger.V(1).Info("Reconciling Raft Autopilot configuration",
		"cluster", cluster.Name,
		"cleanup_dead_servers", desiredConfig.CleanupDeadServers,
		"dead_server_last_contact_threshold", desiredConfig.DeadServerLastContactThreshold,
		"min_quorum", desiredConfig.MinQuorum,
		"server_stabilization_time", desiredConfig.ServerStabilizationTime,
	)

	autopilotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	currentConfig, err := client.ReadRaftAutopilotConfig(autopilotCtx)
	if err != nil {
		return operatorerrors.WrapTransientConnection(
			fmt.Errorf("failed to read Raft Autopilot configuration: %w", err),
		)
	}
	if currentConfig == nil {
		return operatorerrors.WrapTransientConnection(fmt.Errorf("raft Autopilot configuration is missing"))
	}
	if desiredConfig.Matches(*currentConfig) {
		return nil
	}

	if err := client.ConfigureRaftAutopilot(autopilotCtx, desiredConfig); err != nil {
		logger.V(1).Info("Raft Autopilot configuration not ready; will retry on next reconcile", "error", err)
		return operatorerrors.WrapTransientConnection(
			fmt.Errorf("failed to configure Raft Autopilot: %w", err),
		)
	}

	logger.V(1).Info("Raft Autopilot configuration reconciled successfully")
	return nil
}

// newOpenBaoClient constructs an authenticated OpenBao client for talking to the pod-0 instance
// using JWT authentication via ClientManager.
func (m *Manager) newOpenBaoClient(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (Client, error) {
	if strings.TrimSpace(cluster.Name) == "" || strings.TrimSpace(cluster.Namespace) == "" {
		return nil, fmt.Errorf("cluster name and namespace are required")
	}

	baseURL := autopilotBaseURL(cluster)

	trust, err := m.getClientTrustBundle(ctx, cluster)
	if err != nil {
		return nil, err
	}

	// Get client factory
	clusterKey := string(cluster.UID)
	factory := m.clientFactory(clusterKey, trust.CACert, trust.TLSServerName)
	if factory == nil {
		return nil, fmt.Errorf("client factory provider returned nil factory for cluster %s", clusterKey)
	}

	// Resolve only this cluster's selected credential; Target never falls back.
	if m.tokens == nil {
		return nil, fmt.Errorf("controller token provider is required")
	}
	jwtToken, err := m.tokens.Token(ctx, cluster)
	if err != nil {
		return nil, err
	}

	// Create authenticated client
	client, err := factory.NewWithJWT(ctx, baseURL, roleNameOperator, jwtToken)
	if err != nil {
		return nil, m.handleJWTAuthError(cluster, err)
	}

	return client, nil
}

func (m *Manager) newScaleDownClient(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (Client, error) {
	if cluster == nil {
		return nil, fmt.Errorf("cluster is required")
	}

	if cluster.Spec.ControllerJWTMode == openbaov1alpha1.ControllerJWTModeTarget ||
		(cluster.Spec.SelfInit != nil && cluster.Spec.SelfInit.Enabled) {
		return m.newOpenBaoClient(ctx, cluster)
	}

	secretName := cluster.Name + suffixRootToken
	secret, err := m.clientset.CoreV1().Secrets(cluster.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get root token Secret %s/%s: %w", cluster.Namespace, secretName, err)
	}

	rootToken, ok := secret.Data[rootTokenSecretKey]
	if !ok || len(rootToken) == 0 {
		return nil, fmt.Errorf("root token Secret %s/%s is missing token data", cluster.Namespace, secretName)
	}

	client, err := m.newOpenBaoClientWithToken(ctx, cluster, string(rootToken))
	if err != nil {
		return nil, fmt.Errorf("failed to create authenticated OpenBao client: %w", err)
	}

	return client, nil
}

func (m *Manager) getClientTrustBundle(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (openbaotls.ClientTrustBundle, error) {
	return openbaotls.ReadClientTrustBundle(ctx, m.clientset, cluster)
}

// handleJWTAuthError provides helpful error messages for common JWT auth failures.
func (m *Manager) handleJWTAuthError(cluster *openbaov1alpha1.OpenBaoCluster, err error) error {
	if portopenbao.IsStatus(err, http.StatusNotFound) {
		guidance := "Enable JWT auth via spec.selfInit.oidc.enabled: true or configure JWT via self-init requests"
		if cluster.Status.Initialized {
			guidance = "Manually configure JWT authentication via OpenBao API/CLI"
		}
		return operatorerrors.WrapPermanentPrerequisitesMissing(
			fmt.Errorf("JWT authentication method not enabled in OpenBao cluster %s/%s. %s",
				cluster.Namespace, cluster.Name, guidance),
		)
	}
	if portopenbao.IsStatus(err, http.StatusBadRequest) {
		guidance := fmt.Sprintf("Ensure JWT role '%s' is configured", roleNameOperator)
		if cluster.Status.Initialized {
			guidance = "Manually configure JWT role via OpenBao API/CLI"
		}
		return operatorerrors.WrapPermanentPrerequisitesMissing(
			fmt.Errorf("JWT authentication failed for cluster %s/%s. %s. Original error: %w",
				cluster.Namespace, cluster.Name, guidance, err),
		)
	}
	return fmt.Errorf("failed to create authenticated OpenBao client: %w", err)
}

func (m *Manager) wrapScaleDownPermissionError(cluster *openbaov1alpha1.OpenBaoCluster, err error) error {
	if cluster == nil || !portauth.OperatorJWTBootstrapEnabled(cluster) {
		return err
	}
	if !portopenbao.IsStatus(err, http.StatusForbidden) {
		return err
	}

	return operatorerrors.WrapPermanentPrerequisitesMissing(
		fmt.Errorf(
			"controller raft maintenance in cluster %s/%s requires the operator JWT policy to allow Raft configuration reads, Autopilot state reads, and remove-peer updates: %w",
			cluster.Namespace,
			cluster.Name,
			err,
		),
	)
}

// newOpenBaoClientWithToken creates an authenticated OpenBao client with the given token.
func (m *Manager) newOpenBaoClientWithToken(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, token string) (Client, error) {
	if strings.TrimSpace(cluster.Name) == "" || strings.TrimSpace(cluster.Namespace) == "" {
		return nil, fmt.Errorf("cluster name and namespace are required to build OpenBao client")
	}

	baseURL := autopilotBaseURL(cluster)

	trust, err := m.getClientTrustBundle(ctx, cluster)
	if err != nil {
		return nil, err
	}

	// Create OpenBao client using the ClientManager for proper state isolation.
	clusterKey := string(cluster.UID)
	factory := m.clientFactory(clusterKey, trust.CACert, trust.TLSServerName)
	if factory == nil {
		return nil, fmt.Errorf("client factory provider returned nil factory for cluster %s", clusterKey)
	}

	client, err := factory.NewWithToken(baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenBao client for %s: %w", baseURL, err)
	}

	return client, nil
}

func (m *Manager) clientFactory(clusterKey string, caCert []byte, tlsServerName string) ClientFactory {
	if m == nil || m.clientFactoryProvider == nil {
		return nil
	}
	return m.clientFactoryProvider.FactoryFor(clusterKey, caCert, tlsServerName)
}

// autopilotBaseURL returns a stable in-cluster address for performing Raft autopilot operations.
//
// We intentionally do not use Pod DNS names here because blue/green deployments use revisioned
// StatefulSet pod names (e.g. "<cluster>-<revision>-0"), so "<cluster>-0" may not exist.
func autopilotBaseURL(cluster *openbaov1alpha1.OpenBaoCluster) string {
	serviceName := cluster.Name
	if clusterNeedsExternalService(cluster) {
		// When present, the external Service stays stable across rolling and blue/green strategies.
		serviceName = cluster.Name + "-public"
	}

	return fmt.Sprintf("https://%s.%s.svc:%d", serviceName, cluster.Namespace, portAPI)
}

func clusterNeedsExternalService(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if cluster == nil {
		return false
	}

	if cluster.Spec.Service != nil {
		return true
	}
	if cluster.Spec.Ingress != nil && cluster.Spec.Ingress.Enabled {
		return true
	}
	return cluster.Spec.Gateway != nil && cluster.Spec.Gateway.Enabled
}
