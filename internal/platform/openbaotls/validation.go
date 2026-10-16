package openbaotls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// ValidateCABundle validates that the PEM data contains at least one
// certificate suitable for use as a trust bundle.
func ValidateCABundle(pemBytes []byte) error {
	if len(pemBytes) == 0 {
		return fmt.Errorf("ca.crt is empty")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return fmt.Errorf("ca.crt is not valid PEM certificate data")
	}
	if err := cryptopolicy.ValidatePEMCertificates(pemBytes); err != nil {
		return fmt.Errorf("ca.crt key policy: %w", err)
	}
	return nil
}

// ValidateServerSecret parses and validates the server TLS Secret contents,
// returning the leaf certificate on success.
func ValidateServerSecret(secret *corev1.Secret) (*x509.Certificate, error) {
	if secret == nil {
		return nil, fmt.Errorf("server TLS Secret is nil")
	}

	certPEM := secret.Data["tls.crt"]
	if len(certPEM) == 0 {
		return nil, fmt.Errorf("tls.crt is missing or empty")
	}
	keyPEM := secret.Data["tls.key"]
	if len(keyPEM) == 0 {
		return nil, fmt.Errorf("tls.key is missing or empty")
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tls.crt/tls.key do not form a valid key pair: %w", err)
	}
	if err := cryptopolicy.ValidateTLSCertificate(&pair); err != nil {
		return nil, fmt.Errorf("tls.crt key policy: %w", err)
	}

	cert, err := parseLeafCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("tls.crt is not a valid X.509 certificate: %w", err)
	}
	return cert, nil
}

// ValidateExternalServerSecret ensures the externally managed TLS assets are
// usable for operator probes and day-2 workflows.
func ValidateExternalServerSecret(cluster *openbaov1alpha1.OpenBaoCluster, caSecret *corev1.Secret, serverSecret *corev1.Secret) error {
	if caSecret == nil {
		return fmt.Errorf("CA Secret is nil")
	}
	caPEM := caSecret.Data["ca.crt"]
	if err := ValidateCABundle(caPEM); err != nil {
		return fmt.Errorf("CA Secret ca.crt is invalid: %w", err)
	}

	cert, err := ValidateServerSecret(serverSecret)
	if err != nil {
		return err
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("CA Secret ca.crt is not valid PEM certificate data")
	}

	serverName := portopenbao.ComputeTLSServerName(cluster)
	if serverName == "" {
		return fmt.Errorf("failed to determine internal TLS server name")
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		DNSName: serverName,
		Roots:   roots,
	}); err != nil {
		return fmt.Errorf("server certificate is not trusted for %q: %w", serverName, err)
	}

	requiredDNS, requiredIPs := requiredExternalTLSSubjectAltNames(cluster)
	if err := validateRequiredSANs(cert, requiredDNS, requiredIPs); err != nil {
		return err
	}

	return nil
}

func requiredExternalTLSSubjectAltNames(cluster *openbaov1alpha1.OpenBaoCluster) ([]string, []net.IP) {
	if cluster == nil {
		return nil, nil
	}

	dnsSeen := map[string]struct{}{}
	var dnsNames []string
	addDNS := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := dnsSeen[name]; ok {
			return
		}
		dnsSeen[name] = struct{}{}
		dnsNames = append(dnsNames, name)
	}

	ipSeen := map[string]struct{}{}
	var ips []net.IP
	addIP := func(raw string) {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil {
			return
		}
		key := ip.String()
		if _, ok := ipSeen[key]; ok {
			return
		}
		ipSeen[key] = struct{}{}
		ips = append(ips, ip)
	}

	addDNS(portopenbao.ComputeTLSServerName(cluster))
	if cluster.Spec.Ingress != nil && cluster.Spec.Ingress.Enabled {
		addDNS(cluster.Spec.Ingress.Host)
	}
	if cluster.Spec.Gateway != nil && cluster.Spec.Gateway.Enabled {
		addDNS(cluster.Spec.Gateway.Hostname)
	}
	for _, san := range cluster.Spec.TLS.ExtraSANs {
		if net.ParseIP(strings.TrimSpace(san)) != nil {
			addIP(san)
			continue
		}
		addDNS(san)
	}

	return dnsNames, ips
}

func validateRequiredSANs(cert *x509.Certificate, requiredDNS []string, requiredIPs []net.IP) error {
	dnsSet := map[string]struct{}{}
	for _, name := range cert.DNSNames {
		dnsSet[name] = struct{}{}
	}
	for _, name := range requiredDNS {
		if _, ok := dnsSet[name]; !ok {
			return fmt.Errorf("server certificate is missing required DNS SAN %q", name)
		}
	}

	ipSet := map[string]struct{}{}
	for _, ip := range cert.IPAddresses {
		if ip != nil {
			ipSet[ip.String()] = struct{}{}
		}
	}
	for _, ip := range requiredIPs {
		if ip == nil {
			continue
		}
		if _, ok := ipSet[ip.String()]; !ok {
			return fmt.Errorf("server certificate is missing required IP SAN %q", ip.String())
		}
	}

	return nil
}

func parseLeafCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, rest := pemDecodeCertificate(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM certificate block found")
	}
	if len(rest) > 0 {
		_ = rest
	}
	return x509.ParseCertificate(block)
}

func pemDecodeCertificate(pemBytes []byte) ([]byte, []byte) {
	var certDER []byte
	rest := pemBytes
	for len(rest) > 0 {
		block, remaining := pem.Decode(rest)
		rest = remaining
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			certDER = block.Bytes
			break
		}
	}
	return certDER, rest
}
