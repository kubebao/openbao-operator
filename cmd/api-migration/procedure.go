package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

const (
	unrelatedAudience    = "urn:openbao:controller:api-migration-unrelated"
	helmDeployed         = "deployed"
	componentController  = "controller"
	componentProvisioner = "provisioner"
	alreadyRetired       = "already retired"
)

// Procedure runs one command against a locked state directory.
type Procedure struct {
	state     *State
	plan      *Plan
	kube      *Kube
	helm      Helm
	stopAfter string
	program   string
	log       io.Writer
	bao       map[string]*OpenBao
	forwards  []*portForward
}

// step runs one convergent step. A step reports what it changed, or that it
// found the desired state already in place.
func (p *Procedure) step(ctx context.Context, name string, run func() (string, error)) error {
	_, _ = fmt.Fprintf(p.log, "==> %s\n", name)
	if err := p.checkGitOps(ctx); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if slices.Contains(p.plan.Progress.Completed, name) {
		// A later step would be undone by repeating this one, for example by
		// stopping an operator an administrator started at a checkpoint.
		// Every later step still checks its own preconditions.
		_, _ = fmt.Fprintln(p.log, "    already done")
	} else {
		detail, err := run()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		_, _ = fmt.Fprintf(p.log, "    %s\n", detail)
		p.plan.Progress.Completed = append(p.plan.Progress.Completed, name)
		if name == "verify" || name == "final-verify" || name == "verify-source" {
			apiVersion := primitives.PlanDestinationAPIVersion
			if name == "verify-source" {
				apiVersion = primitives.PlanSourceAPIVersion
			}
			p.plan.LastVerification = &VerificationRecord{CheckedAt: time.Now().UTC(), APIVersion: apiVersion, Step: name}
		}
		if err := p.state.Save(p.plan); err != nil {
			return err
		}
	}
	if p.stopAfter == name {
		return errStop
	}
	return nil
}

// runSteps runs a command's steps in order. A --stop-after name must be one of them.
func (p *Procedure) runSteps(ctx context.Context, steps []struct {
	name string
	run  func() (string, error)
}) error {
	names := make([]string, len(steps))
	for i, step := range steps {
		names[i] = step.name
	}
	if p.stopAfter != "" && !slices.Contains(names, p.stopAfter) {
		return fmt.Errorf("--stop-after %q is not a step; the steps are %s", p.stopAfter, strings.Join(names, ", "))
	}
	for _, step := range steps {
		if err := p.step(ctx, step.name, step.run); err != nil {
			return err
		}
	}
	return nil
}

// begin starts or resumes command: a run continues after its last completed step.
func (p *Procedure) begin(command string) {
	if p.plan.Progress.Command != command {
		p.plan.Progress = Progress{Command: command}
	}
}

func (p *Procedure) setPhase(phase string) error {
	p.plan.Phase = phase
	return p.state.Save(p.plan)
}

// openBao reaches a Cluster's OpenBao through the tool's own port-forward. The
// CA comes from the Cluster's <name>-tls-ca Secret unless the config overrides
// it. A configured administrator token adds Raft evidence and a snapshot.
func (p *Procedure) openBao(ctx context.Context, cluster ClusterPlan) (*OpenBao, error) {
	key := cluster.Namespace + "/" + cluster.Name
	if existing, ok := p.bao[key]; ok {
		return existing, nil
	}
	endpoint := openBaoEndpoint{URL: "https://" + cluster.Name + "." + cluster.Namespace + ".svc:" + openBaoPort,
		ServerName: cluster.Name + "." + cluster.Namespace + ".svc"}
	config, _ := p.plan.Config.cluster(cluster.Namespace, cluster.Name)
	var err error
	if bao := config.OpenBao; bao != nil {
		if bao.TokenFile != "" {
			if endpoint.Token, err = readToken(bao.TokenFile); err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
		if bao.TLSServerName != "" {
			endpoint.ServerName = bao.TLSServerName
		}
		if bao.CAFile != "" {
			if endpoint.CA, err = readFileLimited(bao.CAFile, false, openBaoResponseLimit); err != nil {
				return nil, fmt.Errorf("%s: read OpenBao CA: %w", key, err)
			}
		}
	}
	if endpoint.CA == nil {
		secret := &corev1.Secret{}
		if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name + "-tls-ca"},
			secret); err != nil {
			return nil, fmt.Errorf("%s: read the OpenBao CA Secret (set openbao.caFile for external TLS or ACME): %w", key, err)
		}
		endpoint.CA = secret.Data["ca.crt"]
	}
	forward := newPortForward(p.plan.Config.Kubeconfig, p.plan.Config.Context, p.kube.client,
		cluster.Namespace, cluster.Name, cluster.voterStatefulSet())
	endpoint.Dial = forward.DialContext
	bao, err := newOpenBao(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	p.forwards = append(p.forwards, forward)
	p.bao[key] = bao
	return bao, nil
}

// Close stops the port-forwards the procedure started.
func (p *Procedure) Close() {
	for _, forward := range p.forwards {
		forward.Close()
	}
}

// controllerAudience is the audience the active operator's controller
// presents for a live Cluster. The operator's own function decides it: an
// imported Target Cluster keeps its source identity through the marker.
func (p *Procedure) controllerAudience(object *unstructured.Unstructured) (string, error) {
	cluster := &api.OpenBaoCluster{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, cluster); err != nil {
		return "", fmt.Errorf("decode Cluster: %w", err)
	}
	return portauth.ControllerJWTAudience(cluster, p.plan.Operator.Audience)
}

// verifyLogin logs in as the running controller with the audience it presents
// for the live Cluster, and checks that an unrelated audience is denied. The
// audience must be the one planned: the destination continues the source's.
func (p *Procedure) verifyLogin(ctx context.Context, cluster ClusterPlan, object *unstructured.Unstructured) error {
	audience, err := p.controllerAudienceMatches(cluster, object)
	if err != nil {
		return err
	}
	var controller DeploymentPlan
	for _, deployment := range p.plan.Deployments {
		if deployment.Controller {
			controller = deployment
		}
	}
	pod, err := p.kube.runningPod(ctx, p.plan.Config.OperatorNamespace, controller.Name)
	if err != nil {
		return err
	}
	bao, err := p.openBao(ctx, cluster)
	if err != nil {
		return err
	}
	return verifyControllerLogin(ctx, p.kube.clientset, bao, pod, []string{audience}, []string{unrelatedAudience})
}

// controllerUsesJWT mirrors the operator: its controller logs in to OpenBao
// with a JWT only in Target mode or with self-init OIDC bootstrap.
func controllerUsesJWT(object *unstructured.Unstructured) bool {
	cluster := &api.OpenBaoCluster{}
	if runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, cluster) != nil {
		return true
	}
	return cluster.Spec.ControllerJWTMode == api.ControllerJWTModeTarget || portauth.OperatorJWTBootstrapEnabled(cluster)
}

// controllerAudienceMatches computes the audience the active controller
// presents for the live Cluster and requires the planned one.
func (p *Procedure) controllerAudienceMatches(cluster ClusterPlan, object *unstructured.Unstructured) (string, error) {
	audience, err := p.controllerAudience(object)
	if err != nil {
		return "", err
	}
	if cluster.Audience != "" && audience != cluster.Audience {
		return "", fmt.Errorf("the controller would present %s, the planned audience is %s", audience, cluster.Audience)
	}
	return audience, nil
}

func digestJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// plan

func (p *Procedure) Plan(ctx context.Context) error {
	k := p.kube
	kubeSystem, err := k.namespaceUID(ctx, "kube-system")
	if err != nil {
		return err
	}
	p.plan.KubeSystemUID = kubeSystem
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	p.plan.MigrationID = "m" + hex.EncodeToString(id)

	if err := p.planInstall(ctx); err != nil {
		return err
	}
	if err := p.planGitOps(ctx); err != nil {
		return err
	}
	if p.plan.Operator.Install == installHelm {
		if err := p.planChart(ctx); err != nil {
			return err
		}
	} else if err := p.validateOperatorInputs(ctx); err != nil {
		return err
	}
	// Restore records first: their Jobs carry Cluster labels, and Cluster
	// discovery must know which record controls each one. Tenants are planned
	// in every namespace: they live in the operator or their target namespace.
	if p.plan.Restores, err = p.planRecords(ctx, restoreKind, p.scopeNamespace()); err != nil {
		return err
	}
	if p.plan.Tenants, err = p.planRecords(ctx, tenantKind, ""); err != nil {
		return err
	}
	if err := p.planClusters(ctx); err != nil {
		return err
	}
	p.plan.Phase = phasePlanned
	return p.state.Save(p.plan)
}

// checkMigrator proves that impersonation works and that the migrator can make
// the handoff writes, so a missing grant fails before anything changes.
func (p *Procedure) checkMigrator(ctx context.Context) error {
	review := &authenticationv1.SelfSubjectReview{}
	if err := p.kube.migrator.Create(ctx, review); err != nil {
		return fmt.Errorf("impersonate migrator %s: %w", p.plan.Config.migratorPrincipal(), err)
	}
	if review.Status.UserInfo.Username != p.plan.Config.migratorPrincipal() {
		return fmt.Errorf("impersonation resolved to %q", review.Status.UserInfo.Username)
	}
	parts := strings.Split(p.plan.Config.migratorPrincipal(), ":")
	if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: parts[2], Name: parts[3]}, &corev1.ServiceAccount{}); err != nil {
		return fmt.Errorf("migrator ServiceAccount: %w", err)
	}
	return p.checkMigratorAccess(ctx, p.migratorNamespaces())
}

// checkMigratorAccess checks the handoff grants in each namespace.
func (p *Procedure) checkMigratorAccess(ctx context.Context, namespaces []string) error {
	parts := strings.Split(p.plan.Config.migratorPrincipal(), ":")
	namespaces = slices.Compact(slices.Sorted(slices.Values(namespaces)))
	checks := make([]authorizationv1.ResourceAttributes, 0, len(namespaces)*len(migratorAccess()))
	for _, namespace := range namespaces {
		for _, attributes := range migratorAccess() {
			attributes.Namespace = namespace
			checks = append(checks, attributes)
		}
	}
	for _, attributes := range checks {
		review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User: p.plan.Config.migratorPrincipal(), Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + parts[2]},
			ResourceAttributes: &attributes}}
		if err := p.kube.client.Create(ctx, review); err != nil {
			return fmt.Errorf("check migrator access: %w", err)
		}
		if !review.Status.Allowed {
			return fmt.Errorf("migrator cannot %s %s/%s in %s", attributes.Verb, attributes.Resource,
				attributes.Subresource, attributes.Namespace)
		}
	}
	return nil
}

// migratorAccess lists the namespaced writes the handoff makes as the migrator.
// Ownership references with blockOwnerDeletion also require update on the
// owner's finalizers subresource. Role updates are subject to RBAC escalation checks.
func migratorAccess() []authorizationv1.ResourceAttributes {
	const rbac = "rbac.authorization.k8s.io"
	return []authorizationv1.ResourceAttributes{
		{Verb: "patch", Resource: "secrets"}, {Verb: "patch", Resource: "configmaps"},
		{Verb: "patch", Resource: "services"}, {Verb: "patch", Resource: "serviceaccounts"},
		{Verb: "patch", Resource: "persistentvolumeclaims"},
		{Verb: "patch", Group: "policy", Resource: "poddisruptionbudgets"},
		{Verb: "patch", Group: "networking.k8s.io", Resource: "networkpolicies"},
		{Verb: "patch", Group: "networking.k8s.io", Resource: "ingresses"},
		{Verb: "patch", Group: "gateway.networking.k8s.io", Resource: "httproutes"},
		{Verb: "patch", Group: "gateway.networking.k8s.io", Resource: "tlsroutes"},
		{Verb: "patch", Group: "gateway.networking.k8s.io", Resource: "backendtlspolicies"},
		{Verb: "patch", Group: "monitoring.coreos.com", Resource: "servicemonitors"},
		{Verb: "patch", Group: "cilium.io", Resource: "ciliumnetworkpolicies"},
		{Verb: "patch", Group: rbac, Resource: "roles"}, {Verb: "escalate", Group: rbac, Resource: "roles"},
		{Verb: "patch", Group: rbac, Resource: "rolebindings"}, {Verb: "bind", Group: rbac, Resource: "roles"},
		{Verb: "patch", Group: "apps", Resource: "statefulsets"},
		{Verb: "patch", Group: "batch", Resource: "jobs"},
		{Verb: "create", Group: "apps", Resource: "statefulsets"},
		{Verb: "delete", Group: "apps", Resource: "statefulsets"},
		{Verb: "update", Group: "openbao.org", Resource: "openbaoclusters", Subresource: "finalizers"},
		{Verb: "update", Group: "kubebao.org", Resource: "openbaoclusters", Subresource: "finalizers"},
		{Verb: "update", Group: "openbao.org", Resource: "openbaorestores", Subresource: "finalizers"},
		{Verb: "update", Group: "kubebao.org", Resource: "openbaorestores", Subresource: "finalizers"},
	}
}

func (p *Procedure) planDeployments(ctx context.Context, selector client.MatchingLabels) error {
	deployments := &appsv1.DeploymentList{}
	if err := p.kube.client.List(ctx, deployments, client.InNamespace(p.plan.Config.OperatorNamespace),
		selector); err != nil {
		return err
	}
	for _, deployment := range deployments.Items {
		component := deployment.Labels["app.kubernetes.io/component"]
		if component != componentController && component != componentProvisioner {
			continue
		}
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		p.plan.Deployments = append(p.plan.Deployments, DeploymentPlan{Name: deployment.Name, UID: deployment.UID,
			Replicas: replicas, ServiceAccount: deployment.Spec.Template.Spec.ServiceAccountName,
			Controller: component == componentController})
	}
	controllers := 0
	for _, deployment := range p.plan.Deployments {
		if deployment.Controller {
			controllers++
			if deployment.Replicas != 1 {
				return errors.New("the controller Deployment must run one replica before migrating")
			}
		}
	}
	if controllers != 1 {
		return fmt.Errorf("expected one controller Deployment in %s, found %d", p.plan.Config.OperatorNamespace, controllers)
	}
	return nil
}

// releasePinnedValues are the chart values that select the operator's images
// and reported version. The chart derives each from its own release when the
// value is empty.
var releasePinnedValues = [][]string{
	{"image", "digest"}, {"image", "tag"}, {"operatorVersion"},
	{"helperImages", "init"}, {"helperImages", "backup"}, {"helperImages", "upgrade"},
}

// sourceImagePinned refuses a source release whose values pin a
// release-specific value unless the destination values set the same key.
// Otherwise the destination chart would keep the source release's image or
// version.
func sourceImagePinned(source, overlay map[string]any) error {
	for _, path := range releasePinnedValues {
		pinned, _, _ := unstructured.NestedString(source, path...)
		if pinned == "" {
			continue
		}
		if _, set, _ := unstructured.NestedFieldNoCopy(overlay, path...); !set {
			key := strings.Join(path, ".")
			return fmt.Errorf("the source release values pin %s %q, which the destination chart would keep; "+
				"set %s in destinationValues (an empty string selects the destination chart's default)",
				key, pinned, key)
		}
		if strings.Join(path, ".") == "helperImages.backup" {
			destination, _, _ := unstructured.NestedString(overlay, path...)
			if destination == pinned {
				return fmt.Errorf("helperImages.backup retains the source helper %q; select a destination-compatible helper or an empty string for the destination default", pinned)
			}
		}
	}
	env, _, _ := unstructured.NestedSlice(source, "controller", "extraEnv")
	for _, entry := range env {
		variable, ok := entry.(map[string]any)
		if !ok || variable["name"] != constants.EnvOperatorBackupImage {
			continue
		}
		value, _ := variable["value"].(string)
		if strings.TrimSpace(value) == "" && variable["valueFrom"] == nil {
			continue
		}
		replacement, set, _ := unstructured.NestedSlice(overlay, "controller", "extraEnv")
		if !set {
			return errors.New("controller.extraEnv pins OPERATOR_BACKUP_IMAGE; replace controller.extraEnv in destinationValues to select the destination helper or remove the complete image override")
		}
		for _, candidate := range replacement {
			if digestJSON(candidate) == digestJSON(variable) {
				return errors.New("controller.extraEnv retains the source OPERATOR_BACKUP_IMAGE; select a destination-compatible helper or remove the complete image override")
			}
		}
	}
	return nil
}

// Backup helpers submit restore claims through their compiled Kubernetes API
// group. A source binary cannot submit a claim to the destination group. Keep
// active helper selection in release values across the handover; terminal
// restore records may retain their historical executor image.
func validateBackupHelperPin(source *unstructured.Unstructured) error {
	image, _, err := unstructured.NestedString(source.Object, "spec", "backup", "image")
	if err != nil {
		return fmt.Errorf("read spec.backup.image: %w", err)
	}
	if image != "" {
		return fmt.Errorf("spec.backup.image pins helper %q across the API-group move; pause the Cluster, clear this pin before planning, and select the destination backup helper in the destination release configuration; restore any custom pin for the destination before resuming", image)
	}
	return nil
}

func (p *Procedure) planChart(ctx context.Context) error {
	target := p.state.path("destination-chart")
	if out, err := exec.CommandContext(ctx, "cp", "-R", p.plan.Config.DestinationChart, target).CombinedOutput(); err != nil {
		return fmt.Errorf("copy destination chart: %w: %s", err, out)
	}
	p.plan.Helm.DestinationChart = target
	release, err := chartAppVersion(target)
	if err != nil {
		return err
	}
	if err := destinationRelease(release); err != nil {
		return err
	}
	p.plan.DestinationVersion = release
	overlay := map[string]any{}
	if path := p.plan.Config.DestinationValues; path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read destination values: %w", err)
		}
		if err := yaml.Unmarshal(data, &overlay); err != nil {
			return fmt.Errorf("decode destination values: %w", err)
		}
	}
	if err := sourceImagePinned(p.plan.Helm.SourceValues, overlay); err != nil {
		return err
	}
	p.plan.Helm.DestinationOverlay = overlay
	path, err := p.writeValues("render", p.migrationValues())
	if err != nil {
		return err
	}
	if _, err := p.helm.Template(ctx, target, path); err != nil {
		return fmt.Errorf("render destination chart with migration values: %w", err)
	}
	return nil
}

// chartAppVersion reads the release a chart directory installs.
func chartAppVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return "", fmt.Errorf("read the destination chart: %w", err)
	}
	var chart struct {
		AppVersion string `json:"appVersion"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return "", fmt.Errorf("decode the destination Chart.yaml: %w", err)
	}
	return chart.AppVersion, nil
}

func (p *Procedure) planClusters(ctx context.Context) error {
	k := p.kube
	sources, err := k.listClusters(ctx, primitives.PlanSourceAPIVersion, p.scopeNamespace())
	if err != nil {
		return err
	}
	nominated := len(p.plan.Config.Clusters) != 0
	if nominated && len(sources) != len(p.plan.Config.Clusters) {
		return fmt.Errorf("config nominates %d clusters, the installation has %d", len(p.plan.Config.Clusters), len(sources))
	}
	namespaces := p.restoreNamespaces()
	for _, source := range sources {
		if _, ok := p.plan.Config.cluster(source.GetNamespace(), source.GetName()); nominated && !ok {
			return fmt.Errorf("cluster %s/%s is not nominated in the config", source.GetNamespace(), source.GetName())
		}
		namespaces = append(namespaces, source.GetNamespace())
	}
	if err := p.planMigrator(ctx, namespaces); err != nil {
		return err
	}
	operatorNamespace, err := k.namespaceUID(ctx, p.plan.Config.OperatorNamespace)
	if err != nil {
		return err
	}
	for _, listed := range sources {
		destination, err := k.getCluster(ctx, primitives.PlanDestinationAPIVersion, listed.GetNamespace(), listed.GetName())
		if err != nil {
			return err
		}
		if destination != nil {
			return fmt.Errorf("destination %s/%s already exists", listed.GetNamespace(), listed.GetName())
		}
		// Planning an earlier Cluster takes time; status writes, such as a
		// scheduled backup, change this one's revision meanwhile. Plan the
		// current revision of the same object.
		source, err := k.getCluster(ctx, primitives.PlanSourceAPIVersion, listed.GetNamespace(), listed.GetName())
		if err != nil {
			return err
		}
		if source == nil || source.GetUID() != listed.GetUID() {
			return fmt.Errorf("source %s/%s was deleted or replaced during planning", listed.GetNamespace(), listed.GetName())
		}
		cluster, err := p.planCluster(ctx, source, operatorNamespace)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", source.GetNamespace(), source.GetName(), err)
		}
		p.plan.Clusters = append(p.plan.Clusters, cluster)
	}
	return nil
}

func (p *Procedure) planCluster(ctx context.Context, source *unstructured.Unstructured, operatorNamespace types.UID) (ClusterPlan, error) {
	if err := validateRetirementFinalizers(source); err != nil {
		return ClusterPlan{}, err
	}
	if err := validateBackupHelperPin(source); err != nil {
		return ClusterPlan{}, err
	}
	k := p.kube
	namespaceUID, err := k.namespaceUID(ctx, source.GetNamespace())
	if err != nil {
		return ClusterPlan{}, err
	}
	cluster := ClusterPlan{Namespace: source.GetNamespace(), NamespaceUID: namespaceUID, Name: source.GetName(),
		SourceUID: source.GetUID(), SpecDigest: digestJSON(source.Object["spec"]), Pods: map[string]types.UID{}}
	request := primitives.ImportRequest{
		Source: primitives.ObjectIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: clusterKind,
			Namespace: cluster.Namespace, Name: cluster.Name, UID: cluster.SourceUID, ResourceVersion: source.GetResourceVersion()},
		NamespaceUID:      namespaceUID,
		RelatedNamespaces: []primitives.NamespaceIdentity{{Name: p.plan.Config.OperatorNamespace, UID: operatorNamespace}},
	}
	if _, err := primitives.InspectSource(ctx, k.client, request); err != nil {
		if errors.Is(err, primitives.ErrSourceChanged) {
			err = fmt.Errorf("%w; the operator wrote its status during planning, for example for a scheduled backup: run plan again", err)
		}
		return ClusterPlan{}, err
	}
	typed := &api.OpenBaoCluster{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(source.Object, typed); err != nil {
		return ClusterPlan{}, err
	}
	cluster.VoterStatefulSet = portworkload.StableVoterStatefulSetName(typed)
	if cluster.Retained, err = retainedResources(ctx, k, cluster); err != nil {
		return ClusterPlan{}, err
	}
	// Inventory a settled workload: a rollout replaces Pods while discovery runs.
	if err := workloadSettled(ctx, k, cluster); err != nil {
		return ClusterPlan{}, err
	}
	var preview *primitives.ManagedDiscovery
	if err := p.untilPodsSettle(ctx, cluster, func(ctx context.Context) ([]primitives.DiscoveryBlocker, error) {
		var err error
		preview, err = primitives.DiscoverManagedResources(ctx, k.metadata, p.clusterScope(cluster).options(
			cluster.source(), primitives.TransferIdentity{APIVersion: primitives.PlanDestinationAPIVersion,
				Kind: clusterKind, Namespace: cluster.Namespace, Name: cluster.Name}))
		if err != nil {
			return nil, err
		}
		return preview.Blockers, nil
	}); err != nil {
		return ClusterPlan{}, err
	}
	for _, transfer := range preview.Transfers {
		cluster.Inventory = append(cluster.Inventory, transfer.Resource)
	}
	initialized, _, _ := unstructured.NestedBool(source.Object, "status", "initialized")
	if cluster.Claims, err = claimBindings(ctx, k, cluster, initialized); err != nil {
		return ClusterPlan{}, err
	}
	pods := &corev1.PodList{}
	if err := k.client.List(ctx, pods, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
		return ClusterPlan{}, err
	}
	cluster.Paused, _, _ = unstructured.NestedBool(source.Object, "spec", "paused")
	voterReady := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		cluster.Pods[pod.Name] = pod.UID
		if !podReady(pod) {
			continue
		}
		if cluster.Paused {
			cluster.ReadyPods = append(cluster.ReadyPods, pod.Name)
		}
		voterReady = voterReady || voterPod(pod, cluster.voterStatefulSet())
	}
	slices.Sort(cluster.ReadyPods)
	mode, _, _ := unstructured.NestedString(source.Object, "spec", "controllerJWTMode")
	cluster.Shared = mode != "Target"
	if cluster.Shared {
		if err := p.checkMigrateVerb(ctx, "openbaoclusters", cluster.Namespace, cluster.Name); err != nil {
			return ClusterPlan{}, fmt.Errorf("importing a Shared-mode Cluster: %w", err)
		}
	}
	// Nothing in OpenBao changes: the destination controller keeps this audience.
	if cluster.Audience, err = p.controllerAudience(source); err != nil {
		return ClusterPlan{}, err
	}
	// A paused Cluster may have no OpenBao running, or one an administrator is
	// recovering: its operator does not log in either. The controller identity
	// still carries over, so the login is evidence for it, not a requirement.
	if cluster.Paused && !voterReady {
		cluster.LoginSkipped = "no OpenBao Pod was ready"
		return cluster, nil
	}
	if !controllerUsesJWT(source) {
		// It uses the root token Secret, which moves with the Cluster, or nothing.
		cluster.LoginSkipped = "its controller does not log in with a JWT"
	} else if err := p.verifyLogin(ctx, cluster, source); err != nil {
		if !cluster.Paused {
			return ClusterPlan{}, fmt.Errorf("controller login: %w", err)
		}
		cluster.LoginSkipped = err.Error()
		return cluster, nil
	}
	bao, err := p.openBao(ctx, cluster)
	if err != nil {
		return ClusterPlan{}, err
	}
	if bao.token == "" {
		return cluster, nil
	}
	if cluster.RaftPeers, err = bao.RaftPeers(ctx); err != nil {
		return ClusterPlan{}, fmt.Errorf("read Raft configuration: %w", err)
	}
	cluster.Snapshot = "snapshot-" + cluster.Namespace + "-" + cluster.Name + ".snap"
	if err := bao.Snapshot(ctx, p.state.path(cluster.Snapshot)); err != nil {
		return ClusterPlan{}, fmt.Errorf("save Raft snapshot: %w", err)
	}
	return cluster, nil
}

// checkMigrateVerb confirms the administrator may make a marked import. The
// destination admission exempts a Shared Cluster from the Target requirement,
// and a restore record from controller-only status rules, only for a principal
// granted the migrate verb.
func (p *Procedure) checkMigrateVerb(ctx context.Context, resource, namespace, name string) error {
	review := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: "migrate",
			Group: "kubebao.org", Resource: resource, Name: name}}}
	if err := p.kube.client.Create(ctx, review); err != nil {
		return fmt.Errorf("check migrate access: %w", err)
	}
	if !review.Status.Allowed {
		return fmt.Errorf("the migrate verb on %s.kubebao.org is required", resource)
	}
	return nil
}

// scopeNamespace is where the source operator manages Clusters and restore
// records: the target namespace in single-tenant mode, otherwise every one.
func (p *Procedure) scopeNamespace() string {
	if p.plan.Operator.Mode == tenancyMulti {
		return ""
	}
	return p.plan.Operator.TargetNamespace
}

// restoreNamespaces are the namespaces whose restore Jobs the migrator moves.
func (p *Procedure) restoreNamespaces() []string {
	var namespaces []string
	for _, restore := range p.plan.Restores {
		if len(restore.Jobs) != 0 {
			namespaces = append(namespaces, restore.Namespace)
		}
	}
	return namespaces
}

// clusterScope includes the planned restore records: their Jobs carry a
// Cluster's labels but move with their record.
func (p *Procedure) clusterScope(cluster ClusterPlan) custodyScope {
	scope := custodyScope{namespaceUID: cluster.NamespaceUID, retained: cluster.Retained}
	for _, restore := range p.plan.Restores {
		if restore.Namespace != cluster.Namespace {
			continue
		}
		scope.foreign = append(scope.foreign, restore.source())
		if restore.DestinationUID != "" {
			scope.foreign = append(scope.foreign, restore.destination())
		}
	}
	return scope
}

// joinDetails joins the non-empty details of a step's parts.
func joinDetails(details ...string) string {
	return strings.Join(slices.DeleteFunc(details, func(detail string) bool { return detail == "" }), "; ")
}

// retainedResources nominates Secrets, claims, and the upgrade ServiceAccount
// with the source's owner-uid provenance and no controller. The lock policy reserves that annotation for
// the operator, so it proves the operator created and retained them.
func retainedResources(ctx context.Context, k *Kube, cluster ClusterPlan) ([]primitives.TransferIdentity, error) {
	var retained []primitives.TransferIdentity
	for _, kind := range []string{"Secret", "PersistentVolumeClaim", "ServiceAccount"} {
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind(kind + "List"))
		if err := k.metadata.List(ctx, list, client.InNamespace(cluster.Namespace)); err != nil {
			return nil, err
		}
		for _, object := range list.Items {
			if kind == "ServiceAccount" && object.Name != cluster.Name+constants.SuffixUpgradeServiceAccount {
				continue
			}
			if object.Annotations[constants.AnnotationOpenBaoOwnerUID] != string(cluster.SourceUID) ||
				metav1.GetControllerOf(&object) != nil {
				continue
			}
			retained = append(retained, primitives.TransferIdentity{APIVersion: "v1", Kind: kind,
				Namespace: object.Namespace, Name: object.Name, UID: object.UID})
		}
	}
	return retained, nil
}

// claimBindings records the retained data claims and their volumes. An
// initialized Cluster has at least one; one paused before it initialized may
// have none.
func claimBindings(ctx context.Context, k *Kube, cluster ClusterPlan, initialized bool) ([]ClaimBinding, error) {
	var claims []ClaimBinding
	for _, retained := range cluster.Retained {
		if retained.Kind != "PersistentVolumeClaim" {
			continue
		}
		claim := &corev1.PersistentVolumeClaim{}
		if err := k.client.Get(ctx, client.ObjectKey{Namespace: retained.Namespace, Name: retained.Name}, claim); err != nil {
			return nil, err
		}
		if claim.Status.Phase != corev1.ClaimBound || claim.Spec.VolumeName == "" {
			return nil, fmt.Errorf("claim %s is not bound", claim.Name)
		}
		volume := &corev1.PersistentVolume{}
		if err := k.client.Get(ctx, client.ObjectKey{Name: claim.Spec.VolumeName}, volume); err != nil {
			return nil, err
		}
		if volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.UID != claim.UID {
			return nil, fmt.Errorf("volume %s is not bound to claim %s", volume.Name, claim.Name)
		}
		claims = append(claims, ClaimBinding{Name: claim.Name, UID: claim.UID, Volume: volume.Name, VolumeID: volume.UID})
	}
	if len(claims) == 0 && initialized {
		return nil, errors.New("no retained data claims found")
	}
	return claims, nil
}

// ---------------------------------------------------------------------------
// shared steps

func (p *Procedure) replicas(stopped bool) map[string]int32 {
	result := map[string]int32{componentController: 0, componentProvisioner: 0}
	if stopped {
		return result
	}
	for _, deployment := range p.plan.Deployments {
		key := componentProvisioner
		if deployment.Controller {
			key = componentController
		}
		result[key] = deployment.Replicas
	}
	return result
}

// migrationValues stop the operator, hold operations, and enable the lock-policy handoff.
func (p *Procedure) migrationValues() map[string]any {
	return chartValues(p.plan.Helm.SourceValues, p.plan.Helm.DestinationOverlay, p.replicas(true), true)
}

func (p *Procedure) writeValues(name string, values map[string]any) (string, error) {
	path := p.state.path("values-" + name + ".json")
	return path, writePrivateJSON(path, values)
}

func (p *Procedure) stopOperators(ctx context.Context) (string, error) {
	namespace := p.plan.Config.OperatorNamespace
	for _, deployment := range p.plan.Deployments {
		if err := p.kube.scaleDeployment(ctx, namespace, deployment, 0); err != nil {
			return "", err
		}
	}
	err := p.kube.wait(ctx, "operator Pods to stop", func(ctx context.Context) (bool, error) {
		for _, deployment := range p.plan.Deployments {
			pods, err := p.kube.deploymentPods(ctx, namespace, deployment.Name)
			if err != nil || len(pods) > 0 {
				return false, err
			}
		}
		return true, nil
	})
	return "operator Deployments scaled to zero", err
}

// ensureChart upgrades the release to the destination chart with values,
// unless the latest revision already has them.
func (p *Procedure) ensureChart(ctx context.Context, name string, values map[string]any) (string, error) {
	latest, err := p.helm.Latest(ctx)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(latest.Status, "pending") {
		return "", fmt.Errorf("release revision %d is %s; resolve it before re-running", latest.Revision, latest.Status)
	}
	if latest.Status == helmDeployed && latest.Revision != p.plan.Helm.SourceRevision {
		current, err := p.helm.Values(ctx, latest.Revision)
		if err != nil {
			return "", err
		}
		if sameValues(current, values) {
			return fmt.Sprintf("revision %d already has the %s values", latest.Revision, name), nil
		}
	}
	path, err := p.writeValues(name, values)
	if err != nil {
		return "", err
	}
	if err := p.helm.Upgrade(ctx, p.plan.Helm.DestinationChart, path); err != nil {
		return "", err
	}
	latest, err = p.helm.Latest(ctx)
	if err != nil {
		return "", err
	}
	if latest.Status != helmDeployed {
		return "", fmt.Errorf("release revision %d is %s", latest.Revision, latest.Status)
	}
	return fmt.Sprintf("upgraded to revision %d with the %s values", latest.Revision, name), nil
}

func (p *Procedure) waitOperators(ctx context.Context) error {
	return p.kube.wait(ctx, "operator Deployments to become ready", func(ctx context.Context) (bool, error) {
		for _, deployment := range p.plan.Deployments {
			ready, err := p.kube.deploymentReady(ctx, p.plan.Config.OperatorNamespace, deployment.Name)
			if err != nil || !ready {
				return false, err
			}
		}
		return true, nil
	})
}

func (p *Procedure) waitClusters(ctx context.Context, apiVersion string) error {
	return p.kube.wait(ctx, apiVersion+" Clusters to become available, or to be observed paused", func(ctx context.Context) (bool, error) {
		for _, cluster := range p.plan.Clusters {
			object, err := p.kube.getCluster(ctx, apiVersion, cluster.Namespace, cluster.Name)
			if err != nil || !clusterReady(object) {
				return false, err
			}
		}
		return true, nil
	})
}

func (p *Procedure) applyCRDs(ctx context.Context) (string, error) {
	files, err := filepath.Glob(filepath.Join(p.plan.Helm.DestinationChart, "crds", "*.yaml"))
	if err != nil || len(files) == 0 {
		return "", errors.New("destination chart has no CRDs")
	}
	var names []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.UnmarshalStrict(data, crd); err != nil {
			return "", fmt.Errorf("decode %s: %w", file, err)
		}
		if crd.Spec.Group != "kubebao.org" {
			return "", fmt.Errorf("%s does not serve kubebao.org", file)
		}
		crd.TypeMeta = metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition"}
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(crd)
		if err != nil {
			return "", err
		}
		apply := client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: object})
		if err := p.kube.client.Apply(ctx, apply, client.FieldOwner(toolFieldManager), client.ForceOwnership); err != nil {
			return "", fmt.Errorf("apply CRD %s: %w", crd.Name, err)
		}
		names = append(names, crd.Name)
	}
	return "applied " + strings.Join(names, ", "), p.kube.wait(ctx, "CRDs to be established", func(ctx context.Context) (bool, error) {
		for _, name := range names {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := p.kube.client.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
				return false, err
			}
			established := false
			for _, condition := range crd.Status.Conditions {
				established = established || (condition.Type == apiextensionsv1.Established &&
					condition.Status == apiextensionsv1.ConditionTrue)
			}
			if !established {
				return false, nil
			}
		}
		return true, nil
	})
}

// forEachCluster runs one convergent action per Cluster and reports a summary.
func (p *Procedure) forEachCluster(run func(*ClusterPlan) (string, error)) (string, error) {
	var details []string
	for i := range p.plan.Clusters {
		cluster := &p.plan.Clusters[i]
		detail, err := run(cluster)
		if err != nil {
			return "", fmt.Errorf("%s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
		details = append(details, cluster.Namespace+"/"+cluster.Name+": "+detail)
	}
	return strings.Join(details, "; "), p.state.Save(p.plan)
}

// sourceIntact checks that a source Cluster still exists with its planned
// identity and specification: the destination was imported from that one.
func (p *Procedure) sourceIntact(ctx context.Context, cluster ClusterPlan) (bool, error) {
	source, err := p.sourceExists(ctx, cluster)
	if err != nil || source == nil {
		return false, err
	}
	if err := validateSourceCluster(source, cluster); err != nil {
		return false, err
	}
	return true, nil
}

func validateSourceCluster(source *unstructured.Unstructured, cluster ClusterPlan) error {
	if err := validateRetirementFinalizers(source); err != nil {
		return err
	}
	if err := validateBackupHelperPin(source); err != nil {
		return err
	}
	if digestJSON(source.Object["spec"]) != cluster.SpecDigest {
		return errors.New("source specification changed since planning, for example by a GitOps " +
			"controller that resumed; the destination holds the planned one. Roll back, then plan again")
	}
	return nil
}

// sourceExists returns the source Cluster if it still exists with its planned
// identity. Rollback accepts a changed specification but separately refuses
// deletion in progress.
func (p *Procedure) sourceExists(ctx context.Context, cluster ClusterPlan) (*unstructured.Unstructured, error) {
	source, err := p.kube.getCluster(ctx, primitives.PlanSourceAPIVersion, cluster.Namespace, cluster.Name)
	if err != nil || source == nil {
		return nil, err
	}
	if source.GetUID() != cluster.SourceUID {
		return nil, fmt.Errorf("source Cluster was replaced since planning")
	}
	return source, nil
}

// destinationIdentity resolves the imported destination, including one created
// by a run that stopped before recording its UID.
func (p *Procedure) destinationIdentity(ctx context.Context, cluster *ClusterPlan) (*unstructured.Unstructured, error) {
	destination, err := p.kube.getCluster(ctx, primitives.PlanDestinationAPIVersion, cluster.Namespace, cluster.Name)
	if err != nil || destination == nil {
		return nil, err
	}
	if destination.GetAnnotations()[primitives.GateSourceUIDAnnotation] != string(cluster.SourceUID) ||
		(cluster.DestinationUID != "" && destination.GetUID() != cluster.DestinationUID) {
		return nil, errors.New("destination Cluster was not created by this migration")
	}
	cluster.DestinationUID = destination.GetUID()
	return destination, nil
}

// verify checks one side after it runs: availability, custody, storage,
// Raft membership, and which controller audiences the JWT role accepts.
// untilPodsSettle runs a discovery until it reports no blockers. A discovery
// is not one snapshot: a rollout that starts after the workloads settled can
// delete or replace a Pod while it runs. While every blocker is such a Pod, it
// waits and discovers again; any other blocker is reported at once. A step
// that mutates after it rechecks each identity at the mutation.
func (p *Procedure) untilPodsSettle(ctx context.Context, cluster ClusterPlan,
	discover func(context.Context) ([]primitives.DiscoveryBlocker, error),
) error {
	var blockers []primitives.DiscoveryBlocker
	err := p.kube.wait(ctx, cluster.Namespace+"/"+cluster.Name+" Pods to stop changing", func(ctx context.Context) (bool, error) {
		var err error
		if blockers, err = discover(ctx); err != nil || len(blockers) == 0 {
			return err == nil, err
		}
		for _, blocker := range blockers {
			changing, err := p.kube.podChanging(ctx, blocker.Resource)
			if err != nil {
				return false, err
			}
			if !changing {
				return false, blockerError(blockers)
			}
		}
		return false, nil
	})
	if errors.Is(err, errWaitTimeout) && len(blockers) != 0 {
		// Pods kept changing until the timeout: report what was found.
		return blockerError(blockers)
	}
	return err
}

// settledCustody discovers custody for verification once Pods stop changing.
func (p *Procedure) settledCustody(ctx context.Context, cluster ClusterPlan, from, to primitives.TransferIdentity,
) (*custody, error) {
	var state *custody
	err := p.untilPodsSettle(ctx, cluster, func(ctx context.Context) ([]primitives.DiscoveryBlocker, error) {
		var err error
		if state, err = discoverCustody(ctx, p.kube, p.clusterScope(cluster), from, to); err != nil {
			return nil, err
		}
		return state.blockers, nil
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

func (p *Procedure) verify(ctx context.Context, apiVersion string) (string, error) {
	if err := p.waitOperators(ctx); err != nil {
		return "", err
	}
	inactive := primitives.PlanSourceAPIVersion
	if apiVersion == primitives.PlanSourceAPIVersion {
		inactive = primitives.PlanDestinationAPIVersion
	}
	if err := p.inventoryMatches(ctx, inactive, true); err != nil {
		return "", err
	}
	if err := p.waitServing(ctx, apiVersion); err != nil {
		return "", err
	}
	// A rollout replaces Pods between the two discovery passes; verify a settled workload.
	if err := p.waitWorkloads(ctx); err != nil {
		return "", err
	}
	clusters, err := p.forEachCluster(func(cluster *ClusterPlan) (string, error) {
		active, other := cluster.source(), cluster.destination()
		if apiVersion == primitives.PlanDestinationAPIVersion {
			active, other = other, active
		}
		otherExists := other.UID != ""
		if otherExists {
			object, err := p.kube.getCluster(ctx, other.APIVersion, cluster.Namespace, cluster.Name)
			if err != nil {
				return "", err
			}
			otherExists = object != nil && object.GetUID() == other.UID
		}
		if otherExists {
			state, err := p.settledCustody(ctx, *cluster, other, active)
			if err != nil {
				return "", err
			}
			if len(state.pending) != 0 {
				return "", fmt.Errorf("%d resources still reference %s", len(state.pending), other.APIVersion)
			}
		} else {
			retired := other.UID
			other.UID = ""
			options := p.clusterScope(*cluster).options(active, other)
			options.RetiredUID = retired
			preview, err := primitives.DiscoverManagedResources(ctx, p.kube.metadata, options)
			if err != nil {
				return "", err
			}
			if len(preview.Blockers) != 0 {
				data, _ := json.Marshal(preview.Blockers)
				return "", fmt.Errorf("managed resources without provable custody: %s", data)
			}
		}
		if err := verifyClaims(ctx, p.kube, *cluster); err != nil {
			return "", err
		}
		object, err := p.kube.getCluster(ctx, apiVersion, cluster.Namespace, cluster.Name)
		if err != nil || object == nil {
			return "", fmt.Errorf("%s Cluster is missing: %w", apiVersion, err)
		}
		state, evidence := "available", "controller login unchanged"
		if cluster.Paused {
			state = "paused"
		}
		if cluster.LoginSkipped != "" {
			// The audience is still the planned one; there was no login to compare.
			if _, err := p.controllerAudienceMatches(*cluster, object); err != nil {
				return "", err
			}
			evidence = "controller audience unchanged (no login at plan: " + cluster.LoginSkipped + ")"
		} else if err := p.verifyLogin(ctx, *cluster, object); err != nil {
			return "", fmt.Errorf("controller login: %w", err)
		}
		if cluster.RaftPeers != nil {
			bao, err := p.openBao(ctx, *cluster)
			if err != nil {
				return "", err
			}
			peers, err := bao.RaftPeers(ctx)
			if err != nil {
				return "", err
			}
			if !slices.Equal(peers, cluster.RaftPeers) {
				return "", fmt.Errorf("raft membership changed: %v, planned %v", peers, cluster.RaftPeers)
			}
			evidence += "; Raft unchanged"
		}
		restarted, err := restartedPods(ctx, p.kube, *cluster)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s; storage and %s; %d data Pod(s) replaced", state, evidence, restarted), nil
	})
	if err != nil {
		return "", err
	}
	restores, err := p.verifyRecords(ctx, p.plan.Restores, apiVersion)
	if err != nil {
		return "", err
	}
	tenants, err := p.verifyRecords(ctx, p.plan.Tenants, apiVersion)
	return joinDetails(clusters, restores, tenants), err
}

// waitWorkloads waits until every Cluster StatefulSet has finished rolling out.
// The destination may roll Pods when its template differs from the source's.
// A paused Cluster's Pods need not all be ready, only those ready at plan.
func (p *Procedure) waitWorkloads(ctx context.Context) error {
	return p.kube.wait(ctx, "StatefulSet rollouts to settle", func(ctx context.Context) (bool, error) {
		for _, cluster := range p.plan.Clusters {
			sets := &appsv1.StatefulSetList{}
			if err := p.kube.client.List(ctx, sets, client.InNamespace(cluster.Namespace),
				client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
				return false, err
			}
			for _, set := range sets.Items {
				if !statefulSetSettled(&set, !cluster.Paused) {
					return false, nil
				}
			}
			if cluster.Paused {
				ready, err := readyPods(ctx, p.kube, cluster)
				if err != nil {
					return false, err
				}
				for _, name := range cluster.ReadyPods {
					if !slices.Contains(ready, name) {
						return false, nil
					}
				}
			}
		}
		return true, nil
	})
}

func statefulSetSettled(set *appsv1.StatefulSet, requireReady bool) bool {
	want := int32(1)
	if set.Spec.Replicas != nil {
		want = *set.Spec.Replicas
	}
	status := set.Status
	if !set.DeletionTimestamp.IsZero() || status.ObservedGeneration != set.Generation ||
		status.CurrentRevision == "" || status.UpdateRevision == "" || status.Replicas != want ||
		(requireReady && status.ReadyReplicas != want) {
		return false
	}
	// BlueGreen uses OnDelete: a reconciled template need not match running
	// Pods. Recreating the StatefulSet preserves both revisions and those Pods.
	if set.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
		return true
	}
	if set.Spec.UpdateStrategy.Type != "" && set.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		return false
	}
	// An idle strategy transition can retain old Pods behind a partition.
	// Only Pods at or above the partition must use the update revision.
	if rolling := set.Spec.UpdateStrategy.RollingUpdate; rolling != nil && rolling.Partition != nil && *rolling.Partition > 0 {
		return status.UpdatedReplicas >= max(want-*rolling.Partition, 0)
	}
	return status.CurrentRevision == status.UpdateRevision && status.UpdatedReplicas == want
}

// workloadSettled waits until each StatefulSet has applied its update strategy.
// OnDelete and partitioned workloads can retain older Pod revisions. A paused
// Cluster's Pods need not be ready.
func workloadSettled(ctx context.Context, k *Kube, cluster ClusterPlan) error {
	return k.wait(ctx, cluster.Namespace+"/"+cluster.Name+" StatefulSet rollouts to settle",
		func(ctx context.Context) (bool, error) {
			sets := &appsv1.StatefulSetList{}
			if err := k.client.List(ctx, sets, client.InNamespace(cluster.Namespace),
				client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
				return false, err
			}
			for i := range sets.Items {
				if !statefulSetSettled(&sets.Items[i], !cluster.Paused) {
					return false, nil
				}
			}
			return true, nil
		})
}

// readyPods lists the names of a Cluster's ready Pods.
func readyPods(ctx context.Context, k *Kube, cluster ClusterPlan) ([]string, error) {
	pods := &corev1.PodList{}
	if err := k.client.List(ctx, pods, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
		return nil, err
	}
	var ready []string
	for i := range pods.Items {
		if podReady(&pods.Items[i]) {
			ready = append(ready, pods.Items[i].Name)
		}
	}
	slices.Sort(ready)
	return ready, nil
}

func verifyClaims(ctx context.Context, k *Kube, cluster ClusterPlan) error {
	for _, planned := range cluster.Claims {
		claim := &corev1.PersistentVolumeClaim{}
		if err := k.client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: planned.Name}, claim); err != nil {
			return err
		}
		if claim.UID != planned.UID || claim.Spec.VolumeName != planned.Volume || claim.Status.Phase != corev1.ClaimBound {
			return fmt.Errorf("claim %s changed identity or binding", planned.Name)
		}
		volume := &corev1.PersistentVolume{}
		if err := k.client.Get(ctx, client.ObjectKey{Name: planned.Volume}, volume); err != nil {
			return err
		}
		if volume.UID != planned.VolumeID || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.UID != planned.UID {
			return fmt.Errorf("volume %s changed identity or binding", planned.Volume)
		}
	}
	return nil
}

func restartedPods(ctx context.Context, k *Kube, cluster ClusterPlan) (int, error) {
	pods := &corev1.PodList{}
	if err := k.client.List(ctx, pods, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{constants.LabelOpenBaoCluster: cluster.Name}); err != nil {
		return 0, err
	}
	replaced := 0
	for _, pod := range pods.Items {
		if uid, ok := cluster.Pods[pod.Name]; ok && uid != pod.UID {
			replaced++
		}
	}
	return replaced, nil
}

// ---------------------------------------------------------------------------
// migrate

func (p *Procedure) Migrate(ctx context.Context) error {
	switch p.plan.Phase {
	case phaseVerified:
		if err := p.validateForwardPlan(); err != nil {
			return err
		}
		p.plan.Progress = Progress{Command: "reverify"}
		return p.step(ctx, "verify", func() (string, error) { return p.verifyMigrated(ctx) })
	case phasePlanned, phaseMigrating:
	default:
		return fmt.Errorf("migrate is not allowed in phase %s", p.plan.Phase)
	}
	if err := p.validateForwardPlan(); err != nil {
		return err
	}
	p.begin(commandMigrate)
	if err := p.setPhase(phaseMigrating); err != nil {
		return err
	}
	steps := []struct {
		name string
		run  func() (string, error)
	}{
		{"preflight", func() (string, error) { return p.preflight(ctx) }},
		{"migrator", func() (string, error) { return p.ensureMigrator(ctx) }},
		{"stop-operators", func() (string, error) { return p.stopOperators(ctx) }},
		{"recheck", func() (string, error) { return p.recheck(ctx) }},
		{"apply-crds", func() (string, error) {
			if p.plan.Operator.Install != installHelm {
				return "the destination installation applies its CRDs", nil
			}
			return p.applyCRDs(ctx)
		}},
		{"install-destination", func() (string, error) {
			if p.plan.Operator.Install != installHelm {
				return p.awaitOperator(ctx, stateStopped)
			}
			return p.ensureChart(ctx, "migration", p.migrationValues())
		}},
		{"open-handoff", func() (string, error) { return p.setHandoff(ctx, true) }},
		{"import", func() (string, error) {
			importRecord := func(r *RecordPlan) (string, error) { return p.importRecord(ctx, r) }
			tenants, err := p.forEachRecord(p.plan.Tenants, importRecord)
			if err != nil {
				return "", err
			}
			clusters, err := p.forEachCluster(func(c *ClusterPlan) (string, error) { return p.importCluster(ctx, c) })
			if err != nil {
				return "", err
			}
			restores, err := p.forEachRecord(p.plan.Restores, importRecord)
			return joinDetails(tenants, clusters, restores), err
		}},
		{"transfer", func() (string, error) {
			clusters, err := p.forEachCluster(func(c *ClusterPlan) (string, error) {
				moved, err := convergeOwnership(ctx, p.kube, p.clusterScope(*c), c.source(), c.destination())
				return fmt.Sprintf("%d resource(s) moved", moved), err
			})
			if err != nil {
				return "", err
			}
			restores, err := p.forEachRecord(p.plan.Restores, func(r *RecordPlan) (string, error) {
				moved, err := convergeOwnership(ctx, p.kube, r.scope(), r.source(), r.destination())
				return fmt.Sprintf("%d Job(s) moved", moved), err
			})
			return joinDetails(clusters, restores), err
		}},
		{"statefulsets", func() (string, error) {
			return p.forEachCluster(func(c *ClusterPlan) (string, error) {
				recreated, err := recreateStatefulSets(ctx, p.kube, p.state, *c, c.destination())
				return fmt.Sprintf("%d StatefulSet(s) recreated", recreated), err
			})
		}},
		{"close-handoff", func() (string, error) { return p.setHandoff(ctx, false) }},
		{"start-destination", func() (string, error) {
			detail, err := p.transition(ctx, stateHeld,
				chartValues(p.plan.Helm.SourceValues, p.plan.Helm.DestinationOverlay, p.replicas(false), true))
			if err != nil {
				return "", err
			}
			if err := p.waitOperators(ctx); err != nil {
				return "", err
			}
			return detail, p.waitServing(ctx, primitives.PlanDestinationAPIVersion)
		}},
		{"verify", func() (string, error) { return p.verifyMigrated(ctx) }},
	}
	if err := p.runSteps(ctx, steps); err != nil {
		return err
	}
	return p.setPhase(phaseVerified)
}

// preflight re-inspects every source while the source release still runs: an
// operation started after plan would keep its lock once the operator stops.
func (p *Procedure) preflight(ctx context.Context) (string, error) {
	replaced, err := p.sourceReplaced(ctx)
	if err != nil {
		return "", err
	}
	if replaced {
		return "source release already replaced by an earlier run", nil
	}
	detail, err := p.inspectSources(ctx)
	if err != nil {
		return "", fmt.Errorf("%w; nothing was changed, plan again or re-run migrate when it is idle", err)
	}
	return detail, nil
}

// recheck repeats the preflight inspection once the source operator has
// stopped and can start nothing more: it catches an operation or a resource
// that appeared between preflight and the shutdown.
func (p *Procedure) recheck(ctx context.Context) (string, error) {
	detail, err := p.inspectSources(ctx)
	if err != nil {
		return "", fmt.Errorf("%w; the source operator is stopped: run rollback to restart it, let it finish, "+
			"and plan again", err)
	}
	return detail, nil
}

// inspectSources requires the source inventory to be exactly the plan, and
// every planned source to be idle and unchanged.
func (p *Procedure) inspectSources(ctx context.Context) (string, error) {
	if err := p.inventoryMatches(ctx, primitives.PlanSourceAPIVersion, false); err != nil {
		return "", err
	}
	operatorNamespace, err := p.kube.namespaceUID(ctx, p.plan.Config.OperatorNamespace)
	if err != nil {
		return "", err
	}
	var records []string
	for _, planned := range [][]RecordPlan{p.plan.Restores, p.plan.Tenants} {
		detail, err := p.forEachRecord(planned, func(r *RecordPlan) (string, error) {
			return "unchanged", p.inspectRecord(ctx, *r)
		})
		if err != nil {
			return "", err
		}
		records = append(records, detail)
	}
	clusters, err := p.forEachCluster(func(c *ClusterPlan) (string, error) {
		if _, err := p.sourceIntact(ctx, *c); err != nil {
			return "", err
		}
		_, err := primitives.InspectSource(ctx, p.kube.client, primitives.ImportRequest{
			Source: primitives.ObjectIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: clusterKind,
				Namespace: c.Namespace, Name: c.Name, UID: c.SourceUID},
			NamespaceUID:      c.NamespaceUID,
			RelatedNamespaces: []primitives.NamespaceIdentity{{Name: p.plan.Config.OperatorNamespace, UID: operatorNamespace}},
		})
		if err == nil {
			err = workloadSettled(ctx, p.kube, *c)
		}
		return "idle", err
	})
	return joinDetails(append([]string{clusters}, records...)...), err
}

// inventoryMatches compares every Cluster, restore record, and Tenant in one
// group with the plan. A source created after planning would stay behind
// without an operator, so the source group must hold exactly the planned
// objects. After the migration the inactive group may hold only planned ones,
// some already retired.
func (p *Procedure) inventoryMatches(ctx context.Context, apiVersion string, retiring bool) error {
	planned := map[string]types.UID{}
	for _, cluster := range p.plan.Clusters {
		uid := cluster.SourceUID
		if apiVersion == primitives.PlanDestinationAPIVersion {
			uid = cluster.DestinationUID
		}
		planned[clusterKind+" "+cluster.Namespace+"/"+cluster.Name] = uid
	}
	for _, record := range slices.Concat(p.plan.Restores, p.plan.Tenants) {
		uid := record.SourceUID
		if apiVersion == primitives.PlanDestinationAPIVersion {
			uid = record.DestinationUID
		}
		planned[record.Kind+" "+record.Namespace+"/"+record.Name] = uid
	}
	found := 0
	for _, set := range []struct{ kind, namespace string }{
		{clusterKind, p.scopeNamespace()}, {restoreKind, p.scopeNamespace()}, {tenantKind, ""},
	} {
		objects, err := p.kube.listObjects(ctx, apiVersion, set.kind, set.namespace)
		if err != nil {
			return err
		}
		for _, object := range objects {
			key := set.kind + " " + object.GetNamespace() + "/" + object.GetName()
			expected, ok := planned[key]
			if !ok {
				return fmt.Errorf("%s %s is not part of this migration; it would be left without an operator",
					apiVersion, key)
			}
			if expected != "" && object.GetUID() != expected {
				return fmt.Errorf("%s %s was replaced since planning (expected UID %s, observed %s); "+
					"suspend the writer and resolve the unexpected object before retrying with the same state", apiVersion, key, expected, object.GetUID())
			}
			found++
		}
	}
	if !retiring && found != len(planned) {
		return fmt.Errorf("%d %s object(s) exist, %d were planned", found, apiVersion, len(planned))
	}
	return nil
}

func (p *Procedure) verifyMigrated(ctx context.Context) (string, error) {
	return p.verify(ctx, primitives.PlanDestinationAPIVersion)
}

func (p *Procedure) importCluster(ctx context.Context, cluster *ClusterPlan) (string, error) {
	destination, err := p.destinationIdentity(ctx, cluster)
	if err != nil {
		return "", err
	}
	if destination != nil && !primitives.ImportStatusPending(destination) {
		if status, _, _ := unstructured.NestedMap(destination.Object, "status"); len(status) > 0 {
			return "already imported", nil
		}
	}
	intact, err := p.sourceIntact(ctx, *cluster)
	if err != nil {
		return "", err
	}
	if !intact {
		return "", errors.New("source Cluster is missing")
	}
	source, err := p.kube.getCluster(ctx, primitives.PlanSourceAPIVersion, cluster.Namespace, cluster.Name)
	if err != nil {
		return "", err
	}
	operatorNamespace, err := p.kube.namespaceUID(ctx, p.plan.Config.OperatorNamespace)
	if err != nil {
		return "", err
	}
	labels, annotations := importKeys(source)
	request := primitives.ImportRequest{
		Source: primitives.ObjectIdentity{APIVersion: primitives.PlanSourceAPIVersion, Kind: clusterKind,
			Namespace: cluster.Namespace, Name: cluster.Name, UID: cluster.SourceUID, ResourceVersion: source.GetResourceVersion()},
		NamespaceUID: cluster.NamespaceUID, MigrationID: p.plan.MigrationID,
		LabelKeys: labels, AnnotationKeys: annotations, ExpectedDestinationUID: cluster.DestinationUID,
		RelatedNamespaces: []primitives.NamespaceIdentity{{Name: p.plan.Config.OperatorNamespace, UID: operatorNamespace}},
	}
	payload, err := primitives.BuildImport(ctx, p.kube.client, request)
	if err != nil {
		return "", err
	}
	request.ExpectedImportDigest = payload.Digest
	result, err := primitives.Import(ctx, p.kube.client, request)
	if result.Destination.UID != "" {
		cluster.DestinationUID = result.Destination.UID
		if saveErr := p.state.Save(p.plan); saveErr != nil {
			return "", errors.Join(err, saveErr)
		}
	}
	if err != nil {
		return "", err
	}
	if !result.StatusImported {
		return "", errors.New("status import was not confirmed")
	}
	return "imported as " + string(cluster.DestinationUID), nil
}

// importKeys selects the source metadata to copy. The acknowledgement
// annotation names the source record's UID; the decision it recorded is in the
// imported status, and admission refuses it on a new record. GitOps tracking
// metadata names the source object: a controller that trusted a copy could
// prune the destination as its own. It tracks the destination once Git names it.
func importKeys(source *unstructured.Unstructured) (labels, annotations []string) {
	for key := range source.GetLabels() {
		if !gitopsTrackingKey(key) {
			labels = append(labels, key)
		}
	}
	for key := range source.GetAnnotations() {
		if key != "kubectl.kubernetes.io/last-applied-configuration" && key != constants.AnnotationRestoreAcknowledge &&
			!strings.HasPrefix(key, "migration.kubebao.org/") && !gitopsTrackingKey(key) {
			annotations = append(annotations, key)
		}
	}
	slices.Sort(labels)
	slices.Sort(annotations)
	return labels, annotations
}

// ---------------------------------------------------------------------------
// commit

func (p *Procedure) Commit(ctx context.Context) error {
	switch p.plan.Phase {
	case phaseComplete:
		_, _ = fmt.Fprintln(p.log, "migration is already complete")
		return nil
	case phaseVerified, phaseCommitting:
	default:
		return fmt.Errorf("commit requires a verified migration, phase is %s", p.plan.Phase)
	}
	if err := p.validateForwardPlan(); err != nil {
		return err
	}
	p.begin(commandCommit)
	if err := p.setPhase(phaseCommitting); err != nil {
		return err
	}
	steps := []struct {
		name string
		run  func() (string, error)
	}{
		{"verify", func() (string, error) { return p.verifyMigrated(ctx) }},
		// Clusters first: rollback requires every source Cluster, so it can never
		// run once a source restore record is gone.
		{"retire-source", func() (string, error) { return p.retireSource(ctx) }},
		{"release-hold", func() (string, error) {
			detail, err := p.transition(ctx, stateNormal,
				chartValues(p.plan.Helm.SourceValues, p.plan.Helm.DestinationOverlay, p.replicas(false), false))
			if err != nil {
				return "", err
			}
			return detail, p.waitOperators(ctx)
		}},
		{"final-verify", func() (string, error) { return p.verify(ctx, primitives.PlanDestinationAPIVersion) }},
		{"remove-migrator", func() (string, error) { return p.removeMigrator(ctx) }},
	}
	if err := p.runSteps(ctx, steps); err != nil {
		return err
	}
	if err := p.writeHandback(ctx, false); err != nil {
		return err
	}
	return p.setPhase(phaseComplete)
}

// ---------------------------------------------------------------------------
// rollback

func (p *Procedure) Rollback(ctx context.Context) error {
	switch p.plan.Phase {
	case phaseRolledBack:
		_, _ = fmt.Fprintln(p.log, "migration is already rolled back")
		return nil
	case phaseComplete:
		return p.rollbackClosedError("rollback is not allowed after commit completed")
	case phasePlanned, phaseMigrating, phaseVerified, phaseCommitting, phaseRollingBack:
	default:
		return fmt.Errorf("rollback is not allowed in phase %s", p.plan.Phase)
	}
	if err := p.rollbackPossible(ctx); err != nil {
		return err
	}
	p.begin(commandRollback)
	if err := p.setPhase(phaseRollingBack); err != nil {
		return err
	}
	chartChanged := false
	if p.plan.Operator.Install == installHelm {
		latest, err := p.helm.Latest(ctx)
		if err != nil {
			return err
		}
		chartChanged = latest.Revision != p.plan.Helm.SourceRevision && latest.Revision != p.plan.Helm.RestoredRevision
	}
	steps := []struct {
		name string
		run  func() (string, error)
	}{
		{"stop-operators", func() (string, error) { return p.stopOperators(ctx) }},
		{"migrator", func() (string, error) { return p.ensureMigrator(ctx) }},
		{"open-handoff", func() (string, error) { return p.setHandoff(ctx, true) }},
		{"finish-statefulsets", func() (string, error) {
			return p.forEachCluster(func(c *ClusterPlan) (string, error) {
				finished, err := finishStatefulSets(ctx, p.kube, p.state, *c)
				return fmt.Sprintf("%d interrupted StatefulSet recreation(s) finished", finished), err
			})
		}},
		{"transfer-back", func() (string, error) { return p.transferBack(ctx) }},
		{"statefulsets-back", func() (string, error) {
			return p.forEachCluster(func(c *ClusterPlan) (string, error) {
				recreated, err := recreateStatefulSets(ctx, p.kube, p.state, *c, c.source())
				return fmt.Sprintf("%d StatefulSet(s) recreated", recreated), err
			})
		}},
		{"close-handoff", func() (string, error) { return p.setHandoff(ctx, false) }},
		{"restore-source", func() (string, error) {
			if p.plan.Operator.Install != installHelm {
				return p.restoreSourceManifests(ctx)
			}
			detail := "source chart restored by scaling"
			if chartChanged {
				if err := p.helm.Rollback(ctx, p.plan.Helm.SourceRevision); err != nil {
					return "", err
				}
				restored, err := p.helm.Latest(ctx)
				if err != nil {
					return "", err
				}
				p.plan.Helm.RestoredRevision = restored.Revision
				if err := p.state.Save(p.plan); err != nil {
					return "", err
				}
				detail = fmt.Sprintf("Helm release rolled back to revision %d as revision %d",
					p.plan.Helm.SourceRevision, restored.Revision)
			} else {
				for _, deployment := range p.plan.Deployments {
					if err := p.kube.scaleDeployment(ctx, p.plan.Config.OperatorNamespace, deployment, deployment.Replicas); err != nil {
						return "", err
					}
				}
			}
			return detail, p.waitServing(ctx, primitives.PlanSourceAPIVersion)
		}},
		{"retire-destination", func() (string, error) { return p.retireDestination(ctx) }},
		{"verify-source", func() (string, error) { return p.verify(ctx, primitives.PlanSourceAPIVersion) }},
		{"remove-migrator", func() (string, error) { return p.removeMigrator(ctx) }},
	}
	if err := p.runSteps(ctx, steps); err != nil {
		return err
	}
	if err := p.writeHandback(ctx, true); err != nil {
		return err
	}
	return p.setPhase(phaseRolledBack)
}

func (p *Procedure) writeHandback(ctx context.Context, rollback bool) error {
	instructions, err := p.writeGitOpsHandback(ctx, rollback)
	if err != nil {
		return err
	}
	if instructions != "" {
		_, _ = fmt.Fprintln(p.log, "\n"+instructions)
	}
	return nil
}

// rollbackPossible requires every source Cluster and record: rollback deletes
// the destination copies.
func (p *Procedure) rollbackPossible(ctx context.Context) error {
	for _, cluster := range p.plan.Clusters {
		source, err := p.sourceExists(ctx, cluster)
		if err != nil {
			return err
		}
		if source == nil {
			return p.rollbackClosedError(fmt.Sprintf("source %s/%s was retired", cluster.Namespace, cluster.Name))
		}
		if source.GetDeletionTimestamp() != nil {
			return p.rollbackClosedError(fmt.Sprintf("source %s/%s is being deleted", cluster.Namespace, cluster.Name))
		}
	}
	for _, record := range slices.Concat(p.plan.Restores, p.plan.Tenants) {
		source, err := p.kube.getObject(ctx, primitives.PlanSourceAPIVersion, record.Kind, record.Namespace, record.Name)
		if err != nil {
			return err
		}
		if source == nil || source.GetUID() != record.SourceUID {
			return p.rollbackClosedError(fmt.Sprintf("source %s is missing or replaced; rolling back would lose its destination copy", record.label()))
		}
		if source.GetDeletionTimestamp() != nil {
			return p.rollbackClosedError(fmt.Sprintf("source %s is being deleted", record.label()))
		}
	}
	return nil
}

func (p *Procedure) rollbackClosedError(reason string) error {
	switch p.plan.Phase {
	case phaseCommitting:
		return fmt.Errorf("%s; rollback is no longer possible. Resume commit with the same state: %s. "+
			"Use backup recovery only if the migration cannot be completed", reason, p.stateCommand(commandCommit))
	case phaseComplete:
		return fmt.Errorf("%s; follow the completed migration hand-back and check status: %s", reason, p.stateCommand(commandStatus))
	default:
		return fmt.Errorf("%s; rollback is no longer possible because a planned source changed before commit. "+
			"Preserve the state and inspect the source, destination, and workload ownership before recovery: %s", reason, p.stateCommand(commandStatus))
	}
}

// retireSource deletes the source Clusters, then the source restore and Tenant
// records, each without cascading once it controls nothing.
func (p *Procedure) retireSource(ctx context.Context) (string, error) {
	if err := p.preflightSourceRetirement(ctx); err != nil {
		return "", err
	}
	clusters, err := p.forEachCluster(func(c *ClusterPlan) (string, error) {
		source, err := p.sourceExists(ctx, *c)
		if err != nil || source == nil {
			return alreadyRetired, err
		}
		// An earlier run may have deleted it and lost the response; the garbage
		// collector then removes it once it has orphaned what it still owns.
		if deleting, err := orphaningDeletion(source); err != nil || deleting {
			if err != nil {
				return "", err
			}
			return "source Cluster deletion finished",
				p.kube.waitDeleted(ctx, primitives.PlanSourceAPIVersion, clusterKind, c.Namespace, c.Name)
		}
		intact, err := p.sourceIntact(ctx, *c)
		if err != nil || !intact {
			return alreadyRetired, err
		}
		// The destination may still be rolling Pods: discover a settled workload.
		state, err := p.settledCustody(ctx, *c, c.source(), c.destination())
		if err != nil {
			return "", err
		}
		if len(state.pending) != 0 {
			return "", fmt.Errorf("%d resources still reference the source", len(state.pending))
		}
		if err := p.kube.removeFinalizerAndDelete(ctx, primitives.PlanSourceAPIVersion, clusterKind, c.Namespace, c.Name,
			c.SourceUID); err != nil {
			return "", err
		}
		return "source Cluster deleted without cascading", afterSourceRetired()
	})
	if err != nil {
		return "", err
	}
	details := []string{clusters}
	for _, records := range [][]RecordPlan{p.plan.Restores, p.plan.Tenants} {
		detail, err := p.forEachRecord(records, func(r *RecordPlan) (string, error) {
			return p.retireRecord(ctx, r, r.source(), r.destination())
		})
		if err != nil {
			return "", err
		}
		details = append(details, detail)
	}
	return joinDetails(details...), nil
}

// preflightSourceRetirement checks the whole remaining batch before its first
// deletion. A later source with a changed specification, an unsafe helper pin,
// or a foreign finalizer must not close the rollback window for an earlier one.
func (p *Procedure) preflightSourceRetirement(ctx context.Context) error {
	sources := make([]primitives.TransferIdentity, 0, len(p.plan.Clusters)+len(p.plan.Restores)+len(p.plan.Tenants))
	for _, cluster := range p.plan.Clusters {
		sources = append(sources, cluster.source())
	}
	for _, record := range slices.Concat(p.plan.Restores, p.plan.Tenants) {
		sources = append(sources, record.source())
	}
	for _, source := range sources {
		object, err := p.kube.getObject(ctx, source.APIVersion, source.Kind, source.Namespace, source.Name)
		if err != nil {
			return err
		}
		if object == nil {
			continue // An earlier commit attempt already retired this source.
		}
		if object.GetUID() != source.UID {
			return fmt.Errorf("source %s %s/%s was replaced", source.Kind, source.Namespace, source.Name)
		}
		if deleting, err := orphaningDeletion(object); err != nil || deleting {
			if err != nil {
				return err
			}
			continue
		}
		if err := validateRetirementFinalizers(object); err != nil {
			return err
		}
		if source.Kind == clusterKind {
			for _, cluster := range p.plan.Clusters {
				if cluster.source() == source {
					if err := validateSourceCluster(object, cluster); err != nil {
						return err
					}
				}
			}
		} else {
			for _, record := range slices.Concat(p.plan.Restores, p.plan.Tenants) {
				if record.source() == source && digestJSON(object.Object["spec"]) != record.SpecDigest {
					return errors.New("source record specification changed since planning")
				}
			}
		}
	}
	return nil
}

// transferBack returns custody of Cluster resources and restore Jobs to the source.
func (p *Procedure) transferBack(ctx context.Context) (string, error) {
	clusters, err := p.forEachCluster(func(c *ClusterPlan) (string, error) {
		destination, err := p.destinationIdentity(ctx, c)
		if err != nil || destination == nil {
			return "no destination Cluster", err
		}
		moved, err := convergeOwnership(ctx, p.kube, p.clusterScope(*c), c.destination(), c.source())
		return fmt.Sprintf("%d resource(s) moved back", moved), err
	})
	if err != nil {
		return "", err
	}
	restores, err := p.forEachRecord(p.plan.Restores, func(r *RecordPlan) (string, error) {
		destination, err := p.recordDestination(ctx, r)
		if err != nil || destination == nil {
			return "no destination record", err
		}
		moved, err := convergeOwnership(ctx, p.kube, r.scope(), r.destination(), r.source())
		return fmt.Sprintf("%d Job(s) moved back", moved), err
	})
	return joinDetails(clusters, restores), err
}

// retireDestination deletes the destination records and Clusters, each
// without cascading once it controls nothing.
func (p *Procedure) retireDestination(ctx context.Context) (string, error) {
	var details []string
	for _, records := range [][]RecordPlan{p.plan.Restores, p.plan.Tenants} {
		detail, err := p.forEachRecord(records, func(r *RecordPlan) (string, error) {
			destination, err := p.recordDestination(ctx, r)
			if err != nil || destination == nil {
				return "no destination record", err
			}
			return p.retireRecord(ctx, r, r.destination(), r.source())
		})
		if err != nil {
			return "", err
		}
		details = append(details, detail)
	}
	clusters, err := p.forEachCluster(func(c *ClusterPlan) (string, error) {
		destination, err := p.destinationIdentity(ctx, c)
		if err != nil || destination == nil {
			return "no destination Cluster", err
		}
		if deleting, err := orphaningDeletion(destination); err != nil || deleting {
			if err != nil {
				return "", err
			}
			return "destination Cluster deletion finished",
				p.kube.waitDeleted(ctx, primitives.PlanDestinationAPIVersion, clusterKind, c.Namespace, c.Name)
		}
		// The restored source operator rolls Pods back to its own template:
		// discover a settled workload.
		state, err := p.settledCustody(ctx, *c, c.destination(), c.source())
		if err != nil {
			return "", err
		}
		if len(state.pending) != 0 {
			return "", fmt.Errorf("%d resources still reference the destination", len(state.pending))
		}
		restamped, err := restampRevisions(ctx, p.kube, c.DestinationUID, c.source())
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("destination Cluster deleted without cascading; %d StatefulSet revision(s) moved to the source",
				restamped),
			p.kube.removeFinalizerAndDelete(ctx, primitives.PlanDestinationAPIVersion, clusterKind, c.Namespace, c.Name, c.DestinationUID)
	})
	return joinDetails(append([]string{clusters}, details...)...), err
}

// sourceReplaced reports whether an earlier run already replaced the source
// operator: a new Helm revision, or the destination's policies.
func (p *Procedure) sourceReplaced(ctx context.Context) (bool, error) {
	if p.plan.Operator.Install == installHelm {
		latest, err := p.helm.Latest(ctx)
		if err != nil {
			return false, err
		}
		return latest.Revision != p.plan.Helm.SourceRevision, nil
	}
	observed, err := p.observeOperator(ctx)
	if err != nil {
		return false, err
	}
	return observed.group != stateSource.group, nil
}

// transition moves the installation to state: a Helm release is upgraded with
// values; a manifest installation stops at a checkpoint until it is applied.
func (p *Procedure) transition(ctx context.Context, state operatorState, values map[string]any) (string, error) {
	if p.plan.Operator.Install != installHelm {
		return p.awaitOperator(ctx, state)
	}
	return p.ensureChart(ctx, state.name, values)
}

// restoreSourceManifests scales the source back if it was never replaced, and
// otherwise waits for the administrator to re-apply it.
func (p *Procedure) restoreSourceManifests(ctx context.Context) (string, error) {
	observed, err := p.observeOperator(ctx)
	if err != nil {
		return "", err
	}
	if observed.group == stateSource.group && !observed.held {
		for _, deployment := range p.plan.Deployments {
			if err := p.kube.scaleDeployment(ctx, p.plan.Config.OperatorNamespace, deployment, deployment.Replicas); err != nil {
				return "", err
			}
		}
		return "source installation restored by scaling", p.waitServing(ctx, primitives.PlanSourceAPIVersion)
	}
	detail, err := p.awaitOperator(ctx, stateSource)
	if err != nil {
		return "", err
	}
	return detail, p.waitServing(ctx, primitives.PlanSourceAPIVersion)
}

// ---------------------------------------------------------------------------
// status

// validateForwardPlan also runs for saved plans before any progress changes.
// Older development binaries did not record the source backup-helper selection.
// They can still be rolled back with this binary, but cannot advance safely.
func (p *Procedure) validateForwardPlan() error {
	if p.plan.Version != planVersion {
		return fmt.Errorf("plan format %d does not record backup-helper selection; roll back and create a new plan with this binary; if commit has already retired a source, finish it with the original binary", p.plan.Version)
	}
	if p.plan.Operator.Install == installHelm {
		return sourceImagePinned(p.plan.Helm.SourceValues, p.plan.Helm.DestinationOverlay)
	}
	return nil
}
