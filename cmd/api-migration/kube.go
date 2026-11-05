package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/cmd/api-migration/primitives"
)

const (
	clusterKind       = "OpenBaoCluster"
	clusterFinalizer  = "openbao.org/openbaocluster-finalizer"
	restoreKind       = "OpenBaoRestore"
	restoreFinalizer  = "openbao.org/openbaorestore-finalizer"
	tenantKind        = "OpenBaoTenant"
	tenantFinalizer   = "openbao.org/openbaotenant-finalizer"
	statefulSetKind   = "StatefulSet"
	tenancyMulti      = "multi"
	toolFieldManager  = "kubebao-api-migration"
	defaultWaitPeriod = 2 * time.Second
)

// Kube holds direct (uncached) clients. Handoff writes use the impersonated
// migrator, which the chart's lock-policy exception names; all other calls use
// the administrator's identity.
type Kube struct {
	client    client.Client
	migrator  client.Client
	handoff   client.Client
	metadata  client.Reader
	clientset kubernetes.Interface
	timeout   time.Duration
}

// handoffClient reads as the administrator and writes as the migrator, so the
// migrator needs only the write grants that the lock-policy exception covers.
type handoffClient struct {
	client.Client
	writer client.Client
}

func (h handoffClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return h.writer.Create(ctx, obj, opts...)
}

func (h handoffClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return h.writer.Update(ctx, obj, opts...)
}

func (h handoffClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return h.writer.Patch(ctx, obj, patch, opts...)
}

func (h handoffClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return h.writer.Delete(ctx, obj, opts...)
}

func newKube(kubeconfig, kubeContext, migrator string, timeout time.Duration) (*Kube, error) {
	raw, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if raw.Contexts[kubeContext] == nil {
		return nil, fmt.Errorf("context %q is absent from the kubeconfig", kubeContext)
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*raw, kubeContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("configure context %q: %w", kubeContext, err)
	}
	config.Timeout = 30 * time.Second
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	admin, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	impersonated := rest.CopyConfig(config)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: migrator}
	handoff, err := client.New(impersonated, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	metadata, err := primitives.NewDiscoveryReader(config)
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	admin, handoff = wrapWriter(admin), wrapWriter(handoff)
	return &Kube{client: admin, migrator: handoff, handoff: handoffClient{Client: admin, writer: handoff},
		metadata: metadata, clientset: clientset, timeout: timeout}, nil
}

// errWaitTimeout marks a wait that ended because its timeout expired.
var errWaitTimeout = errors.New("timed out")

func (k *Kube) wait(ctx context.Context, what string, done func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	for {
		ok, err := done(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w waiting for %s", errWaitTimeout, what)
		case <-time.After(defaultWaitPeriod):
		}
	}
}

func (k *Kube) namespaceUID(ctx context.Context, name string) (types.UID, error) {
	namespace := &corev1.Namespace{}
	if err := k.client.Get(ctx, client.ObjectKey{Name: name}, namespace); err != nil {
		return "", err
	}
	if !namespace.DeletionTimestamp.IsZero() {
		return "", fmt.Errorf("namespace %s is being deleted", name)
	}
	return namespace.UID, nil
}

// getCluster reads an OpenBaoCluster from either group. A missing object or a
// missing destination CRD returns nil without an error.
func (k *Kube) getCluster(ctx context.Context, apiVersion, namespace, name string) (*unstructured.Unstructured, error) {
	return k.getObject(ctx, apiVersion, clusterKind, namespace, name)
}

// getObject returns nil when the object, or its API group, does not exist.
func (k *Kube) getObject(ctx context.Context, apiVersion, kind, namespace, name string) (*unstructured.Unstructured, error) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(schema.FromAPIVersionAndKind(apiVersion, kind))
	err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, object)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return object, nil
}

func (k *Kube) listClusters(ctx context.Context, apiVersion, namespace string) ([]unstructured.Unstructured, error) {
	return k.listObjects(ctx, apiVersion, clusterKind, namespace)
}

func (k *Kube) listObjects(ctx context.Context, apiVersion, kind, namespace string) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.FromAPIVersionAndKind(apiVersion, kind+"List"))
	err := k.client.List(ctx, list, client.InNamespace(namespace))
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	return list.Items, err
}

// clusterReady requires a reconciled Cluster at its current generation: an
// available one, or a paused one whose operator has observed the pause.
func clusterReady(object *unstructured.Unstructured) bool {
	if object == nil || !object.GetDeletionTimestamp().IsZero() {
		return false
	}
	finalized := false
	for _, finalizer := range object.GetFinalizers() {
		finalized = finalized || finalizer == clusterFinalizer
	}
	generation := object.GetGeneration()
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	observed, _, _ := unstructured.NestedInt64(object.Object, "status", "observedGeneration")
	ready, _, _ := unstructured.NestedInt64(object.Object, "status", "readyReplicas")
	replicas, _, _ := unstructured.NestedInt64(object.Object, "spec", "replicas")
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	available, paused := false, false
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		conditionGeneration, _, _ := unstructured.NestedInt64(condition, "observedGeneration")
		if condition["type"] != "Available" || conditionGeneration != generation {
			continue
		}
		available = available || condition["status"] == "True"
		paused = paused || condition["reason"] == primitives.PausedReason
	}
	if spec, _, _ := unstructured.NestedBool(object.Object, "spec", "paused"); spec {
		return finalized && observed == generation && paused
	}
	readWant, _, _ := unstructured.NestedInt64(object.Object, "spec", "readReplicas", "replicas")
	readReady, _, _ := unstructured.NestedInt64(object.Object, "status", "readReplicas", "readyReplicas")
	readRegistered, _, _ := unstructured.NestedInt64(object.Object, "status", "readReplicas", "registeredReplicas")
	readPool := readWant == 0 || (readReady == readWant && readRegistered == readWant)
	return finalized && phase == "Running" && observed == generation && ready == replicas && replicas > 0 &&
		available && readPool
}

// scaleDeployment changes replicas through a conditional patch. Helm's later
// upgrade or rollback forces its own value back (see Helm.Upgrade).
func (k *Kube) scaleDeployment(ctx context.Context, namespace string, deployment DeploymentPlan, replicas int32) error {
	current := &appsv1.Deployment{}
	if err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: deployment.Name}, current); err != nil {
		return err
	}
	if current.UID != deployment.UID {
		return fmt.Errorf("deployment %s was replaced since planning", deployment.Name)
	}
	if current.Spec.Replicas != nil && *current.Spec.Replicas == replicas {
		return nil
	}
	patch, _ := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(deployment.UID)},
		{"op": "replace", "path": "/spec/replicas", "value": replicas},
	})
	return k.client.Patch(ctx, current, client.RawPatch(types.JSONPatchType, patch), client.FieldOwner(toolFieldManager))
}

func (k *Kube) deploymentPods(ctx context.Context, namespace, name string) ([]corev1.Pod, error) {
	deployment := &appsv1.Deployment{}
	if err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, deployment); err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods := &corev1.PodList{}
	if err := k.client.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func (k *Kube) deploymentReady(ctx context.Context, namespace, name string) (bool, error) {
	deployment := &appsv1.Deployment{}
	if err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, deployment); err != nil {
		return false, err
	}
	want := int32(1)
	if deployment.Spec.Replicas != nil {
		want = *deployment.Spec.Replicas
	}
	return deployment.Status.ObservedGeneration == deployment.Generation && deployment.Status.ReadyReplicas == want &&
		deployment.Status.UpdatedReplicas == want && deployment.Status.Replicas == want, nil
}

// runningPod returns the single ready Pod of a Deployment.
func (k *Kube) runningPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	pods, err := k.deploymentPods(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	var ready []corev1.Pod
	for _, pod := range pods {
		if !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = append(ready, pod)
			}
		}
	}
	if len(ready) != 1 {
		return nil, fmt.Errorf("expected one ready %s Pod, found %d", name, len(ready))
	}
	return &ready[0], nil
}

// podChanging reports whether resource is a Pod that is being deleted or no
// longer exists with its reported UID.
func (k *Kube) podChanging(ctx context.Context, resource primitives.TransferIdentity) (bool, error) {
	if resource.APIVersion != "v1" || resource.Kind != "Pod" {
		return false, nil
	}
	pod := &metav1.PartialObjectMetadata{}
	pod.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Pod"))
	err := k.metadata.Get(ctx, client.ObjectKey{Namespace: resource.Namespace, Name: resource.Name}, pod)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return pod.UID != resource.UID || pod.DeletionTimestamp != nil, nil
}

// validateRetirementFinalizers refuses finalizers the migration cannot retire.
func validateRetirementFinalizers(object *unstructured.Unstructured) error {
	known := map[string]string{clusterKind: clusterFinalizer, restoreKind: restoreFinalizer, tenantKind: tenantFinalizer}[object.GetKind()]
	if known == "" {
		return fmt.Errorf("unsupported retirement kind %s", object.GetKind())
	}
	for _, finalizer := range object.GetFinalizers() {
		if finalizer != known {
			return fmt.Errorf("%s %s/%s has unknown finalizer %q", object.GetAPIVersion(),
				object.GetNamespace(), object.GetName(), finalizer)
		}
	}
	return nil
}

// removeFinalizerAndDelete retires one source or destination without cascading.
// Only the operator's own finalizer may be removed; anything else needs review.
func (k *Kube) removeFinalizerAndDelete(ctx context.Context, apiVersion, kind, namespace, name string, uid types.UID) error {
	object, err := k.getObject(ctx, apiVersion, kind, namespace, name)
	if err != nil || object == nil {
		return err
	}
	if object.GetUID() != uid {
		return fmt.Errorf("%s %s/%s was replaced; refusing to delete", apiVersion, namespace, name)
	}
	if err := validateRetirementFinalizers(object); err != nil {
		return err
	}
	if len(object.GetFinalizers()) > 0 {
		patch, _ := json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": string(uid)},
			{"op": "test", "path": "/metadata/resourceVersion", "value": object.GetResourceVersion()},
			{"op": "remove", "path": "/metadata/finalizers"},
		})
		if err := k.client.Patch(ctx, object, client.RawPatch(types.JSONPatchType, patch)); err != nil {
			return fmt.Errorf("remove %s finalizer: %w", apiVersion, err)
		}
	}
	orphan := metav1.DeletePropagationOrphan
	revision := object.GetResourceVersion()
	err = k.client.Delete(ctx, object, client.Preconditions{UID: &uid, ResourceVersion: &revision},
		client.PropagationPolicy(orphan))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete %s %s/%s: %w", apiVersion, namespace, name, err)
	}
	return k.waitDeleted(ctx, apiVersion, kind, namespace, name)
}

// orphaningDeletion reports whether an object is being deleted after its own
// finalizer was removed, so only the garbage collector's orphan finalizer can
// remain: removeFinalizerAndDelete's deletion was accepted, even if its
// response was lost. Any other deletion in progress is an error.
func orphaningDeletion(object *unstructured.Unstructured) (bool, error) {
	if object.GetDeletionTimestamp() == nil {
		return false, nil
	}
	for _, finalizer := range object.GetFinalizers() {
		if finalizer != metav1.FinalizerOrphanDependents {
			return false, fmt.Errorf("%s %s/%s is being deleted with finalizer %q; inspect it before re-running",
				object.GetKind(), object.GetNamespace(), object.GetName(), finalizer)
		}
	}
	return true, nil
}

// waitDeleted waits until an object no longer exists.
func (k *Kube) waitDeleted(ctx context.Context, apiVersion, kind, namespace, name string) error {
	return k.wait(ctx, "retired "+kind+" deletion", func(ctx context.Context) (bool, error) {
		object, err := k.getObject(ctx, apiVersion, kind, namespace, name)
		return object == nil, err
	})
}

var errStop = errors.New("stopped after the requested step")
