package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func buildSealBlock(cluster *openbaov1alpha1.OpenBaoCluster) (*hclwrite.Block, error) {
	unsealType := unsealTypeStatic
	if cluster.Spec.Unseal != nil && cluster.Spec.Unseal.Type != "" {
		unsealType = cluster.Spec.Unseal.Type
	}

	switch unsealType {
	case unsealTypeStatic:
		currentKey := configUnsealKeyPath
		currentKeyID := configUnsealKeyID
		if cluster.Spec.Unseal != nil && cluster.Spec.Unseal.Static != nil {
			if cluster.Spec.Unseal.Static.CurrentKey != "" {
				currentKey = cluster.Spec.Unseal.Static.CurrentKey
			}
			if cluster.Spec.Unseal.Static.CurrentKeyID != "" {
				currentKeyID = cluster.Spec.Unseal.Static.CurrentKeyID
			}
		}
		return gohcl.EncodeAsBlock(hclSealStatic{
			Type:         unsealTypeStatic,
			CurrentKey:   currentKey,
			CurrentKeyID: currentKeyID,
		}, "seal"), nil
	case "transit":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.Transit == nil {
			return nil, fmt.Errorf("unseal.transit is required when unseal.type is transit")
		}
		cfg := cluster.Spec.Unseal.Transit
		return gohcl.EncodeAsBlock(hclSealTransit{
			Type:           "transit",
			Address:        cfg.Address,
			Token:          stringPtr(cfg.Token),
			KeyName:        cfg.KeyName,
			MountPath:      cfg.MountPath,
			Namespace:      stringPtr(cfg.Namespace),
			DisableRenewal: boolPtrString(cfg.DisableRenewal),
			TLSCACert:      stringPtr(cfg.TLSCACert),
			TLSClientCert:  stringPtr(cfg.TLSClientCert),
			TLSClientKey:   stringPtr(cfg.TLSClientKey),
			TLSServerName:  stringPtr(cfg.TLSServerName),
			TLSSkipVerify:  boolPtrString(cfg.TLSSkipVerify),
		}, "seal"), nil
	case "awskms":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.AWSKMS == nil {
			return nil, fmt.Errorf("unseal.awskms is required when unseal.type is awskms")
		}
		cfg := cluster.Spec.Unseal.AWSKMS
		return gohcl.EncodeAsBlock(hclSealAWSKMS{
			Type:         "awskms",
			Region:       cfg.Region,
			KMSKeyID:     cfg.KMSKeyID,
			Endpoint:     stringPtr(cfg.Endpoint),
			AccessKey:    stringPtr(cfg.AccessKey),
			SecretKey:    stringPtr(cfg.SecretKey),
			SessionToken: stringPtr(cfg.SessionToken),
		}, "seal"), nil
	case "azurekeyvault":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.AzureKeyVault == nil {
			return nil, fmt.Errorf("unseal.azureKeyVault is required when unseal.type is azurekeyvault")
		}
		cfg := cluster.Spec.Unseal.AzureKeyVault
		return gohcl.EncodeAsBlock(hclSealAzureKeyVault{
			Type:         "azurekeyvault",
			VaultName:    cfg.VaultName,
			KeyName:      cfg.KeyName,
			TenantID:     stringPtr(cfg.TenantID),
			ClientID:     stringPtr(cfg.ClientID),
			ClientSecret: stringPtr(cfg.ClientSecret),
			Resource:     stringPtr(cfg.Resource),
			Environment:  stringPtr(cfg.Environment),
			AuthMethod:   stringPtr(azureKeyVaultAuthMethod(cfg)),
		}, "seal"), nil
	case "gcpckms":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.GCPCloudKMS == nil {
			return nil, fmt.Errorf("unseal.gcpCloudKMS is required when unseal.type is gcpckms")
		}
		cfg := cluster.Spec.Unseal.GCPCloudKMS
		return gohcl.EncodeAsBlock(hclSealGCPCloudKMS{
			Type:        "gcpckms",
			Project:     cfg.Project,
			Region:      cfg.Region,
			KeyRing:     cfg.KeyRing,
			CryptoKey:   cfg.CryptoKey,
			Credentials: stringPtr(cfg.Credentials),
		}, "seal"), nil
	case "kmip":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.KMIP == nil {
			return nil, fmt.Errorf("unseal.kmip is required when unseal.type is kmip")
		}
		cfg := cluster.Spec.Unseal.KMIP
		return gohcl.EncodeAsBlock(hclSealKMIP{
			Type:         "kmip",
			Endpoint:     cfg.Endpoint,
			KMSKeyID:     cfg.KMSKeyID,
			ClientCert:   stringPtr(cfg.ClientCert),
			ClientKey:    stringPtr(cfg.ClientKey),
			CACert:       stringPtr(cfg.CACert),
			ServerName:   stringPtr(cfg.ServerName),
			Timeout:      cfg.Timeout,
			EncryptAlg:   stringPtr(cfg.EncryptAlg),
			TLS12Ciphers: stringPtr(cfg.TLS12Ciphers),
			Disabled:     boolPtrString(cfg.Disabled),
		}, "seal"), nil
	case portopenbao.SealTypeKMSPlugin:
		return buildKMSPluginSealBlock(cluster)
	case "ocikms":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.OCIKMS == nil {
			return nil, fmt.Errorf("unseal.ocikms is required when unseal.type is ocikms")
		}
		cfg := cluster.Spec.Unseal.OCIKMS
		return gohcl.EncodeAsBlock(hclSealOCIKMS{
			Type:               "ocikms",
			KeyID:              cfg.KeyID,
			CryptoEndpoint:     cfg.CryptoEndpoint,
			ManagementEndpoint: cfg.ManagementEndpoint,
			AuthTypeAPIKey:     cfg.AuthTypeAPIKey,
			Disabled:           boolPtrString(cfg.Disabled),
		}, "seal"), nil
	case "pkcs11":
		if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.PKCS11 == nil {
			return nil, fmt.Errorf("unseal.pkcs11 is required when unseal.type is pkcs11")
		}
		cfg := cluster.Spec.Unseal.PKCS11
		return gohcl.EncodeAsBlock(hclSealPKCS11{
			Type:                      "pkcs11",
			Lib:                       cfg.Lib,
			Slot:                      stringPtr(cfg.Slot),
			TokenLabel:                stringPtr(cfg.TokenLabel),
			PIN:                       stringPtr(cfg.PIN),
			KeyLabel:                  cfg.KeyLabel,
			KeyID:                     stringPtr(cfg.KeyID),
			Mechanism:                 stringPtr(cfg.Mechanism),
			DisableSoftwareEncryption: boolPtrString(cfg.DisableSoftwareEncryption),
			Disabled:                  boolPtrString(cfg.Disabled),
			RSAOAEPHash:               stringPtr(cfg.RSAOAEPHash),
		}, "seal"), nil
	default:
		return nil, fmt.Errorf("unsupported unseal type %q", unsealType)
	}
}

func buildKMSPluginSealBlock(cluster *openbaov1alpha1.OpenBaoCluster) (*hclwrite.Block, error) {
	if cluster.Spec.Unseal == nil || cluster.Spec.Unseal.KMS == nil {
		return nil, fmt.Errorf("unseal.kms is required when unseal.type is kms")
	}

	cfg := cluster.Spec.Unseal.KMS
	pluginName := strings.TrimSpace(cfg.PluginName)
	if pluginName == "" {
		return nil, fmt.Errorf("unseal.kms.pluginName is required when unseal.type is kms")
	}
	if !hasKMSPlugin(cluster.Spec.Plugins, pluginName) {
		return nil, fmt.Errorf("unseal.kms.pluginName %q must reference a spec.plugins entry with type \"kms\"", pluginName)
	}

	block := hclwrite.NewBlock("seal", []string{pluginName})
	keys := make([]string, 0, len(cfg.Config))
	for key := range cfg.Config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("unseal.kms.config contains an empty key")
		}
		if strings.TrimSpace(key) != key || !hclsyntax.ValidIdentifier(key) {
			return nil, fmt.Errorf("unseal.kms.config key %q must be a valid HCL identifier", key)
		}
		block.Body().SetAttributeValue(key, cty.StringVal(cfg.Config[key]))
	}

	return block, nil
}

func hasKMSPlugin(plugins []openbaov1alpha1.Plugin, name string) bool {
	for _, plugin := range plugins {
		if strings.TrimSpace(plugin.Type) == portopenbao.SealTypeKMSPlugin &&
			strings.TrimSpace(plugin.Name) == name {
			return true
		}
	}
	return false
}
