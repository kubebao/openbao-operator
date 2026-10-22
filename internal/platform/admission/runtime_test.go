package admission

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func ptrFailurePolicy(v admissionregistrationv1.FailurePolicyType) *admissionregistrationv1.FailurePolicyType {
	return &v
}

func newAdmissionScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := admissionregistrationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return scheme
}

func newAdmissionClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(newAdmissionScheme(t))
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	return builder.Build()
}

func newAdmissionClientWithGetError(t *testing.T, matchName string, err error, objs ...client.Object) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(newAdmissionScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == matchName {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	return builder.Build()
}

func newPolicy(name string, failurePolicy *admissionregistrationv1.FailurePolicyType) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ValidatingAdmissionPolicy",
			APIVersion: "admissionregistration.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: failurePolicy,
		},
	}
}

func newBinding(name, policyName string, actions ...admissionregistrationv1.ValidationAction) *admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	return &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ValidatingAdmissionPolicyBinding",
			APIVersion: "admissionregistration.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        policyName,
			ValidationActions: actions,
		},
	}
}

func TestDefaultNamePrefixes(t *testing.T) {
	t.Run("defaults when env unset", func(t *testing.T) {
		t.Setenv("OPERATOR_NAME_PREFIX", "")
		t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "")
		got := DefaultNamePrefixes()
		want := []string{"openbao-operator-", ""}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("DefaultNamePrefixes()=%v, want %v", got, want)
		}
	})

	t.Run("explicit prefix selects only one installation", func(t *testing.T) {
		t.Setenv("OPERATOR_NAME_PREFIX", "demo")
		t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "")
		got := DefaultNamePrefixes()
		want := []string{"demo-"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("DefaultNamePrefixes()=%v, want %v", got, want)
		}
	})

	t.Run("explicit default prefix excludes fallback", func(t *testing.T) {
		t.Setenv("OPERATOR_NAME_PREFIX", "openbao-operator-")
		t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "")
		got := DefaultNamePrefixes()
		want := []string{"openbao-operator-"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("DefaultNamePrefixes()=%v, want %v", got, want)
		}
	})

	t.Run("derives prefix from controller service account", func(t *testing.T) {
		t.Setenv("OPERATOR_NAME_PREFIX", "")
		t.Setenv("OPERATOR_SERVICE_ACCOUNT_NAME", "demo-openbao-operator-controller")
		got := DefaultNamePrefixes()
		want := []string{"demo-openbao-operator-"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("DefaultNamePrefixes()=%v, want %v", got, want)
		}
	})
}

func TestCheckDependencies_InputValidation(t *testing.T) {
	t.Parallel()
	reader := newAdmissionClient(t)
	deps := []Dependency{{Name: "dep", PolicyName: "policy", BindingName: "binding"}}
	prefixes := []string{"openbao-operator-"}

	var nilCtx context.Context
	if _, err := CheckDependencies(nilCtx, reader, deps, prefixes); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("expected context validation error, got %v", err)
	}
	if _, err := CheckDependencies(context.Background(), nil, deps, prefixes); err == nil || !strings.Contains(err.Error(), "kubernetes client reader is required") {
		t.Fatalf("expected reader validation error, got %v", err)
	}
	if _, err := CheckDependencies(context.Background(), reader, nil, prefixes); err == nil || !strings.Contains(err.Error(), "at least one dependency is required") {
		t.Fatalf("expected deps validation error, got %v", err)
	}
	if _, err := CheckDependencies(context.Background(), reader, deps, nil); err == nil || !strings.Contains(err.Error(), "at least one name prefix is required") {
		t.Fatalf("expected prefix validation error, got %v", err)
	}
}

func TestCheckDependencies_DependencyMatrix(t *testing.T) {
	t.Parallel()

	dep := Dependency{Name: "dep", PolicyName: "policy", BindingName: "binding"}
	prefixes := []string{"p-", ""}
	fail := ptrFailurePolicy(admissionregistrationv1.Fail)
	ignore := ptrFailurePolicy(admissionregistrationv1.Ignore)

	tests := []struct {
		name        string
		reader      client.Reader
		wantReady   bool
		wantOverall bool
		wantIssue   string
	}{
		{
			name:        "missing binding",
			reader:      newAdmissionClient(t),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "missing ValidatingAdmissionPolicyBinding",
		},
		{
			name:        "binding read error",
			reader:      newAdmissionClientWithGetError(t, "p-binding", errors.New("boom")),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "failed to read ValidatingAdmissionPolicyBinding",
		},
		{
			name: "binding policy name empty",
			reader: newAdmissionClient(t,
				newBinding("p-binding", "", admissionregistrationv1.Deny),
			),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "has empty spec.policyName",
		},
		{
			name: "unexpected policy name",
			reader: newAdmissionClient(t,
				newBinding("p-binding", "other-policy", admissionregistrationv1.Deny),
				newPolicy("other-policy", fail),
			),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "references unexpected policy",
		},
		{
			name: "missing policy object",
			reader: newAdmissionClient(t,
				newBinding("p-binding", "p-policy", admissionregistrationv1.Deny),
			),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "references missing policy",
		},
		{
			name: "policy read error",
			reader: newAdmissionClientWithGetError(t, "p-policy", errors.New("policy-boom"),
				newBinding("p-binding", "p-policy", admissionregistrationv1.Deny),
			),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "failed to read ValidatingAdmissionPolicy",
		},
		{
			name: "policy failure policy not fail",
			reader: newAdmissionClient(t,
				newBinding("p-binding", "p-policy", admissionregistrationv1.Deny),
				newPolicy("p-policy", ignore),
			),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "must have failurePolicy=Fail",
		},
		{
			name: "binding without deny action",
			reader: newAdmissionClient(t,
				newBinding("p-binding", "p-policy", admissionregistrationv1.Warn),
				newPolicy("p-policy", fail),
			),
			wantReady:   false,
			wantOverall: false,
			wantIssue:   "must include validationActions=Deny",
		},
		{
			name: "dependency ready",
			reader: newAdmissionClient(t,
				newBinding("p-binding", "p-policy", admissionregistrationv1.Deny),
				newPolicy("p-policy", fail),
			),
			wantReady:   true,
			wantOverall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := CheckDependencies(context.Background(), tt.reader, []Dependency{dep}, prefixes)
			if err != nil {
				t.Fatalf("CheckDependencies() error = %v", err)
			}
			if status.OverallReady != tt.wantOverall {
				t.Fatalf("OverallReady=%v, want %v", status.OverallReady, tt.wantOverall)
			}
			if len(status.Dependencies) != 1 {
				t.Fatalf("dependencies=%d, want 1", len(status.Dependencies))
			}
			if status.Dependencies[0].Ready != tt.wantReady {
				t.Fatalf("dependency ready=%v, want %v", status.Dependencies[0].Ready, tt.wantReady)
			}
			if tt.wantIssue != "" {
				joined := strings.Join(status.Dependencies[0].Issues, " | ")
				if !strings.Contains(joined, tt.wantIssue) {
					t.Fatalf("issues=%q, expected substring %q", joined, tt.wantIssue)
				}
			}
		})
	}
}

func TestCheckDependencies_SummaryMessage(t *testing.T) {
	t.Parallel()

	ready := Status{OverallReady: true}
	if got := ready.SummaryMessage(); got != "Required admission policies are installed and correctly bound" {
		t.Fatalf("unexpected ready summary: %q", got)
	}

	unsafe := Status{OverallReady: true, UnsafeMode: true}
	if got := unsafe.SummaryMessage(); got != "Admission policies are disabled by unsafe mode" {
		t.Fatalf("unexpected unsafe summary: %q", got)
	}

	notReady := Status{
		OverallReady: false,
		Dependencies: []DependencyStatus{
			{Dependency: Dependency{Name: "dep-a"}, Ready: false, Issues: []string{"missing binding"}},
			{Dependency: Dependency{Name: "dep-b"}, Ready: false},
		},
	}
	msg := notReady.SummaryMessage()
	if !strings.Contains(msg, "dep-a: missing binding") || !strings.Contains(msg, "dep-b: not ready") {
		t.Fatalf("unexpected summary message: %q", msg)
	}
}

func TestHelperFunctions(t *testing.T) {
	t.Parallel()

	candidates := buildNameCandidates("policy", []string{"p-", ""})
	if len(candidates) != 2 || candidates[0] != "p-policy" || candidates[1] != "policy" {
		t.Fatalf("unexpected candidates: %v", candidates)
	}
	if !containsString([]string{"a", "b"}, "b") {
		t.Fatal("containsString should find existing value")
	}
	if containsString([]string{"a", "b"}, "c") {
		t.Fatal("containsString should not find missing value")
	}

	bind := newBinding("b", "p", admissionregistrationv1.Warn, admissionregistrationv1.Deny)
	if !bindingDenies(bind) {
		t.Fatal("bindingDenies should detect deny action")
	}
	if bindingDenies(newBinding("b2", "p2", admissionregistrationv1.Warn)) {
		t.Fatal("bindingDenies should be false when deny action is absent")
	}
	if bindingDenies(nil) {
		t.Fatal("bindingDenies should be false for nil binding")
	}
}

func TestGetFirstFoundBinding(t *testing.T) {
	t.Parallel()

	b := newBinding("second", "policy", admissionregistrationv1.Deny)
	reader := newAdmissionClient(t, b)

	found, name, err := getFirstFoundBinding(context.Background(), reader, []string{"first", "second"})
	if err != nil {
		t.Fatalf("getFirstFoundBinding() error=%v", err)
	}
	if found == nil || name != "second" {
		t.Fatalf("unexpected binding result: found=%v name=%q", found != nil, name)
	}

	readerErr := newAdmissionClientWithGetError(t, "first", errors.New("read-fail"))
	if _, _, err := getFirstFoundBinding(context.Background(), readerErr, []string{"first", "second"}); err == nil {
		t.Fatal("expected read error")
	}
}

func TestWaitForDependencies(t *testing.T) {
	t.Parallel()

	dep := Dependency{Name: "dep", PolicyName: "policy", BindingName: "binding"}
	prefixes := []string{"p-", ""}
	fail := ptrFailurePolicy(admissionregistrationv1.Fail)
	readyReader := newAdmissionClient(t,
		newBinding("p-binding", "p-policy", admissionregistrationv1.Deny),
		newPolicy("p-policy", fail),
	)

	t.Run("timeout <= 0 delegates single check", func(t *testing.T) {
		status, err := WaitForDependencies(context.Background(), readyReader, []Dependency{dep}, prefixes, 0, 0)
		if err != nil {
			t.Fatalf("WaitForDependencies() error=%v", err)
		}
		if !status.OverallReady {
			t.Fatal("expected ready status")
		}
	})

	t.Run("times out and returns last status", func(t *testing.T) {
		status, err := WaitForDependencies(context.Background(), newAdmissionClient(t), []Dependency{dep}, prefixes, 20*time.Millisecond, 5*time.Millisecond)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
		if status.OverallReady {
			t.Fatal("expected last status to be not-ready")
		}
	})

	t.Run("returns context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		_, err := WaitForDependencies(ctx, newAdmissionClient(t), []Dependency{dep}, prefixes, time.Second, 100*time.Millisecond)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	})

	t.Run("becomes ready before timeout", func(t *testing.T) {
		status, err := WaitForDependencies(context.Background(), readyReader, []Dependency{dep}, prefixes, time.Second, 10*time.Millisecond)
		if err != nil {
			t.Fatalf("WaitForDependencies() error=%v", err)
		}
		if !status.OverallReady {
			t.Fatal("expected ready status")
		}
	})
}

func TestUnsafeAdmissionDisabled(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{name: "true lowercase", env: "true", want: true},
		{name: "true mixed case", env: "TrUe", want: true},
		{name: "trim whitespace", env: "  true  ", want: true},
		{name: "false", env: "false", want: false},
		{name: "empty", env: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", tt.env)
			if got := UnsafeAdmissionDisabled(); got != tt.want {
				t.Fatalf("UnsafeAdmissionDisabled()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefreshStatus_UnsafeAdmissionDisabled(t *testing.T) {
	SetAdmissionDependenciesReady(false)
	t.Cleanup(func() {
		SetAdmissionDependenciesReady(false)
	})
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")

	status, err := RefreshStatus(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("RefreshStatus() error = %v", err)
	}
	if status == nil || !status.OverallReady || !status.UnsafeMode {
		t.Fatalf("RefreshStatus() = %#v, want overall ready unsafe status", status)
	}
	if !AdmissionDependenciesReady() {
		t.Fatal("expected legacy admission readiness signal to be true")
	}
}

func TestSetAdmissionDependenciesReady(t *testing.T) {
	SetAdmissionDependenciesReady(false)
	t.Cleanup(func() {
		SetAdmissionDependenciesReady(false)
	})

	SetAdmissionDependenciesReady(true)
	if !AdmissionDependenciesReady() {
		t.Fatal("expected AdmissionDependenciesReady=true")
	}
	if got := testutil.ToFloat64(admissionDependenciesReadyGauge); got != 1 {
		t.Fatalf("gauge=%v, want 1", got)
	}

	SetAdmissionDependenciesReady(false)
	if AdmissionDependenciesReady() {
		t.Fatal("expected AdmissionDependenciesReady=false")
	}
	if got := testutil.ToFloat64(admissionDependenciesReadyGauge); got != 0 {
		t.Fatalf("gauge=%v, want 0", got)
	}
}

func TestVerifyProvisionerRBACEnforcement(t *testing.T) {
	t.Parallel()

	t.Run("input validation", func(t *testing.T) {
		var nilCtx context.Context
		if err := VerifyProvisionerRBACEnforcement(nilCtx, k8sfake.NewClientset(), "ns"); err == nil || !strings.Contains(err.Error(), "context is required") {
			t.Fatalf("expected context validation error, got %v", err)
		}
		if err := VerifyProvisionerRBACEnforcement(context.Background(), nil, "ns"); err == nil || !strings.Contains(err.Error(), "kubernetes clientset is required") {
			t.Fatalf("expected clientset validation error, got %v", err)
		}
		if err := VerifyProvisionerRBACEnforcement(context.Background(), k8sfake.NewClientset(), ""); err == nil || !strings.Contains(err.Error(), "namespace is required") {
			t.Fatalf("expected namespace validation error, got %v", err)
		}
	})

	t.Run("denied with expected message", func(t *testing.T) {
		cs := k8sfake.NewClientset()
		cs.PrependReactor("create", "roles", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"}, "openbao-operator-admission-canary", fmt.Errorf("Provisioner can only create Roles"))
		})
		if err := VerifyProvisionerRBACEnforcement(context.Background(), cs, "tenant-ns"); err != nil {
			t.Fatalf("expected success for expected deny, got %v", err)
		}
	})

	t.Run("denied with unexpected message", func(t *testing.T) {
		cs := k8sfake.NewClientset()
		cs.PrependReactor("create", "roles", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"}, "openbao-operator-admission-canary", fmt.Errorf("different reason"))
		})
		err := VerifyProvisionerRBACEnforcement(context.Background(), cs, "tenant-ns")
		if err == nil || !strings.Contains(err.Error(), "not by the expected VAP message") {
			t.Fatalf("expected unexpected-message error, got %v", err)
		}
	})

	t.Run("request allowed unexpectedly", func(t *testing.T) {
		cs := k8sfake.NewClientset()
		err := VerifyProvisionerRBACEnforcement(context.Background(), cs, "tenant-ns")
		if err == nil || !strings.Contains(err.Error(), "expected canary Role create to be denied") {
			t.Fatalf("expected accidental-allow error, got %v", err)
		}
	})

	t.Run("unexpected API error", func(t *testing.T) {
		cs := k8sfake.NewClientset()
		cs.PrependReactor("create", "roles", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
		err := VerifyProvisionerRBACEnforcement(context.Background(), cs, "tenant-ns")
		if err == nil || !strings.Contains(err.Error(), "unexpected error") {
			t.Fatalf("expected unexpected-error classification, got %v", err)
		}
	})
}

func TestRestoreDependenciesRejectStalePolicies(t *testing.T) {
	for _, dep := range DefaultDependencies() {
		if dep.Name != dependencyOpenBaoValidateOpenBaoRestore && dep.Name != dependencyProtectRestoreExecution {
			continue
		}
		t.Run(dep.Name, func(t *testing.T) {
			policy := newPolicy(dep.PolicyName, ptrFailurePolicy(admissionregistrationv1.Fail))
			policy.Annotations = map[string]string{policyFingerprintAnnotation: "sha256:old-release"}
			reader := newAdmissionClient(t, policy, newBinding(dep.BindingName, dep.PolicyName, admissionregistrationv1.Deny))
			status, err := CheckDependencies(context.Background(), reader, []Dependency{dep}, []string{""})
			if err != nil {
				t.Fatal(err)
			}
			if status.OverallReady {
				t.Fatal("stale restore policy passed readiness")
			}
		})
	}
}
