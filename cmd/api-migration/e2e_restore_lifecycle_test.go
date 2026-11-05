//go:build apimigration_e2e

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"

	adapterauth "github.com/kubebao/openbao-operator/internal/adapter/auth"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	e2eRetainedTarget = "recovered"
	e2eRetainedRecord = "retained-before-migration"
	e2eSnapshotSeal   = "snapshot-static-seal"
)

// migrateRestoreLifecycle starts with a completed retained-target handoff on
// 0.6.1. Both Clusters and the finished restore record must survive rollback
// and commit. The destination then schedules and completes a disposable restore
// from a new backup, including cleanup of the generated target and its claim.
func (f *e2eFixture) migrateRestoreLifecycle() {
	f.t.Helper()
	count := f.backup(primitivesSourceGroup, 0)
	f.prepareRestoreDestination()
	template := f.freshRestoreTemplate()
	source := f.restoreSnapshotSource()
	f.createRetainedRestore(template, source)
	f.writeNamedRestoreData(e2eRetainedTarget, "retained-handoff", "retained")
	retained := f.clusterNamed(primitivesSourceGroup, e2eRetainedTarget)
	identity := retained.GetUID()
	claim := f.getJSON("get", "pvc", "data-"+e2eRetainedTarget+"-0", "-n", e2eDataNS)
	claimUID := claim["metadata"].(map[string]any)["uid"]
	job := f.restoreRecord(primitivesSourceGroup)["status"].(map[string]any)["execution"].(map[string]any)["jobName"].(string)
	duringWritten := false
	assertSurvivor := func(group string) {
		f.assertRetainedRestore(group, string(identity), job, claimUID, source["expectedClusterID"])
		f.assertNamedRestoreData(e2eRetainedTarget, "before", "source")
		f.assertNamedRestoreData(e2eRetainedTarget, "retained-handoff", "retained")
		if duringWritten {
			f.assertNamedRestoreData(e2eRetainedTarget, "retained-during-migration", e2eDestination)
		}
	}
	assertSurvivor(primitivesSourceGroup)
	// A CR-level pin overrides Helm helper values. Refuse it before stopping
	// either controller, then use the release-level source/destination helpers.
	setPin := func(image string) {
		patch, err := json.Marshal(map[string]any{"spec": map[string]any{"backup": map[string]any{"image": image}}})
		require.NoError(f.t, err)
		f.kubectl(nil, "patch", "openbaoclusters."+primitivesSourceGroup, e2eCluster, "-n", e2eDataNS, "--type=merge", "-p", string(patch))
		f.waitSettled()
	}
	setPin(f.images["backup"])
	require.ErrorContains(f.t, f.run("plan", f.newState("refused-backup-pin"), "--config", f.configFile()), "spec.backup.image")
	setPin("")

	state := f.newState("retained-rollback")
	f.plan(state)
	f.migrate(state)
	f.writeNamedRestoreData(e2eRetainedTarget, "retained-during-migration", e2eDestination)
	duringWritten = true
	assertSurvivor(e2eDestinationGroup)
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "retained-rollback")
	assertSurvivor(primitivesSourceGroup)
	f.migrateAndCommit(f.newState("retained-commit"))
	assertSurvivor(e2eDestinationGroup)

	f.writeData("after-retained-migration", e2eDestination)
	// The threshold excludes the existing source backup. Only a new successful
	// destination backup may start this operator-scheduled restore test.
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"backup": map[string]any{
		"restoreTest": map[string]any{"everySuccessfulBackups": count + 1, "namespace": e2eDataNS,
			"credentialsSecretRef": map[string]any{"name": "backup-credentials"},
			"clusterTemplate":      template, "cleanupAfterSeconds": 60}}}})
	require.NoError(f.t, err)
	f.kubectl(nil, "patch", "openbaoclusters.kubebao.org", e2eCluster, "-n", e2eDataNS, "--type=merge", "-p", string(patch))
	// Do not use backup(), which waits for every Job in this namespace: the
	// scheduled restore must remain alive long enough to inspect its data.
	f.kubectl(nil, "annotate", "openbaoclusters.kubebao.org", e2eCluster, "-n", e2eDataNS, "--overwrite",
		"openbao.org/trigger-backup="+time.Now().UTC().Format(time.RFC3339Nano))
	f.eventually("a new destination backup to trigger the scheduled restore", 8*time.Minute, func() bool {
		cluster := f.cluster(e2eDestinationGroup)
		completed, _, _ := unstructured.NestedInt64(cluster.Object, "status", "backup", "successfulBackups")
		return completed > count
	})
	f.assertScheduledFreshRestore()
}

// prepareRestoreDestination represents the administrator preparation required
// by fresh restores. The independent Secret contains the source static key;
// the target must not initialize a different key for the source snapshot.
func (f *e2eFixture) prepareRestoreDestination() {
	f.t.Helper()
	seal := f.getJSON("get", "secret", e2eCluster+constants.SuffixUnsealKey, "-n", e2eDataNS)
	manifest, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret",
		"metadata":  map[string]any{"name": e2eSnapshotSeal, "namespace": e2eDataNS},
		"immutable": true, "type": "Opaque", "data": seal["data"]})
	require.NoError(f.t, err)
	f.kubectl(manifest, "create", "-f", "-")
	f.kubectl(nil, "label", "namespace", e2eDataNS, "openbao.org/restore-target-approved=true", "--overwrite")
	// Normal managed Clusters get per-Cluster network policies. Fresh targets
	// instead require an administrator-prepared boundary. This fixture permits
	// its own namespace and the operator to reach targets, and target/executor
	// egress for Kubernetes authentication, DNS, and the isolated RustFS server.
	policy, err := json.Marshal(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "fresh-restore-fixture", "namespace": e2eDataNS},
		"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress", "Egress"},
			"ingress": []any{map[string]any{"from": []any{
				map[string]any{"podSelector": map[string]any{}},
				map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": e2eOperatorNS}}},
			}}}, "egress": []any{map[string]any{}}}})
	require.NoError(f.t, err)
	f.kubectl(policy, "apply", "-f", "-")
}

func (f *e2eFixture) freshRestoreTemplate() map[string]any {
	spec := f.cluster(primitivesSourceGroup).Object["spec"].(map[string]any)
	return map[string]any{"version": spec["version"], "image": spec["image"],
		"storage": spec["storage"], "tls": spec["tls"], "initContainer": spec["initContainer"],
		"unseal": map[string]any{"type": "static", "credentialsSecretRef": map[string]any{"name": e2eSnapshotSeal}}}
}

func (f *e2eFixture) restoreSnapshotSource() map[string]any {
	status := f.cluster(primitivesSourceGroup).Object["status"].(map[string]any)["backup"].(map[string]any)
	summary := status["latestSnapshot"].(map[string]any)
	return map[string]any{"key": status["lastBackupName"], "target": e2eStorageTarget(),
		"expectedClusterID": summary["clusterID"], "expectedVersion": summary["version"],
		"expectedDigest": summary["digest"], "expectedSize": summary["size"]}
}

func (f *e2eFixture) restoreRecord(group string) map[string]any {
	return f.getJSON("get", "openbaorestores."+group, e2eRetainedRecord, "-n", e2eDataNS)
}

func (f *e2eFixture) createRetainedRestore(template, source map[string]any) {
	f.t.Helper()
	manifest, err := json.Marshal(map[string]any{"apiVersion": primitivesSourceGroup + "/v1alpha1", "kind": "OpenBaoRestore",
		"metadata": map[string]any{"name": e2eRetainedRecord, "namespace": e2eDataNS},
		"spec": map[string]any{"cluster": e2eRetainedTarget, "clusterTemplate": template,
			"targetLifecycle": "Retain", "force": true, "image": f.images["backup"], "source": source}})
	require.NoError(f.t, err)
	f.kubectl(manifest, "create", "-f", "-")
	f.eventually("the fresh retained target to apply the snapshot", 15*time.Minute, func() bool {
		status := f.restoreRecord(primitivesSourceGroup)["status"]
		applied, _, _ := unstructured.NestedFieldNoCopy(map[string]any{"status": status}, "status", "target", "appliedAt")
		return applied != nil
	})
	status := f.restoreRecord(primitivesSourceGroup)["status"].(map[string]any)
	require.NotEqual(f.t, source["expectedClusterID"], status["target"].(map[string]any)["bootstrapClusterID"])
	require.Equal(f.t, source["expectedDigest"], status["submissionClaim"].(map[string]any)["digest"])
	f.assertNamedRestoreData(e2eRetainedTarget, "before", "source")
	f.repairRetainedControllerTrust()
	uid := f.restoreRecord(primitivesSourceGroup)["metadata"].(map[string]any)["uid"].(string)
	f.kubectl(nil, "annotate", "openbaorestores."+primitivesSourceGroup, e2eRetainedRecord, "-n", e2eDataNS,
		constants.AnnotationRestoreAcknowledge+"="+uid+"/Resume")
	f.eventually("the retained target Resume handoff to finish", 15*time.Minute, func() bool {
		record := f.restoreRecord(primitivesSourceGroup)
		phase, _, _ := unstructured.NestedString(record, "status", "phase")
		completed, _, _ := unstructured.NestedFieldNoCopy(record, "status", "restart", "completedAt")
		target := f.clusterNamed(primitivesSourceGroup, e2eRetainedTarget)
		return phase == "Completed" && completed != nil && clusterReady(target) &&
			target.GetAnnotations()[constants.AnnotationRestoreOrigin] == "" && target.GetAnnotations()[constants.AnnotationRestoreHold] == ""
	})
}

// repairRetainedControllerTrust follows the documented restore administrator
// recovery path: preserve the restored role while replacing its audience with
// the target's identity, and import the API server's public signing keys. The
// restricted target does not have an API token to fetch authenticated JWKS.
func (f *e2eFixture) repairRetainedControllerTrust() {
	f.t.Helper()
	bao, closeClient := f.namedRestoreClient(e2eRetainedTarget)
	defer closeClient()
	ctx := context.Background()
	config, _ := f.restClient()
	keys, err := adapterauth.FetchJWKSKeys(ctx, config, config.Host+"/openid/v1/jwks")
	require.NoError(f.t, err)
	discovery := map[string]any{}
	require.NoError(f.t, json.Unmarshal([]byte(f.kubectl(nil, "get", "--raw", "/.well-known/openid-configuration")), &discovery))
	_, err = bao.request(ctx, http.MethodPost, "auth/jwt-operator/config", map[string]any{
		"bound_issuer": discovery["issuer"], "jwt_validation_pubkeys": keys,
		"jwks_url": "", "jwks_ca_pem": "", "oidc_discovery_url": ""}, true)
	require.NoError(f.t, err)
	data, err := bao.request(ctx, http.MethodGet, "auth/jwt-operator/role/openbao-operator", nil, true)
	require.NoError(f.t, err)
	role := map[string]any{}
	require.NoError(f.t, json.Unmarshal(data, &role))
	fields := role["data"].(map[string]any)
	audience := "urn:openbao:controller:" + string(f.clusterNamed(primitivesSourceGroup, e2eRetainedTarget).GetUID())
	fields["bound_audiences"] = []string{audience}
	_, err = bao.request(ctx, http.MethodPost, "auth/jwt-operator/role/openbao-operator", fields, true)
	require.NoError(f.t, err)
	kube, err := newKube(f.kubeconfig, f.context, "", time.Minute)
	require.NoError(f.t, err)
	controller, err := kube.runningPod(ctx, e2eOperatorNS, "openbao-operator-controller")
	require.NoError(f.t, err)
	clientset, err := kubernetes.NewForConfig(config)
	require.NoError(f.t, err)
	require.NoError(f.t, verifyControllerLogin(ctx, clientset, bao, controller, []string{audience}, []string{unrelatedAudience}))
}

func (f *e2eFixture) namedRestoreClient(name string) (*OpenBao, func()) {
	f.t.Helper()
	_, c := f.restClient()
	forward := newPortForward(f.kubeconfig, f.context, c, e2eDataNS, name, name)
	secret := f.getJSON("get", "secret", name+"-tls-ca", "-n", e2eDataNS)
	ca, err := base64.StdEncoding.DecodeString(secret["data"].(map[string]any)["ca.crt"].(string))
	require.NoError(f.t, err)
	token, err := readToken(f.tokenFile())
	require.NoError(f.t, err)
	bao, err := newOpenBao(openBaoEndpoint{URL: "https://" + name + "." + e2eDataNS + ".svc:8200",
		ServerName: name + "." + e2eDataNS + ".svc", CA: ca, Token: token, Dial: forward.DialContext})
	require.NoError(f.t, err)
	return bao, func() {
		bao.http.CloseIdleConnections()
		bao.stream.CloseIdleConnections()
		forward.Close()
	}
}

func (f *e2eFixture) assertNamedRestoreData(name, key, expected string) {
	f.t.Helper()
	bao, closeClient := f.namedRestoreClient(name)
	defer closeClient()
	f.eventually("restored fixture data on "+name, 3*time.Minute, func() bool {
		data, err := bao.request(context.Background(), http.MethodGet, "migration-data/data/"+key, nil, true)
		if err != nil {
			return false
		}
		response := map[string]any{}
		if err := json.Unmarshal(data, &response); err != nil {
			return false
		}
		value, found, err := unstructured.NestedString(response, "data", "data", "value")
		return err == nil && found && value == expected
	})
}

func (f *e2eFixture) writeNamedRestoreData(name, key, value string) {
	f.t.Helper()
	bao, closeClient := f.namedRestoreClient(name)
	defer closeClient()
	_, err := bao.request(context.Background(), http.MethodPost, "migration-data/data/"+key,
		map[string]any{"data": map[string]any{"value": value}}, true)
	require.NoError(f.t, err, "write retained target data %s", key)
}

func (f *e2eFixture) assertRetainedRestore(group, sourceUID, jobName string, claimUID, clusterID any) {
	f.t.Helper()
	target := f.clusterNamed(group, e2eRetainedTarget)
	require.NotNil(f.t, target)
	require.True(f.t, clusterReady(target))
	if group == e2eDestinationGroup {
		require.Equal(f.t, sourceUID, target.GetAnnotations()[constants.AnnotationMigrationSourceUID])
	} else {
		require.Equal(f.t, sourceUID, string(target.GetUID()))
	}
	claim := f.getJSON("get", "pvc", "data-"+e2eRetainedTarget+"-0", "-n", e2eDataNS)
	require.Equal(f.t, claimUID, claim["metadata"].(map[string]any)["uid"])
	require.Equal(f.t, "Bound", claim["status"].(map[string]any)["phase"])
	annotations := claim["metadata"].(map[string]any)["annotations"].(map[string]any)
	require.Equal(f.t, string(target.GetUID()), annotations[constants.AnnotationOpenBaoOwnerUID])
	record := f.restoreRecord(group)
	status := record["status"].(map[string]any)
	require.Equal(f.t, "Completed", status["phase"])
	require.Equal(f.t, "Resume", status["administratorDisposition"])
	require.NotNil(f.t, status["restart"].(map[string]any)["completedAt"])
	require.Equal(f.t, jobName, status["execution"].(map[string]any)["jobName"])
	// Completed restores retain their execution history, not their executor Job.
	// Both releases perform terminal cleanup; do not race or prevent that cleanup.
	f.eventually("terminal restore executor cleanup", 2*time.Minute, func() bool {
		out, err := f.kubectlOutput("get", "job", jobName, "-n", e2eDataNS, "--ignore-not-found", "-o", "name")
		return err == nil && strings.TrimSpace(out) == ""
	})
	bao, closeClient := f.namedRestoreClient(e2eRetainedTarget)
	defer closeClient()
	health, err := bao.request(context.Background(), http.MethodGet, "sys/health", nil, false)
	require.NoError(f.t, err)
	response := map[string]any{}
	require.NoError(f.t, json.Unmarshal(health, &response))
	require.Equal(f.t, clusterID, response["cluster_id"])
}

func (f *e2eFixture) assertScheduledFreshRestore() {
	f.t.Helper()
	var name, job string
	f.eventually("the scheduled destination restore to apply its snapshot", 15*time.Minute, func() bool {
		cluster := f.cluster(e2eDestinationGroup)
		if cluster == nil {
			return false
		}
		last, _, _ := unstructured.NestedMap(cluster.Object, "status", "backup", "restoreTest", "last")
		if last["outcome"] == "Failed" {
			f.t.Fatalf("scheduled restore failed before inspection: %v", last)
		}
		name, _, _ = unstructured.NestedString(cluster.Object, "status", "backup", "restoreTest", "active", "name")
		if name == "" {
			return false
		}
		out, err := f.kubectlOutput("get", "openbaorestores.kubebao.org", name, "-n", e2eDataNS, "--ignore-not-found", "-o", "json")
		request := map[string]any{}
		if err != nil || json.Unmarshal([]byte(out), &request) != nil {
			return false
		}
		applied, _, _ := unstructured.NestedFieldNoCopy(request, "status", "target", "appliedAt")
		job, _, _ = unstructured.NestedString(request, "status", "execution", "jobName")
		return applied != nil
	})
	f.assertNamedRestoreData(name, "after-retained-migration", e2eDestination)
	f.eventually("a Passed scheduled restore result after migration", 15*time.Minute, func() bool {
		cluster := f.cluster(e2eDestinationGroup)
		if cluster == nil {
			return false
		}
		status, _, _ := unstructured.NestedMap(cluster.Object, "status", "backup", "restoreTest")
		last, _ := status["last"].(map[string]any)
		return status["active"] == nil && last["name"] == name && last["outcome"] == "Passed" && last["reason"] == "SnapshotApplied"
	})
	status, _, _ := unstructured.NestedMap(f.cluster(e2eDestinationGroup).Object, "status", "backup", "restoreTest", "last")
	require.Equal(f.t, e2eDataNS, status["namespace"])
	require.NotEmpty(f.t, status["key"])
	require.Regexp(f.t, `^sha256:[a-f0-9]{64}$`, status["digest"])
	require.NotEmpty(f.t, job)
	f.eventually("the successful disposable restore to clean up its resources", 3*time.Minute, func() bool {
		for _, resource := range []string{"openbaorestores.kubebao.org/" + name, "openbaoclusters.kubebao.org/" + name,
			"persistentvolumeclaim/data-" + name + "-0", "job/" + job} {
			out, err := f.kubectlOutput("get", resource, "-n", e2eDataNS, "--ignore-not-found", "-o", "name")
			if err != nil || strings.TrimSpace(out) != "" {
				return false
			}
		}
		return true
	})
	f.assertData("before", "after-retained-migration")
}
