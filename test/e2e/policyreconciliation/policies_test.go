//go:build e2e

package policyreconciliation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	configbuilder "github.com/kubebao/openbao-operator/internal/adapter/config"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
)

// TestApprovedPolicyRepair exercises the production client and policy manager against
// a real OpenBao process. The disposable dev server is reachable only on loopback.
func TestApprovedPolicyRepair(t *testing.T) {
	address, root := startOpenBao(t)
	cluster := &api.OpenBaoCluster{}
	cluster.Spec.ReconcilePolicies, cluster.Status.Initialized = true, true
	cluster.Spec.Backup = &api.BackupSchedule{}
	admin, err := openbao.NewClient(portopenbao.ClientConfig{BaseURL: address, Token: root})
	require.NoError(t, err)
	require.NoError(t, admin.WriteACLPolicy(t.Context(), portauth.PolicyNameApproval, configbuilder.OperatorPolicyApproval(cluster)))
	code, body := request(t, address, root, http.MethodPost, "auth/token/create", map[string]any{
		"policies": []string{portauth.PolicyNameApproval}, "no_default_policy": true, "ttl": "5m",
	})
	require.Equal(t, http.StatusOK, code)
	var issued struct {
		Auth struct {
			Token string `json:"client_token"`
		} `json:"auth"`
	}
	require.NoError(t, json.Unmarshal(body, &issued))
	require.NotEmpty(t, issued.Auth.Token)
	operator, err := openbao.NewClient(portopenbao.ClientConfig{BaseURL: address, Token: issued.Auth.Token})
	require.NoError(t, err)
	store := &countingClient{PolicyClient: operator}
	manager := &configuration.PolicyManager{ClientFor: func(context.Context, *api.OpenBaoCluster) (portopenbao.PolicyClient, error) { return store, nil }}
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, 4, store.writes, "approved policies can be created")

	for _, tc := range []struct {
		name, path string
		data       map[string]any
	}{
		{"unapproved contents", "sys/policies/acl/openbao-operator", map[string]any{"policy": `path "*" { capabilities = ["sudo"] }`}},
		{"unapproved upgrade contents", "sys/policies/acl/openbao-operator-upgrade", map[string]any{"policy": `path "*" { capabilities = ["sudo"] }`}},
		{"extra key", "sys/policies/acl/openbao-operator", map[string]any{"policy": configbuilder.OperatorPolicies(cluster)[0].Policy, "cas_required": false}},
		{"legacy path", "sys/policy/openbao-operator", map[string]any{"policy": configbuilder.OperatorPolicies(cluster)[0].Policy}},
		{"other policy", "sys/policies/acl/application", map[string]any{"policy": configbuilder.OperatorPolicies(cluster)[0].Policy}},
		{"approval policy", "sys/policies/acl/" + portauth.PolicyNameApproval, map[string]any{"policy": configbuilder.OperatorPolicyApproval(cluster)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := request(t, address, issued.Auth.Token, http.MethodPut, tc.path, tc.data)
			require.Equal(t, http.StatusForbidden, status)
		})
	}
	store.writes = 0
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Zero(t, store.writes, "matching policies must not be rewritten")

	code, _ = request(t, address, root, http.MethodDelete, "sys/policies/acl/"+portauth.PolicyNameBackup, nil)
	require.Equal(t, http.StatusNoContent, code)
	missing, err := admin.ReadACLPolicy(t.Context(), portauth.PolicyNameBackup)
	require.NoError(t, err)
	require.Nil(t, missing)
	cluster.Status.Workload.PolicyReconciliation.LastVerified.Time = time.Now().Add(-6 * time.Minute)
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, 1, store.writes)
	for _, policy := range configbuilder.OperatorPolicies(cluster) {
		actual, err := admin.ReadACLPolicy(t.Context(), policy.Name)
		require.NoError(t, err)
		require.NotNil(t, actual)
		require.Equal(t, policy.Policy, *actual)
	}

	approval := configbuilder.OperatorPolicyApproval(cluster)
	for _, strategy := range []api.UpdateStrategyType{api.UpdateStrategyBlueGreen, api.UpdateStrategyRollingUpdate} {
		cluster.Spec.Upgrade = &api.UpgradeConfig{Strategy: strategy}
		require.Equal(t, approval, configbuilder.OperatorPolicyApproval(cluster))
		require.Error(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameUpgrade), "new operations wait for the selected variant")
		store.writes = 0
		_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
		require.NoError(t, err, "either strategy is approved without administrator intervention")
		require.Equal(t, 1, store.writes, "a strategy change bypasses the successful verification interval")
		require.NoError(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
		for _, policy := range configbuilder.OperatorPolicies(cluster) {
			if policy.Name != portauth.PolicyNameUpgrade {
				continue
			}
			actual, err := admin.ReadACLPolicy(t.Context(), policy.Name)
			require.NoError(t, err)
			require.NotNil(t, actual)
			require.Equal(t, policy.Policy, *actual, "install only the selected strategy's permissions")
			status, _ := request(t, address, issued.Auth.Token, http.MethodPut, "sys/policies/acl/"+policy.Name,
				map[string]any{"policy": policy.Policy + "\n"})
			require.Equal(t, http.StatusForbidden, status, "approval still requires exact contents")
		}
		code, _ = request(t, address, root, http.MethodDelete, "sys/policies/acl/"+portauth.PolicyNameUpgrade, nil)
		require.Equal(t, http.StatusNoContent, code)
		cluster.Status.Workload.PolicyReconciliation.LastVerified.Time = time.Now().Add(-6 * time.Minute)
		_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
		require.NoError(t, err)
		require.Equal(t, 2, store.writes, "either approved variant can be recreated after deletion")
	}

	code, _ = request(t, address, root, http.MethodDelete, "sys/policies/acl/"+portauth.PolicyNameApproval, nil)
	require.Equal(t, http.StatusNoContent, code)
	cluster.Spec.Upgrade = &api.UpgradeConfig{Strategy: api.UpdateStrategyBlueGreen}
	result, err := manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.Error(t, err, "revoked approval cannot authorize changed upgrade permissions")
	require.Equal(t, 5*time.Minute, result.RequeueAfter)
	require.Error(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameUpgrade))
	require.NoError(t, configuration.RequirePolicyReady(cluster, portauth.PolicyNameBackup))
	writes := store.writes
	_, err = manager.Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	require.Equal(t, writes, store.writes, "denied requests respect the cooldown")
}

type countingClient struct {
	portopenbao.PolicyClient
	writes int
}

func (c *countingClient) WriteACLPolicy(ctx context.Context, name, policy string) error {
	c.writes++
	return c.PolicyClient.WriteACLPolicy(ctx, name, policy)
}

func startOpenBao(t *testing.T) (string, string) {
	t.Helper()
	image := os.Getenv("POLICY_TEST_OPENBAO_IMAGE")
	if image == "" {
		image = "openbao/openbao:2.6.5"
	}
	root := uuid.NewString()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-d", "-p", "127.0.0.1::8200",
		"-e", "BAO_DEV_ROOT_TOKEN_ID", "-e", "SKIP_SETCAP=true", image,
		"server", "-dev", "-dev-listen-address=0.0.0.0:8200")
	cmd.Env = append(os.Environ(), "BAO_DEV_ROOT_TOKEN_ID="+root)
	output, err := cmd.Output()
	require.NoError(t, err)
	id := strings.TrimSpace(string(output))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
			t.Errorf("remove test container: %v", err)
		}
	})
	output, err = exec.CommandContext(ctx, "docker", "port", id, "8200/tcp").Output()
	require.NoError(t, err)
	address := "http://" + strings.TrimSpace(string(output))
	client := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		response, err := client.Get(address + "/v1/sys/health")
		if err != nil {
			return false
		}
		defer func() { _ = response.Body.Close() }()
		return response.StatusCode == http.StatusOK
	}, 30*time.Second, 200*time.Millisecond)
	return address, root
}

func request(t *testing.T, address, token, method, path string, data any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if data != nil {
		body, err := json.Marshal(data)
		require.NoError(t, err)
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, address+"/v1/"+path, reader)
	require.NoError(t, err)
	req.Header.Set("X-Vault-Token", token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	require.NoError(t, err, fmt.Sprintf("read response for %s", path))
	return response.StatusCode, body
}
