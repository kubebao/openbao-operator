package workload

import (
	"path"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func TestSealWiring_StaticDefault_MountsUnseal(t *testing.T) {
	cluster := newMinimalCluster("seal-static-default", "default")

	env := buildContainerEnv(cluster)
	mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
	volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

	if hasVolume(volumes, sealCredsVolumeName) {
		t.Fatalf("expected %q volume to be absent for static seal", sealCredsVolumeName)
	}
	if hasVolumeMount(mounts, sealCredsVolumeName) {
		t.Fatalf("expected %q volume mount to be absent for static seal", sealCredsVolumeName)
	}
	if hasEnvVar(env, envVaultToken) || hasEnvVar(env, "VAULT_CACERT") || hasEnvVar(env, envGoogleApplicationCreds) {
		t.Fatalf("expected no external-seal env vars for static seal")
	}

	unsealVol, ok := getVolume(volumes, unsealVolumeName)
	if !ok {
		t.Fatalf("expected %q volume to be present for static seal", unsealVolumeName)
	}
	if unsealVol.Secret == nil || unsealVol.Secret.SecretName != resourceidentity.UnsealSecretName(cluster) {
		t.Fatalf("expected %q volume to use secret %q", unsealVolumeName, resourceidentity.UnsealSecretName(cluster))
	}
	if !hasVolumeMountWithPath(mounts, unsealVolumeName, openBaoUnsealMountPath) {
		t.Fatalf("expected %q volume mount at %q for static seal", unsealVolumeName, openBaoUnsealMountPath)
	}
}

func TestSealWiring_ExternalTypes_WithCredentials_MountsSealCredsAndEnv(t *testing.T) {
	cases := []struct {
		name                  string
		unsealType            string
		expectSealCredsVolume bool
		expectEnvVar          []string
	}{
		{name: "transit", unsealType: portopenbao.SealTypeTransit, expectSealCredsVolume: true, expectEnvVar: []string{envVaultToken}},
		{name: "gcpckms", unsealType: portopenbao.SealTypeGCPCKMS, expectSealCredsVolume: true, expectEnvVar: []string{envGoogleApplicationCreds}},
		{name: "awskms", unsealType: portopenbao.SealTypeAWSKMS, expectSealCredsVolume: true, expectEnvVar: []string{envAWSAccessKeyID, envAWSSecretAccessKey, envAWSSessionToken}},
		{
			name:                  "azurekeyvault",
			unsealType:            portopenbao.SealTypeAzureKeyVault,
			expectSealCredsVolume: true,
			expectEnvVar: []string{
				envAzureTenantID,
				envAzureClientID,
				envAzureClientSecret,
				envAzureEnvironment,
				envAzureADResource,
			},
		},
		{name: "kmip", unsealType: portopenbao.SealTypeKMIP, expectSealCredsVolume: true},
		{name: "kms", unsealType: portopenbao.SealTypeKMSPlugin, expectSealCredsVolume: true},
		{name: "ocikms", unsealType: portopenbao.SealTypeOCIKMS, expectSealCredsVolume: true},
		{name: "pkcs11", unsealType: portopenbao.SealTypePKCS11, expectEnvVar: []string{portopenbao.EnvBaoHSMPIN}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newMinimalCluster("seal-"+tc.name, "default")
			cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
				Type: tc.unsealType,
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: "provider-creds",
				},
			}
			if tc.unsealType == portopenbao.SealTypePKCS11 {
				cluster.Spec.Unseal.PKCS11 = &openbaov1alpha1.PKCS11SealConfig{
					Lib:        "/usr/local/lib/libpkcs11.so",
					TokenLabel: "OpenBao",
					KeyLabel:   "bao-root-key-aes",
				}
			}

			env := buildContainerEnv(cluster)
			mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
			volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

			if hasVolume(volumes, unsealVolumeName) {
				t.Fatalf("expected %q volume to be absent for external seal type %q", unsealVolumeName, tc.unsealType)
			}
			if hasVolumeMount(mounts, unsealVolumeName) {
				t.Fatalf("expected %q volume mount to be absent for external seal type %q", unsealVolumeName, tc.unsealType)
			}

			sealCredsVol, ok := getVolume(volumes, sealCredsVolumeName)
			if tc.expectSealCredsVolume {
				if !ok {
					t.Fatalf("expected %q volume to be present when credentialsSecretRef is set", sealCredsVolumeName)
				}
				if sealCredsVol.Secret == nil || sealCredsVol.Secret.SecretName != "provider-creds" {
					t.Fatalf("expected %q volume to use secret %q", sealCredsVolumeName, "provider-creds")
				}
				if !hasVolumeMountWithPath(mounts, sealCredsVolumeName, sealCredsVolumeMountPath) {
					t.Fatalf("expected %q volume mount at %q when credentialsSecretRef is set", sealCredsVolumeName, sealCredsVolumeMountPath)
				}
			} else if ok || hasVolumeMount(mounts, sealCredsVolumeName) {
				t.Fatalf("expected %q volume/mount to be absent for seal type %q without file-backed credentials", sealCredsVolumeName, tc.unsealType)
			}

			for _, envName := range tc.expectEnvVar {
				if !hasEnvVar(env, envName) {
					t.Fatalf("expected env var %q for seal type %q", envName, tc.unsealType)
				}
			}

			if tc.unsealType == portopenbao.SealTypeTransit {
				vaultToken := findEnvVar(env, envVaultToken)
				if vaultToken == nil || vaultToken.ValueFrom == nil || vaultToken.ValueFrom.SecretKeyRef == nil {
					t.Fatalf("expected %s to come from SecretKeyRef for transit seal", envVaultToken)
				}
				if vaultToken.ValueFrom.SecretKeyRef.Name != "provider-creds" || vaultToken.ValueFrom.SecretKeyRef.Key != secretKeyTransitToken {
					t.Fatalf("expected %s SecretKeyRef to be %q/%q, got %q/%q", envVaultToken, "provider-creds", secretKeyTransitToken, vaultToken.ValueFrom.SecretKeyRef.Name, vaultToken.ValueFrom.SecretKeyRef.Key)
				}
			}
		})
	}
}

func TestSealWiring_ExternalTypes_WithoutCredentials_DoesNotMountSealCredsOrEnv(t *testing.T) {
	types := []string{
		portopenbao.SealTypeTransit,
		portopenbao.SealTypeGCPCKMS,
		portopenbao.SealTypeAWSKMS,
		portopenbao.SealTypeAzureKeyVault,
		portopenbao.SealTypeKMIP,
		portopenbao.SealTypeOCIKMS,
		portopenbao.SealTypePKCS11,
	}

	for _, unsealType := range types {
		t.Run(unsealType, func(t *testing.T) {
			cluster := newMinimalCluster("seal-"+unsealType, "default")
			cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{Type: unsealType}

			env := buildContainerEnv(cluster)
			mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
			volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

			if hasVolume(volumes, sealCredsVolumeName) || hasVolumeMount(mounts, sealCredsVolumeName) {
				t.Fatalf("expected %q volume/mount to be absent when credentialsSecretRef is not set", sealCredsVolumeName)
			}
			if hasEnvVar(env, envVaultToken) || hasEnvVar(env, "VAULT_CACERT") || hasEnvVar(env, envGoogleApplicationCreds) {
				t.Fatalf("expected no credentials-derived env vars when credentialsSecretRef is not set")
			}
		})
	}
}

func TestSealWiring_TransitInlineToken_DoesNotInjectVaultTokenEnv(t *testing.T) {
	cluster := newMinimalCluster("seal-transit-inline-token", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypeTransit,
		CredentialsSecretRef: &corev1.LocalObjectReference{
			Name: "provider-creds",
		},
		Transit: &openbaov1alpha1.TransitSealConfig{
			Token: "inline-token",
		},
	}

	env := buildContainerEnv(cluster)
	if hasEnvVar(env, envVaultToken) {
		t.Fatalf("expected %s to be absent when transit token is configured inline", envVaultToken)
	}
}

func TestSealWiring_OCIKMSAPIKey_WithCredentials_IncludesOCIConfigEnv(t *testing.T) {
	cluster := newMinimalCluster("seal-ocikms-api-key", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypeOCIKMS,
		CredentialsSecretRef: &corev1.LocalObjectReference{
			Name: "provider-creds",
		},
		OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
			AuthTypeAPIKey: ptr.To(true),
		},
	}

	env := buildContainerEnv(cluster)
	configEnv := findEnvVar(env, envOCIConfigFile)
	if configEnv == nil {
		t.Fatalf("expected %s env var for ocikms api-key mode", envOCIConfigFile)
	}
	if configEnv.Value != sealCredsVolumeMountPath+"/"+secretKeyOCIConfig {
		t.Fatalf("%s = %q, want %q", envOCIConfigFile, configEnv.Value, sealCredsVolumeMountPath+"/"+secretKeyOCIConfig)
	}
}

func TestSealWiring_PKCS11RuntimeEnv(t *testing.T) {
	cluster := newMinimalCluster("seal-pkcs11-runtime", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypePKCS11,
		CredentialsSecretRef: &corev1.LocalObjectReference{
			Name: "pkcs11-creds",
		},
		PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib:        "/usr/local/lib/libpkcs11.so",
			TokenLabel: "OpenBao",
			KeyLabel:   "bao-root-key-aes",
			Mechanism:  "AES_GCM",
			Runtime: &openbaov1alpha1.PKCS11RuntimeConfig{
				LibraryPath: "/usr/local/lib",
				Env: []openbaov1alpha1.PKCS11RuntimeEnvVar{
					{Name: "CRYPTOSERVER", SecretKey: "cryptoserver"},
				},
				FileEnv: []openbaov1alpha1.PKCS11RuntimeFileEnvVar{
					{Name: "CS_PKCS11_R3_CFG", SecretKey: "cs_pkcs11_R3.cfg"},
				},
			},
		},
	}

	env := buildContainerEnv(cluster)

	for name, want := range map[string]string{
		portopenbao.EnvBaoSealType:      portopenbao.SealTypePKCS11,
		portopenbao.EnvBaoHSMLib:        "/usr/local/lib/libpkcs11.so",
		portopenbao.EnvBaoHSMTokenLabel: "OpenBao",
		portopenbao.EnvBaoHSMKeyLabel:   "bao-root-key-aes",
		portopenbao.EnvBaoHSMMechanism:  "AES_GCM",
		portopenbao.EnvLDLibraryPath:    "/usr/local/lib",
		"CS_PKCS11_R3_CFG":              "/etc/bao/seal-creds/cs_pkcs11_R3.cfg",
	} {
		got := findEnvVar(env, name)
		if got == nil || got.Value != want {
			t.Fatalf("%s = %v, want %q", name, got, want)
		}
	}

	cryptoServer := findEnvVar(env, "CRYPTOSERVER")
	if cryptoServer == nil || cryptoServer.ValueFrom == nil || cryptoServer.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected CRYPTOSERVER to come from SecretKeyRef")
	}
	if cryptoServer.ValueFrom.SecretKeyRef.Name != "pkcs11-creds" || cryptoServer.ValueFrom.SecretKeyRef.Key != "cryptoserver" {
		t.Fatalf("CRYPTOSERVER SecretKeyRef = %s/%s, want pkcs11-creds/cryptoserver", cryptoServer.ValueFrom.SecretKeyRef.Name, cryptoServer.ValueFrom.SecretKeyRef.Key)
	}

	hsmPIN := findEnvVar(env, portopenbao.EnvBaoHSMPIN)
	if hsmPIN == nil || hsmPIN.ValueFrom == nil || hsmPIN.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("expected %s to come from SecretKeyRef", portopenbao.EnvBaoHSMPIN)
	}

	mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
	volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})
	if !hasVolumeMountWithPath(mounts, sealCredsVolumeName, sealCredsVolumeMountPath) {
		t.Fatalf("expected %q volume mount at %q for PKCS#11 fileEnv", sealCredsVolumeName, sealCredsVolumeMountPath)
	}
	sealCredsVol, ok := getVolume(volumes, sealCredsVolumeName)
	if !ok || sealCredsVol.Secret == nil {
		t.Fatalf("expected %q Secret volume for PKCS#11 fileEnv", sealCredsVolumeName)
	}
	if sealCredsVol.Secret.SecretName != "pkcs11-creds" {
		t.Fatalf("%q SecretName = %q, want pkcs11-creds", sealCredsVolumeName, sealCredsVol.Secret.SecretName)
	}
	if len(sealCredsVol.Secret.Items) != 1 {
		t.Fatalf("%q projected item count = %d, want 1", sealCredsVolumeName, len(sealCredsVol.Secret.Items))
	}
	if got := sealCredsVol.Secret.Items[0]; got.Key != "cs_pkcs11_R3.cfg" || got.Path != "cs_pkcs11_R3.cfg" {
		t.Fatalf("%q projected item = %#v, want cs_pkcs11_R3.cfg only", sealCredsVolumeName, got)
	}
}

func TestSealWiring_PKCS11RuntimeEnvOnlyDoesNotMountSecretVolume(t *testing.T) {
	cluster := newMinimalCluster("seal-pkcs11-runtime-env-only", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypePKCS11,
		CredentialsSecretRef: &corev1.LocalObjectReference{
			Name: "pkcs11-creds",
		},
		PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib:        "/usr/local/lib/libpkcs11.so",
			TokenLabel: "OpenBao",
			KeyLabel:   "bao-root-key-aes",
			Runtime: &openbaov1alpha1.PKCS11RuntimeConfig{
				Env: []openbaov1alpha1.PKCS11RuntimeEnvVar{
					{Name: "CRYPTOSERVER", SecretKey: "cryptoserver"},
				},
			},
		},
	}

	env := buildContainerEnv(cluster)
	cryptoServer := findEnvVar(env, "CRYPTOSERVER")
	if cryptoServer == nil || cryptoServer.ValueFrom == nil || cryptoServer.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected CRYPTOSERVER to come from SecretKeyRef")
	}

	mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
	volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})
	if hasVolume(volumes, sealCredsVolumeName) || hasVolumeMount(mounts, sealCredsVolumeName) {
		t.Fatalf("expected %q volume/mount to be absent for PKCS#11 env-only runtime settings", sealCredsVolumeName)
	}
}

func TestSealWiring_PKCS11InlinePINDoesNotInjectHSMPINEnv(t *testing.T) {
	cluster := newMinimalCluster("seal-pkcs11-inline-pin", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypePKCS11,
		CredentialsSecretRef: &corev1.LocalObjectReference{
			Name: "pkcs11-creds",
		},
		PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib:        "/usr/local/lib/libpkcs11.so",
			TokenLabel: "OpenBao",
			KeyLabel:   "bao-root-key-aes",
			PIN:        "1234",
		},
	}

	env := buildContainerEnv(cluster)
	if hasEnvVar(env, portopenbao.EnvBaoHSMPIN) {
		t.Fatalf("expected %s env var to be absent when pin is configured inline", portopenbao.EnvBaoHSMPIN)
	}
}

func TestSealWiring_StaticExplicitAndImplicit_StillMountsUnseal(t *testing.T) {
	cases := []struct {
		name   string
		unseal *openbaov1alpha1.UnsealConfig
	}{
		{name: "explicit-static", unseal: &openbaov1alpha1.UnsealConfig{Type: portopenbao.SealTypeStatic}},
		{name: "implicit-empty-type", unseal: &openbaov1alpha1.UnsealConfig{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newMinimalCluster("seal-"+tc.name, "default")
			cluster.Spec.Unseal = tc.unseal

			mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
			volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

			if !hasVolume(volumes, unsealVolumeName) || !hasVolumeMount(mounts, unsealVolumeName) {
				t.Fatalf("expected %q volume and mount for static seal case %q", unsealVolumeName, tc.name)
			}
		})
	}
}

func TestSealWiring_AzureKeyVaultWorkloadIdentity_ProjectsToken(t *testing.T) {
	cluster := newMinimalCluster("seal-azure-wi", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypeAzureKeyVault,
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName:        "vault",
			KeyName:          "key",
			TenantID:         "tenant-123",
			WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
		},
	}

	env := buildContainerEnv(cluster)
	mounts := buildContainerVolumeMounts(cluster, path.Dir(openBaoRenderedConfig))
	volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

	wantEnv := map[string]string{
		envAzureClientID:           "client-456",
		envAzureFederatedTokenFile: "/var/run/secrets/azure/tokens/azure-identity-token",
	}
	for name, want := range wantEnv {
		found := false
		for _, e := range env {
			if e.Name == name {
				found = true
				if e.Value != want || e.ValueFrom != nil {
					t.Fatalf("env %s = %q (valueFrom %v), want %q", name, e.Value, e.ValueFrom, want)
				}
			}
		}
		if !found {
			t.Fatalf("expected env %s", name)
		}
	}
	for _, name := range []string{envAzureTenantID, envAzureClientSecret} {
		if hasEnvVar(env, name) {
			t.Fatalf("expected no %s: the tenant comes from the seal configuration and there is no secret", name)
		}
	}

	if !hasVolumeMountWithPath(mounts, azureIdentityTokenVolumeName, "/var/run/secrets/azure/tokens") {
		t.Fatalf("expected %q volume mount", azureIdentityTokenVolumeName)
	}
	vol, ok := getVolume(volumes, azureIdentityTokenVolumeName)
	if !ok || vol.Projected == nil || len(vol.Projected.Sources) != 1 || vol.Projected.Sources[0].ServiceAccountToken == nil {
		t.Fatalf("expected %q to be a projected ServiceAccount token volume, got %+v", azureIdentityTokenVolumeName, vol)
	}
	token := vol.Projected.Sources[0].ServiceAccountToken
	if token.Audience != "api://AzureADTokenExchange" || token.Path != "azure-identity-token" ||
		token.ExpirationSeconds == nil || *token.ExpirationSeconds != 3600 {
		t.Fatalf("unexpected token projection %+v", token)
	}
	if hasVolume(volumes, sealCredsVolumeName) {
		t.Fatalf("expected no %q volume without a credentials Secret", sealCredsVolumeName)
	}
}

func TestSealWiring_AzureKeyVaultWorkloadIdentity_KeepsSecretSettings(t *testing.T) {
	cluster := newMinimalCluster("seal-azure-wi-secret", "default")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type:                 portopenbao.SealTypeAzureKeyVault,
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: "azure-settings"},
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName:        "vault",
			KeyName:          "key",
			TenantID:         "tenant-123",
			WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
		},
	}

	env := buildContainerEnv(cluster)
	volumes := buildStatefulSetVolumes(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

	for _, name := range []string{envAzureEnvironment, envAzureADResource} {
		e := findEnvVar(env, name)
		if e == nil || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
			e.ValueFrom.SecretKeyRef.Name != "azure-settings" || e.ValueFrom.SecretKeyRef.Key != name {
			t.Fatalf("expected %s from Secret azure-settings, got %+v", name, e)
		}
	}
	clientID := findEnvVar(env, envAzureClientID)
	if clientID == nil || clientID.Value != "client-456" || clientID.ValueFrom != nil {
		t.Fatalf("expected %s to be the workload identity client ID, got %+v", envAzureClientID, clientID)
	}
	for _, name := range []string{envAzureTenantID, envAzureClientSecret} {
		if hasEnvVar(env, name) {
			t.Fatalf("expected no %s from the Secret alongside the workload identity", name)
		}
	}
	if !hasVolume(volumes, azureIdentityTokenVolumeName) || !hasVolume(volumes, sealCredsVolumeName) {
		t.Fatalf("expected both the token and the credentials Secret volumes")
	}
}

func hasEnvVar(env []corev1.EnvVar, name string) bool {
	return findEnvVar(env, name) != nil
}

func findEnvVar(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

func hasVolume(volumes []corev1.Volume, name string) bool {
	_, ok := getVolume(volumes, name)
	return ok
}

func getVolume(volumes []corev1.Volume, name string) (*corev1.VolumeSource, bool) {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i].VolumeSource, true
		}
	}
	return nil, false
}

func hasVolumeMount(mounts []corev1.VolumeMount, name string) bool {
	for i := range mounts {
		if mounts[i].Name == name {
			return true
		}
	}
	return false
}

func hasVolumeMountWithPath(mounts []corev1.VolumeMount, name, mountPath string) bool {
	for i := range mounts {
		if mounts[i].Name == name && mounts[i].MountPath == mountPath {
			return true
		}
	}
	return false
}

func TestSealWiring_StaticReferencedKey(t *testing.T) {
	cluster := newMinimalCluster("static-recovery", "recovery")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type:                 "static",
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: "snapshot-seal"},
	}

	provider := newSealWiringProvider(cluster)
	volume, ok := getVolume(provider.Volumes(), unsealVolumeName)
	if !ok || volume.Secret == nil || volume.Secret.SecretName != "snapshot-seal" {
		t.Fatalf("static restore must mount the referenced key: %#v", volume)
	}
	if !hasVolumeMountWithPath(provider.VolumeMounts(), unsealVolumeName, openBaoUnsealMountPath) {
		t.Fatal("referenced keys must use the existing static seal path")
	}
}
