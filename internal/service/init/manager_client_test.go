package init

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestNewOpenBaoClientUsesClusterUID(t *testing.T) {
	t.Parallel()
	clients := openbao.NewClientManager(portopenbao.ClientConfig{})
	t.Cleanup(clients.Close)
	manager := newTestManager(t, &rest.Config{}, kubernetesfake.NewClientset(), clients)
	for _, uid := range []types.UID{"old-uid", "new-uid"} {
		t.Run(string(uid), func(t *testing.T) {
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "same-name", UID: uid},
			}
			_, err := manager.newOpenBaoClient(t.Context(), cluster)
			require.NoError(t, err)
		})
	}
	require.ElementsMatch(t, []string{"old-uid", "new-uid"}, clients.ClusterKeys())
}
