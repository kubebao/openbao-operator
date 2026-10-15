package networking

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	operatorerrors "github.com/kubebao/openbao-operator/internal/platform/errors"
)

const (
	ClusterIP               = "10.43.0.1"
	KubernetesServiceCIDR   = "10.43.0.1/32"
	ManualAPIServerCIDR     = "10.43.0.1/16"
	NormalizedAPIServerCIDR = "10.43.0.0/16"
)

func TestAPIServerDiscovery_DiscoverServiceNetworkCIDR(t *testing.T) {
	kubernetesService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubernetesServiceName,
			Namespace: kubernetesServiceNamespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: ClusterIP,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(kubernetesService).
		WithReturnManagedFields().
		Build()

	discovery := newAPIServerDiscovery(k8sClient)
	got, err := discovery.DiscoverServiceNetworkCIDR(context.Background())
	if err != nil {
		t.Fatalf("DiscoverServiceNetworkCIDR() error: %v", err)
	}
	if got != KubernetesServiceCIDR {
		t.Fatalf("DiscoverServiceNetworkCIDR() = %q, expected %q", got, KubernetesServiceCIDR)
	}
}

func TestIPToSingleHostCIDR(t *testing.T) {
	for address, want := range map[string]string{
		"10.96.0.1":        "10.96.0.1/32",
		"fd00:10:96::1":    "fd00:10:96::1/128",
		"::ffff:10.96.0.1": "10.96.0.1/32",
		"not-an-ip":        "",
	} {
		got, err := ipToSingleHostCIDR(address)
		if got != want || (err != nil) != (want == "") {
			t.Errorf("ipToSingleHostCIDR(%q) = %q, %v; want %q", address, got, err, want)
		}
	}
}

func TestDetectAPIServerInfoIPv6Primary(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "fd00:10:96::1")
	c := fake.NewClientBuilder().WithScheme(testScheme).Build()
	m := &Manager{client: c, reader: c}
	cluster := &openbaov1alpha1.OpenBaoCluster{Spec: openbaov1alpha1.OpenBaoClusterSpec{
		Network: &openbaov1alpha1.NetworkConfig{APIServerEndpointIPs: []string{"192.0.2.1", "2001:db8::1"}},
	}}
	info, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if info.ServiceNetworkCIDR != "fd00:10:96::1/128" || strings.Join(info.EndpointIPs, ",") != "192.0.2.1,2001:db8::1" {
		t.Fatalf("API server egress = %+v, want IPv6 Service VIP and both declared endpoint families", info)
	}
}

func TestDetectAPIServerInfo_ManualCIDRIsNormalized(t *testing.T) {
	m := &Manager{client: fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR: ManualAPIServerCIDR,
			},
		},
	}

	info, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("detectAPIServerInfo() error: %v", err)
	}
	if info.ServiceNetworkCIDR != NormalizedAPIServerCIDR {
		t.Fatalf("ServiceNetworkCIDR=%q, expected %q", info.ServiceNetworkCIDR, NormalizedAPIServerCIDR)
	}
}

func TestDetectAPIServerInfo_InvalidManualCIDRErrors(t *testing.T) {
	m := &Manager{client: fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR: "not-a-cidr",
			},
		},
	}

	_, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestDetectAPIServerInfo_ManualEndpointIPsSkipDiscovery(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", ClusterIP)
	m := &Manager{client: fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerEndpointIPs: []string{" 192.168.1.2 ", ""},
			},
		},
	}

	info, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("detectAPIServerInfo() error: %v", err)
	}
	if len(info.EndpointIPs) != 1 || info.EndpointIPs[0] != "192.168.1.2" {
		t.Fatalf("EndpointIPs=%v, expected %v", info.EndpointIPs, []string{"192.168.1.2"})
	}
}

func TestDetectAPIServerInfo_ServiceGetNotFoundWithManualCIDRSucceeds(t *testing.T) {
	m := &Manager{client: fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR: KubernetesServiceCIDR,
			},
		},
	}

	info, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err != nil {
		t.Fatalf("detectAPIServerInfo() error: %v", err)
	}
	if info.ServiceNetworkCIDR != KubernetesServiceCIDR {
		t.Fatalf("ServiceNetworkCIDR=%q, expected %q", info.ServiceNetworkCIDR, KubernetesServiceCIDR)
	}
}

func TestDetectAPIServerInfo_ServiceGetNotFoundWithoutManualCIDRErrors(t *testing.T) {
	m := &Manager{client: fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()}
	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{},
	}

	_, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !apierrors.IsNotFound(err) {
		// The error is wrapped; verify the root cause is NotFound.
		var statusErr *apierrors.StatusError
		if !errors.As(err, &statusErr) || statusErr.ErrStatus.Reason != metav1.StatusReasonNotFound {
			t.Fatalf("expected a NotFound root cause, got: %v", err)
		}
	}
}

func TestDetectAPIServerInfo_InvalidManualEndpointIPEntryErrors(t *testing.T) {
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()
	m := &Manager{client: k8sClient}

	cluster := &openbaov1alpha1.OpenBaoCluster{
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerEndpointIPs: []string{"not-an-ip"},
			},
		},
	}

	_, err := m.detectAPIServerInfo(context.Background(), logr.Discard(), cluster)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestEnsureNetworkPolicy_APIServerDiscoveryFailureIsPermanentConfig(t *testing.T) {
	cluster := &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithReturnManagedFields().
		Build()

	m := &Manager{
		client:            k8sClient,
		reader:            k8sClient,
		scheme:            testScheme,
		operatorNamespace: "openbao-operator-system",
	}

	err := m.ensureNetworkPolicy(context.Background(), logr.Discard(), cluster)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrAPIServerNetworkConfigurationInvalid) {
		t.Fatalf("expected ErrAPIServerNetworkConfigurationInvalid, got %v", err)
	}
	if !errors.Is(err, operatorerrors.ErrPermanentConfig) {
		t.Fatalf("expected permanent config error, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "spec.network.apiServerEndpointIPs") {
		t.Fatalf("error %q does not mention apiServerEndpointIPs", got)
	}
}
