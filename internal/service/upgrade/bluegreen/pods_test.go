package bluegreen

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestIsPodReady_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name:     "returns false when no conditions are present",
			pod:      &corev1.Pod{},
			expected: false,
		},
		{
			name: "returns false when ready condition is false",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name: "returns true when ready condition is true",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "returns false when only non-ready condition is true",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: false,
		},
		{
			name: "returns false when ready condition is unknown",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionUnknown},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isPodReady(tt.pod); got != tt.expected {
				t.Fatalf("isPodReady() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetPodsByRevision_TableDriven(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbao scheme: %v", err)
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-a"},
	}

	podBlue := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-a-blue-0",
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppInstance:     cluster.Name,
				constants.LabelAppName:         constants.LabelValueAppNameOpenBao,
				constants.LabelOpenBaoRevision: "blue",
			},
		},
	}
	podGreen := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-a-green-0",
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppInstance:     cluster.Name,
				constants.LabelAppName:         constants.LabelValueAppNameOpenBao,
				constants.LabelOpenBaoRevision: "green",
			},
		},
	}
	podOtherCluster := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-b-blue-0",
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppInstance:     "cluster-b",
				constants.LabelAppName:         constants.LabelValueAppNameOpenBao,
				constants.LabelOpenBaoRevision: "blue",
			},
		},
	}

	mgr := &Manager{
		client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(podBlue, podGreen, podOtherCluster).
			Build(),
	}

	tests := []struct {
		name         string
		revision     string
		expectedPods []string
	}{
		{
			name:         "returns pods for blue revision only",
			revision:     "blue",
			expectedPods: []string{"cluster-a-blue-0"},
		},
		{
			name:         "returns pods for green revision only",
			revision:     "green",
			expectedPods: []string{"cluster-a-green-0"},
		},
		{
			name:         "returns empty when revision has no pods",
			revision:     "purple",
			expectedPods: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pods, err := mgr.getPodsByRevision(context.Background(), cluster, tt.revision)
			if err != nil {
				t.Fatalf("getPodsByRevision() error = %v", err)
			}
			if len(pods) != len(tt.expectedPods) {
				t.Fatalf("len(pods) = %d, want %d", len(pods), len(tt.expectedPods))
			}
			for i, podName := range tt.expectedPods {
				if pods[i].Name != podName {
					t.Fatalf("pods[%d].Name = %q, want %q", i, pods[i].Name, podName)
				}
			}
		})
	}
}

func TestCleanupGreenStatefulSet_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		cluster              *openbaov1alpha1.OpenBaoCluster
		withGreenStatefulSet bool
		wantErr              bool
		wantDeleted          bool
	}{
		{
			name: "no-op when bluegreen status is nil",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-a"},
			},
			withGreenStatefulSet: false,
			wantErr:              false,
			wantDeleted:          true,
		},
		{
			name: "no-op when green revision is empty",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-a"},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					BlueGreen: &openbaov1alpha1.BlueGreenStatus{
						GreenRevision: "",
					},
				},
			},
			withGreenStatefulSet: false,
			wantErr:              false,
			wantDeleted:          true,
		},
		{
			name: "succeeds when green statefulset does not exist",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-a"},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					BlueGreen: &openbaov1alpha1.BlueGreenStatus{
						GreenRevision: "green",
					},
				},
			},
			withGreenStatefulSet: false,
			wantErr:              false,
			wantDeleted:          true,
		},
		{
			name: "deletes existing green statefulset",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-a"},
				Status: openbaov1alpha1.OpenBaoClusterStatus{
					BlueGreen: &openbaov1alpha1.BlueGreenStatus{
						GreenRevision: "green",
					},
				},
			},
			withGreenStatefulSet: true,
			wantErr:              false,
			wantDeleted:          true,
		},
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

			tt.cluster.UID = "cluster-a-uid"
			builder := fake.NewClientBuilder().WithScheme(scheme)
			var stsName string
			if tt.cluster.Status.BlueGreen != nil && tt.cluster.Status.BlueGreen.GreenRevision != "" {
				stsName = tt.cluster.Name + "-" + tt.cluster.Status.BlueGreen.GreenRevision
				if tt.withGreenStatefulSet {
					builder = builder.WithObjects(newManagedCleanupStatefulSet(tt.cluster, tt.cluster.Status.BlueGreen.GreenRevision))
				}
			}

			mgr := &Manager{client: builder.Build()}
			err := mgr.cleanupGreenStatefulSet(context.Background(), logr.Discard(), tt.cluster)
			if (err != nil) != tt.wantErr {
				t.Fatalf("cleanupGreenStatefulSet() error = %v, wantErr %v", err, tt.wantErr)
			}

			if stsName == "" {
				return
			}

			got := &appsv1.StatefulSet{}
			getErr := mgr.client.Get(context.Background(), types.NamespacedName{
				Namespace: tt.cluster.Namespace,
				Name:      stsName,
			}, got)
			if tt.wantDeleted {
				if !apierrors.IsNotFound(getErr) {
					t.Fatalf("expected StatefulSet %s to be absent, getErr=%v", stsName, getErr)
				}
				return
			}

			if getErr != nil {
				t.Fatalf("expected StatefulSet %s to exist, getErr=%v", stsName, getErr)
			}
		})
	}
}

func TestCleanupGreenStatefulSet_DoesNotDeletePlaceholderWhenRevisionEmpty(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := openbaov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add openbao scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "tenant-a"},
		Status: openbaov1alpha1.OpenBaoClusterStatus{
			BlueGreen: &openbaov1alpha1.BlueGreenStatus{
				GreenRevision: "",
			},
		},
	}

	placeholder := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name + "-",
		},
	}

	mgr := &Manager{
		client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(placeholder).
			Build(),
	}

	if err := mgr.cleanupGreenStatefulSet(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("cleanupGreenStatefulSet() unexpected error: %v", err)
	}

	got := &appsv1.StatefulSet{}
	if err := mgr.client.Get(context.Background(), types.NamespacedName{
		Namespace: placeholder.Namespace,
		Name:      placeholder.Name,
	}, got); err != nil {
		t.Fatalf("expected placeholder StatefulSet to remain, got error: %v", err)
	}
}
