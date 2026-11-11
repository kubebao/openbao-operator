package primitives

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestRetainedUpgradeServiceAccountTransfer(t *testing.T) {
	request, original, objects := transferFixture()
	account := &corev1.ServiceAccount{ObjectMeta: *original.ObjectMeta.DeepCopy(),
		AutomountServiceAccountToken: new(bool), ImagePullSecrets: []corev1.LocalObjectReference{{Name: "private-images"}}}
	account.Name = request.Source.Name + constants.SuffixUpgradeServiceAccount
	account.OwnerReferences = nil
	account.Labels = map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster: request.Source.Name, constants.LabelOpenBaoServiceAccountRole: constants.ServiceAccountRoleUpgrade}
	request.Resource.Kind, request.Resource.Name, request.Retained = "ServiceAccount", account.Name, true
	objects[len(objects)-1] = account
	c := transferClient(t, objects, interceptor.Funcs{})
	before := account.DeepCopy()
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(account), account))
	require.Equal(t, string(request.Destination.UID), account.Annotations[constants.AnnotationOpenBaoOwnerUID])
	request.Source, request.Destination = request.Destination, request.Source
	require.NoError(t, TransferOwnership(t.Context(), c, request))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(account), account))
	require.Equal(t, before.UID, account.UID)
	require.Equal(t, before.Annotations, account.Annotations)
	require.Equal(t, before.ImagePullSecrets, account.ImagePullSecrets)
	require.Equal(t, before.AutomountServiceAccountToken, account.AutomountServiceAccountToken)
	require.Empty(t, account.OwnerReferences)

	for _, change := range []string{"name", "role", "provenance"} {
		t.Run(change, func(t *testing.T) {
			invalid := before.DeepCopy()
			badRequest := request
			switch change {
			case "name":
				invalid.Name = "unrelated"
				badRequest.Resource.Name = invalid.Name
			case "role":
				invalid.Labels[constants.LabelOpenBaoServiceAccountRole] = "other"
			default:
				delete(invalid.Annotations, constants.AnnotationOpenBaoOwnerUID)
			}
			objects[len(objects)-1] = invalid
			require.Error(t, TransferOwnership(t.Context(), transferClient(t, objects, interceptor.Funcs{}), badRequest))
		})
	}
}

func TestDiscoveryRequiresRetainedUpgradeServiceAccountNomination(t *testing.T) {
	options := discoveryTestOptions()
	account := discoveryTestManaged("v1", "ServiceAccount", options.Source.Name+constants.SuffixUpgradeServiceAccount)
	account.OwnerReferences = nil
	account.Labels = map[string]string{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster: options.Source.Name, constants.LabelOpenBaoServiceAccountRole: constants.ServiceAccountRoleUpgrade}
	reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{account}}
	preview, err := DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Len(t, preview.Blockers, 1)
	options.RetainedResources = []TransferIdentity{discoveryIdentity(&account)}
	preview, err = DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Empty(t, preview.Blockers)
	require.Len(t, preview.Transfers, 1)
	require.True(t, preview.Transfers[0].Retained)
}
