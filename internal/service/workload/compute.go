package workload

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

// ErrStatefulSetPrerequisitesMissing indicates that required prerequisites (ConfigMap or TLS Secret)
// are missing and the StatefulSet cannot be created. Callers can use this error to set a condition
// and requeue instead of failing reconciliation.
var ErrStatefulSetPrerequisitesMissing = errors.New("StatefulSet prerequisites missing")

// ErrAuditFileStorageStatefulSetRequiresRecreate indicates that the existing StatefulSet
// needs an audit storage pod-template change that is blocked by immutable or admission-locked fields.
var ErrAuditFileStorageStatefulSetRequiresRecreate = errors.New("audit file storage requires StatefulSet recreation")

// ErrAzureWorkloadIdentityStatefulSetRequiresRecreate indicates that adding or removing the
// Azure Key Vault workload identity needs a token volume change that is blocked by
// admission-locked pod-template fields of the existing StatefulSet.
var ErrAzureWorkloadIdentityStatefulSetRequiresRecreate = errors.New("azure workload identity requires StatefulSet recreation")

// checkStatefulSetPrerequisites verifies that all required resources exist before creating or updating the StatefulSet.
// This prevents pods from failing to start due to missing ConfigMaps or Secrets.
// Returns ErrStatefulSetPrerequisitesMissing if prerequisites are not found (callers should handle this
// by setting a condition and requeuing). Returns other errors for unexpected failures.
func (m *Manager) checkStatefulSetPrerequisites(ctx context.Context, cluster *openbaov1alpha1.OpenBaoCluster, spec StatefulSetSpec) error {
	// Always check for the config ConfigMap
	configMapName := configMapNameForSpec(cluster, spec)
	configMap := &corev1.ConfigMap{}
	if err := m.client.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      configMapName,
	}, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: config ConfigMap %s/%s not found; cannot create StatefulSet", ErrStatefulSetPrerequisitesMissing, cluster.Namespace, configMapName)
		}
		return fmt.Errorf("failed to get config ConfigMap %s/%s: %w", cluster.Namespace, configMapName, err)
	}

	// Check for TLS secret if TLS is enabled and not in ACME mode
	// In ACME mode, OpenBao manages certificates internally, so no secret is needed
	if cluster.Spec.TLS.Enabled && !usesACMEMode(cluster) {
		tlsSecretName := resourceidentity.TLSServerSecretName(cluster)
		tlsSecret := &corev1.Secret{}
		if err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      tlsSecretName,
		}, tlsSecret); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: TLS server Secret %s/%s not found; cannot create StatefulSet (waiting for TLS reconciliation or external provider)", ErrStatefulSetPrerequisitesMissing, cluster.Namespace, tlsSecretName)
			}
			return fmt.Errorf("failed to get TLS server Secret %s/%s: %w", cluster.Namespace, tlsSecretName, err)
		}
	}

	if claimName := portopenbao.ACMESharedCacheClaimName(cluster); claimName != "" {
		cachePVC := &corev1.PersistentVolumeClaim{}
		if err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      claimName,
		}, cachePVC); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: ACME shared cache PVC %s/%s not found; cannot create StatefulSet", ErrStatefulSetPrerequisitesMissing, cluster.Namespace, claimName)
			}
			return fmt.Errorf("failed to get ACME shared cache PVC %s/%s: %w", cluster.Namespace, claimName, err)
		}
	}

	if claimName := portopenbao.AuditFileStorageClaimName(cluster); claimName != "" {
		auditPVC := &corev1.PersistentVolumeClaim{}
		if err := m.client.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      claimName,
		}, auditPVC); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: audit file storage PVC %s/%s not found; cannot create StatefulSet", ErrStatefulSetPrerequisitesMissing, cluster.Namespace, claimName)
			}
			return fmt.Errorf("failed to get audit file storage PVC %s/%s: %w", cluster.Namespace, claimName, err)
		}
	}

	return nil
}

// EnsureStatefulSetWithRevision manages the StatefulSet for the OpenBaoCluster using Server-Side Apply.
// This is exported for use by BlueGreenManager.
// verifiedImageDigest is the verified image digest to use (if provided, overrides cluster.Spec.Image).
// verifiedInitContainerDigest is the resolved init container image to use.
// When operator image verification is enabled, this should be a digest.
// revision is an optional revision identifier for blue/green deployments (e.g., "blue-v1hash" or "green-v2hash").
// disableSelfInit prevents the pod from attempting to initialize itself (used for Green pods that must join).
// If revision is empty, uses the cluster name (backward compatible behavior).
//
// Note: UpdateStrategy is intentionally not set here to allow UpgradeManager to manage it.
// SSA will preserve fields not specified in the desired object.
func (m *Manager) EnsureStatefulSetWithRevision(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, configContent string, verifiedImageDigest string, verifiedInitContainerDigest string, revision string, disableSelfInit bool) error {
	spec := StatefulSetSpec{
		Name:               statefulSetNameWithRevision(cluster, revision),
		Pool:               constants.LabelValueOpenBaoWorkloadPoolVoter,
		Revision:           revision,
		Image:              verifiedImageDigest,
		InitContainerImage: verifiedInitContainerDigest,
		Replicas:           cluster.Spec.Replicas,
		DisableSelfInit:    disableSelfInit,
	}
	return m.EnsureStatefulSet(ctx, logger, cluster, configContent, spec)
}

func (m *Manager) EnsureStatefulSet(ctx context.Context, logger logr.Logger, cluster *openbaov1alpha1.OpenBaoCluster, configContent string, spec StatefulSetSpec) error {
	if spec.Pool == "" {
		spec.Pool = constants.LabelValueOpenBaoWorkloadPoolVoter
	}
	name := statefulSetNameForSpec(cluster, spec)

	existingSTS := &appsv1.StatefulSet{}
	existingFound := true
	if err := m.client.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, existingSTS); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get StatefulSet %s/%s: %w", cluster.Namespace, name, err)
		}
		existingFound = false
	}

	// Checked before the ConfigMap is written: a Pod restarted with a workload identity
	// configuration but without the token, or the reverse, could not unseal.
	if existingFound {
		if err := m.GuardAzureWorkloadIdentityStatefulSet(ctx, logger, cluster, existingSTS, spec); err != nil {
			return err
		}
	}

	if err := m.ensureConfigMapWithName(ctx, cluster, configMapNameForSpec(cluster, spec), configContent); err != nil {
		return fmt.Errorf("failed to ensure config ConfigMap for StatefulSet %s/%s: %w", cluster.Namespace, name, err)
	}

	// Before creating/updating the StatefulSet, verify all prerequisites exist
	// This is important for External TLS mode where secrets might be deleted/recreated
	if err := m.checkStatefulSetPrerequisites(ctx, cluster, spec); err != nil {
		return err
	}

	staticUnsealSecret, err := m.staticUnsealSecretName(ctx, logger, cluster)
	if err != nil {
		return err
	}
	spec.staticUnsealSecret = staticUnsealSecret

	initialized := cluster.Status.Initialized
	desiredReplicas := desiredStatefulSetReplicas(cluster, initialized, spec)

	if !initialized {
		logger.Info("Cluster not yet initialized; applying staged replica count", "statefulset", name, "desiredReplicas", desiredReplicas, "pool", spec.Pool)
	} else {
		logger.V(1).Info("Cluster initialized; ensuring StatefulSet has desired replicas", "statefulset", name, "desiredReplicas", desiredReplicas, "pool", spec.Pool)
	}

	desired, buildErr := buildStatefulSetForSpec(cluster, configContent, initialized, spec, m.platform)
	if buildErr != nil {
		return fmt.Errorf("failed to build StatefulSet for OpenBaoCluster %s/%s: %w", cluster.Namespace, cluster.Name, buildErr)
	}

	// StatefulSet.spec.volumeClaimTemplates are effectively immutable once the StatefulSet exists.
	// Keep them identical to the existing object; storage expansion is reconciled by patching the PVCs directly.
	if existingFound {
		desired.Spec.VolumeClaimTemplates = existingSTS.Spec.VolumeClaimTemplates

		if statefulSetAuditFileStorageRequiresRecreate(desired, existingSTS) {
			return fmt.Errorf("%w: StatefulSet %s/%s does not include the requested audit file storage volume and mount; recreate the StatefulSet or create a new workload revision so the locked pod-template fields can be applied", ErrAuditFileStorageStatefulSetRequiresRecreate, cluster.Namespace, name)
		}

		// POLICY COMPLIANCE: The operator ships an ValidatingAdmissionPolicy
		// (e.g. openbao-operator-openbao-lock-controller-statefulset-mutations) that forbid
		// the OpenBao controller from mutating specific StatefulSet pod-template fields
		// (volumes/volumeMounts/securityContext/command/args/automountServiceAccountToken).
		//
		// SSA would otherwise attempt to apply our desired values and can be denied even
		// when we only intend to upgrade images. Preserve the locked fields from the
		// existing StatefulSet so the UPDATE passes admission.
		preserveLockedStatefulSetTemplateFields(desired, existingSTS)
		if cluster.Status.Initialized || cluster.Status.SelfInitialized {
			removeSelfInitSecretInputs(&desired.Spec.Template.Spec)
		}

		// Apply the workload-owned strategy change before the template. A failed
		// partition lock must leave the existing template in place.
		if err := m.reconcileStatefulSetUpdateStrategy(ctx, logger, cluster, spec.Pool, existingSTS); err != nil {
			return err
		}
	}

	// Set the desired replica count (SSA will handle create/update)
	desired.Spec.Replicas = int32Ptr(desiredReplicas)

	// Set TypeMeta for SSA
	desired.TypeMeta = metav1.TypeMeta{
		Kind:       "StatefulSet",
		APIVersion: "apps/v1",
	}

	// Note: RollingUpdate.Partition is intentionally not set via SSA here. The AdminOps
	// rolling upgrade manager patches partition via a strategic merge patch to orchestrate rollouts.

	if err := m.applyResource(ctx, desired, cluster); err != nil {
		return fmt.Errorf("failed to ensure StatefulSet %s/%s: %w", cluster.Namespace, name, err)
	}

	if err := m.reconcileMaintenanceAnnotationsForPods(ctx, logger, cluster, spec); err != nil {
		return fmt.Errorf("failed to reconcile maintenance annotations for OpenBaoCluster %s/%s pods: %w", cluster.Namespace, cluster.Name, err)
	}

	return nil
}

func preserveLockedStatefulSetTemplateFields(desired *appsv1.StatefulSet, existing *appsv1.StatefulSet) {
	if desired == nil || existing == nil {
		return
	}

	// Pod-level locked fields
	desired.Spec.Template.Spec.Volumes = existing.Spec.Template.Spec.Volumes
	desired.Spec.Template.Spec.AutomountServiceAccountToken = existing.Spec.Template.Spec.AutomountServiceAccountToken
	desired.Spec.Template.Spec.SecurityContext = existing.Spec.Template.Spec.SecurityContext

	// Container locked fields (order-sensitive in admission policy expressions)
	if len(existing.Spec.Template.Spec.Containers) > 0 {
		desiredByName := make(map[string]corev1.Container, len(desired.Spec.Template.Spec.Containers))
		for _, c := range desired.Spec.Template.Spec.Containers {
			desiredByName[c.Name] = c
		}

		newContainers := make([]corev1.Container, 0, len(existing.Spec.Template.Spec.Containers))
		for _, existingC := range existing.Spec.Template.Spec.Containers {
			if desiredC, ok := desiredByName[existingC.Name]; ok {
				desiredC.Command = existingC.Command
				desiredC.Args = existingC.Args
				desiredC.SecurityContext = existingC.SecurityContext
				desiredC.VolumeMounts = existingC.VolumeMounts
				newContainers = append(newContainers, desiredC)
			} else {
				// Do not add/remove containers under a locked policy; keep existing entry.
				newContainers = append(newContainers, existingC)
			}
		}
		desired.Spec.Template.Spec.Containers = newContainers
	}

	// Init container locked fields (order-sensitive in admission policy expressions)
	existingInit := existing.Spec.Template.Spec.InitContainers
	if existingInit != nil {
		desiredByName := make(map[string]corev1.Container, len(desired.Spec.Template.Spec.InitContainers))
		for _, c := range desired.Spec.Template.Spec.InitContainers {
			desiredByName[c.Name] = c
		}

		newInit := make([]corev1.Container, 0, len(existingInit))
		for _, existingC := range existingInit {
			if desiredC, ok := desiredByName[existingC.Name]; ok {
				desiredC.Command = existingC.Command
				desiredC.Args = existingC.Args
				desiredC.VolumeMounts = existingC.VolumeMounts
				desiredC.SecurityContext = existingC.SecurityContext
				newInit = append(newInit, desiredC)
			} else {
				newInit = append(newInit, existingC)
			}
		}
		desired.Spec.Template.Spec.InitContainers = newInit
	}
}

func statefulSetAuditFileStorageRequiresRecreate(desired *appsv1.StatefulSet, existing *appsv1.StatefulSet) bool {
	desiredVolume, ok := statefulSetVolume(desired, auditFileStorageVolumeName)
	if !ok {
		return false
	}
	existingVolume, ok := statefulSetVolume(existing, auditFileStorageVolumeName)
	if !ok || !samePVCVolumeSource(desiredVolume, existingVolume) {
		return true
	}

	desiredMount, ok := statefulSetContainerVolumeMount(desired, constants.ContainerBao, auditFileStorageVolumeName)
	if !ok {
		return false
	}
	existingMount, ok := statefulSetContainerVolumeMount(existing, constants.ContainerBao, auditFileStorageVolumeName)
	if !ok {
		return true
	}
	return existingMount.MountPath != desiredMount.MountPath ||
		existingMount.SubPath != desiredMount.SubPath ||
		existingMount.SubPathExpr != desiredMount.SubPathExpr ||
		existingMount.ReadOnly != desiredMount.ReadOnly
}

// StatefulSetAzureWorkloadIdentityRequiresRecreate reports whether an existing StatefulSet's
// Azure workload identity token volume or mount disagrees with the cluster spec. Both live in
// admission-locked pod-template fields, which the operator preserves from the existing object.
func StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster *openbaov1alpha1.OpenBaoCluster, existing *appsv1.StatefulSet) bool {
	if cluster == nil || existing == nil {
		return false
	}
	provider := newSealWiringProvider(cluster)
	existingVolume, hasVolume := statefulSetVolume(existing, constants.VolumeAzureIdentityToken)
	existingMount, hasMount := statefulSetContainerVolumeMount(existing, constants.ContainerBao, constants.VolumeAzureIdentityToken)
	desiredVolume, wantVolume := findVolume(provider.Volumes(), constants.VolumeAzureIdentityToken)
	desiredMount, wantMount := findVolumeMount(provider.VolumeMounts(), constants.VolumeAzureIdentityToken)
	if hasVolume != wantVolume || hasMount != wantMount {
		return true
	}
	if !wantVolume {
		return false
	}
	return !apiequality.Semantic.DeepEqual(
		defaultedProjectedVolumeSource(existingVolume.VolumeSource),
		defaultedProjectedVolumeSource(desiredVolume.VolumeSource),
	) ||
		existingMount.MountPath != desiredMount.MountPath ||
		existingMount.SubPath != desiredMount.SubPath ||
		existingMount.SubPathExpr != desiredMount.SubPathExpr ||
		existingMount.ReadOnly != desiredMount.ReadOnly
}

// defaultedProjectedVolumeSource applies the API server defaults for projected volumes, which
// the rendered volume leaves unset, so a StatefulSet read back from the API server compares
// equal to the one the operator renders.
func defaultedProjectedVolumeSource(source corev1.VolumeSource) corev1.VolumeSource {
	if source.Projected == nil {
		return source
	}
	source = *source.DeepCopy()
	if source.Projected.DefaultMode == nil {
		source.Projected.DefaultMode = ptr.To(corev1.ProjectedVolumeSourceDefaultMode)
	}
	for i := range source.Projected.Sources {
		token := source.Projected.Sources[i].ServiceAccountToken
		if token != nil && token.ExpirationSeconds == nil {
			token.ExpirationSeconds = ptr.To(int64(3600))
		}
	}
	return source
}

// CheckAzureWorkloadIdentityStatefulSet returns ErrAzureWorkloadIdentityStatefulSetRequiresRecreate
// when an existing StatefulSet's token projection disagrees with the cluster spec. Callers must
// check it before they write the seal configuration the StatefulSet's Pods read.
func CheckAzureWorkloadIdentityStatefulSet(cluster *openbaov1alpha1.OpenBaoCluster, existing *appsv1.StatefulSet) error {
	if !StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster, existing) {
		return nil
	}
	return fmt.Errorf("%w: StatefulSet %s/%s does not match the requested Azure Key Vault workload identity token projection; recreate the StatefulSet so the locked pod-template fields can be applied", ErrAzureWorkloadIdentityStatefulSetRequiresRecreate, existing.Namespace, existing.Name)
}

func statefulSetVolume(sts *appsv1.StatefulSet, name string) (corev1.Volume, bool) {
	if sts == nil {
		return corev1.Volume{}, false
	}
	return findVolume(sts.Spec.Template.Spec.Volumes, name)
}

func findVolume(volumes []corev1.Volume, name string) (corev1.Volume, bool) {
	for _, volume := range volumes {
		if volume.Name == name {
			return volume, true
		}
	}
	return corev1.Volume{}, false
}

func findVolumeMount(mounts []corev1.VolumeMount, name string) (corev1.VolumeMount, bool) {
	for _, mount := range mounts {
		if mount.Name == name {
			return mount, true
		}
	}
	return corev1.VolumeMount{}, false
}

func samePVCVolumeSource(a corev1.Volume, b corev1.Volume) bool {
	if a.PersistentVolumeClaim == nil || b.PersistentVolumeClaim == nil {
		return a.PersistentVolumeClaim == nil && b.PersistentVolumeClaim == nil
	}
	return a.PersistentVolumeClaim.ClaimName == b.PersistentVolumeClaim.ClaimName &&
		a.PersistentVolumeClaim.ReadOnly == b.PersistentVolumeClaim.ReadOnly
}

func statefulSetContainerVolumeMount(sts *appsv1.StatefulSet, containerName string, volumeName string) (corev1.VolumeMount, bool) {
	if sts == nil {
		return corev1.VolumeMount{}, false
	}
	for _, container := range sts.Spec.Template.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		return findVolumeMount(container.VolumeMounts, volumeName)
	}
	return corev1.VolumeMount{}, false
}
