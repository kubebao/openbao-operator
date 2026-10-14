package admission

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Dependency identifies a required ValidatingAdmissionPolicy and its Binding.
type Dependency struct {
	// Name is a stable identifier for logs and status messages.
	Name string
	// PolicyName is the base metadata.name of the ValidatingAdmissionPolicy in manifests.
	PolicyName string
	// BindingName is the base metadata.name of the ValidatingAdmissionPolicyBinding in manifests.
	BindingName string
	// ExpectedFingerprint identifies the required policy content revision.
	ExpectedFingerprint string
}

// DependencyStatus contains the evaluation result for a single Dependency.
type DependencyStatus struct {
	Dependency Dependency
	Ready      bool
	Issues     []string
}

// Status summarizes admission dependency readiness.
type Status struct {
	CheckedAt    time.Time
	Dependencies []DependencyStatus
	OverallReady bool
	UnsafeMode   bool
}

const (
	// PolicyFingerprintAnnotation stores the source-policy content fingerprint.
	// Helm keeps this value when it renders installation-specific names and identities.
	PolicyFingerprintAnnotation = "openbao.org/admission-policy-fingerprint"

	fingerprintLockControllerStatefulSetMutations  = "sha256:71df31f013ae60eb423266e14891db3753db4276907db82d070f9ae088c028ae"
	fingerprintOpenBaoValidateOpenBaoRestore       = "sha256:4031663c07f1bd884a1a2fd386168fbec7221b0450355b753a2d2643439870bf"
	fingerprintProtectRestoreExecution             = "sha256:c9af55789ddce38e9c2227b2b4b527abea0af9094e82a56eaa4cc2f17a5b8e69"
	fingerprintOpenBaoValidateOpenBaoCluster       = "sha256:73a46b7b9737a882b1163807593929ecaa475775423a6c02345df96e1e51a130"
	fingerprintOpenBaoLockManagedResourceMutations = "sha256:27a3f087ab0a67377bc466fb87bb500cdd559e9cb92603619123f117b8ecfcc7"

	dependencyOpenBaoValidateOpenBaoCluster             = "openbao-validate-openbaocluster"
	dependencyOpenBaoValidateOpenBaoTenant              = "openbao-validate-openbao-tenant"
	dependencyProtectRestoreExecution                   = "openbao-protect-restore-execution"
	dependencyProtectRestoreExecutionBinding            = "openbao-protect-restore-execution-binding"
	dependencyOpenBaoValidateOpenBaoRestore             = "openbao-validate-openbaorestore"
	dependencyOpenBaoLockControllerStatefulSetMutations = "openbao-lock-controller-statefulset-mutations"
	dependencyOpenBaoLockManagedResourceMutations       = "openbao-lock-managed-resource-mutations"
	dependencyOpenBaoEnforceManagedImageDigests         = "openbao-enforce-managed-image-digests"
	dependencyOpenBaoRestrictProvisionerRBAC            = "openbao-restrict-provisioner-rbac"
	dependencyOpenBaoRestrictProvisionerNamespace       = "openbao-restrict-provisioner-namespace-mutations"
	dependencyOpenBaoRestrictProvisionerTenantGuardrail = "openbao-restrict-provisioner-tenant-governance"
	dependencyOpenBaoRestrictControllerRBAC             = "openbao-restrict-controller-rbac"
	dependencyOpenBaoRestrictControllerServiceAccounts  = "openbao-restrict-controller-serviceaccounts"
	dependencyOpenBaoRestrictControllerSecretWrites     = "openbao-restrict-controller-secret-writes"
	dependencyBindingValidateOpenBaoCluster             = "openbao-validate-openbaocluster-binding"
	dependencyBindingValidateOpenBaoTenant              = "openbao-validate-openbao-tenant-binding"
	dependencyBindingValidateOpenBaoRestore             = "openbao-validate-openbaorestore-binding"
	dependencyBindingLockControllerStatefulSetMutations = "openbao-lock-controller-statefulset-mutations-binding"
	dependencyBindingLockManagedResourceMutations       = "openbao-lock-managed-resource-mutations-binding"
	dependencyBindingEnforceManagedImageDigests         = "openbao-enforce-managed-image-digests-binding"
	dependencyBindingRestrictProvisionerRBAC            = "openbao-restrict-provisioner-rbac-binding"
	dependencyBindingRestrictProvisionerNamespace       = "openbao-restrict-provisioner-namespace-mutations-binding"
	dependencyBindingRestrictProvisionerTenantGuardrail = "openbao-restrict-provisioner-tenant-governance-binding"
	dependencyBindingRestrictControllerRBAC             = "openbao-restrict-controller-rbac-binding"
	dependencyBindingRestrictControllerServiceAccounts  = "openbao-restrict-controller-serviceaccounts-binding"
	dependencyBindingRestrictControllerSecretWrites     = "openbao-restrict-controller-secret-writes-binding"
)

// DefaultNamePrefixes returns the resource name prefixes to try when resolving
// admission policy objects.
//
// Kustomize installs use the stable prefix "openbao-operator-".
// Helm installs typically use "<release>-openbao-operator-" (or fullnameOverride).
func DefaultNamePrefixes() []string {
	var prefixes []string

	if env := strings.TrimSpace(os.Getenv("OPERATOR_NAME_PREFIX")); env != "" {
		// Accept either "foo-" or "foo" and normalize to a prefix.
		if !strings.HasSuffix(env, "-") {
			env = env + "-"
		}
		prefixes = append(prefixes, env)
	}
	if controllerSA := strings.TrimSpace(os.Getenv("OPERATOR_SERVICE_ACCOUNT_NAME")); controllerSA != "" {
		if derived, ok := strings.CutSuffix(controllerSA, "controller"); ok {
			prefixes = append(prefixes, derived)
		}
	}

	// Backward compatible defaults.
	prefixes = append(prefixes, "openbao-operator-", "")

	// De-duplicate while keeping order (empty string must remain last if present).
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// DefaultDependencies returns the admission dependencies treated as release-critical.
func DefaultDependencies() []Dependency {
	return []Dependency{
		{
			Name:                dependencyOpenBaoValidateOpenBaoCluster,
			PolicyName:          dependencyOpenBaoValidateOpenBaoCluster,
			BindingName:         dependencyBindingValidateOpenBaoCluster,
			ExpectedFingerprint: fingerprintOpenBaoValidateOpenBaoCluster,
		},
		{
			Name:        dependencyOpenBaoValidateOpenBaoTenant,
			PolicyName:  dependencyOpenBaoValidateOpenBaoTenant,
			BindingName: dependencyBindingValidateOpenBaoTenant,
		},
		{
			Name:                dependencyOpenBaoValidateOpenBaoRestore,
			PolicyName:          dependencyOpenBaoValidateOpenBaoRestore,
			BindingName:         dependencyBindingValidateOpenBaoRestore,
			ExpectedFingerprint: fingerprintOpenBaoValidateOpenBaoRestore,
		},
		{Name: dependencyProtectRestoreExecution, PolicyName: dependencyProtectRestoreExecution,
			BindingName: dependencyProtectRestoreExecutionBinding, ExpectedFingerprint: fingerprintProtectRestoreExecution},
		{
			Name:                dependencyOpenBaoLockControllerStatefulSetMutations,
			PolicyName:          dependencyOpenBaoLockControllerStatefulSetMutations,
			BindingName:         dependencyBindingLockControllerStatefulSetMutations,
			ExpectedFingerprint: fingerprintLockControllerStatefulSetMutations,
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
			Name:                dependencyOpenBaoLockManagedResourceMutations,
			PolicyName:          dependencyOpenBaoLockManagedResourceMutations,
			BindingName:         dependencyBindingLockManagedResourceMutations,
			ExpectedFingerprint: fingerprintOpenBaoLockManagedResourceMutations,
		},
		{
			Name:        dependencyOpenBaoEnforceManagedImageDigests,
			PolicyName:  dependencyOpenBaoEnforceManagedImageDigests,
			BindingName: dependencyBindingEnforceManagedImageDigests,
		},
	}
}

// CheckDependencies validates that the required ValidatingAdmissionPolicy and
// ValidatingAdmissionPolicyBinding objects exist and are configured to enforce denies.
//
// namePrefixes are tried in order when resolving object names (for example,
// "openbao-operator-" and "").
func CheckDependencies(ctx context.Context, c client.Reader, deps []Dependency, namePrefixes []string) (Status, error) {
	if ctx == nil {
		return Status{}, fmt.Errorf("context is required")
	}
	if c == nil {
		return Status{}, fmt.Errorf("kubernetes client reader is required")
	}
	if len(deps) == 0 {
		return Status{}, fmt.Errorf("at least one dependency is required")
	}
	if len(namePrefixes) == 0 {
		return Status{}, fmt.Errorf("at least one name prefix is required")
	}

	status := Status{
		CheckedAt: time.Now(),
	}

	var overallReady = true

	for _, dep := range deps {
		depStatus := checkDependency(ctx, c, dep, namePrefixes)
		status.Dependencies = append(status.Dependencies, depStatus)
		if !depStatus.Ready {
			overallReady = false
		}
	}

	status.OverallReady = overallReady

	return status, nil
}

// SummaryMessage returns a single-line summary for logs, Events, or Conditions.
// It is safe to log (contains no sensitive data).
func (s Status) SummaryMessage() string {
	if s.UnsafeMode {
		return "Admission policies are disabled by unsafe mode"
	}

	if s.OverallReady {
		return "Required admission policies are installed and correctly bound"
	}

	parts := make([]string, 0, len(s.Dependencies))
	for _, dep := range s.Dependencies {
		if dep.Ready {
			continue
		}
		if len(dep.Issues) == 0 {
			parts = append(parts, fmt.Sprintf("%s: not ready", dep.Dependency.Name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", dep.Dependency.Name, strings.Join(dep.Issues, "; ")))
	}
	if len(parts) == 0 {
		return "Admission policies are not ready"
	}
	return "Admission policies are not ready: " + strings.Join(parts, " | ")
}

func checkDependency(ctx context.Context, c client.Reader, dep Dependency, namePrefixes []string) DependencyStatus {
	depStatus := DependencyStatus{
		Dependency: dep,
		Ready:      true,
	}

	policyNameCandidates := buildNameCandidates(dep.PolicyName, namePrefixes)
	bindingNameCandidates := buildNameCandidates(dep.BindingName, namePrefixes)

	binding, bindingName, err := getFirstFoundBinding(ctx, c, bindingNameCandidates)
	if err != nil {
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("failed to read ValidatingAdmissionPolicyBinding: %v", err))
		return depStatus
	}
	if binding == nil {
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("missing ValidatingAdmissionPolicyBinding (%s)", strings.Join(bindingNameCandidates, " or ")))
		return depStatus
	}

	policyName := binding.Spec.PolicyName
	if policyName == "" {
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("binding %q has empty spec.policyName", bindingName))
		return depStatus
	}

	if !containsString(policyNameCandidates, policyName) {
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("binding %q references unexpected policy %q", bindingName, policyName))
	}

	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	if err := c.Get(ctx, client.ObjectKey{Name: policyName}, policy); err != nil {
		if apierrors.IsNotFound(err) {
			depStatus.Ready = false
			depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("binding %q references missing policy %q", bindingName, policyName))
			return depStatus
		}
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("failed to read ValidatingAdmissionPolicy %q: %v", policyName, err))
		return depStatus
	}

	if policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionregistrationv1.Fail {
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("policy %q must have failurePolicy=Fail", policyName))
	}
	if dep.ExpectedFingerprint != "" && policy.Annotations[PolicyFingerprintAnnotation] != dep.ExpectedFingerprint {
		depStatus.Ready = false
		depStatus.Issues = append(
			depStatus.Issues,
			fmt.Sprintf("policy %q does not have expected admission policy fingerprint", policyName),
		)
	}

	if !bindingDenies(binding) {
		depStatus.Ready = false
		depStatus.Issues = append(depStatus.Issues, fmt.Sprintf("binding %q must include validationActions=Deny", bindingName))
	}

	return depStatus
}

func buildNameCandidates(base string, namePrefixes []string) []string {
	candidates := make([]string, 0, len(namePrefixes))
	for _, prefix := range namePrefixes {
		candidates = append(candidates, prefix+base)
	}
	return candidates
}

func getFirstFoundBinding(ctx context.Context, c client.Reader, candidates []string) (*admissionregistrationv1.ValidatingAdmissionPolicyBinding, string, error) {
	for _, name := range candidates {
		binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, binding); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, "", err
		}
		return binding, name, nil
	}
	return nil, "", nil
}

func bindingDenies(binding *admissionregistrationv1.ValidatingAdmissionPolicyBinding) bool {
	if binding == nil {
		return false
	}
	for _, action := range binding.Spec.ValidationActions {
		if action == admissionregistrationv1.Deny {
			return true
		}
	}
	return false
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
