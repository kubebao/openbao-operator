package backup

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/port/adminops"
)

const reasonRestoreTestAdministratorReleased = "AdministratorReleased"

// A nil request without an issue means the previously bound child is gone.
// An unbound reservation cannot use absence as proof that creation never happened.
type restoreTestChild struct {
	request *api.OpenBaoRestore
	reason  string
	message string
}

func readRestoreTestChild(ctx context.Context, reader client.Reader, cluster *api.OpenBaoCluster) (restoreTestChild, error) {
	run := cluster.Status.Backup.RestoreTest.Active
	child := &api.OpenBaoRestore{}
	err := reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Name}, child)
	if apierrors.IsNotFound(err) {
		if run.UID == "" {
			return restoreTestChild{
				reason:  "RequestMissing",
				message: "Restore test creation is uncertain; inspect the destination, stop the old controller, then acknowledge this run with openbao.org/restore-test-acknowledge=" + run.Name + "/Release",
			}, nil
		}
		return restoreTestChild{}, nil
	}
	if err != nil {
		return restoreTestChild{}, err
	}
	if run.UID != "" && run.UID != child.UID {
		return restoreTestChild{}, nil
	}
	if child.Annotations[constants.AnnotationRestoreTestSource] != string(cluster.UID) {
		return restoreTestChild{
			reason:  "RequestReplaced",
			message: "Restore test request identity changed; inspect the destination, stop the old controller, then acknowledge this run with openbao.org/restore-test-acknowledge=" + run.Name + "/Release",
		}, nil
	}
	return restoreTestChild{request: child}, nil
}

// CancelRestoreTest cancels the recorded child before source-cluster deletion.
// Cross-namespace children cannot use owner references. Wait for the recorded
// child UID to disappear before dropping source state.
func CancelRestoreTest(ctx context.Context, reader client.Reader, c client.Client,
	mutate adminops.StatusMutator, cluster *api.OpenBaoCluster,
) error {
	live := &api.OpenBaoCluster{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(cluster), live); err != nil {
		return fmt.Errorf("read restore test before source cleanup: %w", err)
	}
	if live.UID != cluster.UID {
		return fmt.Errorf("source UID changed before restore test cancellation")
	}
	cluster = live
	if cluster.Status.Backup == nil || cluster.Status.Backup.RestoreTest == nil || cluster.Status.Backup.RestoreTest.Active == nil {
		return nil
	}
	run := cluster.Status.Backup.RestoreTest.Active
	observed, err := readRestoreTestChild(ctx, reader, cluster)
	if err != nil {
		return err
	}
	if observed.reason != "" {
		if released, err := releaseRestoreTestReservation(ctx, mutate, cluster); released || err != nil {
			return err
		}
		return fmt.Errorf("%s", observed.message)
	}
	child := observed.request
	if child == nil {
		return nil
	}
	if run.UID == "" {
		// Persist the child identity before cancellation. A later NotFound can
		// then release deletion without forgetting an uncertain creation attempt.
		if err := mutate(ctx, cluster, func(current *api.OpenBaoCluster) error {
			if current.UID != cluster.UID || current.Status.Backup == nil || current.Status.Backup.RestoreTest == nil ||
				!equality.Semantic.DeepEqual(current.Status.Backup.RestoreTest.Active, run) {
				return fmt.Errorf("restore test reservation changed before cancellation")
			}
			current.Status.Backup.RestoreTest.Active.UID = child.UID
			return nil
		}, adminops.ForceOwnershipOnConflict); err != nil {
			return err
		}
	}
	if child.DeletionTimestamp == nil {
		if err := c.Delete(ctx, child, client.Preconditions{UID: &child.UID}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return fmt.Errorf("waiting for disposable restore test cleanup before source deletion")
}

// Call only after observing an uncertain, unbound child. The administrator
// accepts responsibility for any delayed create and remaining destination resources.
func releaseRestoreTestReservation(ctx context.Context, mutate adminops.StatusMutator, cluster *api.OpenBaoCluster) (bool, error) {
	run := cluster.Status.Backup.RestoreTest.Active
	acknowledgement := run.Name + "/Release"
	if run.UID != "" || cluster.Annotations[constants.AnnotationRestoreTestAcknowledge] != acknowledgement {
		return false, nil
	}
	if mutate == nil {
		return false, fmt.Errorf("adminops status mutator is required")
	}
	err := mutate(ctx, cluster, func(current *api.OpenBaoCluster) error {
		if current.UID != cluster.UID || current.Annotations[constants.AnnotationRestoreTestAcknowledge] != acknowledgement ||
			current.Status.Backup == nil || current.Status.Backup.RestoreTest == nil ||
			!equality.Semantic.DeepEqual(current.Status.Backup.RestoreTest.Active, run) {
			return fmt.Errorf("restore test reservation or acknowledgement changed before release")
		}
		status := current.Status.Backup.RestoreTest
		status.Last = failedRestoreTestResult(run, metav1.Now().Time, reasonRestoreTestAdministratorReleased,
			"Administrator released the uncertain reservation and accepted responsibility for destination resources")
		status.Active = nil
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type: api.RestoreTestPassedConditionType, Status: metav1.ConditionFalse, Reason: reasonRestoreTestAdministratorReleased,
			Message:            "Administrator released the uncertain reservation and accepted responsibility for destination resources",
			ObservedGeneration: current.Generation,
		})
		return nil
	}, adminops.ForceOwnershipOnConflict)
	return err == nil, err
}
