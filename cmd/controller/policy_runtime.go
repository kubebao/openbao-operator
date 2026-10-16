package controller

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/openbaotls"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
	"github.com/kubebao/openbao-operator/internal/service/restore"
)

func operatorPolicyClientFactory(
	clientset kubernetes.Interface,
	manager *openbao.ClientManager,
	tokens portauth.ControllerTokenProvider,
) configuration.PolicyClientFactory {
	return func(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) (portopenbao.PolicyClient, error) {
		service := cluster.Name
		if cluster.Spec.Service != nil ||
			(cluster.Spec.Ingress != nil && cluster.Spec.Ingress.Enabled) ||
			(cluster.Spec.Gateway != nil && cluster.Spec.Gateway.Enabled) {
			service += "-public"
		}
		baseURL := fmt.Sprintf("https://%s.%s.svc:%d", service, cluster.Namespace, constants.PortAPI)
		return authenticatedOperatorClient(ctx, clientset, manager, tokens, cluster, baseURL)
	}
}

func restoreRecoveryClientFactory(
	clientset kubernetes.Interface,
	manager *openbao.ClientManager,
	tokens portauth.ControllerTokenProvider,
) restore.RecoveryClientFactory {
	return func(
		ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, podName string,
	) (restore.RecoveryClient, error) {
		baseURL := fmt.Sprintf("https://%s.%s.%s.svc:%d", podName, cluster.Name, cluster.Namespace, constants.PortAPI)
		return authenticatedOperatorClient(ctx, clientset, manager, tokens, cluster, baseURL)
	}
}

func authenticatedOperatorClient(ctx context.Context, clientset kubernetes.Interface, manager *openbao.ClientManager,
	tokens portauth.ControllerTokenProvider, cluster *openbaov1alpha1.OpenBaoCluster, baseURL string,
) (*openbao.Client, error) {
	trust, err := openbaotls.ReadClientTrustBundle(ctx, clientset, cluster)
	if err != nil {
		return nil, err
	}
	jwt, err := tokens.Token(ctx, cluster)
	if err != nil {
		return nil, err
	}
	factory := manager.FactoryFor(string(cluster.UID), trust.CACert, trust.TLSServerName)
	return factory.NewWithJWT(ctx, baseURL, portauth.RoleNameOperator, jwt)
}
