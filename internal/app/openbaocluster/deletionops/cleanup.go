package deletionops

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// Cleanup handles deletion side effects that are not covered by OwnerReferences.
func Cleanup(
	ctx context.Context,
	logger logr.Logger,
	kubeClient client.Client,
	cluster *openbaov1alpha1.OpenBaoCluster,
	policy openbaov1alpha1.DeletionPolicy,
) error {
	if policy == "" {
		policy = openbaov1alpha1.DeletionPolicyRetain
	}

	logger = logger.WithValues("deletionPolicy", string(policy))
	logger.Info(
		"Processing cleanup for deleted OpenBaoCluster",
		"note",
		"Most resources are deleted by Kubernetes GC via OwnerReferences",
	)

	if policy == openbaov1alpha1.DeletionPolicyDeletePVCs || policy == openbaov1alpha1.DeletionPolicyDeleteAll {
		if err := deletePVCs(ctx, logger, kubeClient, cluster); err != nil {
			return fmt.Errorf("failed to delete PVCs for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
		logger.Info("PVCs deleted per deletion policy")
	} else {
		logger.Info("Preserving PVCs per Retain policy")
	}

	return nil
}

func deletePVCs(ctx context.Context, logger logr.Logger, kubeClient client.Client, cluster *openbaov1alpha1.OpenBaoCluster) error {
	var pvcList corev1.PersistentVolumeClaimList
	if err := kubeClient.List(
		ctx,
		&pvcList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(map[string]string{constants.LabelOpenBaoCluster: cluster.Name}),
	); err != nil {
		return err
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if portopenbao.UsesExistingACMESharedCache(cluster) && pvc.Name == portopenbao.ACMESharedCacheClaimName(cluster) {
			continue
		}
		if portopenbao.UsesExistingAuditFileStorage(cluster) && pvc.Name == portopenbao.AuditFileStorageClaimName(cluster) {
			continue
		}
		if !resourceownership.HasOwnerProof(pvc, cluster) {
			logger.Info("Skipping PVC cleanup because owner proof is missing", "pvc", pvc.Name)
			continue
		}
		if err := kubeClient.Delete(ctx, pvc, client.Preconditions{UID: &pvc.UID}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}
