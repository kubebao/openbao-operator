package upgrade

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/internal/port/imageverify"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

type captureImageVerifier struct {
	imageRef string
	digest   string
	called   bool
}

func (v *captureImageVerifier) Verify(_ context.Context, imageRef string, _ imageverify.VerifyConfig) (string, error) {
	v.called = true
	v.imageRef = imageRef
	return v.digest, nil
}

func TestBuildUpgradeExecutorJob_SecurityContext(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Image:       "test-image",
				JWTAuthRole: "test-role",
			},
		},
	}

	tests := []struct {
		name     string
		platform string
		wantUser *int64
		wantGrp  *int64
		wantFS   *int64
	}{
		{
			name:     "kubernetes platform pins IDs",
			platform: constants.PlatformKubernetes,
			wantUser: ptr.To(constants.UserBackup), // Backup and Upgrade use same IDs
			wantGrp:  ptr.To(constants.GroupBackup),
			wantFS:   ptr.To(constants.GroupBackup),
		},
		{
			name:     "openshift platform omits IDs",
			platform: constants.PlatformOpenShift,
			wantUser: nil,
			wantGrp:  nil,
			wantFS:   nil,
		},
		{
			name:     "empty platform defaults to pinning IDs",
			platform: "",
			wantUser: ptr.To(constants.UserBackup),
			wantGrp:  ptr.To(constants.GroupBackup),
			wantFS:   ptr.To(constants.GroupBackup),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, err := buildUpgradeExecutorJob(
				cluster,
				"test-job",
				ExecutorAction("test"),
				"run-id",
				"",
				"",
				"",
				portopenbao.ClientConfig{},
				tt.platform,
			)
			if err != nil {
				t.Fatalf("buildUpgradeExecutorJob() error = %v", err)
			}

			sc := job.Spec.Template.Spec.SecurityContext
			if sc == nil {
				t.Fatal("SecurityContext is nil")
			}

			if !ptrInt64Equal(sc.RunAsUser, tt.wantUser) {
				t.Errorf("RunAsUser = %v, want %v", ptrInt64Value(sc.RunAsUser), ptrInt64Value(tt.wantUser))
			}
			if !ptrInt64Equal(sc.RunAsGroup, tt.wantGrp) {
				t.Errorf("RunAsGroup = %v, want %v", ptrInt64Value(sc.RunAsGroup), ptrInt64Value(tt.wantGrp))
			}
			if !ptrInt64Equal(sc.FSGroup, tt.wantFS) {
				t.Errorf("FSGroup = %v, want %v", ptrInt64Value(sc.FSGroup), ptrInt64Value(tt.wantFS))
			}
		})
	}
}

func TestBuildUpgradeExecutorJob_AllowsOIDCWithoutUpgradeConfig(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "0.0.0-test")

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
			Upgrade: nil,
		},
	}

	job, err := buildUpgradeExecutorJob(
		cluster,
		"test-job",
		ExecutorActionRollingStepDownLeader,
		"pod-0",
		"",
		"",
		"",
		portopenbao.ClientConfig{JWTAuthStrategy: portopenbao.JWTAuthStrategyStandard},
		constants.PlatformKubernetes,
	)
	if err != nil {
		t.Fatalf("buildUpgradeExecutorJob() error = %v", err)
	}

	if job.Spec.Template.Spec.ServiceAccountName != "test-cluster-upgrade-serviceaccount" {
		t.Fatalf("ServiceAccountName = %q, want %q", job.Spec.Template.Spec.ServiceAccountName, "test-cluster-upgrade-serviceaccount")
	}

	foundRole := false
	foundStrategy := false
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == constants.EnvUpgradeJWTAuthRole {
			foundRole = true
			if env.Value != portauth.RoleNameUpgrade {
				t.Fatalf("UPGRADE_JWT_AUTH_ROLE = %q, want %q", env.Value, portauth.RoleNameUpgrade)
			}
		}
		if env.Name == constants.EnvOpenBaoJWTAuthStrategy {
			foundStrategy = true
			if env.Value != portopenbao.JWTAuthStrategyStandard {
				t.Fatalf("OPENBAO_JWT_AUTH_STRATEGY = %q, want %q", env.Value, portopenbao.JWTAuthStrategyStandard)
			}
		}
	}
	if !foundRole {
		t.Fatalf("missing %s env var", constants.EnvUpgradeJWTAuthRole)
	}
	if !foundStrategy {
		t.Fatalf("missing %s env var", constants.EnvOpenBaoJWTAuthStrategy)
	}
}

func TestBuildUpgradeExecutorJob_SetsResourceRequirements(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Image:       "test-image",
				JWTAuthRole: "test-role",
			},
		},
	}

	job, err := buildUpgradeExecutorJob(
		cluster,
		"test-job",
		ExecutorAction("test"),
		"run-id",
		"",
		"",
		"",
		portopenbao.ClientConfig{},
		constants.PlatformKubernetes,
	)
	if err != nil {
		t.Fatalf("buildUpgradeExecutorJob() error = %v", err)
	}

	resources := job.Spec.Template.Spec.Containers[0].Resources
	if got := resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("100m")) != 0 {
		t.Fatalf("CPU request = %s, want 100m", got.String())
	}
	if got := resources.Requests[corev1.ResourceMemory]; got.Cmp(resource.MustParse("128Mi")) != 0 {
		t.Fatalf("memory request = %s, want 128Mi", got.String())
	}
	if got := resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse("500m")) != 0 {
		t.Fatalf("CPU limit = %s, want 500m", got.String())
	}
	if got := resources.Limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse("512Mi")) != 0 {
		t.Fatalf("memory limit = %s, want 512Mi", got.String())
	}
}

func TestBuildUpgradeExecutorJob_RequiresJWTAuthWhenOIDCDisabled(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "0.0.0-test")

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: false,
				},
			},
			Upgrade: nil,
		},
	}

	_, err := buildUpgradeExecutorJob(
		cluster,
		"test-job",
		ExecutorActionRollingStepDownLeader,
		"pod-0",
		"",
		"",
		"",
		portopenbao.ClientConfig{},
		constants.PlatformKubernetes,
	)
	if err == nil {
		t.Fatalf("buildUpgradeExecutorJob() expected error, got nil")
	}
}

func TestBuildUpgradeExecutorJob_DefaultImageConfigurationError(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "")

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
					Enabled: true,
				},
			},
		},
	}

	_, err := buildUpgradeExecutorJob(
		cluster,
		"test-job",
		ExecutorActionRollingStepDownLeader,
		"pod-0",
		"",
		"",
		"",
		portopenbao.ClientConfig{},
		constants.PlatformKubernetes,
	)
	if err == nil {
		t.Fatal("buildUpgradeExecutorJob() error = nil, want error")
	}
	if reason, ok := operatorerrors.Reason(err); !ok || reason != constants.ReasonHelperImageConfigurationInvalid {
		t.Fatalf("reason = %q,%v want %q,true", reason, ok, constants.ReasonHelperImageConfigurationInvalid)
	}
}

func ptrInt64Equal(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrInt64Value(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestEnsureExecutorJob_CreateAlreadyExists(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-time.Second))
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				Image:       "upgrade-executor:0.1.0",
				JWTAuthRole: "upgrade-role",
			},
		},
		Status: openbaov1alpha1.OpenBaoClusterStatus{Upgrade: &openbaov1alpha1.UpgradeProgress{StartedAt: &started}},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*batchv1.Job); ok {
					obj.SetCreationTimestamp(metav1.Now())
					if err := c.Create(ctx, obj, opts...); err != nil {
						return err
					}
					return apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, obj.GetName())
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()

	result, err := EnsureExecutorJob(
		context.Background(),
		k8sClient,
		k8sClient,
		scheme,
		logr.Discard(),
		cluster,
		ExecutorActionRollingStepDownLeader,
		"run-1",
		"",
		"",
		portopenbao.ClientConfig{},
		nil,
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, ExecutorJobName(cluster.Name, ExecutorActionRollingStepDownLeader, "run-1", "", ""), result.Name)
	assert.True(t, result.Exists)
	assert.True(t, result.Running)
	assert.False(t, result.Succeeded)
	assert.False(t, result.Failed)
}

func TestEnsureExecutorJob_RejectsForeignSucceededJob(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-cluster",
		Namespace: "default",
		UID:       "test-cluster-uid",
	}}
	jobName := ExecutorJobName(
		cluster.Name,
		ExecutorActionRollingStepDownLeader,
		"run-1",
		"",
		"",
	)
	foreignJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: cluster.Namespace},
		Status:     batchv1.JobStatus{Succeeded: 1},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreignJob).Build()

	result, err := EnsureExecutorJob(
		context.Background(),
		k8sClient,
		k8sClient,
		scheme,
		logr.Discard(),
		cluster,
		ExecutorActionRollingStepDownLeader,
		"run-1",
		"",
		"",
		portopenbao.ClientConfig{},
		nil,
		"",
	)
	require.Nil(t, result)
	assert.ErrorContains(t, err, "requires managed controller owner OpenBaoCluster")
}

func TestEnsureExecutorJob_VerifiesDefaultUpgradeExecutorImageForHardenedCluster(t *testing.T) {
	t.Setenv(constants.EnvOperatorVersion, "0.1.0")

	scheme := runtime.NewScheme()
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       "test-cluster-uid",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileHardened,
			Replicas: 3,
			Upgrade: &openbaov1alpha1.UpgradeConfig{
				JWTAuthRole: "upgrade-role",
			},
		},
	}

	verifier := &captureImageVerifier{
		digest: constants.DefaultUpgradeImageRepository + "@sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	result, err := EnsureExecutorJob(
		context.Background(),
		k8sClient,
		k8sClient,
		scheme,
		logr.Discard(),
		cluster,
		ExecutorActionRollingStepDownLeader,
		"run-1",
		"",
		"",
		portopenbao.ClientConfig{},
		verifier,
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, verifier.called)
	require.Equal(t, constants.DefaultUpgradeImageRepository+":0.1.0", verifier.imageRef)

	job := &batchv1.Job{}
	err = k8sClient.Get(context.Background(), client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      result.Name,
	}, job)
	require.NoError(t, err)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, verifier.digest, job.Spec.Template.Spec.Containers[0].Image)
}
