package networking

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func (m *Manager) ensureHeadlessService(ctx context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	svcName := resourceidentity.HeadlessServiceName(cluster)

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Spec: corev1.ServiceSpec{
			// Publish both address families for peer discovery when available.
			IPFamilyPolicy:           ptr.To(corev1.IPFamilyPolicyPreferDualStack),
			ClusterIP:                corev1.ClusterIPNone,
			PublishNotReadyAddresses: true,
			Selector:                 servicePodSelectorLabels(cluster),
			Ports: []corev1.ServicePort{
				{
					Name:     "api",
					Port:     constants.PortAPI,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := m.applyResource(ctx, service, cluster); err != nil {
		return fmt.Errorf("failed to ensure headless Service %s/%s: %w", cluster.Namespace, svcName, err)
	}

	return nil
}

// ensureExternalService manages the external-facing Service for the OpenBaoCluster using Server-Side Apply.
func (m *Manager) ensureExternalService(ctx context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	serviceCfg := cluster.Spec.Service
	ingressCfg := cluster.Spec.Ingress
	gatewayCfg := cluster.Spec.Gateway

	needsService := serviceCfg != nil ||
		(ingressCfg != nil && ingressCfg.Enabled) ||
		(gatewayCfg != nil && gatewayCfg.Enabled)
	svcName := externalServiceName(cluster)

	// If service is not needed, check if it exists and delete it
	if !needsService {
		// Delete main external service
		if err := m.deleteServiceIfExists(ctx, cluster, svcName); err != nil {
			return fmt.Errorf("failed to delete external Service %s/%s: %w", cluster.Namespace, svcName, err)
		}
		// Delete any blue/green-specific services that might exist from previous runs
		if err := m.deleteServiceIfExists(ctx, cluster, externalServiceNameBlue(cluster)); err != nil {
			return fmt.Errorf("failed to delete blue external Service %s/%s: %w", cluster.Namespace, externalServiceNameBlue(cluster), err)
		}
		if err := m.deleteServiceIfExists(ctx, cluster, externalServiceNameGreen(cluster)); err != nil {
			return fmt.Errorf("failed to delete green external Service %s/%s: %w", cluster.Namespace, externalServiceNameGreen(cluster), err)
		}
		return nil
	}

	// Build the desired service spec
	svcType := corev1.ServiceTypeClusterIP
	annotations := map[string]string{}
	if serviceCfg != nil {
		if serviceCfg.Type != "" {
			svcType = serviceCfg.Type
		}
		for k, v := range serviceCfg.Annotations {
			annotations[k] = v
		}
	}

	selectorLabels := servicePodSelectorLabels(cluster)
	applyActiveServiceSelector(cluster, selectorLabels)

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   cluster.Namespace,
			Labels:      resourceidentity.Labels(cluster),
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: selectorLabels,
			Ports: []corev1.ServicePort{
				{
					Name:     "api",
					Port:     constants.PortAPI,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}

	if serviceCfg != nil {
		service.Spec.IPFamilyPolicy = serviceCfg.IPFamilyPolicy
		service.Spec.IPFamilies = serviceCfg.IPFamilies
	}
	if err := m.applyResource(ctx, service, cluster); err != nil {
		return fmt.Errorf("failed to ensure external Service %s/%s: %w", cluster.Namespace, svcName, err)
	}

	// Gateway-weighted traffic switching was removed. Clean up any stale Services
	// from previous iterations that used revision-specific HTTPRoute backends.
	if err := m.deleteServiceIfExists(ctx, cluster, externalServiceNameBlue(cluster)); err != nil {
		return fmt.Errorf("failed to delete stale blue external Service: %w", err)
	}
	if err := m.deleteServiceIfExists(ctx, cluster, externalServiceNameGreen(cluster)); err != nil {
		return fmt.Errorf("failed to delete stale green external Service: %w", err)
	}

	return nil
}

func (m *Manager) ensureReadReplicaService(ctx context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	svcName := resourceidentity.ReadReplicaServiceName(cluster)

	enabled := cluster.Spec.ReadReplicas != nil &&
		cluster.Spec.ReadReplicas.Replicas > 0 &&
		cluster.Spec.ReadReplicas.Service != nil &&
		cluster.Spec.ReadReplicas.Service.Enabled
	if !enabled {
		if err := m.deleteServiceIfExists(ctx, cluster, svcName); err != nil {
			return fmt.Errorf("failed to delete read-replica Service %s/%s: %w", cluster.Namespace, svcName, err)
		}
		return nil
	}

	serviceCfg := cluster.Spec.ReadReplicas.Service
	svcType := corev1.ServiceTypeClusterIP
	annotations := map[string]string{}
	if serviceCfg.Type != "" {
		svcType = serviceCfg.Type
	}
	for k, v := range serviceCfg.Annotations {
		annotations[k] = v
	}
	selector := resourceidentity.ReadReplicaPodSelectorLabels(cluster)
	selector[constants.LabelOpenBaoComponent] = constants.ComponentOpenBaoCluster

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   cluster.Namespace,
			Labels:      resourceidentity.Labels(cluster),
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			IPFamilyPolicy: serviceCfg.IPFamilyPolicy,
			IPFamilies:     serviceCfg.IPFamilies,
			Type:           svcType,
			Selector:       selector,
			Ports: []corev1.ServicePort{
				{
					Name:     "api",
					Port:     constants.PortAPI,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := m.applyResource(ctx, service, cluster); err != nil {
		return fmt.Errorf("failed to ensure read-replica Service %s/%s: %w", cluster.Namespace, svcName, err)
	}

	return nil
}

func (m *Manager) ensureMetricsService(ctx context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	svcName := metricsServiceName(cluster)
	if !workloadServiceMonitorEnabled(cluster) {
		if err := m.deleteServiceIfExists(ctx, cluster, svcName); err != nil {
			return fmt.Errorf("failed to delete metrics Service %s/%s: %w", cluster.Namespace, svcName, err)
		}
		return nil
	}

	labels := metricsResourceLabels(cluster)
	selector := servicePodSelectorLabels(cluster)
	if !workloadMetricsAllNodes(cluster) {
		selector[portopenbao.LabelActive] = labelValueTrue
	}

	serviceType := corev1.ServiceTypeClusterIP
	publishNotReady := false
	servicePort := int32(constants.PortAPI)
	targetPort := intstr.FromString("api")
	if metricsOnlyListenerEnabled(cluster) {
		servicePort = metricsOnlyListenerPort(cluster)
		targetPort = intstr.FromString("metrics")
	}
	if workloadMetricsAllNodes(cluster) {
		serviceType = corev1.ServiceTypeClusterIP
		publishNotReady = true
	}

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			IPFamilyPolicy:           ptr.To(corev1.IPFamilyPolicyPreferDualStack),
			Type:                     serviceType,
			ClusterIP:                clusterIPForMetricsService(cluster),
			PublishNotReadyAddresses: publishNotReady,
			Selector:                 selector,
			Ports: []corev1.ServicePort{
				{
					Name:       metricsServicePortName,
					Port:       servicePort,
					TargetPort: targetPort,
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := m.applyResource(ctx, service, cluster); err != nil {
		return fmt.Errorf("failed to ensure metrics Service %s/%s: %w", cluster.Namespace, svcName, err)
	}

	return nil
}

func clusterIPForMetricsService(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if workloadMetricsAllNodes(cluster) {
		return corev1.ClusterIPNone
	}
	return ""
}

// ensureACMEChallengeService manages a dedicated Service for ACME validation in ACME TLS mode.
//
// In ACME mode, OpenBao must complete ACME challenges before it can become Ready (it has no
// serving certificate yet). Most Kubernetes Services only publish ready pod endpoints, creating
// a circular dependency. This Service sets PublishNotReadyAddresses so ACME validators can reach
// pods while they are still initializing.
//
// The Service exposes standard ACME ports (80/443) and forwards to the OpenBao listener port
// (8200). This is particularly useful for private ACME CAs running inside the cluster.
func (m *Manager) ensureACMEChallengeService(ctx context.Context, _ logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	enabled := usesACMEMode(cluster)
	svcName := acmeServiceName(cluster)

	if !enabled {
		if err := m.deleteServiceIfExists(ctx, cluster, svcName); err != nil {
			return fmt.Errorf("failed to delete ACME challenge Service %s/%s: %w", cluster.Namespace, svcName, err)
		}
		return nil
	}

	selectorLabels := servicePodSelectorLabels(cluster)
	applyActiveServiceSelector(cluster, selectorLabels)

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Spec: corev1.ServiceSpec{
			IPFamilyPolicy:           ptr.To(corev1.IPFamilyPolicyPreferDualStack),
			Type:                     corev1.ServiceTypeClusterIP,
			PublishNotReadyAddresses: true,
			Selector:                 selectorLabels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http-80",
					Protocol:   corev1.ProtocolTCP,
					Port:       80,
					TargetPort: intstr.FromInt(constants.PortAPI),
				},
				{
					Name:       "https-443",
					Protocol:   corev1.ProtocolTCP,
					Port:       443,
					TargetPort: intstr.FromInt(constants.PortAPI),
				},
			},
		},
	}

	if err := m.applyResource(ctx, service, cluster); err != nil {
		return fmt.Errorf("failed to ensure ACME challenge Service %s/%s: %w", cluster.Namespace, svcName, err)
	}

	return nil
}

// Helper Jobs share the cluster identity labels but do not expose OpenBao listeners.
// Services require the server component label.
func servicePodSelectorLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	selector := resourceidentity.PodSelectorLabels(cluster)
	selector[constants.LabelOpenBaoComponent] = constants.ComponentOpenBaoCluster
	return selector
}

// deleteServiceIfExists deletes an operator-owned Service with the given name if it exists.
func (m *Manager) deleteServiceIfExists(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}

	service := &corev1.Service{}
	err := m.client.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      name,
	}, service)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := resourceownership.RequireOwnerProof("delete Service", service, cluster); err != nil {
		return err
	}

	if err := m.client.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}
