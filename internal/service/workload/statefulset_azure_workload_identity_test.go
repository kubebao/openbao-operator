package workload

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
)

func azureKeyVaultCluster(name string, workloadIdentity bool) *openbaov1alpha1.OpenBaoCluster {
	cluster := newMinimalCluster(name, "default")
	cluster.Spec.Version = "2.6.0"
	cluster.Spec.Image = "openbao/openbao:2.6.0"
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: portopenbao.SealTypeAzureKeyVault,
		AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
			VaultName: "vault",
			KeyName:   "key",
			TenantID:  "tenant-123",
		},
	}
	if workloadIdentity {
		cluster.Spec.Unseal.AzureKeyVault.WorkloadIdentity = &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"}
	}
	return cluster
}

func TestStatefulSetAzureWorkloadIdentityRequiresRecreate(t *testing.T) {
	withIdentity, err := buildStatefulSet(azureKeyVaultCluster("azure", true), "test-config", true, "", "", "")
	if err != nil {
		t.Fatalf("buildStatefulSet() error = %v", err)
	}
	withoutIdentity, err := buildStatefulSet(azureKeyVaultCluster("azure", false), "test-config", true, "", "", "")
	if err != nil {
		t.Fatalf("buildStatefulSet() error = %v", err)
	}
	volumeOnly := withIdentity.DeepCopy()
	for i := range volumeOnly.Spec.Template.Spec.Containers {
		mounts := volumeOnly.Spec.Template.Spec.Containers[i].VolumeMounts[:0]
		for _, mount := range volumeOnly.Spec.Template.Spec.Containers[i].VolumeMounts {
			if mount.Name != constants.VolumeAzureIdentityToken {
				mounts = append(mounts, mount)
			}
		}
		volumeOnly.Spec.Template.Spec.Containers[i].VolumeMounts = mounts
	}
	withTokenProjection := func(mutate func(*corev1.Volume, *corev1.VolumeMount)) *appsv1.StatefulSet {
		sts := withIdentity.DeepCopy()
		podSpec := &sts.Spec.Template.Spec
		for i := range podSpec.Volumes {
			if podSpec.Volumes[i].Name != constants.VolumeAzureIdentityToken {
				continue
			}
			for j := range podSpec.Containers {
				if podSpec.Containers[j].Name != constants.ContainerBao {
					continue
				}
				for k := range podSpec.Containers[j].VolumeMounts {
					if podSpec.Containers[j].VolumeMounts[k].Name == constants.VolumeAzureIdentityToken {
						mutate(&podSpec.Volumes[i], &podSpec.Containers[j].VolumeMounts[k])
					}
				}
			}
		}
		return sts
	}
	// The API server defaults the projected volume mode, which the rendered volume leaves unset.
	apiServerDefaulted := withTokenProjection(func(volume *corev1.Volume, _ *corev1.VolumeMount) {
		volume.Projected.DefaultMode = ptr.To(corev1.ProjectedVolumeSourceDefaultMode)
	})
	otherAudience := withTokenProjection(func(volume *corev1.Volume, _ *corev1.VolumeMount) {
		volume.Projected.Sources[0].ServiceAccountToken.Audience = "api://other"
	})
	otherPath := withTokenProjection(func(volume *corev1.Volume, _ *corev1.VolumeMount) {
		volume.Projected.Sources[0].ServiceAccountToken.Path = "other-token"
	})
	otherSource := withTokenProjection(func(volume *corev1.Volume, _ *corev1.VolumeMount) {
		volume.VolumeSource = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	})
	otherMountPath := withTokenProjection(func(_ *corev1.Volume, mount *corev1.VolumeMount) {
		mount.MountPath = "/var/run/secrets/other"
	})
	staticCluster := newMinimalCluster("azure", "default")

	tests := []struct {
		name     string
		cluster  *openbaov1alpha1.OpenBaoCluster
		existing *appsv1.StatefulSet
		want     bool
	}{
		{name: "identity unchanged", cluster: azureKeyVaultCluster("azure", true), existing: withIdentity},
		{name: "no identity unchanged", cluster: azureKeyVaultCluster("azure", false), existing: withoutIdentity},
		{name: "identity added", cluster: azureKeyVaultCluster("azure", true), existing: withoutIdentity, want: true},
		{name: "identity removed", cluster: azureKeyVaultCluster("azure", false), existing: withIdentity, want: true},
		{name: "seal type changed away from Azure", cluster: staticCluster, existing: withIdentity, want: true},
		{name: "token volume without its mount", cluster: azureKeyVaultCluster("azure", true), existing: volumeOnly, want: true},
		{name: "API server defaults", cluster: azureKeyVaultCluster("azure", true), existing: apiServerDefaulted},
		{name: "token audience changed", cluster: azureKeyVaultCluster("azure", true), existing: otherAudience, want: true},
		{name: "token path changed", cluster: azureKeyVaultCluster("azure", true), existing: otherPath, want: true},
		{name: "token volume source changed", cluster: azureKeyVaultCluster("azure", true), existing: otherSource, want: true},
		{name: "token mount path changed", cluster: azureKeyVaultCluster("azure", true), existing: otherMountPath, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatefulSetAzureWorkloadIdentityRequiresRecreate(tt.cluster, tt.existing); got != tt.want {
				t.Fatalf("StatefulSetAzureWorkloadIdentityRequiresRecreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Volumes and mounts are locked on an existing StatefulSet, so adding or removing the
// workload identity in place cannot reach the Pods. The operator must stop before it
// renders the new seal configuration or changes the StatefulSet.
func TestEnsureStatefulSet_AzureWorkloadIdentityTransitionRequiresRecreate(t *testing.T) {
	tests := []struct {
		name             string
		existingIdentity bool
		desiredIdentity  bool
	}{
		{name: "add to existing StatefulSet", desiredIdentity: true},
		{name: "remove from existing StatefulSet", existingIdentity: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cluster := azureKeyVaultCluster("azure-transition", tt.desiredIdentity)
			spec := StatefulSetSpec{
				Name:     cluster.Name,
				Pool:     constants.LabelValueOpenBaoWorkloadPoolVoter,
				Replicas: cluster.Spec.Replicas,
			}

			existing, err := buildStatefulSetForSpec(azureKeyVaultCluster(cluster.Name, tt.existingIdentity), "previous-config", true, spec, "")
			if err != nil {
				t.Fatalf("buildStatefulSetForSpec() error = %v", err)
			}
			existing.OwnerReferences = []metav1.OwnerReference{*workloadOwnershipRef(cluster)}
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            configMapNameForSpec(cluster, spec),
					Namespace:       cluster.Namespace,
					OwnerReferences: []metav1.OwnerReference{*workloadOwnershipRef(cluster)},
				},
				Data: map[string]string{configFileName: "previous-config"},
			}
			tlsSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceidentity.TLSServerSecretName(cluster), Namespace: cluster.Namespace},
			}
			mgr := workloadOwnershipManager(cluster, existing, configMap, tlsSecret)

			err = mgr.EnsureStatefulSet(ctx, logr.Discard(), cluster, "desired-config", spec)
			if !errors.Is(err, ErrAzureWorkloadIdentityStatefulSetRequiresRecreate) {
				t.Fatalf("EnsureStatefulSet() error = %v, want %v", err, ErrAzureWorkloadIdentityStatefulSetRequiresRecreate)
			}

			storedConfigMap := &corev1.ConfigMap{}
			if err := mgr.client.Get(ctx, client.ObjectKeyFromObject(configMap), storedConfigMap); err != nil {
				t.Fatalf("get ConfigMap: %v", err)
			}
			if got := storedConfigMap.Data[configFileName]; got != "previous-config" {
				t.Fatalf("ConfigMap config = %q, want the previous configuration kept", got)
			}

			stored := &appsv1.StatefulSet{}
			if err := mgr.client.Get(ctx, client.ObjectKeyFromObject(existing), stored); err != nil {
				t.Fatalf("get StatefulSet: %v", err)
			}
			if got := hasVolume(stored.Spec.Template.Spec.Volumes, constants.VolumeAzureIdentityToken); got != tt.existingIdentity {
				t.Fatalf("StatefulSet token volume present = %v, want it unchanged (%v)", got, tt.existingIdentity)
			}
			if got := hasEnvVar(stored.Spec.Template.Spec.Containers[0].Env, envAzureFederatedTokenFile); got != tt.existingIdentity {
				t.Fatalf("StatefulSet %s present = %v, want it unchanged (%v)", envAzureFederatedTokenFile, got, tt.existingIdentity)
			}
		})
	}
}

// A new StatefulSet is built whole, so a fresh workload, or one recreated after the
// transition was reported, gets the token projection.
func TestEnsureStatefulSet_AzureWorkloadIdentityCreatesTokenProjection(t *testing.T) {
	ctx := context.Background()
	cluster := azureKeyVaultCluster("azure-fresh", true)
	spec := StatefulSetSpec{
		Name:     cluster.Name,
		Pool:     constants.LabelValueOpenBaoWorkloadPoolVoter,
		Replicas: cluster.Spec.Replicas,
	}
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: resourceidentity.TLSServerSecretName(cluster), Namespace: cluster.Namespace},
	}
	mgr := workloadOwnershipManager(cluster, tlsSecret)

	if err := mgr.EnsureStatefulSet(ctx, logr.Discard(), cluster, "desired-config", spec); err != nil {
		t.Fatalf("EnsureStatefulSet() error = %v", err)
	}

	stored := &appsv1.StatefulSet{}
	if err := mgr.client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: spec.Name}, stored); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}
	if !hasVolume(stored.Spec.Template.Spec.Volumes, constants.VolumeAzureIdentityToken) {
		t.Fatalf("expected the new StatefulSet to project the token")
	}
	if StatefulSetAzureWorkloadIdentityRequiresRecreate(cluster, stored) {
		t.Fatalf("expected the new StatefulSet to match the workload identity")
	}
}

// The documented recreation deletes the held StatefulSet in maintenance mode, which admission
// only allows once the maintenance annotation is on it.
func TestGuardAzureWorkloadIdentityStatefulSet_KeepsMaintenanceAnnotation(t *testing.T) {
	ctx := context.Background()
	cluster := azureKeyVaultCluster("azure-maintenance", true)
	cluster.Spec.Maintenance = &openbaov1alpha1.MaintenanceConfig{Enabled: true}
	spec := StatefulSetSpec{
		Name:     cluster.Name,
		Pool:     constants.LabelValueOpenBaoWorkloadPoolVoter,
		Replicas: cluster.Spec.Replicas,
	}

	existing, err := buildStatefulSetForSpec(azureKeyVaultCluster(cluster.Name, false), "previous-config", true, spec, "")
	if err != nil {
		t.Fatalf("buildStatefulSetForSpec() error = %v", err)
	}
	existing.OwnerReferences = []metav1.OwnerReference{*workloadOwnershipRef(cluster)}
	// Admission allows the deletion only with the value "true", so any other value is corrected.
	existing.Annotations = map[string]string{constants.AnnotationMaintenance: "false"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-0",
			Namespace: cluster.Namespace,
			Labels:    resourceidentity.PodSelectorLabelsForPoolWithRevision(cluster, spec.Pool, spec.Revision),
		},
	}
	mgr := workloadOwnershipManager(cluster, existing, pod)

	err = mgr.GuardAzureWorkloadIdentityStatefulSet(ctx, logr.Discard(), cluster, existing.DeepCopy(), spec)
	if !errors.Is(err, ErrAzureWorkloadIdentityStatefulSetRequiresRecreate) {
		t.Fatalf("GuardAzureWorkloadIdentityStatefulSet() error = %v, want %v", err, ErrAzureWorkloadIdentityStatefulSetRequiresRecreate)
	}
	assertMaintenanceAnnotation(t, mgr, existing, pod, true)

	cluster.Spec.Maintenance.Enabled = false
	stored := &appsv1.StatefulSet{}
	if err := mgr.client.Get(ctx, client.ObjectKeyFromObject(existing), stored); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}
	err = mgr.GuardAzureWorkloadIdentityStatefulSet(ctx, logr.Discard(), cluster, stored, spec)
	if !errors.Is(err, ErrAzureWorkloadIdentityStatefulSetRequiresRecreate) {
		t.Fatalf("GuardAzureWorkloadIdentityStatefulSet() error = %v, want %v", err, ErrAzureWorkloadIdentityStatefulSetRequiresRecreate)
	}
	assertMaintenanceAnnotation(t, mgr, existing, pod, false)
}

func assertMaintenanceAnnotation(t *testing.T, mgr *Manager, sts *appsv1.StatefulSet, pod *corev1.Pod, want bool) {
	t.Helper()
	for _, obj := range []client.Object{&appsv1.StatefulSet{}, &corev1.Pod{}} {
		key := client.ObjectKeyFromObject(sts)
		if _, isPod := obj.(*corev1.Pod); isPod {
			key = client.ObjectKeyFromObject(pod)
		}
		if err := mgr.client.Get(context.Background(), key, obj); err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if got := obj.GetAnnotations()[constants.AnnotationMaintenance] == maintenanceAnnotationEnabledValue; got != want {
			t.Fatalf("%T %s maintenance annotation = %v, want %v", obj, key, got, want)
		}
	}
}
