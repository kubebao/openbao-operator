package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

type clientStateListReader struct {
	client.Reader
	list func(context.Context, client.ObjectList, ...client.ListOption) error
}

func (r clientStateListReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.list(ctx, list, opts...)
}

func TestOpenBaoClientStateCleanup(t *testing.T) {
	t.Parallel()
	listErr := errors.New("API unavailable")
	for _, tt := range []struct {
		name       string
		namespace  string
		registered []string
		clusters   []client.Object
		listErr    error
		duringList string
		want       []string
		wantLists  int
	}{
		{
			name:       "retain live clusters and release deleted clusters",
			registered: []string{"live-uid", "deleted-uid"},
			clusters:   []client.Object{clientStateCluster("tenant-a", "live", "live-uid")},
			want:       []string{"live-uid"}, wantLists: 1,
		},
		{
			name:       "recreated name has a separate lifetime",
			registered: []string{"old-uid", "new-uid"},
			clusters:   []client.Object{clientStateCluster("tenant-a", "same-name", "new-uid")},
			want:       []string{"new-uid"}, wantLists: 1,
		},
		{
			name:       "retain state during finalization",
			registered: []string{"deleting-uid"},
			clusters: []client.Object{func() client.Object {
				cluster := clientStateCluster("tenant-a", "deleting", "deleting-uid")
				now := metav1.Now()
				cluster.DeletionTimestamp = &now
				cluster.Finalizers = []string{openbaov1alpha1.OpenBaoClusterFinalizer}
				return cluster
			}()},
			want: []string{"deleting-uid"}, wantLists: 1,
		},
		{
			name:       "API failure retains all state",
			registered: []string{"live-uid", "deleted-uid"}, listErr: listErr,
			want: []string{"live-uid", "deleted-uid"}, wantLists: 1,
		},
		{
			name:       "new state during list survives the older response",
			registered: []string{"deleted-uid"}, duringList: "new-uid",
			want: []string{"new-uid"}, wantLists: 1,
		},
		{
			name: "single tenant list stays in the watched namespace", namespace: "tenant-a",
			registered: []string{"live-uid", "deleted-uid"},
			clusters: []client.Object{
				clientStateCluster("tenant-a", "live", "live-uid"),
				clientStateCluster("tenant-b", "outside-scope", "deleted-uid"),
			},
			want: []string{"live-uid"}, wantLists: 1,
		},
		{name: "empty manager skips the API read"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, openbaov1alpha1.AddToScheme(scheme))
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.clusters...).Build()
			clients := openbao.NewClientManager(portopenbao.ClientConfig{})
			t.Cleanup(clients.Close)
			for _, uid := range tt.registered {
				require.NotNil(t, clients.FactoryFor(uid, nil))
			}
			lists := 0
			cleanup := &openBaoClientStateCleanup{
				clients: clients, namespace: tt.namespace,
				reader: clientStateListReader{Reader: reader, list: func(
					ctx context.Context, list client.ObjectList, opts ...client.ListOption,
				) error {
					lists++
					options := &client.ListOptions{}
					options.ApplyOptions(opts)
					require.Equal(t, tt.namespace, options.Namespace)
					_, hasDeadline := ctx.Deadline()
					require.True(t, hasDeadline)
					if tt.duringList != "" {
						require.NotNil(t, clients.FactoryFor(tt.duringList, nil))
					}
					if tt.listErr != nil {
						return tt.listErr
					}
					return reader.List(ctx, list, opts...)
				}},
			}
			require.False(t, cleanup.NeedLeaderElection())
			err := cleanup.cleanup(t.Context())
			if tt.listErr != nil {
				require.ErrorIs(t, err, tt.listErr)
			} else {
				require.NoError(t, err)
			}
			require.ElementsMatch(t, tt.want, clients.ClusterKeys())
			require.Equal(t, tt.wantLists, lists)
		})
	}
}

func clientStateCluster(namespace, name, uid string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid)},
	}
}

type clientStateTokenProvider struct{}

func (clientStateTokenProvider) Token(context.Context, *openbaov1alpha1.OpenBaoCluster) (string, error) {
	return "test-jwt", nil
}

func TestAuthenticatedOperatorClientsUseClusterUID(t *testing.T) {
	t.Parallel()
	for _, purpose := range []string{"policy", "restore recovery"} {
		t.Run(purpose, func(t *testing.T) {
			clients := openbao.NewClientManager(portopenbao.ClientConfig{})
			t.Cleanup(clients.Close)
			clientset := kubernetesfake.NewClientset()
			cluster := clientStateCluster("tenant-a", "same-name", "old-uid")
			for _, uid := range []types.UID{"old-uid", "new-uid"} {
				cluster.UID = uid
				if purpose == "policy" {
					_, err := operatorPolicyClientFactory(clientset, clients, clientStateTokenProvider{})(t.Context(), cluster)
					require.NoError(t, err)
				} else {
					factory := restoreRecoveryClientFactory(clientset, clients, clientStateTokenProvider{})
					_, err := factory(t.Context(), cluster, "same-name-0")
					require.NoError(t, err)
				}
			}
			require.ElementsMatch(t, []string{"old-uid", "new-uid"}, clients.ClusterKeys())
		})
	}
}
