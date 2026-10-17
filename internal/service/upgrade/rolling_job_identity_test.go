package upgrade

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

func TestRollingStepDownJobProvenance(t *testing.T) {
	t.Parallel()
	started := metav1.NewTime(time.Unix(1000, 0))
	for _, tc := range []struct {
		name, executionID, annotationRunID string
		created                            int64
		missingStart, wrongAction, valid   bool
	}{
		{name: "legacy current", created: 1001, valid: true},
		{name: "legacy same second", created: 1000, valid: true},
		{name: "legacy prior upgrade", created: 999},
		{name: "legacy missing creation time"},
		{name: "legacy missing start", created: 1001, missingStart: true},
		{name: "new scoped identity", executionID: "attempt-2", valid: true},
		{name: "wrong identity", executionID: "attempt-2", annotationRunID: "attempt-1/cluster-2"},
		{name: "wrong action", executionID: "attempt-2", wrongAction: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &api.OpenBaoCluster{Status: api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{
				ExecutionID: tc.executionID, StartedAt: &started,
			}}}
			if tc.missingStart {
				cluster.Status.Upgrade.StartedAt = nil
			}
			runID := RollingStepDownRunID(cluster, "cluster-2")
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: "step-down", Annotations: buildUpgradeExecutorJobAnnotations(ExecutorActionRollingStepDownLeader, runID),
			}}
			if tc.created != 0 {
				job.CreationTimestamp = metav1.NewTime(time.Unix(tc.created, 0))
			}
			if tc.annotationRunID != "" {
				job.Annotations["openbao.org/upgrade-run-id"] = tc.annotationRunID
			}
			if tc.wrongAction {
				job.Annotations["openbao.org/upgrade-action"] = "other"
			}
			err := ValidateRollingStepDownJob(cluster, job, runID)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, RequestRetryFieldPath)
			}
		})
	}
}

func TestRollingExecutorIgnoresRetainedPriorAttempt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status batchv1.JobStatus
	}{
		{name: "succeeded", status: batchv1.JobStatus{Succeeded: 1}},
		{name: "failed", status: batchv1.JobStatus{Failed: 1}},
		{name: "running", status: batchv1.JobStatus{Active: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, api.AddToScheme(scheme))
			require.NoError(t, batchv1.AddToScheme(scheme))
			cluster := &api.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default", UID: "cluster-uid"},
				Spec:       api.OpenBaoClusterSpec{Upgrade: &api.UpgradeConfig{Image: "executor:test", JWTAuthRole: "upgrade"}},
				Status:     api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{ExecutionID: "attempt-1"}},
			}
			oldRunID := RollingStepDownRunID(cluster, "cluster-2")
			old := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: ExecutorJobName(cluster.Name, ExecutorActionRollingStepDownLeader, oldRunID, "", ""), Namespace: cluster.Namespace,
				Annotations: buildUpgradeExecutorJobAnnotations(ExecutorActionRollingStepDownLeader, oldRunID),
			}, Status: tc.status}
			require.NoError(t, opslifecycle.PrepareManagedJobOwner(old, cluster, scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old).Build()
			cluster.Status.Upgrade.ExecutionID = "attempt-2"
			runID := RollingStepDownRunID(cluster, "cluster-2")
			result, err := EnsureExecutorJob(t.Context(), c, c, scheme, logr.Discard(), cluster,
				ExecutorActionRollingStepDownLeader, runID, "", "", portopenbao.ClientConfig{}, nil, "")
			require.NoError(t, err)
			require.True(t, result.Running)
			require.NotEqual(t, old.Name, result.Name)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(old), &batchv1.Job{}))
		})
	}
}

func TestRollingExecutorRejectsCreateCollisionWithWrongAttempt(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	cluster := &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default", UID: "cluster-uid"},
		Spec:       api.OpenBaoClusterSpec{Upgrade: &api.UpgradeConfig{Image: "executor:test", JWTAuthRole: "upgrade"}},
		Status:     api.OpenBaoClusterStatus{Upgrade: &api.UpgradeProgress{ExecutionID: "attempt-2"}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			obj.GetAnnotations()["openbao.org/upgrade-run-id"] = "other-attempt"
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, obj.GetName())
		},
	}).Build()
	result, err := EnsureExecutorJob(t.Context(), c, c, scheme, logr.Discard(), cluster,
		ExecutorActionRollingStepDownLeader, RollingStepDownRunID(cluster, "cluster-2"), "", "", portopenbao.ClientConfig{}, nil, "")
	require.Nil(t, result)
	require.ErrorContains(t, err, "unexpected action or attempt identity")
}
