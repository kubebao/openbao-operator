package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBackupHelperPinRequiresPreparationBeforeHandover(t *testing.T) {
	p, source := retirementFixture(t, clusterKind, "pinned")
	require.NoError(t, unstructured.SetNestedField(source.Object, "registry.example/backup:0.6.1", "spec", "backup", "image"))
	require.NoError(t, p.kube.client.Update(t.Context(), source))
	p.plan.Clusters[0].SpecDigest = digestJSON(source.Object["spec"])
	_, err := p.sourceIntact(t.Context(), p.plan.Clusters[0])
	require.ErrorContains(t, err, "spec.backup.image", "plans from an earlier binary must also stop before handover")
	_, err = p.planCluster(t.Context(), source, "namespace")
	require.ErrorContains(t, err, "spec.backup.image")
	require.NoError(t, p.rollbackPossible(t.Context()), "the preparation rule must not prevent recovery to the source")
}

func TestDestinationCannotRetainTheSourceBackupHelper(t *testing.T) {
	source := map[string]any{"helperImages": map[string]any{"backup": "registry.example/backup:0.6.1"}}
	for _, tc := range []struct {
		name, image string
		allowed     bool
	}{
		{"unchanged source helper", "registry.example/backup:0.6.1", false},
		{"destination default", "", true},
		{"destination custom helper", "registry.example/backup:0.7.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sourceImagePinned(source, map[string]any{"helperImages": map[string]any{"backup": tc.image}})
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "helperImages.backup")
			}
		})
	}
}

func TestDestinationBackupEnvironmentOverride(t *testing.T) {
	literal := func(image string) map[string]any {
		return map[string]any{"name": "OPERATOR_BACKUP_IMAGE", "value": image}
	}
	reference := func(name string) map[string]any {
		return map[string]any{"name": "OPERATOR_BACKUP_IMAGE", "valueFrom": map[string]any{
			"configMapKeyRef": map[string]any{"name": name, "key": "image"}}}
	}
	for _, source := range []map[string]any{literal("backup:0.6.1"), reference("source-helper")} {
		values := map[string]any{"controller": map[string]any{"extraEnv": []any{source}}}
		for _, tc := range []struct {
			name    string
			overlay map[string]any
			allowed bool
		}{
			{"inherited pin", nil, false},
			{"same pin", values, false},
			{"remove override", map[string]any{"controller": map[string]any{"extraEnv": []any{}}}, true},
			{"empty override", map[string]any{"controller": map[string]any{"extraEnv": []any{literal("")}}}, true},
			{"destination literal", map[string]any{"controller": map[string]any{"extraEnv": []any{literal("backup:0.7.0")}}}, true},
			{"destination reference", map[string]any{"controller": map[string]any{"extraEnv": []any{reference("destination-helper")}}}, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := sourceImagePinned(values, tc.overlay)
				if tc.allowed {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "OPERATOR_BACKUP_IMAGE")
				}
			})
		}
	}
	repository := map[string]any{"controller": map[string]any{"extraEnv": []any{
		map[string]any{"name": "OPERATOR_BACKUP_IMAGE_REPOSITORY", "value": "registry.example/backup"}}}}
	require.NoError(t, sourceImagePinned(repository, nil), "a repository override receives the destination version tag")
}

func TestForwardValidatesSavedPlansBeforeChangingProgress(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, phase := range []string{phasePlanned, phaseMigrating, phaseVerified, phaseCommitting} {
			t.Run(fmt.Sprintf("v%d/%s", version, phase), func(t *testing.T) {
				p, _ := retirementFixture(t, clusterKind, "source")
				p.plan.Version = version
				p.plan.Phase = phase
				before := p.plan.Progress
				var err error
				if phase == phaseCommitting {
					err = p.Commit(t.Context())
				} else {
					err = p.Migrate(t.Context())
				}
				require.ErrorContains(t, err, fmt.Sprintf("plan format %d", version))
				require.ErrorContains(t, err, "exact original migration binary")
				require.Equal(t, phase, p.plan.Phase)
				require.Equal(t, before, p.plan.Progress)
				require.NoError(t, p.rollbackPossible(t.Context()))
			})
		}
	}
	p, _ := retirementFixture(t, clusterKind, "source")
	p.plan.Version = planVersion
	p.plan.Operator.Install = installHelm
	p.plan.Helm.SourceValues = map[string]any{"helperImages": map[string]any{"backup": "backup:0.6.1"}}
	p.plan.Helm.DestinationOverlay = p.plan.Helm.SourceValues
	before := p.plan.Progress
	require.ErrorContains(t, p.Commit(t.Context()), "helperImages.backup")
	require.Equal(t, phaseCommitting, p.plan.Phase)
	require.Equal(t, before, p.plan.Progress)
}

func TestRetirementChecksAllHelperPinsAndSpecificationsBeforeDeletingAny(t *testing.T) {
	for _, kind := range []string{clusterKind, restoreKind, tenantKind} {
		for _, pin := range []bool{false, true} {
			if pin && kind != clusterKind {
				continue
			}
			t.Run(fmt.Sprintf("%s/pin=%t", kind, pin), func(t *testing.T) {
				p, later := retirementFixture(t, kind, "later")
				first := later.DeepCopy()
				first.SetKind(clusterKind)
				first.SetName("first")
				first.SetUID("first-uid")
				first.SetResourceVersion("")
				first.SetFinalizers([]string{clusterFinalizer})
				require.NoError(t, p.kube.client.Create(t.Context(), first))
				p.plan.Clusters = append([]ClusterPlan{{Namespace: first.GetNamespace(), Name: first.GetName(),
					SourceUID: first.GetUID(), SpecDigest: digestJSON(first.Object["spec"])}}, p.plan.Clusters...)
				message := "specification changed"
				if pin {
					require.NoError(t, unstructured.SetNestedField(later.Object, "backup:0.6.1", "spec", "backup", "image"))
					message = "spec.backup.image"
				} else {
					require.NoError(t, unstructured.SetNestedField(later.Object, "changed", "spec", "test"))
				}
				require.NoError(t, p.kube.client.Update(t.Context(), later))
				_, err := p.retireSource(t.Context())
				require.ErrorContains(t, err, message)
				fresh, err := p.kube.getObject(t.Context(), first.GetAPIVersion(), first.GetKind(), first.GetNamespace(), first.GetName())
				require.NoError(t, err)
				require.NotNil(t, fresh)
				require.Nil(t, fresh.GetDeletionTimestamp())
				require.Equal(t, first.GetFinalizers(), fresh.GetFinalizers())
				require.NoError(t, p.rollbackPossible(t.Context()))
			})
		}
	}
}

func TestLiveDestinationBackupSelection(t *testing.T) {
	sourceReference := corev1.EnvVar{Name: "OPERATOR_BACKUP_IMAGE", ValueFrom: &corev1.EnvVarSource{
		ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "source-helper"}, Key: "image"}}}
	for _, source := range [][]corev1.EnvVar{
		nil,
		{{Name: "OPERATOR_BACKUP_IMAGE", Value: "backup:0.6.1"}},
		{sourceReference},
	} {
		p := &Procedure{plan: &Plan{Operator: OperatorPlan{BackupImage: backupHelperImage(source, "0.6.1")}}}
		require.ErrorContains(t, p.checkBackupHelperImage(backupHelperImage(source, "0.6.1")), "source backup helper")
		require.NoError(t, p.checkBackupHelperImage(backupHelperImage(nil, "0.7.0")))
		require.NoError(t, p.checkBackupHelperImage(backupHelperImage([]corev1.EnvVar{
			{Name: "OPERATOR_BACKUP_IMAGE", Value: "backup:0.7.0"}}, "0.7.0")))
	}
}
