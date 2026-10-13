package restore

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

const targetBootstrapTimeout = 30 * time.Minute
const targetApplicationTimeout = 10 * time.Minute

func projectRestoreTarget(request *api.OpenBaoRestore) *api.OpenBaoCluster {
	template := request.Spec.ClusterTemplate.DeepCopy()
	oidc := &api.SelfInitOIDCConfig{Enabled: true}
	if request.Spec.ServiceAccountName != "" {
		oidc.AdditionalSubjects = &api.SelfInitOIDCAdditionalSubjects{
			Restore: []api.KubernetesServiceAccountSubject{
				api.KubernetesServiceAccountSubject("system:serviceaccount:" + request.Namespace + ":" + request.Spec.ServiceAccountName),
			},
		}
	}
	return &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        request.Spec.Cluster,
			Namespace:   request.Namespace,
			Annotations: map[string]string{constants.AnnotationRestoreOrigin: string(request.UID)},
		},
		Spec: api.OpenBaoClusterSpec{
			Version:           template.Version,
			Image:             template.Image,
			Replicas:          1,
			Profile:           api.ProfileDevelopment,
			DeletionPolicy:    api.DeletionPolicyRetain,
			ControllerJWTMode: api.ControllerJWTModeTarget,
			Storage:           template.Storage,
			TLS:               template.TLS,
			Resources:         template.Resources,
			InitContainer:     template.InitContainer,
			ImagePullSecrets:  template.ImagePullSecrets,
			Unseal:            &template.Unseal,
			ServiceAccount:    template.ServiceAccount,
			PodMetadata:       template.PodMetadata,
			Plugins:           template.Plugins,
			SelfInit: &api.SelfInitConfig{
				Enabled: true,
				OIDC:    oidc,
				Requests: []api.SelfInitRequest{{
					Name:      "restore-bootstrap-check",
					Operation: api.SelfInitOperationRead,
					Path:      "sys/storage/raft/configuration",
				}},
			},
		},
	}
}

// prepareTarget makes one creation attempt. A lost response can bind the object
// with this request's protected origin; absence never authorizes another create.
func (m *Manager) prepareTarget(ctx context.Context, logger logr.Logger, request *api.OpenBaoRestore) (bool, ctrl.Result, error) {
	if request.Spec.ClusterTemplate == nil {
		return true, ctrl.Result{}, nil
	}

	cluster := &api.OpenBaoCluster{}
	key := client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}
	err := m.reader.Get(ctx, key, cluster)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, ctrl.Result{}, err
	}

	if request.Status.Target == nil {
		result, createErr := m.createFreshTarget(ctx, logger, request, err == nil)
		return false, result, createErr
	}

	target := request.Status.Target
	if apierrors.IsNotFound(err) || cluster.Annotations[constants.AnnotationRestoreOrigin] != string(request.UID) ||
		(target.UID != "" && cluster.UID != target.UID) || cluster.DeletionTimestamp != nil {
		result, err := m.failRestore(ctx, logger, request, "Reserved target is missing, replaced, or deleting; it will not be recreated", ReasonTargetUnavailable)
		return false, result, err
	}
	if target.UID == "" {
		before := request.DeepCopy()
		target.UID = cluster.UID
		return false, ctrl.Result{RequeueAfter: restoreRequeueImmediately}, m.patchStatus(ctx, request, before)
	}
	if time.Now().After(target.ReservedAt.Add(targetBootstrapTimeout)) {
		result, err := m.failRestore(ctx, logger, request, "Fresh target bootstrap deadline exceeded", ReasonTargetBootstrapTimedOut)
		return false, result, err
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: targetPVCName(request)}, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
		}
		return false, ctrl.Result{}, err
	}
	// Destination workloads are trusted: another StatefulSet can copy this owner
	// annotation. The check prevents accidental adoption, not tenant forgery.
	if !originalTargetPVC(pvc, cluster) ||
		(target.DataPVCUID != "" && target.DataPVCUID != pvc.UID) {
		result, err := m.failRestore(ctx, logger, request, "Fresh target PVC was replaced or lacks original owner proof", ReasonTargetStorageChanged)
		return false, result, err
	}
	if target.DataPVCUID == "" {
		before := request.DeepCopy()
		target.DataPVCUID = pvc.UID
		return false, ctrl.Result{RequeueAfter: restoreRequeueImmediately}, m.patchStatus(ctx, request, before)
	}
	if target.BootstrapClusterID != "" {
		return true, ctrl.Result{}, nil
	}

	health, err := m.targetHealth(ctx, cluster)
	if err != nil {
		return false, ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
	}
	if health.Version != request.Spec.Source.ExpectedVersion || health.ClusterID == "" || health.ClusterID == request.Spec.Source.ExpectedClusterID {
		result, err := m.failRestore(ctx, logger, request, "Fresh target must report the expected version and a distinct native identity", ReasonTargetIdentityMismatch)
		return false, result, err
	}
	before := request.DeepCopy()
	target.BootstrapClusterID = health.ClusterID
	return false, ctrl.Result{RequeueAfter: restoreRequeueImmediately}, m.patchStatus(ctx, request, before)
}

func (m *Manager) createFreshTarget(ctx context.Context, logger logr.Logger, request *api.OpenBaoRestore, targetExists bool) (ctrl.Result, error) {
	if request.Status.StartTime != nil && time.Now().After(request.Status.StartTime.Add(targetBootstrapTimeout)) {
		result, err := m.failRestore(ctx, logger, request, "Fresh target validation deadline exceeded", ReasonTargetBootstrapTimedOut)
		return result, err
	}
	if targetExists {
		result, err := m.failRestore(ctx, logger, request, "Fresh targets cannot adopt an existing OpenBaoCluster", ReasonTargetCollision)
		return result, err
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: targetPVCName(request)}, pvc); !apierrors.IsNotFound(err) {
		if err != nil {
			return ctrl.Result{}, err
		}
		result, err := m.failRestore(ctx, logger, request, "Fresh target data PVC already exists", ReasonTargetCollision)
		return result, err
	}

	child := projectRestoreTarget(request)
	// Validate before consuming the creation attempt. This also applies API
	// defaults and installation-specific admission to the bounded template.
	if err := m.client.Create(ctx, child, client.DryRunAll); err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
			result, err := m.failRestore(ctx, logger, request, "Fresh target rejected by destination admission", ReasonDestinationRejected)
			return result, err
		}
		return ctrl.Result{}, fmt.Errorf("validate fresh target: %w", err)
	}
	child.ObjectMeta = projectRestoreTarget(request).ObjectMeta
	before := request.DeepCopy()
	request.Status.Target = &api.RestoreTargetStatus{ReservedAt: metav1.Now()}
	if err := m.patchStatus(ctx, request, before); err != nil {
		return ctrl.Result{}, err
	}
	if err := m.client.Create(ctx, child); err != nil {
		return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, fmt.Errorf("fresh target creation attempt: %w", err)
	}
	return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, nil
}

func targetPVCName(request *api.OpenBaoRestore) string {
	return "data-" + request.Spec.Cluster + "-0"
}

// reconcileTargetRecovery chooses observation, cleanup, or administrator handoff.
// Started cleanup always wins over application checks, even after the hold is gone.
func (m *Manager) reconcileTargetRecovery(ctx context.Context, logger logr.Logger, request *api.OpenBaoRestore) (ctrl.Result, error) {
	target := request.Status.Target
	if target != nil && target.Cleanup != "" {
		return m.cleanupTarget(ctx, logger, request)
	}
	if request.Status.Restart != nil || target == nil || target.BootstrapClusterID == "" {
		return m.reconcileAcknowledgement(ctx, logger, request)
	}
	if target.AppliedAt == nil && request.Status.SubmissionClaim != nil {
		return m.observeTargetApplication(ctx, logger, request)
	}
	return m.finishTargetRecovery(ctx, logger, request)
}

// observeTargetApplication confirms the identity transition only for a fresh
// single-voter target. Existing-cluster health cannot establish that transition.
func (m *Manager) observeTargetApplication(ctx context.Context, logger logr.Logger, request *api.OpenBaoRestore) (ctrl.Result, error) {
	target := request.Status.Target
	cluster := &api.OpenBaoCluster{}
	err := m.reader.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, cluster)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if apierrors.IsNotFound(err) || cluster.UID != target.UID ||
		cluster.Annotations[constants.AnnotationRestoreHold] != string(request.UID) {
		return m.finishTargetRecovery(ctx, logger, request)
	}

	health, err := m.targetHealth(ctx, cluster)
	if err == nil && health.ClusterID == request.Spec.Source.ExpectedClusterID && health.Version == request.Spec.Source.ExpectedVersion {
		before := request.DeepCopy()
		now := metav1.Now()
		target.AppliedAt = &now
		request.Status.Message = "Fresh target reports the expected source identity and healthy leadership; retained targets require administrator handoff"
		return ctrl.Result{RequeueAfter: restoreRequeueImmediately}, m.patchStatus(ctx, request, before)
	}
	if time.Now().Before(request.Status.SubmissionClaim.ClaimedAt.Add(targetApplicationTimeout)) {
		return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
	}
	return m.finishTargetRecovery(ctx, logger, request)
}

func (m *Manager) finishTargetRecovery(ctx context.Context, logger logr.Logger, request *api.OpenBaoRestore) (ctrl.Result, error) {
	if request.Spec.TargetLifecycle != api.RestoreTargetLifecycleDisposable {
		return m.reconcileAcknowledgement(ctx, logger, request)
	}

	target := request.Status.Target
	if target.AppliedAt != nil && time.Now().Before(target.AppliedAt.Add(time.Duration(request.Spec.CleanupAfterSeconds)*time.Second)) {
		return ctrl.Result{RequeueAfter: restoreRequeueJobCheck}, nil
	}
	return m.cleanupTarget(ctx, logger, request)
}

// A failed or cancelled retained target remains available for administrator work,
// including failures before an execution record exists.
func (m *Manager) releaseUnsubmittedTarget(ctx context.Context, request *api.OpenBaoRestore) error {
	if request.Spec.TargetLifecycle != api.RestoreTargetLifecycleRetain || request.Status.Target == nil || !restoreSubmissionExcluded(request) {
		return nil
	}

	cluster := &api.OpenBaoCluster{}
	if err := m.reader.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	if cluster.Annotations[constants.AnnotationRestoreOrigin] != string(request.UID) ||
		(request.Status.Target.UID != "" && request.Status.Target.UID != cluster.UID) {
		return nil
	}

	before := cluster.DeepCopy()
	cluster.Spec.Paused = true
	delete(cluster.Annotations, constants.AnnotationRestoreOrigin)
	return m.client.Patch(ctx, cluster, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func originalTargetPVC(pvc *corev1.PersistentVolumeClaim, cluster *api.OpenBaoCluster) bool {
	return resourceownership.HasOwnerUIDAnnotation(pvc, cluster) &&
		!pvc.CreationTimestamp.Before(&cluster.CreationTimestamp)
}

// needsTargetCleanup applies to terminal reconciliation and request deletion.
// Unknown requests with started cleanup must also finish their final status write.
func needsTargetCleanup(request *api.OpenBaoRestore) bool {
	return request.Spec.TargetLifecycle == api.RestoreTargetLifecycleDisposable &&
		request.Status.Target != nil && request.Status.Target.Cleanup != api.RestoreTargetCleanupComplete &&
		request.Status.AdministratorDisposition != api.RestoreAdministratorAbandon
}

func retainedTargetAccepted(request *api.OpenBaoRestore) bool {
	return request.Spec.TargetLifecycle == api.RestoreTargetLifecycleRetain &&
		request.Status.AdministratorDisposition == api.RestoreAdministratorResume &&
		request.Status.Target != nil && request.Status.Target.AppliedAt != nil
}
