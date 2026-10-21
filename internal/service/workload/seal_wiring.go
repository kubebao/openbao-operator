package workload

import (
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

const (
	sealCredsVolumeName      = "seal-creds"
	sealCredsVolumeMountPath = "/etc/bao/seal-creds" // #nosec G101 -- False positive: path, not a secret

	// The ServiceAccount token Entra ID exchanges for the Azure Key Vault seal's
	// workload identity, projected with the audience Azure expects. Same path and
	// lifetime as the Azure Workload Identity webhook uses.
	azureIdentityTokenVolumeName      = constants.VolumeAzureIdentityToken
	azureIdentityTokenVolumeMountPath = "/var/run/secrets/azure/tokens" // #nosec G101 -- False positive: path, not a secret
	azureIdentityTokenPath            = "azure-identity-token"
	azureIdentityTokenAudience        = "api://AzureADTokenExchange"
	azureIdentityTokenExpirationSecs  = int64(3600)
)

type sealWiringProvider interface {
	EnvVars() []corev1.EnvVar
	VolumeMounts() []corev1.VolumeMount
	Volumes() []corev1.Volume
}

func envVarFromCredentialsSecret(cluster *openbaov1alpha1.OpenBaoCluster, envName string, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cluster.Spec.Unseal.CredentialsSecretRef.Name,
				},
				Key:      secretKey,
				Optional: ptr.To(true),
			},
		},
	}
}

func newSealWiringProvider(cluster *openbaov1alpha1.OpenBaoCluster) sealWiringProvider {
	if usesStaticSeal(cluster) {
		return &staticSealWiringProvider{cluster: cluster}
	}

	switch cluster.Spec.Unseal.Type {
	case portopenbao.SealTypeTransit:
		return &transitSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypeGCPCKMS:
		return &gcpCKMSSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypeAWSKMS:
		return &awsKMSSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypeAzureKeyVault:
		return &azureKeyVaultSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypeKMIP:
		return &kmipSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypeKMSPlugin:
		return &credentialsSecretSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypeOCIKMS:
		return &ociKMSSealWiringProvider{cluster: cluster}
	case portopenbao.SealTypePKCS11:
		return &pkcs11SealWiringProvider{cluster: cluster}
	default:
		// Preserve current behavior: treat unknown non-static seal types as requiring
		// only credentials Secret wiring (if provided).
		return &credentialsSecretSealWiringProvider{cluster: cluster}
	}
}

type staticSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *staticSealWiringProvider) EnvVars() []corev1.EnvVar { return nil }

func (p *staticSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      unsealVolumeName,
			MountPath: openBaoUnsealMountPath,
			ReadOnly:  true,
		},
	}
}

func (p *staticSealWiringProvider) Volumes() []corev1.Volume {
	secretName := resourceidentity.UnsealSecretName(p.cluster)
	if p.cluster.Spec.Unseal != nil && p.cluster.Spec.Unseal.CredentialsSecretRef != nil {
		secretName = p.cluster.Spec.Unseal.CredentialsSecretRef.Name
	}

	return []corev1.Volume{
		{
			Name: unsealVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  secretName,
					DefaultMode: ptr.To(secretFileMode),
				},
			},
		},
	}
}

// credentialsSecretSealWiringProvider wires an optional credentials Secret into the
// pod as a volume. Most external seal types don't require additional env wiring.
type credentialsSecretSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *credentialsSecretSealWiringProvider) EnvVars() []corev1.EnvVar { return nil }

func (p *credentialsSecretSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}
	return []corev1.VolumeMount{
		{
			Name:      sealCredsVolumeName,
			MountPath: sealCredsVolumeMountPath,
			ReadOnly:  true,
		},
	}
}

func (p *credentialsSecretSealWiringProvider) Volumes() []corev1.Volume {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}
	return []corev1.Volume{
		{
			Name: sealCredsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  p.cluster.Spec.Unseal.CredentialsSecretRef.Name,
					DefaultMode: ptr.To(secretFileMode),
				},
			},
		},
	}
}

type awsKMSSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *awsKMSSealWiringProvider) EnvVars() []corev1.EnvVar {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}

	return []corev1.EnvVar{
		envVarFromCredentialsSecret(p.cluster, envAWSAccessKeyID, envAWSAccessKeyID),
		envVarFromCredentialsSecret(p.cluster, envAWSSecretAccessKey, envAWSSecretAccessKey),
		envVarFromCredentialsSecret(p.cluster, envAWSSessionToken, envAWSSessionToken),
	}
}

func (p *awsKMSSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).VolumeMounts()
}

func (p *awsKMSSealWiringProvider) Volumes() []corev1.Volume {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).Volumes()
}

type azureKeyVaultSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *azureKeyVaultSealWiringProvider) EnvVars() []corev1.EnvVar {
	if identity := p.workloadIdentity(); identity != nil {
		// The seal reads the tenant from its own configuration, but the Azure SDK's
		// workload identity credential only takes the client ID and the token file
		// from the environment. A credentials Secret still supplies the settings that
		// are not credentials, such as the resource of a Managed HSM, but none that
		// would compete with the workload identity.
		env := []corev1.EnvVar{
			{Name: envAzureClientID, Value: identity.ClientID},
			{Name: envAzureFederatedTokenFile, Value: path.Join(azureIdentityTokenVolumeMountPath, azureIdentityTokenPath)},
		}
		if p.cluster.Spec.Unseal.CredentialsSecretRef != nil {
			env = append(env,
				envVarFromCredentialsSecret(p.cluster, envAzureEnvironment, envAzureEnvironment),
				envVarFromCredentialsSecret(p.cluster, envAzureADResource, envAzureADResource),
			)
		}
		return env
	}
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}

	return []corev1.EnvVar{
		envVarFromCredentialsSecret(p.cluster, envAzureTenantID, envAzureTenantID),
		envVarFromCredentialsSecret(p.cluster, envAzureClientID, envAzureClientID),
		envVarFromCredentialsSecret(p.cluster, envAzureClientSecret, envAzureClientSecret),
		envVarFromCredentialsSecret(p.cluster, envAzureEnvironment, envAzureEnvironment),
		envVarFromCredentialsSecret(p.cluster, envAzureADResource, envAzureADResource),
	}
}

func (p *azureKeyVaultSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	mounts := (&credentialsSecretSealWiringProvider{cluster: p.cluster}).VolumeMounts()
	if p.workloadIdentity() != nil {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      azureIdentityTokenVolumeName,
			MountPath: azureIdentityTokenVolumeMountPath,
			ReadOnly:  true,
		})
	}
	return mounts
}

func (p *azureKeyVaultSealWiringProvider) Volumes() []corev1.Volume {
	volumes := (&credentialsSecretSealWiringProvider{cluster: p.cluster}).Volumes()
	if p.workloadIdentity() != nil {
		volumes = append(volumes, corev1.Volume{
			Name: azureIdentityTokenVolumeName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          azureIdentityTokenAudience,
								ExpirationSeconds: ptr.To(azureIdentityTokenExpirationSecs),
								Path:              azureIdentityTokenPath,
							},
						},
					},
				},
			},
		})
	}
	return volumes
}

func (p *azureKeyVaultSealWiringProvider) workloadIdentity() *openbaov1alpha1.AzureKeyVaultWorkloadIdentity {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.AzureKeyVault == nil {
		return nil
	}
	return p.cluster.Spec.Unseal.AzureKeyVault.WorkloadIdentity
}

type kmipSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *kmipSealWiringProvider) EnvVars() []corev1.EnvVar { return nil }

func (p *kmipSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).VolumeMounts()
}

func (p *kmipSealWiringProvider) Volumes() []corev1.Volume {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).Volumes()
}

type ociKMSSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *ociKMSSealWiringProvider) EnvVars() []corev1.EnvVar {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}
	if p.cluster.Spec.Unseal.OCIKMS == nil || p.cluster.Spec.Unseal.OCIKMS.AuthTypeAPIKey == nil || !*p.cluster.Spec.Unseal.OCIKMS.AuthTypeAPIKey {
		return nil
	}

	return []corev1.EnvVar{
		{
			Name:  envOCIConfigFile,
			Value: sealCredsVolumeMountPath + "/" + secretKeyOCIConfig,
		},
	}
}

func (p *ociKMSSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).VolumeMounts()
}

func (p *ociKMSSealWiringProvider) Volumes() []corev1.Volume {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).Volumes()
}

type pkcs11SealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *pkcs11SealWiringProvider) EnvVars() []corev1.EnvVar {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.PKCS11 == nil {
		return nil
	}

	cfg := p.cluster.Spec.Unseal.PKCS11
	env := []corev1.EnvVar{
		{Name: portopenbao.EnvBaoSealType, Value: portopenbao.SealTypePKCS11},
		{Name: portopenbao.EnvBaoHSMLib, Value: cfg.Lib},
	}
	if strings.TrimSpace(cfg.Slot) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvBaoHSMSlot, Value: cfg.Slot})
	}
	if strings.TrimSpace(cfg.TokenLabel) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvBaoHSMTokenLabel, Value: cfg.TokenLabel})
	}
	if strings.TrimSpace(cfg.KeyLabel) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvBaoHSMKeyLabel, Value: cfg.KeyLabel})
	}
	if strings.TrimSpace(cfg.KeyID) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvBaoHSMKeyID, Value: cfg.KeyID})
	}
	if strings.TrimSpace(cfg.Mechanism) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvBaoHSMMechanism, Value: cfg.Mechanism})
	}
	if strings.TrimSpace(cfg.RSAOAEPHash) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvBaoHSMRSAOAEPHash, Value: cfg.RSAOAEPHash})
	}
	if cfg.Runtime != nil && strings.TrimSpace(cfg.Runtime.LibraryPath) != "" {
		env = append(env, corev1.EnvVar{Name: portopenbao.EnvLDLibraryPath, Value: cfg.Runtime.LibraryPath})
	}

	if p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return env
	}

	if strings.TrimSpace(cfg.PIN) == "" {
		env = append(env, envVarFromCredentialsSecret(p.cluster, portopenbao.EnvBaoHSMPIN, portopenbao.EnvBaoHSMPIN))
	}
	env = append(env, pkcs11RuntimeEnvVars(p.cluster)...)

	return env
}

func (p *pkcs11SealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	if !pkcs11NeedsCredentialsSecretVolume(p.cluster) {
		return nil
	}
	return []corev1.VolumeMount{
		{
			Name:      sealCredsVolumeName,
			MountPath: sealCredsVolumeMountPath,
			ReadOnly:  true,
		},
	}
}

func (p *pkcs11SealWiringProvider) Volumes() []corev1.Volume {
	if !pkcs11NeedsCredentialsSecretVolume(p.cluster) {
		return nil
	}
	return []corev1.Volume{
		{
			Name: sealCredsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  p.cluster.Spec.Unseal.CredentialsSecretRef.Name,
					DefaultMode: ptr.To(secretFileMode),
					Items:       pkcs11RuntimeFileEnvSecretItems(p.cluster),
				},
			},
		},
	}
}

func pkcs11RuntimeEnvVars(cluster *openbaov1alpha1.OpenBaoCluster) []corev1.EnvVar {
	if cluster == nil || cluster.Spec.Unseal == nil || cluster.Spec.Unseal.PKCS11 == nil ||
		cluster.Spec.Unseal.PKCS11.Runtime == nil || cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}

	secretName := cluster.Spec.Unseal.CredentialsSecretRef.Name
	mappings := portopenbao.PKCS11RuntimeMappings(cluster.Spec.Unseal.PKCS11.Runtime)
	env := make([]corev1.EnvVar, 0, len(mappings))
	for _, item := range mappings {
		switch item.Kind {
		case portopenbao.PKCS11RuntimeMappingEnv:
			env = append(env, corev1.EnvVar{
				Name: item.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
						Key:                  item.SecretKey,
					},
				},
			})
		case portopenbao.PKCS11RuntimeMappingFileEnv:
			env = append(env, corev1.EnvVar{
				Name:  item.Name,
				Value: path.Join(sealCredsVolumeMountPath, item.SecretKey),
			})
		}
	}
	return env
}

func pkcs11NeedsCredentialsSecretVolume(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	return len(pkcs11RuntimeFileEnvSecretItems(cluster)) > 0
}

func pkcs11RuntimeFileEnvSecretItems(cluster *openbaov1alpha1.OpenBaoCluster) []corev1.KeyToPath {
	if cluster == nil || cluster.Spec.Unseal == nil || cluster.Spec.Unseal.PKCS11 == nil ||
		cluster.Spec.Unseal.PKCS11.Runtime == nil || cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}

	mappings := portopenbao.PKCS11RuntimeMappings(cluster.Spec.Unseal.PKCS11.Runtime)
	items := make([]corev1.KeyToPath, 0, len(mappings))
	seen := make(map[string]struct{}, len(mappings))
	for _, item := range mappings {
		if item.Kind != portopenbao.PKCS11RuntimeMappingFileEnv {
			continue
		}
		if _, ok := seen[item.SecretKey]; ok {
			continue
		}
		seen[item.SecretKey] = struct{}{}
		items = append(items, corev1.KeyToPath{
			Key:  item.SecretKey,
			Path: item.SecretKey,
		})
	}
	return items
}

type transitSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *transitSealWiringProvider) EnvVars() []corev1.EnvVar {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}
	if p.cluster.Spec.Unseal.Transit != nil && strings.TrimSpace(p.cluster.Spec.Unseal.Transit.Token) != "" {
		return nil
	}

	// Read token from the mounted secret file and set as VAULT_TOKEN.
	// This allows the seal to use the "token" parameter instead of "token_file",
	// avoiding issues with trailing newlines in mounted Secret files.
	return []corev1.EnvVar{
		{
			Name: envVaultToken,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key: secretKeyTransitToken,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: p.cluster.Spec.Unseal.CredentialsSecretRef.Name,
					},
				},
			},
		},
	}
}

func (p *transitSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).VolumeMounts()
}

func (p *transitSealWiringProvider) Volumes() []corev1.Volume {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).Volumes()
}

type gcpCKMSSealWiringProvider struct {
	cluster *openbaov1alpha1.OpenBaoCluster
}

func (p *gcpCKMSSealWiringProvider) EnvVars() []corev1.EnvVar {
	if p.cluster.Spec.Unseal == nil || p.cluster.Spec.Unseal.CredentialsSecretRef == nil {
		return nil
	}

	// The credentials secret must contain a key named "credentials.json" with the
	// GCP service account JSON credentials. This will be mounted at
	// /etc/bao/seal-creds/credentials.json and referenced by the environment variable.
	return []corev1.EnvVar{
		{
			Name:  envGoogleApplicationCreds,
			Value: sealCredsVolumeMountPath + "/" + secretKeyGoogleCredentials,
		},
	}
}

func (p *gcpCKMSSealWiringProvider) VolumeMounts() []corev1.VolumeMount {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).VolumeMounts()
}

func (p *gcpCKMSSealWiringProvider) Volumes() []corev1.Volume {
	return (&credentialsSecretSealWiringProvider{cluster: p.cluster}).Volumes()
}
