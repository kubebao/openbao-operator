//go:build apimigration_e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

// The hardened shape adds a production Cluster next to the Development one: the
// Hardened profile with three voters, OpenBao's native ACME TLS, and Transit
// auto-unseal, both served by an infra-bao fixture. Hardened verifies image
// signatures, so this Cluster runs the published images.
const (
	e2eHardened         = "hardened"
	e2eHardenedImage    = "ghcr.io/openbao/openbao:" + e2eOpenBaoVer
	e2eInfraBao         = "infra-bao"
	e2eInfraBaoKey      = "openbao-unseal"
	e2eInfraBaoSecret   = "infra-bao-credentials"
	e2eACMEStorageClass = "api-migration-acme-rwx"
	e2eACMECAFile       = "acme-ca.crt"
)

// e2eACMEDomain is the name the ACME certificate carries: the operator's ACME
// validation Service.
const e2eACMEDomain = e2eHardened + "-acme." + e2eDataNS + ".svc"

// restClient is a typed client for the e2ehelpers fixtures.
func (f *e2eFixture) restClient() (*rest.Config, client.Client) {
	f.t.Helper()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: f.kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: f.context}).ClientConfig()
	require.NoError(f.t, err)
	scheme := runtime.NewScheme()
	require.NoError(f.t, clientgoscheme.AddToScheme(scheme))
	c, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(f.t, err)
	return config, c
}

// createHardenedCluster deploys infra-bao with PKI ACME and Transit, prepares
// the ACME shared cache, and creates the Hardened Cluster on the source release.
func (f *e2eFixture) createHardenedCluster() {
	f.t.Helper()
	ctx := context.Background()
	restConfig, c := f.restClient()
	image := f.images["openbao"]
	infra := "https://" + e2eInfraBao + "." + e2eDataNS + ".svc:8200"

	require.NoError(f.t, e2ehelpers.EnsureInfraBao(ctx, restConfig, c,
		e2ehelpers.InfraBaoConfig{Namespace: e2eDataNS, Name: e2eInfraBao, Image: image}))
	token, err := e2ehelpers.ReadInfraBaoRootToken(ctx, c, e2eDataNS, e2eInfraBao)
	require.NoError(f.t, err)
	tlsCA, err := e2ehelpers.ReadInfraBaoTLSCACert(ctx, c, e2eDataNS, e2eInfraBao)
	require.NoError(f.t, err)
	result, err := e2ehelpers.ConfigureInfraBaoPKIACME(ctx, restConfig, c, e2eDataNS, e2eInfraBao, image, infra, infra+"/v1/pki")
	require.NoError(f.t, err)
	require.Equal(f.t, corev1.PodSucceeded, result.Phase, "configure PKI ACME:\n%s", result.Logs)
	result, err = e2ehelpers.ConfigureInfraBaoTransit(ctx, restConfig, c, e2eDataNS, e2eInfraBao, image, infra, e2eInfraBaoKey)
	require.NoError(f.t, err)
	require.Equal(f.t, corev1.PodSucceeded, result.Phase, "configure Transit:\n%s", result.Logs)
	pkiCA, err := e2ehelpers.FetchInfraBaoPKICA(ctx, restConfig, c, e2eDataNS, e2eInfraBao, image, infra)
	require.NoError(f.t, err)
	require.NoError(f.t, e2ehelpers.EnsureInfraBaoSealCredentialsSecret(ctx, c, e2eDataNS, e2eInfraBaoSecret,
		token, tlsCA, pkiCA))
	// The tool verifies the ACME certificate against the PKI CA.
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir, e2eACMECAFile), pkiCA, 0o600))
	f.prepareACMECache()

	cluster := map[string]any{"apiVersion": primitivesSourceGroup + "/v1alpha1", "kind": "OpenBaoCluster",
		"metadata": map[string]any{"name": e2eHardened, "namespace": e2eDataNS},
		"spec": map[string]any{
			"controllerJWTMode": "Target", "profile": "Hardened", "version": e2eOpenBaoVer, "image": e2eHardenedImage,
			"replicas":       3,
			"deletionPolicy": "Retain",
			"initContainer":  map[string]any{"enabled": true, "image": e2eInitImage},
			"selfInit": map[string]any{"enabled": true, "oidc": map[string]any{"enabled": true}, "requests": []any{
				map[string]any{"name": "enable-kv", "operation": "update", "path": "sys/mounts/migration-data",
					"secretEngine": map[string]any{"type": "kv", "options": map[string]any{"version": "2"}}},
			}},
			"tls": map[string]any{"enabled": true, "mode": "ACME", "acme": map[string]any{
				"directoryURL": infra + "/v1/pki/acme/directory", "email": "e2e@example.invalid",
				"sharedCache": map[string]any{"mode": "ManagedPVC", "size": "1Gi", "storageClassName": e2eACMEStorageClass},
			}},
			// The seal credentials carry infra-bao's TLS CA for the ACME directory and Transit.
			"configuration": map[string]any{"acmeCARoot": "/etc/bao/seal-creds/ca.crt"},
			"unseal": map[string]any{"type": "transit",
				"transit": map[string]any{"address": infra, "mountPath": "transit", "keyName": e2eInfraBaoKey,
					"tlsCACert": "/etc/bao/seal-creds/ca.crt"},
				"credentialsSecretRef": map[string]any{"name": e2eInfraBaoSecret}},
			"storage": map[string]any{"size": "1Gi"},
			"network": map[string]any{"apiServerCIDR": "10.96.0.0/12", "apiServerEndpointIPs": f.apiServerAddresses(),
				// infra-bao validates ACME challenges on the Cluster and serves the directory and Transit.
				"trustedIngressPeers": []any{map[string]any{"podSelector": map[string]any{
					"matchLabels": map[string]any{"app": e2eInfraBao}}}},
				"egressRules": []any{map[string]any{
					"to":    []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": e2eInfraBao}}}},
					"ports": []any{map[string]any{"protocol": "TCP", "port": 8200}}}}},
		}}
	// Give the probe the same JWT login used in the Development fixture.
	selfInit := cluster["spec"].(map[string]any)["selfInit"].(map[string]any)
	requests := f.sourceCluster()["spec"].(map[string]any)["selfInit"].(map[string]any)["requests"].([]any)
	selfInit["requests"] = append(selfInit["requests"].([]any), requests[1:]...)
	network := cluster["spec"].(map[string]any)["network"].(map[string]any)
	network["trustedIngressPeers"] = append(network["trustedIngressPeers"].([]any), map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"role": e2eProbeRole}}})
	if f.shape.gitops.kind != "" {
		f.hardenedSource = cluster
		resources := f.gitopsResources()
		f.pushManifests("resources", "source", resources)
		converted, changed := convertManifest(resources)
		require.Positive(f.t, changed)
		f.pushManifests("resources", e2eDestination, converted)
	} else {
		data, _ := json.Marshal(cluster)
		f.kubectl(data, "apply", "-f", "-")
	}
	f.eventually("the Hardened ACME Cluster to initialize", 15*time.Minute, func() bool {
		return clusterReady(f.clusterNamed(primitivesSourceGroup, e2eHardened))
	})
	f.waitACMEReady(primitivesSourceGroup)
}

// prepareACMECache provides the ReadWriteMany volume the managed ACME cache
// claim binds to: a hostPath on the single Kind node, owned by OpenBao's user.
func (f *e2eFixture) prepareACMECache() {
	f.t.Helper()
	claim := e2eHardened + "-acme-cache"
	path := "/tmp/api-migration-acme/" + claim
	f.cmd("docker", nil, "exec", f.name+"-control-plane", "sh", "-c", fmt.Sprintf("mkdir -p %s && chown %d:%d %s && chmod 0770 %s",
		path, constants.UserOpenBao, constants.GroupOpenBao, path, path))
	manifest := fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: %[1]s}
provisioner: kubernetes.io/no-provisioner
volumeBindingMode: Immediate
---
apiVersion: v1
kind: PersistentVolume
metadata: {name: %[2]s}
spec:
  capacity: {storage: 1Gi}
  accessModes: [ReadWriteMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: %[1]s
  claimRef: {apiVersion: v1, kind: PersistentVolumeClaim, namespace: %[3]s, name: %[2]s}
  hostPath: {path: %[4]s, type: DirectoryOrCreate}
`, e2eACMEStorageClass, claim, e2eDataNS, path)
	f.kubectl([]byte(manifest), "apply", "-f", "-")
}

// waitACMEReady waits for the active operator to report the ACME integration
// ready at the Cluster's current generation.
func (f *e2eFixture) waitACMEReady(group string) {
	f.t.Helper()
	f.eventually("ACME integration to be ready in "+group, 10*time.Minute, func() bool {
		object := f.clusterNamed(group, e2eHardened)
		if object == nil {
			return false
		}
		conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, _ := raw.(map[string]any)
			generation, _, _ := unstructured.NestedInt64(condition, "observedGeneration")
			if condition["type"] == "ACMEIntegrationReady" && condition["status"] == e2eConditionTrue &&
				generation == object.GetGeneration() {
				return true
			}
		}
		return false
	})
}

// assertHardened checks the Hardened Cluster in the active group: ready, ACME
// ready, and its ACME cache claim in the active Cluster's custody.
func (f *e2eFixture) assertHardened(group string, retired bool) {
	f.t.Helper()
	f.eventually("the Hardened Cluster to be ready in "+group, 10*time.Minute, func() bool {
		return clusterReady(f.clusterNamed(group, e2eHardened))
	})
	f.waitACMEReady(group)
	if retired {
		other := map[string]string{"openbao.org": "kubebao.org", "kubebao.org": "openbao.org"}[group]
		require.Nil(f.t, f.clusterNamed(other, e2eHardened), "the %s Hardened Cluster must be retired", other)
	}
	uid := f.clusterNamed(group, e2eHardened).GetUID()
	claim := f.getJSON("get", "pvc", e2eHardened+"-acme-cache", "-n", e2eDataNS)
	require.Equal(f.t, "Bound", claim["status"].(map[string]any)["phase"])
	annotations, _, _ := unstructured.NestedStringMap(claim, "metadata", "annotations")
	require.Equal(f.t, string(uid), annotations[constants.AnnotationOpenBaoOwnerUID],
		"the ACME cache claim must follow the active Cluster")
}

// assertReconciles changes the named Cluster's specification and waits until
// the active operator applies it to the Cluster's ServiceAccount.
func (f *e2eFixture) assertReconciles(group, name, proof string) {
	f.t.Helper()
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"serviceAccount": map[string]any{
		"annotations": map[string]any{e2eProofKey: proof}}}})
	f.kubectl(nil, "patch", "openbaoclusters."+group, name, "-n", e2eDataNS, "--type", "merge", "-p", string(patch))
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(e2eProofKey) + `":\s*"` + regexp.QuoteMeta(proof) + `"`)
	f.eventually("the "+group+" Cluster "+name+" to reconcile a new change", 5*time.Minute, func() bool {
		out := f.kubectl(nil, "get", "serviceaccounts", "-n", e2eDataNS, "-l", "openbao.org/cluster="+name, "-o", "json")
		return pattern.MatchString(out) && clusterReady(f.clusterNamed(group, name))
	})
}
