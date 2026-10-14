package bluegreen

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
)

type failingGetClient struct {
	client.Client
	failNamespace string
	failName      string
	err           error
}

func (c *failingGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Namespace == c.failNamespace && key.Name == c.failName {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

type captureOperatorVerifier struct {
	imageRef string
	digest   string
	called   bool
}

func (v *captureOperatorVerifier) Verify(_ context.Context, imageRef string, _ imageverify.VerifyConfig) (string, error) {
	v.called = true
	v.imageRef = imageRef
	return v.digest, nil
}

func TestBuildSnapshotJob_PodSecurityContext_Platform(t *testing.T) {
	testScheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       types.UID("test-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			Backup: &openbaov1alpha1.BackupSchedule{
				Schedule: "0 0 * * *",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint:     "https://s3.example.com",
					Bucket:       "bao",
					Region:       "us-east-1",
					UsePathStyle: true,
				},
			},
		},
	}

	const (
		jobName = "pre-upgrade-snapshot"
		phase   = "pre-upgrade"
		image   = "example.com/backup-executor@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	t.Run("openshift omits pinned IDs", func(t *testing.T) {
		mgr := &Manager{
			Platform:      constants.PlatformOpenShift,
			backupRuntime: backup.NewUpgradeStrategyRuntime(fakeClient, testScheme),
		}
		job, err := mgr.buildSnapshotJob(cluster, jobName, phase, image)
		require.NoError(t, err)

		sc := job.Spec.Template.Spec.SecurityContext
		require.NotNil(t, sc)
		require.Equal(t, ptr.To(true), sc.RunAsNonRoot)
		require.NotNil(t, sc.SeccompProfile)
		require.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type)

		require.Nil(t, sc.RunAsUser)
		require.Nil(t, sc.RunAsGroup)
		require.Nil(t, sc.FSGroup)

		require.Equal(t, ComponentUpgradeSnapshot, job.Labels[constants.LabelOpenBaoComponent])
		require.Equal(t, ComponentUpgradeSnapshot, job.Spec.Template.Labels[constants.LabelOpenBaoComponent])
		require.Equal(t, phase, job.Annotations[AnnotationSnapshotPhase])
	})

	t.Run("kubernetes pins IDs", func(t *testing.T) {
		mgr := &Manager{
			Platform:      constants.PlatformKubernetes,
			backupRuntime: backup.NewUpgradeStrategyRuntime(fakeClient, testScheme),
		}
		job, err := mgr.buildSnapshotJob(cluster, jobName, phase, image)
		require.NoError(t, err)

		sc := job.Spec.Template.Spec.SecurityContext
		require.NotNil(t, sc)
		require.Equal(t, ptr.To(constants.UserBackup), sc.RunAsUser)
		require.Equal(t, ptr.To(constants.GroupBackup), sc.RunAsGroup)
		require.Equal(t, ptr.To(constants.GroupBackup), sc.FSGroup)
	})
}

func TestEnsurePreUpgradeSnapshotJob_RequiresBackupAuthUnlessOIDC(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       types.UID("test-cluster-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
			Backup: &openbaov1alpha1.BackupSchedule{
				Image: "test-image:latest",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://test-endpoint",
					Bucket:   "test-bucket",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	mgr := NewManager(fakeClient, scheme, nil, backup.NewUpgradeStrategyRuntime(fakeClient, scheme), portopenbao.ClientConfig{}, security.NewImageVerifier(testLogger(), fakeClient, nil), nil, "kubernetes")

	_, err := mgr.ensurePreUpgradeSnapshotJob(context.Background(), testLogger(), cluster, "pre-upgrade-snapshot")
	require.Error(t, err)
	require.Contains(t, err.Error(), "backup authentication is required")
}

func TestEnsurePreUpgradeSnapshotJob_AllowsOIDCDefaultRole(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       types.UID("test-cluster-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment,
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
			Backup: &openbaov1alpha1.BackupSchedule{
				Image: "test-image:latest",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://test-endpoint",
					Bucket:   "test-bucket",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	mgr := NewManager(fakeClient, scheme, nil, backup.NewUpgradeStrategyRuntime(fakeClient, scheme), portopenbao.ClientConfig{}, security.NewImageVerifier(testLogger(), fakeClient, nil), nil, "kubernetes")

	result, err := mgr.ensurePreUpgradeSnapshotJob(context.Background(), testLogger(), cluster, "pre-upgrade-snapshot")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Running)
}

func TestEnsurePreUpgradeSnapshotJob_VerifiesDefaultBackupExecutorImageForHardenedCluster(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "0.1.0")

	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       types.UID("test-cluster-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileHardened,
			Backup: &openbaov1alpha1.BackupSchedule{
				JWTAuthRole: "backup",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: "http://test-endpoint",
					Bucket:   "test-bucket",
					S3:       &openbaov1alpha1.S3TargetConfig{RoleARN: ptr.To("nested-snapshot-role")},
				},
			},
			Network: &openbaov1alpha1.NetworkConfig{
				EgressRules: []networkingv1.NetworkPolicyEgressRule{testExplicitEgressRule()},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	operatorVerifier := &captureOperatorVerifier{
		digest: constants.DefaultBackupImageRepository + "@sha256:3333333333333333333333333333333333333333333333333333333333333333",
	}
	mgr := NewManager(
		fakeClient,
		scheme,
		nil,
		backup.NewUpgradeStrategyRuntime(fakeClient, scheme),
		portopenbao.ClientConfig{},
		security.NewImageVerifier(testLogger(), fakeClient, nil),
		operatorVerifier,
		"kubernetes",
	)

	result, err := mgr.ensurePreUpgradeSnapshotJob(context.Background(), testLogger(), cluster, "pre-upgrade-snapshot")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Running)
	require.True(t, operatorVerifier.called)
	require.Equal(t, constants.DefaultBackupImageRepository+":0.1.0", operatorVerifier.imageRef)

	job := &batchv1.Job{}
	err = fakeClient.Get(context.Background(), client.ObjectKey{Name: "pre-upgrade-snapshot", Namespace: cluster.Namespace}, job)
	require.NoError(t, err)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, operatorVerifier.digest, job.Spec.Template.Spec.Containers[0].Image)
	assert.Contains(t, job.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
		Name: constants.EnvAWSRoleARN, Value: "nested-snapshot-role",
	})
}

func TestHandlePhaseIdle_BlocksOnFailedSnapshot(t *testing.T) {
	// Setup scheme
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	// Define cluster with Blue/Green strategy and PreUpgradeSnapshot enabled
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       types.UID("test-cluster-uid"),
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Replicas: 3,
			Image:    "openbao:2.5.0",
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
				BlueGreen: &openbaov1alpha1.BlueGreenConfig{
					PreUpgradeSnapshot: true,
				},
			},
			Backup: &openbaov1alpha1.BackupSchedule{
				JWTAuthRole: "test-role",
				Target: openbaov1alpha1.BackupTarget{
					Bucket: "test-bucket",
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			CurrentVersion: "2.4.0",
			Initialized:    true,
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:        openbaov1alpha1.PhaseIdle,
				BlueRevision: "rev-blue",
			},
		},
	}

	// Create a FAILED snapshot job
	// We need to calculate the deterministic job name first
	// For testing simplicty, we can trust the manager to find it if we name it correctly used by the Manager
	// But calculateRevision depends on image/version/replicas.
	// Let's create the manager first to calculate the name, then inject the job.

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	mgr := NewManager(fakeClient, scheme, nil, backup.NewUpgradeStrategyRuntime(fakeClient, scheme), portopenbao.ClientConfig{}, security.NewImageVerifier(testLogger(), fakeClient, nil), nil, "kubernetes")

	jobName := preUpgradeSnapshotJobName(cluster)

	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelOpenBaoComponent: ComponentUpgradeSnapshot,
			},
		},
		Status: batchv1.JobStatus{
			Failed: 1,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}

	// Add job to client
	err := fakeClient.Create(context.Background(), failedJob)
	assert.NoError(t, err)

	// Set the status to indicate we already tried creating it
	cluster.Status.BlueGreen.PreUpgradeSnapshotJobName = jobName

	// Run handlePhaseIdle
	outcome, err := mgr.handlePhaseIdle(context.Background(), testLogger(), cluster, "")

	// Expectation: Should fail or hold, NOT advance
	// Currently (BUG), it likely returns advance(PhaseDeployingGreen)

	if err != nil {
		t.Logf("Got error as expected: %v", err)
	} else {
		// If no error, check outcome
		assert.NotEqual(t, phaseOutcomeAdvance, outcome.kind, "Should not advance when snapshot failed")
		assert.NotEqual(t, openbaov1alpha1.PhaseDeployingGreen, outcome.nextPhase, "Should not transition to DeployingGreen")
	}
}

func TestHandlePhaseIdle_RespectsTopLevelPreUpgradeSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Replicas: 3,
			Image:    "openbao:2.5.0",
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy:           openbaov1alpha1.UpdateStrategyBlueGreen,
				PreUpgradeSnapshot: true,
				BlueGreen: &openbaov1alpha1.BlueGreenConfig{
					PreUpgradeSnapshot: false,
				},
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			CurrentVersion: "2.4.0",
			Initialized:    true,
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:        openbaov1alpha1.PhaseIdle,
				BlueRevision: "rev-blue",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	mgr := NewManager(fakeClient, scheme, nil, backup.NewUpgradeStrategyRuntime(fakeClient, scheme), portopenbao.ClientConfig{}, security.NewImageVerifier(testLogger(), fakeClient, nil), nil, "kubernetes")

	jobName := preUpgradeSnapshotJobName(cluster)
	cluster.Status.BlueGreen.PreUpgradeSnapshotJobName = jobName
	runningJob := managedBlueGreenJob(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cluster.Namespace,
		},
	}, cluster)
	require.NoError(t, fakeClient.Create(context.Background(), runningJob))

	outcome, err := mgr.handlePhaseIdle(context.Background(), testLogger(), cluster, "")
	require.NoError(t, err)
	require.Equal(t, phaseOutcomeRequeueAfter, outcome.kind, "should requeue while pre-upgrade snapshot is running")
}

func TestHandlePhaseIdle_FailsWhenSnapshotStatusLookupFails(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = openbaov1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:  "2.5.0",
			Replicas: 3,
			Image:    "openbao:2.5.0",
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Strategy:           openbaov1alpha1.UpdateStrategyBlueGreen,
				PreUpgradeSnapshot: true,
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			CurrentVersion: "2.4.0",
			Initialized:    true,
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				Phase:        openbaov1alpha1.PhaseIdle,
				BlueRevision: "rev-blue",
			},
		},
	}

	jobName := preUpgradeSnapshotJobName(cluster)
	cluster.Status.BlueGreen.PreUpgradeSnapshotJobName = jobName

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	mgr := NewManager(
		&failingGetClient{
			Client:        baseClient,
			failNamespace: cluster.Namespace,
			failName:      jobName,
			err:           errors.New("boom"),
		},
		scheme,
		nil,
		nil,
		portopenbao.ClientConfig{},
		nil,
		nil,
		"kubernetes",
	)

	outcome, err := mgr.handlePhaseIdle(context.Background(), testLogger(), cluster, "")
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to check pre-upgrade snapshot job status")
	require.Equal(t, phaseOutcome{}, outcome)
	require.Empty(t, cluster.Status.BlueGreen.GreenRevision)
}

func testLogger() logr.Logger {
	return logr.Discard() // Simplistic logger
}

func TestPreUpgradeSnapshotJobName(t *testing.T) {
	tests := []struct {
		name       string
		cluster    *openbaov1alpha1.OpenBaoCluster
		wantChange func(c *openbaov1alpha1.OpenBaoCluster)
	}{
		{
			name: "stable for same inputs",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "snapshot-cluster"},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version:  "2.4.4",
					Image:    "example.com/openbao:2.4.4",
					Replicas: 3,
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					CurrentVersion: "2.4.3",
					BlueGreen: &openbaov1alpha1.BlueGreenStatus{
						BlueRevision: "blue",
					},
				},
			},
		},
		{
			name: "changes when target version changes",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "snapshot-cluster"},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version:  "2.4.4",
					Image:    "example.com/openbao:2.4.4",
					Replicas: 3,
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					CurrentVersion: "2.4.3",
					BlueGreen: &openbaov1alpha1.BlueGreenStatus{
						BlueRevision: "blue",
					},
				},
			},
			wantChange: func(c *openbaov1alpha1.OpenBaoCluster) { c.Spec.Version = "2.4.5" },
		},
		{
			name: "stays within DNS label limit for long cluster names",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "this-is-a-very-long-cluster-name-that-must-be-trimmed-to-fit"},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version:  "2.4.4",
					Image:    "example.com/openbao:2.4.4",
					Replicas: 3,
				},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					CurrentVersion: "2.4.3",
					BlueGreen: &openbaov1alpha1.BlueGreenStatus{
						BlueRevision: "blue",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := preUpgradeSnapshotJobName(tt.cluster)
			if first == "" {
				t.Fatalf("expected non-empty job name")
			}
			second := preUpgradeSnapshotJobName(tt.cluster)
			if first != second {
				t.Fatalf("expected stable job name, got %q then %q", first, second)
			}
			if len(first) > 63 {
				t.Fatalf("expected job name <= 63 chars, got %d: %q", len(first), first)
			}

			if tt.wantChange != nil {
				changed := tt.cluster.DeepCopy()
				tt.wantChange(changed)
				third := preUpgradeSnapshotJobName(changed)
				if third == first {
					t.Fatalf("expected job name to change after input change, got %q", third)
				}
			}
		})
	}
}

func testExplicitEgressRule() networkingv1.NetworkPolicyEgressRule {
	port := intstr.FromInt32(443)
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "objectstore",
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: ptr.To(corev1.ProtocolTCP),
				Port:     &port,
			},
		},
	}
}
