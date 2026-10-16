package restore

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func (m *Manager) reconcileTargetRecovery(ctx context.Context, logger logr.Logger, request *api.OpenBaoRestore) (ctrl.Result, error) {
	action := decideTargetRecovery(request, targetApplicationNotObserved, time.Now())
	if action == targetRecoveryObserve {
		application, err := m.observeTargetApplication(ctx, request)
		if err != nil {
			return ctrl.Result{}, err
		}
		action = decideTargetRecovery(request, application, time.Now())
	}
	switch action {
	case targetRecoveryCleanup:
		return m.cleanupTarget(ctx, logger, request)
	case targetRecoveryRecordApplication:
		before := request.DeepCopy()
		now := metav1.Now()
		request.Status.Target.AppliedAt = &now
		request.Status.Message = "Fresh target reports the expected source identity and healthy leadership; retained targets require administrator handoff"
		return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, m.patchStatus(ctx, request, before)
	case targetRecoveryWait:
		return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
	default:
		return m.reconcileAcknowledgement(ctx, logger, request)
	}
}

// observeTargetApplication confirms the identity transition only for a fresh
// single-voter target. Existing-cluster health cannot establish that transition.
func (m *Manager) observeTargetApplication(ctx context.Context, request *api.OpenBaoRestore) (targetApplicationState, error) {
	cluster := &api.OpenBaoCluster{}
	err := m.reader.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, cluster)
	if err != nil && !apierrors.IsNotFound(err) {
		return targetApplicationNotObserved, err
	}
	if apierrors.IsNotFound(err) || cluster.UID != request.Status.Target.UID ||
		cluster.Annotations[constants.AnnotationRestoreHold] != string(request.UID) {
		return targetApplicationUnavailable, nil
	}

	health, err := m.targetHealth(ctx, cluster)
	if err == nil && health.ClusterID == request.Spec.Source.ExpectedClusterID && health.Version == request.Spec.Source.ExpectedVersion {
		return targetApplicationConfirmed, nil
	}
	return targetApplicationPending, nil
}
