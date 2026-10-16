//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/kubebao/openbao-operator/internal/platform/observability"
	"github.com/kubebao/openbao-operator/internal/platform/resourceapply"
	"github.com/kubebao/openbao-operator/internal/platform/resourceownership"
)

func TestResourceApplyRequestCounts(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	actor := newControllerClient(t)
	config := rest.CopyConfig(cfg)
	config.Wrap(observability.WrapKubernetesTransport)
	c := newPrivilegedImpersonatedClientForConfig(t, config, k8sScheme, controllerUsername)
	var err error
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "apply-request-owner", Namespace: "default"}}
	require.NoError(t, actor.Create(ctx, owner))
	t.Cleanup(func() { _ = actor.Delete(ctx, owner) })
	for _, retained := range []bool{false, true} {
		name := "owned-request-test"
		if retained {
			name = "retained-request-test"
		}
		t.Run(name, func(t *testing.T) {
			desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string]string{"value": "desired"}}
			t.Cleanup(func() { _ = actor.Delete(ctx, desired) })
			for _, phase := range []string{"create", "unchanged", "drift"} {
				t.Run(phase, func(t *testing.T) {
					if phase == "drift" {
						live := &corev1.ConfigMap{}
						require.NoError(t, actor.Get(ctx, client.ObjectKeyFromObject(desired), live))
						live.Data["value"] = "drifted"
						require.NoError(t, actor.Update(ctx, live))
					}
					before := kubeRequestCounts(t)
					if retained {
						err = resourceapply.ApplyRetained(ctx, c, owner, desired.DeepCopy())
					} else {
						err = resourceapply.ApplyOwned(ctx, c, k8sScheme, owner, desired.DeepCopy())
					}
					require.NoError(t, err)
					after := kubeRequestCounts(t)
					counts := map[string]float64{}
					for verb, value := range after {
						if delta := value - before[verb]; delta != 0 {
							counts[verb] = delta
						}
					}
					t.Logf("requests: %v", counts)
					expected := map[string]float64{"get": 1, "apply": 1}
					if phase == "create" {
						expected = map[string]float64{"get": 1, "create": 1, "patch": 1}
					}
					require.Equal(t, expected, counts)
					live := &corev1.ConfigMap{}
					require.NoError(t, actor.Get(ctx, client.ObjectKeyFromObject(desired), live))
					require.True(t, resourceownership.HasOwnerProof(live, owner))
					require.Equal(t, desired.Data, live.Data)
					if retained {
						require.Empty(t, live.OwnerReferences)
					}
				})
			}
		})
	}
}

func kubeRequestCounts(t *testing.T) map[string]float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	counts := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "openbao_kube_client_requests_total" {
			continue
		}
		for _, metric := range family.Metric {
			var verb, resource string
			for _, label := range metric.Label {
				if label.GetName() == "verb" {
					verb = label.GetValue()
				}
				if label.GetName() == "resource" {
					resource = label.GetValue()
				}
			}
			if resource == "configmaps" {
				counts[verb] += metric.GetCounter().GetValue()
			}
		}
	}
	return counts
}

// Strip the persisted proof and response proof after Apply to exercise the repair
// fallback. The separate client keeps this injected mutation out of request counts.
func TestResourceApplyRepairsMissingResponseProof(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	actor := newControllerClient(t)
	config := rest.CopyConfig(cfg)
	config.Wrap(observability.WrapKubernetesTransport)
	c := newPrivilegedImpersonatedClientForConfig(t, config, k8sScheme, controllerUsername)
	var err error
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "repair-request-owner", Namespace: "default"}}
	require.NoError(t, actor.Create(ctx, owner))
	t.Cleanup(func() { _ = actor.Delete(ctx, owner) })
	for _, retained := range []bool{false, true} {
		name := "owned-repair-test"
		if retained {
			name = "retained-repair-test"
		}
		t.Run(name, func(t *testing.T) {
			desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string]string{"value": "desired"}}
			t.Cleanup(func() { _ = actor.Delete(ctx, desired) })
			if retained {
				require.NoError(t, resourceapply.ApplyRetained(ctx, actor, owner, desired.DeepCopy()))
			} else {
				require.NoError(t, resourceapply.ApplyOwned(ctx, actor, k8sScheme, owner, desired.DeepCopy()))
			}
			wrapped := &applyResponseClient{Client: c, afterApply: func(response client.Object) {
				live := &corev1.ConfigMap{}
				require.NoError(t, actor.Get(ctx, client.ObjectKeyFromObject(desired), live))
				before := live.DeepCopy()
				live.SetAnnotations(nil)
				live.SetOwnerReferences(nil)
				require.NoError(t, actor.Patch(ctx, live, client.MergeFrom(before)))
				response.SetAnnotations(nil)
				response.SetOwnerReferences(nil)
			}}
			before := kubeRequestCounts(t)
			if retained {
				err = resourceapply.ApplyRetained(ctx, wrapped, owner, desired)
			} else {
				err = resourceapply.ApplyOwned(ctx, wrapped, k8sScheme, owner, desired)
			}
			require.NoError(t, err)
			after := kubeRequestCounts(t)
			require.Equal(t, float64(2), after["get"]-before["get"])
			require.Equal(t, float64(1), after["apply"]-before["apply"])
			require.Equal(t, float64(1), after["patch"]-before["patch"])
			live := &corev1.ConfigMap{}
			require.NoError(t, actor.Get(ctx, client.ObjectKeyFromObject(desired), live))
			require.True(t, resourceownership.HasOwnerProof(live, owner))
			require.Equal(t, desired.Data, live.Data)
			if retained {
				require.Empty(t, live.OwnerReferences)
			}
		})
	}
}

type applyResponseClient struct {
	client.Client
	afterApply func(client.Object)
}

func (c *applyResponseClient) Apply(ctx context.Context, config runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	if err := c.Client.Apply(ctx, config, opts...); err != nil {
		return err
	}
	response, ok := config.(client.Object)
	if !ok {
		return fmt.Errorf("apply configuration %T does not expose its response", config)
	}
	c.afterApply(response)
	return nil
}
