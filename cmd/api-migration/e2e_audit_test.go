//go:build apimigration_e2e

package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	e2eAuditStorageClass = "api-migration-audit-rwx"
	e2eAuditHostPath     = "/tmp/api-migration-audit"
	e2eAuditAnchor       = "platform-audit-owner"
)

func (f *e2eFixture) auditClaimName() string {
	if f.shape.auditMode == "ExistingPVC" {
		return "platform-audit"
	}
	return e2eCluster + "-audit"
}

// prepareAudit supplies an RWX fixture on the single Kind node. An external
// claim has a separate controller owner and no OpenBao ownership metadata.
func (f *e2eFixture) prepareAudit() {
	if f.shape.auditMode == "" {
		return
	}
	f.t.Helper()
	for _, pod := range f.auditPodNames() {
		path := e2eAuditHostPath + "/" + pod
		f.cmd("docker", nil, "exec", f.name+"-control-plane", "sh", "-c", fmt.Sprintf(
			"mkdir -p %s && chown %d:%d %s && chmod 0770 %s", path, constants.UserOpenBao, constants.GroupOpenBao, path, path))
	}
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
`, e2eAuditStorageClass, f.auditClaimName(), e2eDataNS, e2eAuditHostPath)
	f.kubectl([]byte(manifest), "apply", "-f", "-")
	if f.shape.auditMode != "ExistingPVC" {
		return
	}
	f.kubectl([]byte(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata: {name: %s, namespace: %s}
`, e2eAuditAnchor, e2eDataNS)), "apply", "-f", "-")
	anchor := f.getJSON("get", "configmap", e2eAuditAnchor, "-n", e2eDataNS)
	claim := map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": f.auditClaimName(), "namespace": e2eDataNS,
			"ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": e2eAuditAnchor,
				"uid": anchor["metadata"].(map[string]any)["uid"], "controller": true}}},
		"spec": map[string]any{"accessModes": []any{"ReadWriteMany"}, "storageClassName": e2eAuditStorageClass,
			"resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}}}
	data, err := json.Marshal(claim)
	require.NoError(f.t, err)
	f.kubectl(data, "apply", "-f", "-")
}

func (f *e2eFixture) configureAudit(spec map[string]any) {
	if f.shape.auditMode == "" {
		return
	}
	storage := map[string]any{"mode": f.shape.auditMode}
	if f.shape.auditMode == "ManagedPVC" {
		storage["size"], storage["storageClassName"] = "1Gi", e2eAuditStorageClass
	} else {
		storage["existingClaimName"] = f.auditClaimName()
	}
	spec["auditFileStorage"] = storage
	spec["audit"] = []any{map[string]any{"type": "file", "path": "file", "description": "Migration audit fixture",
		"fileOptions": map[string]any{"filePath": "/openbao/audit/audit.jsonl"}}}
}

func (f *e2eFixture) auditPodNames() []string {
	names := make([]string, 0, f.shape.replicas+f.shape.readReplicas)
	for ordinal := range f.shape.replicas {
		names = append(names, fmt.Sprintf("%s-%d", e2eCluster, ordinal))
	}
	for ordinal := range f.shape.readReplicas {
		names = append(names, fmt.Sprintf("%s-read-%d", e2eCluster, ordinal))
	}
	return names
}

type auditClaimIdentity struct {
	UID      types.UID
	PVUID    types.UID
	ClaimUID types.UID
	Spec     corev1.PersistentVolumeClaimSpec
	Owners   []metav1.OwnerReference
}

func (f *e2eFixture) auditClaimIdentity(name string) auditClaimIdentity {
	f.t.Helper()
	var claim corev1.PersistentVolumeClaim
	require.NoError(f.t, json.Unmarshal([]byte(f.kubectl(nil, "get", "pvc", name, "-n", e2eDataNS, "-o", "json")), &claim))
	require.Equal(f.t, corev1.ClaimBound, claim.Status.Phase, "claim %s", name)
	var volume corev1.PersistentVolume
	require.NoError(f.t, json.Unmarshal([]byte(f.kubectl(nil, "get", "pv", claim.Spec.VolumeName, "-o", "json")), &volume))
	require.NotNil(f.t, volume.Spec.ClaimRef)
	require.Equal(f.t, claim.UID, volume.Spec.ClaimRef.UID)
	require.Equal(f.t, e2eDataNS, volume.Spec.ClaimRef.Namespace)
	require.Equal(f.t, name, volume.Spec.ClaimRef.Name)
	return auditClaimIdentity{UID: claim.UID, PVUID: volume.UID, ClaimUID: volume.Spec.ClaimRef.UID,
		Spec: claim.Spec, Owners: claim.OwnerReferences}
}

func (f *e2eFixture) waitAuditReady(group string) {
	f.t.Helper()
	f.eventually("audit storage to be ready in "+group, 5*time.Minute, func() bool {
		cluster := f.cluster(group)
		if cluster == nil {
			return false
		}
		conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
		for _, raw := range conditions {
			condition := raw.(map[string]any)
			generation, _, _ := unstructured.NestedInt64(condition, "observedGeneration")
			if condition["type"] == "AuditFileStorageReady" && condition["status"] == string(metav1.ConditionTrue) && generation == cluster.GetGeneration() {
				return clusterReady(cluster)
			}
		}
		return false
	})
}

func (f *e2eFixture) assertAuditMounts(group string, claims map[string]auditClaimIdentity) {
	f.t.Helper()
	f.waitAuditReady(group)
	for name, before := range claims {
		require.Equal(f.t, before, f.auditClaimIdentity(name), "claim and binding %s", name)
	}
	claim := f.getJSON("get", "pvc", f.auditClaimName(), "-n", e2eDataNS)
	annotations, _, _ := unstructured.NestedStringMap(claim, "metadata", "annotations")
	if f.shape.auditMode == "ManagedPVC" {
		require.Equal(f.t, string(f.cluster(group).GetUID()), annotations[constants.AnnotationOpenBaoOwnerUID])
	} else {
		require.Empty(f.t, annotations[constants.AnnotationOpenBaoOwnerUID], "external audit claim must keep its owner")
		require.Len(f.t, claims[f.auditClaimName()].Owners, 1, "fixture must exercise external ownership")
	}
	for _, name := range f.auditPodNames() {
		var pod corev1.Pod
		require.NoError(f.t, json.Unmarshal([]byte(f.kubectl(nil, "get", "pod", name, "-n", e2eDataNS, "-o", "json")), &pod))
		foundVolume, foundMount := false, false
		for _, volume := range pod.Spec.Volumes {
			if volume.Name == constants.VolumeAuditFileStorage {
				require.NotNil(f.t, volume.PersistentVolumeClaim)
				require.Equal(f.t, f.auditClaimName(), volume.PersistentVolumeClaim.ClaimName)
				foundVolume = true
			}
		}
		for _, container := range pod.Spec.Containers {
			for _, mount := range container.VolumeMounts {
				if mount.Name == constants.VolumeAuditFileStorage {
					require.Equal(f.t, "/openbao/audit", mount.MountPath)
					require.Equal(f.t, "$(BAO_K8S_POD_NAME)", mount.SubPathExpr)
					foundMount = true
				}
			}
		}
		require.True(f.t, foundVolume && foundMount, "audit mount on %s", name)
	}
}

type auditPrefix struct {
	bytes int64
	hash  string
}

// auditPrefixes reads only sizes and digests. Audit records are not copied into
// test output or artifacts, because they can contain workload metadata.
func (f *e2eFixture) auditPrefixes() map[string]auditPrefix {
	f.t.Helper()
	prefixes := map[string]auditPrefix{}
	var total int64
	for _, name := range f.auditPodNames() {
		path := e2eAuditHostPath + "/" + name + "/audit.jsonl"
		out := f.cmd("docker", nil, "exec", f.name+"-control-plane", "sh", "-c",
			fmt.Sprintf("size=$(stat -c %%s %s) && hash=$(head -c \"$size\" %s | sha256sum) && printf '%%s %%s\\n' \"$size\" \"$hash\"", path, path))
		fields := strings.Fields(out)
		require.GreaterOrEqual(f.t, len(fields), 2)
		size, err := strconv.ParseInt(fields[0], 10, 64)
		require.NoError(f.t, err)
		prefixes[name] = auditPrefix{bytes: size, hash: fields[1]}
		total += size
	}
	require.Positive(f.t, total, "audit history must contain records before testing its preservation")
	return prefixes
}

func (f *e2eFixture) assertAuditPrefixes(prefixes map[string]auditPrefix) {
	f.t.Helper()
	for name, prefix := range prefixes {
		path := e2eAuditHostPath + "/" + name + "/audit.jsonl"
		out := f.cmd("docker", nil, "exec", f.name+"-control-plane", "sh", "-c",
			fmt.Sprintf("test $(stat -c %%s %s) -ge %d && head -c %d %s | sha256sum", path, prefix.bytes, prefix.bytes, path))
		require.Equal(f.t, prefix.hash, strings.Fields(out)[0], "audit history on %s", name)
	}
}

func (f *e2eFixture) auditWrite(marker string) {
	f.t.Helper()
	f.writeData(marker, marker)
	// Request paths remain readable in audit records. Only grep's status is
	// returned; the matching record never enters the test log.
	f.cmd("docker", nil, "exec", f.name+"-control-plane", "sh", "-c",
		"grep -q -F 'migration-data/data/"+marker+"' "+e2eAuditHostPath+"/*/audit.jsonl")
}

func (f *e2eFixture) migrateAudit() {
	f.t.Helper()
	f.waitAuditReady(primitivesSourceGroup)
	claims := map[string]auditClaimIdentity{}
	for _, pod := range f.auditPodNames() {
		claims["data-"+pod] = f.auditClaimIdentity("data-" + pod)
	}
	claims[f.auditClaimName()] = f.auditClaimIdentity(f.auditClaimName())
	f.assertAuditMounts(primitivesSourceGroup, claims)
	f.auditWrite("audit-before")
	prefixes := f.auditPrefixes()

	state := f.newState("audit-rollback")
	f.plan(state)
	f.migrate(state)
	f.assertAuditMounts(e2eDestinationGroup, claims)
	f.assertAuditPrefixes(prefixes)
	f.auditWrite("audit-during-rollback")
	f.procedure("rollback", state)
	f.assertSurvivor(primitivesSourceGroup, "audit-rollback")
	f.replaceDataPod(primitivesSourceGroup, e2eCluster+"-1")
	f.replaceDataPod(primitivesSourceGroup, e2eCluster+"-read-0")
	f.assertAuditMounts(primitivesSourceGroup, claims)
	f.assertAuditPrefixes(prefixes)
	f.auditWrite("audit-after-rollback")
	// Preserve records written during the rollback rehearsal as well.
	prefixes = f.auditPrefixes()

	f.waitSettled()
	state = f.newState("audit-commit")
	f.plan(state)
	f.migrate(state)
	f.procedure("commit", state)
	f.assertSurvivor(e2eDestinationGroup, "audit-commit")
	f.replaceDataPod(e2eDestinationGroup, e2eCluster+"-1")
	f.replaceDataPod(e2eDestinationGroup, e2eCluster+"-read-0")
	f.assertAuditMounts(e2eDestinationGroup, claims)
	f.assertAuditPrefixes(prefixes)
	f.auditWrite("audit-after-commit")
	f.assertData(f.written...)
}
