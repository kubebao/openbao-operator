package restore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
)

const existingTarget = "preexisting"
const existingDataPVC = "preexisting PVC"

func TestFreshTargetServiceIPFamilies(t *testing.T) {
	for _, policy := range []corev1.IPFamilyPolicy{"", corev1.IPFamilyPolicySingleStack, corev1.IPFamilyPolicyRequireDualStack} {
		t.Run(string(policy), func(t *testing.T) {
			_, request := freshTargetFixture(t)
			if policy != "" {
				families := []corev1.IPFamily{corev1.IPv6Protocol}
				if policy == corev1.IPFamilyPolicyRequireDualStack {
					families = append(families, corev1.IPv4Protocol)
				}
				request.Spec.ClusterTemplate.Service = &api.RestoreTargetServiceConfig{
					IPFamilyPolicy: ptr.To(policy), IPFamilies: families,
				}
			}
			target := projectRestoreTarget(request)
			if policy == "" {
				require.Nil(t, target.Spec.Service, "omission must preserve the internal-only target profile")
				return
			}
			template := request.Spec.ClusterTemplate.Service
			require.Equal(t, template.IPFamilyPolicy, target.Spec.Service.IPFamilyPolicy)
			require.Equal(t, template.IPFamilies, target.Spec.Service.IPFamilies)
			require.Empty(t, target.Spec.Service.Type, "restore targets must retain ClusterIP exposure")
			*target.Spec.Service.IPFamilyPolicy = corev1.IPFamilyPolicyPreferDualStack
			target.Spec.Service.IPFamilies[0] = corev1.IPv4Protocol
			require.Equal(t, policy, *template.IPFamilyPolicy, "target changes must not mutate the immutable request")
			require.Equal(t, corev1.IPv6Protocol, template.IPFamilies[0])
		})
	}
}

func TestFreshTargetSingleCreationAttempt(t *testing.T) {
	for _, outcome := range []string{"acknowledged", "lost response", "not created", existingTarget, existingDataPVC} {
		t.Run(outcome, func(t *testing.T) {
			c, request := freshTargetFixture(t)
			creates := 0
			wrapped := interceptor.NewClient(c, interceptor.Funcs{Create: func(ctx context.Context, base client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				options := &client.CreateOptions{}
				for _, opt := range opts {
					opt.ApplyToCreate(options)
				}
				if len(options.DryRun) > 0 {
					return nil
				}
				creates++
				if outcome == "not created" {
					return errors.New("connection lost")
				}
				obj.SetUID("created-uid")
				if err := base.Create(ctx, obj, opts...); err != nil {
					return err
				}
				if outcome == "lost response" {
					return errors.New("response lost")
				}
				return nil
			}})
			if outcome == existingTarget {
				require.NoError(t, c.Create(t.Context(), projectRestoreTarget(request)))
			}
			if outcome == existingDataPVC {
				require.NoError(t, c.Create(t.Context(), &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: request.Namespace, Name: targetPVCName(request)}}))
			}
			m := NewManager(wrapped, c.Scheme(), nil, nil, "")
			_, _, _ = m.prepareTarget(t.Context(), logr.Discard(), request)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(request), request))
			if request.Status.Phase != api.RestorePhaseFailed {
				_, _, err := m.prepareTarget(t.Context(), logr.Discard(), request)
				require.NoError(t, err)
			}
			if outcome == existingTarget || outcome == existingDataPVC {
				require.Zero(t, creates)
			} else {
				require.Equal(t, 1, creates)
			}
			if outcome == "not created" || outcome == existingTarget || outcome == existingDataPVC {
				require.Equal(t, api.RestorePhaseFailed, request.Status.Phase)
			} else {
				require.Equal(t, types.UID("created-uid"), request.Status.Target.UID)
			}
		})
	}
}

func TestFreshTargetPluginConfiguration(t *testing.T) {
	for _, name := range []string{"awskms", "azurekeyvault", "gcpckms", "ocikms", "pkcs11", "custom-kms"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name          string
				configuration *api.RestoreTargetConfiguration
			}{
				{name: "omitted"},
				{name: "empty", configuration: &api.RestoreTargetConfiguration{}},
				{name: "enabled", configuration: &api.RestoreTargetConfiguration{
					Plugin: &api.PluginConfig{AutoDownload: ptr.To(true)},
				}},
				{name: "disabled", configuration: &api.RestoreTargetConfiguration{
					Plugin: &api.PluginConfig{AutoDownload: ptr.To(false)},
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, request := freshTargetFixture(t)
					template := request.Spec.ClusterTemplate
					template.Configuration = tc.configuration
					template.Unseal = api.UnsealConfig{Type: "kms", KMS: &api.KMSPluginSealConfig{
						PluginName: name, Config: map[string]string{"key_id": "snapshot-key"},
					}}
					template.Plugins = []api.Plugin{{Type: "kms", Name: name,
						Image: "registry.example.com/kms:v1.0.0@sha256:" + strings.Repeat("a", 64),
					}}

					target := projectRestoreTarget(request)
					if tc.configuration == nil {
						require.Nil(t, target.Spec.Configuration)
					} else {
						require.NotNil(t, target.Spec.Configuration)
						require.Equal(t, tc.configuration.Plugin, target.Spec.Configuration.Plugin)
					}
					got, err := configuration.Render(target, configuration.RenderOptions{})
					require.NoError(t, err)
					file, diagnostics := hclsyntax.ParseConfig([]byte(got), "config.hcl", hcl.InitialPos)
					require.False(t, diagnostics.HasErrors(), diagnostics.Error())
					attr := file.Body.(*hclsyntax.Body).Attributes["plugin_auto_download"]
					if tc.configuration == nil || tc.configuration.Plugin == nil {
						require.Nil(t, attr, "omitted settings must retain OpenBao defaults")
						return
					}
					require.NotNil(t, attr)
					value, diagnostics := attr.Expr.Value(nil)
					require.False(t, diagnostics.HasErrors(), diagnostics.Error())
					require.Equal(t, *tc.configuration.Plugin.AutoDownload, value.True())
					*target.Spec.Configuration.Plugin.AutoDownload = !value.True()
					require.Equal(t, value.True(), *template.Configuration.Plugin.AutoDownload,
						"target configuration must not mutate the immutable template")
				})
			}
		})
	}
}

func TestFreshTargetCleanupRefusesReplacement(t *testing.T) {
	for _, replace := range []string{"cluster", "PVC"} {
		t.Run(replace, func(t *testing.T) {
			c, request := freshTargetFixture(t)
			request.Status.Target = &api.RestoreTargetStatus{ReservedAt: metav1.Now(), UID: "original", DataPVCUID: "original-pvc", Cleanup: api.RestoreTargetCleanupPending}
			require.NoError(t, c.Status().Update(t.Context(), request))
			var replacement client.Object
			if replace == "cluster" {
				cluster := projectRestoreTarget(request)
				cluster.UID = "replacement"
				replacement = cluster
			} else {
				replacement = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: request.Namespace, Name: targetPVCName(request), UID: "replacement"}}
			}
			require.NoError(t, c.Create(t.Context(), replacement))
			_, err := NewManager(c, c.Scheme(), nil, nil, "").cleanupTarget(t.Context(), logr.Discard(), request)
			require.NoError(t, err)
			require.Equal(t, api.RestoreTargetCleanupFailed, request.Status.Target.Cleanup)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(replacement), replacement))
		})
	}
}

func TestFreshTargetCleanupPreservesPausedHoldRelease(t *testing.T) {
	c, request := freshTargetFixture(t)
	cluster := projectRestoreTarget(request)
	cluster.UID = "original"
	cluster.Annotations[constants.AnnotationRestoreHold] = string(request.UID)
	require.NoError(t, c.Create(t.Context(), cluster))
	request.Status.Target = &api.RestoreTargetStatus{ReservedAt: metav1.Now(), UID: cluster.UID, Cleanup: api.RestoreTargetCleanupPending}
	require.NoError(t, c.Status().Update(t.Context(), request))
	_, err := NewManager(c, c.Scheme(), nil, nil, "").cleanupTarget(t.Context(), logr.Discard(), request)
	require.NoError(t, err)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
	require.True(t, cluster.Spec.Paused)
	require.Empty(t, cluster.Annotations[constants.AnnotationRestoreHold])
}

func freshTargetFixture(t *testing.T) (client.WithWatch, *api.OpenBaoRestore) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	request := &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "recovery", UID: "request-uid"},
		Spec: api.OpenBaoRestoreSpec{Cluster: "fresh", Force: true, TargetLifecycle: api.RestoreTargetLifecycleDisposable,
			Source:          api.RestoreSource{ExpectedClusterID: "source-id", ExpectedVersion: "2.7.0"},
			ClusterTemplate: &api.RestoreClusterTemplate{Version: "2.7.0"}},
		Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseValidating},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.OpenBaoRestore{}, &api.OpenBaoCluster{}).WithObjects(request).Build()
	return c, request
}

func TestFreshTargetValidationFailureIsTerminal(t *testing.T) {
	for _, reason := range []string{"admission", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			c, request := freshTargetFixture(t)
			if reason == "deadline" {
				request.Status.StartTime = &metav1.Time{Time: time.Now().Add(-time.Hour)}
				require.NoError(t, c.Status().Update(t.Context(), request))
			}
			wrapped := interceptor.NewClient(c, interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return apierrors.NewForbidden(schema.GroupResource{Group: "openbao.org", Resource: "openbaoclusters"}, "fresh", errors.New("fixture rejection"))
			}})
			_, _, err := NewManager(wrapped, c.Scheme(), nil, nil, "").prepareTarget(t.Context(), logr.Discard(), request)
			require.NoError(t, err)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(request), request))
			require.Equal(t, api.RestorePhaseFailed, request.Status.Phase)
			require.Nil(t, request.Status.Target)
		})
	}
}

func TestFreshTargetRequiresProtectedPVCOwnership(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "tenant collision", true: "original owner"}[owned], func(t *testing.T) {
			c, request := freshTargetFixture(t)
			cluster, pvc := freshTargetObjects(request)
			require.NoError(t, c.Create(t.Context(), cluster))
			request.Status.Target = &api.RestoreTargetStatus{UID: cluster.UID, ReservedAt: metav1.Now()}
			require.NoError(t, c.Status().Update(t.Context(), request))
			if !owned {
				pvc.Annotations = nil
			}
			require.NoError(t, c.Create(t.Context(), pvc))
			_, _, err := NewManager(c, c.Scheme(), nil, nil, "").prepareTarget(t.Context(), logr.Discard(), request)
			require.NoError(t, err)
			if owned {
				require.Equal(t, pvc.UID, request.Status.Target.DataPVCUID)
			} else {
				require.Equal(t, api.RestorePhaseFailed, request.Status.Phase)
				require.Empty(t, request.Status.Target.DataPVCUID)
			}
		})
	}
}

// freshTargetObjects returns the original target and its owned data volume.
// Tests create each object separately to exercise missing and replacement states.
func freshTargetObjects(request *api.OpenBaoRestore) (*api.OpenBaoCluster, *corev1.PersistentVolumeClaim) {
	cluster := projectRestoreTarget(request)
	cluster.UID = "target"
	cluster.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Minute))
	cluster.Annotations[constants.AnnotationRestoreHold] = string(request.UID)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         request.Namespace,
			Name:              targetPVCName(request),
			UID:               "data",
			CreationTimestamp: metav1.Now(),
			Annotations:       map[string]string{constants.AnnotationOpenBaoOwnerUID: string(cluster.UID)},
		},
	}
	return cluster, pvc
}

func TestFreshTargetPreservesSealAndIdentityConfiguration(t *testing.T) {
	_, request := freshTargetFixture(t)
	request.Spec.ClusterTemplate.Unseal = api.UnsealConfig{
		Type:                 "kms",
		KMS:                  &api.KMSPluginSealConfig{PluginName: "hsm", Config: map[string]string{"key_id": "original"}},
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: "seal-credentials"},
	}
	request.Spec.ClusterTemplate.Plugins = []api.Plugin{{Type: "kms", Name: "hsm", Command: "hsm-plugin"}}
	request.Spec.ClusterTemplate.ServiceAccount = &api.ServiceAccountConfig{Annotations: map[string]string{"iam.gke.io/gcp-service-account": "recovery@example"}}
	request.Spec.ClusterTemplate.PodMetadata = &api.PodMetadataConfig{Labels: map[string]string{"azure.workload.identity/use": "true"}}

	target := projectRestoreTarget(request)
	template := request.Spec.ClusterTemplate
	require.Equal(t, template.Unseal, *target.Spec.Unseal)
	require.Equal(t, template.Plugins, target.Spec.Plugins)
	require.Equal(t, template.ServiceAccount, target.Spec.ServiceAccount)
	require.Equal(t, template.PodMetadata, target.Spec.PodMetadata)
	// Target reconciliation must not mutate the immutable request template.
	target.Spec.Unseal.KMS.Config["key_id"] = "replacement-key"
	target.Spec.Plugins[0].Command = "replacement-plugin"
	target.Spec.ServiceAccount.Annotations["iam.gke.io/gcp-service-account"] = "replacement@example"
	target.Spec.PodMetadata.Labels["azure.workload.identity/use"] = "false"
	require.Equal(t, "original", template.Unseal.KMS.Config["key_id"])
	require.Equal(t, "hsm-plugin", template.Plugins[0].Command)
	require.Equal(t, "recovery@example", template.ServiceAccount.Annotations["iam.gke.io/gcp-service-account"])
	require.Equal(t, "true", template.PodMetadata.Labels["azure.workload.identity/use"])
}
