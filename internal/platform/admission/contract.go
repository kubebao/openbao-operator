package admission

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

//go:generate make -C ../../.. admission-contracts

// releaseContracts contains specifications from config/policy at build time.
// Installed annotations and installed identity expressions are never trust inputs.
//
//go:embed zz_generated.contracts.json
var releaseContracts []byte

type contractSpecification struct {
	Policy  admissionv1.ValidatingAdmissionPolicySpec        `json:"policy"`
	Binding admissionv1.ValidatingAdmissionPolicyBindingSpec `json:"binding"`
}

type policyContract struct {
	contractSpecification
	Kustomize *contractSpecification `json:"kustomize"`
}

const externalPodSecurityMode = "external"

type installation struct {
	namespace       string
	controller      string
	provisioner     string
	podSecurityMode string
}

func installationFromEnvironment() (installation, error) {
	result := installation{
		namespace:       firstNonempty(os.Getenv("POD_NAMESPACE"), os.Getenv("OPERATOR_NAMESPACE"), "openbao-operator-system"),
		controller:      firstNonempty(os.Getenv("OPERATOR_SERVICE_ACCOUNT_NAME"), "openbao-operator-controller"),
		provisioner:     firstNonempty(os.Getenv("OPERATOR_PROVISIONER_SERVICE_ACCOUNT_NAME"), "openbao-operator-provisioner"),
		podSecurityMode: firstNonempty(os.Getenv("OPENBAO_TENANT_NAMESPACE_POD_SECURITY_LABELS_MODE"), "enforce"),
	}
	if len(validation.IsDNS1123Label(result.namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(result.controller)) != 0 ||
		len(validation.IsDNS1123Subdomain(result.provisioner)) != 0 {
		return installation{}, fmt.Errorf("admission contract installation identities must be valid Kubernetes names")
	}
	if result.podSecurityMode != "enforce" && result.podSecurityMode != externalPodSecurityMode {
		return installation{}, fmt.Errorf("admission contract namespace Pod Security label mode must be enforce or external")
	}
	return result, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func expectedContracts(settings installation) (map[string]policyContract, error) {
	contracts := map[string]policyContract{}
	if err := json.Unmarshal(releaseContracts, &contracts); err != nil {
		return nil, fmt.Errorf("decode release admission contracts: %w", err)
	}
	for name, item := range contracts {
		substituteIdentities(&item.Policy, settings)
		if item.Kustomize != nil {
			substituteIdentities(&item.Kustomize.Policy, settings)
		}
		if name == dependencyOpenBaoRestrictProvisionerNamespace && settings.podSecurityMode == externalPodSecurityMode {
			// External-label mode is a Helm variant. Kustomize manages the labels.
			item.Policy.Validations[1] = admissionv1.Validation{
				Expression: "!variables.is_provisioner",
				Message:    "The Provisioner may not mutate Namespaces when tenant namespace Pod Security labels are externally managed.",
			}
		}
		if settings.podSecurityMode == externalPodSecurityMode {
			item.Kustomize = nil
		}
		contracts[name] = item
	}
	return contracts, nil
}

func substituteIdentities(spec *admissionv1.ValidatingAdmissionPolicySpec, settings installation) {
	for i := range spec.Variables {
		variable := &spec.Variables[i]
		switch variable.Name {
		case "operator_namespace":
			variable.Expression = "'" + settings.namespace + "'"
		case "controller_serviceaccount_name":
			variable.Expression = "'" + settings.controller + "'"
		case "provisioner_serviceaccount_name":
			variable.Expression = "'" + settings.provisioner + "'"
		}
	}
}

func verifyReleaseContract(dep Dependency, policy *admissionv1.ValidatingAdmissionPolicy, binding *admissionv1.ValidatingAdmissionPolicyBinding, contracts map[string]policyContract) error {
	expected, ok := contracts[dep.PolicyName]
	if !ok {
		for _, required := range DefaultDependencies() {
			if required.PolicyName == dep.PolicyName {
				return fmt.Errorf("policy %q has no embedded release admission contract", dep.PolicyName)
			}
		}
		// Custom inventories can add presence-only dependencies. Release dependencies
		// always require a compiled specification, including when generation is broken.
		return nil
	}
	actualPolicy := policy.Spec.DeepCopy()
	actualBinding := binding.Spec.DeepCopy()
	normalizePolicy(actualPolicy)
	normalizeBinding(actualBinding)
	variants := []contractSpecification{expected.contractSpecification}
	if expected.Kustomize != nil {
		variants = append(variants, *expected.Kustomize)
	}
	policyMatches := false
	for _, variant := range variants {
		// Copies prevent normalization from changing another dependency's contract.
		expectedPolicy := variant.Policy.DeepCopy()
		expectedBinding := variant.Binding.DeepCopy()
		normalizePolicy(expectedPolicy)
		normalizeBinding(expectedBinding)
		expectedBinding.PolicyName = policy.Name
		if !apiequality.Semantic.DeepEqual(actualPolicy, expectedPolicy) {
			continue
		}
		policyMatches = true
		if apiequality.Semantic.DeepEqual(actualBinding, expectedBinding) {
			return nil
		}
	}
	if policyMatches {
		return fmt.Errorf("binding %q specification differs from the release admission contract", binding.Name)
	}
	return fmt.Errorf("policy %q specification differs from the release admission contract", policy.Name)
}

func normalizePolicy(spec *admissionv1.ValidatingAdmissionPolicySpec) {
	// Only API defaults are normalized. CEL, identity values, selectors, rules,
	// and their order remain part of the exact release contract.
	if spec.FailurePolicy == nil {
		value := admissionv1.Fail
		spec.FailurePolicy = &value
	}
	normalizeMatchResources(spec.MatchConstraints)
}

func normalizeBinding(spec *admissionv1.ValidatingAdmissionPolicyBindingSpec) {
	// Omitted binding matchResources adds no filter. An empty matchResources has
	// the same effect after the API defaults its selectors and match policy.
	if spec.MatchResources == nil {
		spec.MatchResources = &admissionv1.MatchResources{}
	}
	normalizeMatchResources(spec.MatchResources)
}

func normalizeMatchResources(match *admissionv1.MatchResources) {
	if match == nil {
		return
	}
	if match.MatchPolicy == nil {
		value := admissionv1.Equivalent
		match.MatchPolicy = &value
	}
	if match.NamespaceSelector == nil {
		match.NamespaceSelector = &metav1.LabelSelector{}
	}
	if match.ObjectSelector == nil {
		match.ObjectSelector = &metav1.LabelSelector{}
	}
	for i := range match.ResourceRules {
		normalizeRule(&match.ResourceRules[i])
	}
	for i := range match.ExcludeResourceRules {
		normalizeRule(&match.ExcludeResourceRules[i])
	}
}

func normalizeRule(rule *admissionv1.NamedRuleWithOperations) {
	if rule.Scope == nil {
		value := admissionv1.AllScopes
		rule.Scope = &value
	}
}
