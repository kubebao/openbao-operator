package restore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestTargetApplicationIdentityAndDeadlineSurviveManagerReplacement(t *testing.T) {
	require.Equal(t, 10*time.Minute, targetApplicationTimeout, "the live identity scenario retains the production deadline")
	for _, identity := range []string{"matching", "wrong cluster", "wrong version", "health error"} {
		for _, expired := range []bool{false, true} {
			t.Run(identity+map[bool]string{false: "/before deadline", true: "/after deadline"}[expired], func(t *testing.T) {
				c, request := freshTargetFixture(t)
				age := targetApplicationTimeout - time.Minute
				if expired {
					age = targetApplicationTimeout + time.Minute
				}
				claimedAt := metav1.NewTime(time.Now().Add(-age).Truncate(time.Second))
				request.Status.Phase = api.RestorePhaseUnknown
				request.Status.SubmissionClaim = &api.RestoreSubmissionClaim{ClaimedAt: claimedAt}
				request.Status.Target = &api.RestoreTargetStatus{
					UID: "target", DataPVCUID: "data", BootstrapClusterID: "bootstrap", ReservedAt: metav1.Now(),
				}
				cluster, pvc := freshTargetObjects(request)
				require.NoError(t, c.Create(t.Context(), cluster))
				require.NoError(t, c.Create(t.Context(), pvc))
				require.NoError(t, c.Status().Update(t.Context(), request))
				health := portopenbao.HealthStatus{
					Initialized: true, ClusterID: request.Spec.Source.ExpectedClusterID, Version: request.Spec.Source.ExpectedVersion,
				}
				var healthError error
				switch identity {
				case "wrong cluster":
					health.ClusterID = "another-source"
				case "wrong version":
					health.Version = "another-version"
				case "health error":
					healthError = errors.New("health temporarily unavailable")
				}
				for range 8 {
					// Recreate the manager and reload the durable request for each observation.
					require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(request), request))
					manager := NewManager(c, c.Scheme(), nil, nil, "")
					manager.readHealth = func(context.Context, portopenbao.ClientConfig) (*portopenbao.HealthStatus, error) {
						return &health, healthError
					}
					_, err := manager.observeTargetApplication(t.Context(), logr.Discard(), request)
					require.NoError(t, err)
					require.True(t, claimedAt.Equal(&request.Status.SubmissionClaim.ClaimedAt), "retries must not renew the deadline")
					if identity == "matching" {
						require.NotNil(t, request.Status.Target.AppliedAt)
						return
					}
					require.Nil(t, request.Status.Target.AppliedAt)
				}
				if expired {
					require.Equal(t, api.RestoreTargetCleanupComplete, request.Status.Target.Cleanup)
					require.Equal(t, api.RestorePhaseFailed, request.Status.Phase)
					require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster)))
					require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc)))
					return
				}
				require.Empty(t, request.Status.Target.Cleanup, "identity mismatch must not delete the target before its deadline")
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(pvc), pvc))
			})
		}
	}
}
