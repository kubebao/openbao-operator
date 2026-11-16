package rolling

import (
	"errors"
	"fmt"
	"math"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portworkload "github.com/kubebao/openbao-operator/internal/port/workload"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/core"
)

type upgradeTimeoutSpec struct {
	Deadline time.Duration
	Reason   string
	Message  string
	Error    string
}

func failUpgradeIfStartedTimeout(cluster *openbaov1alpha1.OpenBaoCluster, timeout upgradeTimeoutSpec) error {
	if cluster == nil || cluster.Status.Upgrade == nil {
		return nil
	}
	return failUpgradeIfTimestampTimeout(cluster, cluster.Status.Upgrade.StartedAt, timeout)
}

func failUpgradeIfTimestampTimeout(cluster *openbaov1alpha1.OpenBaoCluster, startedAt *metav1.Time, timeout upgradeTimeoutSpec) error {
	if startedAt == nil {
		return nil
	}
	if time.Since(startedAt.Time) <= timeout.Deadline {
		return nil
	}

	core.SetUpgradeFailed(&cluster.Status, timeout.Reason, timeout.Message)
	return errors.New(timeout.Error)
}

// The existing deadline is measured from upgrade start, not from each Pod.
// Opt-in longer budgets must therefore cover the sequential voter rollout.
func rollingReadinessDeadline(cluster *openbaov1alpha1.OpenBaoCluster) time.Duration {
	if cluster == nil {
		return upgrade.DefaultPodReadyTimeout
	}
	extension := portworkload.StartupBudgetExtension(cluster.Spec.Probes)
	if extension <= 0 {
		return upgrade.DefaultPodReadyTimeout
	}
	seconds := int64((upgrade.DefaultPodReadyTimeout + extension) / time.Second)
	pods := max(int64(1), int64(cluster.Spec.Replicas))
	if cluster.Spec.ReadReplicas != nil {
		pods += max(int64(0), int64(cluster.Spec.ReadReplicas.Replicas))
	}
	seconds *= pods
	// Saturate before converting seconds to nanoseconds for an oversized spec.
	maximumSeconds := int64((time.Duration(math.MaxInt64) - upgrade.DefaultHealthCheckTimeout) / time.Second)
	return time.Duration(min(seconds, maximumSeconds)) * time.Second
}

func podRevisionTimeout(podName string, cluster *openbaov1alpha1.OpenBaoCluster) upgradeTimeoutSpec {
	deadline := rollingReadinessDeadline(cluster)
	return upgradeTimeoutSpec{
		Deadline: deadline,
		Reason:   upgrade.ReasonPodNotReady,
		Message:  fmt.Sprintf(upgrade.MessagePodNotReady, podName, deadline),
		Error:    fmt.Sprintf("pod %s did not roll to update revision within %v", podName, deadline),
	}
}

func podReadyTimeout(podName string, cluster *openbaov1alpha1.OpenBaoCluster) upgradeTimeoutSpec {
	deadline := rollingReadinessDeadline(cluster)
	return upgradeTimeoutSpec{
		Deadline: deadline,
		Reason:   upgrade.ReasonPodNotReady,
		Message:  fmt.Sprintf(upgrade.MessagePodNotReady, podName, deadline),
		Error:    fmt.Sprintf("pod %s did not become ready within %v", podName, deadline),
	}
}

func podHealthTimeout(podName string, cluster *openbaov1alpha1.OpenBaoCluster) upgradeTimeoutSpec {
	deadline := rollingReadinessDeadline(cluster) + upgrade.DefaultHealthCheckTimeout
	return upgradeTimeoutSpec{
		Deadline: deadline,
		Reason:   upgrade.ReasonHealthCheckFailed,
		Message:  fmt.Sprintf(upgrade.MessageHealthCheckFailed, podName, "timeout"),
		Error:    fmt.Sprintf("OpenBao health check timeout for pod %s after %v", podName, deadline),
	}
}

func stepDownTimeout(podName string) upgradeTimeoutSpec {
	return upgradeTimeoutSpec{
		Deadline: upgrade.DefaultStepDownTimeout,
		Reason:   upgrade.ReasonStepDownTimeout,
		Message:  fmt.Sprintf(upgrade.MessageStepDownTimeout, podName),
		Error:    fmt.Sprintf("step-down timeout for pod %s: exceeded %v", podName, upgrade.DefaultStepDownTimeout),
	}
}
