package primitives

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	discoveryAppsVersion     = "apps/v1"
	discoveryClusterKind     = "OpenBaoCluster"
	discoveryRestoreKind     = "OpenBaoRestore"
	discoveryTenantKind      = "OpenBaoTenant"
	discoverySecretKind      = "Secret"
	discoveryPVCKind         = "PersistentVolumeClaim"
	discoveryStatefulSetKind = "StatefulSet"
	discoveryJobKind         = "Job"
	discoveryBatchVersion    = "batch/v1"
	discoveryRevisionKind    = "ControllerRevision"
)

// DiscoveryOptions nominates one source and its already-created destination.
// A destination without a UID requests a preview before the destination exists:
// its transfers cannot be executed because TransferOwnership requires both UIDs.
// RetainedResources must come from a separate custody review. Annotation-only
// provenance does not establish that a resource was intentionally retained.
type DiscoveryOptions struct {
	NamespaceUID      types.UID          `json:"namespaceUID"`
	Source            TransferIdentity   `json:"source"`
	Destination       TransferIdentity   `json:"destination"`
	RetainedResources []TransferIdentity `json:"retainedResources,omitempty"`
	// RetiredUID names a deleted former owner for a preview. Its UID can remain on
	// historical ControllerRevisions, which StatefulSets never rewrite.
	RetiredUID types.UID `json:"retiredUID,omitempty"`
	// ForeignRoots names OpenBaoRestore records migrated alongside a Cluster. A
	// resource one of them controls, such as a restore Job labelled with the
	// Cluster's name, moves with that record: it is reported as its descendant.
	ForeignRoots []TransferIdentity `json:"foreignRoots,omitempty"`
}

// DiscoveryKind records the finite API scope inspected by this discovery.
type DiscoveryKind struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Available  bool   `json:"available"`
}

// DiscoveryDescendant retains its exact StatefulSet owner, Job, or restore record.
// Its ownership must not be replaced with an OpenBaoCluster reference during handover. A
// ControllerRevision can retain owner-uid metadata copied when it was created;
// its exact StatefulSet controller reference establishes descendant custody.
type DiscoveryDescendant struct {
	Resource TransferIdentity `json:"resource"`
	Owner    TransferIdentity `json:"owner"`
}

// DiscoveryBlocker reports only identities and a fixed reason code.
type DiscoveryBlocker struct {
	Resource TransferIdentity `json:"resource"`
	Code     string           `json:"code"`
}

// ManagedDiscovery is a bounded metadata inventory, not a complete ownership
// graph or mutation authorization. All transfers still require a verified gate,
// durable intent, and fresh ownership checks at the mutation boundary.
// Callers must resolve every Blocker before using the transfer batch.
type ManagedDiscovery struct {
	Transfers   []TransferRequest     `json:"transfers"`
	Descendants []DiscoveryDescendant `json:"descendants"`
	Blockers    []DiscoveryBlocker    `json:"blockers"`
	Scope       []DiscoveryKind       `json:"scope"`
	Limitations []string              `json:"limitations"`
}

type discoveryType struct {
	apiVersion string
	kind       string
	optional   bool
}

// This list follows the released workload/networking/credential builders.
var discoveryTypes = []discoveryType{
	{"v1", discoverySecretKind, false}, {"v1", "ConfigMap", false}, {"v1", "Service", false},
	{"v1", "ServiceAccount", false}, {"v1", discoveryPVCKind, false}, {"v1", "Pod", false},
	{discoveryAppsVersion, discoveryStatefulSetKind, false}, {discoveryAppsVersion, discoveryRevisionKind, false},
	{discoveryBatchVersion, discoveryJobKind, false},
	{"policy/v1", "PodDisruptionBudget", false}, {"networking.k8s.io/v1", "NetworkPolicy", false},
	{"networking.k8s.io/v1", "Ingress", false}, {"rbac.authorization.k8s.io/v1", "Role", false},
	{"rbac.authorization.k8s.io/v1", "RoleBinding", false},
	{"gateway.networking.k8s.io/v1", "HTTPRoute", true},
	{"gateway.networking.k8s.io/v1", "TLSRoute", true},
	{"gateway.networking.k8s.io/v1", "BackendTLSPolicy", true},
	{"monitoring.coreos.com/v1", "ServiceMonitor", true}, {"cilium.io/v2", "CiliumNetworkPolicy", true},
}

// DiscoverManagedResources uses metadata-only API reads, including for Secrets.
// Use NewDiscoveryReader to prohibit client-go's full-object JSON fallback.
// Other readers must provide the same metadata-only transport guarantee.
// Labels identify possible incomplete proof;
// they never authorize adoption. A missing optional API is reported in Scope;
// any other list failure aborts discovery rather than returning a partial batch.
func DiscoverManagedResources(ctx context.Context, direct client.Reader,
	options DiscoveryOptions,
) (*ManagedDiscovery, error) {
	if direct == nil || (reflect.ValueOf(direct).Kind() == reflect.Pointer && reflect.ValueOf(direct).IsNil()) {
		return nil, fmt.Errorf("direct discovery reader is required")
	}
	if err := discoveryValidate(options); err != nil {
		return nil, err
	}
	if err := discoveryCheckRoots(ctx, direct, options); err != nil {
		return nil, err
	}
	result := &ManagedDiscovery{Limitations: []string{
		"Only the listed resource kinds in the nominated namespace are inspected.",
		"External references, resource payloads, admission, RBAC contents, and operation drain are not verified.",
		"Objects without matching identity evidence or cluster labels cannot be attributed by this scan.",
		"This is not an atomic snapshot; verify the gate and recheck every identity before mutation.",
	}}
	objects, err := discoveryList(ctx, direct, options.Source.Namespace, result)
	if err != nil {
		return nil, err
	}
	// Controllers whose children are descendants: StatefulSets (Pods, claims,
	// ControllerRevisions) and lifecycle Jobs (Pods). Their children keep their
	// own controller; only the parent's ownership moves. A foreign root's Job is a
	// parent too: its Pods are accounted for, though the Job moves separately.
	sets := make(map[types.UID]TransferIdentity)
	for i := range objects {
		object := &objects[i]
		parent := (object.APIVersion == discoveryAppsVersion && object.Kind == discoveryStatefulSetKind) ||
			(object.APIVersion == discoveryBatchVersion && object.Kind == discoveryJobKind)
		_, foreign := discoveryForeignRoot(object, options.ForeignRoots)
		if parent && discoveryUsable(object) && (foreign || (!discoveryInvalidOpenBaoReference(object, options.Source) &&
			ownershipProof(object, options.Source, discoveryIdentity(object), false))) {
			sets[object.UID] = discoveryIdentity(object)
		}
	}
	seenRetained := make(map[string]bool)
	for i := range objects {
		result.discoveryClassify(&objects[i], options, sets, seenRetained)
	}
	for _, nominated := range options.RetainedResources {
		if !seenRetained[discoveryKey(nominated)] {
			result.blockDiscovery(nominated, "RetainedResourceMissing")
		}
	}
	// Root replacement or deletion during the scan invalidates all observations.
	if err := discoveryCheckRoots(ctx, direct, options); err != nil {
		return nil, err
	}
	slices.SortFunc(result.Transfers, func(a, b TransferRequest) int {
		return strings.Compare(discoveryKey(a.Resource), discoveryKey(b.Resource))
	})
	slices.SortFunc(result.Descendants, func(a, b DiscoveryDescendant) int {
		return strings.Compare(discoveryKey(a.Resource), discoveryKey(b.Resource))
	})
	slices.SortFunc(result.Blockers, func(a, b DiscoveryBlocker) int {
		return strings.Compare(discoveryKey(a.Resource)+"/"+a.Code, discoveryKey(b.Resource)+"/"+b.Code)
	})
	return result, nil
}

func discoveryValidate(options DiscoveryOptions) error {
	source, destination := options.Source, options.Destination
	forward := source.APIVersion == PlanSourceAPIVersion && destination.APIVersion == PlanDestinationAPIVersion
	reverse := source.APIVersion == PlanDestinationAPIVersion && destination.APIVersion == PlanSourceAPIVersion
	if (!forward && !reverse) ||
		(source.Kind != discoveryClusterKind && source.Kind != discoveryRestoreKind) ||
		destination.Kind != source.Kind || source.Name != destination.Name ||
		source.Namespace != destination.Namespace || source.UID == "" ||
		source.UID == destination.UID || options.NamespaceUID == "" ||
		len(validation.IsDNS1123Label(source.Namespace)) > 0 ||
		len(validation.IsDNS1123Subdomain(source.Name)) > 0 {
		return fmt.Errorf("discovery requires pinned source and destination root identities in one pinned namespace")
	}
	if source.Kind == discoveryRestoreKind && (len(options.RetainedResources) != 0 || len(options.ForeignRoots) != 0) {
		return fmt.Errorf("restore discovery has no retained resources or foreign roots")
	}
	for _, root := range options.ForeignRoots {
		if (root.APIVersion != PlanSourceAPIVersion && root.APIVersion != PlanDestinationAPIVersion) ||
			root.Kind != discoveryRestoreKind || root.Namespace != source.Namespace || root.Name == "" || root.UID == "" {
			return fmt.Errorf("foreign discovery roots must be pinned restore records in the source namespace")
		}
	}
	seen := make(map[string]bool)
	for _, retained := range options.RetainedResources {
		request := TransferRequest{NamespaceUID: options.NamespaceUID, Source: source,
			Destination: destination, Resource: retained, Retained: true}
		if request.Destination.UID == "" {
			// A preview validates nominations against a placeholder destination identity.
			request.Destination.UID = source.UID + "-preview"
		}
		if err := validateTransferRequest(request); err != nil {
			return fmt.Errorf("invalid retained discovery nomination: %w", err)
		}
		key := discoveryKey(retained)
		if seen[key] {
			return fmt.Errorf("duplicate retained discovery nomination")
		}
		seen[key] = true
	}
	return nil
}

func discoveryCheckRoots(ctx context.Context, direct client.Reader, options DiscoveryOptions) error {
	roots := []TransferIdentity{{APIVersion: "v1", Kind: "Namespace", Name: options.Source.Namespace,
		UID: options.NamespaceUID}, options.Source}
	if options.Destination.UID != "" {
		roots = append(roots, options.Destination)
	}
	for _, root := range roots {
		object := &metav1.PartialObjectMetadata{}
		object.SetGroupVersionKind(schema.FromAPIVersionAndKind(root.APIVersion, root.Kind))
		if err := direct.Get(ctx, client.ObjectKey{Namespace: root.Namespace, Name: root.Name}, object); err != nil {
			return fmt.Errorf("read discovery identity %s %s: %w", root.Kind, root.Name, err)
		}
		if object.UID != root.UID || !discoveryUsable(object) {
			return fmt.Errorf("discovery root identity changed, is deleting, or lacks an observed revision")
		}
	}
	return nil
}

func discoveryList(ctx context.Context, reader client.Reader, namespace string,
	result *ManagedDiscovery,
) ([]metav1.PartialObjectMetadata, error) {
	var objects []metav1.PartialObjectMetadata
	seenObjects := make(map[string]bool)
	for _, kind := range discoveryTypes {
		available := true
		continuation := ""
		seen := make(map[string]bool)
		for {
			list := &metav1.PartialObjectMetadataList{}
			list.SetGroupVersionKind(schema.FromAPIVersionAndKind(kind.apiVersion, kind.kind+"List"))
			err := reader.List(ctx, list, client.InNamespace(namespace), client.Limit(500), client.Continue(continuation))
			if err != nil {
				if kind.optional && continuation == "" && meta.IsNoMatchError(err) {
					available = false
					break
				}
				return nil, fmt.Errorf("list discovery %s %s metadata: %w", kind.apiVersion, kind.kind, err)
			}
			for i := range list.Items {
				object := list.Items[i]
				object.SetGroupVersionKind(schema.FromAPIVersionAndKind(kind.apiVersion, kind.kind))
				if object.Namespace != namespace {
					return nil, fmt.Errorf("discovery list returned a resource outside the nominated namespace")
				}
				key := discoveryKey(discoveryIdentity(&object))
				if seenObjects[key] {
					return nil, fmt.Errorf("discovery list repeated a resource identity")
				}
				seenObjects[key] = true
				objects = append(objects, object)
			}
			if len(objects) > 10000 {
				return nil, fmt.Errorf("discovery exceeds the 10000-resource metadata bound")
			}
			continuation = list.Continue
			if continuation == "" {
				break
			}
			if seen[continuation] {
				return nil, fmt.Errorf("discovery list repeated a continuation token")
			}
			seen[continuation] = true
		}
		result.Scope = append(result.Scope, DiscoveryKind{APIVersion: kind.apiVersion, Kind: kind.kind, Available: available})
	}
	return objects, nil
}

func (result *ManagedDiscovery) discoveryClassify(object *metav1.PartialObjectMetadata, options DiscoveryOptions,
	sets map[types.UID]TransferIdentity, seenRetained map[string]bool,
) {
	resource := discoveryIdentity(object)
	var retained *TransferIdentity
	for i := range options.RetainedResources {
		if discoveryKey(options.RetainedResources[i]) == discoveryKey(resource) {
			retained = &options.RetainedResources[i]
			seenRetained[discoveryKey(resource)] = true
		}
	}
	if retained == nil && !discoveryAssociated(object, options, sets) {
		return
	}
	if !discoveryUsable(object) {
		result.blockDiscovery(resource, "ResourceIdentityUnusable")
		return
	}
	if retained != nil && retained.UID != resource.UID {
		result.blockDiscovery(resource, "RetainedIdentityChanged")
		return
	}
	if root, foreign := discoveryForeignRoot(object, options.ForeignRoots); foreign && retained == nil {
		result.Descendants = append(result.Descendants, DiscoveryDescendant{Resource: resource, Owner: root})
		return
	}
	if discoveryInvalidOpenBaoReference(object, options.Source) {
		result.blockDiscovery(resource, "UnsupportedOwnerReference")
		return
	}
	if owner, found := discoveryDescendant(object, sets); found {
		if retained != nil || discoveryDescendantConflict(object, owner) ||
			!discoveryDescendantAnnotation(object, owner, options.Source.UID, discoveryOtherUID(options)) {
			result.blockDiscovery(resource, "DescendantCustodyConflict")
			return
		}
		result.Descendants = append(result.Descendants, DiscoveryDescendant{Resource: resource, Owner: owner})
		return
	}
	if !supportedRootResource(options.Source, resource) {
		result.blockDiscovery(resource, "UnsupportedResource")
		return
	}
	if !ownershipProof(object, options.Source, resource, retained != nil) {
		code := "OwnershipProofIncompleteOrConflicting"
		if retained == nil && supportedRetainedResource(options.Source, resource) &&
			ownershipProof(object, options.Source, resource, true) {
			code = "RetainedCustodyUnconfirmed"
		}
		result.blockDiscovery(resource, code)
		return
	}
	result.Transfers = append(result.Transfers, TransferRequest{NamespaceUID: options.NamespaceUID,
		Source: options.Source, Destination: options.Destination, Resource: resource, Retained: retained != nil})
}

func discoveryAssociated(object *metav1.PartialObjectMetadata, options DiscoveryOptions,
	sets map[types.UID]TransferIdentity,
) bool {
	source := options.Source
	if object.Annotations[constants.AnnotationOpenBaoOwnerUID] == string(source.UID) ||
		(options.Destination.UID != "" &&
			object.Annotations[constants.AnnotationOpenBaoOwnerUID] == string(options.Destination.UID)) {
		return true
	}
	// A restore record is never associated by name: a label naming a Cluster
	// says nothing about a record that happens to share the name.
	if source.Kind == discoveryClusterKind && (object.Labels[constants.LabelOpenBaoCluster] == source.Name ||
		(object.Labels[constants.LabelAppInstance] == source.Name &&
			object.Labels[constants.LabelAppManagedBy] == constants.LabelValueAppManagedByOpenBaoOperator)) {
		return true
	}
	for _, ref := range object.OwnerReferences {
		_, descendant := sets[ref.UID]
		openBaoGroup := strings.HasPrefix(ref.APIVersion, "openbao.org/") ||
			strings.HasPrefix(ref.APIVersion, "kubebao.org/")
		if ref.UID == source.UID || (options.Destination.UID != "" && ref.UID == options.Destination.UID) || descendant ||
			(openBaoGroup && ref.Kind == source.Kind && ref.Name == source.Name) {
			return true
		}
	}
	return false
}

// discoveryForeignRoot reports the planned restore record that controls object.
func discoveryForeignRoot(object *metav1.PartialObjectMetadata, roots []TransferIdentity) (TransferIdentity, bool) {
	for _, root := range roots {
		if ownershipProof(object, root, discoveryIdentity(object), false) {
			return root, true
		}
	}
	return TransferIdentity{}, false
}

func discoveryInvalidOpenBaoReference(object metav1.Object, source TransferIdentity) bool {
	for _, ref := range object.GetOwnerReferences() {
		if ref.UID == "" {
			return true
		}
		if strings.HasPrefix(ref.APIVersion, "openbao.org/") || strings.HasPrefix(ref.APIVersion, "kubebao.org/") {
			if ref.APIVersion != source.APIVersion || ref.Kind != source.Kind || ref.Name != source.Name ||
				ref.UID != source.UID || ref.Controller == nil || !*ref.Controller {
				return true
			}
		}
	}
	return false
}

func discoveryDescendant(object *metav1.PartialObjectMetadata,
	sets map[types.UID]TransferIdentity,
) (TransferIdentity, bool) {
	coreChild := object.APIVersion == "v1" && (object.Kind == "Pod" || object.Kind == discoveryPVCKind)
	revision := object.APIVersion == discoveryAppsVersion && object.Kind == discoveryRevisionKind
	if !coreChild && !revision {
		return TransferIdentity{}, false
	}
	for _, ref := range object.OwnerReferences {
		owner, found := sets[ref.UID]
		if found && ref.APIVersion == owner.APIVersion && ref.Kind == owner.Kind && ref.Name == owner.Name &&
			(object.Kind == discoveryPVCKind || (ref.Controller != nil && *ref.Controller)) {
			return owner, true
		}
	}
	return TransferIdentity{}, false
}

// StatefulSet newRevision copies the set's annotations at creation. The old
// owner-uid on a ControllerRevision is historical metadata, not current retained
// resource custody. Only the exact single StatefulSet controller reference can
// qualify this exception; no OpenBaoCluster owner reference is permitted.
func discoveryHistoricalRevision(object *metav1.PartialObjectMetadata, owner TransferIdentity) bool {
	if object.APIVersion != discoveryAppsVersion || object.Kind != discoveryRevisionKind ||
		owner.APIVersion != discoveryAppsVersion || owner.Kind != discoveryStatefulSetKind ||
		object.Namespace != owner.Namespace || len(object.OwnerReferences) != 1 {
		return false
	}
	ref := object.OwnerReferences[0]
	return ref.APIVersion == owner.APIVersion && ref.Kind == owner.Kind && ref.Name == owner.Name &&
		ref.UID == owner.UID && ref.Controller != nil && *ref.Controller
}

func discoveryDescendantAnnotation(object *metav1.PartialObjectMetadata, owner TransferIdentity,
	active, other types.UID,
) bool {
	annotation := object.Annotations[constants.AnnotationOpenBaoOwnerUID]
	return annotation == "" || annotation == string(active) ||
		(annotation == string(other) && discoveryHistoricalRevision(object, owner))
}

func discoveryOtherUID(options DiscoveryOptions) types.UID {
	if options.Destination.UID != "" {
		return options.Destination.UID
	}
	return options.RetiredUID
}

func discoveryDescendantConflict(object *metav1.PartialObjectMetadata, owner TransferIdentity) bool {
	for _, ref := range object.OwnerReferences {
		if strings.HasPrefix(ref.APIVersion, "openbao.org/") || strings.HasPrefix(ref.APIVersion, "kubebao.org/") {
			return true
		}
		matches := ref.APIVersion == owner.APIVersion && ref.Kind == owner.Kind &&
			ref.Name == owner.Name && ref.UID == owner.UID
		if !matches && ((ref.Controller != nil && *ref.Controller && object.Kind != discoveryPVCKind) ||
			ref.Kind == discoveryStatefulSetKind) {
			return true
		}
	}
	return false
}

func discoveryUsable(object metav1.Object) bool {
	return object.GetName() != "" && object.GetUID() != "" && object.GetResourceVersion() != "" &&
		object.GetDeletionTimestamp().IsZero()
}

func discoveryIdentity(object *metav1.PartialObjectMetadata) TransferIdentity {
	return TransferIdentity{APIVersion: object.APIVersion, Kind: object.Kind, Namespace: object.Namespace,
		Name: object.Name, UID: object.UID}
}

func discoveryKey(identity TransferIdentity) string {
	return identity.APIVersion + "/" + identity.Kind + "/" + identity.Namespace + "/" + identity.Name
}

func (result *ManagedDiscovery) blockDiscovery(resource TransferIdentity, code string) {
	result.Blockers = append(result.Blockers, DiscoveryBlocker{Resource: resource, Code: code})
}
