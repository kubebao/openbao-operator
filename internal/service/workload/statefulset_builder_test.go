package workload

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

const testRuntimeRestartAt = "2026-01-19T00:00:00Z"

func TestBuildStatefulSetPodSecurityContext(t *testing.T) {
	tests := []struct {
		name     string
		cluster  *openbaov1alpha1.OpenBaoCluster
		platform string
		wantUser *int64
		wantGrp  *int64
		wantFS   *int64
	}{
		{
			name: "default kubernetes platform pins IDs",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{},
			},
			platform: constants.PlatformKubernetes,
			wantUser: ptr.To(constants.UserOpenBao),
			wantGrp:  ptr.To(constants.GroupOpenBao),
			wantFS:   ptr.To(constants.GroupOpenBao),
		},
		{
			name: "empty platform defaults to pinning IDs (same as kubernetes)",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{},
			},
			platform: "",
			wantUser: ptr.To(constants.UserOpenBao),
			wantGrp:  ptr.To(constants.GroupOpenBao),
			wantFS:   ptr.To(constants.GroupOpenBao),
		},
		{
			name: "openshift platform omits IDs",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{},
			},
			platform: constants.PlatformOpenShift,
			wantUser: nil,
			wantGrp:  nil,
			wantFS:   nil,
		},
		{
			name: "CRD override takes precedence over kubernetes default",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  ptr.To(int64(1001)),
						RunAsGroup: ptr.To(int64(2001)),
						FSGroup:    ptr.To(int64(3001)),
					},
				},
			},
			platform: constants.PlatformKubernetes,
			wantUser: ptr.To(int64(1001)),
			wantGrp:  ptr.To(int64(2001)),
			wantFS:   ptr.To(int64(3001)),
		},
		{
			name: "CRD override takes precedence over openshift default",
			cluster: &openbaov1alpha1.OpenBaoCluster{
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser: ptr.To(int64(1001)),
					},
				},
			},
			platform: constants.PlatformOpenShift,
			wantUser: ptr.To(int64(1001)),
			wantGrp:  nil, // Should remain nil as not overridden
			wantFS:   nil, // Should remain nil as not overridden
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := buildStatefulSetPodSecurityContext(tt.cluster, tt.platform)

			if !ptrInt64Equal(sc.RunAsUser, tt.wantUser) {
				t.Errorf("RunAsUser = %v, want %v", ptrInt64Value(sc.RunAsUser), ptrInt64Value(tt.wantUser))
			}
			if !ptrInt64Equal(sc.RunAsGroup, tt.wantGrp) {
				t.Errorf("RunAsGroup = %v, want %v", ptrInt64Value(sc.RunAsGroup), ptrInt64Value(tt.wantGrp))
			}
			if !ptrInt64Equal(sc.FSGroup, tt.wantFS) {
				t.Errorf("FSGroup = %v, want %v", ptrInt64Value(sc.FSGroup), ptrInt64Value(tt.wantFS))
			}
		})
	}
}

func ptrInt64Equal(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrInt64Value(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestBuildStatefulSet_MaintenanceAnnotations(t *testing.T) {
	cluster := newMinimalCluster("maintenance-cluster", "default")
	cluster.Spec.Maintenance = &openbaov1alpha1.MaintenanceConfig{
		Enabled: true,
	}
	cluster.Spec.Runtime = &openbaov1alpha1.RuntimeConfig{
		RestartAt: testRuntimeRestartAt,
	}

	statefulSet, err := buildStatefulSetWithRevision(cluster, "test-config", true, "", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatalf("buildStatefulSetWithRevision() error = %v", err)
	}

	if got := statefulSet.Annotations[constants.AnnotationMaintenance]; got != maintenanceAnnotationEnabledValue {
		t.Fatalf("expected StatefulSet annotation %q to be %q, got %q", constants.AnnotationMaintenance, maintenanceAnnotationEnabledValue, got)
	}

	if got := statefulSet.Spec.Template.Annotations[constants.AnnotationRestartAt]; got != testRuntimeRestartAt {
		t.Fatalf("expected Pod template annotation %q to be set, got %q", constants.AnnotationRestartAt, got)
	}
}

func TestBuildStatefulSetForSpec_UsesPoolSpecificRestartOverride(t *testing.T) {
	cluster := newMinimalCluster("read-restart-override-cluster", "default")
	cluster.Spec.Runtime = &openbaov1alpha1.RuntimeConfig{
		RestartAt: testRuntimeRestartAt,
	}

	override := "2026-01-20T00:00:00Z"
	statefulSet, err := buildStatefulSetForSpec(cluster, "test-config", true, StatefulSetSpec{
		Name:      "read-restart-override-cluster-read",
		Pool:      constants.LabelValueOpenBaoWorkloadPoolReadReplica,
		Replicas:  1,
		RestartAt: &override,
	}, constants.PlatformKubernetes)
	if err != nil {
		t.Fatalf("buildStatefulSetForSpec() error = %v", err)
	}

	if got := statefulSet.Spec.Template.Annotations[constants.AnnotationRestartAt]; got != override {
		t.Fatalf("expected pool-specific restart override %q, got %q", override, got)
	}
}

func TestBuildStatefulSet_DeletesPVCsOnlyWhenScaledDown(t *testing.T) {
	cluster := newMinimalCluster("scaledown-pvc-cluster", "default")

	statefulSet, err := buildStatefulSetWithRevision(cluster, "test-config", true, "", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatalf("buildStatefulSetWithRevision() error = %v", err)
	}

	retentionPolicy := statefulSet.Spec.PersistentVolumeClaimRetentionPolicy
	if retentionPolicy == nil {
		t.Fatal("expected StatefulSet PVC retention policy")
	}
	if got := retentionPolicy.WhenScaled; got != appsv1.DeletePersistentVolumeClaimRetentionPolicyType {
		t.Fatalf("WhenScaled = %q, want %q", got, appsv1.DeletePersistentVolumeClaimRetentionPolicyType)
	}
	if got := retentionPolicy.WhenDeleted; got != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
		t.Fatalf("WhenDeleted = %q, want %q", got, appsv1.RetainPersistentVolumeClaimRetentionPolicyType)
	}
}

func TestBuildStatefulSet_DefaultPlacementPolicy(t *testing.T) {
	cluster := newMinimalCluster("spread-cluster", "default")

	statefulSet, err := buildStatefulSetWithRevision(cluster, "test-config", true, "", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatalf("buildStatefulSetWithRevision() error = %v", err)
	}

	if got := statefulSet.Spec.Template.Labels[constants.LabelOpenBaoComponent]; got != constants.ComponentOpenBaoCluster {
		t.Fatalf("expected Pod label %q=%q, got %q", constants.LabelOpenBaoComponent, constants.ComponentOpenBaoCluster, got)
	}

	placementLabels := statefulSetPlacementLabels(cluster, StatefulSetSpec{Pool: constants.LabelValueOpenBaoWorkloadPoolVoter})

	affinity := statefulSet.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.PodAntiAffinity == nil {
		t.Fatal("expected preferred pod anti-affinity to be configured")
	}

	terms := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(terms) != 1 {
		t.Fatalf("expected 1 preferred anti-affinity term, got %d", len(terms))
	}

	term := terms[0]
	if term.Weight != statefulSetPlacementPreferredWeight {
		t.Fatalf("expected anti-affinity weight %d, got %d", statefulSetPlacementPreferredWeight, term.Weight)
	}
	if term.PodAffinityTerm.TopologyKey != statefulSetTopologyKeyHostname {
		t.Fatalf("expected anti-affinity topology key %q, got %q", statefulSetTopologyKeyHostname, term.PodAffinityTerm.TopologyKey)
	}
	if term.PodAffinityTerm.LabelSelector == nil {
		t.Fatal("expected anti-affinity label selector")
	}
	if !reflect.DeepEqual(term.PodAffinityTerm.LabelSelector.MatchLabels, placementLabels) {
		t.Fatalf("anti-affinity MatchLabels = %#v, want %#v", term.PodAffinityTerm.LabelSelector.MatchLabels, placementLabels)
	}

	constraints := statefulSet.Spec.Template.Spec.TopologySpreadConstraints
	if len(constraints) != 2 {
		t.Fatalf("expected 2 topology spread constraints, got %d", len(constraints))
	}

	wantTopologyKeys := []string{statefulSetTopologyKeyHostname, statefulSetTopologyKeyZone}
	for i, wantTopologyKey := range wantTopologyKeys {
		constraint := constraints[i]
		if constraint.MaxSkew != 1 {
			t.Fatalf("constraint %d MaxSkew = %d, want 1", i, constraint.MaxSkew)
		}
		if constraint.TopologyKey != wantTopologyKey {
			t.Fatalf("constraint %d TopologyKey = %q, want %q", i, constraint.TopologyKey, wantTopologyKey)
		}
		if constraint.WhenUnsatisfiable != corev1.ScheduleAnyway {
			t.Fatalf("constraint %d WhenUnsatisfiable = %q, want %q", i, constraint.WhenUnsatisfiable, corev1.ScheduleAnyway)
		}
		if constraint.LabelSelector == nil {
			t.Fatalf("constraint %d missing LabelSelector", i)
		}
		if !reflect.DeepEqual(constraint.LabelSelector.MatchLabels, placementLabels) {
			t.Fatalf("constraint %d MatchLabels = %#v, want %#v", i, constraint.LabelSelector.MatchLabels, placementLabels)
		}
	}
}

func TestBuildStatefulSet_PodMetadata(t *testing.T) {
	cluster := newMinimalCluster("metadata-cluster", "default")
	cluster.Spec.PodMetadata = &openbaov1alpha1.PodMetadataConfig{
		Labels: map[string]string{
			"azure.workload.identity/use": "true",
			constants.LabelOpenBaoCluster: "should-not-override",
			"openbao-active":              "true",
			"openbao-sealed":              "false",
		},
		Annotations: map[string]string{
			"example.com/custom":          "enabled",
			configHashAnnotation:          "should-not-override",
			constants.AnnotationRestartAt: "should-not-override",
		},
	}
	cluster.Spec.Runtime = &openbaov1alpha1.RuntimeConfig{
		RestartAt: "2026-01-19T00:00:00Z",
	}
	cluster.Status.Restore = &openbaov1alpha1.ClusterRestoreStatus{
		Name: "restore-a",
		UID:  "restore-uid-a",
	}

	statefulSet, err := buildStatefulSetWithRevision(cluster, "test-config", true, "", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatalf("buildStatefulSetWithRevision() error = %v", err)
	}

	if got := statefulSet.Spec.Template.Labels["azure.workload.identity/use"]; got != "true" {
		t.Fatalf("expected custom pod label to be set, got %q", got)
	}
	if got := statefulSet.Spec.Template.Labels[constants.LabelOpenBaoCluster]; got != cluster.Name {
		t.Fatalf("expected operator-managed pod label %q=%q, got %q", constants.LabelOpenBaoCluster, cluster.Name, got)
	}
	if _, ok := statefulSet.Spec.Template.Labels["openbao-active"]; ok {
		t.Fatalf("did not expect CR podMetadata to set OpenBao service-registration label")
	}
	if _, ok := statefulSet.Spec.Template.Labels["openbao-sealed"]; ok {
		t.Fatalf("did not expect CR podMetadata to set OpenBao service-registration label")
	}

	if got := statefulSet.Spec.Template.Annotations["example.com/custom"]; got != "enabled" {
		t.Fatalf("expected custom pod annotation to be set, got %q", got)
	}
	if got := statefulSet.Spec.Template.Annotations[constants.AnnotationRestartAt]; got != "2026-01-19T00:00:00Z" {
		t.Fatalf("expected operator-managed restart annotation to win, got %q", got)
	}
	if got := statefulSet.Spec.Template.Annotations[constants.AnnotationRestoreRevision]; got != cluster.Status.Restore.UID {
		t.Fatalf("expected legacy restore annotation to remain unchanged, got %q", got)
	}
	if got := statefulSet.Spec.Template.Annotations[configHashAnnotation]; got == "" || got == "should-not-override" {
		t.Fatalf("expected operator-managed config hash annotation to win, got %q", got)
	}
}

func TestBuildStatefulSet_PlacementPolicySpansRevisions(t *testing.T) {
	cluster := newMinimalCluster("bluegreen-cluster", "default")

	statefulSet, err := buildStatefulSetWithRevision(cluster, "test-config", true, "", "", "green-revision", constants.PlatformKubernetes)
	if err != nil {
		t.Fatalf("buildStatefulSetWithRevision() error = %v", err)
	}

	if got := statefulSet.Spec.Template.Labels[constants.LabelOpenBaoRevision]; got != "green-revision" {
		t.Fatalf("expected Pod label %q=%q, got %q", constants.LabelOpenBaoRevision, "green-revision", got)
	}

	selector := statefulSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector
	if selector == nil {
		t.Fatal("expected anti-affinity selector")
	}
	if _, ok := selector.MatchLabels[constants.LabelOpenBaoRevision]; ok {
		t.Fatalf("expected placement selector to omit %q so it spans all cluster revisions", constants.LabelOpenBaoRevision)
	}
}

func TestBuildStatefulSet_SourceGenerationDoesNotRollPods(t *testing.T) {
	cluster := newMinimalCluster("source-generation", "default")
	cluster.Generation = 1
	first, err := buildStatefulSetWithRevision(cluster, "test-config", true, "openbao@sha256:verified", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	cluster.Generation = 2
	next, err := buildStatefulSetWithRevision(cluster, "test-config", true, "openbao@sha256:verified", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	if first.Annotations[constants.AnnotationClusterGeneration] != "1" || next.Annotations[constants.AnnotationClusterGeneration] != "2" {
		t.Fatal("StatefulSet must retain its source generation")
	}
	if !reflect.DeepEqual(first.Spec.Template, next.Spec.Template) {
		t.Fatal("source generation alone must not change the Pod template")
	}
}

func TestBuildStatefulSet_ResolvedMainImageMatchesTemplate(t *testing.T) {
	t.Setenv(constants.EnvOpenBaoImageRepo, "example.org/openbao")
	const digest = "example.org/openbao@sha256:verified"
	const tag = "example.org/openbao:2.6.4"
	for _, tc := range []struct {
		name          string
		specImage     string
		resolvedImage string
		wantImage     string
	}{
		{name: "verified tag", specImage: tag, resolvedImage: digest, wantImage: digest},
		{name: "digest input", specImage: digest, resolvedImage: digest, wantImage: digest},
		{name: "verification disabled", specImage: tag, wantImage: tag},
		{name: "Warn fallback", specImage: tag, resolvedImage: tag, wantImage: tag},
		{name: "default repository override", wantImage: tag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newMinimalCluster("resolved-image", "default")
			cluster.Spec.Version = "2.6.4"
			cluster.Spec.Image = tc.specImage
			sts, err := buildStatefulSetWithRevision(cluster, "test-config", true, tc.resolvedImage, "", "", constants.PlatformKubernetes)
			if err != nil {
				t.Fatal(err)
			}
			if got := sts.Annotations[constants.AnnotationResolvedMainImage]; got != tc.wantImage {
				t.Fatalf("resolved image marker = %q, want %q", got, tc.wantImage)
			}
			if got := sts.Spec.Template.Spec.Containers[0].Image; got != tc.wantImage {
				t.Fatalf("rendered image = %q, want %q", got, tc.wantImage)
			}
			if _, exists := sts.Spec.Template.Annotations[constants.AnnotationResolvedMainImage]; exists {
				t.Fatal("resolved image marker must be StatefulSet metadata only")
			}
		})
	}
}

func TestFreshRestorePodHasNoKubernetesCredential(t *testing.T) {
	cluster := newMinimalCluster("fresh", "recovery")
	cluster.Annotations = map[string]string{constants.AnnotationRestoreOrigin: "request"}
	sts, err := buildStatefulSetWithRevision(cluster, "test-config", false, "", "", "", constants.PlatformKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	pod := sts.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken ||
		pod.EnableServiceLinks == nil || *pod.EnableServiceLinks {
		t.Fatal("fresh server must disable API credential automount and service links")
	}
	for _, volume := range pod.Volumes {
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil {
				t.Fatalf("unexpected ServiceAccount token projection in %s", volume.Name)
			}
		}
	}
}
