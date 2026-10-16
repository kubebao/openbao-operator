//go:build e2e

package tlskeyexchange

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	configbuilder "github.com/kubebao/openbao-operator/internal/adapter/config"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/adapter/probe"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/certs"
)

// TestHybridPQKeyExchange uses operator-issued ECDSA certificates and rendered
// listener settings against the released OpenBao server on an isolated network.
func TestHybridPQKeyExchange(t *testing.T) {
	cluster, files, ca := prepareCluster(t)
	network := "pq-tls-" + uuid.NewString()
	docker(t, "network", "create", network)
	t.Cleanup(func() { docker(t, "network", "rm", network) })
	nodes := make([]node, 3)
	for i := range nodes {
		nodes[i] = startNode(t, cluster, files, network, i)
	}
	clientConfig := portopenbao.ClientConfig{
		BaseURL: nodes[0].address, CACert: ca, TLSServerName: portopenbao.ComputeTLSServerName(cluster),
	}
	bootstrap, err := openbao.NewClient(clientConfig)
	require.NoError(t, err)
	waitForHealth(t, bootstrap, false)
	initialized, err := bootstrap.Init(t.Context(), openbao.InitRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, initialized.RootToken)
	clientConfig.Token = initialized.RootToken
	admin, err := openbao.NewClient(clientConfig)
	require.NoError(t, err)
	waitForHealth(t, admin, true)
	for i := 1; i < len(nodes); i++ {
		followerConfig := clientConfig
		followerConfig.BaseURL = nodes[i].address
		follower, err := openbao.NewClient(followerConfig)
		require.NoError(t, err)
		waitForHealth(t, follower, true)
	}
	require.Eventually(t, func() bool {
		configuration, err := admin.ReadRaftConfiguration(t.Context())
		return err == nil && len(configuration.Config.Servers) == 3
	}, time.Minute, 200*time.Millisecond, "three nodes must join with the configured listener TLS policy")

	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(ca))
	for _, group := range []tls.CurveID{tls.X25519MLKEM768, tls.SecP256r1MLKEM768, tls.SecP384r1MLKEM1024} {
		t.Run(group.String(), func(t *testing.T) {
			for _, address := range []string{nodes[0].address, nodes[0].metricsAddress} {
				conn := connect(t, address, &tls.Config{
					RootCAs: roots, ServerName: clientConfig.TLSServerName, MinVersion: tls.VersionTLS13,
					CurvePreferences: []tls.CurveID{group},
				})
				state := conn.ConnectionState()
				require.Equal(t, uint16(tls.VersionTLS13), state.Version)
				require.Equal(t, group, state.CurveID)
				require.Equal(t, x509.ECDSA, state.PeerCertificates[0].PublicKeyAlgorithm)
				require.NotEmpty(t, state.VerifiedChains)
				t.Logf("%s: TLS 1.3, group=%s, certificate=ECDSA", address, state.CurveID)
				require.NoError(t, conn.Close())
			}
		})
	}
	for _, address := range []string{nodes[0].address, nodes[0].metricsAddress} {
		for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
			dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{
				RootCAs: roots, ServerName: clientConfig.TLSServerName, MinVersion: version, MaxVersion: version,
				CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384, tls.CurveP521},
			}}
			conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(address, "https://"))
			if conn != nil {
				_ = conn.Close()
			}
			require.Error(t, err, "classical-only TLS %x must fail on %s", version, address)
		}
	}
	prober, err := probe.NewProber(probe.ProberConfig{
		Addr: nodes[0].address, CAFile: filepath.Join(files, "tls", "ca.crt"), ServerName: clientConfig.TLSServerName,
		StartupPath:   "/v1/sys/health?standbyok=true&perfstandbyok=true&sealedcode=204&uninitcode=204",
		LivenessPath:  "/v1/sys/health?standbyok=true&perfstandbyok=true&sealedcode=204&uninitcode=204",
		ReadinessPath: "/v1/sys/health?standbyok=true&perfstandbyok=true",
	})
	require.NoError(t, err)
	require.NoError(t, prober.CheckStartup(t.Context()))
	require.NoError(t, prober.CheckLiveness(t.Context()))
	require.NoError(t, prober.CheckReadiness(t.Context()))

	response := request(t, nodes[0].address, clientConfig, http.MethodPost, "sys/mounts/pq-test", []byte(`{"type":"kv"}`))
	require.Equal(t, http.StatusNoContent, response.status)
	response = request(t, nodes[0].address, clientConfig, http.MethodPost, "pq-test/value", []byte(`{"value":"before"}`))
	require.Equal(t, http.StatusNoContent, response.status)
	var snapshot bytes.Buffer
	require.NoError(t, admin.Snapshot(t.Context(), &snapshot))
	require.NotZero(t, snapshot.Len())
	response = request(t, nodes[0].address, clientConfig, http.MethodPost, "pq-test/value", []byte(`{"value":"after"}`))
	require.Equal(t, http.StatusNoContent, response.status)
	require.NoError(t, admin.Restore(t.Context(), bytes.NewReader(snapshot.Bytes()), portopenbao.RestoreOptions{}))
	require.Eventually(t, func() bool {
		result := request(t, nodes[0].address, clientConfig, http.MethodGet, "pq-test/value", nil)
		return bytes.Contains(result.body, []byte(`"before"`))
	}, time.Minute, 200*time.Millisecond, "snapshot restore must preserve the value through enforced hybrid TLS")
	docker(t, "restart", nodes[2].id)
	followerConfig := clientConfig
	followerConfig.BaseURL = "https://" + docker(t, "port", nodes[2].id, "8200/tcp")
	follower, err := openbao.NewClient(followerConfig)
	require.NoError(t, err)
	waitForHealth(t, follower, true)
}

type node struct{ id, address, metricsAddress string }

func prepareCluster(t *testing.T) (*api.OpenBaoCluster, string, []byte) {
	t.Helper()
	cluster := &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "pq-tls", Namespace: "default", UID: types.UID(uuid.NewString())},
		Spec: api.OpenBaoClusterSpec{
			Version: "2.7.1", Replicas: 3, Profile: api.ProfileDevelopment,
			TLS: api.TLSConfig{Enabled: true, RotationPeriod: "720h"},
			Configuration: &api.OpenBaoConfiguration{Listener: &api.ListenerConfig{
				TLSMinVersion: api.TLSVersion13, TLSMaxVersion: api.TLSVersion13,
				TLSKeyExchangePreferences: []api.TLSKeyExchangeGroup{
					api.TLSKeyExchangeX25519MLKEM768, api.TLSKeyExchangeSecP256r1MLKEM768, api.TLSKeyExchangeSecP384r1MLKEM1024,
				},
			}},
			Observability: &api.ObservabilityConfig{Metrics: &api.MetricsConfig{Enabled: true, ScrapeProfile: "AllNodes"}},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	_, err := certs.NewManagerWithReloader(kube, scheme, nil).Reconcile(t.Context(), logr.Discard(), cluster)
	require.NoError(t, err)
	ca, server := &corev1.Secret{}, &corev1.Secret{}
	require.NoError(t, kube.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "pq-tls-tls-ca"}, ca))
	require.NoError(t, kube.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "pq-tls-tls-server"}, server))
	dir := t.TempDir()
	for _, subdir := range []string{"tls", "unseal"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, subdir), 0700))
	}
	for filename, data := range map[string][]byte{"ca.crt": ca.Data["ca.crt"], "tls.crt": server.Data["tls.crt"], "tls.key": server.Data["tls.key"]} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "tls", filename), data, 0600))
	}
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unseal", "key"), key, 0600))
	return cluster, dir, ca.Data["ca.crt"]
}

func startNode(t *testing.T, cluster *api.OpenBaoCluster, files, network string, index int) node {
	t.Helper()
	name := fmt.Sprintf("pq-tls-%d", index)
	fqdn := name + ".pq-tls.default.svc"
	rendered, err := configbuilder.RenderHCL(cluster, configbuilder.InfrastructureDetails{
		HeadlessServiceName: cluster.Name, Namespace: cluster.Namespace, APIPort: 8200, ClusterPort: 8201,
	})
	require.NoError(t, err)
	// Replace Kubernetes discovery with the test network's leader address and
	// remove Kubernetes service registration. Keep
	// the rendered listeners, seal and retry-join certificate settings intact.
	config, diagnostics := hclwrite.ParseConfig(rendered, "config.hcl", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	for _, block := range config.Body().Blocks() {
		if block.Type() == "service_registration" {
			config.Body().RemoveBlock(block)
			continue
		}
		if block.Type() != "storage" {
			continue
		}
		for _, join := range block.Body().Blocks() {
			if index == 0 {
				block.Body().RemoveBlock(join)
				continue
			}
			join.Body().RemoveAttribute("auto_join")
			join.Body().SetAttributeValue("leader_api_addr", cty.StringVal("https://pq-tls-0.pq-tls.default.svc:8200"))
		}
	}
	configPath := filepath.Join(files, name+".hcl")
	require.NoError(t, os.WriteFile(configPath, []byte(strings.ReplaceAll(string(config.Bytes()), "$${HOSTNAME}", name)), 0600))
	dataPath := t.TempDir()
	image := os.Getenv("PQ_TLS_TEST_OPENBAO_IMAGE")
	if image == "" {
		image = "openbao/openbao:2.7.1"
	}
	// Run as the fixture owner and bypass the image entrypoint's user change so
	// the server can read the mounted private files without changing permissions.
	fixtureUser := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	id := docker(t, "run", "-d", "--user", fixtureUser, "--entrypoint", "bao", "--name", network+"-"+name, "--hostname", name,
		"--network", network, "--network-alias", fqdn, "-p", "127.0.0.1::8200", "-p", "127.0.0.1::8202",
		"-v", files+":/etc/bao:ro", "-v", dataPath+":/bao/data",
		image, "server", "-config=/etc/bao/"+name+".hcl")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("OpenBao %s logs:\n%s", name, docker(t, "logs", "--tail", "80", id))
		}
		docker(t, "rm", "-f", id)
	})
	return node{id: id, address: "https://" + docker(t, "port", id, "8200/tcp"),
		metricsAddress: "https://" + docker(t, "port", id, "8202/tcp")}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "docker %v: %s", args, output)
	return strings.TrimSpace(string(output))
}

func waitForHealth(t *testing.T, client *openbao.Client, initialized bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		health, err := client.Health(t.Context())
		return err == nil && (!initialized || (health.Initialized && !health.Sealed))
	}, time.Minute, 200*time.Millisecond)
}

func connect(t *testing.T, address string, config *tls.Config) *tls.Conn {
	t.Helper()
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: config}
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(address, "https://"))
	require.NoError(t, err)
	return conn.(*tls.Conn)
}

type response struct {
	status int
	body   []byte
}

func request(t *testing.T, address string, config portopenbao.ClientConfig, method, path string, data []byte) response {
	t.Helper()
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(config.CACert))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: config.TLSServerName, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), method, address+"/v1/"+path, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("X-Vault-Token", config.Token)
	req.Header.Set("Content-Type", "application/json")
	result, err := client.Do(req)
	if err != nil {
		return response{}
	}
	defer func() { _ = result.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(result.Body, 1024*1024))
	require.NoError(t, err)
	if result.StatusCode >= 400 {
		t.Logf("%s returned status %d", path, result.StatusCode)
	}
	return response{status: result.StatusCode, body: body}
}
