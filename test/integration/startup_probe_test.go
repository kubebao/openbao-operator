//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestCRD_StartupProbeBudget(t *testing.T) {
	namespace := newTestNamespace(t)
	setRestoreDestinationApproval(t, namespace, "true")
	for _, pool := range []string{"cluster", "restore", "scheduled"} {
		for index, tt := range []struct {
			name      string
			threshold int32
			valid     bool
		}{
			{name: "default", valid: true},
			{name: "released budget", threshold: 60, valid: true},
			{name: "slow startup", threshold: 180, valid: true},
			{name: "maximum budget", threshold: 720, valid: true},
			{name: "shortened budget", threshold: 59},
			{name: "unbounded budget", threshold: 721},
			{name: "negative budget", threshold: -1},
		} {
			t.Run(pool+"/"+tt.name, func(t *testing.T) {
				cluster := newMinimalClusterObj(namespace, fmt.Sprintf("startup-%s-%d", pool, index))
				probes := &api.ProbeConfig{Startup: &api.StartupProbeConfig{FailureThreshold: tt.threshold}}
				var object client.Object = cluster
				switch pool {
				case "cluster":
					cluster.Spec.Probes = probes
				case "restore", "scheduled":
					request := newFreshRestoreRequest(namespace, cluster.Name)
					request.Spec.ClusterTemplate.Probes = probes
					if pool == "restore" {
						object = request
					} else {
						cluster.Spec.Backup = &api.BackupSchedule{
							Schedule: "0 * * * *", JWTAuthRole: "backup", Target: request.Spec.Source.Target,
							RestoreTest: &api.RestoreTest{
								EverySuccessfulBackups: 1, Namespace: namespace,
								CredentialsSecretRef: &corev1.LocalObjectReference{Name: "reader"},
								ClusterTemplate:      *request.Spec.ClusterTemplate,
							},
						}
					}
				}
				err := k8sClient.Create(ctx, object, client.DryRunAll)
				if tt.valid {
					require.NoError(t, err)
					if tt.threshold == 0 {
						var actual *api.ProbeConfig
						switch o := object.(type) {
						case *api.OpenBaoRestore:
							actual = o.Spec.ClusterTemplate.Probes
						case *api.OpenBaoCluster:
							actual = o.Spec.Probes
							if pool == "scheduled" {
								actual = o.Spec.Backup.RestoreTest.ClusterTemplate.Probes
							}
						}
						require.EqualValues(t, 60, actual.Startup.FailureThreshold)
					}
				} else {
					requireInvalidRequest(t, err)
					require.ErrorContains(t, err, "failureThreshold")
				}
			})
		}
	}
}
