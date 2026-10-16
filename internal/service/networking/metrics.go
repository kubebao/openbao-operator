package networking

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

func (m *Manager) ensureWorkloadServiceMonitor(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) error {
	name := types.NamespacedName{Namespace: cluster.Namespace, Name: workloadServiceMonitorName(cluster)}

	return reconcileOptionalResource(ctx, optionalResourceOptions{
		kind:              "ServiceMonitor",
		apiVersion:        "monitoring.coreos.com/v1",
		enabled:           workloadServiceMonitorEnabled(cluster),
		name:              name,
		owner:             cluster,
		logger:            logger,
		logKey:            "servicemonitor",
		deleteDisabledMsg: "workload ServiceMonitor no longer enabled; deleting",
		deleteInvalidMsg:  "workload ServiceMonitor configuration invalid; deleting existing ServiceMonitor",
		newEmpty: func() client.Object {
			return newServiceMonitorObject(cluster.Namespace, workloadServiceMonitorName(cluster))
		},
		buildDesired: func() (client.Object, bool, error) {
			desired, err := buildWorkloadServiceMonitor(cluster)
			if err != nil {
				return nil, false, err
			}
			if desired == nil {
				return nil, false, nil
			}
			return desired, true, nil
		},
		ignoreCRDMissing: true,
		get:              m.client.Get,
		delete:           m.client.Delete,
		apply:            func(ctx context.Context, obj client.Object) error { return m.applyResource(ctx, obj, cluster) },
	})
}

func workloadMetricsEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return cluster != nil &&
		cluster.Spec.Observability != nil &&
		cluster.Spec.Observability.Metrics != nil &&
		cluster.Spec.Observability.Metrics.Enabled
}

func workloadServiceMonitorEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if !workloadMetricsEnabled(cluster) {
		return false
	}
	serviceMonitor := cluster.Spec.Observability.Metrics.ServiceMonitor
	return serviceMonitor == nil || serviceMonitor.Enabled
}

func workloadMetricsScrapeProfile(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if !workloadMetricsEnabled(cluster) {
		return metricsScrapeProfileActive
	}
	profile := strings.TrimSpace(cluster.Spec.Observability.Metrics.ScrapeProfile)
	if profile == "" {
		return metricsScrapeProfileActive
	}
	return profile
}

func workloadMetricsAllNodes(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return workloadMetricsScrapeProfile(cluster) == metricsScrapeProfileAllNode
}

func metricsOnlyListenerEnabled(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if !workloadMetricsEnabled(cluster) {
		return false
	}
	listener := cluster.Spec.Observability.Metrics.MetricsOnlyListener
	if listener != nil && listener.Enabled != nil {
		return *listener.Enabled
	}
	return workloadMetricsAllNodes(cluster)
}

func metricsOnlyListenerPort(cluster *openbaov1alpha1.OpenBaoCluster) int32 {
	if workloadMetricsEnabled(cluster) && cluster.Spec.Observability.Metrics.MetricsOnlyListener != nil {
		port := cluster.Spec.Observability.Metrics.MetricsOnlyListener.Port
		if port > 0 {
			return port
		}
	}
	return constants.PortMetrics
}

func metricsResourceLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	labels := resourceidentity.Labels(cluster)
	labels[constants.LabelAppComponent] = metricsComponentLabelValue
	labels[constants.LabelOpenBaoComponent] = metricsComponentLabelValue
	labels[metricsScrapeProfileLabel] = workloadMetricsScrapeProfile(cluster)
	return labels
}

func buildWorkloadServiceMonitor(cluster *openbaov1alpha1.OpenBaoCluster) (*unstructured.Unstructured, error) {
	if !workloadServiceMonitorEnabled(cluster) {
		return nil, nil
	}

	serviceMonitor := cluster.Spec.Observability.Metrics.ServiceMonitor
	labels := metricsResourceLabels(cluster)
	requiredLabels := metricsResourceLabels(cluster)
	if serviceMonitor != nil {
		for key, value := range serviceMonitor.Labels {
			labels[key] = value
		}
	}
	for key, value := range requiredLabels {
		labels[key] = value
	}

	endpoint, err := buildServiceMonitorEndpoint(cluster, serviceMonitor)
	if err != nil {
		return nil, err
	}

	jobLabel := defaultServiceMonitorJobKey
	if serviceMonitor != nil && strings.TrimSpace(serviceMonitor.JobLabel) != "" {
		jobLabel = strings.TrimSpace(serviceMonitor.JobLabel)
	}

	annotations := map[string]string{}
	if serviceMonitor != nil {
		for key, value := range serviceMonitor.Annotations {
			annotations[key] = value
		}
	}

	obj := newServiceMonitorObject(cluster.Namespace, workloadServiceMonitorName(cluster))
	obj.SetLabels(labels)
	obj.SetAnnotations(annotations)
	obj.Object["spec"] = map[string]interface{}{
		"jobLabel": jobLabel,
		"namespaceSelector": map[string]interface{}{
			"matchNames": []interface{}{cluster.Namespace},
		},
		"selector": map[string]interface{}{
			"matchLabels": stringMapToInterface(metricsResourceLabels(cluster)),
		},
		"endpoints": []interface{}{endpoint},
	}
	return obj, nil
}

func buildServiceMonitorEndpoint(cluster *openbaov1alpha1.OpenBaoCluster, serviceMonitor *openbaov1alpha1.ServiceMonitorConfig) (map[string]interface{}, error) {
	endpoint := map[string]interface{}{
		"port": metricsServicePortName,
		"path": metricsPath,
		"params": map[string]interface{}{
			"format": []interface{}{metricsFormatParam},
		},
		"scheme": serviceMonitorScheme(cluster),
	}

	if serviceMonitor != nil {
		if strings.TrimSpace(serviceMonitor.Interval) != "" {
			endpoint["interval"] = strings.TrimSpace(serviceMonitor.Interval)
		}
		if strings.TrimSpace(serviceMonitor.ScrapeTimeout) != "" {
			endpoint["scrapeTimeout"] = strings.TrimSpace(serviceMonitor.ScrapeTimeout)
		}
		if serviceMonitor.Authorization != nil {
			authorization, err := buildServiceMonitorAuthorization(serviceMonitor.Authorization)
			if err != nil {
				return nil, err
			}
			endpoint["authorization"] = authorization
		}
		if serviceMonitor.TLSConfig != nil {
			tlsConfig, err := buildServiceMonitorTLSConfig(serviceMonitor.TLSConfig)
			if err != nil {
				return nil, err
			}
			if len(tlsConfig) > 0 {
				endpoint["tlsConfig"] = tlsConfig
			}
		}
	}
	if workloadMetricsAllNodes(cluster) {
		endpoint["relabelings"] = []interface{}{
			map[string]interface{}{
				"sourceLabels": []interface{}{"__meta_kubernetes_pod_name"},
				"targetLabel":  "pod",
			},
			map[string]interface{}{
				"sourceLabels": []interface{}{"__meta_kubernetes_pod_node_name"},
				"targetLabel":  "node",
			},
		}
	}

	return endpoint, nil
}

func serviceMonitorScheme(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster != nil && cluster.Spec.TLS.Enabled {
		return "https"
	}
	return "http"
}

func buildServiceMonitorAuthorization(auth *openbaov1alpha1.ServiceMonitorAuthorizationConfig) (map[string]interface{}, error) {
	secretName := strings.TrimSpace(auth.CredentialsSecret.Name)
	if secretName == "" {
		return nil, fmt.Errorf("spec.observability.metrics.serviceMonitor.authorization.credentialsSecret.name is required")
	}
	key := strings.TrimSpace(auth.CredentialsSecret.Key)
	if key == "" {
		key = "token"
	}
	authType := strings.TrimSpace(auth.Type)
	if authType == "" {
		authType = "Bearer"
	}
	return map[string]interface{}{
		"type": authType,
		"credentials": map[string]interface{}{
			"name": secretName,
			"key":  key,
		},
	}, nil
}

func buildServiceMonitorTLSConfig(tlsConfig *openbaov1alpha1.ServiceMonitorTLSConfig) (map[string]interface{}, error) {
	if tlsConfig.CAConfigMap != nil && tlsConfig.CASecret != nil {
		return nil, fmt.Errorf("spec.observability.metrics.serviceMonitor.tlsConfig.caConfigMap and caSecret are mutually exclusive")
	}
	if tlsConfig.CAConfigMap != nil && strings.TrimSpace(tlsConfig.CAConfigMap.Name) == "" {
		return nil, fmt.Errorf("spec.observability.metrics.serviceMonitor.tlsConfig.caConfigMap.name is required")
	}
	if tlsConfig.CASecret != nil && strings.TrimSpace(tlsConfig.CASecret.Name) == "" {
		return nil, fmt.Errorf("spec.observability.metrics.serviceMonitor.tlsConfig.caSecret.name is required")
	}

	out := map[string]interface{}{}
	if strings.TrimSpace(tlsConfig.ServerName) != "" {
		out["serverName"] = strings.TrimSpace(tlsConfig.ServerName)
	}
	if tlsConfig.InsecureSkipVerify != nil {
		out["insecureSkipVerify"] = *tlsConfig.InsecureSkipVerify
	}
	if tlsConfig.CAConfigMap != nil {
		out["ca"] = map[string]interface{}{
			"configMap": keyRefObject(tlsConfig.CAConfigMap, "ca.crt"),
		}
	}
	if tlsConfig.CASecret != nil {
		out["ca"] = map[string]interface{}{
			"secret": keyRefObject(tlsConfig.CASecret, "ca.crt"),
		}
	}
	return out, nil
}

func keyRefObject(ref *openbaov1alpha1.ServiceMonitorKeySelector, defaultKey string) map[string]interface{} {
	key := strings.TrimSpace(ref.Key)
	if key == "" {
		key = defaultKey
	}
	return map[string]interface{}{
		"name": strings.TrimSpace(ref.Name),
		"key":  key,
	}
}

func newServiceMonitorObject(namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("monitoring.coreos.com/v1")
	obj.SetKind("ServiceMonitor")
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

func stringMapToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
