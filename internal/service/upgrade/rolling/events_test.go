package rolling

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
)

func expectEventContains(t *testing.T, recorder *events.FakeRecorder, parts ...string) {
	t.Helper()

	select {
	case event := <-recorder.Events:
		for _, part := range parts {
			if !strings.Contains(event, part) {
				t.Fatalf("event %q does not contain %q", event, part)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("expected event, got none")
	}
}

func newRollingEventTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batchv1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rbacv1 scheme: %v", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbao scheme: %v", err)
	}
	return scheme
}

func TestInitializeUpgrade_EmitsUpgradeStartedEvent(t *testing.T) {
	t.Parallel()

	scheme := newRollingEventTestScheme(t)
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upgrade-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Replicas: 3,
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			CurrentVersion: "2.4.0",
			Initialized:    true,
		},
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{constants.AnnotationClusterGeneration: "0"},
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	recorder := events.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, sts).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()
	manager := &Manager{
		client:          k8sClient,
		reader:          k8sClient,
		scheme:          scheme,
		recorder:        recorder,
		adminOpsMutator: testAdminOpsMutator(k8sClient),
	}

	if err := manager.startUpgradeExecutionIfNeeded(context.Background(), logr.Discard(), cluster, nil, "rolling"); err != nil {
		t.Fatalf("startUpgradeExecutionIfNeeded() error = %v", err)
	}

	expectEventContains(t, recorder, "Normal", upgrade.ReasonUpgradeStarted)
}

func TestPrepareFailedUpgradeRetry_EmitsRetryEvents(t *testing.T) {
	t.Parallel()

	scheme := newRollingEventTestScheme(t)
	now := metav1.NewTime(time.Now())
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

	targetPod := fmt.Sprintf("%s-%d", cluster.Name, cluster.Status.Upgrade.CurrentPartition-1)
	staleJob := managedRollingJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      upgrade.ExecutorJobName(cluster.Name, upgrade.ExecutorActionRollingStepDownLeader, targetPod, "", ""),
			Namespace: cluster.Namespace,
		},
	}, cluster)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.AnnotationClusterGeneration: "0",
				constants.AnnotationResolvedMainImage: "openbao/openbao:2.5.0",
			},
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision: "rev-good",
		},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  constants.ContainerBao,
						Image: "openbao/openbao:2.5.0",
					}},
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
			Containers: []corev1.Container{{
				Name:  constants.ContainerBao,
				Image: "openbao/openbao:2.4.0",
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	recorder := events.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(withoutUpgradeStatus(cluster), staleJob, sts, staleTargetPod).
		WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
		WithReturnManagedFields().
		Build()
	seedUpgradeStatus(t, k8sClient, cluster)
	manager := &Manager{
		client:          k8sClient,
		reader:          k8sClient,
		scheme:          scheme,
		recorder:        recorder,
		adminOpsMutator: testAdminOpsMutator(k8sClient),
	}

	err := manager.prepareFailedUpgradeRetry(context.Background(), logr.Discard(), cluster, upgrade.RetryRequestValue(cluster))
	if err != nil {
		t.Fatalf("prepareFailedUpgradeRetry() error = %v", err)
	}

	expectEventContains(t, recorder, "Normal", upgrade.ReasonRollingRetryRequested)
	expectEventContains(t, recorder, "Normal", upgrade.ReasonRollingRetryAccepted)
}

func TestHandlePreUpgradeSnapshot_EmitsSnapshotEvents(t *testing.T) {
	t.Parallel()

	t.Run("job created", func(t *testing.T) {
		t.Parallel()

		scheme := newRollingEventTestScheme(t)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-ns",
				UID:       types.UID("test-cluster-uid"),
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile:  openbaov1alpha1.ProfileDevelopment,
				Version:  "2.4.4",
				Replicas: 3,
				Upgrade: &openbaov1alpha1.UpgradeConfig{
					PreUpgradeSnapshot: true,
				},
				Backup: &openbaov1alpha1.BackupSchedule{
					Image:       "test-image:latest",
					JWTAuthRole: "backup",
					Target: openbaov1alpha1.BackupTarget{
						Endpoint: "http://test-endpoint",
						Bucket:   "test-bucket",
					},
				},
			},
			Status: openbaov1alpha1.OpenBaoClusterStatus{
				CurrentVersion: "2.4.3",
				Initialized:    true,
			},
		}

		recorder := events.NewFakeRecorder(10)
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cluster).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithReturnManagedFields().
			Build()
		manager := NewManager(
			k8sClient,
			scheme,
			backup.NewUpgradeStrategyRuntime(k8sClient, scheme),
			portopenbao.ClientConfig{},
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
			recorder,
		)

		complete, err := manager.handlePreUpgradeSnapshot(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePreUpgradeSnapshot() error = %v", err)
		}
		if complete {
			t.Fatal("complete = true, want false")
		}

		expectEventContains(t, recorder, "Normal", upgrade.ReasonPreUpgradeSnapshotJobCreated)
	})

	t.Run("job completed", func(t *testing.T) {
		t.Parallel()

		scheme := newRollingEventTestScheme(t)
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-cluster",
				Namespace:  "test-ns",
				UID:        types.UID("test-cluster-uid"),
				Generation: 1,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Version:  "2.4.4",
				Replicas: 3,
				Upgrade: &openbaov1alpha1.UpgradeConfig{
					PreUpgradeSnapshot: true,
				},
				Backup: &openbaov1alpha1.BackupSchedule{
					Image:       "test-image:latest",
					JWTAuthRole: "backup",
					Target: openbaov1alpha1.BackupTarget{
						Endpoint: "http://test-endpoint",
						Bucket:   "test-bucket",
					},
				},
			},
			Status: openbaov1alpha1.OpenBaoClusterStatus{
				CurrentVersion: "2.4.3",
				Initialized:    true,
			},
		}
		jobName := (&Manager{}).backupJobName(cluster)
		job := managedRollingJob(&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: cluster.Namespace,
				Labels: map[string]string{
					constants.LabelAppInstance:       cluster.Name,
					constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
					constants.LabelOpenBaoCluster:    cluster.Name,
					constants.LabelOpenBaoComponent:  constants.ComponentBackup,
					constants.LabelOpenBaoBackupType: constants.BackupTypePreUpgrade,
				},
			},
			Status: batchv1.JobStatus{
				Succeeded: 1,
			},
		}, cluster)

		recorder := events.NewFakeRecorder(10)
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cluster, job).
			WithStatusSubresource(&openbaov1alpha1.OpenBaoCluster{}).
			WithReturnManagedFields().
			Build()
		manager := NewManager(
			k8sClient,
			scheme,
			backup.NewUpgradeStrategyRuntime(k8sClient, scheme),
			portopenbao.ClientConfig{},
			security.NewImageVerifier(logr.Discard(), k8sClient, nil),
			"",
			recorder,
		)

		complete, err := manager.handlePreUpgradeSnapshot(context.Background(), logr.Discard(), cluster)
		if err != nil {
			t.Fatalf("handlePreUpgradeSnapshot() error = %v", err)
		}
		if !complete {
			t.Fatal("complete = false, want true")
		}

		expectEventContains(t, recorder, "Normal", upgrade.ReasonPreUpgradeSnapshotCompleted)
	})
}
