package main

import "sigs.k8s.io/controller-runtime/pkg/client"

// Fault seams for interruption qualification. Production leaves them as
// no-ops. The build-tagged end-to-end test sets them to stop a command between
// two writes of one step, or to lose the response to a write the API server
// accepted, and then checks that running the command again converges.
var (
	// afterStatefulSetOrphaned runs between a StatefulSet's orphan delete and
	// its recreation.
	afterStatefulSetOrphaned = func() error { return nil }
	// afterOwnershipMoved runs after each managed resource changes owner.
	afterOwnershipMoved = func() error { return nil }
	// afterSourceRetired runs after commit deletes each source Cluster.
	afterSourceRetired = func() error { return nil }
	// wrapWriter wraps the administrator's and the migrator's clients.
	wrapWriter = func(c client.Client) client.Client { return c }
)
