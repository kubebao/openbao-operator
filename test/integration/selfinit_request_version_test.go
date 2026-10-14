//go:build integration

package integration

import (
	"fmt"
	"testing"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestVAP_OpenBaoCluster_SelfInitRequestVersions(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	tests := []struct {
		name, version, control string
		disabled, denied       bool
	}{
		{name: "plain request on older version", version: "2.4.4"},
		{name: "empty headers on older version", version: "2.5.5", control: "empty-headers"},
		{name: "disabled controls on older version", version: "2.5.5", control: "headers", disabled: true},
		{name: "headers on older version", version: "2.5.5", control: "headers", denied: true},
		{name: "false condition on older version", version: "2.5.5", control: "false", denied: true},
		{name: "true condition on older version", version: "2.4.4", control: "true", denied: true},
		{name: "dynamic condition on prerelease", version: "2.6.0-rc.1", control: "dynamic", denied: true},
		{name: "headers at minimum", version: "2.6.0", control: "headers"},
		{name: "false condition at minimum", version: "2.6.0", control: "false"},
		{name: "dynamic condition with version metadata", version: "v2.6.4+build.1", control: "dynamic"},
		{name: "future minor prerelease", version: "2.7.0-rc.1", control: "headers"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, fmt.Sprintf("selfinit-version-%d", index))
			cluster.Spec.Version = tt.version
			request := selfInitVersionRequest(tt.control)
			cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
				Enabled: !tt.disabled, Requests: []openbaov1alpha1.SelfInitRequest{request},
			}
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
			require.NoError(t, err)
			candidate := &unstructured.Unstructured{Object: object}
			candidate.SetGroupVersionKind(openbaov1alpha1.GroupVersion.WithKind("OpenBaoCluster"))
			if tt.control == "empty-headers" {
				requests, _, err := unstructured.NestedSlice(object, "spec", "selfInit", "requests")
				require.NoError(t, err)
				requests[0].(map[string]any)["headers"] = map[string]any{}
				require.NoError(t, unstructured.SetNestedSlice(object, requests, "spec", "selfInit", "requests"))
			}
			err = k8sClient.Create(ctx, candidate, client.DryRunAll)
			if tt.denied {
				requireAdmissionDenied(t, err)
				require.ErrorContains(t, err, "headers and when require OpenBao >= 2.6.0")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVAP_OpenBaoCluster_SelfInitRequestVersionUpdates(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	cluster := newMinimalClusterObj(namespace, "selfinit-version-update")
	cluster.Spec.Version = "2.5.5"
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true, Requests: []openbaov1alpha1.SelfInitRequest{selfInitVersionRequest("")},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	cluster.Spec.SelfInit.Requests[0] = selfInitVersionRequest("false")
	err := k8sClient.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "headers and when require OpenBao >= 2.6.0")
	cluster.Spec.Version = "2.6.4"
	require.NoError(t, k8sClient.Update(ctx, cluster))
	cluster.Spec.Version = "2.5.5"
	err = k8sClient.Update(ctx, cluster, client.DryRunAll)
	requireAdmissionDenied(t, err)
	require.ErrorContains(t, err, "headers and when require OpenBao >= 2.6.0")
}

func selfInitVersionRequest(control string) openbaov1alpha1.SelfInitRequest {
	request := openbaov1alpha1.SelfInitRequest{
		Name: "health", Operation: openbaov1alpha1.SelfInitOperationRead, Path: "sys/health",
	}
	switch control {
	case "headers":
		request.Headers = map[string][]string{"X-Trace": {"test"}}
	case "false", "true":
		request.When = &apiextensionsv1.JSON{Raw: []byte(control)}
	case "dynamic":
		request.When = &apiextensionsv1.JSON{Raw: []byte(`{"eval_source":"cel","eval_type":"bool","expression":"true"}`)}
	}
	return request
}
