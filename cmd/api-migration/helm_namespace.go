package main

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resolveHelmNamespace keeps Helm storage separate from the operator namespace.
// A local Flux HelmRelease provides the storage namespace without changing its
// configuration. A remote or missing manager requires the administrator to name it.
func (p *Procedure) resolveHelmNamespace(ctx context.Context, controller *appsv1.Deployment) error {
	config := &p.plan.Config
	p.helm.Namespace, p.helm.TargetNamespace = config.helmNamespace(), config.OperatorNamespace
	if name := controller.Annotations["meta.helm.sh/release-name"]; name != "" && name != config.Release {
		return fmt.Errorf("the controller belongs to Helm release %q, not configured release %q", name, config.Release)
	}
	labels := controller.GetLabels()
	name, namespace := labels[fluxHelmName], labels[fluxHelmNamespace]
	if name == "" {
		return nil
	}
	if namespace == "" {
		return fmt.Errorf("the controller names Flux HelmRelease %q without its namespace; set complete tracking metadata before planning", name)
	}
	release := &unstructured.Unstructured{}
	release.SetGroupVersionKind(fluxHelmRelease)
	if err := p.kube.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, release); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			if config.ReleaseNamespace != "" {
				return nil // GitOps suspension is checked separately.
			}
			return fmt.Errorf("cannot discover storage for Flux HelmRelease %s/%s; set releaseNamespace to its existing Helm storage namespace", namespace, name)
		}
		return fmt.Errorf("read Flux HelmRelease %s/%s: %w", namespace, name, err)
	}
	target, _, _ := unstructured.NestedString(release.Object, "spec", "targetNamespace")
	if target == "" {
		target = namespace
	}
	if target != config.OperatorNamespace {
		return fmt.Errorf("tracked Flux HelmRelease %s/%s targets namespace %q, not operatorNamespace %q", namespace, name, target, config.OperatorNamespace)
	}
	if actual, _, _ := unstructured.NestedString(release.Object, "spec", "releaseName"); actual != "" && actual != config.Release {
		return fmt.Errorf("tracked Flux HelmRelease %s/%s names release %q, not configured release %q", namespace, name, actual, config.Release)
	}
	storage, _, _ := unstructured.NestedString(release.Object, "spec", "storageNamespace")
	if storage == "" {
		storage = namespace
	}
	if active, _, _ := unstructured.NestedString(release.Object, "status", "storageNamespace"); active != "" && active != storage {
		return fmt.Errorf("tracked Flux HelmRelease %s/%s is changing storage namespace from %q to %q; settle the source installation before planning", namespace, name, active, storage)
	}
	if config.ReleaseNamespace != "" && config.ReleaseNamespace != storage {
		return fmt.Errorf("releaseNamespace %q differs from Flux HelmRelease %s/%s storage namespace %q; use its existing namespace without changing the HelmRelease", config.ReleaseNamespace, namespace, name, storage)
	}
	config.ReleaseNamespace, p.helm.Namespace = storage, storage
	return nil
}
