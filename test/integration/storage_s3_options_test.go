//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCRD_StorageTargetNestedS3Options(t *testing.T) {
	for _, kind := range []string{"backup", "restore"} {
		for _, tc := range []struct {
			name   string
			s3     *api.S3TargetConfig
			region string
			path   bool
			role   string
		}{
			{name: "legacy", region: "us-east-1", path: true, role: "legacy-role"},
			{name: "empty-block", s3: &api.S3TargetConfig{}, region: "us-east-1", path: true, role: "legacy-role"},
			{name: "nested", s3: &api.S3TargetConfig{
				Region: ptr.To("eu-west-1"), UsePathStyle: ptr.To(false), RoleARN: ptr.To(""),
			}, region: "eu-west-1"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				namespace := newTestNamespace(t)
				waitForOpenBaoClusterAdmissionPolicies(t, namespace)
				target := api.BackupTarget{Endpoint: "https://s3.example.com", Bucket: "snapshots",
					UsePathStyle: true, RoleARN: "legacy-role", S3: tc.s3,
				}
				object, storedTarget := storageTargetObject(namespace, kind, target)
				require.NoError(t, k8sClient.Create(ctx, object))
				require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(object), object))
				stored := storedTarget()
				require.Equal(t, "s3", stored.Provider)
				require.Equal(t, "us-east-1", stored.Region, "legacy default still applies")
				require.True(t, stored.UsePathStyle, "legacy value stays on the wire")
				require.Equal(t, tc.s3, stored.S3, "nested fields have no defaults")
				require.Equal(t, tc.region, stored.EffectiveS3Region())
				require.Equal(t, tc.path, stored.EffectiveS3UsePathStyle())
				require.Equal(t, tc.role, stored.EffectiveS3RoleARN())
			})
		}
		for _, provider := range []string{"gcs", "azure"} {
			t.Run(kind+"/wrong-provider-"+provider, func(t *testing.T) {
				target := api.BackupTarget{Provider: provider, Bucket: "snapshots", S3: &api.S3TargetConfig{}}
				object, _ := storageTargetObject(newTestNamespace(t), kind, target)
				err := k8sClient.Create(ctx, object, client.DryRunAll)
				require.ErrorContains(t, err, "backup target s3 options are only supported when provider is s3")
			})
		}
		t.Run(kind+"/azure-bucket-required", func(t *testing.T) {
			target := api.BackupTarget{Provider: "azure", Azure: &api.AzureTargetConfig{
				StorageAccount: "account", Container: "snapshots",
			}}
			object, _ := storageTargetObject(newTestNamespace(t), kind, target)
			err := k8sClient.Create(ctx, object, client.DryRunAll)
			require.ErrorContains(t, err, "bucket")
		})
	}
}

func TestVAP_StorageTargetNestedS3IdentityDelegation(t *testing.T) {
	for _, kind := range []string{"backup", "restore"} {
		t.Run(kind, func(t *testing.T) {
			namespace := newTestNamespace(t)
			waitForOpenBaoClusterAdmissionPolicies(t, namespace)
			username := "nested-storage-editor"
			grantTenantOpenBaoWriteAccess(t, namespace, username)
			if kind == "restore" {
				grantClusterRestoreAccess(t, namespace, "nested-target", username)
			}
			actor := newImpersonatedClient(t, username)
			target := api.BackupTarget{Endpoint: "https://s3.example.com", Bucket: "snapshots",
				S3: &api.S3TargetConfig{RoleARN: ptr.To("nested-role")},
			}
			object, _ := storageTargetObject(namespace, kind, target)
			require.Eventually(t, func() bool {
				err := actor.Create(ctx, object, client.DryRunAll)
				return err != nil && strings.Contains(err.Error(), "use cloud identities")
			}, 10*time.Second, 100*time.Millisecond)
			// Suppressing the compatibility role must also suppress its cloud
			// delegation requirement because no role is injected into the Job.
			target.RoleARN = "legacy-role"
			target.S3 = &api.S3TargetConfig{RoleARN: ptr.To("")}
			suppressed, _ := storageTargetObject(namespace, kind, target)
			require.NoError(t, actor.Create(ctx, suppressed, client.DryRunAll))
			grantClusterCloudIdentitiesAccess(t, namespace, "nested-target", username)
			require.Eventually(t, func() bool {
				return actor.Create(ctx, object, client.DryRunAll) == nil
			}, 10*time.Second, 100*time.Millisecond)
		})
	}
}

func storageTargetObject(namespace, kind string, target api.BackupTarget) (client.Object, func() *api.BackupTarget) {
	if kind == "backup" {
		cluster := newMinimalClusterObj(namespace, "nested-target")
		cluster.Spec.InitContainer = nil
		cluster.Spec.Backup = &api.BackupSchedule{Schedule: "0 0 * * *", JWTAuthRole: "backup", Target: target}
		return cluster, func() *api.BackupTarget { return &cluster.Spec.Backup.Target }
	}
	request := &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: "nested-request", Namespace: namespace},
		Spec: api.OpenBaoRestoreSpec{Cluster: "nested-target", Source: api.RestoreSource{Key: "snapshot", Target: target}},
	}
	return request, func() *api.BackupTarget { return &request.Spec.Source.Target }
}
