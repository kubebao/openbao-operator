//go:build integration

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/statusops"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/certs"
)

func TestExternalTLSChainReadinessRecovers(t *testing.T) {
	namespace := newTestNamespace(t)
	cluster := newMinimalClusterObj(namespace, "external-chain")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeExternal
	require.NoError(t, k8sClient.Create(ctx, cluster))
	root, intermediate, leaf, key := externalTLSChainPEM(t, portopenbao.ComputeTLSServerName(cluster))
	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name + "-tls-ca", Namespace: namespace},
		Data:       map[string][]byte{"ca.crt": root},
	}
	serverSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.Name + "-tls-server", Namespace: namespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": leaf, "tls.key": key},
	}
	require.NoError(t, k8sClient.Create(ctx, caSecret))
	require.NoError(t, k8sClient.Create(ctx, serverSecret))
	manager := certs.NewManagerWithReloader(k8sClient, k8sScheme, nil)

	checkReadiness := func(ready bool) {
		t.Helper()
		result := statusops.EvaluateTLSReadiness(ctx, k8sClient, cluster)
		reconcileResult, err := manager.Reconcile(ctx, logr.Discard(), cluster)
		require.NoError(t, err)
		if ready {
			require.Equal(t, metav1.ConditionTrue, result.Status)
			require.Zero(t, reconcileResult.RequeueAfter)
		} else {
			require.Equal(t, metav1.ConditionFalse, result.Status)
			require.Equal(t, "TLSSecretInvalid", result.Reason)
			require.Positive(t, reconcileResult.RequeueAfter)
		}
	}
	checkReadiness(false)

	serverSecret.Data["tls.crt"] = append(append([]byte{}, leaf...), intermediate...)
	require.NoError(t, k8sClient.Update(ctx, serverSecret))
	checkReadiness(true)
	unchanged := &corev1.Secret{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(serverSecret), unchanged))
	require.Equal(t, serverSecret.ResourceVersion, unchanged.ResourceVersion,
		"validating an external chain must not modify the provider's Secret")

	// A root supplied in the serving chain must not override the configured trust bundle.
	serverSecret.Data["tls.crt"] = append(serverSecret.Data["tls.crt"], root...)
	require.NoError(t, k8sClient.Update(ctx, serverSecret))
	otherRoot, _, _, _ := externalTLSChainPEM(t, portopenbao.ComputeTLSServerName(cluster))
	caSecret.Data["ca.crt"] = otherRoot
	require.NoError(t, k8sClient.Update(ctx, caSecret))
	checkReadiness(false)
	caSecret.Data["ca.crt"] = root
	require.NoError(t, k8sClient.Update(ctx, caSecret))
	checkReadiness(true)
}

func externalTLSChainPEM(t *testing.T, serverName string) (root, intermediate, leaf, key []byte) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		return key
	}
	rootKey, intermediateKey, leafKey := newKey(), newKey(), newKey()
	now := time.Now()
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "external-root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	intermediateTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "external-intermediate"},
		NotBefore: rootTemplate.NotBefore, NotAfter: rootTemplate.NotAfter,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3), NotBefore: rootTemplate.NotBefore, NotAfter: rootTemplate.NotAfter,
		DNSNames: []string{serverName}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootKey.Public(), rootKey)
	require.NoError(t, err)
	intermediateDER, err := x509.CreateCertificate(
		rand.Reader, intermediateTemplate, rootTemplate, intermediateKey.Public(), rootKey,
	)
	require.NoError(t, err)
	leafDER, err := x509.CreateCertificate(
		rand.Reader, leafTemplate, intermediateTemplate, leafKey.Public(), intermediateKey,
	)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
