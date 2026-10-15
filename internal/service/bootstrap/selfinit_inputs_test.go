package bootstrap

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestSelfInitCompletionSurvivesStatusInterruption(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned completion witness", false: "foreign completion witness"}[owned], func(t *testing.T) {
			cluster := newMinimalCluster("bao", "default")
			cluster.Spec.Version = "2.6.0"
			cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{{
				Name: "login", Path: "auth/userpass/users/admin",
				DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{
					"password": {SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "deleted-bootstrap", Key: "password"}},
				},
			}}}
			// The completion ConfigMap was committed, but the controller stopped
			// before its Initialized/SelfInitialized status patch reached the API.
			completed := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: resourceidentity.ConfigInitMapName(cluster), Namespace: cluster.Namespace},
				Data:       map[string]string{configFileName: completedSelfInitConfig},
			}
			if owned {
				require.NoError(t, controllerutil.SetControllerReference(cluster, completed, testScheme))
				require.NoError(t, resourceownership.SetOwnerUIDAnnotation(completed, cluster))
			}
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(completed).Build()
			manager := NewManager(c, testScheme, "operator")
			err := manager.ensureSelfInitConfigMap(context.Background(), logr.Discard(), cluster)
			if !owned {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := &corev1.ConfigMap{}
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(completed), got))
			require.Equal(t, completedSelfInitConfig, got.Data[configFileName])
			require.False(t, cluster.Status.Initialized, "preservation must not invent initialized status")
		})
	}
}
