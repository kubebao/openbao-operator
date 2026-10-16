package certs

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

// applySecret creates a missing Secret or applies to its observed identity.
func (m *Manager) applySecret(ctx context.Context, secret *corev1.Secret) error {
	secret.TypeMeta = metav1.TypeMeta{
		APIVersion: "v1",
		Kind:       "Secret",
	}

	_, err := resourceapply.ApplyObserved(ctx, m.client, secret, "openbao-cert-manager")
	return err
}

func (m *Manager) ensureManagedSecretMetadata(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, secret *corev1.Secret) error {
	if err := resourceownership.RequireOwnerProof("use operator-managed TLS Secret", secret, cluster); err != nil {
		return err
	}
	before := secret.DeepCopy()

	if secret.Labels == nil {
		secret.Labels = make(map[string]string)
	}
	secret.Labels[constants.LabelAppManagedBy] = constants.LabelValueAppManagedByOpenBaoOperator
	secret.Labels[constants.LabelOpenBaoCluster] = cluster.Name

	if err := controllerutil.SetControllerReference(cluster, secret, m.scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on Secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	if err := resourceownership.SetOwnerUIDAnnotation(secret, cluster); err != nil {
		return err
	}

	if reflect.DeepEqual(before.Labels, secret.Labels) &&
		reflect.DeepEqual(before.Annotations, secret.Annotations) &&
		reflect.DeepEqual(before.OwnerReferences, secret.OwnerReferences) {
		return nil
	}

	if err := m.client.Patch(ctx, secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("failed to patch Secret metadata %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	return nil
}

func (m *Manager) getSecret(ctx context.Context, namespace, name, description string) (*corev1.Secret, bool, error) {
	secret := &corev1.Secret{}
	err := m.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret)
	if err == nil {
		return secret, true, nil
	}
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if apierrors.IsForbidden(err) {
		return nil, false, operatorerrors.WrapTransientKubernetesAPI(
			fmt.Errorf("failed to get %s %s/%s: %w", description, namespace, name, err),
		)
	}
	return nil, false, fmt.Errorf("failed to get %s %s/%s: %w", description, namespace, name, err)
}

func (m *Manager) applyOwnedSecret(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, secret *corev1.Secret, description string) error {
	resolvedCluster, err := resourceapply.ResolveOwnerIdentity(ctx, m.client, cluster)
	if err != nil {
		return err
	}
	if err := resourceapply.EnsureOwnedResourceManageable(ctx, m.client, resolvedCluster, secret); err != nil {
		return fmt.Errorf("failed to verify %s %s/%s owner proof: %w", description, secret.Namespace, secret.Name, err)
	}
	if err := resourceapply.PrepareOwned(secret, resolvedCluster, m.scheme); err != nil {
		return err
	}
	if err := m.applySecret(ctx, secret); err != nil {
		return fmt.Errorf("failed to apply %s %s/%s: %w", description, secret.Namespace, secret.Name, err)
	}
	return resourceapply.EnsureOwnedResourceProofStamped(ctx, m.client, m.scheme, resolvedCluster, secret)
}
