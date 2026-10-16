package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/cryptopolicy"
)

func (m *Manager) validateTransitUnsealPrerequisites(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster) error {
	cfg := cluster.Spec.Unseal
	if cfg == nil || cfg.Transit == nil {
		return nil
	}

	if err := validateTransitAddress(cfg.Transit.Address); err != nil {
		return transitPrerequisitesError(err)
	}
	if err := validateTransitClientTLSPair(cfg.Transit); err != nil {
		return transitPrerequisitesError(err)
	}

	fileRefs, err := transitTLSSecretFileRefs(cfg.Transit)
	if err != nil {
		return transitPrerequisitesError(err)
	}

	requiredSecretKeys := transitRequiredSecretKeys(cluster)
	if cfg.CredentialsSecretRef == nil {
		if len(requiredSecretKeys) == 0 {
			return nil
		}
		return transitPrerequisitesError(
			fmt.Errorf(
				"transit unseal requires spec.unseal.credentialsSecretRef because the configuration references Secret-backed credential keys %s",
				strings.Join(requiredSecretKeys, ", "),
			),
		)
	}

	var secret corev1.Secret
	secretName := types.NamespacedName{Namespace: cluster.Namespace, Name: cfg.CredentialsSecretRef.Name}
	if err := m.reader.Get(ctx, secretName, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return transitPrerequisitesError(fmt.Errorf("transit credentials Secret %s/%s not found", cluster.Namespace, cfg.CredentialsSecretRef.Name))
		}
		return fmt.Errorf("failed to read transit credentials Secret %s/%s: %w", cluster.Namespace, cfg.CredentialsSecretRef.Name, err)
	}

	missingKeys := make([]string, 0, len(requiredSecretKeys))
	for _, key := range requiredSecretKeys {
		if _, ok := secret.Data[key]; ok {
			continue
		}
		missingKeys = append(missingKeys, key)
	}
	if len(missingKeys) == 0 {
		if err := validateTransitSecretData(cfg.Transit, secret.Data, fileRefs); err != nil {
			return transitPrerequisitesError(err)
		}
		return nil
	}

	return transitPrerequisitesError(
		fmt.Errorf(
			"transit credentials Secret %s/%s is missing required keys %s",
			cluster.Namespace,
			cfg.CredentialsSecretRef.Name,
			strings.Join(missingKeys, ", "),
		),
	)
}

func validateTransitAddress(address string) error {
	trimmed := strings.TrimSpace(address)
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("spec.unseal.transit.address must be a valid absolute URL: %w", err)
	}
	if strings.TrimSpace(u.Scheme) == "" || strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("spec.unseal.transit.address must be a valid absolute URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("spec.unseal.transit.address must use HTTPS")
	}
	if u.User != nil {
		return fmt.Errorf("spec.unseal.transit.address must not include userinfo")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("spec.unseal.transit.address must not include query or fragment components")
	}

	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return fmt.Errorf("spec.unseal.transit.address must include a host")
	}
	if isLocalhostName(host) {
		return fmt.Errorf("spec.unseal.transit.address must not point to localhost")
	}
	if isNumericHostname(host) {
		return fmt.Errorf("spec.unseal.transit.address must not use numeric host forms")
	}
	if strings.Contains(host, "%") {
		return fmt.Errorf("spec.unseal.transit.address must not include scoped IP zone identifiers")
	}
	if addr, err := netip.ParseAddr(host); err == nil && isForbiddenTransitAddress(addr) {
		return fmt.Errorf("spec.unseal.transit.address must not point to loopback, link-local, or unspecified addresses")
	}
	return nil
}

func isLocalhostName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func isForbiddenTransitAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsUnspecified()
}

func isNumericHostname(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	for _, r := range host {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validateUnsealCredentialsSecretRef(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster == nil || cluster.Spec.Unseal == nil || cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}

	name := strings.TrimSpace(cluster.Spec.Unseal.CredentialsSecretRef.Name)
	if name == "" {
		return fmt.Errorf("spec.unseal.credentialsSecretRef.name must not be empty")
	}
	if isOperatorManagedSystemSecretName(name) {
		return fmt.Errorf(
			"spec.unseal.credentialsSecretRef must not reference operator-managed system Secret %q",
			name,
		)
	}
	return nil
}

func isOperatorManagedSystemSecretName(name string) bool {
	for _, suffix := range []string{
		constants.SuffixUnsealKey,
		constants.SuffixRootToken,
		constants.SuffixTLSCA,
		constants.SuffixTLSServer,
	} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func validateTransitClientTLSPair(cfg *openbaov1alpha1.TransitSealConfig) error {
	if cfg == nil {
		return nil
	}

	hasClientCert := strings.TrimSpace(cfg.TLSClientCert) != ""
	hasClientKey := strings.TrimSpace(cfg.TLSClientKey) != ""
	if hasClientCert == hasClientKey {
		return nil
	}

	return fmt.Errorf("spec.unseal.transit.tlsClientCert and spec.unseal.transit.tlsClientKey must be set together")
}

type transitTLSSecretFileRef struct {
	fieldName string
	key       string
}

type secretFileRef = transitTLSSecretFileRef

func transitTLSSecretFileRefs(cfg *openbaov1alpha1.TransitSealConfig) ([]transitTLSSecretFileRef, error) {
	if cfg == nil {
		return nil, nil
	}

	out := make([]transitTLSSecretFileRef, 0, 3)
	for _, candidate := range []struct {
		fieldName string
		path      string
	}{
		{fieldName: "tlsCACert", path: cfg.TLSCACert},
		{fieldName: "tlsClientCert", path: cfg.TLSClientCert},
		{fieldName: "tlsClientKey", path: cfg.TLSClientKey},
	} {
		if strings.TrimSpace(candidate.path) == "" {
			continue
		}

		key, ok := mountedSealCredentialsKey(candidate.path)
		if !ok {
			return nil, fmt.Errorf(
				"spec.unseal.transit.%s must reference a file under %s",
				candidate.fieldName,
				sealCredsVolumeMountPath,
			)
		}

		out = append(out, transitTLSSecretFileRef{
			fieldName: candidate.fieldName,
			key:       key,
		})
	}

	return out, nil
}

func validateTransitSecretData(
	cfg *openbaov1alpha1.TransitSealConfig,
	secretData map[string][]byte,
	fileRefs []transitTLSSecretFileRef,
) error {
	if cfg == nil {
		return nil
	}

	if strings.TrimSpace(cfg.Token) == "" && len(strings.TrimSpace(string(secretData["token"]))) == 0 {
		return fmt.Errorf("transit credentials Secret key %q must contain a non-empty token", "token")
	}

	refByField := make(map[string]string, len(fileRefs))
	for _, ref := range fileRefs {
		refByField[ref.fieldName] = ref.key
	}

	if key, ok := refByField["tlsCACert"]; ok {
		if err := validateTransitCAPEM(secretData[key]); err != nil {
			return fmt.Errorf("spec.unseal.transit.tlsCACert (%s) is invalid: %w", key, err)
		}
	}

	clientCertKey, hasClientCert := refByField["tlsClientCert"]
	clientKeyKey, hasClientKey := refByField["tlsClientKey"]
	if hasClientCert && hasClientKey {
		if err := validateTransitClientKeyPairPEM(secretData[clientCertKey], secretData[clientKeyKey]); err != nil {
			return fmt.Errorf(
				"spec.unseal.transit.tlsClientCert (%s) and spec.unseal.transit.tlsClientKey (%s) are invalid: %w",
				clientCertKey,
				clientKeyKey,
				err,
			)
		}
	}

	return nil
}

func validateTransitCAPEM(data []byte) error {
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("PEM bundle is empty")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return fmt.Errorf("expected one or more PEM certificates")
	}
	return cryptopolicy.ValidatePEMCertificates(data)
}

func validateTransitClientKeyPairPEM(certPEM, keyPEM []byte) error {
	if err := validateTransitCAPEM(certPEM); err != nil {
		return fmt.Errorf("client certificate is invalid: %w", err)
	}
	if len(strings.TrimSpace(string(keyPEM))) == 0 {
		return fmt.Errorf("client private key is empty")
	}
	if block, _ := pem.Decode(keyPEM); block == nil {
		return fmt.Errorf("client private key is not valid PEM")
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("client certificate and private key do not form a valid key pair: %w", err)
	}
	return nil
}

func transitRequiredSecretKeys(cluster *openbaov1alpha1.OpenBaoCluster) []string {
	if cluster == nil || cluster.Spec.Unseal == nil || cluster.Spec.Unseal.Transit == nil {
		return nil
	}

	required := make([]string, 0, 4)
	if strings.TrimSpace(cluster.Spec.Unseal.Transit.Token) == "" {
		required = append(required, "token")
	}

	for _, filePath := range []string{
		cluster.Spec.Unseal.Transit.TLSCACert,
		cluster.Spec.Unseal.Transit.TLSClientCert,
		cluster.Spec.Unseal.Transit.TLSClientKey,
	} {
		key, ok := mountedSealCredentialsKey(filePath)
		if !ok {
			continue
		}
		required = append(required, key)
	}

	slices.Sort(required)
	return slices.Compact(required)
}
