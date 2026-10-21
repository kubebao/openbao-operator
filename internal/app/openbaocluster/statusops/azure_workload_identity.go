package statusops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	workloadsvc "github.com/kubebao/openbao-operator/internal/service/workload"
)

// EvaluateAzureWorkloadIdentityStatefulSets reports every existing StatefulSet whose Azure
// Key Vault workload identity token projection disagrees with the cluster spec. The
// projection lives in admission-locked pod-template fields, so adding or removing the
// identity in place needs those StatefulSets recreated. It returns false when every
// StatefulSet matches.
func EvaluateAzureWorkloadIdentityStatefulSets(
	ctx context.Context,
	reader client.Reader,
	cluster *openbaov1alpha1.OpenBaoCluster,
) (ConditionResult, bool, error) {
	if reader == nil || cluster == nil {
		return ConditionResult{}, false, nil
	}

	var list appsv1.StatefulSetList
	if err := reader.List(
		ctx,
		&list,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(resourceidentity.Labels(cluster)),
	); err != nil {
		return ConditionResult{}, false, err
	}

	var held []string
	for i := range list.Items {
		sts := &list.Items[i]
		if workloadsvc.StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster, sts) {
			held = append(held, cluster.Namespace+"/"+sts.Name)
		}
	}
	if len(held) == 0 {
		return ConditionResult{}, false, nil
	}
	sort.Strings(held)
	return ConditionResult{
		Status: metav1.ConditionFalse,
		Reason: constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired,
		Message: fmt.Sprintf(
			"Recreate these StatefulSets so their locked pod-template fields match the requested Azure Key Vault workload identity token projection: %s",
			strings.Join(held, ", "),
		),
	}, true, nil
}
