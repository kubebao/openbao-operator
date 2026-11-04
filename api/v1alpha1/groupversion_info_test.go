package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestDestinationAPIIdentity(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	require.Equal(t, schema.GroupVersion{Group: "kubebao.org", Version: "v1alpha1"}, GroupVersion)
	for _, kind := range []string{"OpenBaoCluster", "OpenBaoTenant", "OpenBaoRestore"} {
		for _, suffix := range []string{"", "List"} {
			_, err := scheme.New(GroupVersion.WithKind(kind + suffix))
			require.NoError(t, err)
			_, err = scheme.New(schema.GroupVersionKind{Group: "openbao.org", Version: "v1alpha1", Kind: kind + suffix})
			require.True(t, runtime.IsNotRegisteredError(err), "destination scheme must not reconcile the source API")
		}
	}
	// The API-group change does not rename cleanup contracts on existing objects.
	require.Equal(t, "openbao.org/openbaocluster-finalizer", OpenBaoClusterFinalizer)
	require.Equal(t, "openbao.org/openbaotenant-finalizer", OpenBaoTenantFinalizer)
	require.Equal(t, "openbao.org/openbaorestore-finalizer", OpenBaoRestoreFinalizer)
}
