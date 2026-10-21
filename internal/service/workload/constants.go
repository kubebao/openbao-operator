package workload

import "github.com/kubebao/openbao-operator/internal/platform/constants"

const (
	dataVolumeName           = constants.VolumeData
	tlsVolumeName            = constants.VolumeTLS
	configVolumeName         = constants.VolumeConfig
	pluginVolumeName         = constants.VolumePlugins
	configInitVolumeName     = "config-init"
	configRenderedVolumeName = "config-rendered"
	unsealVolumeName         = "unseal"
	tmpVolumeName            = "tmp"
	utilsVolumeName          = "utils"
	acmeCacheVolumeName      = "acme-cache"
	kubeAPIAccessVolumeName  = "kube-api-access"
	configFileName           = "config.hcl"
	configTemplatePath       = "/etc/bao/config/config.hcl"
	configInitTemplatePath   = "/etc/bao/config-init/config.hcl"
	openBaoConfigMountPath   = constants.PathConfig
	openBaoRenderedConfig    = "/etc/bao/rendered-config/config.hcl"
	openBaoTLSMountPath      = constants.PathTLS
	openBaoUnsealMountPath   = "/etc/bao/unseal"
	openBaoDataPath          = constants.PathData
	openBaoPluginPath        = constants.PathPlugins
	serviceAccountMountPath  = "/var/run/secrets/kubernetes.io/serviceaccount"
	kubeRootCAConfigMapName  = "kube-root-ca.crt"
	openBaoBinaryName        = constants.BinaryBao
	configHashAnnotation     = "openbao.org/config-hash"

	envAWSAccessKeyID          = "AWS_ACCESS_KEY_ID"
	envAWSSecretAccessKey      = "AWS_SECRET_ACCESS_KEY" // #nosec G101 -- environment variable name, not a secret value
	envAWSSessionToken         = "AWS_SESSION_TOKEN"     // #nosec G101 -- environment variable name, not a secret value
	envAzureADResource         = "AZURE_AD_RESOURCE"
	envAzureClientID           = "AZURE_CLIENT_ID"
	envAzureClientSecret       = "AZURE_CLIENT_SECRET" // #nosec G101 -- environment variable name, not a secret value
	envAzureEnvironment        = "AZURE_ENVIRONMENT"
	envAzureFederatedTokenFile = "AZURE_FEDERATED_TOKEN_FILE" // #nosec G101 -- environment variable name, not a secret value
	envAzureTenantID           = "AZURE_TENANT_ID"
	envGoogleApplicationCreds  = "GOOGLE_APPLICATION_CREDENTIALS" // #nosec G101 -- environment variable name, not a secret value
	envOCIConfigFile           = "OCI_CONFIG_FILE"
	envVaultToken              = "VAULT_TOKEN"      // #nosec G101 -- environment variable name, not a secret value
	secretKeyGoogleCredentials = "credentials.json" // #nosec G101 -- Secret data key name, not a secret value
	secretKeyOCIConfig         = "config"
	secretKeyTransitToken      = "token" // #nosec G101 -- Secret data key name, not a secret value

	openBaoUserID  = constants.UserOpenBao
	openBaoGroupID = constants.GroupOpenBao

	secretFileMode                       = int32(0440)
	serviceAccountFileMode               = int32(0440)
	serviceAccountTokenExpirationSeconds = int64(3600)
)

const auditFileStorageVolumeName = constants.VolumeAuditFileStorage
