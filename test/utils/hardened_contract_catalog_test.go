package utils

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/internal/platform/hardenedcontract"
	hardenedfixtures "github.com/kubebao/openbao-operator/test/fixtures/hardenedcontract"
)

const hardenedPolicyPath = "../../config/policy/openbao-validate-openbaocluster.yaml"

type validatingAdmissionPolicy struct {
	Spec struct {
		Validations []struct {
			Expression string `json:"expression"`
			Message    string `json:"message"`
		} `json:"validations"`
	} `json:"spec"`
}

type fixtureCoverage struct {
	admission map[hardenedcontract.RuleID]int
	runtime   map[hardenedcontract.RuleID]int
	agreement map[hardenedcontract.RuleID]int
	accepted  int
}

func TestHardenedRuleCatalogMatchesAdmissionPolicy(t *testing.T) {
	data, err := os.ReadFile(hardenedPolicyPath)
	if err != nil {
		t.Fatalf("read Hardened admission policy: %v", err)
	}

	var policy validatingAdmissionPolicy
	if err := yaml.Unmarshal(data, &policy); err != nil {
		t.Fatalf("decode Hardened admission policy: %v", err)
	}

	policyMessages := make(map[string]struct{})
	for _, validation := range policy.Spec.Validations {
		if !strings.Contains(validation.Expression, `"Hardened"`) {
			continue
		}
		if _, duplicate := policyMessages[validation.Message]; duplicate {
			t.Fatalf("Hardened admission message is not unique: %q", validation.Message)
		}
		policyMessages[validation.Message] = struct{}{}
	}

	catalogMessages := make(map[string]hardenedcontract.RuleID)
	for _, rule := range hardenedcontract.Rules() {
		if !rule.EnforcedBy(hardenedcontract.LayerAdmissionPolicy) {
			continue
		}
		if rule.AdmissionMessage == "" {
			t.Fatalf("admission-owned rule %q has no admission message", rule.ID)
		}
		if previous, duplicate := catalogMessages[rule.AdmissionMessage]; duplicate {
			t.Fatalf(
				"admission message %q is shared by rules %q and %q",
				rule.AdmissionMessage,
				previous,
				rule.ID,
			)
		}
		catalogMessages[rule.AdmissionMessage] = rule.ID
	}

	for message := range policyMessages {
		if _, found := catalogMessages[message]; !found {
			t.Errorf("Hardened admission validation is missing from catalog: %q", message)
		}
	}
	for message, ruleID := range catalogMessages {
		if _, found := policyMessages[message]; !found {
			t.Errorf("catalog rule %q is missing from admission policy: %q", ruleID, message)
		}
	}
}

func TestHardenedRuleCatalogOwnershipAndFixtureCoverage(t *testing.T) {
	rules := hardenedcontract.Rules()
	byID := catalogByID(t, rules)
	coverage := collectFixtureCoverage(t, byID)

	if coverage.accepted != 2 {
		t.Fatalf("accepted fixture count = %d, want 2", coverage.accepted)
	}

	assertRuleCoverage(t, rules, coverage)
}

func catalogByID(
	t *testing.T,
	rules []hardenedcontract.Rule,
) map[hardenedcontract.RuleID]hardenedcontract.Rule {
	t.Helper()

	byID := make(map[hardenedcontract.RuleID]hardenedcontract.Rule, len(rules))
	for _, rule := range rules {
		if rule.ID == "" {
			t.Fatal("catalog contains an empty rule ID")
		}
		if _, duplicate := byID[rule.ID]; duplicate {
			t.Fatalf("catalog contains duplicate rule ID %q", rule.ID)
		}
		if len(rule.Layers) == 0 {
			t.Fatalf("catalog rule %q has no enforcement owner", rule.ID)
		}
		for _, layer := range rule.Layers {
			switch layer {
			case hardenedcontract.LayerCRDSchema,
				hardenedcontract.LayerAdmissionPolicy,
				hardenedcontract.LayerRuntimeReadiness:
			default:
				t.Fatalf("catalog rule %q has unknown layer %q", rule.ID, layer)
			}
		}
		if !rule.EnforcedBy(hardenedcontract.LayerAdmissionPolicy) && rule.AdmissionMessage != "" {
			t.Fatalf("non-admission rule %q declares an admission message", rule.ID)
		}
		byID[rule.ID] = rule
	}

	return byID
}

func collectFixtureCoverage(
	t *testing.T,
	byID map[hardenedcontract.RuleID]hardenedcontract.Rule,
) fixtureCoverage {
	t.Helper()

	fixtureNames := make(map[string]struct{})
	coverage := fixtureCoverage{
		admission: make(map[hardenedcontract.RuleID]int),
		runtime:   make(map[hardenedcontract.RuleID]int),
		agreement: make(map[hardenedcontract.RuleID]int),
	}

	for _, fixture := range hardenedfixtures.Fixtures() {
		if fixture.Name == "" {
			t.Fatal("fixture has an empty name")
		}
		if _, duplicate := fixtureNames[fixture.Name]; duplicate {
			t.Fatalf("fixture name %q is duplicated", fixture.Name)
		}
		fixtureNames[fixture.Name] = struct{}{}

		if fixture.AdmissionRule == "" && fixture.RuntimeRule == "" {
			coverage.accepted++
		}
		if fixture.AuthorizationOnly && fixture.AdmissionRule == "" {
			t.Fatalf("authorization-only fixture %q has no admission rule", fixture.Name)
		}

		if fixture.AdmissionRule != "" {
			rule, found := byID[fixture.AdmissionRule]
			if !found {
				t.Fatalf("fixture %q references unknown admission rule %q", fixture.Name, fixture.AdmissionRule)
			}
			if !rule.EnforcedBy(hardenedcontract.LayerAdmissionPolicy) {
				t.Fatalf("fixture %q maps admission to non-admission rule %q", fixture.Name, rule.ID)
			}
			coverage.admission[rule.ID]++
		}

		if fixture.RuntimeRule != "" {
			rule, found := byID[fixture.RuntimeRule]
			if !found {
				t.Fatalf("fixture %q references unknown runtime rule %q", fixture.Name, fixture.RuntimeRule)
			}
			if !rule.EnforcedBy(hardenedcontract.LayerRuntimeReadiness) {
				t.Fatalf("fixture %q maps runtime to non-runtime rule %q", fixture.Name, rule.ID)
			}
			coverage.runtime[rule.ID]++
		}

		if fixture.AdmissionRule != "" && fixture.AdmissionRule == fixture.RuntimeRule {
			coverage.agreement[fixture.AdmissionRule]++
		}
	}

	return coverage
}

func assertRuleCoverage(
	t *testing.T,
	rules []hardenedcontract.Rule,
	coverage fixtureCoverage,
) {
	t.Helper()

	for _, rule := range rules {
		if rule.EnforcedBy(hardenedcontract.LayerCRDSchema) {
			t.Errorf("Hardened rule %q unexpectedly claims CRD/CEL ownership", rule.ID)
		}
		if rule.EnforcedBy(hardenedcontract.LayerAdmissionPolicy) && coverage.admission[rule.ID] == 0 {
			t.Errorf("admission rule %q has no fixture", rule.ID)
		}
		if rule.EnforcedBy(hardenedcontract.LayerRuntimeReadiness) && coverage.runtime[rule.ID] == 0 {
			t.Errorf("runtime/readiness rule %q has no fixture", rule.ID)
		}
		if rule.EnforcedBy(hardenedcontract.LayerAdmissionPolicy) &&
			rule.EnforcedBy(hardenedcontract.LayerRuntimeReadiness) &&
			coverage.agreement[rule.ID] == 0 {
			t.Errorf("shared rule %q has no same-rule verdict-agreement fixture", rule.ID)
		}
	}
}
