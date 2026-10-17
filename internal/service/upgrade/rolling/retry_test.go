package rolling

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func TestRetryRequestPending_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cluster *openbaov1alpha1.OpenBaoCluster
		want    bool
	}{
		{
			name:    "nil cluster",
			cluster: nil,
			want:    false,
		},
		{
			name: "missing upgrade request config",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{},
			},
			want: false,
		},
		{
			name: "empty request value",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Requests: &openbaov1alpha1.UpgradeRequestConfig{
							Retry: "",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "whitespace request value",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Requests: &openbaov1alpha1.UpgradeRequestConfig{
							Retry: "   ",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "already handled request value",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Requests: &openbaov1alpha1.UpgradeRequestConfig{
							Retry: "retry-1",
						},
					},
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					UpgradeRequests: &openbaov1alpha1.UpgradeRequestStatus{
						LastHandledRetry: "retry-1",
					},
				},
			},
			want: false,
		},
		{
			name: "new request value",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Requests: &openbaov1alpha1.UpgradeRequestConfig{
							Retry: "retry-2",
						},
					},
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					UpgradeRequests: &openbaov1alpha1.UpgradeRequestStatus{
						LastHandledRetry: "retry-1",
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := upgrade.RetryRequestPending(tt.cluster); got != tt.want {
				t.Fatalf("RetryRequestPending()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestClearUpgradeFailureForRetry_TableDriven(t *testing.T) {
	t.Parallel()

	now := metav1.NewTime(time.Now())
	startedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))

	tests := []struct {
		name    string
		cluster *openbaov1alpha1.OpenBaoCluster
		assert  func(t *testing.T, cluster *openbaov1alpha1.OpenBaoCluster)
	}{
		{
			name:    "nil cluster is no-op",
			cluster: nil,
			assert: func(t *testing.T, cluster *openbaov1alpha1.OpenBaoCluster) {
				t.Helper()
				if cluster != nil {
					t.Fatalf("cluster=%v, want nil", cluster)
				}
			},
		},
		{
			name: "nil upgrade status is no-op",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Status: openbaov1alpha1.OpenBaoClusterStatus{Upgrade: nil},
			},
			assert: func(t *testing.T, cluster *openbaov1alpha1.OpenBaoCluster) {
				t.Helper()
				if cluster.Status.Upgrade != nil {
					t.Fatalf("Upgrade=%v, want nil", cluster.Status.Upgrade)
				}
			},
		},
		{
			name: "clears failure fields but keeps structural progress",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					Upgrade: &openbaov1alpha1.UpgradeProgress{
						TargetVersion:    "2.5.0",
						FromVersion:      "2.4.0",
						CurrentPartition: 2,
						CompletedPods:    []int32{2},
						StartedAt:        &startedAt,
						Failure: &openbaov1alpha1.ControllerErrorStatus{
							Reason:  upgrade.ReasonStepDownTimeout,
							Message: "leader step down timed out",
							At:      &now,
						},
						LastStepDownTime: &now,
					},
				},
			},
			assert: func(t *testing.T, cluster *openbaov1alpha1.OpenBaoCluster) {
				t.Helper()
				if cluster.Status.Upgrade == nil {
					t.Fatalf("Upgrade=nil, want non-nil")
				}
				if cluster.Status.Upgrade.LastStepDownTime != nil {
					t.Fatalf("LastStepDownTime=%v, want nil", cluster.Status.Upgrade.LastStepDownTime)
				}
				if cluster.Status.Upgrade.Failure != nil {
					t.Fatalf("Failure=%#v, want nil", cluster.Status.Upgrade.Failure)
				}
				if cluster.Status.Upgrade.StartedAt == nil {
					t.Fatalf("StartedAt=nil, want refreshed timestamp")
				}
				if !cluster.Status.Upgrade.StartedAt.After(startedAt.Time) {
					t.Fatalf("StartedAt=%v, want time after %v", cluster.Status.Upgrade.StartedAt, startedAt)
				}
				if cluster.Status.Upgrade.TargetVersion != "2.5.0" {
					t.Fatalf("TargetVersion=%q, want %q", cluster.Status.Upgrade.TargetVersion, "2.5.0")
				}
				if cluster.Status.Upgrade.CurrentPartition != 2 {
					t.Fatalf("CurrentPartition=%d, want 2", cluster.Status.Upgrade.CurrentPartition)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clearUpgradeFailureForRetry(tt.cluster)
			tt.assert(t, tt.cluster)
		})
	}
}

func TestPrepareFailedUpgradeRetry_WaitsForStatefulSetTemplateToMatchRetryTarget(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbaov1alpha1 scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batchv1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}

	now := metav1.Now()
	startedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version: "2.5.0",
			Image:   "openbao/openbao:2.5.0",
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Requests: &openbaov1alpha1.UpgradeRequestConfig{
					Retry: "retry-now",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				TargetVersion:    "2.5.0",
				FromVersion:      "2.4.0",
				CurrentPartition: 2,
				CompletedPods:    []int32{2},
				StartedAt:        &startedAt,
				Failure: &openbaov1alpha1.ControllerErrorStatus{
					Reason:  upgrade.ReasonUpgradeFailed,
					Message: "step-down timed out",
					At:      &now,
				},
				LastStepDownTime: &now,
			},
		},
	}

	targetPod := "test-cluster-1"
	jobName := upgrade.ExecutorJobName(cluster.Name, upgrade.ExecutorActionRollingStepDownLeader, targetPod, "", "")
	staleJob := managedRollingJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cluster.Namespace,
		},
	}, cluster)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-bad",
		},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.ContainerBao,
							Image: "openbao/openbao:retry-image-does-not-exist",
						},
					},
				},
			},
		},
	}
	staleTargetPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetPod,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: "rev-bad",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  constants.ContainerBao,
					Image: "openbao/openbao:retry-image-does-not-exist",
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(cluster, staleJob, sts, staleTargetPod).
		Build()

	mgr := &Manager{client: k8sClient, scheme: scheme}

	err := mgr.prepareFailedUpgradeRetry(context.Background(), logr.Discard(), cluster, upgrade.RetryRequestValue(cluster))
	if err == nil {
		t.Fatal("prepareFailedUpgradeRetry() error=nil, want transient retry wait")
	}
	if !errors.Is(err, operatorerrors.ErrTransientKubernetesAPI) {
		t.Fatalf("prepareFailedUpgradeRetry() error = %v, want transient Kubernetes API error", err)
	}
	if cluster.Status.Upgrade == nil {
		t.Fatal("Upgrade=nil, want still failed")
	}
	if cluster.Status.Upgrade.Failure == nil || cluster.Status.Upgrade.Failure.Reason == "" {
		t.Fatal("Failure cleared unexpectedly")
	}

	storedPod := &corev1.Pod{}
	if getErr := k8sClient.Get(context.Background(), types.NamespacedName{Name: targetPod, Namespace: cluster.Namespace}, storedPod); getErr != nil {
		t.Fatalf("Get(target pod) error = %v, want pod preserved", getErr)
	}
}

func TestPatchRetryStatusSSA_ApplyPayloadOmitsClearedFailureFields(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbaov1alpha1 scheme: %v", err)
	}

	failedAt := metav1.NewTime(time.Unix(1_700_000_000, 0).UTC())
	stepDownAt := metav1.NewTime(time.Unix(1_700_000_100, 0).UTC())
	stored := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retry-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{Version: "2.5.0"},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				TargetVersion:    "2.5.0",
				FromVersion:      "2.4.4",
				CurrentPartition: 3,
				Failure: &openbaov1alpha1.ControllerErrorStatus{
					Reason:  upgrade.ReasonPodNotReady,
					Message: "pod failed to become ready",
					At:      &failedAt,
				},
				LastStepDownTime: &stepDownAt,
			},
		},
	}

	var capturedPayload string
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(withoutUpgradeStatus(stored)).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceApply: func(ctx context.Context, c client.Client, subResource string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
				payload, err := json.Marshal(obj)
				if err != nil {
					return err
				}
				capturedPayload = string(payload)
				return c.Status().Apply(ctx, obj, opts...)
			},
		}).
		Build()

	manager := &Manager{
		client:          k8sClient,
		reader:          k8sClient,
		adminOpsMutator: testAdminOpsMutator(k8sClient),
	}
	cluster := stored.DeepCopy()
	seedUpgradeStatus(t, k8sClient, cluster)
	if err := manager.patchRetryStatusSSA(context.Background(), cluster, "retry-now"); err != nil {
		t.Fatalf("patchRetryStatusSSA() error = %v", err)
	}

	for _, forbidden := range []string{
		`"failure":null`,
		`"lastStepDownTime":null`,
	} {
		if strings.Contains(capturedPayload, forbidden) {
			t.Fatalf("retry apply payload unexpectedly contains %s: %s", forbidden, capturedPayload)
		}
	}
	if !strings.Contains(capturedPayload, `"lastHandledRetry":"retry-now"`) {
		t.Fatalf("retry apply payload missing handled retry marker: %s", capturedPayload)
	}
}

func TestPrepareFailedUpgradeRetry_DeletesStaleCompletedPod(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbao scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batchv1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}

	now := metav1.Now()
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retry-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Image:    "openbao/openbao:2.5.0",
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Requests: &openbaov1alpha1.UpgradeRequestConfig{
					Retry: "retry-now",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			Upgrade: &openbaov1alpha1.UpgradeProgress{
				TargetVersion:    "2.5.0",
				FromVersion:      "2.4.4",
				CurrentPartition: 2,
				CompletedPods:    []int32{2},
				Failure: &openbaov1alpha1.ControllerErrorStatus{
					Reason:  upgrade.ReasonPodNotReady,
					Message: "Pod retry-cluster-2 failed to become ready within 10m0s",
					At:      &now,
				},
			},
		},
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
			Annotations: map[string]string{
				constants.AnnotationClusterGeneration: "0",
				constants.AnnotationResolvedMainImage: "openbao/openbao:2.5.0",
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-good",
		},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.ContainerBao,
							Image: "openbao/openbao:2.5.0",
						},
					},
				},
			},
		},
	}
	currentTargetPod := retryPodForResetTest(cluster.Namespace, "retry-cluster-1", "rev-good", "openbao/openbao:2.5.0", true)
	staleCompletedPod := retryPodForResetTest(cluster.Namespace, "retry-cluster-2", "rev-bad", "openbao/openbao:retry-image-does-not-exist", false)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithObjects(withoutUpgradeStatus(cluster), sts, currentTargetPod, staleCompletedPod).
		Build()
	seedUpgradeStatus(t, k8sClient, cluster)
	manager := &Manager{
		client:          k8sClient,
		reader:          k8sClient,
		scheme:          scheme,
		adminOpsMutator: testAdminOpsMutator(k8sClient),
	}

	err := manager.prepareFailedUpgradeRetry(context.Background(), logr.Discard(), cluster, upgrade.RetryRequestValue(cluster))
	if err != nil {
		t.Fatalf("prepareFailedUpgradeRetry() error = %v, want nil", err)
	}

	remainingTargetPod := &corev1.Pod{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: currentTargetPod.Name, Namespace: cluster.Namespace}, remainingTargetPod); err != nil {
		t.Fatalf("current target pod was deleted unexpectedly: %v", err)
	}

	deletedCompletedPod := &corev1.Pod{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{Name: staleCompletedPod.Name, Namespace: cluster.Namespace}, deletedCompletedPod)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected stale completed pod to be deleted, got err=%v", err)
	}
}

func retryPodForResetTest(namespace, name, revision, image string, ready bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				appsv1.StatefulSetRevisionLabel: revision,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  constants.ContainerBao,
					Image: image,
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
	if ready {
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			},
		}
	}
	return pod
}

func TestPrepareFailedUpgradeRetry_SuccessClearsFailureAndRemovesRetrySignal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		withManagedFields bool
	}{
		{name: "with managed fields", withManagedFields: true},
		{name: "without managed fields", withManagedFields: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("add openbao scheme: %v", err)
			}
			if err := appsv1.AddToScheme(scheme); err != nil {
				t.Fatalf("add appsv1 scheme: %v", err)
			}
			if err := batchv1.AddToScheme(scheme); err != nil {
				t.Fatalf("add batchv1 scheme: %v", err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("add corev1 scheme: %v", err)
			}

			now := metav1.Now()
			startedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version: "2.5.0",
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Requests: &openbaov1alpha1.UpgradeRequestConfig{
							Retry: "retry-now",
						},
					},
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					Upgrade: &openbaov1alpha1.UpgradeProgress{
						TargetVersion:    "2.5.0",
						FromVersion:      "2.4.0",
						CurrentPartition: 2,
						CompletedPods:    []int32{2},
						StartedAt:        &startedAt,
						Failure: &openbaov1alpha1.ControllerErrorStatus{
							Reason:  upgrade.ReasonUpgradeFailed,
							Message: "step-down timed out",
							At:      &now,
						},
						LastStepDownTime: &now,
					},
				},
			}

			targetOrdinal := cluster.Status.Upgrade.CurrentPartition - 1
			targetPod := "test-cluster-1"
			if targetOrdinal != 1 {
				t.Fatalf("targetOrdinal=%d, want 1", targetOrdinal)
			}

			jobName := upgrade.ExecutorJobName(cluster.Name, upgrade.ExecutorActionRollingStepDownLeader, targetPod, "", "")
			staleJob := managedRollingJob(&batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: cluster.Namespace,
				},
			}, cluster)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
					Annotations: map[string]string{
						constants.AnnotationClusterGeneration: "0",
						constants.AnnotationResolvedMainImage: "openbao/openbao:2.5.0",
					},
				},
				Status: appsv1.StatefulSetStatus{
					UpdateRevision: "rev-good",
				},
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  constants.ContainerBao,
									Image: "openbao/openbao:2.5.0",
								},
							},
						},
					},
				},
			}
			staleTargetPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      targetPod,
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						appsv1.StatefulSetRevisionLabel: "rev-bad",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.ContainerBao,
							Image: "openbao/openbao:retry-image-does-not-exist",
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			}

			builder := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
				WithObjects(withoutUpgradeStatus(cluster), staleJob, sts, staleTargetPod)
			if tt.withManagedFields {
				builder = builder.WithReturnManagedFields()
			}
			k8sClient := builder.Build()
			seedUpgradeStatus(t, k8sClient, cluster)

			mgr := &Manager{
				client:          k8sClient,
				reader:          k8sClient,
				scheme:          scheme,
				adminOpsMutator: testAdminOpsMutator(k8sClient),
			}

			err := mgr.prepareFailedUpgradeRetry(context.Background(), logr.Discard(), cluster, upgrade.RetryRequestValue(cluster))
			if err != nil {
				t.Fatalf("prepareFailedUpgradeRetry() unexpected error: %v", err)
			}

			if cluster.Status.Upgrade == nil {
				t.Fatalf("Upgrade=nil, want non-nil")
			}
			if cluster.Status.Upgrade.Failure != nil {
				t.Fatalf("Failure=%#v, want nil", cluster.Status.Upgrade.Failure)
			}
			if cluster.Status.Upgrade.LastStepDownTime != nil {
				t.Fatalf("LastStepDownTime=%v, want nil", cluster.Status.Upgrade.LastStepDownTime)
			}
			if cluster.Status.Upgrade.StartedAt == nil {
				t.Fatalf("StartedAt=nil, want refreshed timestamp")
			}
			if !cluster.Status.Upgrade.StartedAt.After(startedAt.Time) {
				t.Fatalf("StartedAt=%v, want time after %v", cluster.Status.Upgrade.StartedAt, startedAt)
			}
			if cluster.Status.Upgrade.TargetVersion != "2.5.0" {
				t.Fatalf("TargetVersion=%q, want %q", cluster.Status.Upgrade.TargetVersion, "2.5.0")
			}
			if cluster.Status.Upgrade.CurrentPartition != 2 {
				t.Fatalf("CurrentPartition=%d, want 2", cluster.Status.Upgrade.CurrentPartition)
			}
			gotHandledRetry := ""
			if cluster.Status.UpgradeRequests != nil {
				gotHandledRetry = cluster.Status.UpgradeRequests.LastHandledRetry
			}
			if gotHandledRetry != "retry-now" {
				t.Fatalf("LastHandledRetry=%q, want retry-now", gotHandledRetry)
			}

			deletedJob := &batchv1.Job{}
			err = k8sClient.Get(context.Background(), types.NamespacedName{Name: jobName, Namespace: cluster.Namespace}, deletedJob)
			if !apierrors.IsNotFound(err) {
				t.Fatalf("expected stale step-down job to be deleted, got err=%v", err)
			}

			deletedPod := &corev1.Pod{}
			err = k8sClient.Get(context.Background(), types.NamespacedName{Name: targetPod, Namespace: cluster.Namespace}, deletedPod)
			if !apierrors.IsNotFound(err) {
				t.Fatalf("expected stale target pod to be deleted, got err=%v", err)
			}
		})
	}
}
