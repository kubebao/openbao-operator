package observability

import api "github.com/kubebao/openbao-operator/api/v1alpha1"

// ObserveClusterRestoreState updates one target cluster's gauge from its cached
// requests. An unresolved execution takes precedence; otherwise the newest
// request wins. An empty selection removes only this target's series.
func ObserveClusterRestoreState(namespace, cluster string, requests []api.OpenBaoRestore) {
	var selected *api.OpenBaoRestore
	for i := range requests {
		request := &requests[i]
		if request.Namespace != namespace || request.Spec.Cluster != cluster || cluster == "" {
			continue
		}
		if selected == nil || preferRestoreState(request, selected) {
			selected = request
		}
	}
	if selected == nil {
		restoreStateGauge.DeleteLabelValues(namespace, cluster)
		return
	}
	NewRestoreMetrics(namespace, cluster).setState(observedRestoreState(selected))
}

// RestoreStateChanged reports whether an update affects the target, selection
// priority, or published state. Other status and metadata updates need no refresh.
func RestoreStateChanged(previous, current *api.OpenBaoRestore) bool {
	return previous.Namespace != current.Namespace || previous.Name != current.Name ||
		previous.Spec.Cluster != current.Spec.Cluster ||
		!previous.CreationTimestamp.Equal(&current.CreationTimestamp) ||
		unresolvedRestore(previous) != unresolvedRestore(current) ||
		observedRestoreState(previous) != observedRestoreState(current)
}

func preferRestoreState(request, previous *api.OpenBaoRestore) bool {
	requestHeld := unresolvedRestore(request)
	previousHeld := unresolvedRestore(previous)
	if requestHeld != previousHeld {
		return requestHeld
	}
	if request.CreationTimestamp.Equal(&previous.CreationTimestamp) {
		return request.Name > previous.Name
	}
	return request.CreationTimestamp.After(previous.CreationTimestamp.Time)
}

func unresolvedRestore(request *api.OpenBaoRestore) bool {
	return request.Status.AdministratorDisposition == "" &&
		(request.Status.Phase == api.RestorePhaseUnknown ||
			(request.Status.Target != nil && request.Status.Target.Cleanup == api.RestoreTargetCleanupFailed))
}

func observedRestoreState(request *api.OpenBaoRestore) float64 {
	if request.Status.AdministratorDisposition == api.RestoreAdministratorAbandon {
		return 7
	}
	if request.Status.Phase == api.RestorePhaseCompleted {
		return 2
	}
	if request.Status.AdministratorDisposition == api.RestoreAdministratorResume {
		return 6
	}
	if request.Status.Restart != nil {
		return 5
	}
	if unresolvedRestore(request) {
		return 4
	}
	if request.Status.Phase == api.RestorePhaseFailed {
		return 3
	}
	return 1
}
