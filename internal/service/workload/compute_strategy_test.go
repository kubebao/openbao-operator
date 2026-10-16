package workload

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

func TestReconcileStatefulSetUpdateStrategySwitchesToOnDeleteAtomically(t *testing.T) {
	cluster := workloadOwnershipCluster()
	cluster.Status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyBlueGreen
	partition := int32(0)
	statefulSet := workloadOwnershipStatefulSet(cluster.Name, cluster.Namespace, workloadOwnershipRef(cluster), 3)
	statefulSet.UID = types.UID("stable-statefulset-uid")
	statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type: appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
			Partition: &partition,
		},
	}
	mgr := workloadOwnershipManager(cluster, statefulSet)
	if err := mgr.client.Get(t.Context(), client.ObjectKeyFromObject(statefulSet), statefulSet); err != nil {
		t.Fatal(err)
	}

	if err := mgr.reconcileStatefulSetUpdateStrategy(context.Background(), logr.Discard(), cluster, constants.LabelValueOpenBaoWorkloadPoolVoter, statefulSet); err != nil {
		t.Fatalf("reconcileStatefulSetUpdateStrategy() error = %v", err)
	}

	stored := &appsv1.StatefulSet{}
	if err := mgr.client.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), stored); err != nil {
		t.Fatalf("get transitioned StatefulSet: %v", err)
	}
	if stored.UID != statefulSet.UID {
		t.Fatalf("StatefulSet UID = %q, want stable UID %q", stored.UID, statefulSet.UID)
	}
	if stored.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		t.Fatalf("update strategy type = %q, want %q", stored.Spec.UpdateStrategy.Type, appsv1.OnDeleteStatefulSetStrategyType)
	}
	if stored.Spec.UpdateStrategy.RollingUpdate != nil {
		t.Fatalf("rollingUpdate = %#v, want nil for OnDelete", stored.Spec.UpdateStrategy.RollingUpdate)
	}
}

func TestReconcileStatefulSetUpdateStrategySwitchesToRollingUpdate(t *testing.T) {
	cluster := workloadOwnershipCluster()
	statefulSet := workloadOwnershipStatefulSet(cluster.Name, cluster.Namespace, workloadOwnershipRef(cluster), 3)
	statefulSet.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type: appsv1.OnDeleteStatefulSetStrategyType,
	}
	statefulSet.Status.CurrentRevision = "current-blue-revision"
	statefulSet.Status.UpdateRevision = "post-promotion-template-revision"
	mgr := workloadOwnershipManager(cluster, statefulSet)
	if err := mgr.client.Get(t.Context(), client.ObjectKeyFromObject(statefulSet), statefulSet); err != nil {
		t.Fatal(err)
	}

	if err := mgr.reconcileStatefulSetUpdateStrategy(context.Background(), logr.Discard(), cluster, constants.LabelValueOpenBaoWorkloadPoolVoter, statefulSet); err != nil {
		t.Fatalf("reconcileStatefulSetUpdateStrategy() error = %v", err)
	}

	stored := &appsv1.StatefulSet{}
	if err := mgr.client.Get(context.Background(), client.ObjectKeyFromObject(statefulSet), stored); err != nil {
		t.Fatalf("get transitioned StatefulSet: %v", err)
	}
	if stored.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		t.Fatalf("update strategy type = %q, want %q", stored.Spec.UpdateStrategy.Type, appsv1.RollingUpdateStatefulSetStrategyType)
	}
	if stored.Spec.UpdateStrategy.RollingUpdate == nil || stored.Spec.UpdateStrategy.RollingUpdate.Partition == nil {
		t.Fatalf("rollingUpdate partition = %#v, want %d", stored.Spec.UpdateStrategy.RollingUpdate, cluster.Spec.Replicas)
	}
	if partition := *stored.Spec.UpdateStrategy.RollingUpdate.Partition; partition != cluster.Spec.Replicas {
		t.Fatalf("rollingUpdate partition = %d, want %d", partition, cluster.Spec.Replicas)
	}
}

func TestReconcileStatefulSetUpdateStrategyRejectsUnownedStatefulSet(t *testing.T) {
	for _, pendingUpgrade := range []bool{false, true} {
		name := "idle strategy transition"
		if pendingUpgrade {
			name = "pending version upgrade"
		}
		t.Run(name, func(t *testing.T) {
			cluster := workloadOwnershipCluster()
			cluster.Status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyBlueGreen
			if pendingUpgrade {
				cluster.Status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyRollingUpdate
				cluster.Status.Initialized = true
				cluster.Status.CurrentVersion = "2.5.5"
			}
			statefulSet := workloadOwnershipStatefulSet(cluster.Name, cluster.Namespace, nil, 3)
			statefulSet.Spec.UpdateStrategy.Type = appsv1.RollingUpdateStatefulSetStrategyType
			mgr := workloadOwnershipManager(cluster, statefulSet)
			if err := mgr.client.Get(t.Context(), client.ObjectKeyFromObject(statefulSet), statefulSet); err != nil {
				t.Fatal(err)
			}
			original := statefulSet.DeepCopy()
			err := mgr.reconcileStatefulSetUpdateStrategy(t.Context(), logr.Discard(), cluster, constants.LabelValueOpenBaoWorkloadPoolVoter, statefulSet)
			if err == nil || !strings.Contains(err.Error(), "requires OpenBaoCluster owner proof") {
				t.Fatalf("reconcileStatefulSetUpdateStrategy() error = %v, want owner proof error", err)
			}
			stored := &appsv1.StatefulSet{}
			if err := mgr.client.Get(t.Context(), client.ObjectKeyFromObject(statefulSet), stored); err != nil {
				t.Fatal(err)
			}
			if stored.Spec.UpdateStrategy.Type != original.Spec.UpdateStrategy.Type || stored.Spec.UpdateStrategy.RollingUpdate != nil {
				t.Fatalf("unowned StatefulSet strategy changed: %#v", stored.Spec.UpdateStrategy)
			}
		})
	}
}
