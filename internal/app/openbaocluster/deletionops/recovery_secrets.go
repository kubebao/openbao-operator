package deletionops

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

// deleteRecoverySecrets replaces GC for generated recovery material protected
// by admission. Read metadata only; cleanup does not need key or token data.
func deleteRecoverySecrets(ctx context.Context, writer client.Client, reader client.Reader, cluster *openbaov1alpha1.OpenBaoCluster) error {
	for _, name := range DefaultRetentionSecrets(cluster) {
		secret := &metav1.PartialObjectMetadata{}
		secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
		if err := reader.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: name}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("read recovery Secret metadata: %w", err)
		}
		if secret.Labels[constants.LabelAppManagedBy] != constants.LabelValueAppManagedByOpenBaoOperator ||
			secret.Labels[constants.LabelOpenBaoCluster] != cluster.Name ||
			!resourceownership.HasOwnerProof(secret, cluster) {
			continue
		}
		if secret.UID == "" || secret.ResourceVersion == "" {
			return fmt.Errorf("recovery Secret %s/%s lacks API identity", secret.Namespace, secret.Name)
		}
		current, err := readDeletionAuthority(ctx, reader, cluster)
		if err != nil {
			return err
		}
		if current.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDeletePVCs &&
			current.Spec.DeletionPolicy != openbaov1alpha1.DeletionPolicyDeleteAll {
			return fmt.Errorf("recovery Secret deletion requires the original deleting cluster with a destructive deletion policy and no restore hold")
		}
		if err := writer.Delete(ctx, secret, client.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete recovery Secret %s/%s: %w", secret.Namespace, secret.Name, err)
		}
	}
	return nil
}
