package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHelmStorageNamespaceFollowsTheExistingFluxRelease(t *testing.T) {
	for _, tc := range []struct {
		name, configured, declared, active, target, release, want, problem string
	}{
		{name: "Flux default", target: "operator", want: "flux-system"},
		{name: "explicit storage", declared: "helm-records", active: "helm-records", target: "operator", want: "helm-records"},
		{name: "matching administrator selection", configured: "helm-records", declared: "helm-records", target: "operator", want: "helm-records"},
		{name: "wrong administrator selection", configured: "operator", target: "operator", problem: "differs from"},
		{name: "pending storage move", declared: "new-storage", active: "old-storage", target: "operator", problem: "changing storage namespace"},
		{name: "other target", target: "other-operator", problem: "not operatorNamespace"},
		{name: "default target", problem: "not operatorNamespace"},
		{name: "other release", target: "operator", release: "other-release", problem: "not configured release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := validConfig()
			config.OperatorNamespace, config.ReleaseNamespace = "operator", tc.configured
			release := gitopsObject(fluxHelmRelease, "flux-system", "operator",
				map[string]any{"spec": map[string]any{"targetNamespace": tc.target, "storageNamespace": tc.declared, "releaseName": tc.release}})
			if tc.active != "" {
				release.Object["status"] = map[string]any{"storageNamespace": tc.active}
			}
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(fluxHelmRelease, &unstructured.Unstructured{})
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(release).Build()
			before := release.DeepCopy()
			require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "flux-system", Name: "operator"}, before))
			p := &Procedure{plan: &Plan{Config: config}, kube: &Kube{client: c}}
			controller := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				fluxHelmName: "operator", fluxHelmNamespace: "flux-system"}}}
			err := p.resolveHelmNamespace(t.Context(), controller)
			if tc.problem != "" {
				require.ErrorContains(t, err, tc.problem)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, p.plan.Config.ReleaseNamespace)
			require.Equal(t, tc.want, p.helm.Namespace)
			require.Equal(t, "operator", p.helm.TargetNamespace)
			fresh := &unstructured.Unstructured{}
			fresh.SetGroupVersionKind(fluxHelmRelease)
			require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "flux-system", Name: "operator"}, fresh))
			require.Equal(t, before.Object, fresh.Object, "namespace discovery must not alter the HelmRelease")
		})
	}
}

func TestHelmStorageNamespaceForPlainAndRemoteInstallations(t *testing.T) {
	for _, tc := range []struct {
		name, configured, want string
		remote, allowed        bool
	}{
		{"plain Helm default", "", "operator", false, true},
		{"plain Helm separate storage", "helm-records", "helm-records", false, true},
		{"remote Flux declared storage", "helm-records", "helm-records", true, true},
		{"remote Flux unknown storage", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(fluxHelmRelease, &unstructured.Unstructured{})
			config := validConfig()
			config.OperatorNamespace, config.ReleaseNamespace = "operator", tc.configured
			p := &Procedure{plan: &Plan{Config: config}, kube: &Kube{client: fake.NewClientBuilder().WithScheme(scheme).Build()}}
			controller := &appsv1.Deployment{}
			if tc.remote {
				controller.Labels = map[string]string{fluxHelmName: "operator", fluxHelmNamespace: "flux-system"}
			}
			err := p.resolveHelmNamespace(t.Context(), controller)
			if !tc.allowed {
				require.ErrorContains(t, err, "set releaseNamespace")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, p.helm.Namespace)
			require.Equal(t, "operator", p.helm.TargetNamespace)
		})
	}
}

func TestReleaseNamespaceConfiguration(t *testing.T) {
	config := validConfig()
	config.ReleaseNamespace = "helm-records"
	require.NoError(t, config.validate())
	config.Release = ""
	config.DestinationChart = ""
	require.ErrorContains(t, config.validate(), "releaseNamespace")
	config = validConfig()
	config.ReleaseNamespace = "bad_namespace"
	require.ErrorContains(t, config.validate(), "releaseNamespace")
}
