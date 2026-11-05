package primitives

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

// TransferIdentity pins an existing resource, never a name available for adoption.
type TransferIdentity struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
}

// TransferRequest is a single ownership mutation within a quiesced installation.
// It is not a migration authorization or a complete migration plan. Callers must
// hold the migration gate and durably journal intent before calling TransferOwnership.
type TransferRequest struct {
	NamespaceUID types.UID        `json:"namespaceUID"`
	Source       TransferIdentity `json:"source"`
	Destination  TransferIdentity `json:"destination"`
	Resource     TransferIdentity `json:"resource"`
	// Retained selects annotation-only provenance for a retained PVC, Secret,
	// or the Cluster's upgrade ServiceAccount.
	// The caller must verify retention from the installation inventory. This is
	// never inferred from an incomplete controller ownership reference.
	Retained bool `json:"retained"`
}

// TransferOwnership updates only ownership metadata. The object UID and current
// resource version are tested by the API server in the same JSON Patch request.
// A retry inspects ownership first, including when an earlier response was lost.
// Reverse handover uses the same request with Source and Destination exchanged.
// The client must perform direct API reads; a cached manager client is unsuitable
// for verifying an ambiguous write or the current ownership identity.
func TransferOwnership(ctx context.Context, c client.Client, request TransferRequest) error {
	if c == nil {
		return fmt.Errorf("migration client is required")
	}
	if err := validateTransferRequest(request); err != nil {
		return err
	}
	var namespace corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: request.Resource.Namespace}, &namespace); err != nil {
		return fmt.Errorf("read migration namespace: %w", err)
	}
	if namespace.UID != request.NamespaceUID || !namespace.DeletionTimestamp.IsZero() {
		return fmt.Errorf("migration namespace identity changed or namespace is deleting")
	}
	for _, identity := range []TransferIdentity{request.Source, request.Destination} {
		if _, err := readTransferObject(ctx, c, identity); err != nil {
			return err
		}
	}
	object, err := readTransferObject(ctx, c, request.Resource)
	if err != nil {
		return err
	}
	if ownershipProof(object, request.Destination, request.Resource, request.Retained) {
		return nil
	}
	if !ownershipProof(object, request.Source, request.Resource, request.Retained) {
		return fmt.Errorf("resource does not have the nominated source ownership")
	}
	refs := object.GetOwnerReferences()
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			refs[i].APIVersion = request.Destination.APIVersion
			refs[i].Kind = request.Destination.Kind
			refs[i].Name = request.Destination.Name
			refs[i].UID = request.Destination.UID
		}
	}
	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[constants.AnnotationOpenBaoOwnerUID] = string(request.Destination.UID)
	patch := []transferPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: string(object.GetUID())},
		{Op: "test", Path: "/metadata/resourceVersion", Value: object.GetResourceVersion()},
		{Op: "add", Path: "/metadata/annotations", Value: annotations},
	}
	if len(refs) > 0 {
		patch = append(patch, transferPatchOperation{Op: "add", Path: "/metadata/ownerReferences", Value: refs})
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode ownership transfer: %w", err)
	}
	if err := c.Patch(ctx, object, client.RawPatch(types.JSONPatchType, data)); err != nil {
		return fmt.Errorf("ownership transfer result requires inspection before retry: %w", err)
	}
	observed, err := readTransferObject(ctx, c, request.Resource)
	if err != nil {
		return fmt.Errorf("verify ownership transfer: %w", err)
	}
	if !ownershipProof(observed, request.Destination, request.Resource, request.Retained) {
		return fmt.Errorf("ownership transfer read-back does not match destination")
	}
	return nil
}

// JSON Patch values cross a JSON API boundary; domain input remains typed.
type transferPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func validateTransferRequest(request TransferRequest) error {
	for _, identity := range []TransferIdentity{request.Source, request.Destination, request.Resource} {
		if identity.APIVersion == "" || identity.Kind == "" || identity.Namespace == "" ||
			identity.Name == "" || identity.UID == "" {
			return fmt.Errorf("transfer identities require API version, kind, namespace, name, and UID")
		}
		if _, err := schema.ParseGroupVersion(identity.APIVersion); err != nil {
			return fmt.Errorf("invalid transfer API version: %w", err)
		}
	}
	if request.NamespaceUID == "" || request.Source.Namespace != request.Resource.Namespace ||
		request.Destination.Namespace != request.Resource.Namespace {
		return fmt.Errorf("ownership transfer requires a pinned namespace shared by both owners and the resource")
	}
	if (request.Source.Kind != discoveryClusterKind && request.Source.Kind != discoveryRestoreKind) ||
		request.Destination.Kind != request.Source.Kind ||
		request.Source.Name != request.Destination.Name || request.Source.UID == request.Destination.UID {
		return fmt.Errorf("transfer requires distinct source and destination root identities with the same name")
	}
	forward := request.Source.APIVersion == PlanSourceAPIVersion &&
		request.Destination.APIVersion == PlanDestinationAPIVersion
	reverse := request.Source.APIVersion == PlanDestinationAPIVersion &&
		request.Destination.APIVersion == PlanSourceAPIVersion
	if !forward && !reverse {
		return fmt.Errorf("unsupported API-group ownership transfer")
	}
	if !supportedRootResource(request.Source, request.Resource) {
		return fmt.Errorf("unsupported migration resource %s %s", request.Resource.APIVersion, request.Resource.Kind)
	}
	if request.Retained && !supportedRetainedResource(request.Source, request.Resource) {
		return fmt.Errorf("annotation-only migration proof is restricted to nominated retained PVCs, Secrets, and upgrade ServiceAccounts")
	}
	return nil
}

func supportedRetainedResource(root, resource TransferIdentity) bool {
	return root.Kind == discoveryClusterKind && resource.APIVersion == "v1" &&
		(resource.Kind == "PersistentVolumeClaim" || resource.Kind == "Secret" ||
			(resource.Kind == "ServiceAccount" && resource.Name == root.Name+constants.SuffixUpgradeServiceAccount))
}

func supportedTransferResource(identity TransferIdentity) bool {
	switch identity.APIVersion + "/" + identity.Kind {
	case "v1/PersistentVolumeClaim", "v1/Secret", "v1/ConfigMap", "v1/Service", "v1/ServiceAccount",
		"apps/v1/StatefulSet", "batch/v1/Job", "policy/v1/PodDisruptionBudget", "networking.k8s.io/v1/NetworkPolicy",
		"rbac.authorization.k8s.io/v1/Role", "rbac.authorization.k8s.io/v1/RoleBinding",
		"networking.k8s.io/v1/Ingress", "gateway.networking.k8s.io/v1/HTTPRoute",
		"gateway.networking.k8s.io/v1/TLSRoute", "gateway.networking.k8s.io/v1/BackendTLSPolicy",
		"monitoring.coreos.com/v1/ServiceMonitor", "cilium.io/v2/CiliumNetworkPolicy":
		return true
	default:
		return false
	}
}

// supportedRootResource limits each root to what its controller creates: a
// restore record controls only its executor Job.
func supportedRootResource(root, identity TransferIdentity) bool {
	if root.Kind == discoveryRestoreKind {
		return identity.APIVersion == discoveryBatchVersion && identity.Kind == discoveryJobKind
	}
	return supportedTransferResource(identity)
}

func readTransferObject(ctx context.Context, reader client.Reader,
	identity TransferIdentity,
) (*unstructured.Unstructured, error) {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion(identity.APIVersion)
	object.SetKind(identity.Kind)
	if err := reader.Get(ctx, client.ObjectKey{Namespace: identity.Namespace, Name: identity.Name}, object); err != nil {
		return nil, fmt.Errorf("read migration %s %s/%s: %w", identity.Kind, identity.Namespace, identity.Name, err)
	}
	if object.GetUID() != identity.UID || !object.GetDeletionTimestamp().IsZero() {
		return nil, fmt.Errorf("migration %s %s/%s was replaced or is deleting",
			identity.Kind, identity.Namespace, identity.Name)
	}
	if object.GetResourceVersion() == "" {
		return nil, fmt.Errorf("migration resource has no observed resource version")
	}
	return object, nil
}

// ownershipProof proves that root controls object. A restore record's Job
// carries the same proof as a Cluster resource: owner-uid provenance and one
// controller reference, both naming the record. Only Clusters retain resources.
func ownershipProof(object metav1.Object, root, resource TransferIdentity, retained bool) bool {
	if root.Kind == discoveryRestoreKind && retained {
		return false
	}
	if retained && resource.Kind == "ServiceAccount" {
		labels := object.GetLabels()
		if !supportedRetainedResource(root, resource) ||
			labels[constants.LabelAppManagedBy] != constants.LabelValueAppManagedByOpenBaoOperator ||
			labels[constants.LabelOpenBaoCluster] != root.Name ||
			labels[constants.LabelOpenBaoServiceAccountRole] != constants.ServiceAccountRoleUpgrade {
			return false
		}
	}
	return transferProof(object, root, retained)
}

func transferProof(object metav1.Object, owner TransferIdentity, retained bool) bool {
	annotation := object.GetAnnotations()[constants.AnnotationOpenBaoOwnerUID]
	if annotation != string(owner.UID) || object.GetNamespace() != owner.Namespace {
		return false
	}
	controllerFound := false
	for _, ref := range object.GetOwnerReferences() {
		if ref.Controller == nil || !*ref.Controller {
			// An obsolete source reference could cause GC after cleanup. Require
			// explicit treatment rather than silently retaining it.
			if ref.APIVersion == PlanSourceAPIVersion || ref.APIVersion == PlanDestinationAPIVersion {
				return false
			}
			continue
		}
		if retained || controllerFound || ref.APIVersion != owner.APIVersion || ref.Kind != owner.Kind ||
			ref.Name != owner.Name || ref.UID != owner.UID {
			return false
		}
		controllerFound = true
	}
	return controllerFound || retained
}
