//go:build apimigration_e2e

package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Probes write and read through each Cluster's Service throughout migration.
// Custody transfer must cause no failed operations. During a destination rollout,
// HA must recover client operations within ten seconds, allowing brief leader
// election and connection failures. Single-replica downtime is measured separately.
const (
	e2eProbeRole     = "availability-probe"
	e2eHAOutageLimit = 10 * time.Second
)

type availabilityProbe struct {
	name, cluster string
	ha            bool
}

// stepClock records when each procedure step starts, from the "==> step" lines.
type stepClock struct {
	mu      sync.Mutex
	pending []byte
	starts  map[string]time.Time
}

func (c *stepClock) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.starts == nil {
		c.starts = map[string]time.Time{}
	}
	c.pending = append(c.pending, p...)
	for {
		end := bytes.IndexByte(c.pending, '\n')
		if end < 0 {
			return len(p), nil
		}
		line := string(c.pending[:end])
		c.pending = c.pending[end+1:]
		if name, ok := strings.CutPrefix(line, "==> "); ok {
			if _, seen := c.starts[name]; !seen {
				c.starts[name] = time.Now()
			}
		}
	}
}

// snapshot returns when each step started, to place failed probes in the procedure.
func (c *stepClock) snapshot() map[string]time.Time {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.starts)
}

func (c *stepClock) started(name string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts[name]
}

func (f *e2eFixture) startAvailabilityProbes() []availabilityProbe {
	probes := []availabilityProbe{f.startAvailabilityProbe(e2eCluster, f.shape.replicas >= 3)}
	if f.shape.hardened {
		probes = append(probes, f.startAvailabilityProbe(e2eHardened, true))
	}
	return probes
}

// startAvailabilityProbe checks acknowledged writes and a stable readable value.
// Standby reads are eventually consistent, including on settled Clusters, so a
// read need not expose the latest version of the immediately preceding write.
// Credentials are projected and never logged.
func (f *e2eFixture) startAvailabilityProbe(cluster string, ha bool) availabilityProbe {
	f.t.Helper()
	name := fmt.Sprintf("%s-%d", e2eProbeRole, time.Now().UnixNano())
	serverName := cluster + "." + e2eDataNS + ".svc"
	var ca []byte
	var err error
	if cluster == e2eHardened {
		ca, err = os.ReadFile(filepath.Join(f.dir, e2eACMECAFile))
		serverName = e2eACMEDomain
	} else {
		secret := f.getJSON("get", "secret", cluster+"-tls-ca", "-n", e2eDataNS)
		ca, err = base64.StdEncoding.DecodeString(secret["data"].(map[string]any)["ca.crt"].(string))
	}
	require.NoError(f.t, err)
	trust := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": name, "namespace": e2eDataNS}, "data": map[string]any{"ca.crt": string(ca)}}
	data, err := json.Marshal(trust)
	require.NoError(f.t, err)
	f.kubectl(data, "apply", "-f", "-")
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": e2eDataNS, "labels": map[string]any{"role": e2eProbeRole}},
		"spec": map[string]any{
			"restartPolicy": "Never", "serviceAccountName": e2eAdmin, "automountServiceAccountToken": false,
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 100, "runAsGroup": 1000,
				"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{
				"name": "probe", "image": f.images["openbao"], "imagePullPolicy": "IfNotPresent",
				"command": []any{"/bin/sh", "-c", `while true; do
  if [ -z "$BAO_TOKEN" ]; then
    BAO_TOKEN=$(bao write -field=token auth/jwt-operator/login role=fixture-administrator jwt=@/jwt/token 2>/dev/null)
    export BAO_TOKEN
  fi
  stage=login
  result=fail
  if [ -n "$BAO_TOKEN" ]; then
    stage=write
    if bao kv put "migration-data/$PROBE_KEY" value=available >/dev/null 2>/tmp/probe-error; then
      stage=read
      if [ "$(bao kv get -field=value "migration-data/$PROBE_KEY" 2>/tmp/probe-error)" = available ]; then
        result=ok
      fi
    fi
  fi
  if [ "$result" = ok ]; then
    echo "$(date +%s) ok"
  else
    echo "$(date +%s) fail"
    echo "diagnostic stage=$stage"
    cat /tmp/probe-error 2>/dev/null || true
  fi
  sleep 0.5
done`},
				"env": []any{
					map[string]any{"name": "BAO_ADDR", "value": "https://" + cluster + "." + e2eDataNS + ".svc:8200"},
					map[string]any{"name": "BAO_CACERT", "value": "/ca/ca.crt"},
					map[string]any{"name": "BAO_TLS_SERVER_NAME", "value": serverName},
					// A slower request on a busy node is latency, not an outage; a longer stall still fails.
					map[string]any{"name": "BAO_CLIENT_TIMEOUT", "value": "5s"},
					map[string]any{"name": "BAO_MAX_RETRIES", "value": "0"},
					map[string]any{"name": "PROBE_KEY", "value": name},
				},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false,
					"capabilities": map[string]any{"drop": []any{"ALL"}}},
				"volumeMounts": []any{map[string]any{"name": "ca", "mountPath": "/ca", "readOnly": true},
					map[string]any{"name": "jwt", "mountPath": "/jwt", "readOnly": true}},
			}},
			"volumes": []any{
				map[string]any{"name": "ca", "configMap": map[string]any{"name": name}},
				map[string]any{"name": "jwt", "projected": map[string]any{"sources": []any{
					map[string]any{"serviceAccountToken": map[string]any{"audience": "openbao-internal", "expirationSeconds": 3600, "path": "token"}}}}},
			},
		}}
	data, err = json.Marshal(pod)
	require.NoError(f.t, err)
	f.kubectl(data, "apply", "-f", "-")
	f.eventually(cluster+" availability probe to acknowledge writes and read its canary", 3*time.Minute, func() bool {
		return strings.Contains(f.probeLog(name), " ok")
	})
	return availabilityProbe{name: name, cluster: cluster, ha: ha}
}

// kubectlOutput runs kubectl without failing the test.
func (f *e2eFixture) kubectlOutput(args ...string) (string, error) {
	out, err := exec.Command("kubectl", append([]string{"--kubeconfig", f.kubeconfig, "--context", f.context},
		args...)...).Output()
	return string(out), err
}

func (f *e2eFixture) probeLog(name string) string {
	out, _ := f.kubectlOutput("logs", "-n", e2eDataNS, name)
	return out
}

type availabilityResult struct {
	probes, custody, rollout int
	custodyProbes            int
	maxSampleGap             time.Duration
	longestOutage            time.Duration
}

// measureAvailability includes the time from the last successful operation until
// recovery, or until the observation ends if the final operation still failed.
func measureAvailability(log string, start, destination, end time.Time) availabilityResult {
	var result availabilityResult
	lastSuccess, failedSince := start, time.Time{}
	lastSample := start
	scanner := bufio.NewScanner(strings.NewReader(log))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		seconds, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		at := time.Unix(seconds, 0)
		if at.Before(start) || at.After(end) {
			continue
		}
		result.probes++
		result.maxSampleGap = max(result.maxSampleGap, at.Sub(lastSample))
		lastSample = at
		if at.Before(destination.Add(-2 * time.Second)) {
			result.custodyProbes++
		}
		if fields[1] == "ok" {
			if !failedSince.IsZero() {
				result.longestOutage = max(result.longestOutage, at.Sub(failedSince))
			}
			lastSuccess, failedSince = at, time.Time{}
			continue
		}
		if failedSince.IsZero() {
			failedSince = lastSuccess
		}
		// Node timestamps have one-second precision. Keep the existing two-second
		// allowance around destination startup out of the custody-only assertion.
		if at.Before(destination.Add(-2 * time.Second)) {
			result.custody++
		} else {
			result.rollout++
		}
	}
	if !failedSince.IsZero() {
		result.longestOutage = max(result.longestOutage, end.Sub(failedSince))
	}
	result.maxSampleGap = max(result.maxSampleGap, end.Sub(lastSample))
	return result
}

// saveOpenBaoLogs keeps the OpenBao server logs of a failed probe window, to
// place the failure in OpenBao's own timeline. Server logs contain no tokens.
func (f *e2eFixture) saveOpenBaoLogs(probe availabilityProbe, start time.Time) {
	f.t.Helper()
	logs, err := f.kubectlOutput("logs", "-n", e2eDataNS, "-l", "openbao.org/cluster="+probe.cluster,
		"--all-containers", "--prefix", "--timestamps", "--tail=-1", "--since-time="+start.UTC().Format(time.RFC3339))
	if err != nil {
		f.t.Logf("%s OpenBao logs unavailable: %v", probe.cluster, err)
		return
	}
	if *e2eArtifacts != "" {
		dir := filepath.Join(*e2eArtifacts, f.name)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			_ = writePrivate(filepath.Join(dir, probe.name+"-openbao.log"), []byte(logs))
		}
	}
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	f.t.Logf("%s OpenBao logs during the probe window (last %d lines):\n%s", probe.cluster,
		min(len(lines), 200), strings.Join(lines[max(len(lines)-200, 0):], "\n"))
}

func (f *e2eFixture) assertAvailability(probe availabilityProbe, start, destination, end time.Time) {
	f.t.Helper()
	log := f.probeLog(probe.name)
	require.NoError(f.t, writePrivate(filepath.Join(f.dir, probe.name+".log"), []byte(log)))
	f.kubectl(nil, "delete", "pod", "-n", e2eDataNS, probe.name, "--wait=false")
	f.kubectl(nil, "delete", "configmap", "-n", e2eDataNS, probe.name)
	require.False(f.t, destination.IsZero(), "the migration must reach start-destination")
	result := measureAvailability(log, start, destination, end)
	if *e2eArtifacts != "" {
		dir := filepath.Join(*e2eArtifacts, f.name)
		require.NoError(f.t, os.MkdirAll(dir, 0o700))
		require.NoError(f.t, writePrivate(filepath.Join(dir, probe.name+".log"), []byte(log)))
		require.NoError(f.t, writePrivateJSON(filepath.Join(dir, probe.name+".json"), map[string]any{
			"cluster": probe.cluster, "nodeImage": *e2eNodeImageFlag,
			"start": start, "destinationStart": destination, "end": end,
			"probes": result.probes, "custodyProbes": result.custodyProbes,
			"custodyFailures": result.custody, "rolloutFailures": result.rollout,
			"longestOutage": result.longestOutage.String(), "maxSampleGap": result.maxSampleGap.String(),
			"stepStarts": f.steps.snapshot(),
		}))
	}
	if result.custody != 0 || (probe.ha && result.longestOutage > e2eHAOutageLimit) {
		// Probe stderr contains the failed operation and transport error, never
		// credentials. Keep it in the job log even if fixture cleanup runs.
		f.t.Logf("%s failed availability observation:\n%s", probe.cluster, log)
		f.saveOpenBaoLogs(probe, start)
	}
	f.t.Logf("%s availability: %d read/write probes; %d failed during custody; %d failed during rollout; longest outage %s",
		probe.cluster, result.probes, result.custody, result.rollout, result.longestOutage)
	require.Positive(f.t, result.probes, "the probe must have run during migration")
	require.Positive(f.t, result.custodyProbes, "the probe must cover ownership transfer")
	require.LessOrEqual(f.t, result.maxSampleGap, e2eHAOutageLimit, "the probe must keep sampling during migration")
	require.Zero(f.t, result.custody, "client reads and writes must succeed while custody moves")
	if probe.ha {
		require.LessOrEqual(f.t, result.longestOutage, e2eHAOutageLimit, "HA client operations must recover within %s", e2eHAOutageLimit)
	}
}

func TestAvailabilityOutageMeasurement(t *testing.T) {
	start, destination, end := time.Unix(100, 0), time.Unix(110, 0), time.Unix(130, 0)
	for _, tc := range []struct {
		name, log        string
		custody, rollout int
		outage           time.Duration
	}{
		{"healthy", "101 ok\n112 ok\n129 ok\n", 0, 0, 0},
		{"custody failure", "101 ok\n103 fail\n105 ok\n", 1, 0, 4 * time.Second},
		{"rollout recovery", "111 ok\n113 fail\n115 ok\n", 0, 1, 4 * time.Second},
		{"unrecovered outage", "111 ok\n113 fail\n", 0, 1, 19 * time.Second},
		{"ignore outside interval", "99 fail\n101 ok\n131 fail\n", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := measureAvailability(tc.log, start, destination, end)
			require.Equal(t, tc.custody, got.custody)
			require.Equal(t, tc.rollout, got.rollout)
			require.Equal(t, tc.outage, got.longestOutage)
		})
	}
}

func TestAvailabilityDetectsMissingSamples(t *testing.T) {
	result := measureAvailability("101 ok\n129 ok\n", time.Unix(100, 0), time.Unix(110, 0), time.Unix(130, 0))
	require.Equal(t, 28*time.Second, result.maxSampleGap)
	require.Equal(t, 1, result.custodyProbes)
}
