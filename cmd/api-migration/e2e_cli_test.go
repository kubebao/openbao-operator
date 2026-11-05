//go:build apimigration_e2e

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

const e2eCLIRelease = "0.7.0"

// rehearseReleaseCLI executes the packaged native binary, including its release
// guard, process signals, OS lock cleanup, and recovery from accepted API writes
// whose responses never reached the process. No fault seam runs inside the CLI.
func (f *e2eFixture) rehearseReleaseCLI() {
	f.t.Helper()
	f.createSecondCluster()
	binary := f.packageCLI()
	proxy, config := f.cliConfig()

	// The release guard must reject a real chart before changing the source.
	f.setCLIChartVersion(config, "0.7.1")
	out, err := f.cli(binary, "plan", f.newState("wrong-release"), "--config", config)
	requireCLIExit(f, err, 1, out)
	require.Contains(f.t, out, "this api-migration binary migrates to release 0.7.0")
	f.setCLIChartVersion(config, e2eCLIRelease)

	state := f.newState("cli-rollback")
	f.waitSettled()
	f.cliOK(binary, "plan", state, "--config", config)
	out, err = f.cli(binary, "migrate", state, "--stop-after", "preflight")
	requireCLIExit(f, err, 3, out)
	pod := f.dataPod()
	setPath := "/apis/apps/v1/namespaces/" + e2eDataNS + "/statefulsets"
	f.interruptCLI(binary, "migrate", state, proxy.arm(http.MethodDelete, setPath+"/"+f.voterSet), syscall.SIGKILL)
	f.eventually("the accepted StatefulSet deletion to orphan its Pods", 2*time.Minute, func() bool {
		out, err := f.kubectlOutput("get", "statefulset", f.voterSet, "-n", e2eDataNS, "--ignore-not-found", "-o", "name")
		return err == nil && strings.TrimSpace(out) == ""
	})
	require.Equal(f.t, pod, f.dataPod(), "orphaning must preserve the running Pod")
	f.cliOK(binary, "rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "cli-rollback")
	f.assertSecond(primitivesSourceGroup, "cli-rollback")

	state = f.newState("cli-commit")
	f.waitSettled()
	f.cliOK(binary, "plan", state, "--config", config)
	pod = f.dataPod()
	f.interruptCLI(binary, "migrate", state, proxy.arm(http.MethodPost, setPath), syscall.SIGTERM)
	require.Equal(f.t, pod, f.dataPod(), "the accepted recreation must preserve the running Pod")
	f.cliOK(binary, "migrate", state)
	f.assertData("before")
	f.writeData("during-cli", e2eDestination)

	// Lose the first retirement acknowledgement. Rollback must refuse even
	// though the process did not record the completed deletion in its plan.
	sourcePath := "/apis/" + primitivesSourceGroup + "/v1alpha1/namespaces/" + e2eDataNS + "/openbaoclusters/" + e2eCluster
	f.interruptCLI(binary, "commit", state, proxy.arm(http.MethodDelete, sourcePath), syscall.SIGKILL)
	out, err = f.cli(binary, "rollback", state)
	requireCLIExit(f, err, 1, out)
	require.True(f.t, strings.Contains(out, "was retired") || strings.Contains(out, "being deleted"), "%s", out)
	f.cliOK(binary, "commit", state)
	f.assertSurvivor("kubebao.org", "cli-commit")
	f.assertSecond("kubebao.org", "cli-commit")
}

// rehearseManifestCLI checks the executable's checkpoint exit status against a
// real rendered-chart installation. Every rerun reads its persisted plan; the
// test applies the requested release state without changing that plan.
func (f *e2eFixture) rehearseManifestCLI() {
	f.t.Helper()
	binary := f.packageCLI()
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	f.destinationChart = filepath.Join(f.dir, "cli-manifest-chart")
	f.destinationVersion = e2eCLIRelease
	f.cmd("cp", nil, "-R", filepath.Join(root, "charts/openbao-operator"), f.destinationChart)
	f.setCLIChartRelease(f.destinationChart, e2eCLIRelease)
	config := f.configFile()
	data, err := os.ReadFile(config)
	require.NoError(f.t, err)
	var input Config
	require.NoError(f.t, json.Unmarshal(data, &input))
	require.Empty(f.t, input.Release, "this rehearsal must not use a Helm release")
	require.Empty(f.t, input.DestinationChart)
	require.Empty(f.t, input.DestinationValues)

	state := f.newState("cli-manifests-rollback")
	f.waitSettled()
	f.cliOK(binary, "plan", state, "--config", config)
	f.manifestCLI(binary, "migrate", state, stateStopped.name, stateHeld.name)
	f.assertData("before")
	f.manifestCLI(binary, "rollback", state, stateSource.name)
	f.assertSurvivor(primitivesSourceGroup, "cli-manifests-rollback")

	state = f.newState("cli-manifests-commit")
	f.waitSettled()
	f.cliOK(binary, "plan", state, "--config", config)
	f.manifestCLI(binary, "migrate", state, stateStopped.name, stateHeld.name)
	f.writeData("during-manifest-cli", e2eDestination)
	f.manifestCLI(binary, "commit", state, stateNormal.name)
	f.assertSurvivor("kubebao.org", "cli-manifests-commit")
}

func (f *e2eFixture) manifestCLI(binary, command, state string, checkpoints ...string) {
	f.t.Helper()
	for _, want := range checkpoints {
		out, err := f.cli(binary, command, state)
		requireCLIExit(f, err, 4, out)
		data, err := os.ReadFile(filepath.Join(state, planFile))
		require.NoError(f.t, err)
		var plan Plan
		require.NoError(f.t, json.Unmarshal(data, &plan))
		require.Equal(f.t, want, plan.Operator.Checkpoint, "%s checkpoint", command)
		require.Contains(f.t, out, "waiting for the "+want+" operator state")
		_, err = os.Stat(filepath.Join(state, "checkpoints", want+".md"))
		require.NoError(f.t, err, "the checkpoint must write its application instructions")
		f.applyOperatorState(want)
	}
	f.cliOK(binary, command, state)
}

func (f *e2eFixture) packageCLI() string {
	f.t.Helper()
	root := strings.TrimSpace(f.cmd("git", nil, "rev-parse", "--show-toplevel"))
	dir := filepath.Join(f.dir, "release-binaries")
	f.cmd("bash", nil, filepath.Join(root, "hack/ci/build-api-migration.sh"), dir, e2eCLIRelease)
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		data, err := os.ReadFile(filepath.Join(dir, "api-migration-"+platform))
		require.NoError(f.t, err)
		f.t.Logf("packaged api-migration-%s sha256:%x", platform, sha256.Sum256(data))
	}
	binary := filepath.Join(dir, "api-migration-"+runtime.GOOS+"-"+runtime.GOARCH)
	cmd := exec.Command(binary, "version")
	out, err := cmd.CombinedOutput()
	require.NoError(f.t, err, "%s", out)
	require.Equal(f.t, e2eCLIRelease, strings.TrimSpace(string(out)))
	return binary
}

// cliConfig copies the chart and values into the fixture. Versioning this test
// candidate does not change the shared worktree or the published source release.
func (f *e2eFixture) cliConfig() (*cliAPIProxy, string) {
	f.t.Helper()
	data, err := os.ReadFile(f.configFile())
	require.NoError(f.t, err)
	var config Config
	require.NoError(f.t, json.Unmarshal(data, &config))
	chart := filepath.Join(f.dir, "cli-destination-chart")
	f.cmd("cp", nil, "-R", config.DestinationChart, chart)
	config.DestinationChart = chart
	data, err = os.ReadFile(config.DestinationValues)
	require.NoError(f.t, err)
	var values map[string]any
	require.NoError(f.t, json.Unmarshal(data, &values))
	values["operatorVersion"] = e2eCLIRelease
	config.DestinationValues = filepath.Join(f.dir, "cli-destination-values.json")
	require.NoError(f.t, writePrivateJSON(config.DestinationValues, values))
	proxy, kubeconfig := f.cliProxy()
	config.Kubeconfig = kubeconfig
	path := filepath.Join(f.dir, "cli-config.json")
	require.NoError(f.t, writePrivateJSON(path, config))
	return proxy, path
}

func (f *e2eFixture) setCLIChartVersion(configPath, version string) {
	f.t.Helper()
	data, err := os.ReadFile(configPath)
	require.NoError(f.t, err)
	var config Config
	require.NoError(f.t, json.Unmarshal(data, &config))
	f.setCLIChartRelease(config.DestinationChart, version)
}

func (f *e2eFixture) setCLIChartRelease(chartDir, version string) {
	f.t.Helper()
	path := filepath.Join(chartDir, "Chart.yaml")
	data, err := os.ReadFile(path)
	require.NoError(f.t, err)
	var chart map[string]any
	require.NoError(f.t, yaml.Unmarshal(data, &chart))
	chart["version"], chart["appVersion"] = version, version
	data, err = yaml.Marshal(chart)
	require.NoError(f.t, err)
	require.NoError(f.t, writePrivate(path, data))
}

func cliCommand(binary, command, state string, extra ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	args := append([]string{command, "--state", state, "--timeout", "8m"}, extra...)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A timeout must also stop kubectl/helm processes the CLI started.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd, cancel
}

func (f *e2eFixture) cli(binary, command, state string, extra ...string) (string, error) {
	f.t.Helper()
	cmd, cancel := cliCommand(binary, command, state, extra...)
	defer cancel()
	out, err := cmd.CombinedOutput()
	f.t.Logf("release CLI %s:\n%s", command, out)
	return string(out), err
}

func (f *e2eFixture) cliOK(binary, command, state string, extra ...string) {
	f.t.Helper()
	out, err := f.cli(binary, command, state, extra...)
	require.NoError(f.t, err, "%s", out)
}

func requireCLIExit(f *e2eFixture, err error, code int, out string) {
	f.t.Helper()
	var exited *exec.ExitError
	require.ErrorAs(f.t, err, &exited, "%s", out)
	require.Equal(f.t, code, exited.ExitCode(), "%s", out)
}

func (f *e2eFixture) interruptCLI(binary, command, state string, trap *cliResponseTrap, signal syscall.Signal) {
	f.t.Helper()
	defer close(trap.release)
	cmd, cancel := cliCommand(binary, command, state)
	defer cancel()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(f.t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	select {
	case <-trap.accepted:
	case err := <-done:
		f.t.Fatalf("CLI exited before the selected API write: %v\n%s", err, out.String())
	case <-time.After(9 * time.Minute):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		f.t.Fatalf("CLI did not reach the selected API write\n%s", out.String())
	}
	// A second executable must see the first process's OS-level lock.
	locked, err := f.cli(binary, "status", state)
	requireCLIExit(f, err, 1, locked)
	require.Contains(f.t, locked, "another api-migration process holds this state directory")
	require.NoError(f.t, syscall.Kill(-cmd.Process.Pid, signal))
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		f.t.Fatalf("CLI did not exit after %s\n%s", signal, out.String())
	}
	f.t.Logf("release CLI %s interrupted with %s after accepted %s %s:\n%s", command, signal, trap.method, trap.path, out.String())
	if signal == syscall.SIGTERM {
		requireCLIExit(f, err, 1, out.String())
	} else {
		var exited *exec.ExitError
		require.ErrorAs(f.t, err, &exited, "%s", out.String())
		status, ok := exited.Sys().(syscall.WaitStatus)
		require.True(f.t, ok)
		require.Equal(f.t, signal, status.Signal())
	}
	// A new executable must reacquire the lock and decode the durable plan.
	f.cliOK(binary, "status", state)
}

type cliResponseTrap struct {
	method   string
	path     string
	accepted chan struct{}
	release  chan struct{}
}

type cliAPIProxy struct {
	mu   sync.Mutex
	trap *cliResponseTrap
}

func (p *cliAPIProxy) arm(method, path string) *cliResponseTrap {
	p.mu.Lock()
	defer p.mu.Unlock()
	trap := &cliResponseTrap{method: method, path: path, accepted: make(chan struct{}), release: make(chan struct{})}
	p.trap = trap
	return trap
}

func (p *cliAPIProxy) withhold(response *http.Response) error {
	p.mu.Lock()
	trap := p.trap
	if trap == nil || response.StatusCode < 200 || response.StatusCode >= 300 ||
		response.Request.Method != trap.method || response.Request.URL.Path != trap.path {
		p.mu.Unlock()
		return nil
	}
	p.trap = nil
	p.mu.Unlock()
	close(trap.accepted)
	select {
	case <-trap.release:
		return nil
	case <-response.Request.Context().Done():
		return response.Request.Context().Err()
	}
}

// cliProxy authenticates to the real API server and preserves the CLI's
// impersonation headers. Only responses to selected, already-accepted writes
// are withheld; production code and the API server run unchanged.
func (f *e2eFixture) cliProxy() (*cliAPIProxy, string) {
	f.t.Helper()
	raw, err := clientcmd.LoadFromFile(f.kubeconfig)
	require.NoError(f.t, err)
	config, err := clientcmd.NewNonInteractiveClientConfig(*raw, f.context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	require.NoError(f.t, err)
	// kubectl port-forward upgrades the connection; keep the upstream hop on
	// HTTP/1.1 so it carries SPDY and WebSocket upgrades unchanged.
	config.NextProtos = []string{"http/1.1"}
	transport, err := rest.TransportFor(config)
	require.NoError(f.t, err)
	target, err := url.Parse(config.Host)
	require.NoError(f.t, err)
	proxy := &cliAPIProxy{}
	reverse := httputil.NewSingleHostReverseProxy(target)
	reverse.Transport = transport
	reverse.ModifyResponse = proxy.withhold
	reverse.ErrorLog = log.New(io.Discard, "", 0)
	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(f.t, err)
	token := base64.RawURLEncoding.EncodeToString(secret)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// The local token authorizes this proxy only; upstream authentication
		// uses the fixture administrator's transport and retains impersonation.
		req.Header.Del("Authorization")
		reverse.ServeHTTP(w, req)
	}))
	f.t.Cleanup(server.Close)
	local := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{"proxy": {Server: server.URL,
			CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"proxy": {Token: token}},
		Contexts:       map[string]*clientcmdapi.Context{f.context: {Cluster: "proxy", AuthInfo: "proxy"}},
		CurrentContext: f.context,
	}
	data, err := clientcmd.Write(local)
	require.NoError(f.t, err)
	path := filepath.Join(f.dir, "cli-kubeconfig")
	require.NoError(f.t, writePrivate(path, data))
	return proxy, path
}
