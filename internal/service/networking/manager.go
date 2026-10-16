package networking

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
)

// Manager reconciles network-owned resources and validations for an OpenBaoCluster.
type Manager struct {
	client            client.Client
	reader            client.Reader
	scheme            *runtime.Scheme
	operatorNamespace string
	Platform          string
}

func NewManager(c client.Client, scheme *runtime.Scheme, operatorNamespace string, platform string) *Manager {
	return &Manager{
		client:            c,
		reader:            c,
		scheme:            scheme,
		operatorNamespace: operatorNamespace,
		Platform:          platform,
	}
}

func NewManagerWithReader(c client.Client, r client.Reader, scheme *runtime.Scheme, operatorNamespace string, platform string) *Manager {
	m := NewManager(c, scheme, operatorNamespace, platform)
	if r != nil {
		m.reader = r
	}
	return m
}

// Reconcile ensures network-owned resources are aligned with the desired state for the cluster.
func (m *Manager) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if err := m.runACMEPreflight(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureHeadlessService(ctx, logger, cluster); err != nil {
		return err
	}
	// Restored data can contain active integrations. The administrator prepares
	// destination isolation; additive ordinary policies would weaken that boundary.
	if cluster.Annotations[constants.AnnotationRestoreOrigin] != "" {
		if cluster.Spec.Service != nil {
			return m.ensureExternalService(ctx, logger, cluster)
		}
		return nil
	}
	if err := m.ensureExternalService(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureReadReplicaService(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureMetricsService(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureWorkloadServiceMonitor(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureACMEChallengeService(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureIngress(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureHTTPRoute(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureTLSRoute(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureGatewayCAConfigMap(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureBackendTLSPolicy(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureNetworkPolicy(ctx, logger, cluster); err != nil {
		return err
	}
	if err := m.ensureJobNetworkPolicy(ctx, logger, cluster); err != nil {
		return err
	}
	return m.ensureCiliumAPIServerPolicy(ctx, logger, cluster)
}

func (m *Manager) applyResource(ctx context.Context, obj client.Object, cluster *openbaov1alpha1.OpenBaoCluster) error {
	if service, ok := obj.(*corev1.Service); ok {
		if err := m.validateServicePrimaryIPFamily(ctx, service); err != nil {
			return err
		}
	}
	return resourceapply.ApplyOwned(ctx, m.client, m.scheme, cluster, obj)
}
