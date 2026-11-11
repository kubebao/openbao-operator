package deletionops

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestOrphanRetentionSecrets(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	clusterUID := types.UID("test-cluster-uid")
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
			UID:       clusterUID,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Version:        "2.4.4",
			Image:          "openbao/openbao:2.4.4",
			Replicas:       3,
			DeletionPolicy: openbaov1alpha1.DeletionPolicyRetain,
		},
	}

	ownerRef := metav1.OwnerReference{
		APIVersion: "kubebao.org/v1alpha1",
		Kind:       "OpenBaoCluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
		Controller: ptrBool(true),
	}

	tests := []struct {
		name                   string
		existingSecrets        []*corev1.Secret
		expectOrphanedSecrets  []string
		expectRemainingSecrets []string
	}{
		{
			name: "orphans unseal-key and root-token secrets",
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-cluster" + constants.SuffixUnsealKey,
						Namespace:       "default",
						OwnerReferences: []metav1.OwnerReference{ownerRef},
					},
					Data: map[string][]byte{"key": []byte("unseal-key-data")},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-cluster" + constants.SuffixRootToken,
						Namespace:       "default",
						OwnerReferences: []metav1.OwnerReference{ownerRef},
					},
					Data: map[string][]byte{"token": []byte("root-token-data")},
				},
			},
			expectOrphanedSecrets: []string{
				"test-cluster" + constants.SuffixUnsealKey,
				"test-cluster" + constants.SuffixRootToken,
			},
		},
		{
			name: "handles missing secrets gracefully",
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-cluster" + constants.SuffixUnsealKey,
						Namespace:       "default",
						OwnerReferences: []metav1.OwnerReference{ownerRef},
					},
					Data: map[string][]byte{"key": []byte("unseal-key-data")},
				},
			},
			expectOrphanedSecrets: []string{
				"test-cluster" + constants.SuffixUnsealKey,
			},
		},
		{
			name: "skips already orphaned secrets",
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-cluster" + constants.SuffixUnsealKey,
						Namespace:       "default",
						OwnerReferences: nil,
					},
					Data: map[string][]byte{"key": []byte("unseal-key-data")},
				},
			},
			expectOrphanedSecrets:  []string{},
			expectRemainingSecrets: []string{"test-cluster" + constants.SuffixUnsealKey},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]runtime.Object, 0, len(tt.existingSecrets)+1)
			objects = append(objects, cluster)
			for _, s := range tt.existingSecrets {
				objects = append(objects, s)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			ctx := context.Background()
			logger := log.FromContext(ctx)

			err := OrphanRetentionSecrets(ctx, logger, fakeClient, cluster, []string{
				"test-cluster" + constants.SuffixUnsealKey,
				"test-cluster" + constants.SuffixRootToken,
			})
			require.NoError(t, err)

			for _, secretName := range tt.expectOrphanedSecrets {
				secret := &corev1.Secret{}
				err := fakeClient.Get(ctx, types.NamespacedName{
					Namespace: "default",
					Name:      secretName,
				}, secret)
				require.NoError(t, err, "expected secret %s to exist", secretName)
				assert.Empty(t, secret.OwnerReferences, "expected secret %s to have no OwnerReferences", secretName)
			}

			for _, secretName := range tt.expectRemainingSecrets {
				secret := &corev1.Secret{}
				err := fakeClient.Get(ctx, types.NamespacedName{
					Namespace: "default",
					Name:      secretName,
				}, secret)
				require.NoError(t, err, "expected secret %s to exist", secretName)
			}
		})
	}
}

func TestOrphanRetentionSecrets_PreservesUnrelatedOwners(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name: "test-cluster", Namespace: "default", UID: types.UID("test-cluster-uid"),
	}}
	clusterOwner := metav1.OwnerReference{
		APIVersion: openbaov1alpha1.GroupVersion.String(), Kind: "OpenBaoCluster", Name: cluster.Name, UID: cluster.UID,
	}
	unrelatedOwner := metav1.OwnerReference{
		APIVersion: "example.org/v1", Kind: "RetentionPolicy", Name: "policy-a", UID: types.UID("policy-uid"),
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "retained-secret", Namespace: cluster.Namespace, OwnerReferences: []metav1.OwnerReference{unrelatedOwner, clusterOwner},
	}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, secret).Build()

	require.NoError(t, OrphanRetentionSecrets(context.Background(), logr.Discard(), kubeClient, cluster, []string{secret.Name}))
	updated := &corev1.Secret{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(secret), updated))
	require.Equal(t, []metav1.OwnerReference{unrelatedOwner}, updated.OwnerReferences)
}

func TestRemoveClusterOwnerReference_RetriesConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, openbaov1alpha1.AddToScheme(scheme))

	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name: "test-cluster", Namespace: "default", UID: types.UID("test-cluster-uid"),
	}}
	unrelatedOwner := metav1.OwnerReference{
		APIVersion: "example.org/v1", Kind: "RetentionPolicy", Name: "policy-a", UID: types.UID("policy-uid"),
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "retained-secret",
		Namespace: cluster.Namespace,
		OwnerReferences: []metav1.OwnerReference{
			unrelatedOwner,
			{APIVersion: openbaov1alpha1.GroupVersion.String(), Kind: "OpenBaoCluster", Name: cluster.Name, UID: cluster.UID},
		},
	}}
	updateAttempts := 0
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					updateAttempts++
					if updateAttempts == 1 {
						return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, obj.GetName(), errors.New("concurrent metadata update"))
					}
				}
				return c.Update(ctx, obj, opts...)
			},
		}).
		Build()

	removed, found, err := RemoveClusterOwnerReference(context.Background(), logr.Discard(), kubeClient, cluster, secret.Name)
	require.NoError(t, err)
	assert.True(t, found)
	assert.True(t, removed)
	assert.Equal(t, 2, updateAttempts)

	updated := &corev1.Secret{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(secret), updated))
	require.Equal(t, []metav1.OwnerReference{unrelatedOwner}, updated.OwnerReferences)
}

func TestHasClusterOwnerReference(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{
		Name: "test-cluster", UID: types.UID("test-uid"),
	}}

	tests := []struct {
		name     string
		refs     []metav1.OwnerReference
		expected bool
	}{
		{
			name: "returns true when exact cluster identity matches",
			refs: []metav1.OwnerReference{{
				APIVersion: openbaov1alpha1.GroupVersion.String(), Kind: "OpenBaoCluster", Name: cluster.Name, UID: cluster.UID,
			}},
			expected: true,
		},
		{
			name: "returns false when only UID matches",
			refs: []metav1.OwnerReference{{
				APIVersion: "example.org/v1", Kind: "Other", Name: cluster.Name, UID: cluster.UID,
			}},
			expected: false,
		},
		{
			name:     "returns false when empty",
			refs:     nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: tt.refs,
				},
			}
			assert.Equal(t, tt.expected, HasClusterOwnerReference(secret, cluster))
		})
	}
}

func ptrBool(b bool) *bool {
	return &b
}
