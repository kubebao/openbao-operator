package workload

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

// ErrVoterRolloutWaiting means the current partition must remain in place.
var ErrVoterRolloutWaiting = errors.New("waiting for Raft voter rollout safety")

// ErrVoterTemplateRolloutPending defers a new template until the current rollout finishes.
var ErrVoterTemplateRolloutPending = errors.New("current voter rollout must finish before changing its template")

// VoterReplicaCount returns the published voter count. A pending scale request
// must not reduce the safety checks for voters that still exist.
func VoterReplicaCount(sts *appsv1.StatefulSet) (int32, error) {
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas < 1 {
		return 0, fmt.Errorf("%w: StatefulSet voter count is unavailable", ErrVoterRolloutWaiting)
	}
	return *sts.Spec.Replicas, nil
}

// VoterRolloutGuard checks current Pod identities and authenticated Raft catch-up
// before a voter disruption or after a replacement. replacing excludes only target.
type VoterRolloutGuard interface {
	CheckVoterRollout(context.Context, logr.Logger, *openbaov1alpha1.OpenBaoCluster, *appsv1.StatefulSet, string, bool) error
}
