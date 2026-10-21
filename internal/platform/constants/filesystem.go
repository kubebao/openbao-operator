package constants

// Common mount paths used by OpenBao pods and helper executables.
const (
	PathTLS            = "/etc/bao/tls"
	PathConfig         = "/etc/bao/config"
	PathData           = "/bao/data"
	PathPlugins        = "/openbao/plugins"
	PathSelfInitInputs = "/etc/bao/self-init-inputs"
)

// Common configuration file names and sentinel contents.
const (
	OpenBaoConfigFileName   = "config.hcl"
	CompletedSelfInitConfig = "# OpenBao self-initialization completed; bootstrap stanzas are intentionally omitted.\n"
)

// Common volume names used by OpenBao pods.
const (
	VolumeTLS     = "tls"
	VolumeConfig  = "config"
	VolumeData    = "data"
	VolumePlugins = "plugins"
)

const VolumeAuditFileStorage = "audit-file-storage"

// VolumeAzureIdentityToken holds the ServiceAccount token projected for the Azure Key Vault
// seal's workload identity.
const VolumeAzureIdentityToken = "azure-identity-token"

// TLS file paths mounted into OpenBao pods and helper executables.
const (
	PathTLSCACert     = PathTLS + "/ca.crt"
	PathTLSServerCert = PathTLS + "/tls.crt"
	PathTLSServerKey  = PathTLS + "/tls.key"
)

// Backup executor mounted file paths.
const (
	PathBackupToken       = "/etc/bao/backup/token/token"           // #nosec G101 -- This is a file path constant, not a credential
	PathBackupCredentials = "/etc/bao/backup/credentials"           // #nosec G101 -- This is a file path constant, not a credential
	PathBackupJWTToken    = "/var/run/secrets/tokens/openbao-token" // #nosec G101 -- This is a file path constant, not a credential
)

// Upgrade executor mounted file paths.
const (
	PathUpgradeJWTToken = "/var/run/secrets/tokens/openbao-token" // #nosec G101 -- This is a file path constant, not a credential
)

// Operator controller mounted file paths.
const (
	PathOperatorJWTToken = "/var/run/secrets/tokens/openbao-token" // #nosec G101 -- This is a file path constant, not a credential
)

// Helper binary paths mounted into OpenBao pods.
const (
	PathWrapperBinary = "/utils/bao-wrapper"
	PathProbeBinary   = "/utils/bao-probe"
)
