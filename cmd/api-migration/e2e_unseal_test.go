//go:build apimigration_e2e

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"net/http"
	"strings"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

var (
	e2eSoftHSMImage = flag.String("softhsm-image", "", "locally built OpenBao 2.6.5 image with the pinned PKCS#11 plugin and SoftHSM fixture")
	e2eKMIPImage    = flag.String("kmip-image", "", "locally built PyKMIP fixture image")
)

const (
	e2eSealPKCS11         = "pkcs11"
	e2eSealKMIP           = "kmip"
	e2eMaintenanceEnabled = "true"
	e2eSealCredentials    = "migration-seal-credentials"
	e2eSoftHSMConfig      = "directories.tokendir = /bao/data/softhsm/tokens\nobjectstore.backend = file\nlog.level = ERROR\nslots.removable = false\n"
)

// prepareUnseal creates externally managed seal credentials before the source
// Cluster starts. Selected cases require their fixture images; they never skip.
func (f *e2eFixture) prepareUnseal() {
	f.t.Helper()
	switch f.shape.seal {
	case "":
		return
	case e2eSealPKCS11:
		require.NotEmpty(f.t, strings.TrimSpace(*e2eSoftHSMImage), "pkcs11 requires -softhsm-image")
		_, c := f.restClient()
		require.NoError(f.t, c.Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: e2eSealCredentials, Namespace: e2eDataNS},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"BAO_HSM_PIN":   []byte("1234"),
				"softhsm2.conf": []byte(e2eSoftHSMConfig),
			},
		}))
	case e2eSealKMIP:
		require.NotEmpty(f.t, strings.TrimSpace(*e2eKMIPImage), "kmip requires -kmip-image")
		f.prepareKMIP()
	default:
		f.t.Fatalf("unsupported seal fixture %q", f.shape.seal)
	}
}

// configureUnseal keeps the source OpenBao and plugin versions fixed throughout
// the API move. The PKCS#11 plugin settings match images/openbao-softhsm.
func (f *e2eFixture) configureUnseal(spec map[string]any) {
	f.t.Helper()
	switch f.shape.seal {
	case "":
		return
	case e2eSealPKCS11:
		require.NotEmpty(f.t, strings.TrimSpace(*e2eSoftHSMImage), "pkcs11 requires -softhsm-image")
		checksum := strings.Fields(f.cmd("docker", nil, "run", "--rm", "--network=none", "--read-only",
			"--entrypoint", "cat", f.images["openbao"], "/opt/openbao-softhsm/plugin.sha256"))
		require.NotEmpty(f.t, checksum, "SoftHSM fixture must contain the pinned plugin checksum")
		require.Regexp(f.t, `^[a-f0-9]{64}$`, checksum[0])
		spec["plugins"] = []any{map[string]any{"type": "kms", "name": "pkcs11",
			"command": "openbao-plugin-kms-pkcs11", "version": "v0.1.0",
			"binaryName": "openbao-plugin-kms-pkcs11", "sha256sum": checksum[0]}}
		spec["unseal"] = map[string]any{"type": "pkcs11",
			"pkcs11": map[string]any{"lib": "/usr/lib/softhsm/libsofthsm2.so", "tokenLabel": "OpenBao",
				"keyLabel": "bao-root-key-rsa", "mechanism": "RSA_PKCS_OAEP", "rsaOAEPHash": "sha1",
				"runtime": map[string]any{"libraryPath": "/usr/lib/softhsm", "fileEnv": []any{
					map[string]any{"name": "SOFTHSM2_CONF", "secretKey": "softhsm2.conf"}}}},
			"credentialsSecretRef": map[string]any{"name": e2eSealCredentials}}
	case e2eSealKMIP:
		spec["unseal"] = map[string]any{"type": "kmip",
			"kmip": map[string]any{"endpoint": e2eKMIPServer + "." + e2eDataNS + ".svc:5696", "kmsKeyID": "1",
				"clientCert": "/etc/bao/seal-creds/client.crt", "clientKey": "/etc/bao/seal-creds/client.key",
				"caCert": "/etc/bao/seal-creds/ca.crt", "encryptAlg": "AES_GCM",
				"tls12Ciphers": "TLS_RSA_WITH_AES_128_CBC_SHA256"},
			"credentialsSecretRef": map[string]any{"name": e2eSealCredentials}}
		network := spec["network"].(map[string]any)
		rules, _ := network["egressRules"].([]any)
		network["egressRules"] = append(rules, map[string]any{
			"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": e2eKMIPServer}}}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": 5696}}})
	}
}

type e2eUnsealSnapshot struct {
	credentials  map[string]string
	settings     string
	claimUID     types.UID
	volume       string
	volumeUID    types.UID
	keyObjects   string
	kmipPod      types.UID
	kmipRestarts int32
}

// migrateUnseal exercises both surviving operators with a real seal-backed Pod
// replacement. Explicit restarts run outside the migration availability windows.
func (f *e2eFixture) migrateUnseal() {
	f.t.Helper()
	baseline := f.snapshotUnseal(primitivesSourceGroup)
	state := f.newState("unseal-rollback")
	f.plan(state)
	f.migrate(state)
	f.assertUnsealPreserved(e2eDestinationGroup, baseline)
	f.writeData("before-unseal-rollback", e2eDestinationGroup)
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "unseal-rollback")
	f.replaceUnsealPod(primitivesSourceGroup, "unseal-rollback")
	f.assertUnsealPreserved(primitivesSourceGroup, baseline)

	f.migrateAndCommit(f.newState("unseal-commit"))
	f.replaceUnsealPod(e2eDestinationGroup, "unseal-commit")
	f.assertUnsealPreserved(e2eDestinationGroup, baseline)
}

func (f *e2eFixture) snapshotUnseal(group string) e2eUnsealSnapshot {
	f.t.Helper()
	ctx := context.Background()
	_, c := f.restClient()
	result := e2eUnsealSnapshot{credentials: map[string]string{}}
	names := []string{e2eSealCredentials}
	if f.shape.seal == e2eSealKMIP {
		names = append(names, e2eKMIPServerTLS)
		pods := &corev1.PodList{}
		require.NoError(f.t, c.List(ctx, pods, client.InNamespace(e2eDataNS), client.MatchingLabels{"app": e2eKMIPServer}))
		require.Len(f.t, pods.Items, 1, "one external KMIP server must remain running")
		result.kmipPod = pods.Items[0].UID
		require.Len(f.t, pods.Items[0].Status.ContainerStatuses, 1)
		result.kmipRestarts = pods.Items[0].Status.ContainerStatuses[0].RestartCount
	}
	for _, name := range names {
		secret := &corev1.Secret{}
		require.NoError(f.t, c.Get(ctx, types.NamespacedName{Namespace: e2eDataNS, Name: name}, secret))
		// Compare digests so failed assertions never print credential payloads.
		result.credentials[name] = string(secret.UID) + ":" + f.unsealDigest(secret.Data)
		require.Empty(f.t, secret.OwnerReferences, "external seal credentials must remain externally owned")
	}
	cluster := f.cluster(group)
	require.NotNil(f.t, cluster)
	settings := map[string]any{}
	for _, field := range []string{"version", "image", "unseal", "plugins", "network"} {
		settings[field], _, _ = unstructured.NestedFieldNoCopy(cluster.Object, "spec", field)
	}
	result.settings = f.unsealDigest(settings)
	claim := &corev1.PersistentVolumeClaim{}
	require.NoError(f.t, c.Get(ctx, types.NamespacedName{Namespace: e2eDataNS, Name: "data-" + f.voterSet + "-0"}, claim))
	require.Equal(f.t, corev1.ClaimBound, claim.Status.Phase)
	result.claimUID, result.volume = claim.UID, claim.Spec.VolumeName
	require.NotEmpty(f.t, result.volume)
	volume := &corev1.PersistentVolume{}
	require.NoError(f.t, c.Get(ctx, types.NamespacedName{Name: result.volume}, volume))
	require.NotNil(f.t, volume.Spec.ClaimRef)
	require.Equal(f.t, claim.UID, volume.Spec.ClaimRef.UID)
	require.Equal(f.t, claim.Namespace, volume.Spec.ClaimRef.Namespace)
	require.Equal(f.t, claim.Name, volume.Spec.ClaimRef.Name)
	result.volumeUID = volume.UID
	if f.shape.seal == e2eSealPKCS11 {
		// Hash the persistent key objects, excluding mutable token metadata.
		// The command emits only a digest, never key material or the PIN.
		output := f.kubectl(nil, "exec", "-n", e2eDataNS, f.voterSet+"-0", "-c", constants.ContainerBao,
			"--", "/bin/sh", "-ec", `files=$(mktemp)
trap 'rm -f "$files"' EXIT
find /bao/data/softhsm/tokens -type f -name '*.object' ! -name token.object -exec sha256sum {} \; > "$files"
test -s "$files"
LC_ALL=C sort "$files" | sha256sum`)
		fields := strings.Fields(output)
		require.NotEmpty(f.t, fields)
		require.Regexp(f.t, `^[a-f0-9]{64}$`, fields[0])
		result.keyObjects = fields[0]
	}
	return result
}

func (f *e2eFixture) assertUnsealPreserved(group string, baseline e2eUnsealSnapshot) {
	f.t.Helper()
	actual := f.snapshotUnseal(group)
	require.Equal(f.t, baseline, actual, "seal credentials, settings, key material, and storage identity must survive")
	f.assertUnsealData()
}

func (f *e2eFixture) unsealDigest(value any) string {
	f.t.Helper()
	data, err := json.Marshal(value)
	require.NoError(f.t, err)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (f *e2eFixture) replaceUnsealPod(group, proof string) {
	f.t.Helper()
	f.replaceDataPod(group, f.voterSet+"-0")
	f.assertUnsealData()
	f.assertControllerLogin(group, proof+"-replacement")
	f.assertData(f.written...)
}

// replaceDataPod performs an administrator-authorized workload restart. Audit
// and seal cases use it for voter and read-replica Pods after rollback/commit.
func (f *e2eFixture) replaceDataPod(group, podName string) {
	f.t.Helper()
	// Admission permits an administrator to delete a managed Pod only after
	// the operator propagates its maintenance annotation.
	f.kubectl(nil, "patch", "openbaoclusters."+group, e2eCluster, "-n", e2eDataNS, "--type=merge", "-p", `{"spec":{"maintenance":{"enabled":true}}}`)
	_, c := f.restClient()
	key := types.NamespacedName{Namespace: e2eDataNS, Name: podName}
	var before types.UID
	f.eventually("Pod "+podName+" maintenance authorization", 5*time.Minute, func() bool {
		pod := &corev1.Pod{}
		if c.Get(context.Background(), key, pod) != nil || pod.Annotations[constants.AnnotationMaintenance] != e2eMaintenanceEnabled {
			return false
		}
		before = pod.UID
		return true
	})
	f.kubectl(nil, "delete", "pod", key.Name, "-n", key.Namespace, "--wait=false")
	f.eventually("the replacement Pod "+podName+" to become ready in "+group, 10*time.Minute, func() bool {
		pod := &corev1.Pod{}
		if c.Get(context.Background(), key, pod) != nil || pod.UID == before || pod.DeletionTimestamp != nil {
			return false
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return clusterReady(f.cluster(group))
			}
		}
		return false
	})
	f.kubectl(nil, "patch", "openbaoclusters."+group, e2eCluster, "-n", e2eDataNS, "--type=merge", "-p", `{"spec":{"maintenance":{"enabled":false}}}`)
	f.eventually("the replacement Pod to leave maintenance", 5*time.Minute, func() bool {
		pod := &corev1.Pod{}
		return c.Get(context.Background(), key, pod) == nil && pod.Annotations[constants.AnnotationMaintenance] == "" && clusterReady(f.cluster(group))
	})
}

func (f *e2eFixture) assertUnsealData() {
	f.t.Helper()
	data, err := f.baoRequest(http.MethodGet, "sys/seal-status", nil)
	require.NoError(f.t, err)
	var seal struct {
		Initialized bool   `json:"initialized"`
		Sealed      bool   `json:"sealed"`
		Version     string `json:"version"`
	}
	require.NoError(f.t, json.Unmarshal(data, &seal))
	require.True(f.t, seal.Initialized)
	require.False(f.t, seal.Sealed, "the retained seal key must unseal the replacement Pod")
	require.Equal(f.t, e2eOpenBaoVer, seal.Version, "API migration must not upgrade OpenBao")
	data, err = f.baoRequest(http.MethodGet, "migration-data/data/before", nil)
	require.NoError(f.t, err)
	var value struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	require.NoError(f.t, json.Unmarshal(data, &value))
	// Use a boolean assertion to keep stored payloads out of failure output.
	require.True(f.t, value.Data.Data["value"] == e2eSourceValue, "the source value must survive seal-backed Pod replacement")
}
