package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	platformsemver "github.com/kubebao/openbao-operator/internal/platform/semver"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/internal/port/initmanager"
	"github.com/zclconf/go-cty/cty"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

type autopilotRequestConfig struct {
	CleanupDeadServers             bool   `json:"cleanup_dead_servers"`
	DeadServerLastContactThreshold string `json:"dead_server_last_contact_threshold"`
	MinQuorum                      int    `json:"min_quorum"`
	ServerStabilizationTime        string `json:"server_stabilization_time"`
}

// RenderSelfInitHCL renders only the self-initialization stanzas as a separate
// HCL configuration. This is stored in a separate ConfigMap that is only mounted
// for pod-0, since only the first pod needs to execute initialization requests.
// If bootstrapConfig is provided, it will be merged with user requests.
func RenderSelfInitHCL(cluster *openbaov1alpha1.OpenBaoCluster, bootstrapConfig *OperatorBootstrapConfig) ([]byte, error) {
	file := hclwrite.NewEmptyFile()
	body := file.Body()

	if err := validateAuditFileStorageConfiguration(cluster); err != nil {
		return nil, err
	}
	if err := validateInitialRecoveryKeysConfiguration(cluster); err != nil {
		return nil, err
	}
	if err := validateSelfInitRequestVersion(cluster); err != nil {
		return nil, err
	}

	// If bootstrap config provided, render it first
	if bootstrapConfig != nil {
		if strings.TrimSpace(bootstrapConfig.OIDCIssuerURL) == "" {
			return nil, fmt.Errorf("OIDC issuer URL is required to render operator bootstrap")
		}
		if strings.TrimSpace(bootstrapConfig.OIDCDiscoveryURL) == "" && strings.TrimSpace(bootstrapConfig.OIDCJWKSURL) == "" && len(bootstrapConfig.JWTKeysPEM) == 0 {
			return nil, fmt.Errorf("an OIDC discovery URL, a JWKS URL, or at least one JWT public key is required to render operator bootstrap")
		}
		if strings.TrimSpace(bootstrapConfig.OperatorNS) == "" {
			return nil, fmt.Errorf("operator namespace is required to render operator bootstrap")
		}
		if strings.TrimSpace(bootstrapConfig.OperatorSA) == "" {
			return nil, fmt.Errorf("operator service account name is required to render operator bootstrap")
		}

		audience, err := portauth.ControllerJWTAudience(cluster, bootstrapConfig.JWTAuthAudience)
		if err != nil {
			return nil, err
		}
		config := *bootstrapConfig
		config.ControllerJWTAudience = audience
		if cluster.Annotations[constants.AnnotationRestoreOrigin] != "" {
			if len(config.JWTKeysPEM) == 0 {
				return nil, fmt.Errorf("fresh restore bootstrap requires observed JWT public keys")
			}
			config.OIDCDiscoveryURL, config.OIDCJWKSURL = "", ""
		}
		body.AppendBlock(buildSelfInitBootstrapInitializeBlock(cluster, config))
	}

	if initialRecoveryKeys := initialRecoveryKeysConfig(cluster); initialRecoveryKeys != nil {
		body.AppendBlock(buildSelfInitInitialRecoveryKeysBlock(initialRecoveryKeys))
	}

	// Render user self-init requests if enabled
	if cluster.Spec.SelfInit != nil && cluster.Spec.SelfInit.Enabled {
		requests := cluster.Spec.SelfInit.Requests
		if !hasRequest(requests, "configure-autopilot") {
			req, err := defaultAutopilotRequest()
			if err != nil {
				return nil, fmt.Errorf("failed to create default autopilot request: %w", err)
			}
			requests = append(requests, req)
		}

		if err := renderSelfInitStanzas(body, requests); err != nil {
			return nil, fmt.Errorf("failed to render self-init stanzas: %w", err)
		}
	}

	return file.Bytes(), nil
}

func validateSelfInitRequestVersion(cluster *openbaov1alpha1.OpenBaoCluster) error {
	if cluster.Spec.SelfInit == nil || !cluster.Spec.SelfInit.Enabled {
		return nil
	}
	for _, request := range cluster.Spec.SelfInit.Requests {
		if len(request.Headers) == 0 && request.When == nil && len(request.DataFrom) == 0 {
			continue
		}
		supported, err := platformsemver.AtLeast(cluster.Spec.Version, 2, 6, 0)
		if err != nil {
			return fmt.Errorf("validate self-init request version: %w", err)
		}
		if !supported {
			if len(request.DataFrom) > 0 {
				return fmt.Errorf("self-init request %q: dataFrom requires OpenBao >= 2.6.0", request.Name)
			}
			return fmt.Errorf("self-init request %q: headers and when require OpenBao >= 2.6.0", request.Name)
		}
	}
	return nil
}

func initialRecoveryKeysConfig(cluster *openbaov1alpha1.OpenBaoCluster) *openbaov1alpha1.InitialRecoveryKeysConfig {
	if cluster == nil || cluster.Spec.RecoveryKeys == nil {
		return nil
	}
	return cluster.Spec.RecoveryKeys.Initial
}

func validateInitialRecoveryKeysConfiguration(cluster *openbaov1alpha1.OpenBaoCluster) error {
	config := initialRecoveryKeysConfig(cluster)
	if config == nil {
		return nil
	}

	if cluster.Spec.SelfInit == nil || !cluster.Spec.SelfInit.Enabled {
		return fmt.Errorf("spec.recoveryKeys.initial requires spec.selfInit.enabled=true")
	}
	if !hasNonStaticUnseal(cluster) {
		return fmt.Errorf("spec.recoveryKeys.initial requires a non-static spec.unseal.type")
	}
	if hasRequestPath(cluster.Spec.SelfInit.Requests, pathSysRotateRecoveryInit) {
		return fmt.Errorf("spec.recoveryKeys.initial cannot be combined with a raw self-init request for %s", pathSysRotateRecoveryInit)
	}
	if config.Shares < 1 {
		return fmt.Errorf("spec.recoveryKeys.initial.shares must be greater than 0")
	}
	if config.Threshold < 1 {
		return fmt.Errorf("spec.recoveryKeys.initial.threshold must be greater than 0")
	}
	if config.Threshold > config.Shares {
		return fmt.Errorf("spec.recoveryKeys.initial.threshold must be less than or equal to shares")
	}
	if len(config.Recipients) != int(config.Shares) {
		return fmt.Errorf("spec.recoveryKeys.initial.recipients must contain exactly %d entries", config.Shares)
	}

	seenNames := make(map[string]struct{}, len(config.Recipients))
	for index, recipient := range config.Recipients {
		name := strings.TrimSpace(recipient.Name)
		if name == "" {
			return fmt.Errorf("spec.recoveryKeys.initial.recipients[%d].name must not be empty", index)
		}
		if _, ok := seenNames[name]; ok {
			return fmt.Errorf("spec.recoveryKeys.initial.recipients contains duplicate name %q", name)
		}
		seenNames[name] = struct{}{}

		if strings.TrimSpace(recipient.PGPPublicKey) == "" {
			return fmt.Errorf("spec.recoveryKeys.initial.recipients[%d].pgpPublicKey must not be empty", index)
		}
	}

	return nil
}

func hasNonStaticUnseal(cluster *openbaov1alpha1.OpenBaoCluster) bool {
	if cluster.Spec.Unseal == nil {
		return false
	}
	unsealType := strings.TrimSpace(cluster.Spec.Unseal.Type)
	return unsealType != "" && unsealType != unsealTypeStatic
}

func hasRequestPath(requests []openbaov1alpha1.SelfInitRequest, path string) bool {
	for _, req := range requests {
		if strings.Trim(req.Path, "/") == path {
			return true
		}
	}
	return false
}

func hasRequest(requests []openbaov1alpha1.SelfInitRequest, name string) bool {
	for _, req := range requests {
		if req.Name == name {
			return true
		}
	}
	return false
}

func defaultAutopilotRequest() (openbaov1alpha1.SelfInitRequest, error) {
	data, err := json.Marshal(autopilotRequestConfig{
		CleanupDeadServers:             true,
		DeadServerLastContactThreshold: "24h",
		MinQuorum:                      3,
		ServerStabilizationTime:        "10s",
	})
	if err != nil {
		return openbaov1alpha1.SelfInitRequest{}, err
	}

	return openbaov1alpha1.SelfInitRequest{
		Name:      "configure-autopilot",
		Operation: openbaov1alpha1.SelfInitOperationUpdate,
		Path:      "sys/storage/raft/autopilot/configuration",
		Data: &apiextensionsv1.JSON{
			Raw: data,
		},
	}, nil
}

// renderSelfInitStanzas generates HCL initialize stanzas for OpenBao's self-initialization feature.
// Each request is rendered as an initialize block containing a named request block with the specified
// operation, path, and optional data fields. The request name is required by OpenBao's configuration
// schema and is used as the map key when parsing JSON/HCL.
func renderSelfInitStanzas(body *hclwrite.Body, requests []openbaov1alpha1.SelfInitRequest) error {
	inputs, err := initmanager.SecretInputs(requests)
	if err != nil {
		return err
	}
	for _, req := range requests {
		if strings.TrimSpace(req.Name) == "" {
			continue
		}

		initLabel := req.Name
		requestLabel := fmt.Sprintf("%s-request", req.Name)

		initBlock := buildInitializeBlock(initLabel)
		initBody := initBlock.Body()
		requestBlock := buildInitializeRequestBlock(requestLabel, string(req.Operation), req.Path, req.AllowFailure)
		requestBody := requestBlock.Body()

		if len(req.Headers) > 0 {
			headersVal, err := selfInitHeadersToCty(req.Headers)
			if err != nil {
				return fmt.Errorf("failed to render self-init request headers for request %q: %w", req.Name, err)
			}
			requestBody.SetAttributeValue("headers", headersVal)
		}

		if req.When != nil && len(req.When.Raw) > 0 {
			whenVal, err := decodeJSONToCty(req.When.Raw, fmt.Sprintf("self-init when for request %q", req.Name))
			if err != nil {
				return err
			}
			if whenVal != cty.NilVal {
				requestBody.SetAttributeValue("when", whenVal)
			}
		}

		dataVal := cty.NilVal
		if structuredVal, handled, err := resolveSelfInitRequestStructuredData(req); err != nil {
			return err
		} else if handled {
			dataVal = structuredVal
		} else if req.Data != nil && len(req.Data.Raw) > 0 {
			ctyVal, err := decodeJSONToCty(req.Data.Raw, fmt.Sprintf("self-init data for request %q", req.Name))
			if err != nil {
				return err
			}
			dataVal = ctyVal
		}

		if len(req.DataFrom) > 0 {
			data := map[string]cty.Value{}
			if dataVal != cty.NilVal {
				for key, value := range dataVal.AsValueMap() {
					data[key] = value
				}
			}
			for _, input := range inputs {
				if input.RequestName == req.Name {
					data[input.FieldName] = cty.ObjectVal(map[string]cty.Value{
						"eval_source": cty.StringVal("file"),
						"eval_type":   cty.StringVal("string"),
						"path":        cty.StringVal(input.Path()),
					})
				}
			}
			dataVal = cty.ObjectVal(data)
		}
		if dataVal != cty.NilVal {
			setSelfInitRequestData(requestBody, dataVal)
		}

		initBody.AppendBlock(requestBlock)
		body.AppendBlock(initBlock)
	}

	return nil
}

func selfInitHeadersToCty(headers map[string][]string) (cty.Value, error) {
	rendered := make(map[string]cty.Value, len(headers))
	keys := make([]string, 0, len(headers))
	for header := range headers {
		keys = append(keys, header)
	}
	sort.Strings(keys)

	for _, header := range keys {
		if strings.TrimSpace(header) == "" {
			return cty.NilVal, fmt.Errorf("headers contains an empty header name")
		}
		if header != strings.TrimSpace(header) {
			return cty.NilVal, fmt.Errorf("header name %q must not contain leading or trailing whitespace", header)
		}

		values := headers[header]
		ctyValues := make([]cty.Value, 0, len(values))
		for _, value := range values {
			ctyValues = append(ctyValues, cty.StringVal(value))
		}
		if len(ctyValues) == 0 {
			rendered[header] = cty.EmptyTupleVal
			continue
		}
		rendered[header] = cty.TupleVal(ctyValues)
	}

	return cty.ObjectVal(rendered), nil
}

func setSelfInitRequestData(requestBody *hclwrite.Body, dataVal cty.Value) {
	if dataVal == cty.NilVal {
		return
	}

	// Prefer "data { ... }" blocks which match OpenBao self-init docs and the
	// operator-bootstrap output. Some endpoints appear to ignore "data = { ... }".
	if dataVal.Type().IsObjectType() || dataVal.Type().IsMapType() {
		dataBlock := requestBody.AppendNewBlock("data", nil)
		dataBody := dataBlock.Body()

		dataMap := dataVal.AsValueMap()
		keys := make([]string, 0, len(dataMap))
		for k := range dataMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			dataBody.SetAttributeValue(k, dataMap[k])
		}
		return
	}

	requestBody.SetAttributeValue("data", dataVal)
}
