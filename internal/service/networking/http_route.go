package networking

import (
	"context"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

// ensureHTTPRoute manages the Gateway API HTTPRoute for the OpenBaoCluster.
func (m *Manager) ensureHTTPRoute(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	gatewayCfg := cluster.Spec.Gateway
	enabled := gatewayCfg != nil && gatewayCfg.Enabled && !gatewayCfg.TLSPassthrough
	name := types.NamespacedName{Namespace: cluster.Namespace, Name: httpRouteName(cluster)}

	return reconcileOptionalResource(ctx, optionalResourceOptions{
		kind:              "HTTPRoute",
		apiVersion:        "gateway.networking.k8s.io/v1",
		enabled:           enabled,
		name:              name,
		owner:             cluster,
		logger:            logger,
		logKey:            "httproute",
		deleteDisabledMsg: "HTTPRoute no longer enabled; deleting",
		deleteInvalidMsg:  "HTTPRoute configuration invalid; deleting existing HTTPRoute",
		newEmpty: func() client.Object {
			return &gatewayv1.HTTPRoute{}
		},
		buildDesired: func() (client.Object, bool, error) {
			desired := buildHTTPRoute(cluster)
			if desired == nil {
				return nil, false, nil
			}
			return desired, true, nil
		},
		degradeOnCRDMissing: true,
		get:                 m.client.Get,
		delete:              m.client.Delete,
		apply:               func(ctx context.Context, obj client.Object) error { return m.applyResource(ctx, obj, cluster) },
	})
}

// buildHTTPRoute constructs an HTTPRoute for the given OpenBaoCluster.
func buildHTTPRoute(cluster *openbaov1alpha1.OpenBaoCluster) *gatewayv1.HTTPRoute {
	if cluster.Spec.Gateway == nil || !cluster.Spec.Gateway.Enabled || cluster.Spec.Gateway.TLSPassthrough {
		return nil
	}

	gw := cluster.Spec.Gateway
	if strings.TrimSpace(gw.Hostname) == "" || strings.TrimSpace(gw.GatewayRef.Name) == "" {
		return nil
	}

	path := gw.Path
	if strings.TrimSpace(path) == "" {
		path = "/"
	}

	gatewayNamespace := gw.GatewayRef.Namespace
	if strings.TrimSpace(gatewayNamespace) == "" {
		gatewayNamespace = cluster.Namespace
	}

	hostname := gatewayv1.Hostname(gw.Hostname)
	pathType := gatewayv1.PathMatchPathPrefix
	port := gatewayv1.PortNumber(constants.PortAPI)
	gatewayNS := gatewayv1.Namespace(gatewayNamespace)
	var sectionName *gatewayv1.SectionName
	if strings.TrimSpace(gw.ListenerName) != "" {
		sn := gatewayv1.SectionName(strings.TrimSpace(gw.ListenerName))
		sectionName = &sn
	}

	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        httpRouteName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      resourceidentity.Labels(cluster),
			Annotations: gw.Annotations,
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:        gatewayv1.ObjectName(gw.GatewayRef.Name),
						Namespace:   &gatewayNS,
						SectionName: sectionName,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{hostname},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathType,
								Value: &path,
							},
						},
					},
					BackendRefs: buildHTTPRouteBackends(cluster, port),
				},
			},
		},
	}
}

func buildHTTPRouteBackends(cluster *openbaov1alpha1.OpenBaoCluster, port gatewayv1.PortNumber) []gatewayv1.HTTPBackendRef {
	name := gatewayv1.ObjectName(externalServiceName(cluster))
	return []gatewayv1.HTTPBackendRef{
		{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: name,
					Port: &port,
				},
			},
		},
	}
}

func httpRouteName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + httpRouteSuffix
}
