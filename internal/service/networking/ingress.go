package networking

import (
	"context"
	"strings"

	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

func (m *Manager) ensureIngress(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	ingressCfg := cluster.Spec.Ingress
	enabled := ingressCfg != nil && ingressCfg.Enabled
	name := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}

	return reconcileOptionalResource(ctx, optionalResourceOptions{
		kind:              "Ingress",
		apiVersion:        "networking.k8s.io/v1",
		enabled:           enabled,
		name:              name,
		owner:             cluster,
		logger:            logger,
		logKey:            "ingress",
		deleteDisabledMsg: "Ingress no longer enabled; deleting",
		deleteInvalidMsg:  "Ingress configuration invalid; deleting existing Ingress",
		newEmpty: func() client.Object {
			return &networkingv1.Ingress{}
		},
		buildDesired: func() (client.Object, bool, error) {
			desired := buildIngress(cluster)
			if desired == nil {
				return nil, false, nil
			}
			return desired, true, nil
		},
		get:    m.client.Get,
		delete: m.client.Delete,
		apply:  func(ctx context.Context, obj client.Object) error { return m.applyResource(ctx, obj, cluster) },
	})
}

// buildIngress constructs an Ingress resource for the given OpenBaoCluster.
func buildIngress(cluster *openbaov1alpha1.OpenBaoCluster) *networkingv1.Ingress {
	if cluster.Spec.Ingress == nil || !cluster.Spec.Ingress.Enabled {
		return nil
	}

	ing := cluster.Spec.Ingress
	if strings.TrimSpace(ing.Host) == "" {
		return nil
	}

	path := ing.Path
	if strings.TrimSpace(path) == "" {
		path = "/"
	}

	pathType := networkingv1.PathTypePrefix
	switch ing.PathType {
	case openbaov1alpha1.IngressPathTypeExact:
		pathType = networkingv1.PathTypeExact
	case openbaov1alpha1.IngressPathTypeImplementationSpecific:
		pathType = networkingv1.PathTypeImplementationSpecific
	}
	backendServiceName := externalServiceName(cluster)

	rule := networkingv1.IngressRule{
		Host: ing.Host,
		IngressRuleValue: networkingv1.IngressRuleValue{
			HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{
					{
						Path:     path,
						PathType: &pathType,
						Backend: networkingv1.IngressBackend{
							Service: &networkingv1.IngressServiceBackend{
								Name: backendServiceName,
								Port: networkingv1.ServiceBackendPort{
									Number: constants.PortAPI,
								},
							},
						},
					},
				},
			},
		},
	}

	secretName := ing.TLSSecretName
	if strings.TrimSpace(secretName) == "" {
		secretName = resourceidentity.TLSServerSecretName(cluster)
	}

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cluster.Name,
			Namespace:   cluster.Namespace,
			Labels:      resourceidentity.Labels(cluster),
			Annotations: ing.Annotations,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{rule},
			TLS: []networkingv1.IngressTLS{
				{
					Hosts:      []string{ing.Host},
					SecretName: secretName,
				},
			},
		},
	}

	if ing.ClassName != nil && strings.TrimSpace(*ing.ClassName) != "" {
		className := strings.TrimSpace(*ing.ClassName)
		ingress.Spec.IngressClassName = &className
	}

	return ingress
}
