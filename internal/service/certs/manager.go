package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	clusterpkg "github.com/kubebao/openbao-operator/internal/adapter/cluster"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	recon "github.com/kubebao/openbao-operator/internal/platform/reconcile"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	caCertKey  = "ca.crt"
	caKeyKey   = "ca.key"
	tlsCertKey = "tls.crt"
	tlsKeyKey  = "tls.key"

	caCertValidityYears    = 10
	serverCertValidityDays = 365
)

// Manager reconciles TLS-related Kubernetes resources for an OpenBaoCluster.
// It is responsible for bootstrapping and maintaining per-cluster CA and server
// certificate Secrets.
type Manager struct {
	client   client.Client
	scheme   *runtime.Scheme
	reloader ReloadSignaler
}

// ReloadSignaler requests a TLS reload for OpenBao versions before 2.7 when
// the server certificate changes. OpenBao 2.7 and later watch the files directly.
type ReloadSignaler interface {
	SignalReload(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, certHash string) error
}

type noopReloadSignaler struct{}

func (n noopReloadSignaler) SignalReload(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, certHash string) error {
	return nil
}

// NewManagerWithReloader constructs a Manager with the provided client, scheme, and reload signaler.
// When reloader is nil, a no-op implementation is used.
// The scheme is used to set OwnerReferences on created resources for garbage collection.
func NewManagerWithReloader(c client.Client, scheme *runtime.Scheme, r ReloadSignaler) *Manager {
	if r == nil {
		r = noopReloadSignaler{}
	}
	return &Manager{
		client:   c,
		scheme:   scheme,
		reloader: r,
	}
}

// Reconcile ensures TLS assets are aligned with the desired state for the given OpenBaoCluster.
//
// It bootstraps a per-cluster CA Secret and server certificate Secret, evaluates
// rotation based on the configured rotation window, and requests a reload via
// the ReloadSignaler for OpenBao versions before 2.7.
//
// When Mode is External, the operator does not generate or rotate certificates.
// It waits for external Secrets and requests a reload before OpenBao 2.7 when they change.
// When Mode is ACME, OpenBao manages certificates internally via its native ACME client.
func (m *Manager) Reconcile(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster) (recon.Result, error) {
	if !cluster.Spec.TLS.Enabled {
		logger.Info("TLS is disabled for OpenBaoCluster; skipping certificate reconciliation")
		return recon.Result{}, nil
	}

	metrics := newTLSMetrics(cluster.Namespace, cluster.Name)

	// Determine TLS mode, defaulting to OperatorManaged for backwards compatibility
	mode := cluster.Spec.TLS.Mode
	if mode == "" {
		mode = openbaov1alpha1.TLSModeOperatorManaged
	}

	// Handle ACME mode: OpenBao manages certificates internally, no operator action needed
	if mode == openbaov1alpha1.TLSModeACME {
		logger.Info("TLS mode is ACME; OpenBao manages certificates internally, skipping operator reconciliation")
		return recon.Result{}, nil
	}

	// Handle External mode: wait for secrets and trigger reload on changes
	if mode == openbaov1alpha1.TLSModeExternal {
		shouldRequeue, err := m.reconcileExternalTLS(ctx, logger, cluster, metrics)
		if err != nil {
			return recon.Result{}, err
		}
		if shouldRequeue {
			return recon.Result{RequeueAfter: constants.RequeueShort}, nil
		}
		return recon.Result{}, nil
	}

	// OperatorManaged mode: generate and rotate certificates
	// Always compute DNS SANs to ensure upgrade-strategy-specific pod names are included
	dnsSANs := clusterpkg.ComputeRequiredDNSSANs(cluster)
	return m.reconcileOperatorManagedTLS(ctx, logger, cluster, metrics, dnsSANs)
}

func caSecretName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + constants.SuffixTLSCA
}

func serverSecretName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + constants.SuffixTLSServer
}

func generateCA(cluster *openbaov1alpha1.OpenBaoCluster, now time.Time) ([]byte, []byte, error) {
	// Use ECDSA P-256 for better security and performance compared to RSA-2048
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate CA private key: %w", err)
	}

	serialNumber, err := randSerialNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate CA serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s OpenBao Root CA", cluster.Name),
			Organization: []string{"OpenBao Operator"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(caCertValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

func buildCASecret(cluster *openbaov1alpha1.OpenBaoCluster, name string, certPEM []byte, keyPEM []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster: cluster.Name,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			caCertKey: certPEM,
			caKeyKey:  keyPEM,
		},
	}
}

func parseCAFromSecret(secret *corev1.Secret) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPEM, ok := secret.Data[caCertKey]
	if !ok || len(certPEM) == 0 {
		return nil, nil, nil, fmt.Errorf("missing %q in CA Secret", caCertKey)
	}

	keyPEM, ok := secret.Data[caKeyKey]
	if !ok || len(keyPEM) == 0 {
		return nil, nil, nil, fmt.Errorf("missing %q in CA Secret", caKeyKey)
	}

	cert, err := parseCertificate(certPEM)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	privateKey, err := parseECDSAPrivateKey(keyPEM)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse CA private key: %w", err)
	}

	return cert, privateKey, certPEM, nil
}

func issueServerCertificate(cluster *openbaov1alpha1.OpenBaoCluster, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, now time.Time, additionalDNSNames []string) ([]byte, []byte, error) {
	// Use ECDSA P-256 for better security and performance compared to RSA-2048
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate server private key: %w", err)
	}

	serialNumber, err := randSerialNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate server certificate serial number: %w", err)
	}

	dnsNames, ipAddresses, sansErr := buildServerSANs(cluster, additionalDNSNames)
	if sansErr != nil {
		return nil, nil, fmt.Errorf("failed to compute SANs for server certificate: %w", sansErr)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s.openbao.svc", cluster.Name),
			Organization: []string{"OpenBao Operator"},
		},
		NotBefore: now.Add(-1 * time.Hour),
		NotAfter:  now.AddDate(0, 0, serverCertValidityDays),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// Include both ServerAuth and ClientAuth to support mutual TLS for retry_join
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create server certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

func buildServerSecret(cluster *openbaov1alpha1.OpenBaoCluster, name string, certPEM []byte, keyPEM []byte, caCertPEM []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
				constants.LabelOpenBaoCluster: cluster.Name,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			tlsCertKey: certPEM,
			tlsKeyKey:  keyPEM,
			caCertKey:  caCertPEM,
		},
	}
}

func randSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %w", err)
	}
	return serialNumber, nil
}

func parseCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return cert, nil
}

func parseECDSAPrivateKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to decode ECDSA private key PEM")
	}
	if block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("failed to decode ECDSA private key PEM (expected EC PRIVATE KEY, got %q)", block.Type)
	}

	privateKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ECDSA private key: %w", err)
	}

	return privateKey, nil
}

// buildServerSANs builds the Subject Alternative Names (SANs) for the server certificate.
// It includes DNS names based on the cluster configuration and user-provided ExtraSANs.
// Pod IPs are intentionally excluded because they are ephemeral in Kubernetes and would
// cause unnecessary certificate rotation when pods are recreated.
// additionalDNSNames are DNS names computed by the controller layer (e.g., upgrade-strategy-specific pod names).
func buildServerSANs(cluster *openbaov1alpha1.OpenBaoCluster, additionalDNSNames []string) ([]string, []net.IP, error) {
	dnsSet := map[string]struct{}{}
	ipSet := map[string]struct{}{}

	addDNS := func(name string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		if _, exists := dnsSet[name]; !exists {
			dnsSet[name] = struct{}{}
		}
	}

	addIP := func(ip net.IP) {
		if ip == nil {
			return
		}
		key := ip.String()
		if _, exists := ipSet[key]; !exists {
			ipSet[key] = struct{}{}
		}
	}

	namespace := strings.TrimSpace(cluster.Namespace)
	if namespace == "" {
		return nil, nil, fmt.Errorf("cluster namespace is required to build server certificate SANs")
	}

	clusterName := strings.TrimSpace(cluster.Name)

	addDNS("localhost")
	addIP(net.ParseIP("127.0.0.1"))

	// Add a common DNS SAN for auto-join with go-discover.
	// This common DNS name is used with leader_tls_servername in retry_join
	// to avoid certificate validation issues when go-discover returns IP addresses.
	// The DNS name does not need to be resolvable; it's just a common identifier
	// that all pods use for TLS validation during auto-join.
	if clusterName != "" {
		commonDNSName := fmt.Sprintf("openbao-cluster-%s.local", clusterName)
		addDNS(commonDNSName)
	}

	if clusterName != "" {
		// Add wildcards for all pods in the cluster
		// Include both .svc and .svc.cluster.local for comprehensive coverage
		addDNS(fmt.Sprintf("*.%s.%s.svc", clusterName, namespace))
		addDNS(fmt.Sprintf("*.%s.%s.svc.cluster.local", clusterName, namespace))
		// Add the headless service name
		addDNS(fmt.Sprintf("%s.%s.svc", clusterName, namespace))
		addDNS(fmt.Sprintf("%s.%s.svc.cluster.local", clusterName, namespace))
		// Add individual pod DNS names for explicit coverage
		// This ensures certificates work for retry_join even if wildcard matching has issues
		replicas := cluster.Spec.Replicas
		for i := int32(0); i < replicas; i++ {
			addDNS(fmt.Sprintf("%s-%d.%s.%s.svc", clusterName, i, clusterName, namespace))
			addDNS(fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", clusterName, i, clusterName, namespace))
		}
	}

	// Add additional DNS names provided by the controller (e.g., upgrade-strategy-specific pod names)
	for _, dnsName := range additionalDNSNames {
		addDNS(dnsName)
	}

	// Add namespace-wide wildcards for both DNS suffixes
	addDNS(fmt.Sprintf("*.%s.svc", namespace))
	addDNS(fmt.Sprintf("*.%s.svc.cluster.local", namespace))

	// Add external service DNS name if service, ingress, or gateway is configured
	// This is needed when ingress controllers or gateways connect to the backend service
	if clusterName != "" {
		needsExternalService := (cluster.Spec.Service != nil) ||
			(cluster.Spec.Ingress != nil && cluster.Spec.Ingress.Enabled) ||
			(cluster.Spec.Gateway != nil && cluster.Spec.Gateway.Enabled)
		if needsExternalService {
			externalServiceName := fmt.Sprintf("%s-public.%s.svc", clusterName, namespace)
			addDNS(externalServiceName)
		}
	}

	if cluster.Spec.Ingress != nil && cluster.Spec.Ingress.Enabled && strings.TrimSpace(cluster.Spec.Ingress.Host) != "" {
		addDNS(strings.TrimSpace(cluster.Spec.Ingress.Host))
	}

	// Add Gateway hostname to TLS SANs if Gateway API is enabled
	if cluster.Spec.Gateway != nil && cluster.Spec.Gateway.Enabled && strings.TrimSpace(cluster.Spec.Gateway.Hostname) != "" {
		addDNS(strings.TrimSpace(cluster.Spec.Gateway.Hostname))
	}

	for _, san := range cluster.Spec.TLS.ExtraSANs {
		san = strings.TrimSpace(san)
		if ip := net.ParseIP(san); ip != nil {
			addIP(ip)
			continue
		}
		addDNS(san)
	}

	// Note: Pod IPs are intentionally NOT included in certificate SANs because Pod IPs
	// are ephemeral in Kubernetes. Every time a Pod is recreated (e.g., node drain, upgrade),
	// its IP changes, which would force unnecessary certificate rotation. We rely on stable
	// DNS entries provided by the StatefulSet (pod-ordinal.service-name.namespace.svc) and
	// the Service ClusterIP, which are already included above.

	dnsNames := make([]string, 0, len(dnsSet))
	for name := range dnsSet {
		dnsNames = append(dnsNames, name)
	}

	ipAddresses := make([]net.IP, 0, len(ipSet))
	for key := range ipSet {
		ipAddresses = append(ipAddresses, net.ParseIP(key))
	}

	return dnsNames, ipAddresses, nil
}

func parseServerCertificateFromSecret(secret *corev1.Secret) (*x509.Certificate, error) {
	certPEM, ok := secret.Data[tlsCertKey]
	if !ok || len(certPEM) == 0 {
		return nil, fmt.Errorf("missing %q in server TLS Secret", tlsCertKey)
	}

	return parseCertificate(certPEM)
}

// certSANsMatch checks if the certificate contains all expected DNS names and IP addresses.
// It returns false if any expected SAN is missing from the certificate.
func certSANsMatch(cert *x509.Certificate, expectedDNS []string, expectedIPs []net.IP) bool {
	// Build sets of DNS names and IPs from the certificate
	certDNSSet := make(map[string]struct{})
	for _, dns := range cert.DNSNames {
		certDNSSet[dns] = struct{}{}
	}

	certIPSet := make(map[string]struct{})
	for _, ip := range cert.IPAddresses {
		if ip != nil {
			certIPSet[ip.String()] = struct{}{}
		}
	}

	// Check that all expected DNS names are present
	for _, expectedDNS := range expectedDNS {
		if _, found := certDNSSet[expectedDNS]; !found {
			return false
		}
	}

	// Check that all expected IP addresses are present
	for _, expectedIP := range expectedIPs {
		if expectedIP == nil {
			continue
		}
		if _, found := certIPSet[expectedIP.String()]; !found {
			return false
		}
	}

	return true
}

func shouldRotateServerCert(cert *x509.Certificate, now time.Time, rotationPeriod string) (bool, error) {
	if rotationPeriod == "" {
		return false, nil
	}

	duration, err := time.ParseDuration(rotationPeriod)
	if err != nil {
		return false, fmt.Errorf("invalid TLS rotation period %q: %w", rotationPeriod, err)
	}

	remaining := cert.NotAfter.Sub(now)
	return remaining < duration, nil
}

func (m *Manager) signalReloadIfNeeded(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, certPEM []byte) error {
	if portopenbao.UsesNativeTLSAutoReload(cluster) {
		return nil
	}
	if len(certPEM) == 0 {
		return nil
	}

	sum := sha256.Sum256(certPEM)
	if m.reloader == nil {
		return nil
	}

	if err := m.reloader.SignalReload(ctx, logger, cluster, fmt.Sprintf("%x", sum[:])); err != nil {
		return fmt.Errorf("failed to signal TLS reload for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
	}

	return nil
}
