package observability

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// BenchmarkRestoreStateObservation measures one metric refresh while other
// clusters' retained restore histories grow. The target retains ten requests.
func BenchmarkRestoreStateObservation(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("namespace_requests=%d", count), func(b *testing.B) {
			namespace := fmt.Sprintf("benchmark-%d", count)
			requests := make([]api.OpenBaoRestore, count)
			for i := range requests {
				requests[i] = api.OpenBaoRestore{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace, Name: fmt.Sprintf("restore-%d", i),
						CreationTimestamp: metav1.NewTime(time.Unix(int64(i), 0)),
					},
					Spec:   api.OpenBaoRestoreSpec{Cluster: fmt.Sprintf("cluster-%d", i/10)},
					Status: api.OpenBaoRestoreStatus{Phase: api.RestorePhaseCompleted},
				}
			}
			b.Cleanup(func() {
				restoreStateGauge.DeletePartialMatch(prometheus.Labels{"namespace": namespace})
			})
			// Other clusters already have published series. The cache index supplies
			// only the ten requests belonging to cluster-0 for this refresh.
			for i := 0; i < count; i += 10 {
				NewRestoreMetrics(namespace, fmt.Sprintf("cluster-%d", i/10)).setState(2)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				ObserveClusterRestoreState(namespace, "cluster-0", requests[:10])
			}
		})
	}
}
