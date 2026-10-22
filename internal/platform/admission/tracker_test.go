package admission

import (
	"context"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTracker_SetAndCurrent(t *testing.T) {
	t.Parallel()

	tracker := NewTracker(nil, nil, nil, time.Minute)
	input := Status{
		CheckedAt:    time.Now(),
		OverallReady: false,
		Dependencies: []DependencyStatus{
			{
				Dependency: Dependency{Name: "policy-a"},
				Ready:      false,
				Issues:     []string{"missing binding"},
			},
		},
	}

	tracker.Set(input)
	current := tracker.Current()
	if current == nil {
		t.Fatal("Current() returned nil")
	}
	if current.OverallReady {
		t.Fatal("Current() unexpectedly marked dependencies ready")
	}
	if len(current.Dependencies) != 1 || len(current.Dependencies[0].Issues) != 1 {
		t.Fatalf("Current() lost dependency details: %#v", current)
	}

	current.Dependencies[0].Issues[0] = "mutated"
	latest := tracker.Current()
	if latest.Dependencies[0].Issues[0] != "missing binding" {
		t.Fatalf("tracker returned a mutable shared status: %#v", latest)
	}
}

func TestTracker_EnsureFreshCachesRecentStatus(t *testing.T) {
	t.Parallel()

	tracker := NewTracker(nil, nil, nil, time.Hour)
	tracker.Set(Status{
		CheckedAt:    time.Now(),
		OverallReady: true,
	})

	current, err := tracker.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh() returned unexpected error: %v", err)
	}
	if current == nil || !current.OverallReady {
		t.Fatalf("EnsureFresh() returned unexpected status: %#v", current)
	}
}

func TestTracker_MarkReadyForUnsafeMode(t *testing.T) {
	tracker := NewTracker(nil, nil, nil, time.Minute)

	tracker.MarkReadyForUnsafeMode()

	current := tracker.Current()
	if current == nil {
		t.Fatal("Current() returned nil")
	}
	if !current.OverallReady || !current.UnsafeMode {
		t.Fatalf("Current() = %#v, want overall ready unsafe status", current)
	}
}

func TestTracker_EnsureFreshKeepsUnsafeMode(t *testing.T) {
	t.Setenv("OPENBAO_UNSAFE_ADMISSION_DISABLED", "true")

	tracker := NewTracker(
		fake.NewClientBuilder().Build(),
		[]Dependency{{
			Name:        "missing-policy",
			PolicyName:  "missing-policy",
			BindingName: "missing-binding",
		}},
		[]string{""},
		time.Nanosecond,
	)
	tracker.Set(Status{
		CheckedAt:    time.Now().Add(-time.Hour),
		OverallReady: false,
	})

	current, err := tracker.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh() returned unexpected error: %v", err)
	}
	if current == nil || !current.OverallReady || !current.UnsafeMode {
		t.Fatalf("EnsureFresh() = %#v, want overall ready unsafe status", current)
	}
}

func TestTracker_RefreshBypassesRecentCache(t *testing.T) {
	t.Parallel()

	tracker := NewTracker(
		fake.NewClientBuilder().Build(),
		[]Dependency{{
			Name:        "missing-policy",
			PolicyName:  "missing-policy",
			BindingName: "missing-binding",
		}},
		[]string{""},
		time.Hour,
	)
	tracker.Set(Status{
		CheckedAt:    time.Now(),
		OverallReady: true,
	})

	current, err := tracker.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh() returned unexpected error: %v", err)
	}
	if current == nil || current.OverallReady {
		t.Fatalf("Refresh() returned unexpected status: %#v", current)
	}
	if len(current.Dependencies) != 1 || current.Dependencies[0].Ready {
		t.Fatalf("Refresh() lost dependency details: %#v", current)
	}
}

func TestTracker_RefreshDetectsPolicyDriftAndRepair(t *testing.T) {
	dep := DefaultDependencies()[0]
	policy, binding := releaseFixture(t, dep, "")
	original := policy.Spec.DeepCopy()
	policy.Spec.Validations[0].Expression = allowAllValidation
	reader := fake.NewClientBuilder().WithScheme(newAdmissionScheme(t)).WithObjects(policy, binding).Build()
	tracker := NewTracker(reader, []Dependency{dep}, []string{""}, time.Hour)
	stale, err := tracker.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stale.OverallReady || AdmissionDependenciesReady() {
		t.Fatal("drift did not clear readiness")
	}
	if !strings.Contains(strings.Join(stale.Dependencies[0].Issues, " | "), "specification differs") {
		t.Fatal(stale.Dependencies[0].Issues)
	}
	if err := reader.Get(context.Background(), client.ObjectKey{Name: policy.Name}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec = *original
	if err := reader.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	current, err := tracker.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !current.OverallReady || !AdmissionDependenciesReady() {
		t.Fatal("repair did not restore readiness")
	}
}
