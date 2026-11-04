package init

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/events"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type initRaftRuntimeStub struct {
	reconcileAutopilotCalls int
}

func (*initRaftRuntimeStub) ConfigureAutopilot(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
	string,
) error {
	return nil
}

func (s *initRaftRuntimeStub) ReconcileAutopilotConfig(
	context.Context,
	logr.Logger,
	*openbaov1alpha1.OpenBaoCluster,
) error {
	s.reconcileAutopilotCalls++
	return nil
}

func newTestManager(
	t *testing.T,
	config *rest.Config,
	clientset kubernetes.Interface,
	clientManager *openbao.ClientManager,
	recorder ...events.EventRecorder,
) *Manager {
	t.Helper()

	manager, err := NewManager(config, clientset, clientManager, &initRaftRuntimeStub{}, recorder...)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func TestNewManagerUsesInjectedRaftRuntime(t *testing.T) {
	clientset := kubernetesfake.NewClientset()
	clientManager := openbao.NewClientManager(portopenbao.ClientConfig{})
	raftRuntime := &initRaftRuntimeStub{}

	manager, err := NewManager(&rest.Config{}, clientset, clientManager, raftRuntime)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if manager.raftRuntime != raftRuntime {
		t.Fatal("NewManager() did not retain the injected Raft runtime")
	}
}

func TestNewManagerRequiresRaftRuntime(t *testing.T) {
	manager, err := NewManager(&rest.Config{}, kubernetesfake.NewClientset(), nil, nil)
	if err == nil {
		t.Fatal("NewManager() error = nil, want missing Raft runtime error")
	}
	if manager != nil {
		t.Fatal("NewManager() manager is non-nil after validation error")
	}
}

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

func TestReconcileSelfInitUsesPodReadiness(t *testing.T) {
	tests := []struct {
		name               string
		podReady           bool
		wantInitialized    bool
		wantSelfInit       bool
		wantAutopilotCalls int
	}{
		{
			name:            "pod not ready does not mark initialized",
			podReady:        false,
			wantInitialized: false,
			wantSelfInit:    false,
		},
		{
			name:               "pod ready marks initialized",
			podReady:           true,
			wantInitialized:    true,
			wantSelfInit:       true,
			wantAutopilotCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster",
					Namespace: "default",
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
					},
				},
			}

			readyStatus := corev1.ConditionFalse
			if tt.podReady {
				readyStatus = corev1.ConditionTrue
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-0",
					Namespace: "default",
					Labels: map[string]string{
						constants.LabelAppInstance:  "cluster",
						constants.LabelAppName:      constants.LabelValueAppNameOpenBao,
						constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: constants.ContainerBao,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{
									StartedAt: metav1.Now(),
								},
							},
							Started: ptrTo(true),
						},
					},
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: readyStatus,
						},
					},
				},
			}

			clientset := kubernetesfake.NewClientset(pod)
			clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
			raftRuntime := &initRaftRuntimeStub{}
			manager, err := NewManager(&rest.Config{}, clientset, clientMgr, raftRuntime)
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}

			if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err != nil {
				t.Fatalf("Reconcile() error = %v, want no error", err)
			}

			if cluster.Status.Initialized != tt.wantInitialized {
				t.Fatalf("Status.Initialized = %t, want %t", cluster.Status.Initialized, tt.wantInitialized)
			}

			if cluster.Status.SelfInitialized != tt.wantSelfInit {
				t.Fatalf("Status.SelfInitialized = %t, want %t", cluster.Status.SelfInitialized, tt.wantSelfInit)
			}

			if raftRuntime.reconcileAutopilotCalls != tt.wantAutopilotCalls {
				t.Fatalf(
					"ReconcileAutopilotConfig() calls = %d, want %d",
					raftRuntime.reconcileAutopilotCalls,
					tt.wantAutopilotCalls,
				)
			}
		})
	}
}

func TestReconcileSelfInitReady_EmitsInitEvents(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			},
		},
	}

	started := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-0",
			Namespace: "default",
			Labels: map[string]string{
				constants.LabelAppInstance:  "cluster",
				constants.LabelAppName:      constants.LabelValueAppNameOpenBao,
				constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: constants.ContainerBao,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{
						StartedAt: metav1.Now(),
					},
				},
				Started: &started,
			}},
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	recorder := events.NewFakeRecorder(10)
	clientset := kubernetesfake.NewClientset(pod)
	clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
	manager := newTestManager(t, &rest.Config{}, clientset, clientMgr, recorder)

	if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	expectEventContains(t, recorder, "Normal", ReasonInitStarted)
	expectEventContains(t, recorder, "Normal", ReasonInitCompleted)
}

func TestReconcileSelfInitReady_InertizesConfigMapBeforeStatus(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			},
		},
	}

	started := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-0",
			Namespace: "default",
			Labels: map[string]string{
				constants.LabelAppInstance:  "cluster",
				constants.LabelAppName:      constants.LabelValueAppNameOpenBao,
				constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: constants.ContainerBao,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{
						StartedAt: metav1.Now(),
					},
				},
				Started: &started,
			}},
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceidentity.ConfigInitMapName(cluster),
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			constants.OpenBaoConfigFileName: `initialize "bootstrap" {}`,
		},
	}

	clientset := kubernetesfake.NewClientset(pod, configMap)
	clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
	manager := newTestManager(t, &rest.Config{}, clientset, clientMgr)

	if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !cluster.Status.Initialized || !cluster.Status.SelfInitialized {
		t.Fatalf("status initialized=%t selfInitialized=%t, want both true", cluster.Status.Initialized, cluster.Status.SelfInitialized)
	}

	got, err := clientset.CoreV1().ConfigMaps(cluster.Namespace).Get(context.Background(), configMap.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get ConfigMap: %v", err)
	}
	if got.Data[constants.OpenBaoConfigFileName] != constants.CompletedSelfInitConfig {
		t.Fatalf("ConfigMap content = %q, want %q", got.Data[constants.OpenBaoConfigFileName], constants.CompletedSelfInitConfig)
	}
	if strings.Contains(got.Data[constants.OpenBaoConfigFileName], "initialize") {
		t.Fatalf("completed self-init ConfigMap still contains initialize stanza:\n%s", got.Data[constants.OpenBaoConfigFileName])
	}
}

func TestReconcileSelfInitReady_DoesNotMarkCompleteWhenConfigMapUpdateFails(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			},
		},
	}

	started := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-0",
			Namespace: "default",
			Labels: map[string]string{
				constants.LabelAppInstance:  "cluster",
				constants.LabelAppName:      constants.LabelValueAppNameOpenBao,
				constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: constants.ContainerBao,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{
						StartedAt: metav1.Now(),
					},
				},
				Started: &started,
			}},
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceidentity.ConfigInitMapName(cluster),
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			constants.OpenBaoConfigFileName: `initialize "bootstrap" {}`,
		},
	}

	clientset := kubernetesfake.NewClientset(pod, configMap)
	clientset.PrependReactor("update", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTooManyRequests("too many requests", 0)
	})
	clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
	manager := newTestManager(t, &rest.Config{}, clientset, clientMgr)

	_, err := manager.Reconcile(context.Background(), logr.Discard(), cluster)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want ConfigMap update error")
	}
	if !operatorerrors.IsTransient(err) {
		t.Fatalf("Reconcile() error = %v, want transient", err)
	}
	if cluster.Status.Initialized || cluster.Status.SelfInitialized {
		t.Fatalf("status initialized=%t selfInitialized=%t, want both false", cluster.Status.Initialized, cluster.Status.SelfInitialized)
	}
}

func TestReconcileOperatorInitFailure_EmitsInitFailedEvent(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "default",
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			TLS: openbaov1alpha1.TLSConfig{Enabled: true},
		},
	}

	started := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-0",
			Namespace: "default",
			Labels: map[string]string{
				constants.LabelAppInstance:  "cluster",
				constants.LabelAppName:      constants.LabelValueAppNameOpenBao,
				constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: constants.ContainerBao,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{
						StartedAt: metav1.Now(),
					},
				},
				Started: &started,
			}},
		},
	}
	tlsServerSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + constants.SuffixTLSServer,
			Namespace: cluster.Namespace,
		},
	}

	recorder := events.NewFakeRecorder(10)
	clientset := kubernetesfake.NewClientset(pod, tlsServerSecret)
	clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
	manager := newTestManager(t, &rest.Config{}, clientset, clientMgr, recorder)

	if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err == nil {
		t.Fatal("expected reconcile to fail when TLS CA Secret is missing")
	}

	expectEventContains(t, recorder, "Normal", ReasonInitStarted)
	expectEventContains(t, recorder, "Warning", ReasonInitFailed)
}

func ptrTo(v bool) *bool {
	return &v
}

func TestReconcileIgnoresServiceLabelsWhenSelfInitDisabled(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "default",
		},
	}

	started := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-0",
			Namespace: "default",
			Labels: map[string]string{
				constants.LabelAppInstance:  "cluster",
				constants.LabelAppName:      constants.LabelValueAppNameOpenBao,
				constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
				"openbao-initialized":       "true",
				"openbao-sealed":            "false",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: constants.ContainerBao,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{
							StartedAt: metav1.Now(),
						},
					},
					Started: &started,
				},
			},
		},
	}

	clientset := kubernetesfake.NewClientset(pod)
	clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
	manager := newTestManager(t, &rest.Config{}, clientset, clientMgr)

	if _, err := manager.Reconcile(context.Background(), logr.Discard(), cluster); err != nil {
		t.Fatalf("Reconcile() error = %v, want no error", err)
	}

	if cluster.Status.Initialized {
		t.Fatalf("Status.Initialized = %t, want false because operator must still capture root token", cluster.Status.Initialized)
	}

	if cluster.Status.SelfInitialized {
		t.Fatalf("Status.SelfInitialized = %t, want false when self-init disabled", cluster.Status.SelfInitialized)
	}
}

func TestStoreRootTokenCreatesOrUpdatesSecret(t *testing.T) {
	tests := []struct {
		name                       string
		existingSecret             *corev1.Secret
		rootToken                  string
		wantTokenInSecret          string
		wantErrContains            string
		transientCreateFailures    int
		wantCreateFailuresObserved int
	}{
		{
			name:              "creates new Secret when none exists",
			rootToken:         "s.roottoken",
			wantTokenInSecret: "s.roottoken",
		},
		{
			name:                       "retries transient Secret create errors",
			rootToken:                  "s.roottoken",
			wantTokenInSecret:          "s.roottoken",
			transientCreateFailures:    2,
			wantCreateFailuresObserved: 2,
		},
		{
			name: "rejects unowned existing Secret",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-root-token",
					Namespace: "default",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					rootTokenSecretKey: []byte("old-token"),
				},
			},
			rootToken:       "s.newtoken",
			wantErrContains: "requires OpenBaoCluster owner proof",
		},
		{
			name: "updates owned mutable existing Secret token",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "cluster-root-token",
					Namespace:       "default",
					OwnerReferences: []metav1.OwnerReference{rootTokenOwnerRef("cluster", types.UID("test-uid-12345"))},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					rootTokenSecretKey: []byte("old-token"),
				},
			},
			rootToken:         "s.newtoken",
			wantTokenInSecret: "s.newtoken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := kubernetesfake.NewClientset()
			clientMgr := openbao.NewClientManager(portopenbao.ClientConfig{})
			manager := newTestManager(t, &rest.Config{}, clientset, clientMgr)

			createFailuresObserved := 0
			if tt.transientCreateFailures > 0 {
				clientset.PrependReactor("create", "secrets", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
					if createFailuresObserved >= tt.transientCreateFailures {
						return false, nil, nil
					}
					createFailuresObserved++
					return true, nil, apierrors.NewTooManyRequests("too many requests", 0)
				})
			}

			cluster := &openbaov1alpha1.OpenBaoCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kubebao.org/v1alpha1",
					Kind:       "OpenBaoCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster",
					Namespace: "default",
					UID:       types.UID("test-uid-12345"),
				},
			}

			if tt.existingSecret != nil {
				if _, err := clientset.CoreV1().Secrets(tt.existingSecret.Namespace).Create(context.Background(), tt.existingSecret, metav1.CreateOptions{}); err != nil {
					t.Fatalf("failed to seed existing Secret: %v", err)
				}
			}

			err := manager.storeRootToken(context.Background(), logr.Discard(), cluster, tt.rootToken)
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("storeRootToken() error = nil, want %q", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("storeRootToken() error = %q, want %q", err.Error(), tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("storeRootToken() error = %v", err)
			}

			if createFailuresObserved != tt.wantCreateFailuresObserved {
				t.Fatalf("createFailuresObserved = %d, want %d", createFailuresObserved, tt.wantCreateFailuresObserved)
			}

			secret, err := clientset.CoreV1().Secrets("default").Get(context.Background(), "cluster-root-token", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("expected root token Secret to exist: %v", err)
			}

			got := string(secret.Data[rootTokenSecretKey])
			if got != tt.wantTokenInSecret {
				t.Fatalf("root token in Secret = %q, want %q", got, tt.wantTokenInSecret)
			}

			if secret.Immutable == nil || *secret.Immutable != true {
				t.Fatalf("expected root token Secret to be immutable")
			}

			// Verify OwnerReference is set
			if len(secret.OwnerReferences) == 0 {
				t.Fatalf("expected OwnerReference to be set on root token Secret")
			}
			ownerRef := secret.OwnerReferences[0]
			if ownerRef.UID != cluster.UID {
				t.Errorf("expected OwnerReference UID %s, got %s", cluster.UID, ownerRef.UID)
			}
			if ownerRef.Kind != "OpenBaoCluster" {
				t.Errorf("expected OwnerReference Kind 'OpenBaoCluster', got %s", ownerRef.Kind)
			}
		})
	}
}

func TestStoreRootTokenRejectsImmutableOwnedSecretWithDifferentToken(t *testing.T) {
	immutable := true
	clusterUID := types.UID("test-uid-12345")
	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "cluster-root-token",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{rootTokenOwnerRef("cluster", clusterUID)},
		},
		Immutable: &immutable,
		Type:      corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			rootTokenSecretKey: []byte("old-token"),
		},
	}
	clientset := kubernetesfake.NewClientset(existingSecret)
	manager := newTestManager(t, &rest.Config{}, clientset, openbao.NewClientManager(portopenbao.ClientConfig{}))
	cluster := &openbaov1alpha1.OpenBaoCluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kubebao.org/v1alpha1",
			Kind:       "OpenBaoCluster",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default", UID: clusterUID},
	}

	err := manager.storeRootToken(context.Background(), logr.Discard(), cluster, "s.newtoken")
	if err == nil {
		t.Fatal("storeRootToken() error = nil, want immutable mismatch error")
	}
	if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("storeRootToken() error = %q, want immutable mismatch", err.Error())
	}
}

func rootTokenOwnerRef(name string, uid types.UID) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: openbaov1alpha1.GroupVersion.String(),
		Kind:       "OpenBaoCluster",
		Name:       name,
		UID:        uid,
		Controller: &controller,
	}
}

func TestEnsureRootTokenSecretPresent(t *testing.T) {
	newCluster := func() *openbaov1alpha1.OpenBaoCluster {
		return &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cluster",
				Namespace: "default",
			},
		}
	}

	t.Run("returns nil when root token Secret exists", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cluster-root-token",
				Namespace: "default",
			},
		}
		clientset := kubernetesfake.NewClientset(secret)
		manager := newTestManager(t, &rest.Config{}, clientset, openbao.NewClientManager(portopenbao.ClientConfig{}))

		if err := manager.ensureRootTokenSecretPresent(context.Background(), newCluster()); err != nil {
			t.Fatalf("ensureRootTokenSecretPresent() error = %v, want nil", err)
		}
	})

	t.Run("returns transient error when root token Secret is missing", func(t *testing.T) {
		clientset := kubernetesfake.NewClientset()
		manager := newTestManager(t, &rest.Config{}, clientset, openbao.NewClientManager(portopenbao.ClientConfig{}))

		err := manager.ensureRootTokenSecretPresent(context.Background(), newCluster())
		if err == nil {
			t.Fatalf("ensureRootTokenSecretPresent() error = nil, want non-nil")
		}
		if !operatorerrors.IsTransient(err) {
			t.Fatalf("ensureRootTokenSecretPresent() error = %v, want transient", err)
		}
	})

	t.Run("returns transient error on forbidden Secret get", func(t *testing.T) {
		clientset := kubernetesfake.NewClientset()
		clientset.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "cluster-root-token", fmt.Errorf("forbidden"))
		})
		manager := newTestManager(t, &rest.Config{}, clientset, openbao.NewClientManager(portopenbao.ClientConfig{}))

		err := manager.ensureRootTokenSecretPresent(context.Background(), newCluster())
		if err == nil {
			t.Fatalf("ensureRootTokenSecretPresent() error = nil, want non-nil")
		}
		if !operatorerrors.IsTransient(err) {
			t.Fatalf("ensureRootTokenSecretPresent() error = %v, want transient", err)
		}
	})

	t.Run("returns non-transient error on unexpected Secret get error", func(t *testing.T) {
		clientset := kubernetesfake.NewClientset()
		clientset.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("boom")
		})
		manager := newTestManager(t, &rest.Config{}, clientset, openbao.NewClientManager(portopenbao.ClientConfig{}))

		err := manager.ensureRootTokenSecretPresent(context.Background(), newCluster())
		if err == nil {
			t.Fatalf("ensureRootTokenSecretPresent() error = nil, want non-nil")
		}
		if operatorerrors.IsTransient(err) {
			t.Fatalf("ensureRootTokenSecretPresent() error = %v, want non-transient", err)
		}
	})
}
