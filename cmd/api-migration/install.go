package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
)

// The operator is installed either as a Helm release, by the Helm CLI or a
// Flux HelmRelease, or as rendered manifests: a chart rendered by Argo CD, the
// published install.yaml, or a Kustomize overlay. The procedure drives a Helm
// release itself. For manifests it stops at each operator transition: it
// writes what to apply, and on the next run it observes that state on the
// cluster and continues. It never applies the manifests itself.

const (
	installHelm      = "helm"
	installManifests = "manifests"

	// Manifest flavours select the instructions a checkpoint writes.
	flavourChart     = "chart"
	flavourKustomize = "kustomize"

	holdArgument     = "--api-migration-hold=true"
	managerContainer = "manager"
)

// operatorState is a state of the operator installation the procedure needs.
type operatorState struct {
	name    string
	group   string // the API group whose admission policies are live
	running bool   // every Deployment ready at its planned replicas
	held    bool   // the controller runs with the API migration hold
}

var (
	stateStopped = operatorState{name: "stopped", group: "kubebao.org"}
	stateHeld    = operatorState{name: "held", group: "kubebao.org", running: true, held: true}
	stateNormal  = operatorState{name: "normal", group: "kubebao.org", running: true}
	stateSource  = operatorState{name: "source", group: "openbao.org", running: true}
)

// checkpointError stops the procedure until the administrator applies a state.
type checkpointError struct {
	State        string
	Instructions string
}

func (e *checkpointError) Error() string {
	return fmt.Sprintf("waiting for the %s operator state: apply it as described in %s, then run the command again",
		e.State, e.Instructions)
}

// operatorObservation is the installation as the cluster shows it.
type operatorObservation struct {
	group          string
	stopped        bool
	running        bool
	readyAtDesired bool // current Deployment replicas, including counts restored after hand-back
	held           bool
	crds           bool
	// version is the controller's OPERATOR_VERSION.
	version     string
	backupImage *corev1.EnvVar
}

func (o operatorObservation) matches(state operatorState) bool {
	if o.group != state.group || o.held != state.held {
		return false
	}
	if state.running {
		return o.running
	}
	return o.stopped && o.crds
}

func (o operatorObservation) String() string {
	return fmt.Sprintf("policies %s, stopped=%t, running=%t, held=%t", o.group, o.stopped, o.running, o.held)
}

// isSourceChart compares a release's chart with the wanted one. It ignores
// semver build metadata: Flux helm-controller appends the OCI artifact digest,
// as in openbao-operator-0.6.1+96dc4cba8574, when it installs a chart from an
// OCIRepository.
func isSourceChart(chart, want string) bool {
	chart, _, _ = strings.Cut(chart, "+")
	return chart == want
}

// planInstall finds the operator Deployments and reads how the source
// operator is configured: its tenancy, JWT audience, and version.
func (p *Procedure) planInstall(ctx context.Context) error {
	config := p.plan.Config
	selector := client.MatchingLabels{"app.kubernetes.io/name": "openbao-operator"}
	p.plan.Operator.Install = installManifests
	if config.Release != "" {
		p.plan.Operator.Install = installHelm
		selector = client.MatchingLabels{"app.kubernetes.io/instance": config.Release}
	}
	if err := p.planDeployments(ctx, selector); err != nil {
		return err
	}
	controller, err := p.controllerDeployment(ctx)
	if err != nil {
		return err
	}
	if config.Release != "" {
		if err := p.resolveHelmNamespace(ctx, controller); err != nil {
			return err
		}
		latest, err := p.helm.Latest(ctx)
		if err != nil {
			return fmt.Errorf("release %s: %w", config.Release, err)
		}
		wantChart := "openbao-operator-" + config.SourceVersion
		if latest.Status != helmDeployed || !isSourceChart(latest.Chart, wantChart) {
			return fmt.Errorf("release must be a deployed %s, found %s (%s)", wantChart, latest.Chart, latest.Status)
		}
		targetNamespace, err := p.helm.ReleaseTargetNamespace(ctx)
		if err != nil {
			return err
		}
		if targetNamespace != config.OperatorNamespace {
			return fmt.Errorf("Helm release %s in storage namespace %s targets namespace %s, not operatorNamespace %s; "+
				"select the installed operator release without changing its storage or target namespace",
				config.Release, p.helm.Namespace, targetNamespace, config.OperatorNamespace)
		}
		values, err := p.helm.Values(ctx, latest.Revision)
		if err != nil {
			return err
		}
		p.plan.Helm = HelmPlan{SourceRevision: latest.Revision, SourceChart: latest.Chart, SourceValues: values}
	}
	env := map[string]string{}
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name != managerContainer {
			continue
		}
		for _, variable := range container.Env {
			env[variable.Name] = variable.Value
		}
		if slices.Contains(container.Args, holdArgument) {
			return errors.New("the source controller already runs with the API migration hold")
		}
	}
	p.plan.Operator.Version = env["OPERATOR_VERSION"]
	if p.plan.Operator.Install == installManifests && p.plan.Operator.Version != config.SourceVersion {
		return fmt.Errorf("the controller runs operator version %q, the config nominates %q",
			p.plan.Operator.Version, config.SourceVersion)
	}
	p.plan.Operator.Mode, p.plan.Operator.TargetNamespace = tenancyMulti, ""
	if namespace := env["WATCH_NAMESPACE"]; namespace != "" {
		p.plan.Operator.Mode, p.plan.Operator.TargetNamespace = "single", namespace
	}
	p.plan.Operator.Audience = portauth.OperatorJWTAudience(env["OPENBAO_JWT_AUDIENCE"])
	p.plan.Operator.Flavour = flavourKustomize
	if _, chart := controller.Labels["helm.sh/chart"]; chart {
		p.plan.Operator.Flavour = flavourChart
	}
	observed, err := p.observeOperator(ctx)
	if err != nil {
		return err
	}
	p.plan.Operator.BackupImage = observed.backupImage
	if !observed.matches(stateSource) {
		return fmt.Errorf("the source operator is not running normally: %s", observed)
	}
	return nil
}

func (p *Procedure) controllerDeployment(ctx context.Context) (*appsv1.Deployment, error) {
	for _, deployment := range p.plan.Deployments {
		if !deployment.Controller {
			continue
		}
		object := &appsv1.Deployment{}
		if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: p.plan.Config.OperatorNamespace,
			Name: deployment.Name}, object); err != nil {
			return nil, err
		}
		return object, nil
	}
	return nil, errors.New("no controller Deployment was planned")
}

// installedPolicy finds this installation's live policy with the given base
// name: its name ends in it, and it pins the operator namespace.
func (p *Procedure) installedPolicy(ctx context.Context, base string) (*admissionv1.ValidatingAdmissionPolicy, error) {
	policies := &admissionv1.ValidatingAdmissionPolicyList{}
	if err := p.kube.client.List(ctx, policies); err != nil {
		return nil, err
	}
	namespace := "'" + p.plan.Config.OperatorNamespace + "'"
	var found []*admissionv1.ValidatingAdmissionPolicy
	for i := range policies.Items {
		policy := &policies.Items[i]
		if strings.HasSuffix(policy.Name, base) && slices.ContainsFunc(policy.Spec.Variables,
			func(v admissionv1.Variable) bool {
				return v.Name == "operator_namespace" && strings.TrimSpace(v.Expression) == namespace
			}) {
			found = append(found, policy)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("expected one %s policy for operator namespace %s, found %d", base,
			p.plan.Config.OperatorNamespace, len(found))
	}
	return found[0], nil
}

// observeOperator reads the operator state from the cluster, whatever installed it.
func (p *Procedure) observeOperator(ctx context.Context) (operatorObservation, error) {
	observed := operatorObservation{stopped: true, running: true, readyAtDesired: true}
	controllerActive := false
	policy, err := p.installedPolicy(ctx, "openbao-validate-openbaocluster")
	if err != nil {
		return observed, err
	}
	if rules := policy.Spec.MatchConstraints; rules != nil && len(rules.ResourceRules) > 0 &&
		len(rules.ResourceRules[0].APIGroups) > 0 {
		observed.group = rules.ResourceRules[0].APIGroups[0]
	}
	for _, planned := range p.plan.Deployments {
		deployment := &appsv1.Deployment{}
		if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: p.plan.Config.OperatorNamespace,
			Name: planned.Name}, deployment); err != nil {
			return observed, err
		}
		pods, err := p.kube.deploymentPods(ctx, p.plan.Config.OperatorNamespace, planned.Name)
		if err != nil {
			return observed, err
		}
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		observed.stopped = observed.stopped && replicas == 0 && len(pods) == 0
		ready, err := p.kube.deploymentReady(ctx, p.plan.Config.OperatorNamespace, planned.Name)
		if err != nil {
			return observed, err
		}
		observed.running = observed.running && ready && replicas == planned.Replicas
		observed.readyAtDesired = observed.readyAtDesired && ready
		if planned.Controller {
			controllerActive = replicas > 0
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name != managerContainer {
					continue
				}
				observed.held = observed.held || slices.Contains(container.Args, holdArgument)
				for _, variable := range container.Env {
					if variable.Name == "OPERATOR_VERSION" {
						observed.version = variable.Value
					}
				}
				observed.backupImage = backupHelperImage(container.Env, observed.version)
			}
		}
	}
	observed.readyAtDesired = observed.readyAtDesired && controllerActive
	observed.crds = true
	for _, name := range []string{"openbaoclusters", "openbaorestores", "openbaotenants"} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		err := p.kube.client.Get(ctx, client.ObjectKey{Name: name + ".kubebao.org"}, crd)
		if apierrors.IsNotFound(err) {
			observed.crds = false
			continue
		}
		if err != nil {
			return observed, err
		}
	}
	return observed, nil
}

// awaitOperator continues once the installation is in state. Otherwise it
// writes what to apply and stops the procedure until the next run.
func (p *Procedure) awaitOperator(ctx context.Context, state operatorState) (string, error) {
	observed, err := p.observeOperator(ctx)
	if err != nil {
		return "", err
	}
	if !observed.matches(state) && p.plan.Operator.Checkpoint == state.name {
		// The administrator was asked for this state and ran the command again:
		// give it a short while to settle, for example for a rollout to finish.
		settle, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_ = p.kube.wait(settle, "the "+state.name+" operator state", func(ctx context.Context) (bool, error) {
			current, err := p.observeOperator(ctx)
			return err == nil && current.matches(state), nil
		})
		cancel()
		if observed, err = p.observeOperator(ctx); err != nil {
			return "", err
		}
	}
	if observed.matches(state) {
		if state.name != stateSource.name {
			if err := destinationRelease(observed.version); err != nil {
				return "", err
			}
			if err := p.checkBackupHelperImage(observed.backupImage); err != nil {
				return "", err
			}
		}
		return "the operator is in the " + state.name + " state (" + observed.String() + ")", nil
	}
	input, label := p.plan.Config.DestinationManifests, "destinationManifests"
	if state.name == stateSource.name {
		input, label = p.plan.Config.SourceManifests, "sourceManifests"
	}
	if err := validateOperatorInput(ctx, label, input); err != nil {
		return "", err
	}
	path, err := p.writeCheckpoint(state)
	if err != nil {
		return "", err
	}
	p.plan.Operator.Checkpoint = state.name
	if err := p.state.Save(p.plan); err != nil {
		return "", err
	}
	return "", &checkpointError{State: state.name, Instructions: path}
}

// Kustomize components shipped in config/components; a test keeps these equal.
const (
	componentStopped = `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
# Stops every operator Deployment for the API migration handoff.
patches:
  - target:
      kind: Deployment
      labelSelector: app.kubernetes.io/name=openbao-operator
    patch: |-
      - op: add
        path: /spec/replicas
        value: 0
`
	componentHold = `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
# Starts the controller with the API migration hold: it reconciles workloads but
# runs no upgrade, backup, or restore until the migration is committed.
patches:
  - target:
      kind: Deployment
      labelSelector: app.kubernetes.io/name=openbao-operator,app.kubernetes.io/component=controller
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/args/-
        value: --api-migration-hold=true
`
)

// writeCheckpoint writes what to apply for state and returns the file to read.
func (p *Procedure) writeCheckpoint(state operatorState) (string, error) {
	dir := p.state.path("checkpoints")
	for name, content := range map[string]string{"api-migration-stopped": componentStopped,
		"api-migration-hold": componentHold} {
		if err := os.MkdirAll(filepath.Join(dir, "components", name), 0o700); err != nil {
			return "", err
		}
		if err := writePrivate(filepath.Join(dir, "components", name, "kustomization.yaml"), []byte(content)); err != nil {
			return "", err
		}
	}
	components := map[string][]string{
		stateStopped.name: {"api-migration-stopped"}, stateHeld.name: {"api-migration-hold"},
	}[state.name]
	source := state.name == stateSource.name
	manifests := p.plan.Config.DestinationManifests
	if source {
		manifests = p.plan.Config.SourceManifests
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Apply the %s operator state\n\n", state.name)
	switch state.name {
	case stateStopped.name:
		b.WriteString("Install the destination release with every operator Deployment scaled to zero.\n")
	case stateHeld.name:
		b.WriteString("Start the destination release with the controller's API migration hold.\n")
	case stateNormal.name:
		b.WriteString("Run the destination release normally, without the hold.\n")
	case stateSource.name:
		fmt.Fprintf(&b, "Re-apply the source release (operator version %s) as it was before the migration.\n",
			p.plan.Operator.Version)
	}
	b.WriteString("Keep GitOps sync suspended. Apply only operator resources at this checkpoint; do not prune.\n" +
		"Do not apply a full platform bundle or any OpenBaoCluster, OpenBaoRestore or OpenBaoTenant.\n" +
		"The tool imports new-group resources itself. Earlier checkpoints must not create them from Git.\n\n")
	if state.name == stateNormal.name {
		b.WriteString("The source CRs have already been retired. A full sync of unconverted Git recreates old-group CRs.\n" +
			"Change only the operator Deployments to remove the hold and restore their planned replicas.\n\n")
	}
	p.checkpointGitOpsInstructions(&b, state)
	if p.plan.Operator.Flavour == flavourChart {
		values := map[string]any{}
		for _, deployment := range p.plan.Deployments {
			component := componentProvisioner
			if deployment.Controller {
				component = componentController
			}
			replicas := deployment.Replicas
			if state.name == stateStopped.name {
				replicas = 0
			}
			values[component] = map[string]any{"replicas": replicas}
		}
		if !source {
			controller, _ := values["controller"].(map[string]any)
			controller["apiMigrationHold"] = state.held
		}
		data, err := yaml.Marshal(values)
		if err != nil {
			return "", err
		}
		path := filepath.Join(dir, state.name+"-values.yaml")
		if err := writePrivate(path, data); err != nil {
			return "", err
		}
		if source {
			b.WriteString("Render the source chart version with your original values and these replicas:\n\n")
		} else {
			b.WriteString("Render the destination chart version with your values, the destination image, and:\n\n")
		}
		fmt.Fprintf(&b, "```yaml\n%s```\n\nThe same values are in %s. Layer them over the original operator values.\n"+
			"For a multi-source Application, change only the verified operator Helm source; preserve the other sources.\n"+
			"Review its rendered operator resources before applying them. Tracking metadata does not identify the source type.\n",
			data, path)
	} else {
		if len(components) > 0 {
			fmt.Fprintf(&b, "Add the Kustomize component(s) %s from config/components of the destination release\n"+
				"to your installation (copies are in %s), and apply it.\n",
				strings.Join(components, ", "), filepath.Join(dir, "components"))
		} else if source {
			b.WriteString("Apply your source installation without the API migration components.\n")
		} else {
			b.WriteString("Apply your destination installation without the API migration components.\n")
		}
		if manifests != "" {
			kustomization := filepath.Join(dir, state.name)
			if err := os.MkdirAll(kustomization, 0o700); err != nil {
				return "", err
			}
			resource, err := checkpointResource(kustomization, manifests)
			if err != nil {
				return "", err
			}
			content := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " + resource + "\n"
			if len(components) > 0 {
				content += "components:\n"
				for _, component := range components {
					content += "  - ../components/" + component + "\n"
				}
			}
			if err := writePrivate(filepath.Join(kustomization, "kustomization.yaml"), []byte(content)); err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\nThe configured manifests (%s) must contain only the operator installation.\n"+
				"If they include OpenBao CRs or other platform resources, prepare an operator-only path first.\n"+
				"The migration command owns the operator Deployments' replica fields while they are stopped.\n"+
				"Use --force-conflicts to apply these reviewed operator-only inputs.\n"+
				"Inspect the rendered output before applying this checkpoint:\n\n```sh\n%s\n%s\n```\n",
				manifests, commandLine("kubectl", "kustomize", kustomization),
				p.checkpointKubectl("apply", "--server-side", "--force-conflicts", "-k", kustomization))
		}
	}
	command := p.plan.Progress.Command
	if command == "" {
		command = "migrate"
		if state.name == stateNormal.name {
			command = "commit"
		} else if source {
			command = "rollback"
		}
	}
	fmt.Fprintf(&b, "\nAfter the operator reaches this state, run:\n\n```sh\n%s\n```\n",
		p.stateCommand(command))
	path := filepath.Join(dir, state.name+".md")
	return path, writePrivate(path, []byte(b.String()))
}

// checkpointGitOpsInstructions distinguishes controller ownership from the
// renderer. A tracking label alone does not identify a Helm source or overlay.
func (p *Procedure) checkpointGitOpsInstructions(b *strings.Builder, state operatorState) {
	for _, manager := range p.plan.GitOps {
		if !slices.Contains(manager.Manages, "the operator") {
			continue
		}
		fmt.Fprintf(b, "## %s\n\n", manager)
		switch manager.Kind {
		case argoApplication.Kind:
			b.WriteString("Keep this Application and its controlling parents paused. Inspect spec.source/spec.sources.\n" +
				"For Argo-rendered Helm, select the operator chart source and layer the checkpoint values below.\n" +
				"For Kustomize or plain manifests, prepare operator-only output with the required component.\n")
			if state.name == stateNormal.name {
				args := []string{"argocd", "app", "sync", manager.Name}
				if manager.Namespace != "" {
					args = append(args, "--app-namespace", manager.Namespace)
				}
				for _, deployment := range p.plan.Deployments {
					args = append(args, "--resource", "apps:Deployment:"+p.plan.Config.OperatorNamespace+"/"+deployment.Name)
				}
				fmt.Fprintf(b, "Select the Argo CD server that owns this Application. Review its desired Deployments, then\n"+
					"selectively sync only these operator Deployments, without --prune:\n\n```sh\n%s\n```\n",
					commandLine(args...))
			} else {
				b.WriteString("Apply the reviewed operator resources without pruning. If this Application also manages OpenBao CRs,\n" +
					"render or select only the operator installation; do not sync the whole Application.\n")
			}
		case fluxKustomization.Kind:
			b.WriteString("Keep this Flux Kustomization and every parent suspended. Do not reconcile or resume them\n" +
				"to apply a checkpoint. Build and apply operator-only manifests manually with the component below.\n" +
				"A shared Kustomization path that includes OpenBao CRs is not an operator checkpoint.\n")
		case fluxHelmRelease.Kind:
			b.WriteString("Keep this Flux HelmRelease and every parent suspended. A HelmRelease installation uses\n" +
				"config.release so the tool performs its Helm transitions. Do not resume helm-controller to apply a checkpoint.\n")
		}
		b.WriteString("\n")
	}
}

func (p *Procedure) checkpointKubectl(args ...string) string {
	command := make([]string, 0, 5+len(args))
	command = append(command, "kubectl", "--kubeconfig", p.plan.Config.Kubeconfig, "--context", p.plan.Config.Context)
	return commandLine(append(command, args...)...)
}

// checkpointResource references the configured manifests from a checkpoint
// kustomization. Kustomize loads a directory by relative path, but a file only
// from inside the kustomization root, so a manifest file is copied in.
func checkpointResource(kustomization, manifests string) (string, error) {
	info, err := os.Stat(manifests)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(manifests)
		if err != nil {
			return "", err
		}
		return "release.yaml", writePrivate(filepath.Join(kustomization, "release.yaml"), data)
	}
	return relativeResolved(kustomization, manifests)
}

// relativeResolved is the path from base to target after resolving symlinks:
// Kustomize resolves them before it follows a relative path, and a temporary
// directory is often a symlink (/var and /tmp on macOS).
func relativeResolved(base, target string) (string, error) {
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	return filepath.Rel(resolvedBase, resolvedTarget)
}

// backupHelperImage follows the controller's helper-image precedence without
// reading Secrets or ConfigMaps. An opaque repository still gets the new
// operator version; a complete valueFrom override must change its reference.
func backupHelperImage(env []corev1.EnvVar, version string) *corev1.EnvVar {
	variables := map[string]corev1.EnvVar{}
	for _, variable := range env {
		variables[variable.Name] = variable
	}
	image := variables[constants.EnvOperatorBackupImage]
	if image.ValueFrom != nil || strings.TrimSpace(image.Value) != "" {
		image.Value = strings.TrimSpace(image.Value)
		return image.DeepCopy()
	}
	repository := variables[constants.EnvOperatorBackupImageRepo]
	if repository.ValueFrom != nil {
		return nil
	}
	repo := strings.TrimSpace(repository.Value)
	if repo == "" {
		repo = constants.DefaultBackupImageRepository
	}
	return &corev1.EnvVar{Name: constants.EnvOperatorBackupImage, Value: repo + ":" + version}
}

func (p *Procedure) checkBackupHelperImage(image *corev1.EnvVar) error {
	if image != nil && p.plan.Operator.BackupImage != nil && reflect.DeepEqual(image, p.plan.Operator.BackupImage) {
		return errors.New("the destination controller retains the source backup helper selection; change OPERATOR_BACKUP_IMAGE (including controller.extraEnv) or clear it to use the destination default before continuing")
	}
	return nil
}

func (p *Procedure) checkDestinationBackupHelper(ctx context.Context) error {
	controller, err := p.controllerDeployment(ctx)
	if err != nil {
		return err
	}
	for _, container := range controller.Spec.Template.Spec.Containers {
		if container.Name != managerContainer {
			continue
		}
		version := ""
		for _, variable := range container.Env {
			if variable.Name == constants.EnvOperatorVersion {
				version = variable.Value
			}
		}
		return p.checkBackupHelperImage(backupHelperImage(container.Env, version))
	}
	return errors.New("the destination controller has no manager container")
}
