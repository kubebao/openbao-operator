package bluegreen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
	"github.com/kubebao/openbao-operator/internal/service/opslifecycle"
)

const defaultValidationHookTimeoutSeconds int32 = 300

type normalizedValidationHook struct {
	Image          string   `json:"image"`
	Command        []string `json:"command"`
	Args           []string `json:"args"`
	TimeoutSeconds int32    `json:"timeoutSeconds"`
}

func normalizeValidationHook(hook *openbaov1alpha1.ValidationHookConfig) (normalizedValidationHook, error) {
	if hook == nil {
		return normalizedValidationHook{}, fmt.Errorf("hook is required")
	}
	if strings.TrimSpace(hook.Image) == "" {
		return normalizedValidationHook{}, fmt.Errorf("hook.image is required")
	}

	timeout := defaultValidationHookTimeoutSeconds
	if hook.TimeoutSeconds != nil {
		timeout = *hook.TimeoutSeconds
	}
	return normalizedValidationHook{
		Image:          strings.TrimSpace(hook.Image),
		Command:        append([]string{}, hook.Command...),
		Args:           append([]string{}, hook.Args...),
		TimeoutSeconds: timeout,
	}, nil
}

func validationHookSpecHash(hook *openbaov1alpha1.ValidationHookConfig) (string, error) {
	normalized, err := normalizeValidationHook(hook)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("failed to encode normalized validation hook: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func validationHookJobName(clusterName, operationID, greenRevision, specHash string) string {
	payload := strings.Join([]string{operationID, greenRevision, specHash}, "|")
	sum := sha256.Sum256([]byte(payload))
	suffix := hex.EncodeToString(sum[:])[:12]

	base := strings.ToLower(fmt.Sprintf("%s%s-validation-hook", jobNamePrefix, clusterName))
	base = strings.ReplaceAll(base, "_", "-")
	maxBaseLen := 63 - 1 - len(suffix)
	if len(base) > maxBaseLen {
		base = strings.TrimRight(base[:maxBaseLen], "-")
	}
	return fmt.Sprintf("%s-%s", base, suffix)
}

func validationHookStatusCopy(status *openbaov1alpha1.BlueGreenValidationHookStatus) *openbaov1alpha1.BlueGreenValidationHookStatus {
	if status == nil {
		return nil
	}
	copy := *status
	return &copy
}

func blueGreenStatusCopy(status *openbaov1alpha1.BlueGreenStatus) *openbaov1alpha1.BlueGreenStatus {
	if status == nil {
		return nil
	}
	copy := *status
	copy.ValidationHook = validationHookStatusCopy(status.ValidationHook)
	return &copy
}

func (m *Manager) persistBlueGreenStatus(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if m.adminOpsMutator == nil {
		return fmt.Errorf("adminops status mutator is required")
	}
	desired := blueGreenStatusCopy(cluster.Status.BlueGreen)
	if err := m.adminOpsMutator(ctx, cluster, func(obj *openbaov1alpha1.OpenBaoCluster) error {
		obj.Status.BlueGreen = desired
		return nil
	}, adminops.ForceOwnershipOnConflict); err != nil {
		return fmt.Errorf("failed to persist blue/green validation hook status: %w", err)
	}
	return nil
}

func validationHookIdentity(
	cluster *openbaov1alpha1.OpenBaoCluster,
	hook *openbaov1alpha1.ValidationHookConfig,
) (operationID, greenRevision, specHash, jobName string, err error) {
	if cluster == nil || cluster.Status.BlueGreen == nil {
		return "", "", "", "", fmt.Errorf("blue/green status is required")
	}
	operationID = strings.TrimSpace(cluster.Status.BlueGreen.OperationID)
	if operationID == "" {
		return "", "", "", "", fmt.Errorf("blue/green operation ID is required")
	}
	greenRevision = strings.TrimSpace(cluster.Status.BlueGreen.GreenRevision)
	if greenRevision == "" {
		return "", "", "", "", fmt.Errorf("green revision is required")
	}
	specHash, err = validationHookSpecHash(hook)
	if err != nil {
		return "", "", "", "", err
	}
	jobName = validationHookJobName(cluster.Name, operationID, greenRevision, specHash)
	return operationID, greenRevision, specHash, jobName, nil
}

func validationHookStatusMatches(
	status *openbaov1alpha1.BlueGreenValidationHookStatus,
	operationID, greenRevision, specHash, jobName string,
) bool {
	return status != nil &&
		status.OperationID == operationID &&
		status.GreenRevision == greenRevision &&
		status.SpecHash == specHash &&
		status.JobName == jobName
}

func (m *Manager) prepareValidationHookExecution(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	operationID, greenRevision, specHash, jobName string,
) error {
	now := metav1.Now()
	cluster.Status.BlueGreen.ValidationHook = &openbaov1alpha1.BlueGreenValidationHookStatus{
		OperationID:   operationID,
		GreenRevision: greenRevision,
		SpecHash:      specHash,
		Stage:         openbaov1alpha1.BlueGreenValidationHookStagePrepared,
		JobName:       jobName,
		PreparedAt:    &now,
	}
	return m.persistBlueGreenStatus(ctx, cluster)
}

func validationHookJobMatchesReceipt(job *batchv1.Job, receipt *openbaov1alpha1.BlueGreenValidationHookStatus) error {
	if job == nil || receipt == nil {
		return fmt.Errorf("validation hook Job and receipt are required")
	}
	if job.Name != receipt.JobName {
		return fmt.Errorf("validation hook Job name %q does not match receipt %q", job.Name, receipt.JobName)
	}
	if job.Annotations[AnnotationValidationHookOperationID] != receipt.OperationID ||
		job.Annotations[AnnotationValidationHookGreenRevision] != receipt.GreenRevision ||
		job.Annotations[AnnotationValidationHookSpecHash] != receipt.SpecHash {
		return fmt.Errorf("validation hook Job %s/%s identity annotations do not match the persisted receipt", job.Namespace, job.Name)
	}
	if receipt.JobUID != "" && job.UID != receipt.JobUID {
		return fmt.Errorf("validation hook Job %s/%s UID %q does not match receipt UID %q", job.Namespace, job.Name, job.UID, receipt.JobUID)
	}
	return nil
}

func (m *Manager) readValidationHookJob(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	receipt *openbaov1alpha1.BlueGreenValidationHookStatus,
) (*batchv1.Job, error) {
	reader := m.reader
	if reader == nil {
		reader = m.client
	}
	job, err := opslifecycle.ReadManagedJob(
		ctx,
		reader,
		types.NamespacedName{Namespace: cluster.Namespace, Name: receipt.JobName},
		cluster,
		openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"),
		"observe blue-green validation hook",
	)
	if err != nil {
		return nil, err
	}
	if err := validationHookJobMatchesReceipt(job, receipt); err != nil {
		return nil, err
	}
	return job, nil
}

func (m *Manager) buildValidationHookJob(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	receipt *openbaov1alpha1.BlueGreenValidationHookStatus,
	hook *openbaov1alpha1.ValidationHookConfig,
) (*batchv1.Job, error) {
	normalized, err := normalizeValidationHook(hook)
	if err != nil {
		return nil, err
	}

	image := normalized.Image
	verifiedDigest, err := m.verifyOperatorImageDigest(
		ctx,
		logger,
		cluster,
		normalized.Image,
		constants.ReasonValidationHookImageVerificationFailed,
		"Validation hook image verification failed",
	)
	if err != nil {
		return nil, err
	}
	if verifiedDigest != "" {
		image = verifiedDigest
	}

	jobLabels := map[string]string{
		constants.LabelAppName:          constants.LabelValueAppNameOpenBao,
		constants.LabelAppInstance:      cluster.Name,
		constants.LabelAppManagedBy:     constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster:   cluster.Name,
		constants.LabelOpenBaoComponent: ComponentValidationHook,
	}
	security.AddManagedWorkloadSecurityLabels(jobLabels, cluster)

	podTemplateLabels := map[string]string{
		constants.LabelAppName:          constants.LabelValueAppNameOpenBao,
		constants.LabelAppInstance:      cluster.Name,
		constants.LabelAppManagedBy:     constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster:   cluster.Name,
		constants.LabelOpenBaoComponent: ComponentValidationHook,
	}
	security.AddManagedWorkloadSecurityLabels(podTemplateLabels, cluster)

	podSecurityContext := &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	if m.Platform != constants.PlatformOpenShift {
		podSecurityContext.RunAsUser = ptr.To(constants.UserNonRoot)
		podSecurityContext.RunAsGroup = ptr.To(constants.UserNonRoot)
	}
	backoffLimit := int32(0)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      receipt.JobName,
			Namespace: cluster.Namespace,
			Labels:    jobLabels,
			Annotations: map[string]string{
				AnnotationValidationHookOperationID:   receipt.OperationID,
				AnnotationValidationHookGreenRevision: receipt.GreenRevision,
				AnnotationValidationHookSpecHash:      receipt.SpecHash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: ptr.To(int64(normalized.TimeoutSeconds)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podTemplateLabels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					ImagePullSecrets:             cluster.Spec.ImagePullSecrets,
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext:              podSecurityContext,
					Containers: []corev1.Container{{
						Name:    "validation",
						Image:   image,
						Command: normalized.Command,
						Args:    normalized.Args,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
							ReadOnlyRootFilesystem: ptr.To(true),
							RunAsNonRoot:           ptr.To(true),
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
		},
	}, nil
}

func (m *Manager) markValidationHookUnknown(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	receipt := cluster.Status.BlueGreen.ValidationHook
	receipt.Stage = openbaov1alpha1.BlueGreenValidationHookStageUnknown
	return m.persistBlueGreenStatus(ctx, cluster)
}

func (m *Manager) persistValidationHookCreated(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	job *batchv1.Job,
) error {
	receipt := cluster.Status.BlueGreen.ValidationHook
	now := metav1.Now()
	receipt.Stage = openbaov1alpha1.BlueGreenValidationHookStageCreated
	receipt.JobUID = job.UID
	receipt.CreatedAt = &now
	return m.persistBlueGreenStatus(ctx, cluster)
}

func (m *Manager) commitAndCreateValidationHookJob(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	hook *openbaov1alpha1.ValidationHookConfig,
) (*JobResult, error) {
	receipt := cluster.Status.BlueGreen.ValidationHook
	job, err := m.buildValidationHookJob(ctx, logger, cluster, receipt, hook)
	if err != nil {
		return nil, err
	}
	if err := opslifecycle.PrepareManagedJobOwner(job, cluster, m.scheme); err != nil {
		return nil, fmt.Errorf("failed to prepare validation hook Job ownership %s/%s: %w", cluster.Namespace, receipt.JobName, err)
	}

	now := metav1.Now()
	receipt.Stage = openbaov1alpha1.BlueGreenValidationHookStageCommitted
	receipt.CommittedAt = &now
	if err := m.persistBlueGreenStatus(ctx, cluster); err != nil {
		return nil, err
	}
	receipt = cluster.Status.BlueGreen.ValidationHook

	logger.Info("Creating pre-promotion validation hook Job", "job", receipt.JobName, "operationID", receipt.OperationID)
	if err := m.client.Create(ctx, job); err != nil {
		existing, readErr := m.readValidationHookJob(ctx, cluster, receipt)
		if readErr == nil {
			if persistErr := m.persistValidationHookCreated(ctx, cluster, existing); persistErr != nil {
				return nil, persistErr
			}
			return jobResultFromJob(existing), nil
		}
		if !apierrors.IsNotFound(readErr) {
			return nil, fmt.Errorf("validation hook Job create failed and its outcome could not be observed: create error: %v; read error: %w", err, readErr)
		}
		if persistErr := m.markValidationHookUnknown(ctx, cluster); persistErr != nil {
			return nil, fmt.Errorf("validation hook Job create failed and marking the execution Unknown also failed: create error: %v; status error: %w", err, persistErr)
		}
		return nil, fmt.Errorf("validation hook execution %s is Unknown after its committed Job creation failed: %w", receipt.OperationID, err)
	}

	if err := m.persistValidationHookCreated(ctx, cluster, job); err != nil {
		return nil, err
	}
	return jobResultFromJob(job), nil
}

func (m *Manager) observePersistedValidationHook(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (*JobResult, bool, error) {
	receipt := cluster.Status.BlueGreen.ValidationHook
	job, err := m.readValidationHookJob(ctx, cluster, receipt)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if persistErr := m.markValidationHookUnknown(ctx, cluster); persistErr != nil {
				return nil, false, persistErr
			}
			return nil, false, fmt.Errorf("validation hook execution %s is Unknown because committed Job %s/%s is missing", receipt.OperationID, cluster.Namespace, receipt.JobName)
		}
		return nil, false, err
	}

	if receipt.Stage == openbaov1alpha1.BlueGreenValidationHookStageCommitted {
		if err := m.persistValidationHookCreated(ctx, cluster, job); err != nil {
			return nil, false, err
		}
		return jobResultFromJob(job), true, nil
	}

	result := jobResultFromJob(job)
	if result.Running {
		return result, false, nil
	}

	now := metav1.Now()
	receipt.Stage = openbaov1alpha1.BlueGreenValidationHookStageTerminalObserved
	receipt.TerminalObservedAt = &now
	if result.Succeeded {
		receipt.TerminalResult = openbaov1alpha1.BlueGreenValidationHookResultSucceeded
	} else {
		receipt.TerminalResult = openbaov1alpha1.BlueGreenValidationHookResultFailed
	}
	if err := m.persistBlueGreenStatus(ctx, cluster); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func validationHookResultFromReceipt(receipt *openbaov1alpha1.BlueGreenValidationHookStatus) (*JobResult, error) {
	if receipt == nil || receipt.Stage != openbaov1alpha1.BlueGreenValidationHookStageTerminalObserved {
		return nil, fmt.Errorf("terminal validation hook receipt is required")
	}
	result := &JobResult{Name: receipt.JobName, Exists: true}
	switch receipt.TerminalResult {
	case openbaov1alpha1.BlueGreenValidationHookResultSucceeded:
		result.Succeeded = true
	case openbaov1alpha1.BlueGreenValidationHookResultFailed:
		result.Failed = true
	default:
		return nil, fmt.Errorf("validation hook receipt has invalid terminal result %q", receipt.TerminalResult)
	}
	return result, nil
}

func (m *Manager) reconcilePrePromotionHookJob(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
	hook *openbaov1alpha1.ValidationHookConfig,
) (*JobResult, bool, error) {
	if cluster == nil || cluster.Status.BlueGreen == nil {
		return nil, false, fmt.Errorf("blue/green status is required")
	}

	receipt := cluster.Status.BlueGreen.ValidationHook
	if receipt == nil {
		if hook == nil {
			return nil, false, nil
		}
		if strings.TrimSpace(cluster.Status.BlueGreen.OperationID) == "" {
			cluster.Status.BlueGreen.OperationID = string(uuid.NewUUID())
			if err := m.persistBlueGreenStatus(ctx, cluster); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		}
		operationID, greenRevision, specHash, jobName, err := validationHookIdentity(cluster, hook)
		if err != nil {
			return nil, false, err
		}
		if err := m.prepareValidationHookExecution(ctx, cluster, operationID, greenRevision, specHash, jobName); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}

	if hook != nil {
		operationID, greenRevision, specHash, jobName, err := validationHookIdentity(cluster, hook)
		if err != nil {
			return nil, false, err
		}
		if !validationHookStatusMatches(receipt, operationID, greenRevision, specHash, jobName) {
			if receipt.Stage == openbaov1alpha1.BlueGreenValidationHookStagePrepared {
				if err := m.prepareValidationHookExecution(ctx, cluster, operationID, greenRevision, specHash, jobName); err != nil {
					return nil, false, err
				}
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("pre-promotion hook specification changed after execution %s was committed", receipt.OperationID)
		}
	} else if receipt.Stage == openbaov1alpha1.BlueGreenValidationHookStagePrepared {
		return nil, false, fmt.Errorf("pre-promotion hook was removed after execution %s was prepared", receipt.OperationID)
	}

	switch receipt.Stage {
	case openbaov1alpha1.BlueGreenValidationHookStagePrepared:
		result, err := m.commitAndCreateValidationHookJob(ctx, logger, cluster, hook)
		return result, err == nil, err
	case openbaov1alpha1.BlueGreenValidationHookStageCommitted,
		openbaov1alpha1.BlueGreenValidationHookStageCreated:
		return m.observePersistedValidationHook(ctx, cluster)
	case openbaov1alpha1.BlueGreenValidationHookStageTerminalObserved:
		result, err := validationHookResultFromReceipt(receipt)
		return result, false, err
	case openbaov1alpha1.BlueGreenValidationHookStageUnknown:
		return nil, false, fmt.Errorf("pre-promotion hook execution %s is Unknown and will not be recreated automatically", receipt.OperationID)
	default:
		return nil, false, fmt.Errorf("pre-promotion hook execution %s has invalid stage %q", receipt.OperationID, receipt.Stage)
	}
}

func (m *Manager) deleteTerminalValidationHookJob(
	ctx context.Context,
	cluster *openbaov1alpha1.OpenBaoCluster,
	receipt *openbaov1alpha1.BlueGreenValidationHookStatus,
) error {
	job, err := m.readValidationHookJob(ctx, cluster, receipt)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := m.client.Delete(ctx, job, client.Preconditions{UID: &job.UID}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete terminal validation hook Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}

func (m *Manager) reconcileValidationHookOutsideSyncing(
	ctx context.Context,
	logger logr.Logger,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (bool, recon.Result, error) {
	if cluster == nil || cluster.Status.BlueGreen == nil || cluster.Status.BlueGreen.ValidationHook == nil {
		return false, recon.Result{}, nil
	}
	if cluster.Status.BlueGreen.Phase == openbaov1alpha1.PhaseSyncing {
		return false, recon.Result{}, nil
	}

	receipt := cluster.Status.BlueGreen.ValidationHook
	switch receipt.Stage {
	case openbaov1alpha1.BlueGreenValidationHookStagePrepared:
		if cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseIdle &&
			cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseRollingBack &&
			cluster.Status.BlueGreen.Phase != openbaov1alpha1.PhaseRollbackCleanup {
			return true, recon.Result{}, fmt.Errorf("validation hook execution %s was not committed before phase %s", receipt.OperationID, cluster.Status.BlueGreen.Phase)
		}
		cluster.Status.BlueGreen.ValidationHook = nil
		if cluster.Status.BlueGreen.Phase == openbaov1alpha1.PhaseIdle {
			cluster.Status.BlueGreen.OperationID = ""
		}
		if err := m.persistBlueGreenStatus(ctx, cluster); err != nil {
			return true, recon.Result{}, err
		}
		return true, requeueShort(), nil
	case openbaov1alpha1.BlueGreenValidationHookStageCommitted,
		openbaov1alpha1.BlueGreenValidationHookStageCreated:
		result, receiptAdvanced, err := m.observePersistedValidationHook(ctx, cluster)
		if err != nil {
			return true, recon.Result{}, err
		}
		if receiptAdvanced || result == nil || result.Running {
			return true, requeueShort(), nil
		}
		return true, requeueShort(), nil
	case openbaov1alpha1.BlueGreenValidationHookStageTerminalObserved:
		if err := m.deleteTerminalValidationHookJob(ctx, cluster, receipt); err != nil {
			return true, recon.Result{}, err
		}
		logger.Info("Deleted terminal pre-promotion validation hook Job", "job", receipt.JobName, "operationID", receipt.OperationID)
		cluster.Status.BlueGreen.ValidationHook = nil
		if cluster.Status.BlueGreen.Phase == openbaov1alpha1.PhaseIdle {
			cluster.Status.BlueGreen.OperationID = ""
		}
		if err := m.persistBlueGreenStatus(ctx, cluster); err != nil {
			return true, recon.Result{}, err
		}
		return true, requeueShort(), nil
	case openbaov1alpha1.BlueGreenValidationHookStageUnknown:
		return true, recon.Result{}, fmt.Errorf("pre-promotion hook execution %s is Unknown and will not be recreated automatically", receipt.OperationID)
	default:
		return true, recon.Result{}, fmt.Errorf("pre-promotion hook execution %s has invalid stage %q", receipt.OperationID, receipt.Stage)
	}
}
