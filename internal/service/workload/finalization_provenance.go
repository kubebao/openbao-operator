package workload

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

// backfillFinalizingStatefulSetProvenance migrates legacy controller-owned
// StatefulSets before deletion. Finalization requeues after the metadata write;
// all subsequent deletion decisions still require full managed owner proof.
func (m *Manager) backfillFinalizingStatefulSetProvenance(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, observed *appsv1.StatefulSet) (bool, error) {
	if _, present := observed.Annotations[constants.AnnotationOpenBaoOwnerUID]; present {
		return false, nil
	}
	set := &appsv1.StatefulSet{}
	if err := m.reader.Get(ctx, client.ObjectKeyFromObject(observed), set); err != nil {
		return false, client.IgnoreNotFound(fmt.Errorf("read legacy StatefulSet for finalization: %w", err))
	}
	current, err := m.readFinalizingCluster(ctx, cluster)
	if err != nil {
		return false, err
	}
	// Validate the exact controller reference before stamping provenance. Never
	// repair conflicting provenance or adopt a replacement with the same name.
	proof := set.DeepCopy()
	if err := resourceownership.SetOwnerUIDAnnotation(proof, current); err != nil {
		return false, err
	}
	if set.UID != observed.UID || set.UID == "" || set.ResourceVersion == "" ||
		set.Labels[constants.LabelAppManagedBy] != constants.LabelValueAppManagedByOpenBaoOperator ||
		set.Labels[constants.LabelOpenBaoCluster] != current.Name ||
		!resourceownership.HasManagedControllerOwnerProof(proof, current, openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster")) {
		return false, fmt.Errorf("StatefulSet %s/%s lacks exact cluster ownership for provenance migration", set.Namespace, set.Name)
	}
	if ownerUID, present := set.Annotations[constants.AnnotationOpenBaoOwnerUID]; present {
		if ownerUID != string(current.UID) {
			return false, fmt.Errorf("StatefulSet %s/%s has conflicting owner UID provenance", set.Namespace, set.Name)
		}
		return false, nil
	}
	annotationPath := "/metadata/annotations/openbao.org~1owner-uid"
	var annotationValue any = string(current.UID)
	if len(set.Annotations) == 0 {
		annotationPath = "/metadata/annotations"
		annotationValue = map[string]string{constants.AnnotationOpenBaoOwnerUID: string(current.UID)}
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(set.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": set.ResourceVersion},
		{"op": "add", "path": annotationPath, "value": annotationValue},
	})
	if err != nil {
		return false, fmt.Errorf("encode StatefulSet provenance patch: %w", err)
	}
	if err := m.client.Patch(ctx, set, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		return false, fmt.Errorf("stamp legacy StatefulSet provenance: %w", err)
	}
	return true, nil
}
