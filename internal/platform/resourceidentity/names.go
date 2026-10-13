package resourceidentity

import (
	"fmt"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const (
	configInitMapSuffix   = "-config-init"
	readReplicaNameSuffix = "-read"
)

func Labels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	return map[string]string{
		constants.LabelAppName:        constants.LabelValueAppNameOpenBao,
		constants.LabelAppInstance:    cluster.Name,
		constants.LabelAppManagedBy:   constants.LabelValueAppManagedByOpenBaoOperator,
		constants.LabelOpenBaoCluster: cluster.Name,
	}
}

func PodSelectorLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	return PodSelectorLabelsWithRevision(cluster, "")
}

func PodSelectorLabelsWithRevision(cluster *openbaov1alpha1.OpenBaoCluster, revision string) map[string]string {
	return PodSelectorLabelsForPoolWithRevision(cluster, "", revision)
}

func PodSelectorLabelsForPool(cluster *openbaov1alpha1.OpenBaoCluster, pool string) map[string]string {
	return PodSelectorLabelsForPoolWithRevision(cluster, pool, "")
}

func PodSelectorLabelsForPoolWithRevision(cluster *openbaov1alpha1.OpenBaoCluster, pool string, revision string) map[string]string {
	labels := Labels(cluster)
	if pool != "" {
		labels[constants.LabelOpenBaoWorkloadPool] = pool
	}
	if revision != "" {
		labels[constants.LabelOpenBaoRevision] = revision
	}
	return labels
}

func VoterPodSelectorLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	return PodSelectorLabelsForPool(cluster, constants.LabelValueOpenBaoWorkloadPoolVoter)
}

func VoterPodSelectorLabelsWithRevision(cluster *openbaov1alpha1.OpenBaoCluster, revision string) map[string]string {
	return PodSelectorLabelsForPoolWithRevision(cluster, constants.LabelValueOpenBaoWorkloadPoolVoter, revision)
}

func ReadReplicaPodSelectorLabels(cluster *openbaov1alpha1.OpenBaoCluster) map[string]string {
	return PodSelectorLabelsForPool(cluster, constants.LabelValueOpenBaoWorkloadPoolReadReplica)
}

func UnsealSecretName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + constants.SuffixUnsealKey
}

func ConfigMapName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + constants.SuffixConfigMap
}

func ConfigMapNameWithRevision(cluster *openbaov1alpha1.OpenBaoCluster, revision string) string {
	if revision == "" {
		return ConfigMapName(cluster)
	}
	return fmt.Sprintf("%s%s-%s", cluster.Name, constants.SuffixConfigMap, revision)
}

func ReadReplicaConfigMapName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return fmt.Sprintf("%s%s%s", cluster.Name, constants.SuffixConfigMap, readReplicaNameSuffix)
}

func ConfigMapNameForPoolWithRevision(cluster *openbaov1alpha1.OpenBaoCluster, pool string, revision string) string {
	if pool == constants.LabelValueOpenBaoWorkloadPoolReadReplica {
		if revision == "" {
			return ReadReplicaConfigMapName(cluster)
		}
		return fmt.Sprintf("%s-%s", ReadReplicaConfigMapName(cluster), revision)
	}

	return ConfigMapNameWithRevision(cluster, revision)
}

func ConfigInitMapName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + configInitMapSuffix
}

func TLSServerSecretName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + constants.SuffixTLSServer
}

func HeadlessServiceName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name
}

func ReadReplicaStatefulSetName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + readReplicaNameSuffix
}

func ReadReplicaServiceName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	return cluster.Name + readReplicaNameSuffix
}

func ServiceAccountName(cluster *openbaov1alpha1.OpenBaoCluster) string {
	if cluster.Spec.ServiceAccount != nil && cluster.Spec.ServiceAccount.Name != "" {
		return cluster.Spec.ServiceAccount.Name
	}
	return cluster.Name + constants.SuffixServiceAccount
}

// RestoreServiceAccountName resolves the executor account without changing the
// target workload's ServiceAccount.
func RestoreServiceAccountName(cluster *openbaov1alpha1.OpenBaoCluster, request *openbaov1alpha1.OpenBaoRestore) string {
	if request.Spec.ServiceAccountName != "" {
		return request.Spec.ServiceAccountName
	}
	return cluster.Name + constants.SuffixRestoreServiceAccount
}
