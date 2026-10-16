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
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// ensureBackendTLSPolicy manages the Gateway API BackendTLSPolicy for the OpenBaoCluster.
func (m *Manager) ensureBackendTLSPolicy(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	gatewayCfg := cluster.Spec.Gateway
	gatewayEnabled := gatewayCfg != nil && gatewayCfg.Enabled

	if gatewayCfg != nil && gatewayCfg.TLSPassthrough {
		name := backendTLSPolicyName(cluster)
		backendTLSPolicy := &gatewayv1.BackendTLSPolicy{}
		err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      name,
		}, backendTLSPolicy)
		if err != nil {
			if operatorerrors.IsCRDMissingError(err) || apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get BackendTLSPolicy %s/%s: %w", cluster.Namespace, name, err)
		}

		logger.V(1).Info("BackendTLSPolicy not needed with TLS passthrough; deleting", "backendtlspolicy", name)
		if err := resourceownership.RequireOwnerProof("delete BackendTLSPolicy", backendTLSPolicy, cluster); err != nil {
			return err
		}
		if err := m.client.Delete(ctx, backendTLSPolicy, client.Preconditions{UID: &backendTLSPolicy.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete BackendTLSPolicy %s/%s: %w", cluster.Namespace, name, err)
		}
		return nil
	}

	backendTLSEnabled := gatewayEnabled
	if gatewayCfg != nil && gatewayCfg.BackendTLS != nil && gatewayCfg.BackendTLS.Enabled != nil {
		backendTLSEnabled = *gatewayCfg.BackendTLS.Enabled
	}

	if backendTLSEnabled && !cluster.Spec.TLS.Enabled {
		logger.V(1).Info("BackendTLSPolicy requires TLS to be enabled; skipping", "tls_enabled", cluster.Spec.TLS.Enabled)
		return nil
	}

	name := types.NamespacedName{Namespace: cluster.Namespace, Name: backendTLSPolicyName(cluster)}

	return reconcileOptionalResource(ctx, optionalResourceOptions{
		kind:              "BackendTLSPolicy",
		apiVersion:        "gateway.networking.k8s.io/v1",
		enabled:           backendTLSEnabled,
		name:              name,
		owner:             cluster,
		logger:            logger,
		logKey:            "backendtlspolicy",
		deleteDisabledMsg: "BackendTLSPolicy no longer enabled; deleting",
		deleteInvalidMsg:  "BackendTLSPolicy configuration invalid; deleting existing BackendTLSPolicy",
		newEmpty: func() client.Object {
			return &gatewayv1.BackendTLSPolicy{}
		},
		buildDesired: func() (client.Object, bool, error) {
			desired := buildBackendTLSPolicy(cluster)
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

// buildBackendTLSPolicy constructs a BackendTLSPolicy for the given OpenBaoCluster.
func buildBackendTLSPolicy(cluster *openbaov1alpha1.OpenBaoCluster) *gatewayv1.BackendTLSPolicy {
	gatewayCfg := cluster.Spec.Gateway
	if gatewayCfg == nil || !gatewayCfg.Enabled || !cluster.Spec.TLS.Enabled {
		return nil
	}

	backendTLSEnabled := true
	if gatewayCfg.BackendTLS != nil && gatewayCfg.BackendTLS.Enabled != nil {
		backendTLSEnabled = *gatewayCfg.BackendTLS.Enabled
	}
	if !backendTLSEnabled {
		return nil
	}

	backendServiceName := externalServiceName(cluster)
	caConfigMapName := cluster.Name + constants.SuffixTLSCA

	hostname := ""
	if gatewayCfg.BackendTLS != nil {
		hostname = gatewayCfg.BackendTLS.Hostname
	}
	if strings.TrimSpace(hostname) == "" {
		hostname = portopenbao.ComputeTLSServerName(cluster)
	}

	targetRefs := []gatewayv1.LocalPolicyTargetReferenceWithSectionName{
		{
			LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
				Group: gatewayv1.Group(""),
				Kind:  gatewayv1.Kind("Service"),
				Name:  gatewayv1.ObjectName(backendServiceName),
			},
		},
	}

	return &gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backendTLSPolicyName(cluster),
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: targetRefs,
			Validation: gatewayv1.BackendTLSPolicyValidation{
				CACertificateRefs: []gatewayv1.LocalObjectReference{
					{
						Group: "",
						Kind:  "ConfigMap",
						Name:  gatewayv1.ObjectName(caConfigMapName),
					},
				},
				Hostname: gatewayv1.PreciseHostname(hostname),
			},
		},
	}
}

func backendTLSPolicyName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + backendTLSPolicySuffix
}

func (m *Manager) ensureGatewayCAConfigMap(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	gatewayCfg := cluster.Spec.Gateway
	enabled := gatewayCfg != nil && gatewayCfg.Enabled
	configMapName := cluster.Name + constants.SuffixTLSCA

	if !enabled {
		configMap := &corev1.ConfigMap{}
		err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      configMapName,
		}, configMap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get Gateway CA ConfigMap %s/%s: %w", cluster.Namespace, configMapName, err)
		}

		logger.Info("Gateway disabled; deleting CA ConfigMap", "configmap", configMapName)
		if err := resourceownership.RequireOwnerProof("delete Gateway CA ConfigMap", configMap, cluster); err != nil {
			return err
		}
		if deleteErr := m.client.Delete(ctx, configMap, client.Preconditions{UID: &configMap.UID}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			return fmt.Errorf("failed to delete Gateway CA ConfigMap %s/%s: %w", cluster.Namespace, configMapName, deleteErr)
		}
		return nil
	}

	caSecretName := cluster.Name + constants.SuffixTLSCA
	caSecret := &corev1.Secret{}
	if err := m.client.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      caSecretName,
	}, caSecret); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("CA Secret not found; skipping Gateway CA ConfigMap creation", "secret", caSecretName)
			return nil
		}
		if apierrors.IsForbidden(err) {
			logger.V(1).Info("CA Secret access forbidden (likely waiting for RBAC); skipping Gateway CA ConfigMap creation", "secret", caSecretName)
			return nil
		}
		return fmt.Errorf("failed to get CA Secret %s/%s: %w", cluster.Namespace, caSecretName, err)
	}

	caCertPEM, ok := caSecret.Data["ca.crt"]
	if !ok || len(caCertPEM) == 0 {
		return fmt.Errorf("CA Secret %s/%s missing 'ca.crt' key", cluster.Namespace, caSecretName)
	}

	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.Labels(cluster),
		},
		Data: map[string]string{
			"ca.crt": string(caCertPEM),
		},
	}

	if err := m.applyResource(ctx, configMap, cluster); err != nil {
		return fmt.Errorf("failed to ensure Gateway CA ConfigMap %s/%s: %w", cluster.Namespace, configMapName, err)
	}

	return nil
}
