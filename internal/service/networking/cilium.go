package networking

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

func ciliumAPIServerEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster.Spec.Network != nil && cluster.Spec.Network.APIServerEgress != nil
}

func newCiliumAPIServerPolicy(cluster *openbaov1alpha1.OpenBaoCluster) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("cilium.io/v2")
	obj.SetKind("CiliumNetworkPolicy")
	obj.SetNamespace(cluster.Namespace)
	obj.SetName(cluster.Name + "-api-server")
	return obj
}

func buildCiliumAPIServerPolicy(cluster *openbaov1alpha1.OpenBaoCluster) (*unstructured.Unstructured, error) {
	config := cluster.Spec.Network.APIServerEgress
	if config.Mode != "Cilium" || config.Port < 0 || config.Port > 65535 {
		return nil, operatorerrors.WrapPermanentConfig(fmt.Errorf("%w: spec.network.apiServerEgress requires mode Cilium and a TCP port from 1 to 65535", ErrAPIServerNetworkConfigurationInvalid))
	}
	port := config.Port
	if port == 0 {
		port = 6443
	}
	obj := newCiliumAPIServerPolicy(cluster)
	obj.SetLabels(resourceidentity.Labels(cluster))
	obj.Object["spec"] = map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{constants.LabelOpenBaoCluster: cluster.Name},
		},
		"enableDefaultDeny": map[string]interface{}{"ingress": false, "egress": false},
		"egress": []interface{}{map[string]interface{}{
			"toEntities": []interface{}{"kube-apiserver"},
			"toPorts": []interface{}{map[string]interface{}{
				"ports": []interface{}{map[string]interface{}{"port": strconv.Itoa(int(port)), "protocol": "TCP"}},
			}},
		}},
	}
	return obj, nil
}

func (m *Manager) ensureCiliumAPIServerPolicy(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	obj := newCiliumAPIServerPolicy(cluster)
	err := reconcileOptionalResource(ctx, optionalResourceOptions{
		kind: "CiliumNetworkPolicy", apiVersion: "cilium.io/v2",
		enabled: ciliumAPIServerEnabled(cluster), name: client.ObjectKeyFromObject(obj), owner: cluster,
		logger: logger, deleteDisabledMsg: "Cilium API server egress disabled; deleting owned policy",
		newEmpty: func() client.Object { return newCiliumAPIServerPolicy(cluster) },
		buildDesired: func() (client.Object, bool, error) {
			desired, err := buildCiliumAPIServerPolicy(cluster)
			return desired, true, err
		},
		get: m.reader.Get,
		delete: func(ctx context.Context, obj client.Object, _ ...client.DeleteOption) error {
			// Refuse to delete a replacement created after the ownership check.
			uid, rv := obj.GetUID(), obj.GetResourceVersion()
			return m.client.Delete(ctx, obj, client.Preconditions{UID: &uid, ResourceVersion: &rv})
		},
		apply: func(ctx context.Context, obj client.Object) error { return m.applyResource(ctx, obj, cluster) },
	})
	if err != nil && (operatorerrors.IsCRDMissingError(err) ||
		(apierrors.IsForbidden(err) && ciliumAPIServerEnabled(cluster))) {
		return operatorerrors.WrapPermanentConfig(fmt.Errorf("%w: cannot manage the namespaced Cilium API server policy; install the CiliumNetworkPolicy API and grant the operator get/create/patch/delete permissions required by spec.network.apiServerEgress: %w", ErrAPIServerNetworkConfigurationInvalid, err))
	}
	return err
}

// This validates the applied configuration, not dataplane connectivity.
func (m *Manager) evaluateCiliumAPIServerPolicy(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) APIServerNetworkReadiness {
	desired, err := buildCiliumAPIServerPolicy(cluster)
	if err == nil {
		current := newCiliumAPIServerPolicy(cluster)
		err = m.reader.Get(ctx, client.ObjectKeyFromObject(current), current)
		if err == nil {
			err = resourceownership.RequireOwnerProof("observe Cilium API server policy", current, cluster)
			if err == nil && current.GetDeletionTimestamp() == nil && reflect.DeepEqual(current.Object["spec"], desired.Object["spec"]) {
				return APIServerNetworkReadiness{Status: metav1.ConditionTrue, Reason: constants.ReasonAPIServerNetworkReady,
					Message: "The owned Cilium kube-apiserver entity policy matches spec.network.apiServerEgress. This validates configuration, not live API connectivity or Cilium policy realization."}
			}
			if err == nil {
				return APIServerNetworkReadiness{Status: metav1.ConditionUnknown, Reason: constants.ReasonAPIServerEgressPolicyPending,
					Message: "Waiting for the owned Cilium API server policy to match spec.network.apiServerEgress."}
			}
		}
	}
	if apierrors.IsNotFound(err) {
		return APIServerNetworkReadiness{Status: metav1.ConditionUnknown, Reason: constants.ReasonAPIServerEgressPolicyPending,
			Message: "Waiting for the operator to create the namespaced Cilium API server policy."}
	}
	return APIServerNetworkReadiness{Status: metav1.ConditionFalse, Reason: constants.ReasonAPIServerNetworkConfigurationInvalid,
		Message: fmt.Sprintf("Cannot validate the Cilium API server policy. Check the CiliumNetworkPolicy API, operator permissions, and resource ownership: %v", err)}
}
