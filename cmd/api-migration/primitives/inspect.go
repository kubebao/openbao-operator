package primitives

import (
	"context"
	"encoding/hex"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

type SourceInspection struct {
	Source         ObjectIdentity `json:"source"`
	NamespaceUID   types.UID      `json:"namespaceUID"`
	Generation     int64          `json:"generation"`
	CurrentVersion string         `json:"currentVersion"`
}

// InspectSource reads only the explicitly nominated source API identity. Typed
// conversion below validates fields; it never selects the source API endpoint.
// The result is CR health evidence, not a live OpenBao probe or operation drain.
// A restore record is accepted only once it is finished, a Tenant at any time
// it is not being deleted.
func InspectSource(ctx context.Context, reader client.Reader, request ImportRequest) (SourceInspection, error) {
	if reader == nil {
		return SourceInspection{}, fmt.Errorf("direct source reader is required")
	}
	kind := request.Source.Kind
	if request.Source.APIVersion != PlanSourceAPIVersion || !importKind(kind) ||
		request.NamespaceUID == "" || len(validation.IsDNS1123Label(request.Source.Namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(request.Source.Name)) != 0 {
		return SourceInspection{}, fmt.Errorf("inspection requires an explicit source root and pinned namespace")
	}
	source, err := importReadSource(ctx, reader, request)
	if err != nil {
		return SourceInspection{}, err
	}
	if err := importValidateSource(source); err != nil {
		return SourceInspection{}, err
	}
	version, _, _ := unstructured.NestedString(source.Object, "status", "currentVersion")
	return SourceInspection{Source: identity(PlanSourceAPIVersion, kind, source),
		NamespaceUID: request.NamespaceUID, Generation: source.GetGeneration(), CurrentVersion: version}, nil
}

func validateImportDigest(digest string) error {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
		return fmt.Errorf("import requires a reviewed canonical SHA-256 digest")
	}
	return nil
}

func importValidateSource(source *unstructured.Unstructured) error {
	switch source.GetKind() {
	case discoveryRestoreKind:
		return validateRestoreSource(source)
	case discoveryTenantKind:
		return validateTenantSource(source)
	}
	if len(source.GetOwnerReferences()) != 0 || source.GetAnnotations()[constants.AnnotationRestoreOrigin] != "" {
		return fmt.Errorf("source owner or restore-origin dependencies are unsupported")
	}
	cluster := &api.OpenBaoCluster{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(source.Object, cluster); err != nil {
		return fmt.Errorf("source does not match the released cluster schema")
	}
	switch cluster.Spec.ControllerJWTMode {
	case "", api.ControllerJWTModeShared, api.ControllerJWTModeTarget:
	default:
		return fmt.Errorf("source has an unsupported controller JWT mode")
	}
	if cluster.Generation < 1 || cluster.Status.ObservedGeneration != cluster.Generation {
		return fmt.Errorf("source must be observed at its current generation")
	}
	available := meta.FindStatusCondition(cluster.Status.Conditions, string(api.ConditionAvailable))
	if cluster.Spec.Paused {
		// A paused Cluster moves as it is. Its operator leaves the workload and
		// OpenBao alone and does not evaluate availability, so only the pause
		// itself must be observed, with nothing in flight.
		if available == nil || available.Reason != PausedReason || available.ObservedGeneration != cluster.Generation {
			return fmt.Errorf("source pause is not observed at its current generation")
		}
		return validateSourceOperations(cluster)
	}
	if !cluster.Status.Initialized || cluster.Status.Phase != api.ClusterPhaseRunning ||
		cluster.Spec.Replicas < 1 || cluster.Status.ReadyReplicas != cluster.Spec.Replicas {
		return fmt.Errorf("source must be paused, or initialized, Running, and ready")
	}
	if !readReplicasSettled(cluster) {
		return fmt.Errorf("source read replicas must all be ready and registered")
	}
	if available == nil || available.Status != metav1.ConditionTrue || available.ObservedGeneration != cluster.Generation {
		return fmt.Errorf("source availability is not proven at its current generation")
	}
	if err := validateSourceOperations(cluster); err != nil {
		return err
	}
	return importValidateVersion(cluster)
}

// PausedReason is the Available reason the operator records once it observes
// a pause, in the source release and in the destination.
const PausedReason = "Paused"

// readReplicasSettled requires a configured read-replica pool to be complete,
// so Raft membership is stable before custody moves.
func readReplicasSettled(cluster *api.OpenBaoCluster) bool {
	want := int32(0)
	if cluster.Spec.ReadReplicas != nil {
		want = cluster.Spec.ReadReplicas.Replicas
	}
	if want == 0 {
		return true
	}
	status := cluster.Status.ReadReplicas
	return status != nil && status.DesiredReplicas == want && status.ReadyReplicas == want &&
		status.RegisteredReplicas == want
}

// validateSourceOperations rejects in-flight work. status.restore is not one:
// it is history retained from earlier releases and is imported unchanged.
func validateSourceOperations(cluster *api.OpenBaoCluster) error {
	if cluster.Status.OperationLock != nil || cluster.Status.Upgrade != nil ||
		cluster.Annotations[constants.AnnotationRestoreHold] != "" ||
		cluster.Annotations[constants.AnnotationTriggerBackup] != "" ||
		(cluster.Status.BreakGlass != nil && cluster.Status.BreakGlass.Active) {
		return fmt.Errorf("source has an operation, restore hold, backup trigger, or active break-glass state")
	}
	if bg := cluster.Status.BlueGreen; bg != nil &&
		((bg.Phase != "" && bg.Phase != api.PhaseIdle) || bg.ValidationHook != nil) {
		return fmt.Errorf("source has blue-green execution state")
	}
	if backup := cluster.Status.Backup; backup != nil &&
		((backup.Retention != nil && backup.Retention.Active != nil) ||
			(backup.RestoreTest != nil && backup.RestoreTest.Active != nil)) {
		return fmt.Errorf("source has active backup retention or restore testing")
	}
	return importValidateUpgradeRequests(cluster)
}

// This bounded scope pins each nominated namespace and rejects unfinished
// operator Jobs in it. Finished Jobs are history: they move with their owner.
// Tenant and restore records are the caller's to inspect and import. It
// cannot discover other namespaces.
func inspectRelatedScope(ctx context.Context, reader client.Reader, request ImportRequest) error {
	namespaces := map[string]types.UID{request.Source.Namespace: request.NamespaceUID}
	for _, namespace := range request.RelatedNamespaces {
		if namespace.UID == "" || len(validation.IsDNS1123Label(namespace.Name)) != 0 {
			return fmt.Errorf("related namespaces require valid names and UIDs")
		}
		if uid, found := namespaces[namespace.Name]; found && uid != namespace.UID {
			return fmt.Errorf("related namespace identity conflicts with the source namespace")
		}
		namespaces[namespace.Name] = namespace.UID
	}
	for name, uid := range namespaces {
		namespace := &corev1.Namespace{}
		if err := reader.Get(ctx, client.ObjectKey{Name: name}, namespace); err != nil {
			return fmt.Errorf("read related namespace: %w", err)
		}
		if namespace.UID != uid || !namespace.DeletionTimestamp.IsZero() {
			return fmt.Errorf("related namespace was replaced or is deleting")
		}
		if err := inspectRunningJobs(ctx, reader, name); err != nil {
			return err
		}
	}
	return nil
}

// inspectRunningJobs refuses operator Jobs that have not finished. A running
// backup, retention, or restore-test Job still holds its operation.
func inspectRunningJobs(ctx context.Context, reader client.Reader, namespace string) error {
	jobs := &batchv1.JobList{}
	if err := reader.List(ctx, jobs, client.InNamespace(namespace),
		client.MatchingLabels{constants.LabelAppManagedBy: constants.LabelValueAppManagedByOpenBaoOperator}); err != nil {
		return fmt.Errorf("inspect operator Jobs: %w", err)
	}
	for _, job := range jobs.Items {
		finished := false
		for _, condition := range job.Status.Conditions {
			finished = finished || ((condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) &&
				condition.Status == corev1.ConditionTrue)
		}
		if !finished {
			return fmt.Errorf("operator Job %s/%s is still running; wait for it to finish", namespace, job.Name)
		}
	}
	return nil
}
