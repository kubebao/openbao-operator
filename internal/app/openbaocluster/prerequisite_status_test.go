package openbaocluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
)

func TestReconcilePrerequisiteConditionsPreservesOrder(t *testing.T) {
	scheme := newPrerequisiteStatusTestScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	applications := newPrerequisiteStatusTestApplications(reader, scheme)
	cluster := newPrerequisiteStatusTestCluster()
	cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
		APIServerCIDR:        "10.43.0.1/32",
		APIServerEndpointIPs: []string{"192.0.2.10"},
	}
	cluster.Spec.Replicas = 2
	cluster.Spec.TLS.Mode = openbaov1alpha1.TLSModeACME
	cluster.Spec.TLS.ACME = &openbaov1alpha1.ACMEConfig{DirectoryURL: "https://acme.example/directory"}
	cluster.Spec.Gateway = &openbaov1alpha1.GatewayConfig{Enabled: true}
	cluster.Spec.Ingress = &openbaov1alpha1.IngressConfig{Enabled: true}
	cluster.Spec.AuditFileStorage = &openbaov1alpha1.AuditFileStorageConfig{
		Mode:              openbaov1alpha1.AuditFileStorageModeExistingPVC,
		ExistingClaimName: " ",
	}
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		Target: openbaov1alpha1.BackupTarget{Provider: "s3", Bucket: "backups"},
	}
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type: "awskms",
		AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
			Region:   "eu-central-1",
			KMSKeyID: "alias/openbao",
		},
	}

	applications.ReconcilePrerequisiteConditions(t.Context(), cluster)

	want := []string{
		string(openbaov1alpha1.ConditionAPIServerNetworkReady),
		string(openbaov1alpha1.ConditionTLSReady),
		string(openbaov1alpha1.ConditionACMEIntegrationReady),
		string(openbaov1alpha1.ConditionACMECacheReady),
		string(openbaov1alpha1.ConditionAuditFileStorageReady),
		string(openbaov1alpha1.ConditionGatewayIntegrationReady),
		string(openbaov1alpha1.ConditionIngressIntegrationReady),
		string(openbaov1alpha1.ConditionBackupConfigurationReady),
		string(openbaov1alpha1.ConditionCloudUnsealIdentityReady),
	}
	if len(cluster.Status.Conditions) != len(want) {
		t.Fatalf("condition count = %d, want %d: %#v", len(cluster.Status.Conditions), len(want), cluster.Status.Conditions)
	}
	for i, conditionType := range want {
		if cluster.Status.Conditions[i].Type != conditionType {
			t.Errorf("condition[%d].Type = %q, want %q", i, cluster.Status.Conditions[i].Type, conditionType)
		}
		if cluster.Status.Conditions[i].ObservedGeneration != cluster.Generation {
			t.Errorf("condition[%d].ObservedGeneration = %d, want %d", i, cluster.Status.Conditions[i].ObservedGeneration, cluster.Generation)
		}
	}
}

func TestReconcilePrerequisiteConditionsRemovesInapplicableConditions(t *testing.T) {
	scheme := newPrerequisiteStatusTestScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	applications := newPrerequisiteStatusTestApplications(reader, scheme)
	cluster := newPrerequisiteStatusTestCluster()
	cluster.Spec.Network = &openbaov1alpha1.NetworkConfig{
		APIServerCIDR:        "10.43.0.1/32",
		APIServerEndpointIPs: []string{"192.0.2.10"},
	}

	optionalConditions := []openbaov1alpha1.ConditionType{
		openbaov1alpha1.ConditionACMEIntegrationReady,
		openbaov1alpha1.ConditionACMECacheReady,
		openbaov1alpha1.ConditionAuditFileStorageReady,
		openbaov1alpha1.ConditionGatewayIntegrationReady,
		openbaov1alpha1.ConditionIngressIntegrationReady,
		openbaov1alpha1.ConditionBackupConfigurationReady,
		openbaov1alpha1.ConditionCloudUnsealIdentityReady,
	}
	for _, conditionType := range optionalConditions {
		cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
			Type:   string(conditionType),
			Status: metav1.ConditionTrue,
			Reason: "Stale",
		})
	}

	applications.ReconcilePrerequisiteConditions(t.Context(), cluster)

	for _, conditionType := range optionalConditions {
		if condition := meta.FindStatusCondition(cluster.Status.Conditions, string(conditionType)); condition != nil {
			t.Errorf("condition %s was not removed: %#v", conditionType, condition)
		}
	}
	assertPrerequisiteCondition(
		t,
		cluster,
		openbaov1alpha1.ConditionAPIServerNetworkReady,
		metav1.ConditionTrue,
		constants.ReasonAPIServerNetworkReady,
		"192.0.2.10",
	)
	assertPrerequisiteCondition(
		t,
		cluster,
		openbaov1alpha1.ConditionTLSReady,
		metav1.ConditionFalse,
		"TLSSecretMissing",
		"CA TLS Secret",
	)
}

func TestReconcilePrerequisiteConditionsTurnsReadErrorsIntoUnknownConditions(t *testing.T) {
	scheme := newPrerequisiteStatusTestScheme(t)
	baseReader := fake.NewClientBuilder().WithScheme(scheme).Build()
	reader := &prerequisiteErrorReader{Reader: baseReader, err: errors.New("reader unavailable")}
	applications := newPrerequisiteStatusTestApplications(reader, scheme)
	cluster := newPrerequisiteStatusTestCluster()
	cluster.Spec.TLS.Enabled = false
	cluster.Spec.Profile = openbaov1alpha1.ProfileDevelopment
	cluster.Spec.Backup = &openbaov1alpha1.BackupSchedule{
		JWTAuthRole: "backup-role",
		Target: openbaov1alpha1.BackupTarget{
			Provider:             "s3",
			Bucket:               "backups",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: "backup-creds"},
		},
	}
	cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
		Type:                 "awskms",
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: "unseal-creds"},
		AWSKMS: &openbaov1alpha1.AWSKMSSealConfig{
			Region:   "eu-central-1",
			KMSKeyID: "alias/openbao",
		},
	}

	applications.ReconcilePrerequisiteConditions(t.Context(), cluster)

	const wantBackupMessage = "Failed to evaluate backup Job prerequisites: failed to read backup storage credentials Secret default/backup-creds: reader unavailable"
	assertPrerequisiteCondition(
		t,
		cluster,
		openbaov1alpha1.ConditionBackupConfigurationReady,
		metav1.ConditionUnknown,
		constants.ReasonUnknown,
		wantBackupMessage,
	)
	const wantCloudMessage = "Failed to evaluate cloud KMS unseal identity prerequisites: failed to read AWS KMS unseal credentials Secret default/unseal-creds: reader unavailable"
	assertPrerequisiteCondition(
		t,
		cluster,
		openbaov1alpha1.ConditionCloudUnsealIdentityReady,
		metav1.ConditionUnknown,
		constants.ReasonUnknown,
		wantCloudMessage,
	)
	if condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionBackupConfigurationReady)); condition.Message != wantBackupMessage {
		t.Errorf("BackupConfigurationReady message = %q, want %q", condition.Message, wantBackupMessage)
	}
	if condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionCloudUnsealIdentityReady)); condition.Message != wantCloudMessage {
		t.Errorf("CloudUnsealIdentityReady message = %q, want %q", condition.Message, wantCloudMessage)
	}
}

func TestReconcilePrerequisiteConditionsReportsAzureWorkloadIdentityRecreate(t *testing.T) {
	newCluster := func() *openbaov1alpha1.OpenBaoCluster {
		cluster := newPrerequisiteStatusTestCluster()
		cluster.Spec.Unseal = &openbaov1alpha1.UnsealConfig{
			Type: "azurekeyvault",
			AzureKeyVault: &openbaov1alpha1.AzureKeyVaultSealConfig{
				VaultName:        "vault",
				KeyName:          "key",
				TenantID:         "tenant-123",
				WorkloadIdentity: &openbaov1alpha1.AzureKeyVaultWorkloadIdentity{ClientID: "client-456"},
			},
		}
		return cluster
	}
	newStatefulSet := func(cluster *openbaov1alpha1.OpenBaoCluster, name string, withToken bool) *appsv1.StatefulSet {
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: cluster.Namespace,
				Labels:    resourceidentity.Labels(cluster),
			},
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: constants.ContainerBao}},
					},
				},
			},
		}
		if withToken {
			sts.Spec.Template.Spec.Volumes = []corev1.Volume{{
				Name: constants.VolumeAzureIdentityToken,
				VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Audience: "api://AzureADTokenExchange",
						Path:     "azure-identity-token",
					}}},
				}},
			}}
			sts.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
				Name:      constants.VolumeAzureIdentityToken,
				MountPath: "/var/run/secrets/azure/tokens",
				ReadOnly:  true,
			}}
		}
		return sts
	}

	t.Run("existing StatefulSet without the token", func(t *testing.T) {
		scheme := newPrerequisiteStatusTestScheme(t)
		cluster := newCluster()
		reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newStatefulSet(cluster, cluster.Name, false)).Build()

		newPrerequisiteStatusTestApplications(reader, scheme).ReconcilePrerequisiteConditions(t.Context(), cluster)

		assertPrerequisiteCondition(
			t,
			cluster,
			openbaov1alpha1.ConditionCloudUnsealIdentityReady,
			metav1.ConditionFalse,
			constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired,
			"token projection: default/example",
		)
	})

	t.Run("voter and read-replica StatefulSets without the token", func(t *testing.T) {
		scheme := newPrerequisiteStatusTestScheme(t)
		cluster := newCluster()
		reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newStatefulSet(cluster, cluster.Name, false),
			newStatefulSet(cluster, resourceidentity.ReadReplicaStatefulSetName(cluster), false),
		).Build()

		newPrerequisiteStatusTestApplications(reader, scheme).ReconcilePrerequisiteConditions(t.Context(), cluster)

		assertPrerequisiteCondition(
			t,
			cluster,
			openbaov1alpha1.ConditionCloudUnsealIdentityReady,
			metav1.ConditionFalse,
			constants.ReasonAzureWorkloadIdentityStatefulSetRecreateRequired,
			"token projection: default/example, default/"+resourceidentity.ReadReplicaStatefulSetName(cluster),
		)
	})

	t.Run("existing StatefulSet with the token", func(t *testing.T) {
		scheme := newPrerequisiteStatusTestScheme(t)
		cluster := newCluster()
		reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newStatefulSet(cluster, cluster.Name, true)).Build()

		newPrerequisiteStatusTestApplications(reader, scheme).ReconcilePrerequisiteConditions(t.Context(), cluster)

		assertPrerequisiteCondition(
			t,
			cluster,
			openbaov1alpha1.ConditionCloudUnsealIdentityReady,
			metav1.ConditionTrue,
			"Ready",
			"client ID \"client-456\"",
		)
	})
}

type prerequisiteErrorReader struct {
	client.Reader
	err error
}

func (r *prerequisiteErrorReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func newPrerequisiteStatusTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for name, addToScheme := range map[string]func(*runtime.Scheme) error{
		"client-go":        clientgoscheme.AddToScheme,
		"apps/v1":          appsv1.AddToScheme,
		"gateway-api/v1":   gatewayv1.Install,
		"openbao/v1alpha1": openbaov1alpha1.AddToScheme,
	} {
		if err := addToScheme(scheme); err != nil {
			t.Fatalf("add %s to scheme: %v", name, err)
		}
	}
	return scheme
}

func newPrerequisiteStatusTestApplications(reader client.Reader, scheme *runtime.Scheme) *Applications {
	var c client.Client
	if typed, ok := reader.(client.Client); ok {
		c = typed
	} else {
		c = fake.NewClientBuilder().WithScheme(scheme).Build()
	}
	return NewApplications(ApplicationsConfig{
		StatusDependencies: StatusDependencies{Reader: reader},
		StatusIntegration: StatusIntegrationDependencies{
			Client:    c,
			APIReader: c,
			Scheme:    scheme,
		},
	})
}

func newPrerequisiteStatusTestCluster() *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "example",
			Namespace:  "default",
			Generation: 2,
		},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Replicas: 1,
			Profile:  openbaov1alpha1.ProfileHardened,
			TLS:      openbaov1alpha1.TLSConfig{Enabled: true},
			SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true},
		},
	}
}

func assertPrerequisiteCondition(
	t *testing.T,
	cluster *openbaov1alpha1.OpenBaoCluster,
	conditionType openbaov1alpha1.ConditionType,
	wantStatus metav1.ConditionStatus,
	wantReason string,
	wantMessage string,
) {
	t.Helper()
	condition := meta.FindStatusCondition(cluster.Status.Conditions, string(conditionType))
	if condition == nil {
		t.Fatalf("condition %s is absent", conditionType)
	}
	if condition.Status != wantStatus || condition.Reason != wantReason {
		t.Errorf("condition %s = status %s, reason %q; want status %s, reason %q", conditionType, condition.Status, condition.Reason, wantStatus, wantReason)
	}
	if !strings.Contains(condition.Message, wantMessage) {
		t.Errorf("condition %s message = %q, want substring %q", conditionType, condition.Message, wantMessage)
	}
	if condition.ObservedGeneration != cluster.Generation {
		t.Errorf("condition %s ObservedGeneration = %d, want %d", conditionType, condition.ObservedGeneration, cluster.Generation)
	}
	if condition.LastTransitionTime.IsZero() {
		t.Errorf("condition %s LastTransitionTime is zero", conditionType)
	}
}
