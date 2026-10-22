package entrypoint

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/internal/platform/admission"
)

type readinessInformer struct {
	cache.Informer
	synced, stopped bool
}

func (i readinessInformer) HasSynced() bool { return i.synced }
func (i readinessInformer) IsStopped() bool { return i.stopped }

func TestManagerReadiness(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		running, synced, stopped bool
		unsafe                   string
		status                   *admission.Status
		wantError                string
	}{
		{name: "not started", wantError: "manager cache synchronization"},
		{name: "cache pending", running: true, wantError: "watched resource cache"},
		{name: "cache stopped", running: true, synced: true, stopped: true, wantError: "watched resource cache"},
		{name: "absent admission", running: true, synced: true, wantError: "have not been checked"},
		{name: "unready admission", running: true, synced: true,
			status: &admission.Status{CheckedAt: time.Now()}, wantError: "not ready"},
		{name: "stale admission", running: true, synced: true,
			status: &admission.Status{CheckedAt: time.Now().Add(-31 * time.Second), OverallReady: true}, wantError: "stale"},
		{name: "future admission", running: true, synced: true,
			status: &admission.Status{CheckedAt: time.Now().Add(time.Minute), OverallReady: true}, wantError: "stale"},
		{name: "fresh ready", running: true, synced: true,
			status: &admission.Status{CheckedAt: time.Now(), OverallReady: true}},
		{name: "unsafe cache pending", running: true, unsafe: "true", wantError: "watched resource cache"},
		{name: "unsafe not started", synced: true, unsafe: "true", wantError: "manager cache synchronization"},
		{name: "unsafe ready", running: true, synced: true, unsafe: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", tc.unsafe)
			r := &managerReadiness{
				informers:       []cache.Informer{readinessInformer{synced: tc.synced, stopped: tc.stopped}},
				admissionStatus: tc.status,
			}
			r.running.Store(tc.running)
			for range 3 {
				err := r.Check(httptest.NewRequest("GET", "/readyz", nil))
				if tc.wantError == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tc.wantError)
				}
			}
			require.False(t, r.NeedLeaderElection())
		})
	}
}

type readinessErrorReader struct {
	client.Reader
	calls    int
	deadline time.Time
}

func (r *readinessErrorReader) Get(ctx context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	r.calls++
	r.deadline, _ = ctx.Deadline()
	return fmt.Errorf("API unavailable")
}

func TestReadinessRefreshIsBoundedAndProbesDoNotReadAPI(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	reader := &readinessErrorReader{}
	r := &managerReadiness{reader: reader}
	r.running.Store(true)
	r.refresh(t.Context())
	require.Equal(t, len(admission.DefaultDependencies()), reader.calls, "each missing binding stops its dependency check")
	require.Positive(t, time.Until(reader.deadline))
	require.LessOrEqual(t, time.Until(reader.deadline), 10*time.Second)
	for range 10 {
		require.ErrorContains(t, r.Check(nil), "admission dependencies are not ready")
	}
	require.Equal(t, len(admission.DefaultDependencies()), reader.calls)
	require.Equal(t, 15*time.Second, admissionReadinessRefreshInterval)
	require.Equal(t, 30*time.Second, admissionReadinessMaxAge)
}

func TestReadinessStopsOnCancellation(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := &managerReadiness{}
	require.NoError(t, r.Start(ctx))
	require.False(t, r.running.Load())
	require.Error(t, r.Check(nil))
}

func TestReadinessRejectsPolicyAndBindingDriftAndRecovers(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "")
	for _, key := range []string{"POD_NAMESPACE", "OPERATOR_NAMESPACE", "OPERATOR_NAME_PREFIX", "OPERATOR_SERVICE_ACCOUNT_NAME", "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE"} {
		t.Setenv(key, "")
	}
	scheme := runtime.NewScheme()
	require.NoError(t, admissionv1.AddToScheme(scheme))
	var objects []client.Object
	for _, dependency := range admission.DefaultDependencies() {
		for _, fixture := range []struct {
			name   string
			object client.Object
		}{
			{dependency.PolicyName, &admissionv1.ValidatingAdmissionPolicy{}},
			{dependency.BindingName, &admissionv1.ValidatingAdmissionPolicyBinding{}},
		} {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "policy", fixture.name+".yaml"))
			require.NoError(t, err)
			require.NoError(t, yaml.UnmarshalStrict(data, fixture.object))
			objects = append(objects, fixture.object)
		}
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	readiness := &managerReadiness{reader: kube}
	readiness.running.Store(true)
	readiness.refresh(t.Context())
	require.NoError(t, readiness.Check(nil))
	policy := objects[0].(*admissionv1.ValidatingAdmissionPolicy)
	original := policy.Spec.DeepCopy()
	policy.Spec.Validations[0].Expression = "true"
	require.NoError(t, kube.Update(t.Context(), policy))
	readiness.refresh(t.Context())
	require.ErrorContains(t, readiness.Check(nil), "admission dependencies are not ready")
	policy.Spec = *original
	require.NoError(t, kube.Update(t.Context(), policy))
	readiness.refresh(t.Context())
	require.NoError(t, readiness.Check(nil))
	binding := objects[1].(*admissionv1.ValidatingAdmissionPolicyBinding)
	originalBinding := binding.Spec.DeepCopy()
	binding.Spec.MatchResources = &admissionv1.MatchResources{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"guard": "selected"}}}
	require.NoError(t, kube.Update(t.Context(), binding))
	readiness.refresh(t.Context())
	require.ErrorContains(t, readiness.Check(nil), "admission dependencies are not ready")
	binding.Spec = *originalBinding
	require.NoError(t, kube.Update(t.Context(), binding))
	readiness.refresh(t.Context())
	require.NoError(t, readiness.Check(nil))
}
