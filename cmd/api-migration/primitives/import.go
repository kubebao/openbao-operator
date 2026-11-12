package primitives

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

const (
	ImportMigrationAnnotation = "migration.kubebao.org/id"
	ImportHashAnnotation      = "migration.kubebao.org/import-hash"
)

// ImportRequest pins a gated source revision. Import accepts an OpenBaoCluster,
// a finished OpenBaoRestore record, or an OpenBaoTenant.
// The caller must stop source and destination reconciliation, protect import
// markers, and durably record intent before using these primitives.
type ImportRequest struct {
	Source                 ObjectIdentity `json:"source"`
	NamespaceUID           types.UID      `json:"namespaceUID"`
	MigrationID            string         `json:"migrationID"`
	LabelKeys              []string       `json:"labelKeys,omitempty"`
	AnnotationKeys         []string       `json:"annotationKeys,omitempty"`
	ExpectedDestinationUID types.UID      `json:"expectedDestinationUID,omitempty"`
	ExpectedImportDigest   string         `json:"expectedImportDigest,omitempty"`
	// CreateOnly returns the created identity before any status write. The caller
	// can persist the UID and update its admission guard before importing status.
	CreateOnly bool `json:"createOnly,omitempty"`
	// Additional explicitly pinned namespaces, including the operator namespace,
	// are checked for unsupported related CRs and helper workloads.
	RelatedNamespaces []NamespaceIdentity `json:"relatedNamespaces,omitempty"`
}

// ImportPayload is transient in-memory configuration, not journal content. It
// can contain source configuration and must not be logged or persisted as a
// snapshot. Only its digest and resource identities belong in a journal. A nil
// Status means the destination controller recomputes it.
type ImportPayload struct {
	Object *unstructured.Unstructured `json:"-"`
	Status map[string]any             `json:"-"`
	Digest string                     `json:"digest"`
}

// ImportResult records observed identity without retaining configuration.
// StatusImported requires the exact status and release of the importer's field
// ownership. It is not permission to activate a controller.
type ImportResult struct {
	Destination    ObjectIdentity `json:"destination"`
	StatusImported bool           `json:"statusImported"`
}

// BuildImport constructs an exact-spec destination from a pinned source. It
// strips server metadata, finalizers and owner references. Owner relationships
// and restore-origin references requiring translation are rejected for now.
// A restore record's status is history and is imported unchanged. A Tenant's
// status only reports provisioning, which the destination provisioner redoes.
func BuildImport(ctx context.Context, reader client.Reader, request ImportRequest) (*ImportPayload, error) {
	if reader == nil {
		return nil, fmt.Errorf("direct migration reader is required")
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, fmt.Errorf("direct migration reader is required")
		}
	}
	if err := importValidateRequest(request); err != nil {
		return nil, err
	}
	source, err := importReadSource(ctx, reader, request)
	if err != nil {
		return nil, err
	}
	if err := importValidateSource(source); err != nil {
		return nil, err
	}
	labels, err := importSelectMetadata(source.GetLabels(), request.LabelKeys)
	if err != nil {
		return nil, err
	}
	annotations, err := importSelectMetadata(source.GetAnnotations(), request.AnnotationKeys)
	if err != nil {
		return nil, err
	}
	object := &unstructured.Unstructured{Object: map[string]any{}}
	object.SetGroupVersionKind(schema.FromAPIVersionAndKind(PlanDestinationAPIVersion, request.Source.Kind))
	object.SetNamespace(request.Source.Namespace)
	object.SetName(request.Source.Name)
	object.SetLabels(labels)
	object.SetAnnotations(annotations)
	spec, found, err := unstructured.NestedMap(source.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("source specification is missing or malformed")
	}
	object.Object["spec"] = spec
	var status map[string]any
	if request.Source.Kind != discoveryTenantKind {
		status, found, err = unstructured.NestedMap(source.Object, "status")
		if err != nil || !found {
			return nil, fmt.Errorf("source status is missing or malformed")
		}
	}
	// Preserve workload-critical state and execution receipts. Zeroing counters
	// or initialization facts can trigger bootstrap or change workload population.
	// These remain historical observations until the destination verifies them.
	if request.Source.Kind == discoveryClusterKind {
		delete(status, "observedGeneration")
		delete(status, "conditions")
		unstructured.RemoveNestedField(status, "backup", "retention", "conditions")
		unstructured.RemoveNestedField(status, "backup", "restoreTest", "conditions")
	}
	payload := &ImportPayload{Object: object, Status: status}
	encoded, err := json.Marshal(struct {
		Source       ObjectIdentity
		NamespaceUID types.UID
		MigrationID  string
		Object       map[string]any
		Status       map[string]any
	}{request.Source, request.NamespaceUID, request.MigrationID, object.Object, status})
	if err != nil {
		return nil, fmt.Errorf("hash import configuration: %w", err)
	}
	sum := sha256.Sum256(encoded)
	payload.Digest = hex.EncodeToString(sum[:])
	annotations[GateSourceUIDAnnotation] = string(request.Source.UID)
	annotations[ImportMigrationAnnotation] = request.MigrationID
	annotations[ImportHashAnnotation] = payload.Digest
	object.SetAnnotations(annotations)
	return payload, nil
}

func importValidateRequest(request ImportRequest) error {
	source := request.Source
	if source.APIVersion != PlanSourceAPIVersion || !importKind(source.Kind) {
		return fmt.Errorf("only openbao.org/v1alpha1 OpenBaoCluster, OpenBaoRestore, and OpenBaoTenant import is supported")
	}
	if source.UID == "" || source.ResourceVersion == "" || request.NamespaceUID == "" || request.MigrationID == "" {
		return fmt.Errorf("import requires source UID/revision, namespace UID and migration ID")
	}
	if len(validation.IsDNS1123Label(source.Namespace)) > 0 ||
		len(validation.IsDNS1123Subdomain(source.Name)) > 0 ||
		len(validation.IsValidLabelValue(request.MigrationID)) > 0 {
		return fmt.Errorf("invalid import namespace, name or migration ID")
	}
	for _, keys := range [][]string{request.LabelKeys, request.AnnotationKeys} {
		for _, key := range keys {
			if len(validation.IsQualifiedName(key)) > 0 || strings.HasPrefix(key, "migration.kubebao.org/") {
				return fmt.Errorf("invalid or reserved selected metadata key")
			}
		}
	}
	return nil
}

func importKind(kind string) bool {
	return kind == discoveryClusterKind || kind == discoveryRestoreKind || kind == discoveryTenantKind
}

// ErrSourceChanged reports a source that was replaced, is deleting, or has a
// different revision than the request pins. A status write changes the revision.
var ErrSourceChanged = errors.New("import source identity or revision changed, or source is deleting")

func importReadSource(ctx context.Context, reader client.Reader,
	request ImportRequest,
) (*unstructured.Unstructured, error) {
	ns := &corev1.Namespace{}
	if err := reader.Get(ctx, client.ObjectKey{Name: request.Source.Namespace}, ns); err != nil {
		return nil, fmt.Errorf("read import namespace: %w", err)
	}
	if ns.UID != request.NamespaceUID || !ns.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("import namespace was replaced or is deleting")
	}
	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(schema.FromAPIVersionAndKind(request.Source.APIVersion, request.Source.Kind))
	if err := reader.Get(ctx, client.ObjectKey{Namespace: request.Source.Namespace, Name: request.Source.Name},
		source); err != nil {
		return nil, fmt.Errorf("read import source: %w", err)
	}
	if source.GetUID() == "" || source.GetResourceVersion() == "" ||
		(request.Source.UID != "" && source.GetUID() != request.Source.UID) ||
		(request.Source.ResourceVersion != "" && source.GetResourceVersion() != request.Source.ResourceVersion) ||
		!source.GetDeletionTimestamp().IsZero() {
		return nil, ErrSourceChanged
	}
	if err := inspectRelatedScope(ctx, reader, request); err != nil {
		return nil, err
	}
	return source, nil
}

// importValidateVersion requires a running source to have reached its desired
// version. A paused source may not have: its operator has not started that
// upgrade, and the destination starts it once the Cluster is resumed.
func importValidateVersion(cluster *api.OpenBaoCluster) error {
	if cluster.Status.CurrentVersion == "" || cluster.Status.CurrentVersion != cluster.Spec.Version {
		return fmt.Errorf("source version has not converged to its desired version")
	}
	return nil
}

func importValidateUpgradeRequests(cluster *api.OpenBaoCluster) error {
	if cluster.Spec.Upgrade == nil || cluster.Spec.Upgrade.Requests == nil {
		return nil
	}
	requests := cluster.Spec.Upgrade.Requests
	handled := cluster.Status.UpgradeRequests
	if handled == nil {
		handled = &api.UpgradeRequestStatus{}
	}
	for _, pair := range [][2]string{
		{requests.Retry, handled.LastHandledRetry},
		{requests.Promote, handled.LastHandledPromote},
		{requests.Rollback, handled.LastHandledRollback},
	} {
		if value := strings.TrimSpace(pair[0]); value != "" && value != strings.TrimSpace(pair[1]) {
			return fmt.Errorf("source has an unhandled upgrade request")
		}
	}
	return nil
}

func importSelectMetadata(values map[string]string, keys []string) (map[string]string, error) {
	result := map[string]string{}
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			return nil, fmt.Errorf("selected source metadata key is absent")
		}
		result[key] = value
	}
	return result, nil
}

// Import creates a destination and imports its status unless CreateOnly
// is set. CreateOnly still validates any existing destination and status.
// Each write rechecks the pinned source. Kubernetes has no cross-object CAS;
// admission gating and stopping all writers remain external prerequisites.
// Resume accepts only an exact protected import identity and configuration.
func Import(ctx context.Context, c client.Client, request ImportRequest) (ImportResult, error) {
	if err := validateImportDigest(request.ExpectedImportDigest); err != nil {
		return ImportResult{}, err
	}
	payload, err := BuildImport(ctx, c, request)
	if err != nil {
		return ImportResult{}, err
	}
	if payload.Digest != request.ExpectedImportDigest {
		return ImportResult{}, fmt.Errorf("import digest differs from the reviewed plan")
	}
	validate := func(ctx context.Context) error {
		_, err := importReadSource(ctx, c, request)
		return err
	}
	return importApplyPayload(ctx, c, request, payload, validate)
}

func importMatchDestination(object *unstructured.Unstructured, request ImportRequest, payload *ImportPayload) error {
	if object.GetUID() == "" || object.GetResourceVersion() == "" || !object.GetDeletionTimestamp().IsZero() ||
		(request.ExpectedDestinationUID != "" && object.GetUID() != request.ExpectedDestinationUID) {
		return fmt.Errorf("import destination identity is absent, replaced or deleting")
	}
	if len(object.GetOwnerReferences()) != 0 || len(object.GetFinalizers()) != 0 ||
		!maps.Equal(object.GetLabels(), payload.Object.GetLabels()) ||
		!maps.Equal(object.GetAnnotations(), payload.Object.GetAnnotations()) ||
		!reflect.DeepEqual(object.Object["spec"], payload.Object.Object["spec"]) {
		return fmt.Errorf("destination is foreign or import configuration drifted; refusing adoption")
	}
	return nil
}

func importInspectCreate(ctx context.Context, reader client.Reader, request ImportRequest,
	payload *ImportPayload, cause error,
) (ImportResult, error) {
	result := ImportResult{}
	observed := payload.Object.DeepCopy()
	if err := reader.Get(ctx, client.ObjectKeyFromObject(observed), observed); err != nil {
		return result, fmt.Errorf("create outcome is unconfirmed; inspect before resume: %w", cause)
	}
	if err := importMatchDestination(observed, request, payload); err != nil {
		return result, err
	}
	result.Destination = identity(PlanDestinationAPIVersion, request.Source.Kind, observed)
	return result, fmt.Errorf("matching destination observed after create error; record identity before resume: %w", cause)
}
