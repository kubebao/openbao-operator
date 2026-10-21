package openbaocluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	bootstrapmanager "github.com/kubebao/openbao-operator/internal/service/bootstrap"
	networkingmanager "github.com/kubebao/openbao-operator/internal/service/networking"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

type scaleDownRuntimeStub struct {
	prepareFunc     func(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, statefulSetName string, currentReplicas, desiredReplicas int32) error
	readPrepareFunc func(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, statefulSetName string, currentReplicas, desiredReplicas int32) error
}

func (s scaleDownRuntimeStub) PrepareScaleDown(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, statefulSetName string, currentReplicas, desiredReplicas int32) error {
	if s.prepareFunc != nil {
		return s.prepareFunc(ctx, logger, cluster, statefulSetName, currentReplicas, desiredReplicas)
	}
	return nil
}

func (s scaleDownRuntimeStub) PrepareReadReplicaScaleDown(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, statefulSetName string, currentReplicas, desiredReplicas int32) error {
	if s.readPrepareFunc != nil {
		return s.readPrepareFunc(ctx, logger, cluster, statefulSetName, currentReplicas, desiredReplicas)
	}
	return nil
}

func TestHandleScaleDownSafety(t *testing.T) {
	clusterName := "test-cluster"
	namespace := "default"

	tests := []struct {
		name            string
		currentReplicas int32
		desiredReplicas int32
		statusReplicas  int32
		readyReplicas   int32
		currentStatus   int32
		generation      int64
		observedGen     int64
		runtimeError    string
		expectedApplied int32
		expectedError   string
	}{
		{
			name:            "no scale down",
			currentReplicas: 3,
			desiredReplicas: 3,
			statusReplicas:  3,
			readyReplicas:   3,
			currentStatus:   3,
			generation:      1,
			observedGen:     1,
			expectedApplied: 3,
			expectedError:   "",
		},
		{
			name:            "scale up",
			currentReplicas: 3,
			desiredReplicas: 4,
			statusReplicas:  3,
			readyReplicas:   3,
			currentStatus:   3,
			generation:      1,
			observedGen:     1,
			expectedApplied: 4,
			expectedError:   "",
		},
		{
			name:            "scale down uses one safe step",
			currentReplicas: 3,
			desiredReplicas: 1,
			statusReplicas:  3,
			readyReplicas:   3,
			currentStatus:   3,
			generation:      1,
			observedGen:     1,
			expectedApplied: 2,
			expectedError:   "",
		},
		{
			name:            "scale down waits for statefulset to settle between steps",
			currentReplicas: 2,
			desiredReplicas: 1,
			statusReplicas:  2,
			readyReplicas:   1,
			currentStatus:   1,
			generation:      2,
			observedGen:     2,
			expectedApplied: 2,
			expectedError:   "waiting for StatefulSet default/test-cluster to settle at 2 replicas before next scale-down step",
		},
		{
			name:            "scale down requeues when runtime blocks",
			currentReplicas: 3,
			desiredReplicas: 2,
			statusReplicas:  3,
			readyReplicas:   3,
			currentStatus:   3,
			generation:      1,
			observedGen:     1,
			runtimeError:    "waiting for leader step-down on test-cluster-2 to complete",
			expectedApplied: 3,
			expectedError:   "waiting for leader step-down on test-cluster-2 to complete",
		},
		{
			name:            "scale down blocks without runtime",
			currentReplicas: 3,
			desiredReplicas: 2,
			statusReplicas:  3,
			readyReplicas:   3,
			currentStatus:   3,
			generation:      1,
			observedGen:     1,
			expectedApplied: 3,
			expectedError:   "scale-down runtime is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Replicas: tt.desiredReplicas,
				},
			}

			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       clusterName,
					Namespace:  namespace,
					Generation: tt.generation,
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: &tt.currentReplicas,
				},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: tt.observedGen,
					Replicas:           tt.statusReplicas,
					ReadyReplicas:      tt.readyReplicas,
					CurrentReplicas:    tt.currentStatus,
				},
			}

			r := &infraReconciler{}
			if tt.name != "scale down blocks without runtime" {
				r.deps.ScaleDown = InfraScaleDownRuntime{
					Runtime: scaleDownRuntimeStub{
						prepareFunc: func(_ context.Context, _ logr.Logger, gotCluster *openbaov1alpha1.OpenBaoCluster, statefulSetName string, currentReplicas, desiredReplicas int32) error {
							assert.Same(t, cluster, gotCluster)
							assert.Equal(t, clusterName, statefulSetName)
							assert.Equal(t, tt.currentReplicas, currentReplicas)
							if tt.currentReplicas > tt.desiredReplicas {
								expectedDesired := tt.currentReplicas - 1
								if expectedDesired < tt.desiredReplicas {
									expectedDesired = tt.desiredReplicas
								}
								assert.Equal(t, expectedDesired, desiredReplicas)
							}
							if tt.runtimeError != "" {
								return errors.New(tt.runtimeError)
							}
							return nil
						},
					},
				}
			}

			appliedReplicas, err := r.handleScaleDownSafety(context.Background(), cluster, tt.desiredReplicas, sts)
			assert.Equal(t, tt.expectedApplied, appliedReplicas)
			if tt.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			}
		})
	}
}

func TestHandleReadReplicaScaleDownSafety(t *testing.T) {
	clusterName := "test-cluster-read"
	namespace := "default"

	tests := []struct {
		name            string
		initialized     bool
		currentReplicas int32
		desiredReplicas int32
		statusReplicas  int32
		readyReplicas   int32
		currentStatus   int32
		generation      int64
		observedGen     int64
		runtimeError    string
		expectedApplied int32
		expectedError   string
	}{
		{
			name:            "no scale down before initialization",
			initialized:     false,
			currentReplicas: 2,
			desiredReplicas: 1,
			statusReplicas:  2,
			readyReplicas:   2,
			currentStatus:   2,
			generation:      1,
			observedGen:     1,
			expectedApplied: 1,
			expectedError:   "",
		},
		{
			name:            "scale down uses one safe step",
			initialized:     true,
			currentReplicas: 3,
			desiredReplicas: 1,
			statusReplicas:  3,
			readyReplicas:   3,
			currentStatus:   3,
			generation:      1,
			observedGen:     1,
			expectedApplied: 2,
			expectedError:   "",
		},
		{
			name:            "scale down waits for statefulset to settle between steps",
			initialized:     true,
			currentReplicas: 2,
			desiredReplicas: 1,
			statusReplicas:  2,
			readyReplicas:   1,
			currentStatus:   1,
			generation:      2,
			observedGen:     2,
			expectedApplied: 2,
			expectedError:   "waiting for StatefulSet default/test-cluster-read to settle at 2 replicas before next read-replica scale-down step",
		},
		{
			name:            "scale down requeues when runtime blocks",
			initialized:     true,
			currentReplicas: 2,
			desiredReplicas: 1,
			statusReplicas:  2,
			readyReplicas:   2,
			currentStatus:   2,
			generation:      1,
			observedGen:     1,
			runtimeError:    "waiting for non-voter removal",
			expectedApplied: 2,
			expectedError:   "waiting for non-voter removal",
		},
		{
			name:            "scale down blocks without runtime",
			initialized:     true,
			currentReplicas: 2,
			desiredReplicas: 1,
			statusReplicas:  2,
			readyReplicas:   2,
			currentStatus:   2,
			generation:      1,
			observedGen:     1,
			expectedApplied: 2,
			expectedError:   "read-replica scale-down runtime is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: namespace,
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					Initialized: tt.initialized,
				},
			}

			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       clusterName,
					Namespace:  namespace,
					Generation: tt.generation,
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: &tt.currentReplicas,
				},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: tt.observedGen,
					Replicas:           tt.statusReplicas,
					ReadyReplicas:      tt.readyReplicas,
					CurrentReplicas:    tt.currentStatus,
				},
			}

			r := &infraReconciler{}
			if tt.name != "scale down blocks without runtime" {
				r.deps.ScaleDown = InfraScaleDownRuntime{
					ReadReplicaRuntime: scaleDownRuntimeStub{
						readPrepareFunc: func(_ context.Context, _ logr.Logger, gotCluster *openbaov1alpha1.OpenBaoCluster, statefulSetName string, currentReplicas, desiredReplicas int32) error {
							assert.Same(t, cluster, gotCluster)
							assert.Equal(t, clusterName, statefulSetName)
							assert.Equal(t, tt.currentReplicas, currentReplicas)
							if tt.currentReplicas > tt.desiredReplicas {
								expectedDesired := tt.currentReplicas - 1
								if expectedDesired < tt.desiredReplicas {
									expectedDesired = tt.desiredReplicas
								}
								assert.Equal(t, expectedDesired, desiredReplicas)
							}
							if tt.runtimeError != "" {
								return errors.New(tt.runtimeError)
							}
							return nil
						},
					},
				}
			}

			appliedReplicas, err := r.handleReadReplicaScaleDownSafety(context.Background(), cluster, tt.desiredReplicas, sts)
			assert.Equal(t, tt.expectedApplied, appliedReplicas)
			if tt.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			}
		})
	}
}

func TestReconcileDisabledReadReplicas_DeletesDrainedResources(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       types.UID("test-cluster-uid"),
		},
	}
	readReplicas := int32(0)
	readSTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            resourceidentity.ReadReplicaStatefulSetName(cluster),
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{infraOwnerRef(cluster)},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &readReplicas,
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			Replicas:           0,
			ReadyReplicas:      0,
			CurrentReplicas:    0,
		},
	}
	readConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceidentity.ReadReplicaConfigMapName(cluster),
			Namespace: cluster.Namespace,
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, readSTS, readConfig).
		Build()

	r := &infraReconciler{
		deps: InfraDependencies{
			Kubernetes: InfraKubernetesRuntime{
				Client: k8sClient,
				Scheme: scheme,
			},
		},
	}

	readSpec := workloadsvc.StatefulSetSpec{
		Name:               resourceidentity.ReadReplicaStatefulSetName(cluster),
		Pool:               constants.LabelValueOpenBaoWorkloadPoolReadReplica,
		Replicas:           0,
		SkipReconciliation: true,
	}

	requeue, err := r.reconcileDisabledReadReplicas(context.Background(), logr.Discard(), cluster, readSpec, readSTS, true)
	assert.NoError(t, err)
	assert.False(t, requeue)

	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: cluster.Namespace, Name: readSTS.Name}, &appsv1.StatefulSet{})
	assert.True(t, apierrors.IsNotFound(err))
	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: cluster.Namespace, Name: readConfig.Name}, &corev1.ConfigMap{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcileDisabledReadReplicas_RequeuesUntilDrained(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       types.UID("test-cluster-uid"),
		},
	}
	currentReplicas := int32(1)
	readSTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            resourceidentity.ReadReplicaStatefulSetName(cluster),
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{infraOwnerRef(cluster)},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &currentReplicas,
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			ReadyReplicas:      1,
			CurrentReplicas:    1,
		},
	}
	readConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceidentity.ReadReplicaConfigMapName(cluster),
			Namespace: cluster.Namespace,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, readSTS, readConfig).
		Build()

	r := &infraReconciler{
		deps: InfraDependencies{
			Kubernetes: InfraKubernetesRuntime{
				Client: k8sClient,
				Scheme: scheme,
			},
		},
	}

	readSpec := workloadsvc.StatefulSetSpec{
		Name:               resourceidentity.ReadReplicaStatefulSetName(cluster),
		Pool:               constants.LabelValueOpenBaoWorkloadPoolReadReplica,
		Replicas:           0,
		SkipReconciliation: true,
	}

	requeue, err := r.reconcileDisabledReadReplicas(context.Background(), logr.Discard(), cluster, readSpec, readSTS, true)
	assert.NoError(t, err)
	assert.True(t, requeue)

	updatedSTS := &appsv1.StatefulSet{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: cluster.Namespace, Name: readSTS.Name}, updatedSTS)
	assert.NoError(t, err)
	if assert.NotNil(t, updatedSTS.Spec.Replicas) {
		assert.EqualValues(t, 0, *updatedSTS.Spec.Replicas)
	}

	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: cluster.Namespace, Name: readConfig.Name}, &corev1.ConfigMap{})
	assert.NoError(t, err)
}

func TestInfraReconciler_ResolveOIDC_LazyDiscoveryForSelfInit(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	var called int
	r := &infraReconciler{deps: InfraDependencies{
		OIDC: InfraOIDCRuntime{
			RestConfig:  &rest.Config{Host: "https://kubernetes.default.svc"},
			OIDCIssuer:  "",
			OIDCJWTKeys: nil,
			DiscoverOIDCConfig: func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error) {
				called++
				return &OIDCConfig{
					IssuerURL: "https://issuer.example",
					JWKSURL:   "https://issuer.example/keys",
				}, nil
			},
		},
	}}

	oidc, err := r.resolveOIDC(context.Background(), cluster)
	assert.NoError(t, err)
	assert.Equal(t, 1, called)
	assert.Equal(t, "https://issuer.example", oidc.IssuerURL)
	assert.Equal(t, "https://issuer.example/keys", oidc.JWKSURL)
}

func TestInfraReconciler_VerifyInitContainerImageDigest_UsesResolvedDefaultImage(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "1.2.3")

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileHardened,
		},
	}

	var verifiedImage string
	r := &infraReconciler{deps: InfraDependencies{
		ImageVerification: InfraImageVerificationRuntime{
			VerifyOperatorImage: func(_ context.Context, _ logr.Logger, _ imageverify.Verifier, _ *openbaov1alpha1.OpenBaoCluster, imageRef string) (string, error) {
				verifiedImage = imageRef
				return "ghcr.io/kubebao/openbao-init@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
			},
		},
	}}

	initImage, err := r.resolveInitContainerImage(cluster)
	if err != nil {
		t.Fatalf("resolveInitContainerImage() error = %v", err)
	}
	if initImage != "ghcr.io/kubebao/openbao-init:1.2.3" {
		t.Fatalf("resolveInitContainerImage() = %q, want %q", initImage, "ghcr.io/kubebao/openbao-init:1.2.3")
	}

	digest, err := r.verifyInitContainerImageDigest(context.Background(), logr.Discard(), cluster, initImage)
	if err != nil {
		t.Fatalf("verifyInitContainerImageDigest() error = %v", err)
	}
	if digest != "ghcr.io/kubebao/openbao-init@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("verifyInitContainerImageDigest() = %q", digest)
	}
	if verifiedImage != initImage {
		t.Fatalf("verified image = %q, want %q", verifiedImage, initImage)
	}
}

func TestComputeStatefulSetSpec_BlueGreenCleanupRetainsStatefulSetName(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bluegreen-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:        openbaov1alpha1.PhaseCleanup,
				BlueRevision: "blue123",
			},
		},
	}

	spec := (&infraReconciler{}).computeStatefulSetSpec(logr.Discard(), cluster, "sha256:main", "sha256:init")
	assert.True(t, spec.SkipReconciliation)
	assert.Equal(t, "blue123", spec.Revision)
	assert.Equal(t, "bluegreen-cluster-blue123", spec.Name)
}

func TestInfraReconciler_ResolveTargetMainImage_BlueGreenPrefersActivePods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.4.4",
			Image:    "openbao/openbao:2.4.4",
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
				BlueGreen: &openbaov1alpha1.BlueGreenConfig{
					AutoPromote: false,
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			CurrentVersion: "2.4.3", // upgrade pending
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:        openbaov1alpha1.PhaseIdle,
				BlueRevision: "rev-old",
				BlueImage:    "", // missing in status; should be inferred
			},
		},
	}

	activePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-rev-old-0",
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/name":       "openbao",
				"app.kubernetes.io/instance":   "test-cluster",
				"app.kubernetes.io/managed-by": "openbao-operator",
				"openbao.org/cluster":          "test-cluster",
				"openbao.org/revision":         "rev-old",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "openbao", Image: "openbao/openbao:2.4.3"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, activePod).
		Build()

	r := &infraReconciler{deps: InfraDependencies{
		Kubernetes: InfraKubernetesRuntime{
			Client: k8sClient,
			Scheme: scheme,
		},
	}}

	got := r.resolveTargetMainImage(context.Background(), logr.Discard(), cluster)
	assert.Equal(t, "openbao/openbao:2.4.3", got)
	assert.Equal(t, "openbao/openbao:2.4.3", cluster.Status.BlueGreen.BlueImage)
}

func TestInfraReconciler_Reconcile_BlocksInvalidVersionSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cluster    *openbaov1alpha1.OpenBaoCluster
		wantReason string
	}{
		{
			name: "downgrade is rejected",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version: "2.4.4",
					Image:   "openbao/openbao:2.4.4",
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					CurrentVersion: "2.5.0",
				},
			},
			wantReason: upgrade.ReasonDowngradeBlocked,
		},
		{
			name: "image version mismatch is rejected",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version: "2.5.0",
					Image:   "openbao/openbao:2.4.4",
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					CurrentVersion: "2.4.4",
				},
			},
			wantReason: upgrade.ReasonImageVersionMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &infraReconciler{}
			_, err := r.Reconcile(context.Background(), logr.Discard(), tt.cluster)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
				t.Fatalf("expected permanent config error, got %v", err)
			}
			reason, ok := operatorerrors.Reason(err)
			if !ok {
				t.Fatalf("expected reasoned error, got %v", err)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestInfraReconciler_Reconcile_MapsAPIServerNetworkConfigurationError(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "1.2.3")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
			UID:       types.UID("example-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
			Version: "2.5.0",
			Image:   "openbao/openbao:2.5.0",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		WithReturnManagedFields().
		Build()

	r := &infraReconciler{
		deps: InfraDependencies{
			Kubernetes: InfraKubernetesRuntime{
				Client:            k8sClient,
				Scheme:            scheme,
				OperatorNamespace: "openbao-operator-system",
			},
		},
	}

	_, err := r.Reconcile(context.Background(), logr.Discard(), cluster)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
		t.Fatalf("expected permanent config error, got %v", err)
	}
	reason, ok := operatorerrors.Reason(err)
	if !ok {
		t.Fatalf("expected reasoned error, got %v", err)
	}
	if reason != constants.ReasonAPIServerNetworkConfigurationInvalid {
		t.Fatalf("reason = %q, want %s", reason, constants.ReasonAPIServerNetworkConfigurationInvalid)
	}
	if !strings.Contains(err.Error(), "spec.network.apiServerEndpointIPs") {
		t.Fatalf("error %q does not mention apiServerEndpointIPs", err)
	}
}

// The token projection lives in admission-locked StatefulSet fields. When an existing
// StatefulSet cannot take the requested change, the reconcile stops before it renders the
// seal configuration its Pods read.
func TestInfraReconciler_Reconcile_AzureWorkloadIdentityStopsBeforeConfig(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "1.2.3")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
			UID:       types.UID("example-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  "2.6.0",
			Image:    "openbao/openbao:2.6.0",
			Replicas: 1,
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Image: "openbao-init:test",
			},
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR:        "10.43.0.1/32",
				APIServerEndpointIPs: []string{"192.0.2.10"},
			},
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "azurekeyvault",
				AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
					VaultName:        "vault",
					KeyName:          "key",
					TenantID:         "tenant-123",
					WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
				},
			},
		},
	}
	replicas := int32(1)
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.ContainerBao}},
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, statefulSet).
		WithReturnManagedFields().
		Build()

	r := &infraReconciler{
		deps: InfraDependencies{
			Kubernetes: InfraKubernetesRuntime{
				Client:            k8sClient,
				Scheme:            scheme,
				OperatorNamespace: "openbao-operator-system",
			},
		},
	}

	_, err := r.Reconcile(context.Background(), logr.Discard(), cluster)
	if !errors.Is(err, workloadsvc.ErrAzureWorkloadIdentityStatefulSetRequiresRecreate) {
		t.Fatalf("Reconcile() error = %v, want %v", err, workloadsvc.ErrAzureWorkloadIdentityStatefulSetRequiresRecreate)
	}
	reason, ok := operatorerrors.Reason(err)
	if !ok || reason != constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired {
		t.Fatalf("reason = %q, want %s", reason, constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired)
	}

	configMap := &corev1.ConfigMap{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: cluster.Namespace, Name: resourceidentity.ConfigMapName(cluster)}, configMap)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no seal configuration to be rendered, got ConfigMap error %v", err)
	}
}

func TestInfraReconciler_Reconcile_AzureWorkloadIdentityHoldsEveryPool(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "1.2.3")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
			UID:       types.UID("example-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  "2.6.0",
			Image:    "openbao/openbao:2.6.0",
			Replicas: 1,
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Image: "openbao-init:test",
			},
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR:        "10.43.0.1/32",
				APIServerEndpointIPs: []string{"192.0.2.10"},
			},
			ReadReplicas: &openbaov1alpha1.ReadReplicaConfig{Replicas: 1},
			Maintenance:  &openbaov1alpha1.MaintenanceConfig{Enabled: true},
			Unseal: &openbaov1alpha1.UnsealConfig{
				Type: "azurekeyvault",
				AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
					VaultName:        "vault",
					KeyName:          "key",
					TenantID:         "tenant-123",
					WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
				},
			},
		},
	}
	newStatefulSet := func(name string) *appsv1.StatefulSet {
		replicas := int32(1)
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: openbaov1alpha1.GroupVersion.String(),
					Kind:       "OpenBaoCluster",
					Name:       cluster.Name,
					UID:        cluster.UID,
					Controller: ptr.To(true),
				}},
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: constants.ContainerBao}},
					},
				},
			},
		}
	}
	voter := newStatefulSet(cluster.Name)
	readReplica := newStatefulSet(resourceidentity.ReadReplicaStatefulSetName(cluster))

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, voter, readReplica).
		WithReturnManagedFields().
		Build()

	r := &infraReconciler{
		deps: InfraDependencies{
			Kubernetes: InfraKubernetesRuntime{
				Client:            k8sClient,
				Scheme:            scheme,
				OperatorNamespace: "openbao-operator-system",
			},
		},
	}

	requireHeld := func(want ...*appsv1.StatefulSet) {
		t.Helper()
		_, err := r.Reconcile(context.Background(), logr.Discard(), cluster)
		if !errors.Is(err, workloadsvc.ErrAzureWorkloadIdentityStatefulSetRequiresRecreate) {
			t.Fatalf("Reconcile() error = %v, want %v", err, workloadsvc.ErrAzureWorkloadIdentityStatefulSetRequiresRecreate)
		}
		for _, sts := range want {
			if !strings.Contains(err.Error(), sts.Namespace+"/"+sts.Name) {
				t.Fatalf("Reconcile() error = %v, want it to name StatefulSet %s", err, sts.Name)
			}
			stored := &appsv1.StatefulSet{}
			if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(sts), stored); err != nil {
				t.Fatalf("get StatefulSet %s: %v", sts.Name, err)
			}
			if stored.Annotations[constants.AnnotationMaintenance] != "true" {
				t.Fatalf("StatefulSet %s maintenance annotation = %q, want true", sts.Name, stored.Annotations[constants.AnnotationMaintenance])
			}
		}
	}

	// Both pools are held and annotated in the same reconcile, so both can be deleted in
	// one maintenance window.
	requireHeld(voter, readReplica)

	// Deleting only the voter StatefulSet leaves the read replicas held: the voter is not
	// recreated and no seal configuration is rendered until every held StatefulSet is gone.
	if err := k8sClient.Delete(context.Background(), voter); err != nil {
		t.Fatalf("delete voter StatefulSet: %v", err)
	}
	requireHeld(readReplica)
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(voter), &appsv1.StatefulSet{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the voter StatefulSet to stay deleted, got %v", err)
	}
	configMap := &corev1.ConfigMap{}
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: cluster.Namespace, Name: resourceidentity.ConfigMapName(cluster)}, configMap)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no seal configuration to be rendered, got ConfigMap error %v", err)
	}

	if err := k8sClient.Delete(context.Background(), readReplica); err != nil {
		t.Fatalf("delete read-replica StatefulSet: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), logr.Discard(), cluster); errors.Is(err, workloadsvc.ErrAzureWorkloadIdentityStatefulSetRequiresRecreate) {
		t.Fatalf("Reconcile() error = %v, want the guard released once every held StatefulSet is deleted", err)
	}
}

func TestInfraReconciler_Reconcile_DoesNotRequeuePermanentScaleDownPrerequisites(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = openbaov1alpha1.AddToScheme(scheme)

	currentReplicas := int32(3)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  "2.5.0",
			Image:    "openbao/openbao:2.5.0",
			Replicas: 1,
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Image: "openbao-init:test",
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Initialized: true,
		},
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       cluster.Name,
			Namespace:  cluster.Namespace,
			Generation: 1,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &currentReplicas,
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			Replicas:           3,
			ReadyReplicas:      3,
			CurrentReplicas:    3,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, statefulSet).
		Build()

	r := &infraReconciler{
		deps: InfraDependencies{
			Kubernetes: InfraKubernetesRuntime{
				Client: k8sClient,
				Scheme: scheme,
			},
			ScaleDown: InfraScaleDownRuntime{
				Runtime: scaleDownRuntimeStub{
					prepareFunc: func(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster, string, int32, int32) error {
						return operatorerrors.WrapPermanentPrerequisitesMissing(errors.New("missing raft permissions"))
					},
				},
			},
		},
	}

	result, err := r.Reconcile(context.Background(), logr.Discard(), cluster)
	assert.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
}

func TestInfraReconciler_MapManagerReconcileError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{name: "oidc bootstrap audience mismatch", err: bootstrapmanager.ErrOIDCBootstrapAudienceMismatch, wantReason: constants.ReasonOIDCBootstrapConfigurationInvalid},
		{name: "gateway api missing", err: networkingmanager.ErrGatewayAPIMissing, wantReason: constants.ReasonGatewayAPIMissing},
		{name: "api server network invalid", err: networkingmanager.ErrAPIServerNetworkConfigurationInvalid, wantReason: constants.ReasonAPIServerNetworkConfigurationInvalid},
		{name: "Service primary IP family change", err: networkingmanager.ErrServiceIPFamilyChangeNotSupported, wantReason: constants.ReasonServiceIPFamilyChangeNotSupported},
		{name: "prerequisites missing", err: workloadsvc.ErrStatefulSetPrerequisitesMissing, wantReason: constants.ReasonPrerequisitesMissing},
		{name: "azure workload identity recreate required", err: workloadsvc.ErrAzureWorkloadIdentityStatefulSetRequiresRecreate, wantReason: constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired},
		{name: "acme domain not resolvable", err: networkingmanager.ErrACMEDomainNotResolvable, wantReason: constants.ReasonACMEDomainNotResolvable},
		{name: "acme gateway not configured", err: networkingmanager.ErrACMEGatewayNotConfiguredForPassthrough, wantReason: constants.ReasonACMEGatewayNotConfiguredForPassthrough},
	}

	r := &infraReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.mapManagerReconcileError(tt.err)
			reason, ok := operatorerrors.Reason(got)
			if !ok {
				t.Fatalf("expected reasoned error, got %v", got)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
			if !errors.Is(got, tt.err) {
				t.Fatalf("wrapped error %v should preserve %v", got, tt.err)
			}
		})
	}

	t.Run("unmapped error passes through", func(t *testing.T) {
		original := errors.New("boom")
		if got := r.mapManagerReconcileError(original); got != original { //nolint:errorlint // Exact identity is the behavior under test.
			t.Fatalf("got %v, want original %v", got, original)
		}
	})
}

func TestInfraReconciler_ResolveOIDC_MissingRestConfigReturnsBootstrapReason(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	r := &infraReconciler{}

	_, err := r.resolveOIDC(context.Background(), cluster)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
		t.Fatalf("expected permanent config error, got %v", err)
	}
	reason, ok := operatorerrors.Reason(err)
	if !ok || reason != constants.ReasonOIDCBootstrapConfigurationInvalid {
		t.Fatalf("reason = %q,%v want %s,true", reason, ok, constants.ReasonOIDCBootstrapConfigurationInvalid)
	}
}

func TestInfraReconciler_ResolveOIDC_ForbiddenDiscoveryReturnsBootstrapReason(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	r := &infraReconciler{
		deps: InfraDependencies{
			OIDC: InfraOIDCRuntime{
				RestConfig: &rest.Config{Host: "https://kubernetes.default.svc"},
				DiscoverOIDCConfig: func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error) {
					return nil, errors.New("forbidden")
				},
				DiscoveryStatusCode: func(err error) (int, bool) {
					return http.StatusForbidden, true
				},
			},
		},
	}

	_, err := r.resolveOIDC(context.Background(), cluster)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
		t.Fatalf("expected permanent config error, got %v", err)
	}
	reason, ok := operatorerrors.Reason(err)

	if !ok || reason != constants.ReasonOIDCBootstrapConfigurationInvalid {
		t.Fatalf("reason = %q,%v want %s,true", reason, ok, constants.ReasonOIDCBootstrapConfigurationInvalid)
	}
}

func TestInfraReconciler_ResolveOIDC_EmptyIssuerReturnsBootstrapReason(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	r := &infraReconciler{
		deps: InfraDependencies{
			OIDC: InfraOIDCRuntime{
				RestConfig: &rest.Config{Host: "https://kubernetes.default.svc"},
				DiscoverOIDCConfig: func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error) {
					return &OIDCConfig{JWKSURL: "https://issuer.example/keys"}, nil
				},
			},
		},
	}

	_, err := r.resolveOIDC(context.Background(), cluster)
	assertOIDCBootstrapConfigurationError(t, err)
}

func TestInfraReconciler_ResolveOIDC_NoJWTValidationMaterialReturnsBootstrapReason(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	r := &infraReconciler{
		deps: InfraDependencies{
			OIDC: InfraOIDCRuntime{
				RestConfig: &rest.Config{Host: "https://kubernetes.default.svc"},
				DiscoverOIDCConfig: func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error) {
					return &OIDCConfig{IssuerURL: "https://issuer.example"}, nil
				},
			},
		},
	}

	_, err := r.resolveOIDC(context.Background(), cluster)
	assertOIDCBootstrapConfigurationError(t, err)
}

func TestInfraReconciler_ResolveOIDC_MalformedJWKSReturnsBootstrapReason(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	r := &infraReconciler{
		deps: InfraDependencies{
			OIDC: InfraOIDCRuntime{
				RestConfig: &rest.Config{Host: "https://kubernetes.default.svc"},
				DiscoverOIDCConfig: func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error) {
					return nil, fmt.Errorf("%w: failed to parse jwks document: %w", portauth.ErrDiscoveryContentInvalid, malformedJSONError())
				},
			},
		},
	}

	_, err := r.resolveOIDC(context.Background(), cluster)
	assertOIDCBootstrapConfigurationError(t, err)
}

func TestInfraReconciler_ResolveOIDC_TransientJWKSFetchFailureStaysTransient(t *testing.T) {
	t.Parallel()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	r := &infraReconciler{
		deps: InfraDependencies{
			OIDC: InfraOIDCRuntime{
				RestConfig: &rest.Config{Host: "https://kubernetes.default.svc"},
				DiscoverOIDCConfig: func(ctx context.Context, cfg *rest.Config) (*OIDCConfig, error) {
					return nil, fmt.Errorf("failed to fetch JWKS keys: failed to fetch jwks endpoint: %w", context.DeadlineExceeded)
				},
			},
		},
	}

	_, err := r.resolveOIDC(context.Background(), cluster)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, operatorerrors.ErrPermanentConfig) {
		t.Fatalf("expected transient error, got permanent config: %v", err)
	}
	if !operatorerrors.IsTransient(err) {
		t.Fatalf("expected transient error, got %v", err)
	}
}

func assertOIDCBootstrapConfigurationError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
		t.Fatalf("expected permanent config error, got %v", err)
	}
	reason, ok := operatorerrors.Reason(err)
	if !ok || reason != constants.ReasonOIDCBootstrapConfigurationInvalid {
		t.Fatalf("reason = %q,%v want %s,true", reason, ok, constants.ReasonOIDCBootstrapConfigurationInvalid)
	}
}

func malformedJSONError() error {
	var payload map[string]any
	return json.Unmarshal([]byte("{"), &payload)
}

func infraOwnerRef(cluster *openbaov1alpha1.OpenBaoCluster) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: openbaov1alpha1.GroupVersion.String(),
		Kind:       "OpenBaoCluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
		Controller: &controller,
	}
}
