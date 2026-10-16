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

// ensureTLSRoute manages the Gateway API TLSRoute for the OpenBaoCluster using Server-Side Apply.
func (m *Manager) ensureTLSRoute(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	gatewayCfg := cluster.Spec.Gateway
	enabled := gatewayCfg != nil && gatewayCfg.Enabled && gatewayCfg.TLSPassthrough
	name := types.NamespacedName{Namespace: cluster.Namespace, Name: tlsRouteName(cluster)}

	return reconcileOptionalResource(ctx, optionalResourceOptions{
		kind:              "TLSRoute",
		apiVersion:        "gateway.networking.k8s.io/v1",
		enabled:           enabled,
		name:              name,
		owner:             cluster,
		logger:            logger,
		logKey:            "tlsroute",
		deleteDisabledMsg: "TLSRoute no longer enabled; deleting",
		deleteInvalidMsg:  "TLSRoute configuration invalid; deleting existing TLSRoute",
		newEmpty: func() client.Object {
			return &gatewayv1.TLSRoute{}
		},
		buildDesired: func() (client.Object, bool, error) {
			desired := buildTLSRoute(cluster)
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

// buildTLSRoute constructs a TLSRoute for the given OpenBaoCluster.
func buildTLSRoute(cluster *openbaov1alpha1.OpenBaoCluster) *gatewayv1.TLSRoute {
	if cluster.Spec.Gateway == nil || !cluster.Spec.Gateway.Enabled || !cluster.Spec.Gateway.TLSPassthrough {
		return nil
	}

	gw := cluster.Spec.Gateway
	if strings.TrimSpace(gw.Hostname) == "" || strings.TrimSpace(gw.GatewayRef.Name) == "" {
		return nil
	}

	gatewayNamespace := gw.GatewayRef.Namespace
	if strings.TrimSpace(gatewayNamespace) == "" {
		gatewayNamespace = cluster.Namespace
	}

	backendServiceName := externalServiceName(cluster)
	hostname := gatewayv1.Hostname(gw.Hostname)
	port := gatewayv1.PortNumber(constants.PortAPI)
	if usesACMEMode(cluster) {
		backendServiceName = acmeServiceName(cluster)
		port = gatewayv1.PortNumber(443)
	}
	gatewayNS := gatewayv1.Namespace(gatewayNamespace)
	var sectionName *gatewayv1.SectionName
	if strings.TrimSpace(gw.ListenerName) != "" {
		sn := gatewayv1.SectionName(strings.TrimSpace(gw.ListenerName))
		sectionName = &sn
	}

	return &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        tlsRouteName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      resourceidentity.Labels(cluster),
			Annotations: gw.Annotations,
		},
		Spec: gatewayv1.TLSRouteSpec{
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
			Rules: []gatewayv1.TLSRouteRule{
				{
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: gatewayv1.ObjectName(backendServiceName),
								Port: &port,
							},
						},
					},
				},
			},
		},
	}
}

func tlsRouteName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + tlsRouteSuffix
}
