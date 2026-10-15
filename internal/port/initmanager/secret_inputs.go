package initmanager

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"k8s.io/apimachinery/pkg/util/validation"
)

var secretInputFieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// SecretInput describes a projected bootstrap file. It never contains Secret data.
type SecretInput struct {
	RequestName string
	FieldName   string
	SecretName  string
	SecretKey   string
	FileName    string
}

// Path returns the absolute path used by OpenBao and the bootstrap preflight.
func (input SecretInput) Path() string {
	return path.Join(constants.PathSelfInitInputs, input.FileName)
}

// SecretInputs validates raw-request sources and returns deterministic file mappings.
// It does not access Kubernetes or evaluate the request payload.
func SecretInputs(requests []openbaov1alpha1.SelfInitRequest) ([]SecretInput, error) {
	var inputs []SecretInput
	secretNames := make(map[string]struct{})
	requestNames := make(map[string]struct{})
	for _, request := range requests {
		if len(request.DataFrom) == 0 {
			continue
		}
		if _, exists := requestNames[request.Name]; exists {
			return nil, fmt.Errorf("self-init dataFrom requires unique source-bearing request names")
		}
		requestNames[request.Name] = struct{}{}
		if err := validateSecretInputRequest(request); err != nil {
			return nil, fmt.Errorf("self-init request %q: %w", request.Name, err)
		}
		fields := make([]string, 0, len(request.DataFrom))
		for field := range request.DataFrom {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			ref := request.DataFrom[field].SecretKeyRef
			if !secretInputFieldPattern.MatchString(field) || len(validation.IsDNS1123Subdomain(ref.Name)) != 0 ||
				len(validation.IsConfigMapKey(ref.Key)) != 0 {
				return nil, fmt.Errorf("self-init request %q: dataFrom requires a non-empty field and valid Secret name and key", request.Name)
			}
			secretNames[ref.Name] = struct{}{}
			if len(secretNames) > 2 {
				return nil, fmt.Errorf("self-init dataFrom supports at most two distinct Secrets per OpenBaoCluster")
			}
			inputs = append(inputs, SecretInput{
				RequestName: request.Name, FieldName: field, SecretName: ref.Name, SecretKey: ref.Key,
				FileName: fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(
					[]string{request.Name, field, ref.Name, ref.Key}, "\x00")))),
			})
		}
	}
	return inputs, nil
}

func validateSecretInputRequest(request openbaov1alpha1.SelfInitRequest) error {
	if request.AuditDevice != nil || request.AuthMethod != nil || request.SecretEngine != nil || request.Policy != nil {
		return fmt.Errorf("dataFrom cannot be combined with structured request fields")
	}
	requestPath := strings.Trim(request.Path, "/")
	for _, prefix := range []string{"sys/audit/", "sys/auth/", "sys/mounts/", "sys/policies/"} {
		if strings.HasPrefix(requestPath, prefix) {
			return fmt.Errorf("dataFrom is only supported for raw request paths")
		}
	}
	if request.Data == nil {
		return nil
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(request.Data.Raw, &data); err != nil || data == nil {
		// Do not include JSON decoder errors: they can quote payload contents.
		return fmt.Errorf("data must be a JSON object when dataFrom is set")
	}
	for field := range request.DataFrom {
		if _, exists := data[field]; exists {
			return fmt.Errorf("field %q occurs in both data and dataFrom", field)
		}
	}
	return nil
}
