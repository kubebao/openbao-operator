//go:build integration
// +build integration

package integration

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	infraPublicServiceSuffix    = "-public"
	infraHTTPRouteSuffix        = "-httproute"
	infraTLSRouteSuffix         = "-tlsroute"
	infraBackendTLSPolicySuffix = "-backend-tls-policy"
	gatewayCAConfigMapKeyCACert = "ca.crt"
)

func TestInfraNetwork_ServiceSelectorsExcludeHelperPods(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)
	cluster := newMinimalClusterObj(namespace, "infra-server-backends")
	cluster.Spec.Service = &openbaov1alpha1.ServiceConfig{}
	require.NoError(t, k8sClient.Create(ctx, cluster))
	createTLSSecret(t, namespace, cluster.Name)
	c := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), c, k8sScheme, cluster, spec))
	sts := &appsv1.StatefulSet{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), sts))
	require.NotContains(t, sts.Spec.Selector.MatchLabels, constants.LabelOpenBaoComponent,
		"the immutable StatefulSet selector must remain compatible with existing workloads")
	for name, podLabels := range map[string]map[string]string{
		"server": sts.Spec.Template.Labels,
		"helper": resourceidentity.Labels(cluster),
	} {
		if name == "helper" {
			podLabels[constants.LabelOpenBaoComponent] = "upgrade"
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: podLabels},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "test.invalid/fixture:latest"}}}}
		require.NoError(t, c.Create(ctx, pod))
	}
	for _, name := range []string{cluster.Name, cluster.Name + infraPublicServiceSuffix} {
		svc := &corev1.Service{}
		key := client.ObjectKey{Namespace: namespace, Name: name}
		require.NoError(t, k8sClient.Get(ctx, key, svc))
		uid, clusterIP := svc.UID, svc.Spec.ClusterIP
		// Simulate an existing broad selector, then repair it through real SSA.
		delete(svc.Spec.Selector, constants.LabelOpenBaoComponent)
		require.NoError(t, c.Update(ctx, svc))
		before := &corev1.PodList{}
		require.NoError(t, k8sClient.List(ctx, before,
			client.InNamespace(namespace), client.MatchingLabels(svc.Spec.Selector)))
		require.Len(t, before.Items, 2)
		require.NoError(t, reconcileClusterResources(ctx, logr.Discard(), c, k8sScheme, cluster, spec))
		require.NoError(t, k8sClient.Get(ctx, key, svc))
		require.Equal(t, uid, svc.UID)
		require.Equal(t, clusterIP, svc.Spec.ClusterIP)
		after := &corev1.PodList{}
		require.NoError(t, k8sClient.List(ctx, after,
			client.InNamespace(namespace), client.MatchingLabels(svc.Spec.Selector)))
		require.Len(t, after.Items, 1)
		require.Equal(t, "server", after.Items[0].Name)
	}
}

func TestInfraNetwork_HeadlessService_IsIdempotent(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-headless")
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)

	controllerClient := newControllerClient(t)

	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() second call error = %v", err)
	}

	headless := &corev1.Service{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, headless); err != nil {
		t.Fatalf("expected headless Service to exist: %v", err)
	}
	if headless.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("expected headless Service ClusterIP None, got %q", headless.Spec.ClusterIP)
	}
	if !headless.Spec.PublishNotReadyAddresses {
		t.Fatalf("expected headless Service publishNotReadyAddresses to be true")
	}
	if len(headless.Spec.Ports) == 0 || headless.Spec.Ports[0].Port != constants.PortAPI {
		t.Fatalf("expected headless Service port %d, got %#v", constants.PortAPI, headless.Spec.Ports)
	}
}

func TestInfraNetwork_ExternalService_CreatesAndDeletes(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-external")
	cluster.Spec.Service = &openbaov1alpha1.ServiceConfig{
		Type: corev1.ServiceTypeLoadBalancer,
		Annotations: map[string]string{
			"service-annotation": testTrueString,
		},
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	externalName := cluster.Name + infraPublicServiceSuffix
	external := &corev1.Service{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: externalName}, external); err != nil {
		t.Fatalf("expected external Service to exist: %v", err)
	}
	if external.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("expected ServiceType LoadBalancer, got %q", external.Spec.Type)
	}
	if external.Annotations["service-annotation"] != testTrueString {
		t.Fatalf("expected annotation service-annotation=true, got %#v", external.Annotations)
	}

	// Remove all configs that require an external Service and reconcile again; it should delete.
	cluster.Spec.Service = nil
	cluster.Spec.Ingress = nil
	cluster.Spec.Gateway = nil
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after disabling external access error = %v", err)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: externalName}, external); err == nil {
		t.Fatalf("expected external Service to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get external Service: %v", err)
	}
}

func TestInfraNetwork_Ingress_CreatesAndDeletes(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-ingress")
	cluster.Spec.Ingress = &openbaov1alpha1.IngressConfig{
		Enabled: true,
		Host:    "bao.example.local",
	}
	cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
		TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "ingress-system",
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	ing := &networkingv1.Ingress{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, ing); err != nil {
		t.Fatalf("expected Ingress to exist: %v", err)
	}

	// Disable ingress and ensure deletion.
	cluster.Spec.Ingress.Enabled = false
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after disabling ingress error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, ing); err == nil {
		t.Fatalf("expected Ingress to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get ingress: %v", err)
	}
}

func TestInfraNetwork_HTTPRoute_CreatesAndDeletes(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-gateway")
	cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
		Enabled: true,
		GatewayRef: openbaov1alpha1.GatewayReference{
			Name: "traefik-gateway",
		},
		Hostname: "bao.example.local",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	httpRoute := &gatewayv1.HTTPRoute{}
	routeName := cluster.Name + infraHTTPRouteSuffix
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: routeName}, httpRoute); err != nil {
		t.Fatalf("expected HTTPRoute to exist: %v", err)
	}
	if len(httpRoute.Spec.Hostnames) != 1 || string(httpRoute.Spec.Hostnames[0]) != "bao.example.local" {
		t.Fatalf("expected HTTPRoute hostname %q, got %#v", "bao.example.local", httpRoute.Spec.Hostnames)
	}
	if len(httpRoute.Spec.ParentRefs) != 1 || string(httpRoute.Spec.ParentRefs[0].Name) != "traefik-gateway" {
		t.Fatalf("expected HTTPRoute parent ref %q, got %#v", "traefik-gateway", httpRoute.Spec.ParentRefs)
	}

	// Disable Gateway and reconcile again; HTTPRoute should be deleted.
	cluster.Spec.Gateway.Enabled = false
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after disabling gateway error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: routeName}, httpRoute); err == nil {
		t.Fatalf("expected HTTPRoute to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get HTTPRoute: %v", err)
	}
}

func TestInfraNetwork_GatewayCAConfigMap_CreatesUpdatesAndDeletes(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-gateway-ca")
	cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
		Enabled: true,
		GatewayRef: openbaov1alpha1.GatewayReference{
			Name: "traefik-gateway",
		},
		Hostname: "bao.example.local",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)

	ca1 := []byte("ca-1")
	createCASecret(t, namespace, cluster.Name, ca1)

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	cm := &corev1.ConfigMap{}
	cmName := cluster.Name + constants.SuffixTLSCA
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, cm); err != nil {
		t.Fatalf("expected Gateway CA ConfigMap to exist: %v", err)
	}
	if cm.Data[gatewayCAConfigMapKeyCACert] != string(ca1) {
		t.Fatalf("expected ConfigMap ca.crt=%q got %q", string(ca1), cm.Data[gatewayCAConfigMapKeyCACert])
	}

	// Update CA Secret, reconcile, and expect ConfigMap to update.
	secret := &corev1.Secret{}
	secretName := cluster.Name + constants.SuffixTLSCA
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, secret); err != nil {
		t.Fatalf("get CA secret: %v", err)
	}
	ca2 := []byte("ca-2")
	secret.Data = map[string][]byte{"ca.crt": ca2}
	if err := k8sClient.Update(ctx, secret); err != nil {
		t.Fatalf("update CA secret: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after CA update error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, cm); err != nil {
		t.Fatalf("get ConfigMap: %v", err)
	}
	if cm.Data[gatewayCAConfigMapKeyCACert] != string(ca2) {
		t.Fatalf("expected ConfigMap ca.crt=%q got %q", string(ca2), cm.Data[gatewayCAConfigMapKeyCACert])
	}

	// Disable Gateway and reconcile again; ConfigMap should be deleted.
	cluster.Spec.Gateway.Enabled = false
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, logr.Discard(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after disabling gateway error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, cm); err == nil {
		t.Fatalf("expected Gateway CA ConfigMap to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get ConfigMap: %v", err)
	}
}

func TestInfraNetwork_BlueGreenExternalService_UsesRevisionSelectorAndCleansStaleServices(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-bluegreen-service-selector")
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
		Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
	}
	cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
		Enabled: true,
		GatewayRef: openbaov1alpha1.GatewayReference{
			Name: "traefik-gateway",
		},
		Hostname: "bao.example.local",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.Name}, cluster); err != nil {
		t.Fatalf("get OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)

	staleBlue := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster.Name + "-public-blue",
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{integrationOwnerRef(cluster)},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "api", Port: constants.PortAPI, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	if err := k8sClient.Create(ctx, staleBlue); err != nil {
		t.Fatalf("create stale blue Service: %v", err)
	}
	staleGreen := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster.Name + "-public-green",
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{integrationOwnerRef(cluster)},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "api", Port: constants.PortAPI, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	if err := k8sClient.Create(ctx, staleGreen); err != nil {
		t.Fatalf("create stale green Service: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{
			Phase:         openbaov1alpha1.PhasePromoting,
			BlueRevision:  "blue123",
			GreenRevision: "green456",
		}
	})

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	mainSvcName := cluster.Name + infraPublicServiceSuffix
	mainSvc := &corev1.Service{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: mainSvcName}, mainSvc); err != nil {
		t.Fatalf("expected main external Service to exist: %v", err)
	}
	if mainSvc.Spec.Selector[constants.LabelOpenBaoRevision] != "blue123" {
		t.Fatalf("expected main Service selector revision=blue123 got %#v", mainSvc.Spec.Selector)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: staleBlue.Name}, staleBlue); err == nil {
		t.Fatalf("expected stale blue Service to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get stale blue Service: %v", err)
	}
	staleGreenKey := types.NamespacedName{Namespace: namespace, Name: staleGreen.Name}
	if err := k8sClient.Get(ctx, staleGreenKey, staleGreen); err == nil {
		t.Fatalf("expected stale green Service to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get stale green Service: %v", err)
	}

	// Ensure HTTPRoute exists and references the main Service.
	route := &gatewayv1.HTTPRoute{}
	httpRouteName := cluster.Name + infraHTTPRouteSuffix
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: httpRouteName}, route); err != nil {
		t.Fatalf("expected HTTPRoute to exist: %v", err)
	}
	if len(route.Spec.Rules) == 0 || len(route.Spec.Rules[0].BackendRefs) != 1 {
		t.Fatalf("expected HTTPRoute to have 1 backend, got %#v", route.Spec.Rules)
	}
	if string(route.Spec.Rules[0].BackendRefs[0].Name) != mainSvcName {
		t.Fatalf("expected HTTPRoute backend %q got %q", mainSvcName, route.Spec.Rules[0].BackendRefs[0].Name)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.BlueGreen.Phase = openbaov1alpha1.PhaseDemotingBlue
	})
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() during demoting blue error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: mainSvcName}, mainSvc); err != nil {
		t.Fatalf("get main Service during demoting blue: %v", err)
	}
	if mainSvc.Spec.Selector[constants.LabelOpenBaoRevision] != "blue123" {
		t.Fatalf("expected main Service selector revision=blue123 during demoting blue, got %#v", mainSvc.Spec.Selector)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.BlueGreen.Phase = openbaov1alpha1.PhaseCleanup
	})
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() during cleanup error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: mainSvcName}, mainSvc); err != nil {
		t.Fatalf("get main Service during cleanup: %v", err)
	}
	if mainSvc.Spec.Selector[constants.LabelOpenBaoRevision] != "green456" {
		t.Fatalf("expected main Service selector revision=green456 during cleanup, got %#v", mainSvc.Spec.Selector)
	}
}

func TestInfraNetwork_TLSRoute_CreatesAndDeletes(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-tlsroute")
	cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
		Enabled: true,
		GatewayRef: openbaov1alpha1.GatewayReference{
			Name: "traefik-gateway",
		},
		Hostname:       "bao.example.local",
		TLSPassthrough: true,
		Annotations:    map[string]string{"route-annotation": testTrueString},
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)
	createCASecret(t, namespace, cluster.Name, []byte("ca-1"))

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	mainSvcName := cluster.Name + infraPublicServiceSuffix
	// TLSRoute is created for passthrough mode.
	tlsRoute := &gatewayv1.TLSRoute{}
	tlsRouteName := cluster.Name + infraTLSRouteSuffix
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: tlsRouteName}, tlsRoute); err != nil {
		t.Fatalf("expected TLSRoute to exist: %v", err)
	}
	if len(tlsRoute.Spec.Hostnames) != 1 || string(tlsRoute.Spec.Hostnames[0]) != "bao.example.local" {
		t.Fatalf("expected TLSRoute hostname %q, got %#v", "bao.example.local", tlsRoute.Spec.Hostnames)
	}
	if len(tlsRoute.Spec.Rules) != 1 || len(tlsRoute.Spec.Rules[0].BackendRefs) != 1 {
		t.Fatalf("expected TLSRoute to have 1 backend, got %#v", tlsRoute.Spec.Rules)
	}
	if string(tlsRoute.Spec.Rules[0].BackendRefs[0].Name) != mainSvcName {
		t.Fatalf(
			"expected TLSRoute backend Service %q, got %q",
			mainSvcName,
			tlsRoute.Spec.Rules[0].BackendRefs[0].Name,
		)
	}
	if tlsRoute.Annotations["route-annotation"] != testTrueString {
		t.Fatalf("expected TLSRoute annotation route-annotation=true, got %#v", tlsRoute.Annotations)
	}

	// HTTPRoute is mutually exclusive with TLSRoute.
	httpRoute := &gatewayv1.HTTPRoute{}
	httpRouteName := cluster.Name + infraHTTPRouteSuffix
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: httpRouteName}, httpRoute); err == nil {
		t.Fatalf("expected HTTPRoute to not exist when TLS passthrough is enabled")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get HTTPRoute: %v", err)
	}

	// BackendTLSPolicy is not needed for passthrough mode.
	backendTLS := &gatewayv1.BackendTLSPolicy{}
	backendTLSName := cluster.Name + infraBackendTLSPolicySuffix
	backendTLSKey := types.NamespacedName{Namespace: namespace, Name: backendTLSName}
	if err := k8sClient.Get(ctx, backendTLSKey, backendTLS); err == nil {
		t.Fatalf("expected BackendTLSPolicy to not exist when TLS passthrough is enabled")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get BackendTLSPolicy: %v", err)
	}

	// Switch back to HTTPRoute mode; TLSRoute should be deleted and HTTPRoute created.
	cluster.Spec.Gateway.TLSPassthrough = false
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after disabling TLS passthrough error = %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: tlsRouteName}, tlsRoute); err == nil {
		t.Fatalf("expected TLSRoute to be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get TLSRoute: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: httpRouteName}, httpRoute); err != nil {
		t.Fatalf("expected HTTPRoute to exist after disabling TLS passthrough: %v", err)
	}
}

func TestInfraNetwork_BackendTLSPolicy_CreatesAndDeletes(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "infra-backend-tls-policy")
	cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
		Enabled: true,
		GatewayRef: openbaov1alpha1.GatewayReference{
			Name: "traefik-gateway",
		},
		Hostname: "bao.example.local",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}
	createTLSSecret(t, namespace, cluster.Name)
	createCASecret(t, namespace, cluster.Name, []byte("ca-1"))

	controllerClient := newControllerClient(t)
	spec := newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	mainSvcName := cluster.Name + infraPublicServiceSuffix
	backendTLS := &gatewayv1.BackendTLSPolicy{}
	backendTLSName := cluster.Name + infraBackendTLSPolicySuffix
	backendTLSKey := types.NamespacedName{Namespace: namespace, Name: backendTLSName}
	if err := k8sClient.Get(ctx, backendTLSKey, backendTLS); err != nil {
		t.Fatalf("expected BackendTLSPolicy to exist: %v", err)
	}
	if len(backendTLS.Spec.TargetRefs) != 1 || string(backendTLS.Spec.TargetRefs[0].Name) != mainSvcName {
		t.Fatalf("expected BackendTLSPolicy target Service %q, got %#v", mainSvcName, backendTLS.Spec.TargetRefs)
	}
	expectedHostname := portopenbao.ComputeTLSServerName(cluster)
	if string(backendTLS.Spec.Validation.Hostname) != expectedHostname {
		t.Fatalf(
			"expected BackendTLSPolicy validation hostname %q, got %q",
			expectedHostname,
			backendTLS.Spec.Validation.Hostname,
		)
	}
	caName := cluster.Name + constants.SuffixTLSCA
	if len(backendTLS.Spec.Validation.CACertificateRefs) != 1 ||
		string(backendTLS.Spec.Validation.CACertificateRefs[0].Name) != caName {
		t.Fatalf(
			"expected BackendTLSPolicy CA ConfigMap ref %q, got %#v",
			caName,
			backendTLS.Spec.Validation.CACertificateRefs,
		)
	}

	// Disable BackendTLS; BackendTLSPolicy should be deleted.
	disabled := false
	cluster.Spec.Gateway.BackendTLS = &openbaov1alpha1.BackendTLSConfig{Enabled: &disabled}
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after disabling BackendTLS error = %v", err)
	}
	if err := k8sClient.Get(ctx, backendTLSKey, backendTLS); err == nil {
		t.Fatalf("expected BackendTLSPolicy to be deleted after disabling BackendTLS")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get BackendTLSPolicy: %v", err)
	}

	// Re-enable BackendTLS; BackendTLSPolicy should be recreated.
	enabled := true
	cluster.Spec.Gateway.BackendTLS = &openbaov1alpha1.BackendTLSConfig{Enabled: &enabled}
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after enabling BackendTLS error = %v", err)
	}
	if err := k8sClient.Get(ctx, backendTLSKey, backendTLS); err != nil {
		t.Fatalf("expected BackendTLSPolicy to exist after re-enabling: %v", err)
	}

	// Enabling TLS passthrough should remove BackendTLSPolicy (mutually exclusive concerns).
	cluster.Spec.Gateway.TLSPassthrough = true
	if err := k8sClient.Update(ctx, cluster); err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	spec = newTestStatefulSetSpec(cluster)
	if err := reconcileClusterResources(ctx, discardLogger(), controllerClient, k8sScheme, cluster, spec); err != nil {
		t.Fatalf("Reconcile() after enabling TLS passthrough error = %v", err)
	}
	if err := k8sClient.Get(ctx, backendTLSKey, backendTLS); err == nil {
		t.Fatalf("expected BackendTLSPolicy to be deleted after enabling TLS passthrough")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get BackendTLSPolicy: %v", err)
	}
}
