//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestVAP_PodMetadataSync(t *testing.T) {
	// Configures a shared policy; keep this test sequential.
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace, otherNamespace := newTestNamespace(t), newTestNamespace(t)
	const username = "kubernetes-super-admin"
	const limitedUsername = "pod-metadata-syncer"
	const annotationKey = "example.com/host-metadata"
	syncer := newPrivilegedImpersonatedClient(t, username)
	creator := newControllerClient(t)
	probe := newMetadataSyncPod(namespace, "probe")
	require.NoError(t, creator.Create(ctx, probe))
	probe.Annotations = map[string]string{annotationKey: "host"}
	require.Eventually(t, func() bool {
		err := syncer.Update(ctx, probe.DeepCopy(), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "denied request: Direct modification")
	}, 5*time.Second, 50*time.Millisecond)

	configurePodMetadataSync(t, map[string]map[string][]string{
		username: {namespace: {annotationKey}}, limitedUsername: {namespace: {annotationKey}},
	})
	grantPodMetadataSyncAccess(t, namespace, limitedUsername)
	t.Cleanup(func() { configurePodMetadataSync(t, map[string]map[string][]string{}) })
	require.Eventually(t, func() bool {
		return syncer.Update(ctx, probe.DeepCopy(), client.DryRunAll) == nil
	}, 5*time.Second, 50*time.Millisecond, "configured annotation update must become allowed")

	for _, component := range []string{
		"openbaocluster", "backup", "restore", "upgrade", "upgrade-snapshot", "validation-hook",
	} {
		t.Run(component, func(t *testing.T) {
			for _, test := range []struct {
				name    string
				before  func(*corev1.Pod)
				change  func(*corev1.Pod)
				user    string
				otherNS bool
				allowed bool
			}{
				{name: "add", allowed: true},
				{name: "dedicated-caller-without-cluster-admin", user: limitedUsername, allowed: true},
				{name: "change", before: func(p *corev1.Pod) { p.Annotations[annotationKey] = "previous" }, allowed: true},
				{name: "remove", before: func(p *corev1.Pod) { p.Annotations[annotationKey] = "previous" },
					change: func(p *corev1.Pod) { delete(p.Annotations, annotationKey) }, allowed: true},
				{name: "wrong-user", user: "other-syncer"},
				{name: "wrong-namespace", otherNS: true},
				{name: "unrelated-annotation", change: func(p *corev1.Pod) { p.Annotations["example.com/other"] = "not-allowed" }},
				{name: "maintenance", user: limitedUsername,
					change: func(p *corev1.Pod) { p.Annotations["openbao.org/maintenance"] = "true" }},
				{name: "owner-provenance", change: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = "other" }},
				{name: "owner-provenance-preserved", before: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = "cluster-uid" }, allowed: true},
				{name: "owner-provenance-removed", before: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = "cluster-uid" },
					change: func(p *corev1.Pod) { delete(p.Annotations, "openbao.org/owner-uid") }},
				{name: "empty-owner-provenance", before: func(p *corev1.Pod) { p.Annotations["openbao.org/owner-uid"] = "" }},
				{name: "owner-reference", change: func(p *corev1.Pod) { p.OwnerReferences[0].UID = "other" }},
				{name: "finalizer-add", change: func(p *corev1.Pod) { p.Finalizers = append(p.Finalizers, "example.com/other") }},
				{name: "finalizer-remove", change: func(p *corev1.Pod) { p.Finalizers = nil }},
				{name: "image", change: func(p *corev1.Pod) { p.Spec.Containers[0].Image = "example.com/other:test" }},
				{name: "routing-label", change: func(p *corev1.Pod) { p.Labels["openbao-active"] = "true" }},
				{name: "managed-label", change: func(p *corev1.Pod) { delete(p.Labels, "app.kubernetes.io/managed-by") }},
				{name: "native-controller-pod", allowed: true},
				{name: "missing-owner", before: func(p *corev1.Pod) { p.OwnerReferences = nil }},
				{name: "wrong-component", before: func(p *corev1.Pod) { p.Labels["openbao.org/component"] = "unknown" }},
				{name: "server-with-job-owner", before: func(p *corev1.Pod) {
					p.Labels["openbao.org/component"] = "openbaocluster"
					p.OwnerReferences[0].APIVersion, p.OwnerReferences[0].Kind = "batch/v1", "Job"
				}},
				{name: "helper-with-statefulset-owner", before: func(p *corev1.Pod) {
					p.Labels["openbao.org/component"] = "backup"
					p.OwnerReferences[0].APIVersion, p.OwnerReferences[0].Kind = "apps/v1", "StatefulSet"
				}},
				{name: "foreign-owner-api", before: func(p *corev1.Pod) { p.OwnerReferences[0].APIVersion = "example.com/v1" }},
				{name: "noncontroller-owner", before: func(p *corev1.Pod) { p.OwnerReferences[0].Controller = ptr.To(false) }},
			} {
				t.Run(test.name, func(t *testing.T) {
					ns := namespace
					if test.otherNS {
						ns = otherNamespace
					}
					pod := newMetadataSyncPod(ns, component+"-"+test.name)
					pod.Labels["openbao.org/component"] = component
					if component != "openbaocluster" {
						pod.OwnerReferences[0].APIVersion, pod.OwnerReferences[0].Kind = "batch/v1", "Job"
					}
					if test.before != nil {
						test.before(pod)
					}
					require.NoError(t, creator.Create(ctx, pod))
					if pod.Annotations == nil {
						pod.Annotations = map[string]string{}
					}
					pod.Annotations[annotationKey] = "host"
					if test.change != nil {
						test.change(pod)
					}
					caller := syncer
					if test.user != "" {
						caller = newPrivilegedImpersonatedClient(t, test.user)
						if test.user == limitedUsername {
							caller = newImpersonatedClient(t, limitedUsername)
						}
					}
					err := caller.Update(ctx, pod, client.DryRunAll)
					if test.allowed {
						require.NoError(t, err)
					} else {
						requirePVCSyncDenied(t, err)
					}
				})
			}
			t.Run("no-create-or-delete", func(t *testing.T) {
				pod := newMetadataSyncPod(namespace, component+"-no-extra-access")
				pod.Labels["openbao.org/component"] = component
				if component != "openbaocluster" {
					pod.OwnerReferences[0].APIVersion, pod.OwnerReferences[0].Kind = "batch/v1", "Job"
				}
				requirePVCSyncDenied(t, syncer.Create(ctx, pod.DeepCopy(), client.DryRunAll))
				require.NoError(t, creator.Create(ctx, pod))
				requirePVCSyncDenied(t, syncer.Delete(ctx, pod, client.GracePeriodSeconds(0), client.DryRunAll))
			})
		})
	}
	t.Run("no-create-delete-or-other-resource-access", func(t *testing.T) {
		requirePVCSyncDenied(t, syncer.Create(ctx, newMetadataSyncPod(namespace, "forbidden-create"), client.DryRunAll))
		requirePVCSyncDenied(t, syncer.Delete(ctx, probe.DeepCopy(), client.GracePeriodSeconds(0), client.DryRunAll))
		config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "managed-config", Namespace: namespace,
			Labels: probe.Labels, Annotations: map[string]string{"openbao.org/owner-uid": "cluster-uid"}}}
		require.NoError(t, creator.Create(ctx, config))
		config.Annotations[annotationKey] = "host"
		requirePVCSyncDenied(t, syncer.Update(ctx, config, client.DryRunAll))
	})
	t.Run("status-subresource-already-allowed", func(t *testing.T) {
		pod := &corev1.Pod{}
		require.NoError(t, syncer.Get(ctx, client.ObjectKeyFromObject(probe), pod))
		pod.Status.Phase = corev1.PodRunning
		require.NoError(t, syncer.Status().Update(ctx, pod, client.DryRunAll))
	})
}

func grantPodMetadataSyncAccess(t *testing.T, namespace, username string) {
	t.Helper()
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "pod-metadata-sync", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "update"}}}}
	require.NoError(t, k8sClient.Create(ctx, role))
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: username}}}
	require.NoError(t, k8sClient.Create(ctx, binding))
}

func configurePodMetadataSync(t *testing.T, callers map[string]map[string][]string) {
	t.Helper()
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: pvcSyncPolicyName}, policy))
	encoded, err := json.Marshal(callers)
	require.NoError(t, err)
	for i := range policy.Spec.Variables {
		if policy.Spec.Variables[i].Name == "pod_metadata_sync_callers" {
			policy.Spec.Variables[i].Expression = string(encoded)
			require.NoError(t, k8sClient.Update(ctx, policy))
			return
		}
	}
	t.Fatal("Pod metadata sync caller variable is missing")
}

func newMetadataSyncPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "openbao-operator",
			"openbao.org/cluster": "example", "openbao.org/component": "openbaocluster"},
		Annotations: map[string]string{},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "example",
			UID: "statefulset-uid", Controller: ptr.To(true)}}, Finalizers: []string{"example.com/hold"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "openbao", Image: "registry.k8s.io/pause:3.9"}},
			RestartPolicy: corev1.RestartPolicyNever}}
}
