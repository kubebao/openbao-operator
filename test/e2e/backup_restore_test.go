//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	nativev1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	restoresvc "github.com/kubebao/openbao-operator/internal/service/restore"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const (
	// RustFS (S3) constants
	rustfsName      = "rustfs"
	rustfsEndpoint  = "http://rustfs-svc.rustfs.svc.cluster.local:9000"
	rustfsBucket    = "openbao-backups"
	rustfsAccessKey = "rustfsadmin"
	rustfsSecretKey = "rustfsadmin"

	// fake-gcs-server constants
	fakeGCSName      = "fake-gcs-server"
	fakeGCSNamespace = "gcs"
	fakeGCSEndpoint  = "http://fake-gcs-server.gcs.svc.cluster.local:4443"
	fakeGCSBucket    = "openbao-backups"
	fakeGCSProject   = "test-project"

	// Azurite constants
	azuriteName      = "azurite"
	azuriteNamespace = "azure"
	azuriteEndpoint  = "http://azurite.azure.svc.cluster.local:10000/devstoreaccount1"
	azuriteAccount   = "devstoreaccount1"
	azuriteKey       = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	azuriteContainer = "openbao-backups"
)

// ensureFakeGCS deploys fake-gcs-server in the cluster for GCS emulator testing.
func ensureFakeGCS(ctx context.Context, c client.Client, namespace string) error {
	cfg := e2ehelpers.DefaultGCSConfig()
	cfg.Namespace = namespace
	cfg.Name = fakeGCSName
	cfg.Project = fakeGCSProject

	_, _ = fmt.Fprintf(GinkgoWriter, "Deploying fake-gcs-server in namespace %q...\n", namespace)
	if err := e2ehelpers.EnsureFakeGCS(ctx, c, cfg); err != nil {
		return fmt.Errorf("failed to deploy fake-gcs-server: %w", err)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "fake-gcs-server deployed successfully\n")
	return nil
}

// ensureAzurite deploys Azurite in the cluster for Azure emulator testing.
func ensureAzurite(ctx context.Context, c client.Client, namespace string) error {
	cfg := e2ehelpers.DefaultAzuriteConfig()
	cfg.Namespace = namespace
	cfg.Name = azuriteName

	_, _ = fmt.Fprintf(GinkgoWriter, "Deploying Azurite in namespace %q...\n", namespace)
	if err := e2ehelpers.EnsureAzurite(ctx, c, cfg); err != nil {
		return fmt.Errorf("failed to deploy Azurite: %w", err)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "Azurite deployed successfully\n")
	return nil
}

// createAzureCredentialsSecret creates an Azure credentials Secret with account key.
func createAzureCredentialsSecret(ctx context.Context, c client.Client, namespace, name string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"accountKey": []byte(azuriteKey),
		},
	}

	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Azure credentials secret: %w", err)
	}
	return nil
}

// ensureRustFS ensures RustFS is deployed in the cluster.
// It creates the namespace if needed and deploys RustFS using the helper.
func ensureRustFS(ctx context.Context, c client.Client, restCfg *rest.Config) error {
	// Ensure namespace exists
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: rustfsName,
		},
	}
	if err := c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create RustFS namespace: %w", err)
	}

	// Deploy RustFS using the helper
	cfg := e2ehelpers.DefaultRustFSConfig()
	cfg.Namespace = rustfsName
	cfg.Name = rustfsName
	cfg.AccessKey = rustfsAccessKey
	cfg.SecretKey = rustfsSecretKey
	cfg.Buckets = []string{rustfsBucket} // Create the backup bucket

	_, _ = fmt.Fprintf(GinkgoWriter, "Deploying RustFS in namespace %q...\n", rustfsName)
	if err := e2ehelpers.EnsureRustFS(ctx, c, restCfg, cfg); err != nil {
		return fmt.Errorf("failed to deploy RustFS: %w", err)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "RustFS deployed successfully\n")
	return nil
}

func triggerManualBackup(ctx context.Context, c client.Client, namespace, clusterName string) error {
	cluster := &openbaov1alpha1.OpenBaoCluster{}
	if err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster); err != nil {
		return fmt.Errorf("get cluster for manual backup trigger: %w", err)
	}

	original := cluster.DeepCopy()
	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Annotations[constants.AnnotationTriggerBackup] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := c.Patch(ctx, cluster, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch manual backup trigger annotation: %w", err)
	}

	return nil
}

func waitForBackupJobCreated(ctx context.Context, c client.Client, namespace, clusterName string) {
	Eventually(func(g Gomega) {
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster)).To(Succeed())

		var jobs batchv1.JobList
		err := c.List(ctx, &jobs, client.InNamespace(namespace), client.MatchingLabels{
			constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
			constants.LabelOpenBaoComponent:  "backup",
			constants.LabelOpenBaoBackupType: constants.BackupTypeScheduled,
			constants.LabelOpenBaoCluster:    clusterName,
		})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(jobs.Items).ToNot(BeEmpty())
		for i := range jobs.Items {
			g.Expect(validateManagedLifecycleJobOwnerProof(&jobs.Items[i], cluster)).To(Succeed())
		}
	}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
}

func validateManagedLifecycleJobOwnerProof(
	job *batchv1.Job,
	owner *openbaov1alpha1.OpenBaoCluster,
) error {
	if job == nil || owner == nil || owner.UID == "" {
		return fmt.Errorf("job and owner with UID are required")
	}
	if got := job.Annotations[constants.AnnotationOpenBaoOwnerUID]; got != string(owner.UID) {
		return fmt.Errorf("job %s/%s owner UID annotation = %q, want %q", job.Namespace, job.Name, got, owner.UID)
	}
	controllerRef := metav1.GetControllerOfNoCopy(job)
	if controllerRef == nil {
		return fmt.Errorf("job %s/%s has no controller owner reference", job.Namespace, job.Name)
	}
	if controllerRef.APIVersion != openbaov1alpha1.GroupVersion.String() ||
		controllerRef.Kind != "OpenBaoCluster" ||
		controllerRef.Name != owner.Name ||
		controllerRef.UID != owner.UID {
		return fmt.Errorf(
			"job %s/%s controller owner = %s %s/%s with UID %q, want OpenBaoCluster %s/%s with UID %q",
			job.Namespace,
			job.Name,
			controllerRef.Kind,
			owner.Namespace,
			controllerRef.Name,
			controllerRef.UID,
			owner.Namespace,
			owner.Name,
			owner.UID,
		)
	}
	return nil
}

func waitForSuccessfulBackupJob(ctx context.Context, c client.Client, namespace, clusterName string) {
	Eventually(func(g Gomega) {
		var jobs batchv1.JobList
		err := c.List(ctx, &jobs, client.InNamespace(namespace), client.MatchingLabels{
			constants.LabelAppManagedBy:      constants.LabelValueAppManagedByOpenBaoOperator,
			constants.LabelOpenBaoComponent:  "backup",
			constants.LabelOpenBaoBackupType: constants.BackupTypeScheduled,
			constants.LabelOpenBaoCluster:    clusterName,
		})
		g.Expect(err).NotTo(HaveOccurred())

		foundSuccess := false
		for i := range jobs.Items {
			if jobs.Items[i].Status.Succeeded > 0 {
				foundSuccess = true
				break
			}
		}
		g.Expect(foundSuccess).To(BeTrue())
	}, 15*time.Minute, 30*time.Second).Should(Succeed())
}

func recordLatestBackupKey(
	ctx context.Context,
	f *framework.Framework,
	c client.Client,
	namespace string,
	clusterName string,
	backupKey *string,
) {
	Eventually(func(g Gomega) {
		_ = f.TriggerReconcile(ctx, clusterName)
		updated := &openbaov1alpha1.OpenBaoCluster{}
		err := c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, updated)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(updated.Status.Backup).NotTo(BeNil())
		g.Expect(updated.Status.Backup.LastBackupName).NotTo(BeEmpty())
		*backupKey = updated.Status.Backup.LastBackupName
	}, framework.DefaultLongWaitTimeout, 5*time.Second).Should(Succeed())
}

func newBackupNetworkPolicy(
	namespace string,
	clusterName string,
	storageNamespace string,
	storagePort int,
	components ...string,
) *nativev1.NetworkPolicy {
	return &nativev1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-dr-network-policy", clusterName),
			Namespace: namespace,
		},
		Spec: nativev1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.LabelOpenBaoCluster: clusterName,
				},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      constants.LabelOpenBaoComponent,
						Operator: metav1.LabelSelectorOpIn,
						Values:   components,
					},
				},
			},
			PolicyTypes: []nativev1.PolicyType{
				nativev1.PolicyTypeEgress,
			},
			Egress: []nativev1.NetworkPolicyEgressRule{
				namespaceEgressRule("kube-system", corev1.ProtocolUDP, 53),
				namespaceEgressRule(storageNamespace, corev1.ProtocolTCP, storagePort),
				clusterEgressRule(clusterName, corev1.ProtocolTCP, 8200),
			},
		},
	}
}

func namespaceEgressRule(namespace string, protocol corev1.Protocol, port int) nativev1.NetworkPolicyEgressRule {
	return nativev1.NetworkPolicyEgressRule{
		To: []nativev1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": namespace,
					},
				},
			},
		},
		Ports: []nativev1.NetworkPolicyPort{networkPolicyPort(protocol, port)},
	}
}

func clusterEgressRule(clusterName string, protocol corev1.Protocol, port int) nativev1.NetworkPolicyEgressRule {
	return nativev1.NetworkPolicyEgressRule{
		To: []nativev1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						constants.LabelOpenBaoCluster: clusterName,
					},
				},
			},
		},
		Ports: []nativev1.NetworkPolicyPort{networkPolicyPort(protocol, port)},
	}
}

func networkPolicyPort(protocol corev1.Protocol, port int) nativev1.NetworkPolicyPort {
	return nativev1.NetworkPolicyPort{
		Protocol: ptr.To(protocol),
		Port:     ptr.To(intstr.FromInt(port)),
	}
}

func restartControllerDeployment(ctx context.Context, c client.Client, namespace string) error {
	deploy := &appsv1.Deployment{}
	key := types.NamespacedName{Name: "openbao-operator-controller", Namespace: namespace}
	if err := c.Get(ctx, key, deploy); err != nil {
		return fmt.Errorf("get controller deployment for restart: %w", err)
	}

	original := deploy.DeepCopy()
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations["e2e.openbao.org/restarted-at"] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := c.Patch(ctx, deploy, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch controller deployment restart annotation: %w", err)
	}

	if err := waitForDeploymentsAvailable(namespace, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for operator deployments after controller restart: %w", err)
	}

	return nil
}

var _ = Describe("DR: Storage Providers Backup & Restore", Label("dr", "backup", "restore", "storage-providers", "nightly", "slow"), Ordered, func() {
	ctx := context.Background()

	var (
		cfg    *rest.Config
		scheme *runtime.Scheme
		admin  client.Client
	)

	BeforeAll(func() {
		var err error

		cfg, err = ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		scheme = runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(openbaov1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(appsv1.AddToScheme(scheme)).To(Succeed())

		admin, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

	})

	Context("S3 Backup & Restore with RustFS", func() {
		var (
			tenantNamespace   string
			tenantFW          *framework.Framework
			drCluster         *openbaov1alpha1.OpenBaoCluster
			credentialsSecret *corev1.Secret
			backupKey         string
		)

		BeforeAll(func() {
			var err error

			err = ensureRustFS(ctx, admin, cfg)
			Expect(err).NotTo(HaveOccurred(), "RustFS deployment failed")

			tenantFW, err = framework.New(ctx, admin, "tenant-s3-dr", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace

			// Create S3 credentials Secret for RustFS
			credentialsSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rustfs-secret",
					Namespace: tenantNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"accessKeyId":     []byte(rustfsAccessKey),
					"secretAccessKey": []byte(rustfsSecretKey),
				},
			}
			Expect(admin.Create(ctx, credentialsSecret)).To(Succeed())

			// Create cluster with S3 backup/restore configuration
			// Using BootstrapJWTAuth to auto-create backup and restore roles
			drCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-dr-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  openBaoVersion,
					Image:    openBaoImage,
					Replicas: 3,
					ReadReplicas: &openbaov1alpha1.ReadReplicaConfig{
						Replicas: 1,
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled:  true,
						OIDC:     &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}, // Operator will auto-create backup and restore roles
						Requests: e2ehelpers.CreateE2ERequests(tenantNamespace),
					},
					TLS: openbaov1alpha1.TLSConfig{
						Enabled:        true,
						Mode:           openbaov1alpha1.TLSModeOperatorManaged,
						RotationPeriod: "720h",
					},
					Storage: openbaov1alpha1.StorageConfig{
						Size: "1Gi",
					},
					Network: &openbaov1alpha1.NetworkConfig{
						APIServerCIDR:        apiServerCIDR,
						APIServerEndpointIPs: apiServerEndpointIPs,
					},
					Backup: &openbaov1alpha1.BackupSchedule{
						Schedule: "*/5 * * * *",
						Image:    backupExecutorImage,
						// JWTAuthRole not set - operator will auto-create backup role when OIDC is enabled
						Target: openbaov1alpha1.BackupTarget{
							Provider:   constants.StorageProviderS3,
							Endpoint:   rustfsEndpoint,
							Bucket:     rustfsBucket,
							PathPrefix: "clusters",
							S3: &openbaov1alpha1.S3TargetConfig{
								Region: ptr.To("us-east-1"), UsePathStyle: ptr.To(true),
							},
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
						Retention: &openbaov1alpha1.BackupRetention{
							MaxCount: 7,
							MaxAge:   "168h",
						},
					},
					Restore: &openbaov1alpha1.RestoreConfig{
						JWTAuthRole: "restore", // Triggers auto-creation of restore policy/role via bootstrap
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, drCluster)).To(Succeed())

			backupNetworkPolicy := newBackupNetworkPolicy(
				tenantNamespace,
				drCluster.Name,
				rustfsName,
				9000,
				"backup",
				"restore",
			)
			Expect(admin.Create(ctx, backupNetworkPolicy)).To(Succeed())

			// Wait for cluster to be ready
			Eventually(func(g Gomega) {
				_ = tenantFW.TriggerReconcile(ctx, drCluster.Name)

				updated := &openbaov1alpha1.OpenBaoCluster{}
				err := admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.SelfInitialized).To(BeTrue())

				available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.ReadReplicas).NotTo(BeNil())
				g.Expect(updated.Status.ReadReplicas.DesiredReplicas).To(Equal(int32(1)))
				g.Expect(updated.Status.ReadReplicas.ReadyReplicas).To(Equal(int32(1)))
				g.Expect(updated.Status.ReadReplicas.RegisteredReplicas).To(Equal(int32(1)))

				for _, condType := range []openbaov1alpha1.ConditionType{
					openbaov1alpha1.ConditionReadReplicasReady,
					openbaov1alpha1.ConditionReadServingAvailable,
					openbaov1alpha1.ConditionRaftMembershipReady,
					openbaov1alpha1.ConditionReadReplicaStorageConfigured,
				} {
					cond := meta.FindStatusCondition(updated.Status.Conditions, string(condType))
					g.Expect(cond).NotTo(BeNil(), "expected read-replica condition %s", condType)
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue), "expected read-replica condition %s to be true", condType)
				}

				readSts := &appsv1.StatefulSet{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      resourceidentity.ReadReplicaStatefulSetName(drCluster),
					Namespace: tenantNamespace,
				}, readSts)).To(Succeed())
				g.Expect(readSts.Status.ReadyReplicas).To(Equal(int32(1)))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())
			tenantFW.WaitForConditionReason(drCluster.Name, openbaov1alpha1.ConditionBackupConfigurationReady, metav1.ConditionTrue, "Ready")
		})

		AfterAll(func() {
			if tenantFW == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if err := tenantFW.Cleanup(cleanupCtx); err != nil {
				// Keep the operator available to inspect and recover an unresolved hold.
				skipCleanup = true
				Expect(err).NotTo(HaveOccurred(), "retaining the operator and failed restore fixture")
			}
		})

		It("starts the first scheduled S3 backup", Label("e2e-anchor", "case:dr-s3-initial-schedule"), func() {
			By("creating a cluster without backup status or a manual trigger")
			scheduled := drCluster.DeepCopy()
			scheduled.ObjectMeta = metav1.ObjectMeta{Name: "s3-scheduled-cluster", Namespace: tenantNamespace}
			scheduled.Status = openbaov1alpha1.OpenBaoClusterStatus{}
			scheduled.Spec.Replicas = 1
			scheduled.Spec.ReadReplicas = nil
			// Leave time to observe the persisted deadline before the first attempt.
			firstRun := time.Now().UTC().Add(3 * time.Minute).Truncate(time.Minute)
			scheduled.Spec.Backup.Schedule = fmt.Sprintf("%d %d * * *", firstRun.Minute(), firstRun.Hour())
			Expect(admin.Create(ctx, scheduled)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(admin.Delete(ctx, scheduled))).To(Succeed())
			})
			policy := newBackupNetworkPolicy(tenantNamespace, scheduled.Name, rustfsName, 9000, "backup", "restore")
			Expect(admin.Create(ctx, policy)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(admin.Delete(ctx, policy))).To(Succeed())
			})

			By("checking that the first deadline is persisted before any backup attempt")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(scheduled), updated)).To(Succeed())
				g.Expect(updated.Status.Backup).NotTo(BeNil())
				g.Expect(updated.Status.Backup.NextScheduledBackup).NotTo(BeNil())
				g.Expect(updated.Status.Backup.LastAttemptTime).To(BeNil())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("waiting for a scheduled snapshot without sending a manual trigger")
			waitForBackupJobCreated(ctx, admin, tenantNamespace, scheduled.Name)
			waitForSuccessfulBackupJob(ctx, admin, tenantNamespace, scheduled.Name)
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(scheduled), updated)).To(Succeed())
				g.Expect(updated.Status.Backup.LastBackupName).NotTo(BeEmpty())
				g.Expect(updated.Status.Backup.LastHandledManualTrigger).To(BeEmpty())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		It("creates a restorable S3 backup", Label(
			"e2e-anchor",
			"case:dr-s3-restorable-backup",
			"covers:lifecycle-job-owner-proof",
			"read-replicas",
			"read-replicas-restore",
		), func() {
			By("Writing a secret before backup")
			secretPath := "secret/backup-test"
			secretData := map[string]string{"foo": "bar", "version": "v1"}
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   drCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}

			// Enable KV engine (idempotent)
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, drCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, secretData)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed(), "Failed to write pre-backup secret")

			By("waiting for any earlier scheduled backup to release its operation lock")
			var previousBackupKey string
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(drCluster), updated)).To(Succeed())
				g.Expect(updated.Status.OperationLock).To(BeNil())
				if updated.Status.Backup != nil {
					previousBackupKey = updated.Status.Backup.LastBackupName
				}
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			Eventually(func() error {
				return triggerManualBackup(ctx, admin, tenantNamespace, drCluster.Name)
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())
			waitForBackupJobCreated(ctx, admin, tenantNamespace, drCluster.Name)
			By("waiting for the S3 backup job to complete successfully")
			waitForSuccessfulBackupJob(ctx, admin, tenantNamespace, drCluster.Name)

			By("waiting for a new snapshot that includes the pre-backup secret")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(drCluster), updated)).To(Succeed())
				g.Expect(updated.Status.Backup).NotTo(BeNil())
				g.Expect(updated.Status.Backup.LastBackupName).NotTo(BeEmpty())
				g.Expect(updated.Status.Backup.LastBackupName).NotTo(Equal(previousBackupKey))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("recording the latest S3 backup key from cluster status")
			recordLatestBackupKey(ctx, tenantFW, admin, tenantNamespace, drCluster.Name, &backupKey)
		})

		It("restores from S3 backup using OpenBaoRestore CR", Label(
			"e2e-anchor",
			"case:dr-s3-restore-cr",
			"read-replicas",
			"read-replicas-restore",
		), func() {
			Expect(backupKey).NotTo(BeEmpty(), "backup key should have been set by previous test")

			voterPodUIDsBeforeRestore := map[string]types.UID{}
			By("recording voter Pod identities before restore")
			Eventually(func(g Gomega) {
				pods := &corev1.PodList{}
				err := admin.List(ctx, pods,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster:      drCluster.Name,
						constants.LabelOpenBaoWorkloadPool: constants.LabelValueOpenBaoWorkloadPoolVoter,
					},
				)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(int(drCluster.Spec.Replicas)))
				voterPodUIDsBeforeRestore = map[string]types.UID{}
				for i := range pods.Items {
					voterPodUIDsBeforeRestore[pods.Items[i].Name] = pods.Items[i].UID
				}
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-restore",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					Cluster: drCluster.Name,
					Source: openbaov1alpha1.RestoreSource{
						Target: openbaov1alpha1.BackupTarget{
							Provider: constants.StorageProviderS3,
							Endpoint: rustfsEndpoint,
							Bucket:   rustfsBucket,
							S3: &openbaov1alpha1.S3TargetConfig{
								Region: ptr.To("us-east-1"), UsePathStyle: ptr.To(true),
							},
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
						Key: backupKey,
					},
					JWTAuthRole: "restore",
					Image:       backupExecutorImage,
				},
			}

			_, _ = fmt.Fprintf(GinkgoWriter, "Creating OpenBaoRestore CR: %s\n", restore.Name)
			Expect(admin.Create(ctx, restore)).To(Succeed())
			Expect(restore.UID).NotTo(BeEmpty())

			By("verifying restore configuration is accepted before execution")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoRestore{}
				err := admin.Get(ctx, types.NamespacedName{Name: restore.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())

				configuration := meta.FindStatusCondition(updated.Status.Conditions, restoresvc.RestoreConfigurationConditionType)
				g.Expect(configuration).NotTo(BeNil())
				g.Expect(configuration.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(configuration.Reason).To(Equal("Ready"))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			waitForHeldRestore(ctx, admin, restore)

			By("verifying the operator did not restart voters on HTTP acceptance")
			for name, uid := range voterPodUIDsBeforeRestore {
				pod := &corev1.Pod{}
				Expect(admin.Get(ctx, client.ObjectKey{Namespace: tenantNamespace, Name: name}, pod)).To(Succeed())
				Expect(pod.UID).To(Equal(uid))
			}

			By("acknowledging recovery and waiting for managed voter and read-replica restarts")
			acknowledgeHealthyRestore(ctx, admin, restore)
			Expect(restore.Status.Restart.Pods).To(HaveLen(4))
			Expect(restore.Status.Restart.CompletedAt).NotTo(BeNil())

			By("Verifying secret persists after restore")
			secretPath := "secret/backup-test"
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   drCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}
			// Note: Restore is destructive, it replaces the data.
			// So after restore, the secret we wrote before backup should be there.
			// Reuse the same JWT logic as before (roles are auto-created by bootstrap)
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, drCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				val, err := e2ehelpers.ReadSecretViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, "foo")
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read post-restore secret")
				g.Expect(val).To(Equal("bar"))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

			By("Verifying restore metrics are emitted")
			metricsOutput, metricErr := framework.WaitForControllerMetricSubstrings(
				operatorNamespace,
				2*time.Minute,
				"openbao_restore_total{",
				fmt.Sprintf(`namespace="%s"`, tenantNamespace),
				fmt.Sprintf(`name="%s"`, drCluster.Name),
			)
			Expect(metricErr).NotTo(HaveOccurred(), "Last metrics output:\n%s", metricsOutput)
		})

		It("handles transient S3 auth failure with backup retry after controller restart", Label(
			"case:backup-restore-handles-transient-s3-auth-failure-with-10300347",
			"failure-injection",
		), func() {
			Expect(backupKey).NotTo(BeEmpty(), "backup key should be available before failure injection")

			By("injecting invalid backup credentials")
			secret := &corev1.Secret{}
			Expect(admin.Get(ctx, types.NamespacedName{Name: credentialsSecret.Name, Namespace: tenantNamespace}, secret)).To(Succeed())
			originalSecretData := secret.DeepCopy().Data
			Expect(originalSecretData).To(HaveKey("secretAccessKey"))

			secretOriginal := secret.DeepCopy()
			secret.Data["secretAccessKey"] = []byte("invalid-rustfs-key")
			Expect(admin.Patch(ctx, secret, client.MergeFrom(secretOriginal))).To(Succeed())
			DeferCleanup(func() {
				current := &corev1.Secret{}
				Expect(admin.Get(ctx, types.NamespacedName{Name: credentialsSecret.Name, Namespace: tenantNamespace}, current)).To(Succeed())
				restoreOriginal := current.DeepCopy()
				current.Data = originalSecretData
				Expect(admin.Patch(ctx, current, client.MergeFrom(restoreOriginal))).To(Succeed())
			})

			By("triggering a manual backup with invalid credentials")
			preTriggerJobUIDs := map[types.UID]struct{}{}
			{
				var jobs batchv1.JobList
				Expect(admin.List(ctx, &jobs, client.InNamespace(tenantNamespace), client.MatchingLabels{
					"app.kubernetes.io/managed-by": "openbao-operator",
					"openbao.org/component":        "backup",
					constants.LabelOpenBaoCluster:  drCluster.Name,
				})).To(Succeed())
				for i := range jobs.Items {
					preTriggerJobUIDs[jobs.Items[i].UID] = struct{}{}
				}
			}
			Expect(triggerManualBackup(ctx, admin, tenantNamespace, drCluster.Name)).To(Succeed())
			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())

			By("waiting for backup activity")
			Eventually(func(g Gomega) {
				var jobs batchv1.JobList
				err := admin.List(ctx, &jobs, client.InNamespace(tenantNamespace), client.MatchingLabels{
					"app.kubernetes.io/managed-by": "openbao-operator",
					"openbao.org/component":        "backup",
					constants.LabelOpenBaoCluster:  drCluster.Name,
				})
				g.Expect(err).NotTo(HaveOccurred())

				hasNewJob := false
				for i := range jobs.Items {
					job := jobs.Items[i]
					if _, found := preTriggerJobUIDs[job.UID]; !found {
						hasNewJob = true
						break
					}
				}
				g.Expect(hasNewJob).To(BeTrue(), "expected backup job after outage trigger")
			}, 5*time.Minute, 5*time.Second).Should(Succeed())

			By("restarting the controller while failed backup status is being reconciled")
			Expect(restartControllerDeployment(ctx, admin, operatorNamespace)).To(Succeed())

			By("observing failure status with invalid credentials")
			Eventually(func(g Gomega) {
				_ = tenantFW.TriggerReconcile(ctx, drCluster.Name)

				updated := &openbaov1alpha1.OpenBaoCluster{}
				err := admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updated.Status.Backup).NotTo(BeNil())
				g.Expect(updated.Status.Backup.ConsecutiveFailures).To(BeNumerically(">=", 1))
				g.Expect(updated.Status.Backup.LastFailureReason).NotTo(BeEmpty())
				g.Expect(updated.Status.Backup.LastFailureMessage).NotTo(BeEmpty())
			}, 10*time.Minute, 10*time.Second).Should(Succeed())

			By("restoring valid credentials and retriggering backup")
			current := &corev1.Secret{}
			Expect(admin.Get(ctx, types.NamespacedName{Name: credentialsSecret.Name, Namespace: tenantNamespace}, current)).To(Succeed())
			restoreOriginal := current.DeepCopy()
			current.Data = originalSecretData
			Expect(admin.Patch(ctx, current, client.MergeFrom(restoreOriginal))).To(Succeed())

			time.Sleep(1100 * time.Millisecond)
			recoveryTriggerTime := time.Now().UTC()
			Expect(triggerManualBackup(ctx, admin, tenantNamespace, drCluster.Name)).To(Succeed())
			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())

			By("verifying backup recovery clears stale failure fields")
			Eventually(func(g Gomega) {
				_ = tenantFW.TriggerReconcile(ctx, drCluster.Name)

				updated := &openbaov1alpha1.OpenBaoCluster{}
				err := admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updated.Status.Backup).NotTo(BeNil())
				g.Expect(updated.Status.Backup.LastBackupName).NotTo(BeEmpty())
				g.Expect(updated.Status.Backup.ConsecutiveFailures).To(Equal(int32(0)))
				g.Expect(updated.Status.Backup.LastFailureReason).To(BeEmpty())
				g.Expect(updated.Status.Backup.LastFailureMessage).To(BeEmpty())
				g.Expect(updated.Status.Backup.LastBackupTime).NotTo(BeNil())
				g.Expect(updated.Status.Backup.LastBackupTime.Time.After(recoveryTriggerTime.Add(-2 * time.Minute))).To(BeTrue())
				backupKey = updated.Status.Backup.LastBackupName
			}, 15*time.Minute, 15*time.Second).Should(Succeed())

			By("ensuring backup operation lock is released after recovery")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				err := admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updated.Status.OperationLock).To(BeNil())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		It("retains the restore hold after controller restart while running", Label(
			"e2e-anchor",
			"case:dr-s3-restore-controller-restart",
			"failure-injection",
		), func() {
			Expect(backupKey).NotTo(BeEmpty(), "backup key should be available before restore restart test")

			By("switching the idle cluster to BlueGreen so voters use OnDelete")
			// Cluster status can still report the preceding restore's hold until
			// normal reconciliation observes the resumed workload.
			Eventually(func(g Gomega) {
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(drCluster), drCluster)).To(Succeed())
				beforeStrategy := drCluster.DeepCopy()
				drCluster.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{
					Strategy: openbaov1alpha1.UpdateStrategyBlueGreen, Image: upgradeExecutorImage,
				}
				g.Expect(admin.Patch(ctx, drCluster, client.MergeFrom(beforeStrategy))).To(Succeed())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(drCluster), drCluster)).To(Succeed())
				g.Expect(drCluster.Status.AcceptedUpgradeStrategy).To(Equal(openbaov1alpha1.UpdateStrategyBlueGreen))
				sts := &appsv1.StatefulSet{}
				g.Expect(admin.Get(ctx, client.ObjectKey{Namespace: tenantNamespace, Name: drCluster.Name}, sts)).To(Succeed())
				g.Expect(sts.Spec.UpdateStrategy.Type).To(Equal(appsv1.OnDeleteStatefulSetStrategyType))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			restoreName := "s3-restore-restart"
			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      restoreName,
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					Cluster: drCluster.Name,
					Source: openbaov1alpha1.RestoreSource{
						Target: openbaov1alpha1.BackupTarget{
							Provider:     constants.StorageProviderS3,
							Endpoint:     rustfsEndpoint,
							Bucket:       rustfsBucket,
							UsePathStyle: true,
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
						Key: backupKey,
					},
					JWTAuthRole: "restore",
					Image:       backupExecutorImage,
					Force:       true,
				},
			}
			Expect(admin.Create(ctx, restore)).To(Succeed())

			By("waiting for restore to enter Running phase")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoRestore{}
				err := admin.Get(ctx, types.NamespacedName{Name: restoreName, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updated.Status.Phase).NotTo(Equal(openbaov1alpha1.RestorePhaseFailed))
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.RestorePhaseRunning))
			}, 5*time.Minute, 5*time.Second).Should(Succeed())

			By("restarting controller deployment during restore execution")
			Expect(restartControllerDeployment(ctx, admin, operatorNamespace)).To(Succeed())

			waitForHeldRestore(ctx, admin, restore)
			claim := restore.Status.SubmissionClaim.DeepCopy()
			jobUID := restore.Status.Execution.JobUID
			Consistently(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoRestore{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(restore), updated)).To(Succeed())
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.RestorePhaseUnknown))
				g.Expect(updated.Status.SubmissionClaim).To(Equal(claim))
				g.Expect(updated.Status.Execution.JobUID).To(Equal(jobUID))
			}, time.Minute, 10*time.Second).Should(Succeed())
			By("authorizing managed voter and read-replica restarts")
			acknowledgeHealthyRestore(ctx, admin, restore)
		})
	})

	Context("GCS Backup with fake-gcs-server", func() {
		var (
			tenantNamespace   string
			tenantFW          *framework.Framework
			drCluster         *openbaov1alpha1.OpenBaoCluster
			credentialsSecret *corev1.Secret
		)

		BeforeAll(func() {
			var err error

			err = ensureFakeGCS(ctx, admin, fakeGCSNamespace)
			Expect(err).NotTo(HaveOccurred(), "fake-gcs-server deployment failed")

			tenantFW, err = framework.New(ctx, admin, "tenant-gcs-dr", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace

			// Create GCS credentials Secret
			err = e2ehelpers.CreateGCSCredentialsSecret(ctx, admin, tenantNamespace, "gcs-credentials", fakeGCSProject)
			Expect(err).NotTo(HaveOccurred())
			credentialsSecret = &corev1.Secret{}
			Expect(admin.Get(ctx, types.NamespacedName{Name: "gcs-credentials", Namespace: tenantNamespace}, credentialsSecret)).To(Succeed())

			// Initialize the bucket in fake-gcs-server (required because emulator starts empty)
			err = e2ehelpers.CreateFakeGCSBucket(ctx, cfg, admin, "gcs", fakeGCSEndpoint, fakeGCSBucket)
			Expect(err).NotTo(HaveOccurred(), "Failed to create bucket in fake-gcs-server")

			// Create cluster with GCS backup configuration.
			// fake-gcs-server is used as a provider compatibility smoke for backup writes only.
			drCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gcs-dr-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  openBaoVersion,
					Image:    openBaoImage,
					Replicas: 1,
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						}, // Operator will auto-create backup role
						Requests: e2ehelpers.CreateE2ERequests(tenantNamespace),
					},
					TLS: openbaov1alpha1.TLSConfig{
						Enabled:        true,
						Mode:           openbaov1alpha1.TLSModeOperatorManaged,
						RotationPeriod: "720h",
					},
					Storage: openbaov1alpha1.StorageConfig{
						Size: "1Gi",
					},
					Network: &openbaov1alpha1.NetworkConfig{
						APIServerCIDR:        apiServerCIDR,
						APIServerEndpointIPs: apiServerEndpointIPs,
					},
					Backup: &openbaov1alpha1.BackupSchedule{
						Schedule: "*/5 * * * *",
						Image:    backupExecutorImage,
						Target: openbaov1alpha1.BackupTarget{
							Provider:   constants.StorageProviderGCS,
							Endpoint:   fakeGCSEndpoint,
							Bucket:     fakeGCSBucket,
							PathPrefix: "clusters",
							GCS: &openbaov1alpha1.GCSTargetConfig{
								Project: fakeGCSProject,
							},
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
						Retention: &openbaov1alpha1.BackupRetention{
							MaxCount: 7,
							MaxAge:   "168h",
						},
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, drCluster)).To(Succeed())

			backupNetworkPolicy := newBackupNetworkPolicy(
				tenantNamespace,
				drCluster.Name,
				fakeGCSNamespace,
				4443,
				"backup",
			)
			Expect(admin.Create(ctx, backupNetworkPolicy)).To(Succeed())

			// Wait for cluster to be ready
			Eventually(func(g Gomega) {
				_ = tenantFW.TriggerReconcile(ctx, drCluster.Name)

				updated := &openbaov1alpha1.OpenBaoCluster{}
				err := admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.SelfInitialized).To(BeTrue())

				available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())
		})

		AfterAll(func() {
			if tenantFW == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			_ = tenantFW.Cleanup(cleanupCtx)
		})

		It("executes a manual backup to GCS", Label(
			"e2e-anchor",
			"case:dr-gcs-provider-backup-smoke",
			"provider-smoke",
		), func() {
			By("annotating the cluster to trigger a manual GCS backup")
			Eventually(func() error {
				return triggerManualBackup(ctx, admin, tenantNamespace, drCluster.Name)
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("forcing a reconcile after the manual GCS backup trigger")
			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())

			By("waiting for a GCS backup job to be created")
			waitForBackupJobCreated(ctx, admin, tenantNamespace, drCluster.Name)

			By("waiting for the GCS backup job to complete successfully")
			waitForSuccessfulBackupJob(ctx, admin, tenantNamespace, drCluster.Name)
		})
	})

	Context("Azure Backup & Restore with Azurite", func() {
		var (
			tenantNamespace   string
			tenantFW          *framework.Framework
			drCluster         *openbaov1alpha1.OpenBaoCluster
			credentialsSecret *corev1.Secret
			backupKey         string
		)

		BeforeAll(func() {
			var err error

			err = ensureAzurite(ctx, admin, azuriteNamespace)
			Expect(err).NotTo(HaveOccurred(), "Azurite deployment failed")

			tenantFW, err = framework.New(ctx, admin, "tenant-azure-dr", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace

			// Create Azure credentials Secret
			err = createAzureCredentialsSecret(ctx, admin, tenantNamespace, "azure-credentials")
			Expect(err).NotTo(HaveOccurred())
			credentialsSecret = &corev1.Secret{}
			Expect(admin.Get(ctx, types.NamespacedName{Name: "azure-credentials", Namespace: tenantNamespace}, credentialsSecret)).To(Succeed())

			// Initialize the container in Azurite (required because emulator starts empty)
			err = e2ehelpers.CreateAzuriteContainer(ctx, cfg, admin, "azure", azuriteEndpoint, azuriteContainer, azuriteKey)
			Expect(err).NotTo(HaveOccurred(), "Failed to create container in Azurite")

			// Create cluster with Azure backup/restore configuration
			// Using BootstrapJWTAuth to auto-create backup and restore roles
			drCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "azure-dr-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  openBaoVersion,
					Image:    openBaoImage,
					Replicas: 1,
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled:  true,
						OIDC:     &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}, // Operator will auto-create backup and restore roles
						Requests: e2ehelpers.CreateE2ERequests(tenantNamespace),
					},
					TLS: openbaov1alpha1.TLSConfig{
						Enabled:        true,
						Mode:           openbaov1alpha1.TLSModeOperatorManaged,
						RotationPeriod: "720h",
					},
					Storage: openbaov1alpha1.StorageConfig{
						Size: "1Gi",
					},
					Network: &openbaov1alpha1.NetworkConfig{
						APIServerCIDR:        apiServerCIDR,
						APIServerEndpointIPs: apiServerEndpointIPs,
					},
					Backup: &openbaov1alpha1.BackupSchedule{
						Schedule: "*/5 * * * *",
						Image:    backupExecutorImage,
						// JWTAuthRole not set - operator will auto-create backup role when OIDC is enabled
						Target: openbaov1alpha1.BackupTarget{
							Provider:   constants.StorageProviderAzure,
							Endpoint:   azuriteEndpoint,
							Bucket:     "unused-legacy-container", // azure.container selects the actual destination.
							PathPrefix: "clusters",
							Azure: &openbaov1alpha1.AzureTargetConfig{
								StorageAccount: azuriteAccount,
								Container:      azuriteContainer,
							},
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
						Retention: &openbaov1alpha1.BackupRetention{
							MaxCount: 7,
							MaxAge:   "168h",
						},
					},
					Restore: &openbaov1alpha1.RestoreConfig{
						JWTAuthRole: "restore", // Triggers auto-creation of restore policy/role via bootstrap
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, drCluster)).To(Succeed())

			backupNetworkPolicy := newBackupNetworkPolicy(
				tenantNamespace,
				drCluster.Name,
				azuriteNamespace,
				10000,
				"backup",
				"restore",
			)
			Expect(admin.Create(ctx, backupNetworkPolicy)).To(Succeed())

			// Wait for cluster to be ready
			Eventually(func(g Gomega) {
				_ = tenantFW.TriggerReconcile(ctx, drCluster.Name)

				updated := &openbaov1alpha1.OpenBaoCluster{}
				err := admin.Get(ctx, types.NamespacedName{Name: drCluster.Name, Namespace: tenantNamespace}, updated)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.SelfInitialized).To(BeTrue())

				available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())
		})

		AfterAll(func() {
			if tenantFW == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			_ = tenantFW.Cleanup(cleanupCtx)
		})

		It("executes a manual backup to Azure", Label(
			"e2e-anchor",
			"case:dr-azure-provider-backup-smoke",
			"provider-smoke",
		), func() {
			By("annotating the cluster to trigger a manual Azure backup")
			Eventually(func() error {
				return triggerManualBackup(ctx, admin, tenantNamespace, drCluster.Name)
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("forcing a reconcile after the manual Azure backup trigger")
			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())

			By("waiting for an Azure backup job to be created")
			waitForBackupJobCreated(ctx, admin, tenantNamespace, drCluster.Name)

			By("waiting for the Azure backup job to complete successfully")
			waitForSuccessfulBackupJob(ctx, admin, tenantNamespace, drCluster.Name)

			By("recording the latest Azure backup key from cluster status")
			recordLatestBackupKey(ctx, tenantFW, admin, tenantNamespace, drCluster.Name, &backupKey)
		})

		It("applies Azure retention in a separate storage workload", Label("e2e-anchor", "case:dr-azure-retention"), func() {
			By("waiting for the first backup and retention to release the operation lock")
			Eventually(func(g Gomega) {
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(drCluster), drCluster)).To(Succeed())
				g.Expect(drCluster.Status.OperationLock).To(BeNil())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			previousKey := drCluster.Status.Backup.LastBackupName
			By("retaining one snapshot and creating another verified backup")
			before := drCluster.DeepCopy()
			drCluster.Spec.Backup.Retention.MaxCount = 1
			Expect(admin.Patch(ctx, drCluster, client.MergeFrom(before))).To(Succeed())
			Expect(triggerManualBackup(ctx, admin, tenantNamespace, drCluster.Name)).To(Succeed())
			Expect(tenantFW.TriggerReconcile(ctx, drCluster.Name)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(drCluster), drCluster)).To(Succeed())
				g.Expect(drCluster.Status.Backup.LastBackupName).NotTo(Equal(previousKey))
				g.Expect(drCluster.Status.Backup.Retention).NotTo(BeNil())
				result := drCluster.Status.Backup.Retention.Last
				g.Expect(result).NotTo(BeNil())
				g.Expect(result.Key).To(Equal(drCluster.Status.Backup.LastBackupName))
				g.Expect(result.Outcome).To(Equal(openbaov1alpha1.BackupRetentionSucceeded))
				g.Expect(result.DeletedByCount).To(BeNumerically(">=", 1))
				g.Expect(drCluster.Status.Backup.Retention.Active).To(BeNil())
				g.Expect(drCluster.Status.OperationLock).To(BeNil())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			backupKey = drCluster.Status.Backup.LastBackupName
			By("checking that the worker retains storage identity without OpenBao authentication")
			job := &batchv1.Job{}
			Expect(admin.Get(ctx, client.ObjectKey{Namespace: tenantNamespace, Name: drCluster.Status.Backup.Retention.Last.Name}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal(drCluster.Name + constants.SuffixBackupServiceAccount))
			Expect(job.Spec.Template.Labels[constants.LabelOpenBaoBackupType]).To(Equal("retention"))
			for _, volume := range job.Spec.Template.Spec.Volumes {
				Expect(volume.Name).To(Equal("backup-credentials"))
			}
		})

		It("restores from Azure backup using OpenBaoRestore CR", Label(
			"case:backup-restore-restores-from-azure-backup-using-openbaorestore-b3ea354a",
		), func() {
			Expect(backupKey).NotTo(BeEmpty(), "backup key should have been set by previous test")

			By("creating an OpenBaoRestore resource from the Azure backup key")
			restore := &openbaov1alpha1.OpenBaoRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "azure-restore",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoRestoreSpec{
					Cluster: drCluster.Name,
					Source: openbaov1alpha1.RestoreSource{
						Target: openbaov1alpha1.BackupTarget{
							Provider:   constants.StorageProviderAzure,
							Endpoint:   azuriteEndpoint,
							Bucket:     "unused-legacy-container", // Preserve the Azure container alias for restores.
							PathPrefix: "clusters",
							Azure: &openbaov1alpha1.AzureTargetConfig{
								StorageAccount: azuriteAccount,
								Container:      azuriteContainer,
							},
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
						Key: backupKey,
					},
					JWTAuthRole: "restore",
					Image:       backupExecutorImage,
					Force:       true,
				},
			}

			_, _ = fmt.Fprintf(GinkgoWriter, "Creating OpenBaoRestore CR: %s\n", restore.Name)
			Expect(admin.Create(ctx, restore)).To(Succeed())

			waitForHeldRestore(ctx, admin, restore)
			acknowledgeHealthyRestore(ctx, admin, restore)
		})
	})
})

// These helpers exercise administrator release on healthy local nodes. They do
// not qualify process fencing when a Kubernetes node is unreachable.
func waitForHeldRestore(ctx context.Context, c client.Client, request *openbaov1alpha1.OpenBaoRestore) {
	Eventually(func(g Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
		g.Expect(request.Status.Phase).To(Equal(openbaov1alpha1.RestorePhaseUnknown))
		g.Expect(request.Status.SubmissionClaim).NotTo(BeNil())
		g.Expect(request.Status.Execution).NotTo(BeNil())
		g.Expect(request.Status.Execution.TerminalResult).To(Equal(openbaov1alpha1.RestoreExecutionResultSucceeded))
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(c.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, cluster)).To(Succeed())
		g.Expect(cluster.Annotations[constants.AnnotationRestoreHold]).To(Equal(string(request.UID)))
	}, 15*time.Minute, 10*time.Second).Should(Succeed())
}

func acknowledgeHealthyRestore(ctx context.Context, c client.Client, request *openbaov1alpha1.OpenBaoRestore) {
	// The executor has terminated. Resume also requires direct health checks of
	// every voter and managed Pod replacements; the status keeps the unconfirmed application outcome.
	before := request.DeepCopy()
	if request.Annotations == nil {
		request.Annotations = map[string]string{}
	}
	request.Annotations[constants.AnnotationRestoreAcknowledge] = string(request.UID) + "/Resume"
	Expect(c.Patch(ctx, request, client.MergeFrom(before))).To(Succeed())
	Eventually(func(g Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
		g.Expect(request.Status.AdministratorDisposition).To(Equal(openbaov1alpha1.RestoreAdministratorResume))
		g.Expect(request.Status.Phase).To(Equal(openbaov1alpha1.RestorePhaseUnknown))
		g.Expect(request.Status.Restart).NotTo(BeNil())
		g.Expect(request.Status.Restart.CompletedAt).NotTo(BeNil())
		for _, original := range request.Status.Restart.Pods {
			pod := &corev1.Pod{}
			g.Expect(c.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: original.Name}, pod)).To(Succeed())
			g.Expect(pod.UID).NotTo(Equal(original.UID))
		}
		cluster := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(c.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, cluster)).To(Succeed())
		g.Expect(cluster.Annotations[constants.AnnotationRestoreHold]).To(BeEmpty())
		g.Expect(cluster.Status.OperationLock).To(BeNil())
	}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

	metricsOutput, metricErr := framework.WaitForControllerMetricSubstrings(operatorNamespace, time.Minute,
		fmt.Sprintf(`openbao_restore_state{name="%s",namespace="%s"} 6`, request.Spec.Cluster, request.Namespace))
	Expect(metricErr).NotTo(HaveOccurred(), "Last metrics output:\n%s", metricsOutput)

	// A new decision cannot silently overwrite an already released recovery.
	conflicting := request.DeepCopy()
	conflicting.Annotations[constants.AnnotationRestoreAcknowledge] = string(request.UID) + "/Abandon"
	Expect(c.Patch(ctx, conflicting, client.MergeFrom(request), client.DryRunAll)).To(MatchError(ContainSubstring("Recovery has already been released")))

	// Finish the request while the controller still has namespace permissions.
	// Removing the tenant first can strand its finalizer during operator teardown.
	Expect(deleteMinimalFixtureObject(ctx, c, request)).To(Succeed())
}
