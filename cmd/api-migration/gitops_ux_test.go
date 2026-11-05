package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGitOpsFreezeReportsActiveParentsAndChildrenTogether(t *testing.T) {
	child := gitopsObject(fluxHelmRelease, "flux-system", "operator", map[string]any{
		"spec": map[string]any{"suspend": false}})
	child.SetLabels(map[string]string{fluxKustomizeName: "platform", fluxKustomizeNamespace: "flux-system"})
	parent := gitopsObject(fluxKustomization, "flux-system", "platform", map[string]any{
		"spec": map[string]any{"suspend": false}})
	p := gitopsProcedure(t, child, parent)
	p.plan.GitOps = []GitOpsManager{{Kind: fluxHelmRelease.Kind, Namespace: "flux-system", Name: "operator",
		Manages: []string{"the operator"}}}
	err := p.checkGitOps(t.Context())
	require.Error(t, err)
	text := err.Error()
	require.Contains(t, text, "Kustomization flux-system/platform is not suspended")
	require.Contains(t, text, "HelmRelease flux-system/operator is not suspended")
	require.Contains(t, text, "manages HelmRelease flux-system/operator")
	require.Less(t, strings.Index(text, "Kustomization flux-system/platform"),
		strings.Index(text, "HelmRelease flux-system/operator:"))

	// The report only observes the managers. A later step still fails until
	// both are suspended, and detects a parent resumed after an earlier step.
	for _, object := range []client.Object{parent, child} {
		live := object.DeepCopyObject().(client.Object)
		require.NoError(t, p.kube.client.Get(t.Context(), client.ObjectKeyFromObject(object), live))
		require.Equal(t, object.GetLabels(), live.GetLabels())
		require.Equal(t, false, live.(*unstructured.Unstructured).Object["spec"].(map[string]any)["suspend"])
	}
	child.Object["spec"] = map[string]any{"suspend": true}
	require.NoError(t, p.kube.client.Update(t.Context(), child))
	require.ErrorContains(t, p.checkGitOps(t.Context()), "Kustomization flux-system/platform is not suspended")
	parent.Object["spec"] = map[string]any{"suspend": true}
	require.NoError(t, p.kube.client.Update(t.Context(), parent))
	require.NoError(t, p.checkGitOps(t.Context()))
	parent.Object["spec"] = map[string]any{"suspend": false}
	require.NoError(t, p.kube.client.Update(t.Context(), parent))
	require.ErrorContains(t, p.checkGitOps(t.Context()), "Kustomization flux-system/platform is not suspended")
}

func TestGitOpsFreezeIncludesApplicationSetAndItsParent(t *testing.T) {
	application := gitopsObject(argoApplication, "argocd", "shared", map[string]any{
		"spec": map[string]any{"source": map[string]any{"repoURL": "do-not-print-source-details"},
			"syncPolicy": map[string]any{"automated": map[string]any{}}}})
	application.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: argoApplicationSet.GroupVersion().String(),
		Kind: argoApplicationSet.Kind, Name: "platform", UID: "set"}})
	set := gitopsObject(argoApplicationSet, "argocd", "platform", map[string]any{"spec": map[string]any{}})
	set.SetLabels(map[string]string{fluxKustomizeName: "argo-config", fluxKustomizeNamespace: "flux-system"})
	parent := gitopsObject(fluxKustomization, "flux-system", "argo-config", map[string]any{
		"spec": map[string]any{"suspend": false}})
	p := gitopsProcedure(t, application, set, parent)
	p.plan.GitOps = []GitOpsManager{{Kind: argoApplication.Kind, Namespace: "argocd", Name: "shared",
		Manages: []string{"the operator", "OpenBaoClusters"}}}
	err := p.checkGitOps(t.Context())
	require.Error(t, err)
	for _, expected := range []string{"Kustomization flux-system/argo-config is not suspended",
		"ApplicationSet argocd/platform would restore the sync policy", "Application argocd/shared syncs automatically"} {
		require.Contains(t, err.Error(), expected)
	}
	require.Less(t, strings.Index(err.Error(), "Kustomization flux-system/argo-config:"),
		strings.Index(err.Error(), "ApplicationSet argocd/platform:"))
	require.Less(t, strings.Index(err.Error(), "ApplicationSet argocd/platform:"),
		strings.Index(err.Error(), "Application argocd/shared:"))
	require.NotContains(t, err.Error(), "do-not-print-source-details")
}

func TestGitOpsFreezeRemoteConfirmationAndNestingRemainFailClosed(t *testing.T) {
	p := gitopsProcedure(t)
	p.plan.GitOps = []GitOpsManager{{Kind: argoApplication.Kind, Name: "remote", Manages: []string{"the operator"}}}
	require.ErrorContains(t, p.checkGitOps(t.Context()), "unknown: controller is not visible")
	p.plan.Config.ExternalGitOpsSuspended = true
	require.NoError(t, p.checkGitOps(t.Context()))
	require.Contains(t, p.gitopsFreezeReport(t.Context()).String(), "administrator confirmed external suspension")

	objects := make([]client.Object, 0, 6)
	for i, name := range []string{"a", "b", "c", "d", "e", "f"} {
		object := gitopsObject(fluxKustomization, "flux-system", name,
			map[string]any{"spec": map[string]any{"suspend": true}})
		if i < 5 {
			object.SetLabels(map[string]string{fluxKustomizeName: string(rune('b' + i)),
				fluxKustomizeNamespace: "flux-system"})
		}
		objects = append(objects, object)
	}
	p = gitopsProcedure(t, objects...)
	p.plan.GitOps = []GitOpsManager{{Kind: fluxKustomization.Kind, Namespace: "flux-system", Name: "a"}}
	require.ErrorContains(t, p.checkGitOps(t.Context()), "too many nested GitOps objects")
}

func checkpointUXProcedure(t *testing.T, kind string) *Procedure {
	t.Helper()
	return &Procedure{state: &State{dir: t.TempDir()}, plan: &Plan{
		Config: Config{Kubeconfig: "/private/kube config", Context: "migration context", OperatorNamespace: "operator-system",
			SourceVersion: "0.6.1"}, DestinationVersion: "0.7.0",
		Operator: OperatorPlan{Install: installHelm, Flavour: flavourChart, Version: "0.6.1"},
		Deployments: []DeploymentPlan{{Name: "operator-controller", Controller: true, Replicas: 1},
			{Name: "operator-provisioner", Replicas: 1}},
		GitOps: []GitOpsManager{{Kind: kind, Namespace: "gitops", Name: "shared",
			Manages: []string{"the operator", "OpenBaoClusters"}}}}}
}

func TestGitOpsCheckpointSharedArgoNormalSelectsOperatorDeployments(t *testing.T) {
	p := checkpointUXProcedure(t, argoApplication.Kind)
	path, err := p.writeCheckpoint(stateNormal)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	require.Contains(t, text, "source CRs have already been retired")
	require.Contains(t, text, "argocd app sync shared --app-namespace gitops "+
		"--resource apps:Deployment:operator-system/operator-controller "+
		"--resource apps:Deployment:operator-system/operator-provisioner")
	require.Contains(t, text, "api-migration commit --state ")
	require.NotContains(t, text, "--prune\n")
	require.NotContains(t, text, "run a manual sync")

	path, err = p.writeCheckpoint(stateHeld)
	require.NoError(t, err)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "do not sync the whole Application")
	require.Contains(t, string(data), "Earlier checkpoints must not create them from Git")
	require.NotContains(t, string(data), "argocd app sync ")
}

func TestGitOpsCheckpointKustomizeRequiresOperatorOnlyInput(t *testing.T) {
	for _, state := range []operatorState{stateStopped, stateHeld, stateNormal, stateSource} {
		t.Run(state.name, func(t *testing.T) {
			p := checkpointUXProcedure(t, fluxKustomization.Kind)
			p.plan.Operator.Flavour = flavourKustomize
			p.plan.Config.DestinationManifests = filepath.Join(p.state.dir, "operator manifests")
			p.plan.Config.SourceManifests = p.plan.Config.DestinationManifests
			require.NoError(t, os.MkdirAll(p.plan.Config.DestinationManifests, 0o700))
			path, err := p.writeCheckpoint(state)
			require.NoError(t, err)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			text := string(data)
			require.Contains(t, text, "shared Kustomization path that includes OpenBao CRs is not an operator checkpoint")
			require.Contains(t, text, "must contain only the operator installation")
			require.Contains(t, text, "kubectl --kubeconfig '/private/kube config' --context 'migration context' apply --server-side --force-conflicts -k ")
			require.Contains(t, text, "replica fields")
			require.Contains(t, text, "Do not reconcile or resume them")
		})
	}
}

func TestGitOpsHandbackStagesSuspensionAndRetainsBothOutcomes(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			p := checkpointUXProcedure(t, fluxHelmRelease.Kind)
			object := gitopsObject(fluxHelmRelease, "gitops", "shared", map[string]any{
				"spec": map[string]any{"suspend": true}})
			p.kube = gitopsProcedure(t, object).kube
			p.plan.Helm.SourceRevision = 3
			p.plan.Helm.SourceValues = map[string]any{"helperImages": map[string]any{"backup": "backup:0.6.1"}}
			p.plan.Helm.DestinationOverlay = map[string]any{"helperImages": map[string]any{"backup": "backup:0.7.0"}}
			text, err := p.writeGitOpsHandback(t.Context(), rollback)
			require.NoError(t, err)
			for _, expected := range []string{"spec.suspend=true in Git", "spec.suspend:false",
				"current observedGeneration", "intended artifact.revision", "Restore Deployment operator-system/operator-controller to 1",
				"Resume a child only after its own source artifact matches"} {
				require.Contains(t, text, expected)
			}
			name, group, helper := "after-commit.md", "kubebao.org/v1alpha1", "backup:0.7.0"
			if rollback {
				name, group, helper = "after-rollback.md", "openbao.org/v1alpha1", "backup:0.6.1"
				require.Contains(t, text, "source Helm revision is 3")
			} else {
				require.Contains(t, text, "Source objects have been retired")
			}
			require.Contains(t, text, group)
			require.Contains(t, text, helper)
			data, err := os.ReadFile(p.state.path(name))
			require.NoError(t, err)
			require.Contains(t, text, string(data))
			info, err := os.Stat(p.state.path(name))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		})
	}
}
