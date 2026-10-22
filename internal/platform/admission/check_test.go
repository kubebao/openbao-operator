package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
	sigsyaml "sigs.k8s.io/yaml"
)

const policyFingerprintAnnotation = "openbao.org/admission-policy-fingerprint"

func TestDefaultDependencies(t *testing.T) {
	t.Parallel()

	expected := []Dependency{
		{
			Name:        dependencyOpenBaoValidateOpenBaoCluster,
			PolicyName:  dependencyOpenBaoValidateOpenBaoCluster,
			BindingName: dependencyBindingValidateOpenBaoCluster,
		},
		{
			Name:        dependencyOpenBaoValidateOpenBaoTenant,
			PolicyName:  dependencyOpenBaoValidateOpenBaoTenant,
			BindingName: dependencyBindingValidateOpenBaoTenant,
		},
		{
			Name:        dependencyOpenBaoValidateOpenBaoRestore,
			PolicyName:  dependencyOpenBaoValidateOpenBaoRestore,
			BindingName: dependencyBindingValidateOpenBaoRestore,
		},
		{Name: dependencyProtectRestoreExecution, PolicyName: dependencyProtectRestoreExecution, BindingName: dependencyProtectRestoreExecutionBinding},
		{
			Name:        dependencyOpenBaoLockControllerStatefulSetMutations,
			PolicyName:  dependencyOpenBaoLockControllerStatefulSetMutations,
			BindingName: dependencyBindingLockControllerStatefulSetMutations,
		},
		{
			Name:        dependencyOpenBaoRestrictProvisionerRBAC,
			PolicyName:  dependencyOpenBaoRestrictProvisionerRBAC,
			BindingName: dependencyBindingRestrictProvisionerRBAC,
		},
		{
			Name:        dependencyOpenBaoRestrictProvisionerNamespace,
			PolicyName:  dependencyOpenBaoRestrictProvisionerNamespace,
			BindingName: dependencyBindingRestrictProvisionerNamespace,
		},
		{
			Name:        dependencyOpenBaoRestrictProvisionerTenantGuardrail,
			PolicyName:  dependencyOpenBaoRestrictProvisionerTenantGuardrail,
			BindingName: dependencyBindingRestrictProvisionerTenantGuardrail,
		},
		{
			Name:        dependencyOpenBaoRestrictControllerRBAC,
			PolicyName:  dependencyOpenBaoRestrictControllerRBAC,
			BindingName: dependencyBindingRestrictControllerRBAC,
		},
		{
			Name:        dependencyOpenBaoRestrictControllerServiceAccounts,
			PolicyName:  dependencyOpenBaoRestrictControllerServiceAccounts,
			BindingName: dependencyBindingRestrictControllerServiceAccounts,
		},
		{
			Name:        dependencyOpenBaoRestrictControllerSecretWrites,
			PolicyName:  dependencyOpenBaoRestrictControllerSecretWrites,
			BindingName: dependencyBindingRestrictControllerSecretWrites,
		},
		{
			Name:        dependencyOpenBaoLockManagedResourceMutations,
			PolicyName:  dependencyOpenBaoLockManagedResourceMutations,
			BindingName: dependencyBindingLockManagedResourceMutations,
		},
		{
			Name:        dependencyOpenBaoEnforceManagedImageDigests,
			PolicyName:  dependencyOpenBaoEnforceManagedImageDigests,
			BindingName: dependencyBindingEnforceManagedImageDigests,
		},
	}

	got := DefaultDependencies()
	if !reflect.DeepEqual(expected, got) {
		t.Fatalf("DefaultDependencies() mismatch\nwant: %#v\ngot:  %#v", expected, got)
	}

	seen := map[string]struct{}{}
	for i, dep := range got {
		if dep.Name == "" {
			t.Fatalf("DefaultDependencies()[%d] has empty name", i)
		}
		if dep.PolicyName == "" {
			t.Fatalf("DefaultDependencies()[%d] has empty policy name", i)
		}
		if dep.BindingName == "" {
			t.Fatalf("DefaultDependencies()[%d] has empty binding name", i)
		}
		if _, exists := seen[dep.Name]; exists {
			t.Fatalf("DefaultDependencies()[%d] duplicates name %q", i, dep.Name)
		}
		seen[dep.Name] = struct{}{}
	}
}

func TestDefaultDependenciesCoverConfigPolicyValidatingPolicies(t *testing.T) {
	t.Parallel()

	configPolicies, err := readConfigPolicyNames(filepath.Join("..", "..", "..", "config", "policy"))
	if err != nil {
		t.Fatalf("read config policy names: %v", err)
	}
	if len(configPolicies) == 0 {
		t.Fatal("expected at least one ValidatingAdmissionPolicy in config/policy")
	}

	dependencyPolicies := make(map[string]struct{}, len(DefaultDependencies()))
	for _, dep := range DefaultDependencies() {
		dependencyPolicies[dep.PolicyName] = struct{}{}
	}

	var missingFromDependencies []string
	for policyName := range configPolicies {
		if _, ok := dependencyPolicies[policyName]; !ok {
			missingFromDependencies = append(missingFromDependencies, policyName)
		}
	}
	sort.Strings(missingFromDependencies)

	var missingFromConfig []string
	for policyName := range dependencyPolicies {
		if _, ok := configPolicies[policyName]; !ok {
			missingFromConfig = append(missingFromConfig, policyName)
		}
	}
	sort.Strings(missingFromConfig)

	if len(missingFromDependencies) > 0 || len(missingFromConfig) > 0 {
		t.Fatalf(
			"DefaultDependencies() and config/policy VAP set drifted: missing_from_dependencies=%v missing_from_config=%v",
			missingFromDependencies,
			missingFromConfig,
		)
	}
}

func TestExpectedPolicyFingerprintsMatchSourcePolicyContent(t *testing.T) {
	t.Parallel()

	configPolicyDir := filepath.Join("..", "..", "..", "config", "policy")
	kustomizationBytes, err := os.ReadFile(filepath.Join(configPolicyDir, "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read policy kustomization: %v", err)
	}
	var k policyKustomization
	if err := yaml.Unmarshal(kustomizationBytes, &k); err != nil {
		t.Fatalf("decode policy kustomization: %v", err)
	}

	for _, resource := range k.Resources {
		manifestBytes, err := os.ReadFile(filepath.Join(configPolicyDir, resource))
		if err != nil {
			t.Fatalf("read policy resource %q: %v", resource, err)
		}
		manifestJSON, err := sigsyaml.YAMLToJSON(manifestBytes)
		if err != nil {
			t.Fatalf("convert policy resource %q to JSON: %v", resource, err)
		}
		var policy map[string]any
		if err := json.Unmarshal(manifestJSON, &policy); err != nil {
			t.Fatalf("decode policy resource %q: %v", resource, err)
		}
		metadata, ok := policy["metadata"].(map[string]any)
		if !ok {
			continue
		}
		policyName, _ := metadata["name"].(string)
		annotations, _ := metadata["annotations"].(map[string]any)
		annotationFingerprint, found := annotations[policyFingerprintAnnotation].(string)
		if !found {
			continue
		}

		encodedSpec, err := json.Marshal(policy["spec"])
		if err != nil {
			t.Fatalf("encode policy %q spec: %v", policyName, err)
		}
		sum := sha256.Sum256(encodedSpec)
		contentFingerprint := "sha256:" + hex.EncodeToString(sum[:])
		if annotationFingerprint != contentFingerprint {
			t.Errorf(
				"policy %q annotation fingerprint = %q, want content fingerprint %q",
				policyName,
				annotationFingerprint,
				contentFingerprint,
			)
		}
	}

}

type policyKustomization struct {
	Resources []string `yaml:"resources"`
}

type manifestHeader struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

func readConfigPolicyNames(configPolicyDir string) (map[string]struct{}, error) {
	out := map[string]struct{}{}

	kustomizationBytes, err := os.ReadFile(filepath.Join(configPolicyDir, "kustomization.yaml"))
	if err != nil {
		return nil, err
	}

	var k policyKustomization
	if err := yaml.Unmarshal(kustomizationBytes, &k); err != nil {
		return nil, err
	}

	for _, resource := range k.Resources {
		manifestBytes, err := os.ReadFile(filepath.Join(configPolicyDir, resource))
		if err != nil {
			return nil, err
		}

		var header manifestHeader
		if err := yaml.Unmarshal(manifestBytes, &header); err != nil {
			return nil, err
		}

		if header.Kind != "ValidatingAdmissionPolicy" {
			continue
		}
		if header.Metadata.Name == "" {
			continue
		}
		out[header.Metadata.Name] = struct{}{}
	}

	return out, nil
}
