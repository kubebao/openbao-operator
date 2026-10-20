//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const pvcSyncPolicyName = "openbao-operator-openbao-lock-managed-resource-mutations"

func TestVAP_PVCBindingSync(t *testing.T) {
	// This test changes the shared policy and must remain sequential. Parallel
	// top-level integration tests run after sequential tests finish.
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace, otherNamespace := newTestNamespace(t), newTestNamespace(t)
	const username = "kubernetes-super-admin"
	const limitedUsername = "pvc-binding-syncer"
	syncer := newPrivilegedImpersonatedClient(t, username)
	creator := newPrivilegedImpersonatedClient(t, "system:serviceaccount:kube-system:statefulset-controller")
	probe := newManagedStatefulSetPVC(namespace, "example", "sync-probe")
	require.NoError(t, creator.Create(ctx, probe))
	probe.Spec.VolumeName = "pv-host"
	require.Eventually(t, func() bool {
		err := syncer.Update(ctx, probe.DeepCopy(), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "denied request: Direct modification")
	}, 5*time.Second, 50*time.Millisecond, "default configuration must deny syncer writes")

	configurePVCBindingSync(t, fmt.Sprintf(`{%q:[%q],%q:[%q]}`, username, namespace, limitedUsername, namespace))
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.NoError(collect, syncer.Update(ctx, probe.DeepCopy(), client.DryRunAll))
	}, 5*time.Second, 50*time.Millisecond, "configured policy must allow binding")
	grantPVCBindingSyncWriteAccess(t, namespace, limitedUsername)
	expandable := true
	storageClass := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: namespace + "-expandable"},
		Provisioner: "example.csi.test", AllowVolumeExpansion: &expandable,
	}
	require.NoError(t, k8sClient.Create(ctx, storageClass))
	t.Cleanup(func() { require.NoError(t, k8sClient.Delete(ctx, storageClass)) })

	type testCase struct {
		name     string
		before   func(*corev1.PersistentVolumeClaim)
		change   func(*corev1.PersistentVolumeClaim)
		username string
		otherNS  bool
		bound    bool
		allowed  bool
	}
	tests := slices.Grow([]testCase{
		{name: "binding", change: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-host" }, allowed: true},
		{name: "binding-and-storage-annotations", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.VolumeName = "pv-host"
			p.Annotations["pv.kubernetes.io/bind-completed"] = "yes"
			p.Annotations["pv.kubernetes.io/bound-by-controller"] = "yes"
			p.Annotations["volume.beta.kubernetes.io/storage-provisioner"] = "example.csi.test"
			p.Annotations["volume.kubernetes.io/storage-provisioner"] = "example.csi.test"
			p.Annotations["volume.kubernetes.io/selected-node"] = "host-node"
		}, allowed: true},
		{
			name:     "wrong-user",
			username: "other-syncer",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Spec.VolumeName = "pv-host"
			},
		},
		{
			name:     "user-prefix",
			username: username + "-extra",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Spec.VolumeName = "pv-host"
			},
		},
		{
			name:    "wrong-namespace",
			otherNS: true,
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Spec.VolumeName = "pv-host"
			},
		},
		{
			name: "missing-provenance",
			before: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, "openbao.org/owner-uid")
			},
			change: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-host" }},
		{
			name: "empty-provenance",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["openbao.org/owner-uid"] = ""
			},
			change: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-host" }},
		{
			name: "provenance-change",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["openbao.org/owner-uid"] = "other"
			},
		},
		{
			name: "provenance-removal",
			change: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, "openbao.org/owner-uid")
			},
		},
		{name: "mixed-binding-and-label", change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.VolumeName = "pv-host"
			p.Labels["example.com/team"] = "other"
		}},
		{
			name: "managed-label-removal",
			change: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Labels, "app.kubernetes.io/managed-by")
			},
		},
		{name: "owner-reference", change: func(p *corev1.PersistentVolumeClaim) {
			p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other"}}
		}},
		{
			name: "finalizer-addition",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Finalizers = append(p.Finalizers, "example.com/keep")
			},
		},
		{name: "finalizer-removal", change: func(p *corev1.PersistentVolumeClaim) { p.Finalizers = nil }},
		{
			name: "unrelated-annotation",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["example.com/edit"] = "true"
			},
		},
		{
			name:     "maintenance-without-delegated-authority",
			username: limitedUsername,
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["openbao.org/maintenance"] = "true"
			},
		},
		{
			name: "binding-marker-value",
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["pv.kubernetes.io/bind-completed"] = "no"
			},
		},
		{
			name: "binding-marker-removal",
			before: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["pv.kubernetes.io/bind-completed"] = "yes"
			},
			change: func(p *corev1.PersistentVolumeClaim) { delete(p.Annotations, "pv.kubernetes.io/bind-completed") }},
		{name: "provisioner-replacement", before: func(p *corev1.PersistentVolumeClaim) {
			p.Annotations["volume.beta.kubernetes.io/storage-provisioner"] = "original.csi.test"
		},
			change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations["volume.beta.kubernetes.io/storage-provisioner"] = "other.csi.test"
			}},
		{name: "bound-claim-resize", bound: true, change: func(p *corev1.PersistentVolumeClaim) {
			p.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Gi")
		}},
	}, 16)
	for _, annotation := range []struct{ name, key string }{
		{"ga-provisioner", "volume.kubernetes.io/storage-provisioner"},
		{"selected-node", "volume.kubernetes.io/selected-node"},
	} {
		setOriginal := func(p *corev1.PersistentVolumeClaim) { p.Annotations[annotation.key] = "original" }
		tests = append(tests,
			testCase{name: annotation.name + "-addition", change: setOriginal, allowed: true},
			testCase{name: annotation.name + "-preserved", before: setOriginal,
				change: func(p *corev1.PersistentVolumeClaim) { p.Spec.VolumeName = "pv-host" }, allowed: true},
			testCase{name: annotation.name + "-empty", change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations[annotation.key] = ""
			}},
			testCase{name: annotation.name + "-replacement", before: setOriginal, change: func(p *corev1.PersistentVolumeClaim) {
				p.Annotations[annotation.key] = "other"
			}},
			testCase{name: annotation.name + "-removal", before: setOriginal, change: func(p *corev1.PersistentVolumeClaim) {
				delete(p.Annotations, annotation.key)
			}},
			testCase{name: annotation.name + "-unrelated-annotation", change: func(p *corev1.PersistentVolumeClaim) {
				setOriginal(p)
				p.Annotations["example.com/edit"] = "true"
			}},
			testCase{name: annotation.name + "-wrong-user", change: setOriginal, username: "other-syncer"},
			testCase{name: annotation.name + "-wrong-namespace", change: setOriginal, otherNS: true},
		)
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := namespace
			if test.otherNS {
				target = otherNamespace
			}
			claim := newManagedStatefulSetPVC(target, "example", fmt.Sprintf("sync-case-%d", i))
			if test.before != nil {
				test.before(claim)
			}
			if test.bound {
				claim.Spec.VolumeName = "pv-bound"
				claim.Spec.StorageClassName = &storageClass.Name
			}
			require.NoError(t, creator.Create(ctx, claim))
			if test.bound {
				claim.Status.Phase = corev1.ClaimBound
				claim.Status.Capacity = claim.Spec.Resources.Requests.DeepCopy()
				require.NoError(t, k8sClient.Status().Update(ctx, claim))
			}
			test.change(claim)
			caller := syncer
			if test.username != "" {
				caller = newPrivilegedImpersonatedClient(t, test.username)
				if test.username == limitedUsername {
					caller = newImpersonatedClient(t, limitedUsername)
				}
			}
			err := caller.Update(ctx, claim, client.DryRunAll)
			if test.allowed {
				require.NoError(t, err)
			} else {
				requirePVCSyncDenied(t, err)
			}
		})
	}

	t.Run("create-delete-and-other-resources", func(t *testing.T) {
		claim := newManagedStatefulSetPVC(namespace, "example", "sync-create")
		requirePVCSyncDenied(t, syncer.Create(ctx, claim, client.DryRunAll))
		requirePVCSyncDenied(t, syncer.Delete(ctx, probe, client.DryRunAll))
		for _, obj := range []client.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sync-secret", Namespace: namespace, Labels: probe.Labels}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sync-pod", Namespace: namespace, Labels: probe.Labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.com/app:test"}}}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "sync-role", Namespace: namespace, Labels: probe.Labels}},
			&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "sync-binding", Namespace: namespace, Labels: probe.Labels},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "sync-role"},
			},
		} {
			requirePVCSyncDenied(t, syncer.Create(ctx, obj, client.DryRunAll))
		}
	})

	t.Run("status-subresource", func(t *testing.T) {
		claim := &corev1.PersistentVolumeClaim{}
		require.NoError(t, syncer.Get(ctx, client.ObjectKeyFromObject(probe), claim))
		claim.Status.Phase = corev1.ClaimBound
		require.NoError(t, syncer.Status().Update(ctx, claim, client.DryRunAll))
	})

	configurePVCBindingSync(t, "{}")
	require.Eventually(t, func() bool {
		err := syncer.Update(ctx, probe.DeepCopy(), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "denied request: Direct modification")
	}, 5*time.Second, 50*time.Millisecond, "removing the caller must revoke the binding allowance")
}

func grantPVCBindingSyncWriteAccess(t *testing.T, namespace, username string) {
	t.Helper()
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-binding-sync", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"get", "update"}}},
	}
	require.NoError(t, k8sClient.Create(ctx, role))
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: username}},
	}
	require.NoError(t, k8sClient.Create(ctx, binding))
}

func configurePVCBindingSync(t *testing.T, callers string) {
	t.Helper()
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: pvcSyncPolicyName}, policy))
	before := policy.DeepCopy()
	var value map[string][]string
	require.NoError(t, json.Unmarshal([]byte(callers), &value))
	for i := range policy.Spec.Variables {
		if policy.Spec.Variables[i].Name != "pvc_binding_sync_callers" {
			continue
		}
		old := policy.Spec.Variables[i].Expression
		policy.Spec.Variables[i].Expression = callers
		require.NoError(t, k8sClient.Patch(ctx, policy, client.MergeFrom(before)))
		t.Cleanup(func() {
			current := &admissionv1.ValidatingAdmissionPolicy{}
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: pvcSyncPolicyName}, current))
			original := current.DeepCopy()
			current.Spec.Variables[i].Expression = old
			require.NoError(t, k8sClient.Patch(ctx, current, client.MergeFrom(original)))
		})
		return
	}
	t.Fatal("PVC binding sync caller variable is missing")
}

func requirePVCSyncDenied(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Contains(t, err.Error(), "ValidatingAdmissionPolicy '"+pvcSyncPolicyName+"'")
	require.Contains(t, err.Error(), "denied request:")
}
