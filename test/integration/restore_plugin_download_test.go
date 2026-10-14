//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/app/openbaocluster/adminopsstatus"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
	"github.com/kubebao/openbao-operator/internal/service/restore"
	"github.com/kubebao/openbao-operator/internal/service/workload"
)

func TestRestoreTestKMSPluginDownloads(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	for _, provider := range []string{"azurekeyvault", "custom-kms"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", provider, enabled), func(t *testing.T) {
				namespace := newTestNamespace(t)
				setRestoreDestinationApproval(t, namespace, "true")
				controller := newControllerClient(t)
				template := newFreshRestoreRequest(namespace, "template").Spec.ClusterTemplate
				template.InitContainer = &api.InitContainerConfig{Enabled: true, Image: "example/init:qualification"}
				template.Unseal = api.UnsealConfig{Type: "kms", KMS: &api.KMSPluginSealConfig{
					PluginName: provider, Config: map[string]string{"key_id": "snapshot-key"},
				}}
				if provider == "azurekeyvault" {
					template.Unseal = api.UnsealConfig{Type: provider,
						AzureKeyVault: &api.AzureKeyVaultSealConfig{VaultName: "recovery", KeyName: "snapshot-key"},
					}
				}
				template.Plugins = []api.Plugin{{Type: "kms", Name: provider,
					Image: "registry.example.com/kms:v1.0.0@sha256:" + strings.Repeat("a", 64),
				}}
				template.Configuration = &api.RestoreTargetConfiguration{
					Plugin: &api.PluginConfig{AutoDownload: ptr.To(enabled)},
				}
				source := newMinimalClusterObj(namespace, "source")
				source.Spec.Backup = &api.BackupSchedule{
					Schedule: "0 0 1 1 *", Image: "backup:dev", JWTAuthRole: "backup",
					Target: api.BackupTarget{Endpoint: "https://storage.example", Bucket: "snapshots"},
					RestoreTest: &api.RestoreTest{EverySuccessfulBackups: 1, Namespace: namespace,
						CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage-reader"},
						ClusterTemplate:      *template},
				}
				require.NoError(t, k8sClient.Create(ctx, source))
				updateClusterStatus(t, source, func(status *api.OpenBaoClusterStatus) {
					status.Initialized = true
					stamp := metav1.Now()
					status.Backup = &api.BackupStatus{SuccessfulBackups: 1, LastBackupName: "snapshot", LastBackupTime: &stamp,
						LatestSnapshot: &api.BackupSnapshotSummary{ClusterID: "source", Version: "2.7.0", Size: 123,
							Digest: "sha256:" + strings.Repeat("b", 64)},
					}
				})
				backupManager := backup.NewManager(controller, k8sScheme, portopenbao.ClientConfig{}, nil, "").
					WithReader(controller).WithAdminOpsStatusMutator(adminopsstatus.NewMutator(controller, controller))
				_, err := backupManager.Reconcile(ctx, logr.Discard(), source)
				require.NoError(t, err)
				children := &api.OpenBaoRestoreList{}
				require.NoError(t, controller.List(ctx, children, client.InNamespace(namespace)))
				require.Len(t, children.Items, 1)
				request := &children.Items[0]
				require.Equal(t, template.Configuration, request.Spec.ClusterTemplate.Configuration,
					"source and request CRDs must preserve plugin settings")

				restoreManager := restore.NewManager(controller, k8sScheme, nil, nil, "").WithReader(controller)
				for range 3 {
					require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(request), request))
					_, err := restoreManager.Reconcile(ctx, logr.Discard(), request)
					require.NoError(t, err)
				}
				target := &api.OpenBaoCluster{}
				require.NoError(t, controller.Get(ctx, client.ObjectKey{Namespace: namespace, Name: request.Spec.Cluster}, target))
				require.NotNil(t, target.Spec.Configuration)
				require.Equal(t, template.Configuration.Plugin, target.Spec.Configuration.Plugin)
				rendered, err := configuration.Render(target, configuration.RenderOptions{})
				require.NoError(t, err)
				require.Regexp(t, fmt.Sprintf(`(?m)^plugin_auto_download\s*=\s*%t$`, enabled), rendered)
				require.NoError(t, controller.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: resourceidentity.TLSServerSecretName(target),
					Labels: map[string]string{
						constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
						constants.LabelOpenBaoCluster: target.Name,
					},
				}}))
				workloads := workload.NewManager(controller, k8sScheme, constants.PlatformKubernetes)
				require.NoError(t, workloads.EnsureStatefulSet(ctx, logr.Discard(), target, rendered,
					workload.StatefulSetSpec{Name: target.Name, Replicas: 1}))
				statefulSet := &appsv1.StatefulSet{}
				require.NoError(t, controller.Get(ctx, client.ObjectKeyFromObject(target), statefulSet))
				pluginMount := false
				for _, mount := range statefulSet.Spec.Template.Spec.Containers[0].VolumeMounts {
					if mount.MountPath == "/openbao/plugins" {
						pluginMount = true
						require.False(t, mount.ReadOnly)
					}
				}
				require.Equal(t, enabled, pluginMount, "automatic downloads need a writable plugin directory")
			})
		}
	}
}
