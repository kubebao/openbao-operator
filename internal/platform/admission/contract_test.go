package admission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const allowAllValidation = "true"

func releaseFixture(t *testing.T, dep Dependency, prefix string) (*admissionv1.ValidatingAdmissionPolicy, *admissionv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	settings, err := installationFromEnvironment()
	require.NoError(t, err)
	contracts, err := expectedContracts(settings)
	require.NoError(t, err)
	item, ok := contracts[dep.PolicyName]
	require.True(t, ok)
	policy := &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: prefix + dep.PolicyName,
		Annotations: map[string]string{policyFingerprintAnnotation: "sha256:retained-source-annotation"}}, Spec: item.Policy}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: prefix + dep.BindingName}, Spec: item.Binding}
	binding.Spec.PolicyName = policy.Name
	return policy, binding
}

func TestReleaseContractsMatchEverySourceSpecification(t *testing.T) {
	var contracts map[string]policyContract
	require.NoError(t, json.Unmarshal(releaseContracts, &contracts))
	require.Len(t, contracts, len(DefaultDependencies()))
	for _, dep := range DefaultDependencies() {
		t.Run(dep.Name, func(t *testing.T) {
			item, found := contracts[dep.PolicyName]
			require.True(t, found)
			for _, fixture := range []struct {
				name   string
				object any
			}{
				{dep.PolicyName, &admissionv1.ValidatingAdmissionPolicy{}},
				{dep.BindingName, &admissionv1.ValidatingAdmissionPolicyBinding{}},
			} {
				data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "policy", fixture.name+".yaml"))
				require.NoError(t, err)
				require.NoError(t, yaml.UnmarshalStrict(data, fixture.object))
				switch object := fixture.object.(type) {
				case *admissionv1.ValidatingAdmissionPolicy:
					require.Equal(t, object.Spec, item.Policy, "run make admission-contracts")
				case *admissionv1.ValidatingAdmissionPolicyBinding:
					require.Equal(t, object.Spec, item.Binding, "run make admission-contracts")
				}
			}
		})
	}
}

func TestReleaseContractsDetectDriftWithRetainedAnnotations(t *testing.T) {
	for _, dep := range DefaultDependencies() {
		t.Run(dep.Name, func(t *testing.T) {
			tests := []struct {
				name   string
				mutate func(*admissionv1.ValidatingAdmissionPolicy, *admissionv1.ValidatingAdmissionPolicyBinding)
			}{
				{"expression", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
					p.Spec.Validations[0].Expression = allowAllValidation
				}},
				{"policy matching", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
					p.Spec.MatchConstraints.ResourceRules[0].Operations = []admissionv1.OperationType{admissionv1.Connect}
				}},
				{"identity", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
					p.Spec.Variables[0].Expression = "'different-identity'"
				}},
				{"match condition", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
					p.Spec.MatchConditions = append(p.Spec.MatchConditions, admissionv1.MatchCondition{Name: "bypass", Expression: "false"})
				}},
				{"policy namespace selector", func(p *admissionv1.ValidatingAdmissionPolicy, _ *admissionv1.ValidatingAdmissionPolicyBinding) {
					p.Spec.MatchConstraints.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"guard": "selected"}}
				}},
				{"binding namespace selector", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
					ensureBindingMatch(b)
					b.Spec.MatchResources.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"guard": "selected"}}
				}},
				{"binding object selector", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
					ensureBindingMatch(b)
					b.Spec.MatchResources.ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"guard": "selected"}}
				}},
				{"binding operations", func(_ *admissionv1.ValidatingAdmissionPolicy, b *admissionv1.ValidatingAdmissionPolicyBinding) {
					ensureBindingMatch(b)
					b.Spec.MatchResources.ResourceRules = []admissionv1.NamedRuleWithOperations{{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Connect}, Rule: admissionv1.Rule{APIGroups: []string{"*"}, APIVersions: []string{"*"}, Resources: []string{"*"}}}}}
				}},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					policy, binding := releaseFixture(t, dep, "")
					annotation := policy.Annotations[policyFingerprintAnnotation]
					tt.mutate(policy, binding)
					require.Equal(t, annotation, policy.Annotations[policyFingerprintAnnotation])
					status, err := CheckDependencies(context.Background(), newAdmissionClient(t, policy, binding), []Dependency{dep}, []string{""})
					require.NoError(t, err)
					require.False(t, status.OverallReady)
					require.Contains(t, strings.Join(status.Dependencies[0].Issues, " | "), "specification differs")
				})
			}
		})
	}
}

func ensureBindingMatch(binding *admissionv1.ValidatingAdmissionPolicyBinding) {
	if binding.Spec.MatchResources == nil {
		binding.Spec.MatchResources = &admissionv1.MatchResources{}
	}
}

func TestReleaseContractsAcceptOnlyAPIDefaultsAndMetadataChanges(t *testing.T) {
	for _, dep := range DefaultDependencies() {
		t.Run(dep.Name, func(t *testing.T) {
			policy, binding := releaseFixture(t, dep, "")
			policy.Annotations = nil
			normalizePolicy(&policy.Spec)
			normalizeBinding(&binding.Spec)
			policy.Spec.MatchConstraints.NamespaceSelector.MatchLabels = map[string]string{}
			policy.Spec.MatchConstraints.ObjectSelector.MatchExpressions = []metav1.LabelSelectorRequirement{}
			status, err := CheckDependencies(context.Background(), newAdmissionClient(t, policy, binding), []Dependency{dep}, []string{""})
			require.NoError(t, err)
			require.True(t, status.OverallReady, status.SummaryMessage())
		})
	}
}

func TestReleaseContractInstallationSettings(t *testing.T) {
	for _, tt := range []struct{ name, key, value string }{
		{"invalid namespace", "POD_NAMESPACE", "bad namespace"},
		{"invalid controller", "OPERATOR_SERVICE_ACCOUNT_NAME", "bad'identity"},
		{"invalid provisioner", "OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME", "bad'identity"},
		{"invalid mode", "OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE", "disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, err := installationFromEnvironment()
			require.Error(t, err)
		})
	}
}

func TestExplicitInstallationPrefixDoesNotAcceptFallback(t *testing.T) {
	t.Setenv("OPERATOR_NAME_PREFIX", "different-installation-")
	dep := DefaultDependencies()[0]
	policy, binding := releaseFixture(t, dep, "openbao-operator-")
	status, err := CheckDependencies(context.Background(), newAdmissionClient(t, policy, binding), []Dependency{dep}, DefaultNamePrefixes())
	require.NoError(t, err)
	require.False(t, status.OverallReady)
}

func TestBindingCannotReferenceAnotherInstallation(t *testing.T) {
	dep := DefaultDependencies()[0]
	policy, binding := releaseFixture(t, dep, "other-")
	binding.Name = "first-" + dep.BindingName
	status, err := CheckDependencies(context.Background(), newAdmissionClient(t, policy, binding), []Dependency{dep}, []string{"first-", "other-"})
	require.NoError(t, err)
	require.False(t, status.OverallReady)
	require.Contains(t, status.SummaryMessage(), "references unexpected policy")
}

func TestReleaseDependencyCannotOmitCompiledContract(t *testing.T) {
	dep := DefaultDependencies()[0]
	policy, binding := releaseFixture(t, dep, "")
	require.ErrorContains(t, verifyReleaseContract(dep, policy, binding, map[string]policyContract{}), "no embedded release admission contract")
}
