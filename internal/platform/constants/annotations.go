package constants

// Annotation keys used by the operator.
const (
	// AnnotationTriggerBackup is the annotation key used to trigger an immediate manual backup.
	AnnotationTriggerBackup = "openbao.org/trigger-backup"
	// AnnotationConfigHash is the annotation key used to track ConfigMap/Secret changes.
	AnnotationConfigHash = "openbao.org/config-hash"
	// AnnotationClusterGeneration records the OpenBaoCluster generation used to render a StatefulSet.
	AnnotationClusterGeneration = "openbao.org/cluster-generation"
	// AnnotationResolvedMainImage records the main image published with the StatefulSet template.
	AnnotationResolvedMainImage = "openbao.org/resolved-main-image"
	// AnnotationMaintenance is the annotation key used to put a cluster into maintenance mode.
	AnnotationMaintenance = "openbao.org/maintenance"
	// AnnotationMaintenanceAllowed is the annotation key used to check if maintenance is allowed.
	AnnotationMaintenanceAllowed = "openbao.org/maintenance-allowed"
	// AnnotationRestartAt is the annotation key used to trigger a rolling restart via Pod template updates.
	AnnotationRestartAt = "openbao.org/restart-at"

	// AnnotationRestoreRevision preserves the Pod template value written by legacy restores.
	AnnotationRestoreRevision = "openbao.org/restore-revision"
	// AnnotationOpenBaoOwnerUID ties retained operator-managed resources to the owning OpenBaoCluster UID.
	AnnotationOpenBaoOwnerUID = "openbao.org/owner-uid"
)

// AnnotationRestoreOrigin binds restricted fresh-target workloads to a restore request.
const AnnotationRestoreOrigin = "openbao.org/restore-origin"

// AnnotationRestoreTestSource binds a scheduled restore to the source cluster UID.
const AnnotationRestoreTestSource = "openbao.org/restore-test-source"
