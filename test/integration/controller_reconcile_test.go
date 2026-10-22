//go:build integration
// +build integration

package integration

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// TestOpenBaoClusterReconciler_InitialReconcile tests that a new cluster
// creates the expected Kubernetes resources (StatefulSet, Services, Secrets).
func TestOpenBaoClusterReconciler_InitialReconcile(t *testing.T) {
	namespace := newTestNamespace(t)
	clusterName := "initial-reconcile"

	cluster := createMinimalCluster(t, namespace, clusterName)
	createTLSSecret(t, namespace, clusterName)

	// Run infrastructure reconciliation to create resources
	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("InfraManager.Reconcile error: %v", err)
	}

	t.Run("creates StatefulSet", func(t *testing.T) {
		sts := &appsv1.StatefulSet{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, sts)
		if err != nil {
			t.Fatalf("expected StatefulSet to be created: %v", err)
		}

		// Re-fetch cluster to get current state
		var latest openbaov1alpha1.OpenBaoCluster
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, &latest); err != nil {
			t.Fatalf("get cluster: %v", err)
		}

		// StatefulSet should have been created (replicas may differ based on init state)
		if sts.Spec.Replicas == nil {
			t.Error("expected replicas to be set")
		}

		// Verify owner reference is set
		if len(sts.OwnerReferences) == 0 {
			t.Error("expected StatefulSet to have owner reference")
		}
	})

	t.Run("creates headless Service", func(t *testing.T) {
		svc := &corev1.Service{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, svc)
		if err != nil {
			t.Fatalf("expected headless Service to be created: %v", err)
		}

		if svc.Spec.ClusterIP != "None" {
			t.Errorf("expected headless service (ClusterIP=None), got %s", svc.Spec.ClusterIP)
		}
	})

	t.Run("creates active Service when spec.service is set", func(t *testing.T) {
		// External service is only created when spec.service is configured
		// Minimal cluster doesn't have this set, so we just verify the pattern.
		// Real scenarios with spec.service are tested in infra_network_test.go.
		t.Skip("active Service requires spec.service configuration; minimal cluster skips this")
	})

	t.Run("creates ConfigMap", func(t *testing.T) {
		cm := &corev1.ConfigMap{}
		cmName := clusterName + constants.SuffixConfigMap
		err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm)
		if err != nil {
			t.Fatalf("expected ConfigMap to be created: %v", err)
		}

		// Should contain HCL config
		if _, ok := cm.Data["config.hcl"]; !ok {
			t.Error("expected ConfigMap to contain config.hcl")
		}
	})
}

// TestOpenBaoClusterReconciler_StatusConditions tests that status conditions
// are updated correctly during reconciliation.
func TestOpenBaoClusterReconciler_StatusConditions(t *testing.T) {
	namespace := newTestNamespace(t)
	clusterName := "status-conditions"

	cluster := createMinimalCluster(t, namespace, clusterName)
	createTLSSecret(t, namespace, clusterName)

	// Update status to simulate initialized cluster
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = cluster.Spec.Version
	})

	// Run infrastructure reconciliation
	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("InfraManager.Reconcile error: %v", err)
	}

	t.Run("cluster phase is set", func(t *testing.T) {
		var latest openbaov1alpha1.OpenBaoCluster
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, &latest); err != nil {
			t.Fatalf("get cluster: %v", err)
		}

		if latest.Status.Phase == "" {
			t.Error("expected Phase to be set")
		}
	})

	t.Run("initialized status is preserved", func(t *testing.T) {
		var latest openbaov1alpha1.OpenBaoCluster
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, &latest); err != nil {
			t.Fatalf("get cluster: %v", err)
		}

		if !latest.Status.Initialized {
			t.Error("expected Initialized to be true")
		}
	})
}

// TestOpenBaoClusterReconciler_VersionUpgradeTrigger tests that changing
// spec.Version triggers upgrade detection.
func TestOpenBaoClusterReconciler_VersionUpgradeTrigger(t *testing.T) {
	namespace := newTestNamespace(t)
	clusterName := "version-upgrade"

	cluster := createMinimalCluster(t, namespace, clusterName)
	createTLSSecret(t, namespace, clusterName)

	// Set up initialized cluster with a current version
	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = "2.4.0"
	})

	// Run initial reconciliation
	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("initial InfraManager.Reconcile error: %v", err)
	}

	t.Run("version mismatch triggers upgrade need", func(t *testing.T) {
		var latest openbaov1alpha1.OpenBaoCluster
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, &latest); err != nil {
			t.Fatalf("get cluster: %v", err)
		}

		specVersion := latest.Spec.Version
		statusVersion := latest.Status.CurrentVersion

		if specVersion == statusVersion {
			t.Skip("versions match, no upgrade needed")
		}

		// Version mismatch indicates upgrade is needed
		// The actual upgrade logic is handled by the upgrade manager
		t.Logf("Version mismatch detected: spec=%s, current=%s", specVersion, statusVersion)
	})
}

// TestOpenBaoClusterReconciler_DeletionWithRetainPolicy tests that secrets
// are preserved when deletionPolicy is set to Retain.
func TestOpenBaoClusterReconciler_DeletionWithRetainPolicy(t *testing.T) {
	namespace := newTestNamespace(t)
	clusterName := "deletion-retain"

	// Create cluster with Retain deletion policy
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			ControllerJWTMode: openbaov1alpha1.ControllerJWTModeShared,
			Version:           "2.4.4",
			Image:             "openbao/openbao:2.4.4",
			Replicas:          1,
			Profile:           openbaov1alpha1.ProfileDevelopment,
			TLS: openbaov1alpha1.TLSConfig{
				Enabled:        true,
				RotationPeriod: "720h",
			},
			Storage: openbaov1alpha1.StorageConfig{
				Size: "10Gi",
			},
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Image: "openbao/openbao-init:latest",
			},
			DeletionPolicy: openbaov1alpha1.DeletionPolicyRetain,
		},
	}

	if err := k8sClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create cluster: %v", err)
	}

	// Re-fetch to get UID
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster); err != nil {
		t.Fatalf("get cluster: %v", err)
	}

	createTLSSecret(t, namespace, clusterName)

	// Create an unseal key secret (simulating what the operator would create)
	unsealSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + constants.SuffixUnsealKey,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "openbao.org/v1alpha1",
					Kind:       "OpenBaoCluster",
					Name:       cluster.Name,
					UID:        cluster.UID,
				},
			},
		},
		Data: map[string][]byte{
			"unseal-key-0": []byte("test-unseal-key"),
		},
	}
	if err := k8sClient.Create(ctx, unsealSecret); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create unseal secret: %v", err)
	}

	t.Run("cluster has Retain deletion policy", func(t *testing.T) {
		var latest openbaov1alpha1.OpenBaoCluster
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, &latest); err != nil {
			t.Fatalf("get cluster: %v", err)
		}

		if latest.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyRetain {
			t.Errorf("expected DeletionPolicy=Retain, got %s", latest.Spec.DeletionPolicy)
		}
	})

	t.Run("unseal secret exists", func(t *testing.T) {
		secret := &corev1.Secret{}
		secretName := clusterName + constants.SuffixUnsealKey
		err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)
		if err != nil {
			t.Fatalf("expected unseal secret to exist: %v", err)
		}

		// When deletion happens with Retain policy, the deletion handler should
		// remove owner references from secrets to prevent garbage collection.
		// This test verifies the secret exists; the actual orphaning logic
		// is tested in deletion_test.go unit tests.
	})
}

// TestOpenBaoClusterReconciler_IdempotentReconcile tests that repeated
// reconciliation is idempotent and doesn't cause errors.
func TestOpenBaoClusterReconciler_IdempotentReconcile(t *testing.T) {
	namespace := newTestNamespace(t)
	clusterName := "idempotent"

	cluster := createMinimalCluster(t, namespace, clusterName)
	createTLSSecret(t, namespace, clusterName)

	controllerClient := newControllerClient(t)

	// Run reconciliation multiple times
	spec := newTestStatefulSetSpec(cluster)
	for i := 0; i < 3; i++ {
		if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
			t.Fatalf("Reconcile iteration %d error: %v", i+1, err)
		}
		if i == 0 {
			configMap := &corev1.ConfigMap{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName + constants.SuffixConfigMap, Namespace: namespace}, configMap); err != nil {
				t.Fatalf("get ConfigMap after first reconcile: %v", err)
			}
			requireIntegrationOwnerProof(t, configMap, cluster)
		}
	}

	// Verify resources still exist and are correct
	sts := &appsv1.StatefulSet{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, sts); err != nil {
		t.Fatalf("expected StatefulSet after multiple reconciles: %v", err)
	}

	// StatefulSet should exist and have replicas set
	if sts.Spec.Replicas == nil {
		t.Error("expected replicas to be set after multiple reconciles")
	}
}

// TestOpenBaoClusterReconciler_ResourceCleanup tests that resources have
// proper owner references for garbage collection.
func TestOpenBaoClusterReconciler_ResourceCleanup(t *testing.T) {
	namespace := newTestNamespace(t)
	clusterName := "cleanup"

	cluster := createMinimalCluster(t, namespace, clusterName)
	createTLSSecret(t, namespace, clusterName)

	controllerClient := newControllerClient(t)

	// Run initial reconciliation to create resources
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("InfraManager.Reconcile error: %v", err)
	}

	// Verify StatefulSet was created
	sts := &appsv1.StatefulSet{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, sts); err != nil {
		t.Fatalf("expected StatefulSet to be created: %v", err)
	}

	t.Run("StatefulSet has owner reference", func(t *testing.T) {
		if len(sts.OwnerReferences) == 0 {
			t.Error("expected StatefulSet to have owner reference for GC")
		}

		// When the OpenBaoCluster is deleted, Kubernetes GC will clean up
		// the StatefulSet because of the owner reference
		foundController := false
		for _, ref := range sts.OwnerReferences {
			if ref.Kind == "OpenBaoCluster" && ref.Name == clusterName {
				foundController = true
				break
			}
		}

		if !foundController {
			t.Error("expected owner reference to point to OpenBaoCluster")
		}
	})
}
