//go:build apimigration_e2e

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	e2eKMIPServer    = "migration-kmip"
	e2eKMIPServerTLS = "migration-kmip-server-tls"
	e2eKMIPClientCN  = "openbao-kmip-client"
)

// prepareKMIP matches the mTLS and seeded-key configuration in the native KMIP
// suite. The server remains external to both API groups throughout the move.
func (f *e2eFixture) prepareKMIP() {
	f.t.Helper()
	require.NotEmpty(f.t, f.images["kmip"], "load the explicit PyKMIP fixture image before preparing the seal")
	ctx := context.Background()
	_, c := f.restClient()
	ca, caKey, caPEM := f.kmipCertificate(&x509.Certificate{
		Subject:               pkix.Name{CommonName: "API Migration KMIP Fixture CA"},
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}, nil, nil)
	serverName := e2eKMIPServer + "." + e2eDataNS
	_, serverKey, serverPEM := f.kmipCertificate(&x509.Certificate{
		Subject:     pkix.Name{CommonName: serverName + ".svc"},
		DNSNames:    []string{e2eKMIPServer, serverName, serverName + ".svc", serverName + ".svc.cluster.local"},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, caKey)
	_, clientKey, clientPEM := f.kmipCertificate(&x509.Certificate{
		Subject:     pkix.Name{CommonName: e2eKMIPClientCN},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, ca, caKey)
	for _, secret := range []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: e2eSealCredentials, Namespace: e2eDataNS},
			Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
				"ca.crt": caPEM, "client.crt": clientPEM, "client.key": kmipPrivateKeyPEM(clientKey)}},
		{ObjectMeta: metav1.ObjectMeta{Name: e2eKMIPServerTLS, Namespace: e2eDataNS},
			Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
				"ca.crt": caPEM, "server.crt": serverPEM, "server.key": kmipPrivateKeyPEM(serverKey)}},
	} {
		require.NoError(f.t, c.Create(ctx, secret))
	}
	labels := map[string]string{"app": e2eKMIPServer}
	require.NoError(f.t, c.Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: e2eKMIPServer, Namespace: e2eDataNS, Labels: labels},
		Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{
			{Name: "kmip", Protocol: corev1.ProtocolTCP, Port: 5696, TargetPort: intstr.FromInt(5696)}}},
	}))
	require.NoError(f.t, c.Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: e2eKMIPServer, Namespace: e2eDataNS, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{FSGroup: ptr.To(int64(1000))},
					Containers: []corev1.Container{{
						Name: "pykmip", Image: f.images["kmip"], ImagePullPolicy: corev1.PullIfNotPresent,
						Env:   []corev1.EnvVar{{Name: "KMIP_KEY_ID", Value: "1"}, {Name: "KMIP_KEY_OWNER", Value: e2eKMIPClientCN}},
						Ports: []corev1.ContainerPort{{Name: "kmip", ContainerPort: 5696, Protocol: corev1.ProtocolTCP}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(5696)}},
							InitialDelaySeconds: 2, PeriodSeconds: 2},
						LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(5696)}},
							InitialDelaySeconds: 10, PeriodSeconds: 10},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "tls", MountPath: "/etc/pykmip/tls", ReadOnly: true},
							{Name: "data", MountPath: "/var/lib/pykmip"}, {Name: "tmp", MountPath: "/tmp"}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), RunAsNonRoot: ptr.To(true),
							RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)),
							Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("96Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}},
					}},
					Volumes: []corev1.Volume{
						{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: e2eKMIPServerTLS, DefaultMode: ptr.To(int32(0o440))}}},
						{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				},
			},
		},
	}))
	f.eventually("the external mTLS KMIP server to become ready", 5*time.Minute, func() bool {
		deployment := &appsv1.Deployment{}
		return c.Get(ctx, types.NamespacedName{Namespace: e2eDataNS, Name: e2eKMIPServer}, deployment) == nil &&
			deployment.Status.ObservedGeneration == deployment.Generation && deployment.Status.ReadyReplicas == 1
	})
}

func (f *e2eFixture) kmipCertificate(template, parent *x509.Certificate, signer *rsa.PrivateKey) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	f.t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(f.t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(f.t, err)
	template.SerialNumber, template.NotBefore, template.NotAfter = serial, time.Now().Add(-time.Hour), time.Now().AddDate(0, 0, 30)
	if parent == nil {
		parent, signer = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	require.NoError(f.t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(f.t, err)
	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func kmipPrivateKeyPEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
