//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/statusapply"
	provisionerpkg "github.com/kubebao/openbao-operator/internal/service/provisioner"
	hardenedfixtures "github.com/kubebao/openbao-operator/test/fixtures/hardenedcontract"
)

func requireInvalidRequest(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected invalid request error, got nil")
	}

	if apierrors.IsInvalid(err) {
		return
	}

	var apiStatus apierrors.APIStatus
	if errors.As(err, &apiStatus) {
		status := apiStatus.Status()
		if status.Code == http.StatusUnprocessableEntity || status.Reason == metav1.StatusReasonInvalid {
			return
		}
	}

	if strings.Contains(err.Error(), "is invalid") {
		return
	}

	t.Fatalf("expected invalid request error, got %T: %v", err, err)
}

func waitForResourceAuthorization(t *testing.T, username, namespace, apiGroup, resourceName, objectName, verb string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		review := &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				User: username,
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: namespace,
					Verb:      verb,
					Group:     apiGroup,
					Resource:  resourceName,
					Name:      objectName,
				},
			},
		}
		if err := k8sClient.Create(ctx, review); err != nil {
			t.Fatalf("create SubjectAccessReview for %s %s/%s: %v", verb, resourceName, objectName, err)
		}
		if review.Status.Allowed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"wait for %s authorization to %s %s/%s: reason=%q evaluationError=%q",
				username,
				verb,
				resourceName,
				objectName,
				review.Status.Reason,
				review.Status.EvaluationError,
			)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func grantTenantOpenBaoWriteAccess(t *testing.T, namespace, username string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-openbao-writer",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"openbao.org"},
				Resources: []string{"openbaoclusters", "openbaorestores"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create tenant writer role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-openbao-writer-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create tenant writer rolebinding: %v", err)
	}
	waitForResourceAuthorization(t, username, namespace, "openbao.org", "openbaoclusters", "", "create")
}

func grantClusterOpenBaoVerbs(t *testing.T, namespace, clusterName, username, roleName string, verbs ...string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"openbao.org"},
				Resources:     []string{"openbaoclusters"},
				ResourceNames: []string{clusterName},
				Verbs:         append([]string{"get"}, verbs...),
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create delegated OpenBao role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName + "-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create delegated OpenBao rolebinding: %v", err)
	}
	for _, verb := range verbs {
		waitForResourceAuthorization(t, username, namespace, "openbao.org", "openbaoclusters", clusterName, verb)
	}
}

func grantClusterHelperImageAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()
	grantClusterOpenBaoVerbs(t, namespace, clusterName, username, "cluster-helper-image-access", "usehelperimages")
}

func grantClusterCustomExecutablesAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()
	grantClusterOpenBaoVerbs(t, namespace, clusterName, username, "cluster-custom-executables-access", "usecustomexecutables")
}

func grantClusterImageTrustRootsAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()
	grantClusterOpenBaoVerbs(t, namespace, clusterName, username, "cluster-image-trust-roots-access", "useimagetrustroots")
}

func grantClusterRestoreAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()
	grantClusterOpenBaoVerbs(t, namespace, clusterName, username, "cluster-restore-access", "restore")
}

func grantClusterCloudIdentitiesAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()
	grantClusterOpenBaoVerbs(t, namespace, clusterName, username, "cluster-cloud-identities-access", "usecloudidentities")
}

func grantClusterNetworkPublicationAccess(t *testing.T, namespace, clusterName, username string) {
	t.Helper()
	grantClusterOpenBaoVerbs(t, namespace, clusterName, username, "cluster-network-publication-"+clusterName, "publishnetworking")
}

func grantNamespacedResourceVerbs(t *testing.T, namespace, username, roleName, apiGroup, resourceName string, resourceNames []string, verbs ...string) {
	t.Helper()

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{apiGroup},
				Resources:     []string{resourceName},
				ResourceNames: resourceNames,
				Verbs:         verbs,
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create delegated reference role: %v", err)
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName + "-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create delegated reference rolebinding: %v", err)
	}
	for _, verb := range verbs {
		for _, objectName := range resourceNames {
			waitForResourceAuthorization(t, username, namespace, apiGroup, resourceName, objectName, verb)
		}
	}
}

func grantClusterScopedResourceVerbs(t *testing.T, username, roleName, apiGroup, resourceName string, resourceNames []string, verbs ...string) {
	t.Helper()

	role := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{apiGroup},
				Resources:     []string{resourceName},
				ResourceNames: resourceNames,
				Verbs:         verbs,
			},
		},
	}
	if err := k8sClient.Create(ctx, role); err != nil {
		t.Fatalf("create delegated cluster reference role: %v", err)
	}

	binding := &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName + "-binding",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     username,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}
	if err := k8sClient.Create(ctx, binding); err != nil {
		t.Fatalf("create delegated cluster reference rolebinding: %v", err)
	}
	for _, verb := range verbs {
		for _, objectName := range resourceNames {
			waitForResourceAuthorization(t, username, "", apiGroup, resourceName, objectName, verb)
		}
	}
}

func waitForOpenBaoRestoreAdmissionPolicies(t *testing.T, namespace string) {
	t.Helper()

	ensureDefaultAdmissionPoliciesApplied(t)

	for attempt := 0; attempt < 25; attempt++ {
		restore := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("restore-policy-probe-%d", attempt),
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{
				Cluster: "policy-probe",
				Source: openbaov1alpha1.RestoreSource{
					Target: openbaov1alpha1.BackupTarget{
						Provider: "s3",
						Endpoint: "http://example.com",
						Bucket:   testBackupBucket,
					},
					Key: "clusters/probe/snapshot.snap",
				},
				JWTAuthRole: "restore-role",
			},
		}

		err := k8sClient.Create(ctx, restore)
		if err == nil {
			_ = k8sClient.Delete(ctx, restore)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		return
	}

	t.Fatalf("expected OpenBaoRestore admission policies to become active after retries")
}

func TestCRD_OpenBaoRestore_RejectsMissingSpec(t *testing.T) {
	namespace := newTestNamespace(t)

	restore := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "openbao.org/v1alpha1",
			"kind":       "OpenBaoRestore",
			"metadata": map[string]any{
				"name":      "restore-missing-spec",
				"namespace": namespace,
			},
		},
	}

	err := k8sClient.Create(ctx, restore)
	requireInvalidRequest(t, err)
}

func TestCRD_OpenBaoCluster_RejectsMissingSpec(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "openbao.org/v1alpha1",
			"kind":       "OpenBaoCluster",
			"metadata": map[string]any{
				"name":      "cluster-missing-spec",
				"namespace": namespace,
			},
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
}

func TestCRD_OpenBaoTenant_RejectsMissingSpec(t *testing.T) {
	namespace := newTestNamespace(t)

	tenant := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "openbao.org/v1alpha1",
			"kind":       "OpenBaoTenant",
			"metadata": map[string]any{
				"name":      "tenant-missing-spec",
				"namespace": namespace,
			},
		},
	}

	err := k8sClient.Create(ctx, tenant)
	requireInvalidRequest(t, err)
}

func TestVAP_OpenBaoRestore_RejectsSpecMutation(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)

	for attempt := 0; attempt < 25; attempt++ {
		name := fmt.Sprintf("restore-immutable-%d", attempt)
		restore := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{
				Cluster: "cluster-1",
				Source: openbaov1alpha1.RestoreSource{
					Key: "backup.enc",
					Target: openbaov1alpha1.BackupTarget{
						Endpoint: "https://objectstore.example.com",
						Bucket:   "backups",
					},
				},
				JWTAuthRole: "restore-role",
			},
		}

		if err := k8sClient.Create(ctx, restore); err != nil {
			t.Fatalf("create OpenBaoRestore: %v", err)
		}

		var latest openbaov1alpha1.OpenBaoRestore
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: restore.Name}, &latest); err != nil {
			t.Fatalf("get OpenBaoRestore: %v", err)
		}
		original := latest.DeepCopy()
		latest.Spec.Source.Key = "backup-v2.enc"

		err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original))
		if err == nil {
			_ = k8sClient.Delete(ctx, &latest)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec is immutable") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny OpenBaoRestore spec mutation after retries")
}

func TestCRD_OpenBaoCluster_RequiresProfile(t *testing.T) {
	namespace := newTestNamespace(t)

	for attempt := 0; attempt < 25; attempt++ {
		cluster := newMinimalClusterObj(namespace, fmt.Sprintf("cluster-missing-profile-%d", attempt))
		cluster.Spec.Profile = ""

		err := k8sClient.Create(ctx, cluster)
		if err == nil {
			_ = k8sClient.Delete(ctx, cluster)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireInvalidRequest(t, err)
		if !strings.Contains(err.Error(), "spec.profile") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected CRD validation to reject OpenBaoCluster create without spec.profile after retries")
}

func TestCRD_OpenBaoCluster_ValidatesSemanticVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		valid   bool
	}{
		{name: "release", version: "2.6.2", valid: true},
		{name: "lowercase v prefix", version: "v2.6.2", valid: true},
		{name: "prerelease and build", version: "2.7.0-rc.1+build.7", valid: true},
		{name: "missing patch", version: "2.6", valid: false},
		{name: "leading zero segment", version: "2.06.1", valid: false},
		{name: "leading zero numeric prerelease", version: "2.7.0-rc.01", valid: false},
		{name: "uppercase v prefix", version: "V2.6.2", valid: false},
		{name: "surrounding whitespace", version: " 2.6.2 ", valid: false},
		{name: "image tag alias", version: "latest", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "cluster-semver")
			cluster.Spec.Version = tt.version

			err := k8sClient.Create(ctx, cluster)
			if tt.valid {
				if err != nil {
					t.Fatalf("expected semantic version %q to be accepted, got: %v", tt.version, err)
				}
				return
			}
			requireInvalidRequest(t, err)
		})
	}
}

func TestCRD_OpenBaoCluster_AcceptsVoterResources(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "cluster-voter-resources")
	cluster.Spec.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}

	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected voter resources to be accepted, got: %v", err)
	}

	var stored openbaov1alpha1.OpenBaoCluster
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &stored); err != nil {
		t.Fatalf("get OpenBaoCluster: %v", err)
	}
	if stored.Spec.Resources == nil {
		t.Fatal("stored spec.resources is nil")
	}
	if got := stored.Spec.Resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("stored CPU request = %q, want %q", got, "500m")
	}
	if got := stored.Spec.Resources.Limits.Memory().String(); got != "2Gi" {
		t.Fatalf("stored memory limit = %q, want %q", got, "2Gi")
	}
}

func TestCRD_OpenBaoCluster_AcceptsMigratedStabilityFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "fixtures", "api-migration", "0.5.0-openbaocluster.yaml"))
	if err != nil {
		t.Fatalf("read migrated API fixture: %v", err)
	}
	data, err = utilyaml.ToJSON(data)
	if err != nil {
		t.Fatalf("convert migrated API fixture to JSON: %v", err)
	}
	cluster := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &cluster.Object); err != nil {
		t.Fatalf("decode migrated API fixture: %v", err)
	}
	cluster.SetNamespace(newTestNamespace(t))
	cluster.SetName("cluster-migrated-api")
	// Keep the historical fixture unchanged; a new CREATE now declares its mode.
	if err := unstructured.SetNestedField(cluster.Object, "Shared", "spec", "controllerJWTMode"); err != nil {
		t.Fatalf("set creation JWT mode: %v", err)
	}

	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected migrated 0.5.0 API fixture to be accepted, got: %v", err)
	}
}

func TestCRD_OpenBaoCluster_RequiresSelectedUnsealConfiguration(t *testing.T) {
	for _, unsealType := range []string{
		"transit",
		"awskms",
		"azurekeyvault",
		"gcpckms",
		"kmip",
		"kms",
		"ocikms",
		"pkcs11",
	} {
		t.Run(unsealType, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "cluster-unseal-branch")
			cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{Type: unsealType}

			err := k8sClient.Create(ctx, cluster)
			requireInvalidRequest(t, err)
			if !strings.Contains(err.Error(), "requires its matching configuration block") {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestCRD_OpenBaoCluster_RejectsUnselectedUnsealConfiguration(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "cluster-unseal-extra-branch")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "transit",
		Transit: &openbaov1alpha1.TransitSealConfig{
			Address:   "https://transit.example.com",
			KeyName:   "autounseal",
			MountPath: "transit",
		},
		AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
			Region:   "eu-west-1",
			KMSKeyID: "alias/unused",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
	if !strings.Contains(err.Error(), "spec.unseal.awskms is only supported when spec.unseal.type is awskms") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCRD_OpenBaoCluster_AllowsStaticUnsealWithoutConfiguration(t *testing.T) {
	cluster := newMinimalClusterObj(newTestNamespace(t), "cluster-static-unseal")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{Type: "static"}

	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected static unseal without an explicit configuration block to succeed, got: %v", err)
	}
}

func TestCRD_OpenBaoCluster_ValidatesBackupTargetProviderShape(t *testing.T) {
	tests := []struct {
		name    string
		target  openbaov1alpha1.BackupTarget
		message string
	}{
		{
			name: "s3 endpoint is required",
			target: openbaov1alpha1.BackupTarget{
				Provider: "s3",
				Bucket:   testBackupBucket,
			},
			message: "backup target endpoint is required when provider is s3",
		},
		{
			name: "gcs options require gcs provider",
			target: openbaov1alpha1.BackupTarget{
				Provider: "s3",
				Endpoint: "https://objectstore.example.com",
				Bucket:   testBackupBucket,
				GCS:      &openbaov1alpha1.GCSTargetConfig{},
			},
			message: "backup target gcs options are only supported when provider is gcs",
		},
		{
			name: "azure options require azure provider",
			target: openbaov1alpha1.BackupTarget{
				Provider: "gcs",
				Bucket:   testBackupBucket,
				Azure:    &openbaov1alpha1.AzureTargetConfig{StorageAccount: "unused"},
			},
			message: "backup target azure options are only supported when provider is azure",
		},
		{
			name: "role arn requires s3 provider",
			target: openbaov1alpha1.BackupTarget{
				Provider: "gcs",
				Bucket:   testBackupBucket,
				RoleARN:  "arn:aws:iam::123456789012:role/unused",
			},
			message: "backup target roleArn is only supported when provider is s3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "cluster-backup-target")
			cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
				Schedule: "0 3 * * *",
				Target:   tt.target,
			}

			err := k8sClient.Create(ctx, cluster)
			requireInvalidRequest(t, err)
			if !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestVAP_OpenBaoCluster_AllowsDefaultInitContainer(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-default-init")
	cluster.Spec.Profile = openbaov1alpha1.ProfileDevelopment
	cluster.Spec.InitContainer = nil

	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected OpenBaoCluster create without spec.initContainer to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_GuardsIdleUpgradeStrategySwitches(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	healthyStatus := func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.Phase = openbaov1alpha1.ClusterPhaseRunning
		status.CurrentVersion = testOpenBaoVersion244
		status.ReadyReplicas = 3
		status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyRollingUpdate
		status.Conditions = []metav1.Condition{
			{
				Type:               string(openbaov1alpha1.ConditionAvailable),
				Status:             metav1.ConditionTrue,
				Reason:             "Ready",
				Message:            "all voters are ready",
				LastTransitionTime: metav1.Now(),
			},
		}
	}

	cluster := newMinimalClusterObj(namespace, "cluster-strategy-switch-idle")
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
		Strategy:    openbaov1alpha1.UpdateStrategyRollingUpdate,
		JWTAuthRole: "upgrade-role",
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create strategy-switch cluster: %v", err)
	}
	updateClusterStatus(t, cluster, healthyStatus)

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get cluster before RollingUpdate to BlueGreen switch: %v", err)
	}
	latest.Spec.Upgrade.Strategy = openbaov1alpha1.UpdateStrategyBlueGreen
	if err := k8sClient.Update(ctx, &latest); err != nil {
		t.Fatalf("expected healthy idle RollingUpdate to BlueGreen switch to succeed, got: %v", err)
	}

	updateClusterStatus(t, &latest, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		healthyStatus(status)
		status.AcceptedUpgradeStrategy = openbaov1alpha1.UpdateStrategyBlueGreen
		status.BlueGreen = &openbaov1alpha1.BlueGreenStatus{Phase: openbaov1alpha1.PhaseIdle}
	})
	explicitNullCluster := &openbaov1alpha1.OpenBaoCluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: openbaov1alpha1.GroupVersion.String(),
			Kind:       "OpenBaoCluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      latest.Name,
			Namespace: latest.Namespace,
		},
	}
	applyConfig, err := statusapply.ToApplyConfigurationWithExplicitNulls(
		explicitNullCluster,
		k8sClient,
		"status.operationLock",
	)
	if err != nil {
		t.Fatalf("build explicit-null operation lock apply: %v", err)
	}
	if err := k8sClient.Status().Apply(
		ctx,
		applyConfig,
		client.FieldOwner(constants.FieldOwnerOperationLockStatus),
	); err != nil {
		t.Fatalf("persist cleared operation lock as explicit null: %v", err)
	}
	var raw unstructured.Unstructured
	raw.SetAPIVersion(openbaov1alpha1.GroupVersion.String())
	raw.SetKind("OpenBaoCluster")
	if err := k8sClient.Get(ctx, key, &raw); err != nil {
		t.Fatalf("get raw cluster after explicit-null operation lock apply: %v", err)
	}
	operationLock, found, err := unstructured.NestedFieldNoCopy(raw.Object, "status", "operationLock")
	if err != nil || !found || operationLock != nil {
		t.Fatalf("expected explicit null status.operationLock, found=%v value=%#v err=%v", found, operationLock, err)
	}
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get cluster before BlueGreen to RollingUpdate switch: %v", err)
	}
	latest.Spec.Upgrade.Strategy = openbaov1alpha1.UpdateStrategyRollingUpdate
	if err := k8sClient.Update(ctx, &latest); err != nil {
		t.Fatalf("expected healthy idle BlueGreen to RollingUpdate switch to succeed, got: %v", err)
	}

	blocked := newMinimalClusterObj(namespace, "cluster-strategy-switch-blocked")
	blocked.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
		Strategy:    openbaov1alpha1.UpdateStrategyRollingUpdate,
		JWTAuthRole: "upgrade-role",
	}
	if err := k8sClient.Create(ctx, blocked); err != nil {
		t.Fatalf("create blocked strategy-switch cluster: %v", err)
	}
	updateClusterStatus(t, blocked, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		healthyStatus(status)
		status.OperationLock = &openbaov1alpha1.OperationLockStatus{
			Operation: openbaov1alpha1.ClusterOperationBackup,
			Holder:    "backup-manager",
		}
	})
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: blocked.Name}, blocked); err != nil {
		t.Fatalf("get blocked strategy-switch cluster: %v", err)
	}
	blocked.Spec.Upgrade.Strategy = openbaov1alpha1.UpdateStrategyBlueGreen
	updateErr := k8sClient.Update(ctx, blocked)
	requireAdmissionDenied(t, updateErr)
	if !strings.Contains(updateErr.Error(), "can change only while the cluster is initialized, healthy") {
		t.Fatalf("unexpected strategy-switch rejection: %v", updateErr)
	}
}

func TestVAP_OpenBaoCluster_RequiresReferenceUseAuthorization(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "reference-use-editor"
	tenantClient := newImpersonatedClient(t, username)
	grantTenantOpenBaoWriteAccess(t, namespace, username)

	configureTrustedIngressPeers := func(cluster *openbaov1alpha1.OpenBaoCluster) {
		cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
			TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": "ingress-system",
						},
					},
				},
			},
		}
	}

	t.Run("service-account-use", func(t *testing.T) {
		const serviceAccountName = "custom-openbao-sa"

		denied := newMinimalClusterObj(namespace, "cluster-serviceaccount-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.ServiceAccount = &openbaov1alpha1.ServiceAccountConfig{Name: serviceAccountName}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec.serviceAccount.name") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"serviceaccount-use-access",
			"",
			"serviceaccounts",
			[]string{serviceAccountName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-serviceaccount-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.ServiceAccount = &openbaov1alpha1.ServiceAccountConfig{Name: serviceAccountName}
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected service-account-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("image-pull-secret-use", func(t *testing.T) {
		const secretName = "tenant-pull-secret"

		denied := newMinimalClusterObj(namespace, "cluster-image-pull-secret-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: secretName}}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec.imagePullSecrets") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"image-pull-secret-use-access",
			"",
			"secrets",
			[]string{secretName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-image-pull-secret-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: secretName}}
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected image-pull-secret-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("ingress-class-use", func(t *testing.T) {
		const className = "tenant-ingress-class"

		denied := newMinimalClusterObj(namespace, "cluster-ingress-class-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled:   true,
			ClassName: ptr.To(className),
			Host:      "bao.example.com",
		}
		configureTrustedIngressPeers(denied)
		grantClusterNetworkPublicationAccess(t, namespace, denied.Name, username)
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec.ingress.className") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantClusterScopedResourceVerbs(
			t,
			username,
			"ingressclass-use-"+username,
			"networking.k8s.io",
			"ingressclasses",
			[]string{className},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-ingress-class-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled:   true,
			ClassName: ptr.To(className),
			Host:      "bao.example.com",
		}
		configureTrustedIngressPeers(allowed)
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected ingress-class-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("ingress-tls-secret-use", func(t *testing.T) {
		const secretName = "tenant-ingress-tls"

		denied := newMinimalClusterObj(namespace, "cluster-ingress-tls-secret-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled:       true,
			Host:          "bao.example.com",
			TLSSecretName: secretName,
		}
		configureTrustedIngressPeers(denied)
		grantClusterNetworkPublicationAccess(t, namespace, denied.Name, username)
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec.ingress.tlsSecretName") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"ingress-tls-secret-use-access",
			"",
			"secrets",
			[]string{secretName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-ingress-tls-secret-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled:       true,
			Host:          "bao.example.com",
			TLSSecretName: secretName,
		}
		configureTrustedIngressPeers(allowed)
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected ingress-tls-secret-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("gateway-use", func(t *testing.T) {
		const gatewayName = "tenant-gateway"

		denied := newMinimalClusterObj(namespace, "cluster-gateway-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
			Enabled: true,
			GatewayRef: openbaov1alpha1.GatewayReference{
				Name: gatewayName,
			},
			Hostname: "bao.example.com",
		}
		grantClusterNetworkPublicationAccess(t, namespace, denied.Name, username)
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec.gateway.gatewayRef") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"gateway-use-access",
			"gateway.networking.k8s.io",
			"gateways",
			[]string{gatewayName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-gateway-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
			Enabled: true,
			GatewayRef: openbaov1alpha1.GatewayReference{
				Name: gatewayName,
			},
			Hostname: "bao.example.com",
		}
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected gateway-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("existing-pvc-use", func(t *testing.T) {
		const pvcName = "tenant-audit-pvc"

		denied := newMinimalClusterObj(namespace, "cluster-existing-pvc-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.AuditFileStorage = &openbaov1alpha1.AuditFileStorageConfig{
			Mode:              openbaov1alpha1.AuditFileStorageModeExistingPVC,
			ExistingClaimName: pvcName,
		}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "existing PVC references") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"existing-pvc-use-access",
			"",
			"persistentvolumeclaims",
			[]string{pvcName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-existing-pvc-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.AuditFileStorage = &openbaov1alpha1.AuditFileStorageConfig{
			Mode:              openbaov1alpha1.AuditFileStorageModeExistingPVC,
			ExistingClaimName: pvcName,
		}
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected existing-pvc-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("storage-class-use", func(t *testing.T) {
		const storageClassName = "tenant-fast-storage"

		denied := newMinimalClusterObj(namespace, "cluster-storageclass-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Storage.StorageClassName = ptr.To(storageClassName)
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "StorageClass references") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantClusterScopedResourceVerbs(
			t,
			username,
			"storageclass-use-"+username,
			"storage.k8s.io",
			"storageclasses",
			[]string{storageClassName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-storageclass-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Storage.StorageClassName = ptr.To(storageClassName)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected storage-class-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("image-verification-pull-secret-get", func(t *testing.T) {
		const secretName = "tenant-verification-pull-secret"

		denied := newMinimalClusterObj(namespace, "cluster-image-verification-pull-secret-get-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
			Enabled:          true,
			FailurePolicy:    "Block",
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: secretName}},
		}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "image verification pull Secrets") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"image-verification-pull-secret-get-access",
			"",
			"secrets",
			[]string{secretName},
			"get",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-image-verification-pull-secret-get-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
			Enabled:          true,
			FailurePolicy:    "Block",
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: secretName}},
		}
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected image-verification-pull-secret-get-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("servicemonitor-tls-secret-use", func(t *testing.T) {
		const secretName = "tenant-servicemonitor-ca"

		denied := newMinimalClusterObj(namespace, "cluster-servicemonitor-tls-secret-use-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{
			Metrics: &openbaov1alpha1.MetricsConfig{
				Enabled: true,
				ServiceMonitor: &openbaov1alpha1.ServiceMonitorConfig{
					Enabled: true,
					TLSConfig: &openbaov1alpha1.ServiceMonitorTLSConfig{
						CASecret: &openbaov1alpha1.ServiceMonitorKeySelector{
							Name: secretName,
							Key:  "ca.crt",
						},
					},
				},
			},
		}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "ServiceMonitor TLS references") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"servicemonitor-tls-secret-use-access",
			"",
			"secrets",
			[]string{secretName},
			"use",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-servicemonitor-tls-secret-use-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Observability = &openbaov1alpha1.ObservabilityConfig{
			Metrics: &openbaov1alpha1.MetricsConfig{
				Enabled: true,
				ServiceMonitor: &openbaov1alpha1.ServiceMonitorConfig{
					Enabled: true,
					TLSConfig: &openbaov1alpha1.ServiceMonitorTLSConfig{
						CASecret: &openbaov1alpha1.ServiceMonitorKeySelector{
							Name: secretName,
							Key:  "ca.crt",
						},
					},
				},
			},
		}
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected servicemonitor-tls-secret-use-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})

	t.Run("backup-credentials-secret-get", func(t *testing.T) {
		const secretName = "tenant-backup-creds"

		denied := newMinimalClusterObj(namespace, "cluster-backup-credentials-secret-get-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Backup = &openbaov1alpha1.BackupSchedule{
			Schedule:    "0 0 * * *",
			JWTAuthRole: "backup-role",
			Target: openbaov1alpha1.BackupTarget{
				Provider: "s3",
				Endpoint: "https://objectstore.example.com",
				Bucket:   testBackupBucket,
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: secretName,
				},
			},
		}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "backup credentials") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"backup-credentials-secret-get-access",
			"",
			"secrets",
			[]string{secretName},
			"get",
		)

		allowed := newMinimalClusterObj(namespace, "cluster-backup-credentials-secret-get-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Backup = &openbaov1alpha1.BackupSchedule{
			Schedule:    "0 0 * * *",
			JWTAuthRole: "backup-role",
			Target: openbaov1alpha1.BackupTarget{
				Provider: "s3",
				Endpoint: "https://objectstore.example.com",
				Bucket:   testBackupBucket,
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: secretName,
				},
			},
		}
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected backup-credentials-secret-get-authorized OpenBaoCluster create to succeed, got: %v", err)
		}
	})
}

func TestVAP_OpenBaoCluster_RequiresNetworkPublicationAuthorization(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "network-publication-editor"
	tenantClient := newImpersonatedClient(t, username)
	grantTenantOpenBaoWriteAccess(t, namespace, username)

	configureTrustedIngressPeers := func(cluster *openbaov1alpha1.OpenBaoCluster) {
		cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
			TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": "ingress-system",
						},
					},
				},
			},
		}
	}

	t.Run("service-loadbalancer", func(t *testing.T) {
		denied := newMinimalClusterObj(namespace, "netpub-svc-lb-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Service = &openbaov1alpha1.ServiceConfig{Type: corev1.ServiceTypeLoadBalancer}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "publish networking") {
			t.Fatalf("unexpected error message: %v", err)
		}

		allowed := newMinimalClusterObj(namespace, "netpub-svc-lb-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Service = &openbaov1alpha1.ServiceConfig{Type: corev1.ServiceTypeLoadBalancer}
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected publishnetworking-authorized LoadBalancer Service to succeed, got: %v", err)
		}
	})

	t.Run("service-annotations", func(t *testing.T) {
		denied := newMinimalClusterObj(namespace, "netpub-svc-anno-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Service = &openbaov1alpha1.ServiceConfig{
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
			},
		}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "publish networking") {
			t.Fatalf("unexpected error message: %v", err)
		}

		allowed := newMinimalClusterObj(namespace, "netpub-svc-anno-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Service = &openbaov1alpha1.ServiceConfig{
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
			},
		}
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected publishnetworking-authorized Service annotations to succeed, got: %v", err)
		}
	})

	t.Run("read-replica-nodeport", func(t *testing.T) {
		denied := newMinimalClusterObj(namespace, "netpub-rr-nodeport-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{
			Replicas: 1,
			Service: &openbaov1alpha1.ReadReplicaServiceConfig{
				Enabled: true,
				Type:    corev1.ServiceTypeNodePort,
			},
		}
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "publish networking") {
			t.Fatalf("unexpected error message: %v", err)
		}

		allowed := newMinimalClusterObj(namespace, "netpub-rr-nodeport-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{
			Replicas: 1,
			Service: &openbaov1alpha1.ReadReplicaServiceConfig{
				Enabled: true,
				Type:    corev1.ServiceTypeNodePort,
			},
		}
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected publishnetworking-authorized read-replica Service to succeed, got: %v", err)
		}
	})

	t.Run("ingress-enabled", func(t *testing.T) {
		denied := newMinimalClusterObj(namespace, "netpub-ingress-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled: true,
			Host:    "bao.example.com",
		}
		configureTrustedIngressPeers(denied)
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "publish networking") {
			t.Fatalf("unexpected error message: %v", err)
		}

		allowed := newMinimalClusterObj(namespace, "netpub-ingress-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Ingress = &openbaov1alpha1.IngressConfig{
			Enabled: true,
			Host:    "bao.example.com",
		}
		configureTrustedIngressPeers(allowed)
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected publishnetworking-authorized Ingress to succeed, got: %v", err)
		}
	})

	t.Run("gateway-enabled", func(t *testing.T) {
		const gatewayName = "tenant-gateway"

		denied := newMinimalClusterObj(namespace, "netpub-gateway-denied")
		denied.Spec.InitContainer = nil
		denied.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
			Enabled: true,
			GatewayRef: openbaov1alpha1.GatewayReference{
				Name: gatewayName,
			},
			Hostname: "bao.example.com",
		}
		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"gateway-use-for-netpub-denied",
			"gateway.networking.k8s.io",
			"gateways",
			[]string{gatewayName},
			"use",
		)
		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "publish networking") {
			t.Fatalf("unexpected error message: %v", err)
		}

		allowed := newMinimalClusterObj(namespace, "netpub-gateway-allowed")
		allowed.Spec.InitContainer = nil
		allowed.Spec.Gateway = &openbaov1alpha1.GatewayConfig{
			Enabled: true,
			GatewayRef: openbaov1alpha1.GatewayReference{
				Name: gatewayName,
			},
			Hostname: "bao.example.com",
		}
		grantClusterNetworkPublicationAccess(t, namespace, allowed.Name, username)
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected publishnetworking-authorized Gateway to succeed, got: %v", err)
		}
	})
}

func TestVAP_OpenBaoCluster_RequiresCloudIdentityAuthorization(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "cloud-identity-editor"
	tenantClient := newImpersonatedClient(t, username)
	grantTenantOpenBaoWriteAccess(t, namespace, username)

	denied := newMinimalClusterObj(namespace, "cluster-cloud-identity-denied")
	denied.Spec.InitContainer = nil
	denied.Spec.ServiceAccount = &openbaov1alpha1.ServiceAccountConfig{
		Annotations: map[string]string{
			"iam.amazonaws.com/role": "openbao-runtime",
		},
	}
	err := tenantClient.Create(ctx, denied, client.DryRunAll)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "use cloud identities") {
		t.Fatalf("unexpected error message: %v", err)
	}

	clusterName := "cluster-cloud-identity-allowed"
	grantClusterCloudIdentitiesAccess(t, namespace, clusterName, username)
	allowed := newMinimalClusterObj(namespace, clusterName)
	allowed.Spec.InitContainer = nil
	allowed.Spec.ServiceAccount = &openbaov1alpha1.ServiceAccountConfig{
		Annotations: map[string]string{
			"iam.amazonaws.com/role": "openbao-runtime",
		},
	}
	if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
		t.Fatalf("expected cloud-identity-authorized OpenBaoCluster create to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_RejectsOIDCBootstrapWithoutSelfInitEnabled(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-oidc-without-self-init")
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: false,
		OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
			Enabled: true,
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "spec.selfInit.oidc.enabled requires spec.selfInit.enabled to be true") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCRD_OpenBaoCluster_ValidatesOIDCAdditionalSubjects(t *testing.T) {
	namespace := newTestNamespace(t)

	t.Run("rejects malformed service account subject", func(t *testing.T) {
		cluster := newMinimalClusterObj(namespace, "cluster-invalid-additional-subject")
		cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
			Enabled: true,
			OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
				Enabled: true,
				AdditionalSubjects: &openbaov1alpha1.SelfInitOIDCAdditionalSubjects{
					Backup: []openbaov1alpha1.KubernetesServiceAccountSubject{"recovery:backup"},
				},
			},
		}

		err := k8sClient.Create(ctx, cluster, client.DryRunAll)
		requireInvalidRequest(t, err)
		if !strings.Contains(err.Error(), "additionalSubjects.backup") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("requires OIDC bootstrap", func(t *testing.T) {
		cluster := newMinimalClusterObj(namespace, "cluster-disabled-additional-subject")
		cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
			Enabled: true,
			OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
				Enabled: false,
				AdditionalSubjects: &openbaov1alpha1.SelfInitOIDCAdditionalSubjects{
					Restore: []openbaov1alpha1.KubernetesServiceAccountSubject{
						"system:serviceaccount:recovery:target-restore-serviceaccount",
					},
				},
			},
		}

		err := k8sClient.Create(ctx, cluster, client.DryRunAll)
		requireInvalidRequest(t, err)
		if !strings.Contains(err.Error(), "spec.selfInit.oidc.additionalSubjects requires spec.selfInit.oidc.enabled=true") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestVAP_OpenBaoCluster_RequiresTrustedIngressPeersForManagedIngress(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-ingress-without-peer")
	cluster.Spec.Ingress = &openbaov1alpha1.IngressConfig{
		Enabled: true,
		Host:    "bao.example.com",
	}

	err := k8sClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	wantMessage := "spec.ingress.enabled requires at least one spec.network.trustedIngressPeers"
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("unexpected error message: %v", err)
	}

	allowed := newMinimalClusterObj(namespace, "cluster-ingress-with-peer")
	allowed.Spec.Ingress = &openbaov1alpha1.IngressConfig{
		Enabled: true,
		Host:    "bao.example.com",
	}
	allowed.Spec.Network = &openbaov1alpha1.NetworkConfig{
		TrustedIngressPeers: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "ingress-system",
					},
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, allowed); err != nil {
		t.Fatalf("expected ingress with trusted ingress peers to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_DeniesCustomBackupImageWithoutCustomExecutablesVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "backup-image-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	cluster := newMinimalClusterObj(namespace, "cluster-custom-backup-image-denied")
	cluster.Spec.InitContainer = nil
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		Image:       "ghcr.io/attacker/backup-exfil:latest",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider: "s3",
			Endpoint: "https://objectstore.example.com",
			Bucket:   testBackupBucket,
		},
	}

	err := tenantClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "CR-selected custom executables") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_DeniesBackupImageChangeWithoutCustomExecutablesVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-custom-backup-image-update-denied")
	cluster.Spec.InitContainer = nil
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	username := "backup-image-update-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	if err := tenantClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoCluster as tenant editor: %v", err)
	}
	original := latest.DeepCopy()
	latest.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		Image:       "ghcr.io/attacker/backup-exfil:latest",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider: "s3",
			Endpoint: "https://objectstore.example.com",
			Bucket:   testBackupBucket,
		},
	}

	err := tenantClient.Patch(ctx, &latest, client.MergeFrom(original))
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "CR-selected custom executables") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_AllowsCustomBackupImageWithHelperImageVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "backup-image-delegate"
	clusterName := "cluster-custom-backup-image-allowed"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterHelperImageAccess(t, namespace, clusterName, username)
	tenantClient := newImpersonatedClient(t, username)

	cluster := newMinimalClusterObj(namespace, clusterName)
	cluster.Spec.InitContainer = nil
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		Image:       "ghcr.io/platform/backup-helper:1.2.3",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider: "s3",
			Endpoint: "https://objectstore.example.com",
			Bucket:   testBackupBucket,
		},
	}

	if err := tenantClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected helper-image-authorized OpenBaoCluster create to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_DeniesUnchangedCustomBackupImageWithoutCustomExecutablesVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-custom-backup-image-unchanged")
	cluster.Spec.InitContainer = nil
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		Image:       "ghcr.io/platform/backup-helper:1.2.3",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider: "s3",
			Endpoint: "https://objectstore.example.com",
			Bucket:   testBackupBucket,
		},
	}
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create platform-authored OpenBaoCluster with custom helper image: %v", err)
	}

	username := "backup-image-standard-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	if err := tenantClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoCluster as tenant editor: %v", err)
	}
	original := latest.DeepCopy()
	latest.Spec.Backup.Schedule = "0 1 * * *"

	err := tenantClient.Patch(ctx, &latest, client.MergeFrom(original))
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "CR-selected custom executables") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_DeniesCustomExecutableFieldsWithoutDelegatedVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "custom-executables-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	tests := []struct {
		name      string
		configure func(*openbaov1alpha1.OpenBaoCluster)
	}{
		{
			name: "custom-init-image",
			configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.InitContainer = &openbaov1alpha1.InitContainerConfig{
					Enabled: true,
					Image:   "ghcr.io/attacker/openbao-init:latest",
				}
			},
		},
		{
			name: "custom-upgrade-image",
			configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
					Image: "ghcr.io/attacker/openbao-upgrade:latest",
				}
			},
		},
		{
			name: "bluegreen-hook",
			configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
					Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
					BlueGreen: &openbaov1alpha1.BlueGreenConfig{
						Verification: &openbaov1alpha1.VerificationConfig{
							PrePromotionHook: &openbaov1alpha1.ValidationHookConfig{
								Image: "ghcr.io/attacker/validation-hook:latest",
							},
						},
					},
				}
			},
		},
		{
			name: "plugin-image",
			configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				cluster.Spec.Plugins = []openbaov1alpha1.Plugin{
					newTestPluginWithImage("ghcr.io/attacker/openbao-plugin:latest"),
				}
			},
		},
		{
			name: "plugin-command",
			configure: func(cluster *openbaov1alpha1.OpenBaoCluster) {
				plugin := newTestPluginWithImage("")
				plugin.Command = "attacker-plugin"
				cluster.Spec.Plugins = []openbaov1alpha1.Plugin{plugin}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, "cluster-custom-executables-"+tt.name)
			cluster.Spec.InitContainer = nil
			tt.configure(cluster)

			err := tenantClient.Create(ctx, cluster)
			requireAdmissionDenied(t, err)
			if !strings.Contains(err.Error(), "CR-selected custom executables") {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestVAP_OpenBaoCluster_AllowsCustomExecutableFieldsWithDelegatedVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "custom-executables-delegate"
	clusterName := "cluster-custom-executables-allowed"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterCustomExecutablesAccess(t, namespace, clusterName, username)
	tenantClient := newImpersonatedClient(t, username)

	cluster := newMinimalClusterObj(namespace, clusterName)
	cluster.Spec.InitContainer = &openbaov1alpha1.InitContainerConfig{
		Enabled: true,
		Image:   "ghcr.io/platform/openbao-init:1.2.3",
	}
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		Image:       "ghcr.io/platform/openbao-backup:1.2.3",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider: "s3",
			Endpoint: "https://objectstore.example.com",
			Bucket:   testBackupBucket,
		},
	}
	cluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
		Image:    "ghcr.io/platform/openbao-upgrade:1.2.3",
		Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
		BlueGreen: &openbaov1alpha1.BlueGreenConfig{
			Verification: &openbaov1alpha1.VerificationConfig{
				PrePromotionHook: &openbaov1alpha1.ValidationHookConfig{
					Image: "ghcr.io/platform/openbao-validation-hook:1.2.3",
				},
			},
		},
	}
	cluster.Spec.Plugins = []openbaov1alpha1.Plugin{
		newTestPluginWithImage("ghcr.io/platform/openbao-plugin:1.2.3"),
	}

	if err := tenantClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected custom-executables-authorized OpenBaoCluster create to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_AllowsControllerMetadataPatchWithTenantDelegation(t *testing.T) {
	namespace := newTestNamespace(t)
	ensureProvisionerRBACApplied(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	provisionerClient := newImpersonatedClient(t, provisionerUsername)
	applyClientObject(t, provisionerClient, provisionerpkg.GenerateTenantRole(namespace))
	applyClientObject(t, provisionerClient, provisionerpkg.GenerateTenantRoleBinding(
		namespace,
		provisionerpkg.OperatorServiceAccount{
			Name:      testControllerSAName,
			Namespace: testDefaultOperatorNS,
		},
	))

	clusterName := "cluster-controller-metadata-patch"
	setupUsername := "controller-metadata-setup"
	grantTenantOpenBaoWriteAccess(t, namespace, setupUsername)
	grantClusterCustomExecutablesAccess(t, namespace, clusterName, setupUsername)
	grantClusterImageTrustRootsAccess(t, namespace, clusterName, setupUsername)
	setupClient := newImpersonatedClient(t, setupUsername)

	cluster := newValidHardenedAdmissionCluster(namespace, clusterName)
	cluster.Spec.InitContainer = &openbaov1alpha1.InitContainerConfig{
		Enabled: true,
		Image:   "ghcr.io/platform/openbao-init:1.2.3",
	}
	cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled:       true,
		FailurePolicy: "Block",
		IssuerRegExp:  "^https://issuer.example.com$",
		SubjectRegExp: "^https://github.com/example/repo/.github/workflows/release.yml@refs/tags/.+$",
	}

	if err := setupClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create setup OpenBaoCluster: %v", err)
	}

	controllerClient := newImpersonatedClient(t, controllerUsername)
	var latest openbaov1alpha1.OpenBaoCluster
	if err := controllerClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterName}, &latest); err != nil {
		t.Fatalf("controller get OpenBaoCluster: %v", err)
	}
	original := latest.DeepCopy()
	latest.Finalizers = append(latest.Finalizers, openbaov1alpha1.OpenBaoClusterFinalizer)

	if err := controllerClient.Patch(ctx, &latest, client.MergeFrom(original)); err != nil {
		t.Fatalf("controller metadata patch with generated tenant delegation should succeed, got: %v", err)
	}
}

func newTestPluginWithImage(image string) openbaov1alpha1.Plugin {
	return openbaov1alpha1.Plugin{
		Type:       "secret",
		Name:       "test-plugin",
		Image:      image,
		Version:    "1.2.3",
		BinaryName: "test-plugin",
		SHA256Sum:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestCRD_OpenBaoCluster_RejectsHAACMEWithoutSharedCache(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "cluster-acme-ha-missing-cache")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		DirectoryURL: "https://acme.example/directory",
		Domains:      []string{"bao.example.com"},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
	if !strings.Contains(err.Error(), "HA ACME clusters require spec.tls.acme.sharedCache") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCRD_OpenBaoCluster_RejectsSharedCacheOutsideACME(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "cluster-non-acme-shared-cache")
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeExternal
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{
		SharedCache: &openbaov1alpha1.ACMESharedCacheConfig{
			Mode: openbaov1alpha1.ACMESharedCacheModeManagedPVC,
			Size: "1Gi",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
	if !strings.Contains(err.Error(), "spec.tls.acme.sharedCache is only supported when spec.tls.mode is ACME") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCRD_OpenBaoCluster_RejectsOCIKMSCredentialsSecretWithoutAPIKeyMode(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "cluster-ocikms-secret-without-api-key")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type:                 "ocikms",
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: "oci-creds"},
		OCIKMS: &openbaov1alpha1.OCIKMSSealConfig{
			KeyID:              "ocid1.key.oc1..example",
			CryptoEndpoint:     "https://kms.us-ashburn-1.oraclecloud.com",
			ManagementEndpoint: "https://kms.us-ashburn-1.oraclecloud.com",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
	wantMessage := "spec.unseal.credentialsSecretRef for ocikms requires spec.unseal.ocikms.authTypeAPIKey=true"
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCRD_OpenBaoCluster_RejectsPKCS11WithoutSlotOrTokenLabel(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "cluster-pkcs11-missing-slot-tokenlabel")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "pkcs11",
		PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib:      "/usr/lib/libpkcs11.so",
			KeyLabel: "openbao-key",
			PIN:      "1234",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
	if !strings.Contains(err.Error(), "spec.unseal.pkcs11.slot or spec.unseal.pkcs11.tokenLabel is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCRD_OpenBaoCluster_RejectsPKCS11WithSlotAndTokenLabel(t *testing.T) {
	namespace := newTestNamespace(t)

	cluster := newMinimalClusterObj(namespace, "cluster-pkcs11-slot-and-tokenlabel")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "pkcs11",
		PKCS11: &openbaov1alpha1.PKCS11SealConfig{
			Lib:        "/usr/lib/libpkcs11.so",
			Slot:       "0",
			TokenLabel: "openbao-token",
			KeyLabel:   "openbao-key",
			PIN:        "1234",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireInvalidRequest(t, err)
	if !strings.Contains(err.Error(), "spec.unseal.pkcs11.slot and spec.unseal.pkcs11.tokenLabel are mutually exclusive") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_RejectsHardenedTransitAddressWithoutHTTPS(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-hardened-transit-http")
	cluster.Spec.Profile = openbaov1alpha1.ProfileHardened
	cluster.Spec.ControllerJWTMode = openbaov1alpha1.ControllerJWTModeTarget
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeExternal
	cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
		Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true},
	}
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "transit",
		Transit: &openbaov1alpha1.TransitSealConfig{
			Address:   "http://infra-bao.example",
			KeyName:   "autounseal",
			MountPath: "transit/",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "Transit unseal address must use HTTPS") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_RejectsTransitClientCertWithoutKey(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-transit-client-cert-without-key")
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "transit",
		Transit: &openbaov1alpha1.TransitSealConfig{
			Address:       "https://infra-bao.example",
			KeyName:       "autounseal",
			MountPath:     "transit/",
			TLSClientCert: "/etc/bao/seal-creds/client.crt",
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	wantMessage := "spec.unseal.transit.tlsClientCert and spec.unseal.transit.tlsClientKey must be set together"
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_AllowsHardenedOfficialImageVerificationDefaults(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newValidHardenedAdmissionCluster(namespace, "cluster-hardened-image-verification-defaults")
	cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled:       true,
		FailurePolicy: "Block",
	}
	cluster.Spec.OperatorImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled:       true,
		FailurePolicy: "Block",
	}

	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf(
			"expected Hardened OpenBaoCluster with enabled official image verification defaults to succeed, got: %v",
			err,
		)
	}
}

func TestVAP_OpenBaoCluster_DeniesHardenedCustomImageTrustRootsWithoutDelegatedVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "image-trust-root-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	cluster := newValidHardenedAdmissionCluster(namespace, "cluster-hardened-custom-trust-root-denied")
	cluster.Spec.InitContainer = nil
	cluster.Spec.ImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled:       true,
		FailurePolicy: "Block",
		IssuerRegExp:  "^https://issuer.example.com$",
		SubjectRegExp: "^https://github.com/example/repo/.github/workflows/release.yml@refs/tags/.+$",
		IgnoreTlog:    true,
	}

	err := tenantClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "custom image verification trust roots") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_AllowsHardenedCustomImageTrustRootsWithDelegatedVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	username := "image-trust-root-delegate"
	clusterName := "cluster-hardened-custom-trust-root-allowed"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterImageTrustRootsAccess(t, namespace, clusterName, username)
	tenantClient := newImpersonatedClient(t, username)

	cluster := newValidHardenedAdmissionCluster(namespace, clusterName)
	cluster.Spec.InitContainer = nil
	cluster.Spec.OperatorImageVerification = &openbaov1alpha1.ImageVerificationConfig{
		Enabled:       true,
		FailurePolicy: "Block",
		PublicKey:     "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A\n-----END PUBLIC KEY-----",
	}

	if err := tenantClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected image-trust-root-authorized OpenBaoCluster create to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_AllowsHardenedSafeSecurityContextOverrides(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newValidHardenedAdmissionCluster(namespace, "cluster-hardened-safe-security")
	cluster.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot:        ptr.To(true),
		RunAsUser:           ptr.To(int64(1001)),
		RunAsGroup:          ptr.To(int64(1001)),
		FSGroup:             ptr.To(int64(1001)),
		FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
		SupplementalGroups:  []int64{1002},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("expected Hardened OpenBaoCluster with safe securityContext overrides to succeed, got: %v", err)
	}
}

func newValidHardenedAdmissionCluster(namespace, name string) *openbaov1alpha1.OpenBaoCluster {
	return hardenedfixtures.NewValidCluster(namespace, name)
}

func TestVAP_OpenBaoCluster_RejectsDisabledInitContainerOverride(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)

	for attempt := 0; attempt < 25; attempt++ {
		cluster := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "openbao.org/v1alpha1",
				"kind":       "OpenBaoCluster",
				"metadata": map[string]any{
					"name":      fmt.Sprintf("cluster-disabled-init-override-%d", attempt),
					"namespace": namespace,
				},
				"spec": map[string]any{
					"version":           testOpenBaoVersion244,
					"image":             testOpenBaoImage244,
					"replicas":          int64(3),
					"profile":           "Development",
					"controllerJWTMode": "Shared",
					"tls": map[string]any{
						"enabled":        true,
						"rotationPeriod": "720h",
					},
					"storage": map[string]any{
						"size": "10Gi",
					},
					"initContainer": map[string]any{
						"enabled": false,
					},
				},
			},
		}

		err := k8sClient.Create(ctx, cluster)
		if err == nil {
			_ = k8sClient.Delete(ctx, cluster)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		wantMessage := "spec.initContainer is optional; when set, spec.initContainer.enabled must be true."
		if !strings.Contains(err.Error(), wantMessage) {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny OpenBaoCluster create with disabled initContainer override after retries")
}

func TestVAP_OpenBaoCluster_RejectsDowngradeBelowCurrentVersion(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-downgrade-current-version")
	cluster.Spec.Version = testOpenBaoVersion250
	cluster.Spec.Image = testOpenBaoImage250
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.CurrentVersion = testOpenBaoVersion250
	})

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoCluster: %v", err)
	}

	original := latest.DeepCopy()
	latest.Spec.Version = testOpenBaoVersion244
	latest.Spec.Image = testOpenBaoImage244

	err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original))
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "spec.version cannot be downgraded below status.currentVersion.") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_RejectsRollingTargetRegressionAfterRolloutStarts(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-rolling-target-regression")
	cluster.Spec.Version = "2.6.0"
	cluster.Spec.Image = "openbao/openbao:2.6.0"
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.CurrentVersion = testOpenBaoVersion244
		status.Upgrade = &openbaov1alpha1.UpgradeProgress{
			FromVersion:      testOpenBaoVersion244,
			TargetVersion:    "2.6.0",
			CurrentPartition: 2,
			CompletedPods:    []int32{2},
		}
	})

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoCluster: %v", err)
	}

	original := latest.DeepCopy()
	latest.Spec.Version = testOpenBaoVersion250
	latest.Spec.Image = testOpenBaoImage250

	err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original))
	requireAdmissionDenied(t, err)
	wantMessage := "spec.version cannot be reduced below status.upgrade.targetVersion after rolling progress has started."
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_RejectsNumericBackupEndpoint(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-backup-numeric-endpoint")
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Schedule:    "0 0 * * *",
		Image:       "ghcr.io/kubebao/openbao-backup:1.0.0",
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Endpoint: "http://2130706433:9000",
			Bucket:   testBackupBucket,
			CredentialsSecretRef: &corev1.LocalObjectReference{
				Name: "backup-creds",
			},
		},
	}

	err := k8sClient.Create(ctx, cluster)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "numeric IP encoding") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoCluster_RejectsBackupEndpointSSRFBypasses(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	tests := []struct {
		name        string
		endpoint    string
		wantMessage string
	}{
		{
			name:        "uppercase-scheme-link-local",
			endpoint:    "HTTP://169.254.169.254/latest/meta-data",
			wantMessage: "Backup endpoint cannot point to link-local addresses",
		},
		{
			name:        "userinfo-link-local",
			endpoint:    "http://storage.example.com@169.254.169.254/latest/meta-data",
			wantMessage: "Backup endpoint cannot point to link-local addresses",
		},
		{
			name:        "ipv4-mapped-ipv6-link-local",
			endpoint:    "HTTP://[::ffff:169.254.169.254]/latest/meta-data",
			wantMessage: "numeric IP encoding",
		},
		{
			name:        "shorthand-loopback",
			endpoint:    "http://127.1:9000",
			wantMessage: "numeric IP encoding",
		},
		{
			name:        "hex-loopback",
			endpoint:    "http://0x7f000001:9000",
			wantMessage: "numeric IP encoding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(namespace, "cluster-backup-ssrf-"+tt.name)
			cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
				Schedule:    "0 0 * * *",
				Image:       "ghcr.io/kubebao/openbao-backup:1.0.0",
				JWTAuthRole: "backup-role",
				Target: openbaov1alpha1.BackupTarget{
					Endpoint: tt.endpoint,
					Bucket:   testBackupBucket,
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: "backup-creds",
					},
				},
			}

			err := k8sClient.Create(ctx, cluster)
			requireAdmissionDenied(t, err)
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("unexpected error message: %v", err)
			}
		})
	}
}

func TestVAP_OpenBaoRestore_DeniesRestoreWithoutTargetClusterRestoreVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	username := "restore-target-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-target-denied",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "target-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Endpoint: "https://objectstore.example.com",
					Bucket:   testBackupBucket,
				},
				Key: "clusters/prod/snapshot.snap",
			},
			JWTAuthRole: "restore-role",
			Force:       true,
		},
	}

	err := tenantClient.Create(ctx, restore)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "must be authorized to restore the target OpenBaoCluster") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoRestore_RequiresReferenceAuthorization(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	username := "restore-reference-editor"
	clusterName := "target-cluster"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterRestoreAccess(t, namespace, clusterName, username)
	tenantClient := newImpersonatedClient(t, username)

	t.Run("restore-credentials-secret-get", func(t *testing.T) {
		const credentialsSecretName = "tenant-restore-creds"
		const tokenSecretName = "tenant-restore-token"

		denied := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-credentials-secret-get-denied",
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{
				Cluster: clusterName,
				Source: openbaov1alpha1.RestoreSource{
					Target: openbaov1alpha1.BackupTarget{
						Provider: "s3",
						Endpoint: "https://objectstore.example.com",
						Bucket:   testBackupBucket,
						CredentialsSecretRef: &corev1.LocalObjectReference{
							Name: credentialsSecretName,
						},
					},
					Key: "clusters/prod/snapshot.snap",
				},
				TokenSecretRef: &corev1.LocalObjectReference{
					Name: tokenSecretName,
				},
				Force: true,
			},
		}

		err := tenantClient.Create(ctx, denied, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "restore credentials") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantNamespacedResourceVerbs(
			t,
			namespace,
			username,
			"restore-credentials-secret-get-access",
			"",
			"secrets",
			[]string{credentialsSecretName, tokenSecretName},
			"get",
		)

		allowed := denied.DeepCopy()
		allowed.Name = "restore-credentials-secret-get-allowed"
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected restore-credentials-secret-get-authorized OpenBaoRestore create to succeed, got: %v", err)
		}
	})

	t.Run("restore-cloud-identity", func(t *testing.T) {
		restore := &openbaov1alpha1.OpenBaoRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-cloud-identity-denied",
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{
				Cluster: clusterName,
				Source: openbaov1alpha1.RestoreSource{
					Target: openbaov1alpha1.BackupTarget{
						Provider: "s3",
						Endpoint: "https://objectstore.example.com",
						Bucket:   testBackupBucket,
						RoleARN:  "arn:aws:iam::123456789012:role/openbao-restore",
					},
					Key: "clusters/prod/snapshot.snap",
				},
				JWTAuthRole: "restore-role",
				Force:       true,
			},
		}

		err := tenantClient.Create(ctx, restore, client.DryRunAll)
		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "use cloud identities") {
			t.Fatalf("unexpected error message: %v", err)
		}

		grantClusterCloudIdentitiesAccess(t, namespace, clusterName, username)

		allowed := restore.DeepCopy()
		allowed.Name = "restore-cloud-identity-allowed"
		if err := tenantClient.Create(ctx, allowed, client.DryRunAll); err != nil {
			t.Fatalf("expected restore-cloud-identity-authorized OpenBaoRestore create to succeed, got: %v", err)
		}
	})
}

func TestVAP_OpenBaoRestore_DeniesCustomImageWithoutHelperImageVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	username := "restore-image-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterRestoreAccess(t, namespace, "target-cluster", username)
	tenantClient := newImpersonatedClient(t, username)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-custom-image-denied",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "target-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Endpoint: "https://objectstore.example.com",
					Bucket:   testBackupBucket,
				},
				Key: "clusters/prod/snapshot.snap",
			},
			JWTAuthRole: "restore-role",
			Image:       "ghcr.io/attacker/restore-exfil:latest",
			Force:       true,
		},
	}

	err := tenantClient.Create(ctx, restore)
	requireAdmissionDenied(t, err)
	if !strings.Contains(err.Error(), "custom restore helper images") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestVAP_OpenBaoRestore_AllowsCustomImageWithHelperImageVerb(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	username := "restore-image-delegate"
	clusterName := "target-cluster"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	grantClusterHelperImageAccess(t, namespace, clusterName, username)
	grantClusterRestoreAccess(t, namespace, clusterName, username)
	tenantClient := newImpersonatedClient(t, username)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-custom-image-allowed",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: clusterName,
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Endpoint: "https://objectstore.example.com",
					Bucket:   testBackupBucket,
				},
				Key: "clusters/prod/snapshot.snap",
			},
			JWTAuthRole: "restore-role",
			Image:       "ghcr.io/platform/backup-helper:1.2.3",
			Force:       true,
		},
	}

	if err := tenantClient.Create(ctx, restore); err != nil {
		t.Fatalf("expected helper-image-authorized OpenBaoRestore create to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoRestore_AllowsMetadataUpdateWithoutRedelegatingCustomImage(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	clusterName := "target-cluster"
	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-custom-image-unchanged",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: clusterName,
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Endpoint: "https://objectstore.example.com",
					Bucket:   testBackupBucket,
				},
				Key: "clusters/prod/snapshot.snap",
			},
			JWTAuthRole: "restore-role",
			Image:       "ghcr.io/platform/backup-helper:1.2.3",
			Force:       true,
		},
	}
	if err := k8sClient.Create(ctx, restore); err != nil {
		t.Fatalf("create platform-authored OpenBaoRestore with custom helper image: %v", err)
	}

	username := "restore-image-standard-editor"
	grantTenantOpenBaoWriteAccess(t, namespace, username)
	tenantClient := newImpersonatedClient(t, username)

	var latest openbaov1alpha1.OpenBaoRestore
	key := types.NamespacedName{Namespace: namespace, Name: restore.Name}
	if err := tenantClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoRestore as tenant editor: %v", err)
	}
	original := latest.DeepCopy()
	latest.Annotations = map[string]string{"openbao.org/test": "metadata-update"}
	// Metadata writes include recovery acknowledgements and still require
	// target restore authority, even when immutable inputs were delegated earlier.
	requireAdmissionDenied(t, tenantClient.Patch(ctx, &latest, client.MergeFrom(original), client.DryRunAll))
	grantClusterRestoreAccess(t, namespace, clusterName, username)

	if err := tenantClient.Patch(ctx, &latest, client.MergeFrom(original)); err != nil {
		t.Fatalf("metadata-only update must not redelegate the immutable spec: %v", err)
	}
}

func TestVAP_OpenBaoRestore_RejectsUnsafeEndpoints(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)
	namespace := newTestNamespace(t)

	tests := []struct {
		name        string
		endpoint    string
		wantMessage string
	}{
		{
			name:        "link-local",
			endpoint:    "http://169.254.169.254/latest/meta-data",
			wantMessage: "Restore endpoint cannot point to link-local addresses",
		},
		{
			name:        "uppercase-scheme-link-local",
			endpoint:    "HTTPS://169.254.169.254/latest/meta-data",
			wantMessage: "Restore endpoint cannot point to link-local addresses",
		},
		{
			name:        "userinfo-link-local",
			endpoint:    "https://storage.example.com@169.254.169.254/latest/meta-data",
			wantMessage: "Restore endpoint cannot point to link-local addresses",
		},
		{
			name:        "ipv4-mapped-ipv6-link-local",
			endpoint:    "HTTPS://[::ffff:169.254.169.254]/latest/meta-data",
			wantMessage: "numeric IP encoding",
		},
		{
			name:        "shorthand-loopback",
			endpoint:    "https://127.1:9000",
			wantMessage: "numeric IP encoding",
		},
		{
			name:        "hex-loopback",
			endpoint:    "https://0x7f000001:9000",
			wantMessage: "numeric IP encoding",
		},
		{
			name:        "plain-http-external",
			endpoint:    "http://example.com",
			wantMessage: "Restore endpoint must use HTTPS or S3 scheme",
		},
		{
			name:        "plain-http-fake-svc-external-domain",
			endpoint:    "http://storage.namespace.svc.evil.example:9000",
			wantMessage: "Restore endpoint must use HTTPS or S3 scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for attempt := 0; attempt < 25; attempt++ {
				restore := &openbaov1alpha1.OpenBaoRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("restore-%s-%d", tt.name, attempt),
						Namespace: namespace,
					},
					Spec: openbaov1alpha1.OpenBaoRestoreSpec{
						Cluster: "does-not-matter-for-admission",
						Source: openbaov1alpha1.RestoreSource{
							Target: openbaov1alpha1.BackupTarget{
								Provider: "s3",
								Endpoint: tt.endpoint,
								Bucket:   testBackupBucket,
								CredentialsSecretRef: &corev1.LocalObjectReference{
									Name: "restore-creds",
								},
							},
							Key: "clusters/prod/snapshot.snap",
						},
						JWTAuthRole: "restore",
						Image:       "ghcr.io/kubebao/openbao-backup:1.0.0",
						Force:       true,
					},
				}

				err := k8sClient.Create(ctx, restore)
				if err == nil {
					_ = k8sClient.Delete(ctx, restore)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				requireAdmissionDenied(t, err)
				if !strings.Contains(err.Error(), tt.wantMessage) {
					t.Fatalf("unexpected error message: %v", err)
				}
				return
			}

			t.Fatalf("expected VAP to deny OpenBaoRestore endpoint %q after retries", tt.endpoint)
		})
	}
}

func TestVAP_OpenBaoRestore_AllowsInClusterHTTPServiceEndpoint(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoRestoreAdmissionPolicies(t, namespace)

	restore := &openbaov1alpha1.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-in-cluster-http-service",
			Namespace: namespace,
		},
		Spec: openbaov1alpha1.OpenBaoRestoreSpec{
			Cluster: "target-cluster",
			Source: openbaov1alpha1.RestoreSource{
				Target: openbaov1alpha1.BackupTarget{
					Provider: "s3",
					Endpoint: "http://rustfs-svc.rustfs.svc.cluster.local:9000",
					Bucket:   testBackupBucket,
				},
				Key: "clusters/prod/snapshot.snap",
			},
			JWTAuthRole: "restore",
			Force:       true,
		},
	}

	if err := k8sClient.Create(ctx, restore); err != nil {
		t.Fatalf("expected in-cluster HTTP restore endpoint to be allowed, got: %v", err)
	}
}

func TestVAP_OpenBaoCluster_AllowsRollingTargetCorrectionBeforeRolloutStarts(t *testing.T) {
	namespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, namespace)

	cluster := newMinimalClusterObj(namespace, "cluster-rolling-target-correction")
	cluster.Spec.Version = "2.6.0"
	cluster.Spec.Image = "openbao/openbao:2.6.0"
	if err := k8sClient.Create(ctx, cluster); err != nil {
		t.Fatalf("create OpenBaoCluster: %v", err)
	}

	updateClusterStatus(t, cluster, func(status *openbaov1alpha1.OpenBaoClusterStatus) {
		status.Initialized = true
		status.CurrentVersion = testOpenBaoVersion244
		status.Upgrade = &openbaov1alpha1.UpgradeProgress{
			FromVersion:      testOpenBaoVersion244,
			TargetVersion:    "2.6.0",
			CurrentPartition: cluster.Spec.Replicas,
		}
	})

	var latest openbaov1alpha1.OpenBaoCluster
	key := types.NamespacedName{Namespace: namespace, Name: cluster.Name}
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatalf("get OpenBaoCluster: %v", err)
	}

	original := latest.DeepCopy()
	latest.Spec.Version = testOpenBaoVersion250
	latest.Spec.Image = testOpenBaoImage250

	if err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original)); err != nil {
		t.Fatalf("expected retarget before rollout progress to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoTenant_RejectsCrossNamespaceSelfService(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)
	targetNamespace := newTestNamespace(t)

	for attempt := 0; attempt < 25; attempt++ {
		tenant := &openbaov1alpha1.OpenBaoTenant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("tenant-self-service-%d", attempt),
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoTenantSpec{
				TargetNamespace: targetNamespace,
			},
		}

		err := k8sClient.Create(ctx, tenant)
		if err == nil {
			_ = k8sClient.Delete(ctx, tenant)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "can only target its own namespace") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny cross-namespace OpenBaoTenant create after retries")
}

func TestVAP_OpenBaoTenant_RejectsSelfServiceQuotaCustomization(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	namespace := newTestNamespace(t)

	for attempt := 0; attempt < 25; attempt++ {
		tenant := &openbaov1alpha1.OpenBaoTenant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("tenant-self-service-quota-%d", attempt),
				Namespace: namespace,
			},
			Spec: openbaov1alpha1.OpenBaoTenantSpec{
				TargetNamespace: namespace,
				Quota: &corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						corev1.ResourcePods: resource.MustParse("5"),
					},
				},
			},
		}

		err := k8sClient.Create(ctx, tenant)
		if err == nil {
			_ = k8sClient.Delete(ctx, tenant)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "may not customize spec.quota or spec.limitRange") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny self-service OpenBaoTenant quota customization after retries")
}

func TestVAP_OpenBaoTenant_AllowsAdminQuotaCustomization(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	operatorNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openbao-operator-system",
		},
	}
	if err := k8sClient.Create(ctx, operatorNamespace); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create operator namespace: %v", err)
	}

	targetNamespace := newTestNamespace(t)
	tenant := &openbaov1alpha1.OpenBaoTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-admin-quota",
			Namespace: operatorNamespace.Name,
		},
		Spec: openbaov1alpha1.OpenBaoTenantSpec{
			TargetNamespace: targetNamespace,
			Quota: &corev1.ResourceQuotaSpec{
				Hard: corev1.ResourceList{
					corev1.ResourcePods: resource.MustParse("5"),
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, tenant); err != nil {
		t.Fatalf("expected operator-namespace OpenBaoTenant with quota override to succeed, got: %v", err)
	}
}

func TestVAP_OpenBaoTenant_RejectsTargetNamespaceMutation(t *testing.T) {
	ensureDefaultAdmissionPoliciesApplied(t)

	operatorNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openbao-operator-system",
		},
	}
	if err := k8sClient.Create(ctx, operatorNamespace); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create operator namespace: %v", err)
	}

	targetNamespace := newTestNamespace(t)
	otherTargetNamespace := newTestNamespace(t)

	for attempt := 0; attempt < 25; attempt++ {
		tenant := &openbaov1alpha1.OpenBaoTenant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("tenant-immutable-%d", attempt),
				Namespace: operatorNamespace.Name,
			},
			Spec: openbaov1alpha1.OpenBaoTenantSpec{
				TargetNamespace: targetNamespace,
			},
		}

		if err := k8sClient.Create(ctx, tenant); err != nil {
			t.Fatalf("create OpenBaoTenant: %v", err)
		}

		var latest openbaov1alpha1.OpenBaoTenant
		tenantKey := types.NamespacedName{Namespace: tenant.Namespace, Name: tenant.Name}
		if err := k8sClient.Get(ctx, tenantKey, &latest); err != nil {
			t.Fatalf("get OpenBaoTenant: %v", err)
		}
		original := latest.DeepCopy()
		latest.Spec.TargetNamespace = otherTargetNamespace

		err := k8sClient.Patch(ctx, &latest, client.MergeFrom(original))
		if err == nil {
			_ = k8sClient.Delete(ctx, &latest)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		requireAdmissionDenied(t, err)
		if !strings.Contains(err.Error(), "spec.targetNamespace is immutable") {
			t.Fatalf("unexpected error message: %v", err)
		}
		return
	}

	t.Fatalf("expected VAP to deny OpenBaoTenant targetNamespace mutation after retries")
}

func TestCRD_OpenBaoCluster_PluginDeclaration(t *testing.T) {
	tests := []struct {
		name   string
		plugin openbaov1alpha1.Plugin
		valid  bool
	}{
		{name: "digest-pinned", plugin: openbaov1alpha1.Plugin{Image: "registry.example.com/plugin:v1.0.0@sha256:" + strings.Repeat("a", 64)}, valid: true},
		{name: "command-kms", plugin: openbaov1alpha1.Plugin{Command: "openbao-plugin-kms-pkcs11"}, valid: true},
		{name: "bad-checksum", plugin: openbaov1alpha1.Plugin{Command: "plugin", SHA256Sum: "bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := newMinimalClusterObj(newTestNamespace(t), "plugin-"+tt.name)
			cluster.Spec.Version = "2.7.0"
			tt.plugin.Type, tt.plugin.Name = "kms", "pkcs11"
			cluster.Spec.Plugins = []openbaov1alpha1.Plugin{tt.plugin}
			err := k8sClient.Create(ctx, cluster)
			if !tt.valid {
				requireInvalidRequest(t, err)
				return
			}
			if err != nil {
				t.Fatalf("create plugin declaration: %v", err)
			}
			var actual openbaov1alpha1.OpenBaoCluster
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &actual); err != nil {
				t.Fatal(err)
			}
			if len(actual.Spec.Plugins) != 1 || actual.Spec.Plugins[0].SHA256Sum != "" || actual.Spec.Plugins[0].BinaryName != "" || actual.Spec.Plugins[0].Version != "" {
				t.Fatalf("omitted plugin fields were not preserved: %#v", actual.Spec.Plugins)
			}
		})
	}
}
