//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestVAP_OpenBaoCluster_CloudIdentityPodTemplates(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)
	username := "pod-cloud-identity-editor"
	tenantClient := newImpersonatedClient(t, username)
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantNamespacedResourceVerbs(t, namespace, username, "cloud-delegation", "openbao.org", "openbaoclusters",
		[]string{"delegated-create", "delegated-update"}, "usecloudidentities")

	baseline := map[string]*openbaov1alpha1.OpenBaoCluster{}
	for _, name := range []string{"ordinary", "delegated"} {
		cluster := newMinimalClusterObj(namespace, name+"-update")
		cluster.Spec.InitContainer = nil
		// Keep the read pool disabled: identity metadata must require delegation
		// before an author can later enable workloads that inherit it.
		cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{}
		require.NoError(t, tenantClient.Create(ctx, cluster))
		baseline[name] = cluster
	}

	for _, location := range []struct {
		name        string
		readReplica bool
	}{
		{name: "main"},
		{name: "read-replicas", readReplica: true},
	} {
		for _, identity := range []struct {
			name        string
			annotations map[string]string
			labels      map[string]string
		}{
			{name: "aws-eks", annotations: map[string]string{
				"eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/openbao",
			}},
			{name: "aws-iam", annotations: map[string]string{"iam.amazonaws.com/role": "openbao"}},
			{name: "gcp", annotations: map[string]string{
				"iam.gke.io/gcp-service-account": "openbao@example.iam.gserviceaccount.com",
			}},
			{name: "azure-annotation", annotations: map[string]string{
				"azure.workload.identity/client-id": "client-id",
			}},
			{name: "azure-label", labels: map[string]string{"azure.workload.identity/use": "true"}},
			{name: "azure-legacy-label", labels: map[string]string{"aadpodidbinding": "openbao"}},
		} {
			t.Run(location.name+"/"+identity.name, func(t *testing.T) {
				for _, name := range []string{"ordinary", "delegated"} {
					cluster := baseline[name].DeepCopy()
					metadata := &openbaov1alpha1.PodMetadataConfig{
						Annotations: identity.annotations,
						Labels:      identity.labels,
					}
					if location.readReplica {
						cluster.Spec.ReadReplicas.Template = &openbaov1alpha1.ReadReplicaTemplateConfig{
							Metadata: metadata,
						}
					} else {
						cluster.Spec.PodMetadata = metadata
					}
					for _, operation := range []string{"CREATE", "UPDATE"} {
						candidate := cluster.DeepCopy()
						var err error
						if operation == "CREATE" {
							candidate.Name = name + "-create"
							candidate.ResourceVersion = ""
							candidate.UID = ""
							candidate.CreationTimestamp = metav1.Time{}
							err = tenantClient.Create(ctx, candidate, client.DryRunAll)
						} else {
							err = tenantClient.Update(ctx, candidate, client.DryRunAll)
						}
						if name == "delegated" {
							require.NoError(t, err, "%s with resource-scoped cloud identity delegation", operation)
							continue
						}
						requireAdmissionDenied(t, err)
						require.ErrorContains(t, err, "use cloud identities", "%s without delegation", operation)
					}
				}
			})
		}
	}

	t.Run("unrelated-metadata", func(t *testing.T) {
		cluster := baseline["ordinary"].DeepCopy()
		cluster.Spec.ReadReplicas.Template = &openbaov1alpha1.ReadReplicaTemplateConfig{
			Metadata: &openbaov1alpha1.PodMetadataConfig{
				Annotations: map[string]string{"example.com/owner": "team"},
				Labels:      map[string]string{"example.com/team": "team"},
			},
		}
		require.NoError(t, tenantClient.Update(ctx, cluster, client.DryRunAll))
	})
}
