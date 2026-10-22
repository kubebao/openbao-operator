//go:build integration
// +build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	bootstrapmanager "github.com/kubebao/openbao-operator/internal/service/bootstrap"
	identitymanager "github.com/kubebao/openbao-operator/internal/service/identity"
	networkingmanager "github.com/kubebao/openbao-operator/internal/service/networking"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

func requireAdmissionDenied(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected admission to deny the request, got nil error")
	}

	if apierrors.IsForbidden(err) {
		return
	}

	var apiStatus apierrors.APIStatus
	if errors.As(err, &apiStatus) {
		status := apiStatus.Status()
		if status.Code == http.StatusForbidden || status.Reason == metav1.StatusReasonForbidden {
			return
		}
	}

	if strings.Contains(err.Error(), "is forbidden") {
		return
	}

	t.Fatalf("expected Forbidden admission error, got %T: %v", err, err)
}

func newTestNamespace(t *testing.T) string {
	t.Helper()

	base := strings.ToLower(t.Name())
	base = strings.ReplaceAll(base, "/", "-")
	base = strings.ReplaceAll(base, "_", "-")
	if len(base) > 40 {
		base = base[:40]
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("it-%s-%d", base, time.Now().UnixNano()),
		},
	}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}

	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), ns)
	})

	return ns.Name
}

func waitForOpenBaoClusterAdmissionPolicies(t *testing.T, namespace string) {
	t.Helper()

	ensureDefaultAdmissionPoliciesApplied(t)

	for attempt := 0; attempt < 25; attempt++ {
		invalid := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "openbao.org/v1alpha1",
				"kind":       "OpenBaoCluster",
				"metadata": map[string]any{
					"name":      fmt.Sprintf("cluster-policy-probe-%d", attempt),
					"namespace": namespace,
				},
				"spec": map[string]any{
					"version":           testOpenBaoVersion244,
					"image":             testOpenBaoImage244,
					"replicas":          int64(3),
					"profile":           "Development",
					"controllerJWTMode": "Shared",
					"tls": map[string]any{
						"enabled":        true,
						"rotationPeriod": "720h",
					},
					"storage": map[string]any{
						"size": "10Gi",
					},
					"initContainer": map[string]any{
						"enabled": false,
					},
				},
			},
		}

		err := k8sClient.Create(ctx, invalid)
		if err == nil {
			_ = k8sClient.Delete(ctx, invalid)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		return
	}

	t.Fatalf("expected OpenBaoCluster admission policies to become active after retries")
}

func createTLSSecret(t *testing.T, namespace, clusterName string) {
	t.Helper()

	caPEM := []byte("test-ca")
	secretName := clusterName + constants.SuffixTLSServer
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"tls.crt": []byte("test-cert"),
			"tls.key": []byte("test-key"),
			"ca.crt":  caPEM,
		},
	}

	if err := k8sClient.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create TLS secret: %v", err)
	}

	createCASecret(t, namespace, clusterName, caPEM)
}

func createCASecret(t *testing.T, namespace, clusterName string, caPEM []byte) {
	t.Helper()

	secretName := clusterName + constants.SuffixTLSCA
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"ca.crt": caPEM,
		},
	}

	if err := k8sClient.Create(ctx, secret); err == nil {
		return
	} else if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create CA secret: %v", err)
	}

	existing := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, existing); err != nil {
		t.Fatalf("get existing CA secret: %v", err)
	}
	existing.Data = map[string][]byte{
		"ca.crt": caPEM,
	}
	if err := k8sClient.Update(ctx, existing); err != nil {
		t.Fatalf("update CA secret: %v", err)
	}
}

func createMinimalCluster(t *testing.T, namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	t.Helper()

	cluster := newMinimalClusterObj(namespace, name)

	if err := k8sClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, cluster); err != nil {
		t.Fatalf("get created OpenBaoCluster: %v", err)
	}

	return cluster
}

func newMinimalClusterObj(namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			ControllerJWTMode: openbaov1alpha1.ControllerJWTModeShared,
			Version:           testOpenBaoVersion244,
			Image:             testOpenBaoImage244,
			Replicas:          3,
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
		},
	}
}

func updateClusterStatus(
	t *testing.T,
	cluster *openbaov1alpha1.OpenBaoCluster,
	mutate func(*openbaov1alpha1.OpenBaoClusterStatus),
) {
	t.Helper()

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoCluster for status update: %v", err)
	}
	mutate(&latest.Status)
	if err := k8sClient.Status().Update(ctx, &latest); err != nil {
		t.Fatalf("update OpenBaoCluster status: %v", err)
	}
	cluster.Status = latest.Status
}

func discardLogger() logr.Logger {
	return logr.Discard()
}

func envVarMap(envVars []corev1.EnvVar) map[string]string {
	env := make(map[string]string, len(envVars))
	for _, envVar := range envVars {
		env[envVar.Name] = envVar.Value
	}
	return env
}

// newTestStatefulSetSpec creates a minimal StatefulSetSpec for testing.
func newTestStatefulSetSpec(cluster *openbaov1alpha1.OpenBaoCluster) workloadsvc.StatefulSetSpec {
	return workloadsvc.StatefulSetSpec{
		Name:               cluster.Name,
		Revision:           "",
		Image:              cluster.Spec.Image,
		InitContainerImage: "",
		Replicas:           cluster.Spec.Replicas,
		ConfigHash:         "",
		DisableSelfInit:    false,
		SkipReconciliation: false,
	}
}

func reconcileClusterResources(
	ctx context.Context,
	logger logr.Logger,
	kubeClient client.Client,
	scheme *runtime.Scheme,
	cluster *openbaov1alpha1.OpenBaoCluster,
	spec workloadsvc.StatefulSetSpec,
) error {
	liveCluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, liveCluster); err != nil {
		return fmt.Errorf("failed to get live OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
	}
	cluster = liveCluster

	configContent, err := bootstrapmanager.NewManagerWithReader(
		kubeClient,
		kubeClient,
		scheme,
		"openbao-operator-system",
	).PrepareWorkload(ctx, logger, cluster)
	if err != nil {
		return err
	}

	if err := networkingmanager.NewManagerWithReader(
		kubeClient,
		kubeClient,
		scheme,
		"openbao-operator-system",
		"",
	).Reconcile(ctx, logger, cluster); err != nil {
		return err
	}

	if err := identitymanager.NewManager(kubeClient, scheme).Reconcile(ctx, logger, cluster); err != nil {
		return err
	}

	return workloadsvc.NewManager(kubeClient, scheme, "").
		WithReader(kubeClient).
		Reconcile(ctx, logger, cluster, configContent, spec)
}

func integrationOwnerRef(cluster *openbaov1alpha1.OpenBaoCluster) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: openbaov1alpha1.GroupVersion.String(),
		Kind:       "OpenBaoCluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
		Controller: &controller,
	}
}

func requireIntegrationOwnerProof(t *testing.T, obj client.Object, owner client.Object) {
	t.Helper()

	if resourceownership.HasOwnerProof(obj, owner) {
		return
	}
	t.Fatalf(
		"expected %T %s/%s to have owner proof for OpenBaoCluster UID %q, ownerRefs=%#v annotations=%#v",
		obj,
		obj.GetNamespace(),
		obj.GetName(),
		owner.GetUID(),
		obj.GetOwnerReferences(),
		obj.GetAnnotations(),
	)
}
