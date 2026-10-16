package provisioner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kubebao/openbao-operator/internal/platform/entrypoint"
)

func TestGovernanceReadsBypassCache(t *testing.T) {
	options := newManagerOptions(metricsserver.Options{}, "", false)
	require.Contains(t, options.Client.Cache.DisableFor, &corev1.ResourceQuota{})
	require.Contains(t, options.Client.Cache.DisableFor, &corev1.LimitRange{})
}

func TestAdmissionStartupFailures(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "false")
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	cfg := runConfig{admissionEnforcement: entrypoint.AdmissionEnforcementFail}
	tracker, err := initializeAdmissionTracker(t.Context(), reader, cfg)
	require.Nil(t, tracker)
	require.ErrorContains(t, err, "admission policy dependencies not ready")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg.admissionStartupTimeout = time.Minute
	tracker, err = initializeAdmissionTracker(ctx, reader, cfg)
	require.Nil(t, tracker)
	require.ErrorIs(t, err, context.Canceled)

	cfg.admissionEnforcement = entrypoint.AdmissionEnforcementWarn
	tracker, err = initializeAdmissionTracker(t.Context(), reader, cfg)
	require.NoError(t, err)
	require.NotNil(t, tracker.Current())
	require.False(t, tracker.Current().OverallReady)

	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")
	tracker, err = initializeAdmissionTracker(t.Context(), nil, cfg)
	require.NoError(t, err)
	require.True(t, tracker.Current().OverallReady)
	require.True(t, tracker.Current().UnsafeMode)
}

func TestVerifyAdmissionCanary(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantError string
		status                int
	}{
		{name: "allowed", body: `{"kind":"Role","apiVersion":"rbac.authorization.k8s.io/v1"}`,
			status: http.StatusCreated, wantError: "but it was allowed"},
		{name: "unrelated denial", body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
			`"reason":"Forbidden","message":"RBAC denied","code":403}`,
			status: http.StatusForbidden, wantError: "not by the expected VAP message"},
		{name: "expected denial", body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
			`"reason":"Forbidden","message":"Provisioner can only create Roles","code":403}`,
			status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/apis/rbac.authorization.k8s.io/v1/namespaces/default/roles", r.URL.Path)
				assert.Equal(t, "All", r.URL.Query().Get("dryRun"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(server.Close)
			err := verifyAdmissionCanary(t.Context(), &rest.Config{Host: server.URL})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVerifyAdmissionCanaryClientFailure(t *testing.T) {
	err := verifyAdmissionCanary(t.Context(), &rest.Config{Host: "http://[invalid"})
	require.ErrorContains(t, err, "create Kubernetes clientset for admission canary")
}
