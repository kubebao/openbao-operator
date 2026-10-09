//go:build integration
// +build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/dc-tec/openbao-operator/api/v1alpha1"
	"github.com/dc-tec/openbao-operator/internal/platform/constants"
	provisionerpkg "github.com/dc-tec/openbao-operator/internal/service/provisioner"
)

func newPrivilegedImpersonatedClient(t *testing.T, username string) client.Client {
	t.Helper()

	return newImpersonatedClientWithGroups(t, username, "system:masters")
}

func ensureControllerRBACManager(t *testing.T, namespace string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "controller-rbac-manager",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     []string{"create", "delete", "get", "patch", "update"},
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create controller rbac role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "controller-rbac-manager-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "openbao-operator-controller",
				Namespace: "openbao-operator-system",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create controller rbac rolebinding: %v", err)
	}
}

func grantPodDeleteAccess(t *testing.T, namespace, username string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-pod-maintenance",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"delete", "get"},
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create pod maintenance role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-pod-maintenance-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create pod maintenance binding: %v", err)
	}
}

func grantPodUpdateAccess(t *testing.T, namespace, username string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-pod-update",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "update"},
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create pod update role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-pod-update-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create pod update binding: %v", err)
	}
}

func grantServiceAccountWriteAccess(t *testing.T, namespace, username string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-serviceaccount-write",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"create", "get", "update"},
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create serviceaccount write role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-serviceaccount-write-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create serviceaccount write binding: %v", err)
	}
}

func grantJobCreateAccess(t *testing.T, namespace, username string) {
	t.Helper()

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lifecycle-job-create",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{"batch"},
			Resources: []string{"jobs"},
			Verbs:     []string{"create"},
		}},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create Job writer role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lifecycle-job-create-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{{
			Kind:     "User",
			Name:     username,
			APIGroup: "rbac.authorization.k8s.io",
		}},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create Job writer binding: %v", err)
	}
}

func grantClusterMaintenanceAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-maintenance-access",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"openbao.org"},
				Resources:     []string{"openbaoclusters"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"get", "maintenance"},
			},
			{
				APIGroups:     []string{"openbao.org"},
				Resources:     []string{"openbaoclusters/status"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"get"},
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create maintenance role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-maintenance-access-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create maintenance rolebinding: %v", err)
	}
}

func createClusterForServiceMonitorGuard(t *testing.T, namespace, clusterName string) *openbaov1alpha1.OpenBaoCluster {
	t.Helper()

	cluster := newMinimalClusterObj(namespace, clusterName)
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster %s/%s: %v", namespace, clusterName, err)
	}
	var persisted openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterName}, &persisted); err != nil {
		t.Fatalf("get OpenBaoCluster %s/%s: %v", namespace, clusterName, err)
	}
	return &persisted
}

func newServiceMonitorObject(namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("monitoring.coreos.com/v1")
	obj.SetKind("ServiceMonitor")
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.Object["spec"] = map[string]interface{}{
		"endpoints": []interface{}{
			map[string]interface{}{
				"port": "metrics",
				"path": "/v1/sys/metrics",
			},
		},
		"namespaceSelector": map[string]interface{}{
			"matchNames": []interface{}{namespace},
		},
		"selector": map[string]interface{}{
			"matchLabels": map[string]interface{}{
				constants.LabelAppName: constants.LabelValueAppNameOpenBao,
			},
		},
	}
	return obj
}

func newOwnedServiceMonitorObject(cluster *openbaov1alpha1.OpenBaoCluster) *unstructured.Unstructured {
	obj := newServiceMonitorObject(cluster.Namespace, cluster.Name+"-metrics")
	obj.SetLabels(map[string]string{
		constants.LabelAppName:          constants.LabelValueAppNameOpenBao,
		constants.LabelAppManagedBy:     constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelAppComponent:     "metrics",
		constants.LabelOpenBaoComponent: "metrics",
		constants.LabelOpenBaoCluster:   cluster.Name,
	})
	controller := true
	obj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: "openbao.org/v1alpha1",
			Kind:       "OpenBaoCluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
			Controller: &controller,
		},
	})
	return obj
}

func newManagedStatefulSetPVC(namespace, clusterName, name string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				constants.LabelAppName:        constants.LabelValueAppNameOpenBao,
				constants.LabelAppInstance:    clusterName,
				constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster: clusterName,
			},
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: "example-cluster-uid",
			},
			Finalizers: []string{"kubernetes.io/pvc-protection"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
}

func createManagedMaintenancePod(t *testing.T, c client.Client, namespace, clusterName, podName string) {
	t.Helper()

	cluster := newMinimalClusterObj(namespace, clusterName)
	cluster.Spec.InitContainer = &openbaov1alpha1.InitContainerConfig{
		Image: "openbao/openbao-init:latest",
	}
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeOperatorManaged
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Annotations: map[string]string{
				constants.AnnotationMaintenance:     testTrueString,
				constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
			},
			Labels: map[string]string{
				"app.kubernetes.io/name":       "openbao",
				"app.kubernetes.io/instance":   clusterName,
				"app.kubernetes.io/managed-by": "openbao-operator",
				"openbao.org/cluster":          clusterName,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "pause",
					Image: "registry.k8s.io/pause:3.9",
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("create managed maintenance pod: %v", err)
	}
}

func TestVAP_LockManagedRBAC_RestrictsOperatorServiceMonitorOwnership(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	controllerClient := newPrivilegedImpersonatedClient(t, controllerUsername)

	rogue := newServiceMonitorObject(namespace, "rogue-monitor")
	var rogueDenied bool
	for attempt := 0; attempt < 25; attempt++ {
		err := controllerClient.Create(ctx, rogue.DeepCopy())
		if err == nil {
			_ = k8sClient.Delete(ctx, rogue)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "ServiceMonitors that match the OpenBao metrics ownership shape") {
			t.Fatalf("unexpected error message: %v", err)
		}
		rogueDenied = true
		break
	}
	if !rogueDenied {
		t.Fatalf("expected VAP to deny operator-created rogue ServiceMonitor after retries")
	}

	ownedCluster := createClusterForServiceMonitorGuard(t, namespace, "owned")
	ownedMonitor := newOwnedServiceMonitorObject(ownedCluster)
	if err := controllerClient.Create(ctx, ownedMonitor); err != nil {
		t.Fatalf("expected owned ServiceMonitor create to succeed, got: %v", err)
	}

	var latestOwned unstructured.Unstructured
	latestOwned.SetAPIVersion("monitoring.coreos.com/v1")
	latestOwned.SetKind("ServiceMonitor")
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ownedMonitor.GetName()}, &latestOwned); err != nil {
		t.Fatalf("get owned ServiceMonitor: %v", err)
	}
	originalOwned := latestOwned.DeepCopy()
	latestOwned.SetAnnotations(map[string]string{"example.com/reconcile": "true"})
	if err := controllerClient.Patch(ctx, &latestOwned, client.MergeFrom(originalOwned)); err != nil {
		t.Fatalf("expected owned ServiceMonitor update to succeed, got: %v", err)
	}

	takeoverCluster := createClusterForServiceMonitorGuard(t, namespace, "takeover")
	userOwned := newServiceMonitorObject(namespace, "takeover-metrics")
	userOwned.SetLabels(map[string]string{
		"release": "kube-prometheus-stack",
	})
	if err := k8sClient.Create(ctx, userOwned); err != nil {
		t.Fatalf("create user-owned ServiceMonitor: %v", err)
	}

	var latestUserOwned unstructured.Unstructured
	latestUserOwned.SetAPIVersion("monitoring.coreos.com/v1")
	latestUserOwned.SetKind("ServiceMonitor")
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: userOwned.GetName()}, &latestUserOwned); err != nil {
		t.Fatalf("get user-owned ServiceMonitor: %v", err)
	}
	originalUserOwned := latestUserOwned.DeepCopy()
	ownedShape := newOwnedServiceMonitorObject(takeoverCluster)
	latestUserOwned.SetLabels(ownedShape.GetLabels())
	latestUserOwned.SetOwnerReferences(ownedShape.GetOwnerReferences())
	err := controllerClient.Patch(ctx, &latestUserOwned, client.MergeFrom(originalUserOwned))
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "ServiceMonitors that match the OpenBao metrics ownership shape") {
		t.Fatalf("unexpected takeover error message: %v", err)
	}

	err = controllerClient.Delete(ctx, userOwned)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "ServiceMonitors that match the OpenBao metrics ownership shape") {
		t.Fatalf("unexpected delete error message: %v", err)
	}

	if err := controllerClient.Delete(ctx, ownedMonitor); err != nil {
		t.Fatalf("expected owned ServiceMonitor delete to succeed, got: %v", err)
	}
}

func TestVAP_LockManagedRBAC_DeniesForgedServiceAccountOwnerUID(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	editorUsername := "serviceaccount-provenance-editor"
	grantServiceAccountWriteAccess(t, namespace, editorUsername)
	cluster := createClusterForServiceMonitorGuard(t, namespace, "forged-sa")

	forged := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + constants.SuffixUpgradeServiceAccount,
			Namespace: namespace,
			Labels: map[string]string{
				constants.LabelAppName:                   constants.LabelValueAppNameOpenBao,
				constants.LabelAppInstance:               cluster.Name,
				constants.LabelAppManagedBy:              constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster:            cluster.Name,
				constants.LabelOpenBaoComponent:          constants.ServiceAccountRoleUpgrade,
				constants.LabelOpenBaoServiceAccountRole: constants.ServiceAccountRoleUpgrade,
			},
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
			},
		},
	}

	editorClient := newImpersonatedClient(t, editorUsername)
	for attempt := 0; attempt < 25; attempt++ {
		err := editorClient.Create(ctx, forged.DeepCopy())
		if err == nil {
			_ = k8sClient.Delete(ctx, forged)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "openbao.org/owner-uid annotation is reserved") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny forged ServiceAccount owner UID provenance")
}

func TestVAP_LockManagedRBAC_DeniesForgedLifecycleJobOwnerUID(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	username := "lifecycle-job-creator"
	grantJobCreateAccess(t, namespace, username)
	cluster := createMinimalCluster(t, namespace, "lifecycle-owner-proof")
	jobCreator := newImpersonatedClient(t, username)
	ownerRef := *metav1.NewControllerRef(
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
	)

	ownerReferenceOnly := newLifecycleCollisionJob(namespace, "owner-reference-only", ownerRef)
	if err := jobCreator.Create(ctx, ownerReferenceOnly); err != nil {
		t.Fatalf("create Job with owner reference only: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, ownerReferenceOnly)
	})

	forged := newLifecycleCollisionJob(namespace, "forged-owner-proof", ownerRef)
	forged.Annotations = map[string]string{
		constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
	}
	err := jobCreator.Create(ctx, forged)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "openbao.org/owner-uid annotation is reserved") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func newLifecycleCollisionJob(
	namespace string,
	name string,
	ownerRef metav1.OwnerReference,
) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "collision",
				Image:   "example.invalid/collision:latest",
				Command: []string{"true"},
			}},
		}}},
	}
}

func TestVAP_LockManagedRBAC_AllowsStatefulSetControllerManagedPVC(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	editorClient := newPrivilegedImpersonatedClient(t, "managed-pvc-editor")

	var deniedForgedPVC bool
	for attempt := 0; attempt < 25; attempt++ {
		forged := newManagedStatefulSetPVC(namespace, "example", fmt.Sprintf("data-example-forged-%d", attempt))
		err := editorClient.Create(ctx, forged.DeepCopy())
		if err == nil {
			_ = k8sClient.Delete(ctx, forged)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "openbao.org/owner-uid annotation is reserved") {
			t.Fatalf("unexpected error message: %v", err)
		}
		deniedForgedPVC = true
		break
	}
	if !deniedForgedPVC {
		t.Fatalf("expected VAP to deny forged managed PVC owner UID provenance")
	}

	statefulSetControllerClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:kube-system:statefulset-controller",
	)
	managedPVC := newManagedStatefulSetPVC(namespace, "example", "data-example-0")
	if err := statefulSetControllerClient.Create(ctx, managedPVC); err != nil {
		t.Fatalf("expected StatefulSet controller managed PVC create to succeed, got: %v", err)
	}

	if err := editorClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before direct editor update: %v", err)
	}
	if managedPVC.Annotations == nil {
		managedPVC.Annotations = map[string]string{}
	}
	managedPVC.Annotations["example.com/direct-edit"] = "true"
	err := editorClient.Update(ctx, managedPVC)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
		t.Fatalf("unexpected direct PVC update error message: %v", err)
	}

	schedulerClient := newPrivilegedImpersonatedClient(t, "system:kube-scheduler")
	if err := schedulerClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before scheduler bind update: %v", err)
	}
	if managedPVC.Annotations == nil {
		managedPVC.Annotations = map[string]string{}
	}
	managedPVC.Annotations["volume.kubernetes.io/selected-node"] = "worker-0"
	if err := schedulerClient.Update(ctx, managedPVC); err != nil {
		t.Fatalf("expected kube-scheduler managed PVC selected-node update to succeed, got: %v", err)
	}

	// AKS runs its managed scheduler and volume controllers as "aksService".
	aksControlPlaneClient := newPrivilegedImpersonatedClient(t, "aksService")
	if err := aksControlPlaneClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before AKS control plane bind update: %v", err)
	}
	managedPVC.Annotations["volume.kubernetes.io/selected-node"] = "aks-zone1-00000000-vms1"
	if err := aksControlPlaneClient.Update(ctx, managedPVC); err != nil {
		t.Fatalf("expected AKS control plane managed PVC selected-node update to succeed, got: %v", err)
	}

	if err := aksControlPlaneClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before AKS control plane label update: %v", err)
	}
	managedPVC.Labels["example.com/direct-edit"] = "true"
	err = aksControlPlaneClient.Update(ctx, managedPVC)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
		t.Fatalf("unexpected AKS control plane PVC label update error message: %v", err)
	}
	delete(managedPVC.Labels, "example.com/direct-edit")
	if err := aksControlPlaneClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC after denied AKS control plane label update: %v", err)
	}

	storageProvisionerClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:storage-system:example-csi-provisioner",
	)
	if err := storageProvisionerClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before storage provisioner update: %v", err)
	}
	if managedPVC.Annotations == nil {
		managedPVC.Annotations = map[string]string{}
	}
	managedPVC.Annotations["volume.kubernetes.io/storage-provisioner"] = "example.csi.test"
	managedPVC.Finalizers = append(
		managedPVC.Finalizers,
		"external-provisioner.volume.kubernetes.io/finalizer",
	)
	if err := storageProvisionerClient.Update(ctx, managedPVC); err != nil {
		t.Fatalf("expected CSI provisioner managed PVC metadata update to succeed, got: %v", err)
	}

	persistentVolumeBinderClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:kube-system:persistent-volume-binder",
	)
	if err := persistentVolumeBinderClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before persistent volume binder update: %v", err)
	}
	managedPVC.Spec.VolumeName = "pv-example"
	if err := persistentVolumeBinderClient.Update(ctx, managedPVC); err != nil {
		t.Fatalf("expected persistent-volume-binder managed PVC bind update to succeed, got: %v", err)
	}

	pvcProtectionClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:kube-system:pvc-protection-controller",
	)
	if err := pvcProtectionClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedPVC.Name}, managedPVC); err != nil {
		t.Fatalf("get managed PVC before PVC protection finalizer update: %v", err)
	}
	managedPVC.Finalizers = []string{"external-provisioner.volume.kubernetes.io/finalizer"}
	if err := pvcProtectionClient.Update(ctx, managedPVC); err != nil {
		t.Fatalf("expected pvc-protection-controller managed PVC finalizer update to succeed, got: %v", err)
	}
}

func TestVAP_LockManagedRBAC_DeniesDirectMutationOfControllerManagedRole(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	ensureControllerRBACManager(t, namespace)
	controllerClient := newPrivilegedImpersonatedClient(t, controllerUsername)
	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-serviceaccount-role",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "openbao",
				"app.kubernetes.io/instance":   "example",
				"app.kubernetes.io/managed-by": "openbao-operator",
				"openbao.org/cluster":          "example",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups:     []string{""},
				Resources:     []string{"pods"},
				ResourceNames: []string{"example-0", "example-1", "example-2"},
				Verbs:         []string{"patch", "update"},
			},
		},
	}
	if err := controllerClient.Create(ctx, role); err != nil {
		t.Fatalf("create managed Role: %v", err)
	}

	for attempt := 0; attempt < 25; attempt++ {
		var latest rbacv1.Role
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: role.Name}, &latest); err != nil {
			t.Fatalf("get managed Role: %v", err)
		}

		original := latest.DeepCopy()
		latest.Rules = append(latest.Rules, rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get"},
		})
		err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original))
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny direct mutation of controller-managed Role after retries")
}

func TestVAP_LockManagedRBAC_DeniesMaintenanceMutationWithoutClusterMaintenanceVerb(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	clusterName := "example"
	podName := "example-maintenance-pod"
	editorUsername := "maintenance-editor-no-verb"
	controllerClient := newPrivilegedImpersonatedClient(t, controllerUsername)
	createManagedMaintenancePod(t, controllerClient, namespace, clusterName, podName)
	grantPodDeleteAccess(t, namespace, editorUsername)

	editorClient := newImpersonatedClient(t, editorUsername)
	for attempt := 0; attempt < 25; attempt++ {
		err := editorClient.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace}})
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny maintenance mutation without the cluster maintenance verb")
}

func TestVAP_LockManagedRBAC_AllowsMaintenanceMutationWithClusterMaintenanceVerb(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	clusterName := "example"
	podName := "example-maintenance-pod"
	editorUsername := "maintenance-editor"
	controllerClient := newPrivilegedImpersonatedClient(t, controllerUsername)
	createManagedMaintenancePod(t, controllerClient, namespace, clusterName, podName)
	grantPodDeleteAccess(t, namespace, editorUsername)
	grantClusterMaintenanceAccess(t, namespace, clusterName, editorUsername)

	editorClient := newImpersonatedClient(t, editorUsername)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace}}
	if err := editorClient.Delete(ctx, pod); err != nil {
		t.Fatalf("expected maintenance-authorized pod delete to succeed, got: %v", err)
	}

	var deleted corev1.Pod
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, &deleted); err == nil {
		t.Fatalf("expected Pod %s/%s to be deleted", namespace, podName)
	}
}

func TestVAP_LockManagedRBAC_DeniesMaintenanceRelabelToAuthorizedCluster(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	victimClusterName := "victim"
	attackerClusterName := "attacker"
	podName := "victim-maintenance-pod"
	editorUsername := "maintenance-relabel-editor"
	controllerClient := newPrivilegedImpersonatedClient(t, controllerUsername)
	createManagedMaintenancePod(t, controllerClient, namespace, victimClusterName, podName)
	grantPodUpdateAccess(t, namespace, editorUsername)
	grantClusterMaintenanceAccess(t, namespace, attackerClusterName, editorUsername)

	editorClient := newImpersonatedClient(t, editorUsername)
	pod := &corev1.Pod{}
	if err := editorClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, pod); err != nil {
		t.Fatalf("get managed maintenance pod: %v", err)
	}
	pod.Labels["openbao.org/cluster"] = attackerClusterName
	pod.Labels["app.kubernetes.io/instance"] = attackerClusterName

	for attempt := 0; attempt < 25; attempt++ {
		err := editorClient.Update(ctx, pod)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny maintenance relabel against a different cluster")
}

func TestVAP_LockManagedRBAC_DeniesDirectMutationOfProvisionerManagedRoleBinding(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	ensureProvisionerRBACApplied(t)

	namespace := newTestNamespace(t)
	provisionerClient := newPrivilegedImpersonatedClient(t, provisionerUsername)
	tenantRole := provisionerpkg.GenerateTenantRole(namespace)
	if err := provisionerClient.Create(ctx, tenantRole); err != nil {
		t.Fatalf("create tenant Role: %v", err)
	}

	roleBinding := provisionerpkg.GenerateTenantRoleBinding(namespace, provisionerpkg.OperatorServiceAccount{
		Name:      "openbao-operator-controller",
		Namespace: "openbao-operator-system",
	})
	if err := provisionerClient.Create(ctx, roleBinding); err != nil {
		t.Fatalf("create managed RoleBinding: %v", err)
	}

	for attempt := 0; attempt < 25; attempt++ {
		var latest rbacv1.RoleBinding
		roleBindingKey := types.NamespacedName{Namespace: namespace, Name: roleBinding.Name}
		if err := k8sClient.Get(ctx, roleBindingKey, &latest); err != nil {
			t.Fatalf("get managed RoleBinding: %v", err)
		}

		original := latest.DeepCopy()
		latest.Subjects = append(latest.Subjects, rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      "unexpected",
			Namespace: namespace,
		})
		err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original))
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny direct mutation of provisioner-managed RoleBinding after retries")
}

func TestVAP_LockManagedRBAC_AllowsNamespaceControllerServiceAccountDeleteOnly(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	ensureProvisionerRBACApplied(t)

	namespace := newTestNamespace(t)
	provisionerClient := newPrivilegedImpersonatedClient(t, provisionerUsername)
	namespaceControllerClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:kube-system:namespace-controller",
	)
	tenantNamespaceControllerClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:"+namespace+":namespace-controller",
	)

	tenantRole := provisionerpkg.GenerateTenantRole(namespace)
	if err := provisionerClient.Create(ctx, tenantRole); err != nil {
		t.Fatalf("create tenant Role: %v", err)
	}

	roleBinding := provisionerpkg.GenerateTenantRoleBinding(namespace, provisionerpkg.OperatorServiceAccount{
		Name:      "openbao-operator-controller",
		Namespace: "openbao-operator-system",
	})
	if err := provisionerClient.Create(ctx, roleBinding); err != nil {
		t.Fatalf("create managed RoleBinding: %v", err)
	}

	roleKey := types.NamespacedName{Namespace: namespace, Name: tenantRole.Name}
	var updateDenied bool
	for attempt := 0; attempt < 25; attempt++ {
		var latestRole rbacv1.Role
		if err := namespaceControllerClient.Get(ctx, roleKey, &latestRole); err != nil {
			t.Fatalf("get managed Role before namespace controller update: %v", err)
		}
		latestRole.Rules = append(latestRole.Rules, rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get"},
		})
		err := namespaceControllerClient.Update(ctx, &latestRole)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
			t.Fatalf("unexpected namespace controller update error: %v", err)
		}
		updateDenied = true
		break
	}
	if !updateDenied {
		t.Fatal("expected namespace controller ServiceAccount update to be denied")
	}

	err := tenantNamespaceControllerClient.Delete(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleBinding.Name, Namespace: namespace},
	})
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
		t.Fatalf("unexpected tenant namespace controller delete error: %v", err)
	}

	if err := namespaceControllerClient.Delete(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleBinding.Name, Namespace: namespace},
	}); err != nil {
		t.Fatalf("expected namespace controller ServiceAccount to delete managed RoleBinding, got: %v", err)
	}
	if err := namespaceControllerClient.Delete(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: tenantRole.Name, Namespace: namespace},
	}); err != nil {
		t.Fatalf("expected namespace controller ServiceAccount to delete managed Role, got: %v", err)
	}
}

func TestVAP_LockManagedRBAC_DoesNotTrustCertManagerNamespace(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	controllerClient := newPrivilegedImpersonatedClient(t, controllerUsername)
	certManagerClient := newPrivilegedImpersonatedClient(
		t,
		"system:serviceaccount:cert-manager:cert-manager",
	)

	managedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls-server",
			Namespace: namespace,
			Labels: map[string]string{
				constants.LabelAppName:        constants.LabelValueAppNameOpenBao,
				constants.LabelAppInstance:    "example",
				constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster: "example",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte("managed-certificate"),
			corev1.TLSPrivateKeyKey: []byte("managed-key"),
		},
	}
	if err := controllerClient.Create(ctx, managedSecret); err != nil {
		t.Fatalf("create managed Secret: %v", err)
	}

	var managedMutationDenied bool
	for attempt := 0; attempt < 25; attempt++ {
		var latest corev1.Secret
		if err := certManagerClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: managedSecret.Name}, &latest); err != nil {
			t.Fatalf("get managed Secret: %v", err)
		}
		latest.Data[corev1.TLSCertKey] = []byte("replaced-certificate")
		err := certManagerClient.Update(ctx, &latest)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "Direct modification of OpenBao-managed resources is prohibited") {
			t.Fatalf("unexpected error message: %v", err)
		}
		managedMutationDenied = true
		break
	}
	if !managedMutationDenied {
		t.Fatal("expected the managed Secret mutation to be denied")
	}

	externalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-tls",
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte("external-certificate"),
			corev1.TLSPrivateKeyKey: []byte("external-key"),
		},
	}
	if err := certManagerClient.Create(ctx, externalSecret); err != nil {
		t.Fatalf("expected cert-manager to create an external TLS Secret, got: %v", err)
	}
}
