package upgrade

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

// EnsureUpgradeServiceAccount creates or updates the ServiceAccount for upgrade executor Jobs using
// Server-Side Apply. This ServiceAccount is used by upgrade executor Jobs for JWT Auth to OpenBao.
func EnsureUpgradeServiceAccount(ctx context.Context, c client.Client, cluster *openbaov1alpha1.OpenBaoCluster, fieldOwner string) error {
	if cluster == nil {
		return fmt.Errorf("cluster is required")
	}
	if fieldOwner == "" {
		fieldOwner = constants.FieldOwnerOpenBaoOperator
	}
	resolvedCluster, err := resourceapply.ResolveOwnerIdentity(ctx, c, cluster)
	if err != nil {
		return err
	}
	if err := resourceownership.RequireOwnerUID(resolvedCluster); err != nil {
		return err
	}

	saName := cluster.Name + constants.SuffixUpgradeServiceAccount

	sa := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppName:                   constants.LabelValueAppNameOpenBao,
				constants.LabelAppInstance:               cluster.Name,
				constants.LabelAppManagedBy:              constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster:            cluster.Name,
				constants.LabelOpenBaoComponent:          "upgrade",
				constants.LabelOpenBaoServiceAccountRole: constants.ServiceAccountRoleUpgrade,
			},
		},
	}

	if err := resourceapply.EnsureOwnedResourceManageable(ctx, c, resolvedCluster, sa); err != nil {
		return fmt.Errorf("failed to verify upgrade ServiceAccount %s/%s owner proof: %w", cluster.Namespace, saName, err)
	}
	if err := resourceownership.SetOwnerUIDAnnotation(sa, resolvedCluster); err != nil {
		return err
	}

	response, err := resourceapply.ApplyObserved(ctx, c, sa, fieldOwner)
	if err != nil {
		return fmt.Errorf("failed to ensure upgrade ServiceAccount %s/%s: %w", cluster.Namespace, saName, err)
	}

	return resourceapply.EnsureRetainedResourceProofStamped(ctx, c, resolvedCluster, response)
}
