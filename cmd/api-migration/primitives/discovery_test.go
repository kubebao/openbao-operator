package primitives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	discoveryTestForeign     = "foreign"
	discoveryTestOther       = "other"
	discoveryTestOtherUID    = "other-uid"
	discoveryTestReplacement = "replacement"
	discoveryTestDestination = "destination-uid"
)

func discoveryTestOptions() DiscoveryOptions {
	return DiscoveryOptions{NamespaceUID: "namespace-uid",
		Source: TransferIdentity{APIVersion: PlanSourceAPIVersion, Kind: "OpenBaoCluster",
			Namespace: "tenant", Name: "bao", UID: "source-uid"},
		Destination: TransferIdentity{APIVersion: PlanDestinationAPIVersion, Kind: "OpenBaoCluster",
			Namespace: "tenant", Name: "bao", UID: discoveryTestDestination}}
}

func discoveryTestMetadata(apiVersion, kind, name string) metav1.PartialObjectMetadata {
	return metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: apiVersion, Kind: kind},
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: name, UID: types.UID(name + "-uid"),
			ResourceVersion: "4"}}
}

func discoveryTestManaged(apiVersion, kind, name string) metav1.PartialObjectMetadata {
	object := discoveryTestMetadata(apiVersion, kind, name)
	object.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: "source-uid"}
	object.OwnerReferences = []metav1.OwnerReference{{APIVersion: PlanSourceAPIVersion, Kind: "OpenBaoCluster",
		Name: "bao", UID: "source-uid", Controller: ptr.To(true)}}
	return object
}

type discoveryTestReader struct {
	objects  []metav1.PartialObjectMetadata
	getHook  func(client.ObjectKey, *metav1.PartialObjectMetadata) error
	listHook func(*metav1.PartialObjectMetadataList, client.ListOptions) error
	gets     int
	lists    int
}

func (reader *discoveryTestReader) Get(_ context.Context, key client.ObjectKey,
	object client.Object, _ ...client.GetOption,
) error {
	metadata, ok := object.(*metav1.PartialObjectMetadata)
	if !ok {
		return fmt.Errorf("discovery fetched a resource payload: %T", object)
	}
	reader.gets++
	metadata.Name, metadata.Namespace = key.Name, key.Namespace
	metadata.ResourceVersion = "1"
	switch metadata.APIVersion + "/" + metadata.Kind {
	case "v1/Namespace":
		metadata.UID = "namespace-uid"
	case PlanSourceAPIVersion + "/OpenBaoCluster":
		metadata.UID = "source-uid"
	case PlanDestinationAPIVersion + "/OpenBaoCluster":
		metadata.UID = discoveryTestDestination
	case PlanSourceAPIVersion + "/OpenBaoRestore":
		metadata.UID = "restore-source-uid"
	case PlanDestinationAPIVersion + "/OpenBaoRestore":
		metadata.UID = "restore-destination-uid"
	default:
		return fmt.Errorf("unexpected individual resource read")
	}
	if reader.getHook != nil {
		return reader.getHook(key, metadata)
	}
	return nil
}

func (reader *discoveryTestReader) List(_ context.Context, list client.ObjectList,
	options ...client.ListOption,
) error {
	metadata, ok := list.(*metav1.PartialObjectMetadataList)
	if !ok {
		return fmt.Errorf("discovery listed resource payloads: %T", list)
	}
	reader.lists++
	settings := client.ListOptions{}
	settings.ApplyOptions(options)
	if settings.Namespace != "tenant" || settings.Limit != 500 || settings.LabelSelector != nil {
		return fmt.Errorf("discovery must inspect bounded pages without hiding unlabelled ownership")
	}
	if reader.listHook != nil {
		return reader.listHook(metadata, settings)
	}
	for i := range reader.objects {
		object := &reader.objects[i]
		if object.APIVersion == metadata.APIVersion && object.Kind+"List" == metadata.Kind {
			metadata.Items = append(metadata.Items, *object.DeepCopy())
		}
	}
	return nil
}

func TestDiscoverManagedResourcesMetadataOnlyAndDeterministic(t *testing.T) {
	options := discoveryTestOptions()
	var objects []metav1.PartialObjectMetadata
	for _, kind := range discoveryTypes {
		identity := TransferIdentity{APIVersion: kind.apiVersion, Kind: kind.kind}
		if supportedTransferResource(identity) {
			objects = append(objects, discoveryTestManaged(kind.apiVersion, kind.kind, strings.ToLower(kind.kind)))
		}
	}
	retained := discoveryTestManaged("v1", "Secret", "retained")
	retained.OwnerReferences = nil
	retained.Annotations["example.org/credential"] = "DO-NOT-EXPORT"
	objects = append(objects, retained)
	options.RetainedResources = []TransferIdentity{discoveryIdentity(&retained)}
	reader := &discoveryTestReader{objects: objects}
	result, err := DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Transfers, 18)
	require.Len(t, result.Scope, len(discoveryTypes))
	require.NotEmpty(t, result.Limitations)
	require.Equal(t, 6, reader.gets, "the namespace and both roots must be checked before and after listing")
	for _, transfer := range result.Transfers {
		require.NoError(t, validateTransferRequest(transfer))
		require.Equal(t, transfer.Resource.Name == "retained", transfer.Retained)
	}
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "DO-NOT-EXPORT")
	require.NotContains(t, string(encoded), "annotations")
	slices.Reverse(reader.objects)
	repeated, err := DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Equal(t, result, repeated)
}

func TestDiscoveryPreservesStatefulSetDescendants(t *testing.T) {
	set := discoveryTestManaged(discoveryAppsVersion, discoveryStatefulSetKind, "bao")
	objects := make([]metav1.PartialObjectMetadata, 0, 4)
	objects = append(objects, set)
	for _, kind := range []string{"Pod", "PersistentVolumeClaim", "ControllerRevision"} {
		version := "v1"
		if kind == "ControllerRevision" {
			version = discoveryAppsVersion
		}
		object := discoveryTestMetadata(version, kind, strings.ToLower(kind))
		object.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: "source-uid"}
		object.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: discoveryAppsVersion, Kind: discoveryStatefulSetKind, Name: set.Name,
			UID: set.UID, Controller: ptr.To(kind != "PersistentVolumeClaim")}}
		objects = append(objects, object)
	}
	result, err := DiscoverManagedResources(t.Context(), &discoveryTestReader{objects: objects}, discoveryTestOptions())
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Transfers, 1)
	require.Len(t, result.Descendants, 3)
	for _, descendant := range result.Descendants {
		require.Equal(t, discoveryIdentity(&set), descendant.Owner)
	}
}

func TestDiscoveryRejectsIncompleteConflictingAndUnsupportedProof(t *testing.T) {
	cases := map[string]struct {
		edit func(*metav1.PartialObjectMetadata)
		code string
	}{
		"missing annotation": {func(o *metav1.PartialObjectMetadata) { o.Annotations = nil },
			"OwnershipProofIncompleteOrConflicting"},
		"wrong annotation": {func(o *metav1.PartialObjectMetadata) {
			o.Annotations[constants.AnnotationOpenBaoOwnerUID] = discoveryTestForeign
		}, "OwnershipProofIncompleteOrConflicting"},
		"wrong owner UID": {func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].UID = discoveryTestForeign },
			"UnsupportedOwnerReference"},
		"missing owner UID": {func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].UID = "" },
			"UnsupportedOwnerReference"},
		"wrong owner name": {func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].Name = discoveryTestForeign },
			"UnsupportedOwnerReference"},
		"unsupported old API": {func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences[0].APIVersion = "openbao.org/v1beta1"
		}, "UnsupportedOwnerReference"},
		"already transferred": {func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences[0].APIVersion = PlanDestinationAPIVersion
			o.OwnerReferences[0].UID = discoveryTestDestination
			o.Annotations[constants.AnnotationOpenBaoOwnerUID] = discoveryTestDestination
		}, "UnsupportedOwnerReference"},
		"noncontroller old ref": {func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences[0].Controller = ptr.To(false)
		}, "UnsupportedOwnerReference"},
		"additional obsolete ref": {func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences = append(o.OwnerReferences, metav1.OwnerReference{
				APIVersion: PlanSourceAPIVersion, Kind: "OpenBaoRestore", Name: "old", UID: "old-uid"})
		}, "UnsupportedOwnerReference"},
		"annotation only secret": {func(o *metav1.PartialObjectMetadata) { o.OwnerReferences = nil },
			"RetainedCustodyUnconfirmed"},
		"label only": {func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences, o.Annotations = nil, nil
			o.Labels = map[string]string{constants.LabelOpenBaoCluster: "bao"}
		}, "OwnershipProofIncompleteOrConflicting"},
		"deleting": {func(o *metav1.PartialObjectMetadata) { o.DeletionTimestamp = ptr.To(metav1.Now()) },
			"ResourceIdentityUnusable"},
		"missing resource UID": {func(o *metav1.PartialObjectMetadata) { o.UID = "" }, "ResourceIdentityUnusable"},
		"missing revision": {func(o *metav1.PartialObjectMetadata) { o.ResourceVersion = "" },
			"ResourceIdentityUnusable"},
		"root-owned Pod": {func(o *metav1.PartialObjectMetadata) { o.APIVersion, o.Kind = "v1", "Pod" },
			"UnsupportedResource"},
		"root-owned ControllerRevision": {func(o *metav1.PartialObjectMetadata) {
			o.APIVersion, o.Kind = "apps/v1", "ControllerRevision"
		}, "UnsupportedResource"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			object := discoveryTestManaged("v1", "Secret", "bao")
			tc.edit(&object)
			reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{object}}
			result, err := DiscoverManagedResources(t.Context(), reader, discoveryTestOptions())
			require.NoError(t, err)
			require.Empty(t, result.Transfers)
			require.Empty(t, result.Descendants)
			require.Equal(t, []DiscoveryBlocker{{Resource: discoveryIdentity(&object), Code: tc.code}}, result.Blockers)
		})
	}
}

func TestDiscoveryNeverAdoptsNamesOrUnrelatedResources(t *testing.T) {
	nameOnly := discoveryTestMetadata("v1", "Secret", "bao")
	other := discoveryTestManaged("v1", "Secret", discoveryTestOther)
	other.OwnerReferences[0].UID, other.OwnerReferences[0].Name = discoveryTestOtherUID, discoveryTestOther
	other.Annotations[constants.AnnotationOpenBaoOwnerUID] = discoveryTestOtherUID
	result, err := DiscoverManagedResources(t.Context(),
		&discoveryTestReader{objects: []metav1.PartialObjectMetadata{nameOnly, other}}, discoveryTestOptions())
	require.NoError(t, err)
	require.Empty(t, result.Transfers)
	require.Empty(t, result.Blockers)
}

func TestDiscoveryPinsRetainedNominations(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			retained := discoveryTestManaged("v1", "PersistentVolumeClaim", "data")
			retained.OwnerReferences = nil
			options := discoveryTestOptions()
			options.RetainedResources = []TransferIdentity{discoveryIdentity(&retained)}
			reader := &discoveryTestReader{}
			code := "RetainedResourceMissing"
			if changed {
				retained.UID = discoveryTestReplacement
				reader.objects = []metav1.PartialObjectMetadata{retained}
				code = "RetainedIdentityChanged"
			}
			result, err := DiscoverManagedResources(t.Context(), reader, options)
			require.NoError(t, err)
			require.Empty(t, result.Transfers)
			require.Len(t, result.Blockers, 1)
			require.Equal(t, code, result.Blockers[0].Code)
		})
	}
}

func TestDiscoveryRejectsConflictingDescendantCustody(t *testing.T) {
	for name, edit := range map[string]func(*metav1.PartialObjectMetadata, *DiscoveryOptions){
		"nominated as retained": func(o *metav1.PartialObjectMetadata, options *DiscoveryOptions) {
			options.RetainedResources = []TransferIdentity{discoveryIdentity(o)}
		},
		"foreign custody annotation": func(o *metav1.PartialObjectMetadata, _ *DiscoveryOptions) {
			o.Annotations[constants.AnnotationOpenBaoOwnerUID] = discoveryTestForeign
		},
		"foreign StatefulSet reference": func(o *metav1.PartialObjectMetadata, _ *DiscoveryOptions) {
			o.OwnerReferences = append(o.OwnerReferences, metav1.OwnerReference{
				APIVersion: discoveryAppsVersion, Kind: discoveryStatefulSetKind,
				Name: discoveryTestForeign, UID: discoveryTestForeign})
		},
		"direct Cluster reference": func(o *metav1.PartialObjectMetadata, _ *DiscoveryOptions) {
			o.OwnerReferences = append(o.OwnerReferences, discoveryTestManaged("v1", "Secret", "x").OwnerReferences...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			options := discoveryTestOptions()
			set := discoveryTestManaged(discoveryAppsVersion, discoveryStatefulSetKind, "bao")
			claim := discoveryTestMetadata("v1", "PersistentVolumeClaim", "data")
			claim.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: "source-uid"}
			claim.OwnerReferences = []metav1.OwnerReference{{APIVersion: discoveryAppsVersion, Kind: discoveryStatefulSetKind,
				Name: set.Name, UID: set.UID}}
			edit(&claim, &options)
			result, err := DiscoverManagedResources(t.Context(),
				&discoveryTestReader{objects: []metav1.PartialObjectMetadata{set, claim}}, options)
			require.NoError(t, err)
			require.Len(t, result.Transfers, 1)
			require.Empty(t, result.Descendants)
			require.Equal(t, []DiscoveryBlocker{{Resource: discoveryIdentity(&claim), Code: "DescendantCustodyConflict"}},
				result.Blockers)
		})
	}
}

func TestDiscoveryInputValidation(t *testing.T) {
	for name, edit := range map[string]func(*DiscoveryOptions){
		"namespace pin missing": func(o *DiscoveryOptions) { o.NamespaceUID = "" },
		"wrong namespace":       func(o *DiscoveryOptions) { o.Destination.Namespace = discoveryTestOther },
		"wrong group":           func(o *DiscoveryOptions) { o.Source.APIVersion = PlanDestinationAPIVersion },
		"same UID":              func(o *DiscoveryOptions) { o.Source.UID = o.Destination.UID },
		"wrong kind":            func(o *DiscoveryOptions) { o.Source.Kind = "OpenBaoTenant" },
		"invalid name":          func(o *DiscoveryOptions) { o.Source.Name = "invalid/name" },
		"retained wrong kind": func(o *DiscoveryOptions) {
			resource := discoveryTestManaged("v1", "ConfigMap", "config")
			o.RetainedResources = []TransferIdentity{discoveryIdentity(&resource)}
		},
		"retained duplicate": func(o *DiscoveryOptions) {
			resource := discoveryTestManaged("v1", "Secret", "secret")
			o.RetainedResources = []TransferIdentity{discoveryIdentity(&resource), discoveryIdentity(&resource)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			options := discoveryTestOptions()
			edit(&options)
			reader := &discoveryTestReader{}
			_, err := DiscoverManagedResources(t.Context(), reader, options)
			require.Error(t, err)
			require.Zero(t, reader.gets)
			require.Zero(t, reader.lists)
		})
	}
	_, err := DiscoverManagedResources(t.Context(), nil, discoveryTestOptions())
	require.Error(t, err)
	var typedNil *discoveryTestReader
	_, err = DiscoverManagedResources(t.Context(), typedNil, discoveryTestOptions())
	require.Error(t, err)
}

func TestDiscoveryRootPinsAndNoPayloadFallback(t *testing.T) {
	for name, hook := range map[string]func(client.ObjectKey, *metav1.PartialObjectMetadata) error{
		"namespace replaced": func(_ client.ObjectKey, o *metav1.PartialObjectMetadata) error {
			if o.Kind == "Namespace" {
				o.UID = discoveryTestReplacement
			}
			return nil
		},
		"source replaced": func(_ client.ObjectKey, o *metav1.PartialObjectMetadata) error {
			if o.APIVersion == PlanSourceAPIVersion {
				o.UID = discoveryTestReplacement
			}
			return nil
		},
		"destination deleting": func(_ client.ObjectKey, o *metav1.PartialObjectMetadata) error {
			if o.APIVersion == PlanDestinationAPIVersion {
				o.DeletionTimestamp = ptr.To(metav1.Now())
			}
			return nil
		},
		"read denied": func(_ client.ObjectKey, _ *metav1.PartialObjectMetadata) error { return errors.New("denied") },
	} {
		t.Run(name, func(t *testing.T) {
			reader := &discoveryTestReader{getHook: hook}
			result, err := DiscoverManagedResources(t.Context(), reader, discoveryTestOptions())
			require.Error(t, err)
			require.Nil(t, result)
			require.Zero(t, reader.lists)
		})
	}
	reader := &discoveryTestReader{}
	reader.getHook = func(_ client.ObjectKey, o *metav1.PartialObjectMetadata) error {
		if reader.gets > 3 {
			o.UID = "replacement-after-scan"
		}
		return nil
	}
	result, err := DiscoverManagedResources(t.Context(), reader, discoveryTestOptions())
	require.Error(t, err)
	require.Nil(t, result)
	require.Positive(t, reader.lists)
}

func TestDiscoveryOptionalAPIAndListFailures(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("optional missing=%t", missing), func(t *testing.T) {
			reader := &discoveryTestReader{listHook: func(list *metav1.PartialObjectMetadataList,
				_ client.ListOptions,
			) error {
				if list.Kind != "HTTPRouteList" {
					return nil
				}
				if missing {
					return &meta.NoKindMatchError{GroupKind: schema.GroupKind{
						Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}}
				}
				return apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io",
					Resource: "httproutes"}, "", errors.New("denied"))
			}}
			result, err := DiscoverManagedResources(t.Context(), reader, discoveryTestOptions())
			if !missing {
				require.Error(t, err)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			for _, kind := range result.Scope {
				require.Equal(t, kind.Kind != "HTTPRoute", kind.Available)
			}
		})
	}
}

func TestDiscoveryPaginationAndRepeatedToken(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeated=%t", repeated), func(t *testing.T) {
			reader := &discoveryTestReader{listHook: func(list *metav1.PartialObjectMetadataList,
				settings client.ListOptions,
			) error {
				if list.Kind != "SecretList" {
					return nil
				}
				if settings.Continue == "" {
					list.Items = []metav1.PartialObjectMetadata{discoveryTestManaged("v1", "Secret", "first")}
					list.Continue = "next"
				} else {
					list.Items = []metav1.PartialObjectMetadata{discoveryTestManaged("v1", "Secret", "second")}
					if repeated {
						list.Continue = "next"
					}
				}
				return nil
			}}
			result, err := DiscoverManagedResources(t.Context(), reader, discoveryTestOptions())
			if repeated {
				require.ErrorContains(t, err, "repeated a continuation token")
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Transfers, 2)
		})
	}
}

func TestDiscoverySupportsReverseHandover(t *testing.T) {
	options := discoveryTestOptions()
	options.Source, options.Destination = options.Destination, options.Source
	set := discoveryTestManaged(discoveryAppsVersion, discoveryStatefulSetKind, "bao")
	set.OwnerReferences[0].APIVersion = options.Source.APIVersion
	set.OwnerReferences[0].UID = options.Source.UID
	set.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(options.Source.UID)
	secret := discoveryTestMetadata("v1", "Secret", "retained")
	secret.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: string(options.Source.UID)}
	options.RetainedResources = []TransferIdentity{discoveryIdentity(&secret)}
	pod := discoveryTestMetadata("v1", "Pod", "bao-0")
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: set.APIVersion, Kind: set.Kind,
		Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}
	reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{pod, secret, set}}
	result, err := DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Transfers, 2)
	require.Len(t, result.Descendants, 1)
	for _, transfer := range result.Transfers {
		require.Equal(t, options.Source, transfer.Source)
		require.Equal(t, options.Destination, transfer.Destination)
		require.NoError(t, validateTransferRequest(transfer))
	}
	set.OwnerReferences[0].APIVersion = options.Destination.APIVersion
	set.OwnerReferences[0].UID = options.Destination.UID
	set.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(options.Destination.UID)
	reader.objects = []metav1.PartialObjectMetadata{secret, set}
	result, err = DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Len(t, result.Blockers, 1, "reverse discovery cannot adopt an already returned object")
}

func TestDiscoveryControllerRevisionPreservesHistoricalProvenance(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			options := discoveryTestOptions()
			if reverse {
				options.Source, options.Destination = options.Destination, options.Source
			}
			set := discoveryTestManaged(discoveryAppsVersion, discoveryStatefulSetKind, "bao")
			set.OwnerReferences[0].APIVersion = options.Source.APIVersion
			set.OwnerReferences[0].UID = options.Source.UID
			set.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(options.Source.UID)
			for _, provenance := range []types.UID{"", options.Source.UID, options.Destination.UID} {
				revision := discoveryTestMetadata(discoveryAppsVersion, discoveryRevisionKind, "bao-history")
				revision.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: string(provenance)}
				revision.OwnerReferences = []metav1.OwnerReference{{APIVersion: set.APIVersion, Kind: set.Kind,
					Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}
				reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{set, revision}}
				before := revision.DeepCopy()
				result, err := DiscoverManagedResources(t.Context(), reader, options)
				require.NoError(t, err)
				require.Empty(t, result.Blockers)
				require.Len(t, result.Transfers, 1)
				require.Equal(t, []DiscoveryDescendant{{Resource: discoveryIdentity(&revision),
					Owner: discoveryIdentity(&set)}}, result.Descendants)
				require.Equal(t, *before, reader.objects[1], "discovery must preserve historical metadata")
			}
		})
	}
}

func TestDiscoveryHistoricalProvenanceRequiresExactRevisionController(t *testing.T) {
	for name, edit := range map[string]func(*metav1.PartialObjectMetadata){
		"foreign annotation": func(o *metav1.PartialObjectMetadata) {
			o.Annotations[constants.AnnotationOpenBaoOwnerUID] = discoveryTestForeign
		},
		"different owner UID":  func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].UID = discoveryTestForeign },
		"different owner name": func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].Name = discoveryTestForeign },
		"different owner kind": func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].Kind = discoveryJobKind },
		"noncontroller owner":  func(o *metav1.PartialObjectMetadata) { o.OwnerReferences[0].Controller = ptr.To(false) },
		"extra owner": func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences = append(o.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap",
				Name: discoveryTestForeign, UID: discoveryTestForeign})
		},
		"old CR reference": func(o *metav1.PartialObjectMetadata) {
			o.OwnerReferences = append(o.OwnerReferences, discoveryTestManaged("v1", "Secret", "x").OwnerReferences...)
		},
		"Pod provenance": func(o *metav1.PartialObjectMetadata) { o.APIVersion, o.Kind = "v1", "Pod" },
		"PVC provenance": func(o *metav1.PartialObjectMetadata) { o.APIVersion, o.Kind = "v1", discoveryPVCKind },
	} {
		t.Run(name, func(t *testing.T) {
			options := discoveryTestOptions()
			set := discoveryTestManaged(discoveryAppsVersion, discoveryStatefulSetKind, "bao")
			revision := discoveryTestMetadata(discoveryAppsVersion, discoveryRevisionKind, "bao-history")
			revision.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: string(options.Destination.UID)}
			revision.OwnerReferences = []metav1.OwnerReference{{APIVersion: set.APIVersion, Kind: set.Kind,
				Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}
			edit(&revision)
			reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{set, revision}}
			result, err := DiscoverManagedResources(t.Context(), reader, options)
			require.NoError(t, err)
			require.Len(t, result.Transfers, 1)
			require.Empty(t, result.Descendants)
			require.Len(t, result.Blockers, 1)
		})
	}
}

func TestDiscoveryPreviewBeforeDestinationExists(t *testing.T) {
	options := discoveryTestOptions()
	options.Destination.UID = ""
	retained := discoveryTestManaged("v1", "Secret", "retained")
	retained.OwnerReferences = nil
	options.RetainedResources = []TransferIdentity{discoveryIdentity(&retained)}
	reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{
		discoveryTestManaged("v1", "ConfigMap", "config"), retained}}
	result, err := DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Transfers, 2)
	require.Equal(t, 4, reader.gets, "a preview checks the namespace and source roots only")
	for _, transfer := range result.Transfers {
		require.Error(t, validateTransferRequest(transfer), "a preview transfer must not be executable")
	}
}

func TestDiscoveryPreviewAcceptsRetiredOwnerRevisions(t *testing.T) {
	options := discoveryTestOptions()
	options.Source, options.Destination = options.Destination, options.Source
	options.Destination.UID = ""
	set := discoveryTestManaged(discoveryAppsVersion, discoveryStatefulSetKind, "bao")
	set.OwnerReferences[0].APIVersion, set.OwnerReferences[0].UID = options.Source.APIVersion, options.Source.UID
	set.Annotations[constants.AnnotationOpenBaoOwnerUID] = string(options.Source.UID)
	revision := discoveryTestMetadata(discoveryAppsVersion, discoveryRevisionKind, "bao-history")
	revision.Annotations = map[string]string{constants.AnnotationOpenBaoOwnerUID: "source-uid"}
	revision.OwnerReferences = []metav1.OwnerReference{{APIVersion: set.APIVersion, Kind: set.Kind,
		Name: set.Name, UID: set.UID, Controller: ptr.To(true)}}
	reader := &discoveryTestReader{objects: []metav1.PartialObjectMetadata{set, revision}}

	result, err := DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Len(t, result.Blockers, 1, "an unknown historical owner is a blocker")

	options.RetiredUID = "source-uid"
	result, err = DiscoverManagedResources(t.Context(), reader, options)
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Descendants, 1)
}

func TestDiscoveryMovesLifecycleJobsWithTheirPods(t *testing.T) {
	job := discoveryTestManaged(testBatchAPIVersion, discoveryJobKind, "bao-backup-1")
	pod := discoveryTestMetadata("v1", "Pod", "bao-backup-1-x7k2")
	pod.Labels = map[string]string{constants.LabelOpenBaoCluster: "bao"}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: testBatchAPIVersion, Kind: discoveryJobKind,
		Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}
	result, err := DiscoverManagedResources(t.Context(), &discoveryTestReader{
		objects: []metav1.PartialObjectMetadata{job, pod}}, discoveryTestOptions())
	require.NoError(t, err)
	require.Empty(t, result.Blockers)
	require.Len(t, result.Transfers, 1)
	require.Equal(t, discoveryJobKind, result.Transfers[0].Resource.Kind)
	require.Equal(t, []DiscoveryDescendant{{Resource: discoveryIdentity(&pod), Owner: discoveryIdentity(&job)}},
		result.Descendants)

	unowned := discoveryTestManaged(testBatchAPIVersion, discoveryJobKind, "unowned")
	unowned.OwnerReferences = nil
	result, err = DiscoverManagedResources(t.Context(), &discoveryTestReader{
		objects: []metav1.PartialObjectMetadata{unowned}}, discoveryTestOptions())
	require.NoError(t, err)
	require.Len(t, result.Blockers, 1, "a Job without controller proof is never adopted")
}
