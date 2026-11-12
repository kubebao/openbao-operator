package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

// A GitOps controller that syncs the operator or the OpenBao resources during
// the procedure would undo it: it would start the old operator again, or prune
// a resource whose owner is moving. The tool never changes Git or a GitOps
// object. It finds the Argo CD Applications and Flux HelmReleases and
// Kustomizations that manage the installation, and requires each one, and
// every parent that could revert it, to stay suspended. After commit, update
// Git to the destination release and kubebao.org, then resume them.

const (
	fluxHelmName           = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespace      = "helm.toolkit.fluxcd.io/namespace"
	fluxKustomizeName      = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNamespace = "kustomize.toolkit.fluxcd.io/namespace"
	argoTrackingID         = "argocd.argoproj.io/tracking-id"
	argoInstanceLabel      = "app.kubernetes.io/instance"
	gitopsParentDepth      = 5
)

var (
	fluxHelmRelease    = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	fluxKustomization  = schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}
	argoApplication    = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
	argoApplicationSet = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSet"}
)

// gitopsTrackingKey reports whether a label or annotation key is GitOps
// tracking metadata: Argo CD's tracking ID, or Flux's ownership labels.
func gitopsTrackingKey(key string) bool {
	return key == argoTrackingID || strings.HasPrefix(key, "kustomize.toolkit.fluxcd.io/") ||
		strings.HasPrefix(key, "helm.toolkit.fluxcd.io/")
}

// GitOpsManager is one GitOps object that manages part of the installation.
type GitOpsManager struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Manages says what it manages: the operator or OpenBao resources.
	Manages []string `json:"manages"`
}

func (m GitOpsManager) String() string {
	if m.Namespace == "" {
		return m.Kind + " " + m.Name
	}
	return m.Kind + " " + m.Namespace + "/" + m.Name
}

// planGitOps records every GitOps manager of the operator and the OpenBao
// resources, and requires them to be suspended.
func (p *Procedure) planGitOps(ctx context.Context) error {
	var managed []metav1.Object
	var labels []string
	for _, deployment := range p.plan.Deployments {
		object := &metav1.PartialObjectMetadata{}
		object.SetGroupVersionKind(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
		if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: p.plan.Config.OperatorNamespace,
			Name: deployment.Name}, object); err != nil {
			return err
		}
		managed, labels = append(managed, object), append(labels, "the operator")
	}
	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		namespace := p.scopeNamespace()
		if kind == tenantKind {
			namespace = ""
		}
		objects, err := p.kube.listObjects(ctx, primitives.PlanSourceAPIVersion, kind, namespace)
		if err != nil {
			return err
		}
		for i := range objects {
			managed, labels = append(managed, &objects[i]), append(labels, kind+"s")
		}
	}
	managers := map[string]*GitOpsManager{}
	for i, object := range managed {
		found, err := p.gitopsManagers(ctx, object)
		if err != nil {
			return err
		}
		for _, manager := range found {
			key := manager.String()
			if managers[key] == nil {
				managers[key] = &manager
			}
			if !slices.Contains(managers[key].Manages, labels[i]) {
				managers[key].Manages = append(managers[key].Manages, labels[i])
			}
		}
	}
	p.plan.GitOps = nil
	for _, manager := range managers {
		p.plan.GitOps = append(p.plan.GitOps, *manager)
	}
	slices.SortFunc(p.plan.GitOps, func(a, b GitOpsManager) int { return strings.Compare(a.String(), b.String()) })
	report := p.gitopsFreezeReport(ctx)
	if err := report.check(); err != nil {
		return err
	}
	if p.log != nil && len(report) > 0 {
		_, _ = fmt.Fprint(p.log, report.String())
	}
	return nil
}

// checkGitOps requires every recorded manager, and each parent that could
// revert it, to be suspended. It runs before every step.
func (p *Procedure) checkGitOps(ctx context.Context) error {
	return p.gitopsFreezeReport(ctx).check()
}

// A freeze requirement is an observation, not persistent state. Every step
// reads it again so a resumed parent cannot silently undo a child's pause.
type gitopsFreezeRequirement struct {
	manager GitOpsManager
	state   string
	problem error
}

type gitopsFreezeReport []gitopsFreezeRequirement

func (r gitopsFreezeReport) String() string {
	var b strings.Builder
	b.WriteString("GitOps freeze requirements (suspend or protect parents before children):\n")
	for i, requirement := range r {
		fmt.Fprintf(&b, "  %d. %s: %s; manages %s\n", i+1, requirement.manager,
			requirement.state, strings.Join(requirement.manager.Manages, ", "))
		if requirement.problem != nil {
			fmt.Fprintf(&b, "     %s\n", requirement.problem)
		}
	}
	b.WriteString("Keep every listed manager paused or protected for the whole procedure. Do not prune.\n")
	return b.String()
}

func (r *gitopsFreezeReport) add(requirement gitopsFreezeRequirement) {
	for i := range *r {
		existing := &(*r)[i]
		if existing.manager.String() != requirement.manager.String() || existing.state != requirement.state ||
			fmt.Sprint(existing.problem) != fmt.Sprint(requirement.problem) {
			continue
		}
		for _, managed := range requirement.manager.Manages {
			if !slices.Contains(existing.manager.Manages, managed) {
				existing.manager.Manages = append(existing.manager.Manages, managed)
			}
		}
		return
	}
	*r = append(*r, requirement)
}

func (r gitopsFreezeReport) check() error {
	for _, requirement := range r {
		if requirement.problem != nil {
			return errors.New(r.String() + "Resolve every freeze requirement, then run the command again.")
		}
	}
	return nil
}

func (p *Procedure) gitopsFreezeReport(ctx context.Context) gitopsFreezeReport {
	var report gitopsFreezeReport
	for _, manager := range p.plan.GitOps {
		p.gitopsFreezeRequirements(ctx, manager, gitopsParentDepth, "", &report)
	}
	return report
}

// gitopsManagers reads the tracking metadata on one object.
func (p *Procedure) gitopsManagers(ctx context.Context, object metav1.Object) ([]GitOpsManager, error) {
	var managers []GitOpsManager
	labels, annotations := object.GetLabels(), object.GetAnnotations()
	if name := labels[fluxHelmName]; name != "" {
		managers = append(managers, GitOpsManager{Kind: fluxHelmRelease.Kind, Namespace: labels[fluxHelmNamespace], Name: name})
	}
	if name := labels[fluxKustomizeName]; name != "" {
		managers = append(managers, GitOpsManager{Kind: fluxKustomization.Kind, Namespace: labels[fluxKustomizeNamespace],
			Name: name})
	}
	application := ""
	if id := annotations[argoTrackingID]; id != "" {
		application, _, _ = strings.Cut(id, ":")
	} else if name := labels[argoInstanceLabel]; name != "" {
		// Label tracking shares the Helm instance label: it counts only if
		// such an Application exists.
		application = name
	}
	if application != "" {
		found, err := p.findApplications(ctx, application)
		if err != nil {
			return nil, err
		}
		if len(found) == 0 && annotations[argoTrackingID] != "" {
			// A tracking ID is always Argo CD's. Its Application may live in
			// another cluster, where the tool cannot check it.
			namespace, name, qualified := strings.Cut(application, "_")
			if !qualified {
				namespace, name = "", application
			}
			found = []GitOpsManager{{Kind: argoApplication.Kind, Namespace: namespace, Name: name}}
		}
		managers = append(managers, found...)
	}
	return managers, nil
}

// findApplications resolves an Argo CD application name, "<namespace>_<name>"
// when applications live in any namespace.
func (p *Procedure) findApplications(ctx context.Context, application string) ([]GitOpsManager, error) {
	namespace, name, qualified := strings.Cut(application, "_")
	if !qualified {
		namespace, name = "", application
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(argoApplication.GroupVersion().WithKind("ApplicationList"))
	err := p.kube.client.List(ctx, list, client.InNamespace(namespace))
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list Argo CD Applications: %w", err)
	}
	var found []GitOpsManager
	for _, item := range list.Items {
		if item.GetName() == name {
			found = append(found, GitOpsManager{Kind: argoApplication.Kind, Namespace: item.GetNamespace(), Name: name})
		}
	}
	return found, nil
}

// gitopsFreezeRequirements visits parents before adding a child's state, even
// when the child is active. The depth bound also rejects dependency cycles.
func (p *Procedure) gitopsFreezeRequirements(ctx context.Context, manager GitOpsManager, depth int,
	application string, report *gitopsFreezeReport,
) {
	requirement := gitopsFreezeRequirement{manager: manager, state: "unknown"}
	if depth == 0 {
		requirement.problem = fmt.Errorf("%s is managed by too many nested GitOps objects", manager)
		report.add(requirement)
		return
	}
	gvk := map[string]schema.GroupVersionKind{fluxHelmRelease.Kind: fluxHelmRelease,
		fluxKustomization.Kind: fluxKustomization, argoApplication.Kind: argoApplication,
		argoApplicationSet.Kind: argoApplicationSet}[manager.Kind]
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	var err error
	if manager.Kind == argoApplication.Kind && manager.Namespace == "" {
		err = apierrors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: "applications"}, manager.Name)
	} else {
		err = p.kube.client.Get(ctx, client.ObjectKey{Namespace: manager.Namespace, Name: manager.Name}, object)
	}
	if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
		requirement.state = "unknown: controller is not visible in this cluster"
		if p.plan.Config.ExternalGitOpsSuspended {
			requirement.state += "; administrator confirmed external suspension"
		} else {
			requirement.problem = fmt.Errorf("%s manages part of the installation but is not in this cluster; "+
				"suspend it and its parents where they run, then set externalGitOpsSuspended in the config "+
				"(leftover tracking metadata needs the same confirmation)", manager)
		}
		report.add(requirement)
		return
	}
	if err != nil {
		requirement.problem = fmt.Errorf("read %s: %w", manager, err)
		report.add(requirement)
		return
	}
	parents, err := p.gitopsManagers(ctx, object)
	if err != nil {
		requirement.problem = err
	}
	for _, parent := range parents {
		if parent.String() == manager.String() {
			continue
		}
		parent.Manages = []string{manager.String()}
		p.gitopsFreezeRequirements(ctx, parent, depth-1, "", report)
	}
	if manager.Kind == argoApplication.Kind {
		for _, owner := range object.GetOwnerReferences() {
			if owner.Kind == argoApplicationSet.Kind && strings.HasPrefix(owner.APIVersion, argoApplicationSet.Group+"/") {
				parent := GitOpsManager{Kind: owner.Kind, Namespace: object.GetNamespace(), Name: owner.Name,
					Manages: []string{manager.String() + " sync policy"}}
				p.gitopsFreezeRequirements(ctx, parent, depth-1, object.GetName(), report)
			}
		}
	}
	state, problem := p.gitopsFreezeState(ctx, manager, object, application)
	requirement.state = state
	requirement.problem = errors.Join(requirement.problem, problem)
	report.add(requirement)
}

func (p *Procedure) gitopsFreezeState(ctx context.Context, manager GitOpsManager,
	object *unstructured.Unstructured, application string,
) (string, error) {
	switch manager.Kind {
	case argoApplication.Kind:
		automated, found, _ := unstructured.NestedMap(object.Object, "spec", "syncPolicy", "automated")
		enabled, set, _ := unstructured.NestedBool(object.Object, "spec", "syncPolicy", "automated", "enabled")
		if found && automated != nil && (!set || enabled) {
			return "active: automatic sync enabled", fmt.Errorf("%s syncs automatically; disable automatic sync "+
				"after protecting its parents; use only the operator checkpoint instructions for manual syncs", manager)
		}
		return "automatic sync disabled", nil
	case argoApplicationSet.Kind:
		if ignoresSyncPolicy(object, application) {
			return "protected: ignores this Application's sync policy", nil
		}
		policy, err := p.applicationSetPolicy(ctx, object)
		if err != nil {
			return "unknown: ApplicationSet controller policy", err
		}
		if policy == "create-only" || policy == "create-delete" {
			return "protected: effective applicationsSync=" + policy, nil
		}
		state := "unprotected: ApplicationSet can restore automatic sync"
		if !strings.HasPrefix(policy, "its effective policy is ") {
			state = "unknown: ApplicationSet controller policy is not visible"
		}
		return state, fmt.Errorf(
			"%s would restore the sync policy of Application %s (%s); add an ignoreApplicationDifferences "+
				"rule for /spec/syncPolicy that covers it", manager, application, policy)
	default:
		if suspended, _, _ := unstructured.NestedBool(object.Object, "spec", "suspend"); !suspended {
			return "active: spec.suspend is not true", fmt.Errorf("%s is not suspended; set spec.suspend=true", manager)
		}
		return "suspended: spec.suspend=true", nil
	}
}

// ignoresSyncPolicy reports whether an ignore rule keeps the live sync policy
// of the named Application.
func ignoresSyncPolicy(set *unstructured.Unstructured, application string) bool {
	rules, _, _ := unstructured.NestedSlice(set.Object, "spec", "ignoreApplicationDifferences")
	return slices.ContainsFunc(rules, func(item any) bool {
		rule, _ := item.(map[string]any)
		if name, _ := rule["name"].(string); name != "" && name != application {
			return false
		}
		pointers, _ := rule["jsonPointers"].([]any)
		expressions, _ := rule["jqPathExpressions"].([]any)
		return slices.ContainsFunc(pointers, func(pointer any) bool {
			return pointer == "/spec/syncPolicy" || pointer == "/spec/syncPolicy/automated"
		}) || slices.ContainsFunc(expressions, func(expression any) bool {
			return expression == ".spec.syncPolicy" || expression == ".spec.syncPolicy.automated"
		})
	})
}

// Argo CD's ApplicationSet controller: its labels, the flags that set its
// policy, and their argocd-cmd-params-cm keys.
const (
	argoApplicationSetControllerName = "argocd-applicationset-controller"
	argoPolicyKey                    = "applicationsetcontroller.policy"
	argoPolicyOverrideKey            = "applicationsetcontroller.enable.policy.override"
	argoPolicyEnv                    = "ARGOCD_APPLICATIONSET_CONTROLLER_POLICY"
	argoPolicyOverrideEnv            = "ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_POLICY_OVERRIDE"
)

// applicationSetPolicy is the policy the ApplicationSet controllers apply to
// set's Applications. Every controller in the cluster must apply a policy that
// never updates them; the result names the first one that would, or explains
// why none could be read.
func (p *Procedure) applicationSetPolicy(ctx context.Context, set *unstructured.Unstructured) (string, error) {
	requested, _, _ := unstructured.NestedString(set.Object, "spec", "syncPolicy", "applicationsSync")
	deployments := &appsv1.DeploymentList{}
	if err := p.kube.client.List(ctx, deployments,
		client.MatchingLabels{"app.kubernetes.io/name": argoApplicationSetControllerName}); err != nil {
		return "", fmt.Errorf("read the ApplicationSet controller: %w", err)
	}
	if len(deployments.Items) == 0 {
		return "the ApplicationSet controller and its policy were not found in this cluster", nil
	}
	effective := ""
	for i := range deployments.Items {
		policy, override, err := p.applicationSetControllerPolicy(ctx, &deployments.Items[i])
		if err != nil {
			return "", err
		}
		applied := requested
		if policy != "" && (!override || requested == "") {
			applied = policy
		}
		if applied == "" {
			applied = "sync"
		}
		if applied != "create-only" && applied != "create-delete" {
			return "its effective policy is " + applied, nil
		}
		effective = applied
	}
	return effective, nil
}

// applicationSetControllerPolicy reads the controller's --policy and
// --enable-policy-override, from its arguments or from the environment the
// install manifests fill from argocd-cmd-params-cm.
func (p *Procedure) applicationSetControllerPolicy(ctx context.Context, deployment *appsv1.Deployment,
) (string, bool, error) {
	containers := deployment.Spec.Template.Spec.Containers
	index := slices.IndexFunc(containers, func(c corev1.Container) bool { return c.Name == argoApplicationSetControllerName })
	if index < 0 {
		if len(containers) == 0 {
			return "", false, nil
		}
		index = 0
	}
	container := containers[index]
	policy, override := "", false
	for _, env := range container.Env {
		value, key := env.Value, ""
		if ref := env.ValueFrom; ref != nil && ref.ConfigMapKeyRef != nil {
			key = ref.ConfigMapKeyRef.Key
			config := &corev1.ConfigMap{}
			err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: deployment.Namespace,
				Name: ref.ConfigMapKeyRef.Name}, config)
			if err != nil && !apierrors.IsNotFound(err) {
				return "", false, fmt.Errorf("read ApplicationSet controller configuration: %w", err)
			}
			value = config.Data[key]
		}
		switch {
		case env.Name == argoPolicyEnv || key == argoPolicyKey:
			policy = value
		case env.Name == argoPolicyOverrideEnv || key == argoPolicyOverrideKey:
			override = value == "true"
		}
	}
	args := slices.Concat(container.Command, container.Args)
	for i, arg := range args {
		switch {
		case arg == "--policy" && i+1 < len(args):
			policy = args[i+1]
		case strings.HasPrefix(arg, "--policy="):
			policy = strings.TrimPrefix(arg, "--policy=")
		case arg == "--enable-policy-override":
			override = true
		case strings.HasPrefix(arg, "--enable-policy-override="):
			override = strings.TrimPrefix(arg, "--enable-policy-override=") == "true"
		}
	}
	return policy, override, nil
}

// gitopsAfterCommit describes the desired Git state without changing it.
func (p *Procedure) gitopsAfterCommit() string { return p.gitopsHandback(false) }

func (p *Procedure) gitopsHandback(rollback bool) string {
	if len(p.plan.GitOps) == 0 {
		return ""
	}
	release, group, outcome := p.plan.DestinationVersion, "kubebao.org/v1alpha1", "commit"
	if rollback {
		release, group, outcome = p.plan.Config.SourceVersion, "openbao.org/v1alpha1", "rollback"
	}
	if release == "" {
		release = "the verified destination release"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GitOps hand-back after %s (manual actions):\n", outcome)
	fmt.Fprintf(&b, "1. Update the operator source to %s and the OpenBao manifests to apiVersion %s.\n", release, group)
	b.WriteString("   Remove the API migration hold and stopped components. Set controller.apiMigrationHold=false.\n")
	for _, deployment := range p.plan.Deployments {
		fmt.Fprintf(&b, "   Restore Deployment %s/%s to %d replica(s).\n",
			p.plan.Config.OperatorNamespace, deployment.Name, deployment.Replicas)
	}
	if rollback {
		b.WriteString("   Restore the original source chart/manifests and source values.\n")
		if p.plan.Operator.Install == installHelm {
			fmt.Fprintf(&b, "   The saved source Helm revision is %d.\n", p.plan.Helm.SourceRevision)
		}
		b.WriteString("   Revert Git changes prepared for kubebao.org; the source objects remain authoritative.\n" +
			"   Restore source-compatible helper pins and any custom spec.backup.image cleared during preparation.\n" +
			"   If Git still describes the source installation, Git needs no change. Confirm that each live\n" +
			"   Application source matches Git. If a live Application keeps checkpoint values, follow steps 3 and 4\n" +
			"   to replace them. Then confirm that each controller's desired state matches the live source and\n" +
			"   restore the recorded sync settings. For Flux, run flux resume for each suspended object, parents first.\n")
	} else {
		b.WriteString("   Convert every managed Cluster, Restore and Tenant manifest, including shared platform bundles.\n" +
			"   api-migration convert --write <files> rewrites the API version; review the resulting Git diff.\n" +
			"   Source objects have been retired. Unconverted Git can recreate them with different UIDs.\n" +
			"   Keep the prepared destination release helper pins. Restore any cleared custom spec.backup.image with a destination-compatible image before resuming the Cluster.\n" +
			"   Update other references to the openbao.org group: Argo CD AppProject resource allow-lists,\n" +
			"   ignoreDifferences rules and health customizations, RBAC apiGroups, and scripts.\n")
	}
	values := p.plan.Helm.SourceValues
	if !rollback {
		values = mergeValues(deepCopyMap(values), deepCopyMap(p.plan.Helm.DestinationOverlay))
	}
	for _, path := range releasePinnedValues {
		if value, found, _ := unstructured.NestedString(values, path...); found {
			if value == "" {
				value = "(release default)"
			}
			fmt.Fprintf(&b, "   Prepared %s: %s\n", strings.Join(path, "."), value)
		}
	}
	b.WriteString("   Review helper pins in the prepared operator manifests or values, including OPERATOR_BACKUP_IMAGE/extraEnv.\n" +
		"2. Stage a Git change that keeps every child paused while updating parents and sources.\n" +
		"   Set Flux children spec.suspend=true in Git, even if they normally declare spec.suspend:false.\n" +
		"   The children are the objects listed under step 4. The freeze requirements below name their parents.\n" +
		"   Keep Argo child automatic sync disabled and ApplicationSet sync-policy protection in place.\n" +
		"   Publish the intended chart or manifest artifact before any parent can resume its children.\n" +
		"3. Resume parents from the outermost level inward, keeping their children paused.\n" +
		"   Wait for each parent to apply the staged revision and confirm that its children remain paused.\n" +
		"   For Flux, require a Ready source with current observedGeneration and the intended artifact.revision.\n" +
		"   A Ready condition or the old source artifact alone does not prove that the new revision is served.\n" +
		"4. Review the rendered child output: intended release/group, hold disabled, planned replicas and helper pins.\n" +
		"   Resume a child only after its own source artifact matches that output.\n" +
		"   Imported resources lack the old GitOps tracking metadata, so Argo CD reports them OutOfSync until\n" +
		"   their first sync adds it. Checkpoint values set on a live Application must not remain: confirm that\n" +
		"   its source matches Git after the parent applies the staged revision.\n")
	for _, manager := range p.plan.GitOps {
		fmt.Fprintf(&b, "   - %s (manages %s)\n", manager, strings.Join(manager.Manages, ", "))
		if manager.Kind == fluxHelmRelease.Kind {
			if rollback {
				b.WriteString("     Resume a HelmRelease only after its chart source serves the restored source chart.\n")
			} else {
				b.WriteString("     Resume a HelmRelease only after its chart source serves the destination chart.\n")
			}
		}
	}
	b.WriteString("5. Remove child suspension in a separate Git change or resume it manually, then verify reconciliation.\n" +
		"   Flux keeps a suspension set with flux suspend or kubectl after Git drops the field: run flux resume.\n" +
		"   Restore parent automation and ApplicationSet policy only after the child desired state is safe.\n" +
		"   Keep pruning disabled until the reviewed source fully matches the completed migration or rollback.\n")
	return b.String()
}

// writeGitOpsHandback retains a reviewable runbook. It never changes Git,
// source artifacts, suspension, or controller policies.
func (p *Procedure) writeGitOpsHandback(ctx context.Context, rollback bool) (string, error) {
	instructions := p.gitopsHandback(rollback)
	if instructions == "" {
		return "", nil
	}
	instructions += "\n" + p.gitopsFreezeReport(ctx).String()
	name := afterCommitFile
	if rollback {
		name = afterRollbackFile
	}
	path := p.state.path(name)
	if err := writePrivate(path, []byte(instructions)); err != nil {
		return "", fmt.Errorf("write GitOps hand-back: %w", err)
	}
	return instructions + "(also in " + path + ")\n", nil
}
