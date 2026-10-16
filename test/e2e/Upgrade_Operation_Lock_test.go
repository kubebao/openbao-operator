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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/service/backup"
	upgradecore "github.com/kubebao/openbao-operator/internal/service/upgrade/core"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

const (
	heldBackupExecutorImage        = "invalid.invalid/openbao-backup:e2e-hold"
	backupFirstMinSyncDuration     = "15s"
	backupFirstTLSRotationPeriod   = "720h"
	backupFirstStorageSize         = "1Gi"
	backupFirstObjectStorageRegion = "us-east-1"
)

type backupFirstUpgradeCase struct {
	clusterName    string
	strategy       openbaov1alpha1.UpdateStrategyType
	initialVersion string
	targetVersion  string
}

func runBackupFirstUpgradeCase(
	ctx context.Context,
	cfg *rest.Config,
	admin client.Client,
	tenantFW *framework.Framework,
	tenantNamespace string,
	credentialsSecretName string,
	testCase backupFirstUpgradeCase,
) {
	if testCase.initialVersion == testCase.targetVersion {
		Skip(fmt.Sprintf("Backup-first operation ordering test skipped: versions identical (%s)", testCase.initialVersion))
	}

	cluster := newBackupFirstUpgradeCluster(tenantNamespace, credentialsSecretName, testCase)
	clusterKey := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}

	By(fmt.Sprintf("creating the %s backup-first operation ordering cluster", testCase.strategy))
	Expect(admin.Create(ctx, cluster)).To(Succeed())
	DeferCleanup(func() {
		current := &openbaov1alpha1.OpenBaoCluster{}
		err := admin.Get(ctx, clusterKey, current)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(admin.Delete(ctx, current)).To(Succeed())
		Eventually(func() bool {
			err := admin.Get(ctx, clusterKey, &openbaov1alpha1.OpenBaoCluster{})
			return apierrors.IsNotFound(err)
		}, 5*time.Minute, 5*time.Second).Should(BeTrue())
	})

	waitForBackupFirstClusterReady(ctx, admin, clusterKey, testCase.strategy, testCase.initialVersion)

	By("configuring a backup Job that remains pending")
	Eventually(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		original := updated.DeepCopy()
		updated.Spec.Backup.Image = heldBackupExecutorImage
		g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
	}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

	preTriggerJobUIDs := backupJobUIDs(ctx, admin, cluster.Namespace, cluster.Name)
	Expect(triggerManualBackup(ctx, admin, cluster.Namespace, cluster.Name)).To(Succeed())
	Expect(tenantFW.TriggerReconcile(ctx, cluster.Name)).To(Succeed())

	By("waiting for the backup operation lock and held Job")
	var heldJob *batchv1.Job
	Eventually(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		g.Expect(updated.Status.OperationLock).NotTo(BeNil())
		g.Expect(updated.Status.OperationLock.Operation).To(Equal(openbaov1alpha1.ClusterOperationBackup))

		jobs := &batchv1.JobList{}
		g.Expect(admin.List(ctx, jobs,
			client.InNamespace(cluster.Namespace),
			client.MatchingLabels{
				constants.LabelOpenBaoCluster:   cluster.Name,
				constants.LabelOpenBaoComponent: backup.ComponentBackup,
			},
		)).To(Succeed())
		for i := range jobs.Items {
			job := &jobs.Items[i]
			if _, found := preTriggerJobUIDs[job.UID]; found {
				continue
			}
			g.Expect(job.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			if job.Spec.Template.Spec.Containers[0].Image != heldBackupExecutorImage {
				continue
			}
			g.Expect(job.Status.Succeeded).To(BeZero())
			g.Expect(job.Status.Failed).To(BeZero())
			heldJob = job.DeepCopy()
			break
		}
		g.Expect(heldJob).NotTo(BeNil())
	}, 5*time.Minute, time.Second).Should(Succeed())

	By("stopping the controller before the held backup can be processed")
	controllerDeployment, err := getControllerDeployment(ctx, admin, operatorNamespace)
	Expect(err).NotTo(HaveOccurred())
	controllerServiceAccount := controllerDeployment.Spec.Template.Spec.ServiceAccountName
	Expect(controllerServiceAccount).NotTo(BeEmpty())
	controllerUser := fmt.Sprintf(
		"system:serviceaccount:%s:%s",
		controllerDeployment.Namespace,
		controllerServiceAccount,
	)
	controllerGroups := []string{
		"system:serviceaccounts",
		fmt.Sprintf("system:serviceaccounts:%s", controllerDeployment.Namespace),
		"system:authenticated",
	}
	runAsController := func(action func(client.Client) error) error {
		return e2ehelpers.RunWithImpersonation(
			ctx,
			cfg,
			admin.Scheme(),
			controllerUser,
			controllerGroups,
			action,
		)
	}
	originalControllerReplicas := int32(1)
	if controllerDeployment.Spec.Replicas != nil {
		originalControllerReplicas = *controllerDeployment.Spec.Replicas
	}
	DeferCleanup(func() {
		current, err := getControllerDeployment(ctx, admin, operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		if current.Spec.Replicas == nil || *current.Spec.Replicas != originalControllerReplicas {
			Expect(scaleControllerDeployment(ctx, admin, operatorNamespace, originalControllerReplicas)).To(Succeed())
		}
	})
	// Register after controller restoration so failure evidence is captured
	// before cleanup restarts reconciliation or removes the backup workload.
	DeferCleanup(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		dumpBlueGreenUpgradeDiagnostics(cluster.Namespace, cluster.Name)
		pods := &corev1.PodList{}
		if err := admin.List(ctx, pods, client.InNamespace(cluster.Namespace), client.MatchingLabels{
			constants.LabelOpenBaoCluster:   cluster.Name,
			constants.LabelOpenBaoComponent: backup.ComponentBackup,
		}); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed to list backup Pods for diagnostics: %v\n", err)
			return
		}
		for _, pod := range pods.Items {
			dumpKubectlOutput("describe", "pod", pod.Name, "-n", pod.Namespace)
			dumpKubectlOutput("logs", pod.Name, "-n", pod.Namespace, "--all-containers=true", "--tail=200")
		}
	})
	Expect(scaleControllerDeployment(ctx, admin, operatorNamespace, 0)).To(Succeed())

	By("requesting the upgrade while the backup owns the operation lock")
	Eventually(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		original := updated.DeepCopy()
		updated.Spec.Version = testCase.targetVersion
		updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", testCase.targetVersion)
		updated.Spec.Backup.Image = backupExecutorImage
		g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
	}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

	By("replacing the held Job with the same Job configured to complete")
	Expect(runAsController(func(controllerClient client.Client) error {
		// Kubernetes can otherwise orphan the invalid-image Pod when the Job is deleted.
		return controllerClient.Delete(ctx, heldJob, client.PropagationPolicy(metav1.DeletePropagationForeground))
	})).To(Succeed())
	Eventually(func() bool {
		err := admin.Get(ctx, types.NamespacedName{Name: heldJob.Name, Namespace: heldJob.Namespace}, &batchv1.Job{})
		return apierrors.IsNotFound(err)
	}, 2*time.Minute, time.Second).Should(BeTrue())

	replacementJob := replacementBackupJob(heldJob, backupExecutorImage)
	Expect(runAsController(func(controllerClient client.Client) error {
		return controllerClient.Create(ctx, replacementJob)
	})).To(Succeed())
	Eventually(func(g Gomega) {
		completed := &batchv1.Job{}
		g.Expect(admin.Get(ctx, types.NamespacedName{Name: replacementJob.Name, Namespace: replacementJob.Namespace}, completed)).To(Succeed())
		g.Expect(completed.Status.Failed).To(BeZero())
		g.Expect(completed.Status.Succeeded).To(BeNumerically(">", 0))
	}, 10*time.Minute, 5*time.Second).Should(Succeed())

	backupKey := replacementJob.Annotations["openbao.org/backup-key"]
	Expect(backupKey).NotTo(BeEmpty())
	Consistently(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		g.Expect(updated.Status.OperationLock).NotTo(BeNil())
		g.Expect(updated.Status.OperationLock.Operation).To(Equal(openbaov1alpha1.ClusterOperationBackup))
		g.Expect(updated.Status.Backup).NotTo(BeNil())
		g.Expect(updated.Status.Backup.LastBackupName).NotTo(Equal(backupKey))
	}, 5*time.Second, time.Second).Should(Succeed())

	By("starting the controller with a terminal backup Job and a pending upgrade")
	Expect(scaleControllerDeployment(ctx, admin, operatorNamespace, originalControllerReplicas)).To(Succeed())
	Expect(tenantFW.TriggerReconcile(ctx, cluster.Name)).To(Succeed())

	By("verifying the backup result is processed and the backup lock is released")
	Eventually(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		g.Expect(updated.Status.Backup).NotTo(BeNil())
		g.Expect(updated.Status.Backup.LastBackupName).To(Equal(backupKey))
		if updated.Status.OperationLock != nil {
			g.Expect(updated.Status.OperationLock.Operation).NotTo(Equal(openbaov1alpha1.ClusterOperationBackup))
		}
	}, 3*time.Minute, 2*time.Second).Should(Succeed())

	By(fmt.Sprintf("verifying the %s upgrade starts after backup completion", testCase.strategy))
	Eventually(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		g.Expect(updated.Status.OperationLock).NotTo(BeNil())
		g.Expect(updated.Status.OperationLock.Operation).To(Equal(openbaov1alpha1.ClusterOperationUpgrade))

		if testCase.strategy == openbaov1alpha1.UpdateStrategyBlueGreen {
			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.Phase).NotTo(Equal(openbaov1alpha1.PhaseIdle))
			g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
			return
		}
		g.Expect(updated.Status.Upgrade).NotTo(BeNil())
		g.Expect(updated.Status.Upgrade.TargetVersion).To(Equal(testCase.targetVersion))
	}, 5*time.Minute, 2*time.Second).Should(Succeed())
}

func newBackupFirstUpgradeCluster(
	namespace string,
	credentialsSecretName string,
	testCase backupFirstUpgradeCase,
) *openbaov1alpha1.OpenBaoCluster {
	upgradeConfig := &openbaov1alpha1.UpgradeConfig{
		Strategy: testCase.strategy,
		Image:    upgradeExecutorImage,
	}
	if testCase.strategy == openbaov1alpha1.UpdateStrategyBlueGreen {
		upgradeConfig.BlueGreen = &openbaov1alpha1.BlueGreenConfig{
			AutoPromote: true,
			Verification: &openbaov1alpha1.VerificationConfig{
				MinSyncDuration: backupFirstMinSyncDuration,
			},
		}
	}

	return &openbaov1alpha1.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{Name: testCase.clusterName, Namespace: namespace},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile:  openbaov1alpha1.ProfileDevelopment,
			Version:  testCase.initialVersion,
			Image:    fmt.Sprintf("openbao/openbao:%s", testCase.initialVersion),
			Replicas: 1,
			InitContainer: &openbaov1alpha1.InitContainerConfig{
				Enabled: true,
				Image:   configInitImage,
			},
			SelfInit: &openbaov1alpha1.SelfInitConfig{
				Enabled:  true,
				OIDC:     &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true},
				Requests: e2ehelpers.CreateE2ERequests(namespace),
			},
			TLS: openbaov1alpha1.TLSConfig{
				Enabled:        true,
				Mode:           openbaov1alpha1.TLSModeOperatorManaged,
				RotationPeriod: backupFirstTLSRotationPeriod,
			},
			Storage: openbaov1alpha1.StorageConfig{Size: backupFirstStorageSize},
			Network: &openbaov1alpha1.NetworkConfig{
				APIServerCIDR:        apiServerCIDR,
				APIServerEndpointIPs: apiServerEndpointIPs,
				APIServerEgress:      framework.APIServerEgressForNetworkProfile(),
			},
			Upgrade: upgradeConfig,
			Backup: &openbaov1alpha1.BackupSchedule{
				Schedule: "0 5 * * *",
				Image:    backupExecutorImage,
				Target: openbaov1alpha1.BackupTarget{
					Endpoint:     rustfsEndpoint,
					Bucket:       rustfsBucket,
					Region:       backupFirstObjectStorageRegion,
					UsePathStyle: true,
					CredentialsSecretRef: &corev1.LocalObjectReference{
						Name: credentialsSecretName,
					},
				},
			},
			DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
		},
	}
}

func waitForBackupFirstClusterReady(
	ctx context.Context,
	admin client.Client,
	clusterKey types.NamespacedName,
	strategy openbaov1alpha1.UpdateStrategyType,
	initialVersion string,
) {
	Eventually(func(g Gomega) {
		updated := &openbaov1alpha1.OpenBaoCluster{}
		g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
		g.Expect(updated.Status.Initialized).To(BeTrue())
		g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))

		available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
		g.Expect(available).NotTo(BeNil())
		g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		if strategy == openbaov1alpha1.UpdateStrategyBlueGreen {
			g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
			g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
		}
	}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

	Eventually(func(g Gomega) {
		statefulSets := &appsv1.StatefulSetList{}
		g.Expect(admin.List(ctx, statefulSets,
			client.InNamespace(clusterKey.Namespace),
			client.MatchingLabels{constants.LabelOpenBaoCluster: clusterKey.Name},
		)).To(Succeed())
		var readyReplicas int32
		for i := range statefulSets.Items {
			readyReplicas += statefulSets.Items[i].Status.ReadyReplicas
		}
		g.Expect(readyReplicas).To(Equal(int32(1)))
	}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
}

func backupJobUIDs(ctx context.Context, admin client.Client, namespace, clusterName string) map[types.UID]struct{} {
	jobs := &batchv1.JobList{}
	ExpectWithOffset(1, admin.List(ctx, jobs,
		client.InNamespace(namespace),
		client.MatchingLabels{
			constants.LabelOpenBaoCluster:   clusterName,
			constants.LabelOpenBaoComponent: backup.ComponentBackup,
		},
	)).To(Succeed())

	uids := make(map[types.UID]struct{}, len(jobs.Items))
	for i := range jobs.Items {
		uids[jobs.Items[i].UID] = struct{}{}
	}
	return uids
}

func replacementBackupJob(heldJob *batchv1.Job, executorImage string) *batchv1.Job {
	replacement := heldJob.DeepCopy()
	replacement.ResourceVersion = ""
	replacement.UID = ""
	replacement.Generation = 0
	replacement.CreationTimestamp = metav1.Time{}
	replacement.DeletionTimestamp = nil
	replacement.DeletionGracePeriodSeconds = nil
	replacement.ManagedFields = nil
	replacement.Finalizers = nil
	replacement.Status = batchv1.JobStatus{}
	replacement.Spec.Selector = nil
	replacement.Spec.ManualSelector = nil

	for _, label := range []string{
		batchv1.ControllerUidLabel,
		batchv1.JobNameLabel,
		"controller-uid",
		"job-name",
	} {
		delete(replacement.Labels, label)
		delete(replacement.Spec.Template.Labels, label)
	}
	replacement.Spec.Template.Spec.Containers[0].Image = executorImage
	return replacement
}

var _ = Describe("Upgrade Strategies: Operation Lock Contention", Label("upgrade", "backup", "operation-lock", "slow"), Serial, Ordered, func() {
	ctx := context.Background()

	var (
		cfg               *rest.Config
		tenantFW          *framework.Framework
		tenantNamespace   string
		admin             client.Client
		lockCluster       *openbaov1alpha1.OpenBaoCluster
		initialVersion    string
		initialImage      string
		targetVersion     string
		targetImage       string
		credentialsSecret *corev1.Secret
	)

	BeforeAll(func() {
		var err error

		cfg, err = ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		tenantFW, err = framework.NewSetup(ctx, "tenant-upgrade-lock", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		tenantNamespace = tenantFW.Namespace
		admin = tenantFW.Client

		initialVersion = envOrDefault("E2E_UPGRADE_FROM_VERSION", defaultUpgradeFromVersion)
		targetVersion = envOrDefault("E2E_UPGRADE_TO_VERSION", defaultUpgradeToVersion)
		initialImage = fmt.Sprintf("openbao/openbao:%s", initialVersion)
		targetImage = fmt.Sprintf("openbao/openbao:%s", targetVersion)

		if initialVersion == targetVersion {
			Skip(fmt.Sprintf("Operation lock contention test skipped: versions identical (%s)", initialVersion))
		}

		err = ensureRustFS(ctx, admin, cfg)
		Expect(err).NotTo(HaveOccurred(), "RustFS deployment failed")

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

		lockCluster = &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "upgrade-lock-cluster",
				Namespace: tenantNamespace,
			},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile:  openbaov1alpha1.ProfileDevelopment,
				Version:  initialVersion,
				Image:    initialImage,
				Replicas: 3,
				InitContainer: &openbaov1alpha1.InitContainerConfig{
					Enabled: true,
					Image:   configInitImage,
				},
				SelfInit: &openbaov1alpha1.SelfInitConfig{
					Enabled: true,
					OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
						Enabled: true,
					},
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
					APIServerEgress:      framework.APIServerEgressForNetworkProfile(),
				},
				Upgrade: &openbaov1alpha1.UpgradeConfig{
					Image: upgradeExecutorImage,
				},
				Backup: &openbaov1alpha1.BackupSchedule{
					Schedule: "0 0 * * *",
					Image:    backupExecutorImage,
					Target: openbaov1alpha1.BackupTarget{
						Endpoint:     rustfsEndpoint,
						Bucket:       rustfsBucket,
						Region:       "us-east-1",
						UsePathStyle: true,
						CredentialsSecretRef: &corev1.LocalObjectReference{
							Name: credentialsSecret.Name,
						},
					},
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		Expect(admin.Create(ctx, lockCluster)).To(Succeed())

		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, types.NamespacedName{Name: lockCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
			g.Expect(updated.Status.Initialized).To(BeTrue())
			g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))

			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		_, err = tenantFW.WaitForStatefulSetReady(
			ctx,
			lockCluster.Name,
			3,
			framework.DefaultLongWaitTimeout,
			framework.DefaultPollInterval,
		)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		if tenantFW != nil {
			cleanupUpgradeFixture(ctx, tenantFW)
		}
	})

	It("processes a completed backup before starting a rolling upgrade", Label(
		"e2e-anchor",
		"case:backup-first-rolling-operation-order",
		"covers:operation-lock",
		"covers:backup-owner-first",
		"covers:rolling-upgrade",
	), func() {
		runBackupFirstUpgradeCase(ctx, cfg, admin, tenantFW, tenantNamespace, credentialsSecret.Name, backupFirstUpgradeCase{
			clusterName:    "backup-first-rolling",
			strategy:       openbaov1alpha1.UpdateStrategyRollingUpdate,
			initialVersion: initialVersion,
			targetVersion:  targetVersion,
		})
	})

	It("processes a completed backup before starting a blue-green upgrade", Label(
		"e2e-anchor",
		"case:backup-first-bluegreen-operation-order",
		"covers:operation-lock",
		"covers:backup-owner-first",
		"covers:bluegreen-upgrade",
	), func() {
		runBackupFirstUpgradeCase(ctx, cfg, admin, tenantFW, tenantNamespace, credentialsSecret.Name, backupFirstUpgradeCase{
			clusterName:    "backup-first-bluegreen",
			strategy:       openbaov1alpha1.UpdateStrategyBlueGreen,
			initialVersion: blueGreenUpgradeFromVersion(),
			targetVersion:  blueGreenUpgradeToVersion(),
		})
	})

	It("holds a manual backup request until the rolling upgrade lock is released", Label(
		"e2e-anchor",
		"case:upgrade-backup-lock-contention",
		"covers:operation-lock",
		"covers:rolling-upgrade",
		"covers:backup-queueing",
	), func() {
		clusterKey := types.NamespacedName{Name: lockCluster.Name, Namespace: tenantNamespace}
		preTriggerJobUIDs := map[types.UID]struct{}{}

		By("starting a rolling upgrade")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			original := updated.DeepCopy()
			updated.Spec.Version = targetVersion
			updated.Spec.Image = targetImage
			g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		Expect(tenantFW.TriggerReconcile(ctx, lockCluster.Name)).To(Succeed())

		By("waiting for the upgrade controller to hold the cluster operation lock")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			g.Expect(updated.Status.Upgrade).NotTo(BeNil())
			g.Expect(updated.Status.OperationLock).NotTo(BeNil())
			g.Expect(updated.Status.OperationLock.Operation).To(Equal(openbaov1alpha1.ClusterOperationUpgrade))
			g.Expect(updated.Status.OperationLock.Holder).To(Equal(upgradecore.UpgradeOperationLockHolder))

			upgrading := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionUpgrading))
			g.Expect(upgrading).NotTo(BeNil())
			g.Expect(upgrading.Status).To(Equal(metav1.ConditionTrue))
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())

		By("requesting a manual backup while the upgrade lock is held")
		{
			jobs := &batchv1.JobList{}
			Expect(admin.List(ctx, jobs,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster:   lockCluster.Name,
					constants.LabelOpenBaoComponent: backup.ComponentBackup,
				},
			)).To(Succeed())
			for i := range jobs.Items {
				preTriggerJobUIDs[jobs.Items[i].UID] = struct{}{}
			}
		}
		Expect(triggerManualBackup(ctx, admin, tenantNamespace, lockCluster.Name)).To(Succeed())

		By("verifying the backup request remains queued behind the active upgrade")
		Consistently(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			g.Expect(updated.Status.OperationLock).NotTo(BeNil())
			g.Expect(updated.Status.OperationLock.Operation).To(Equal(openbaov1alpha1.ClusterOperationUpgrade))
			g.Expect(updated.Status.OperationLock.Holder).To(Equal(upgradecore.UpgradeOperationLockHolder))
			g.Expect(updated.Annotations).To(HaveKey(constants.AnnotationTriggerBackup))

			jobs := &batchv1.JobList{}
			g.Expect(admin.List(ctx, jobs,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster:   lockCluster.Name,
					constants.LabelOpenBaoComponent: backup.ComponentBackup,
				},
			)).To(Succeed())
			g.Expect(jobs.Items).To(BeEmpty(), "backup job should not start while the upgrade lock is held")
		}, 20*time.Second, 5*time.Second).Should(Succeed())

		By("waiting for the upgrade to complete")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			g.Expect(updated.Status.Upgrade).To(BeNil())
			g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))

			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(available).NotTo(BeNil())
			g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
		}, 20*time.Minute, 10*time.Second).Should(Succeed())

		By("re-triggering reconcile so the queued backup request is picked up immediately")
		Expect(tenantFW.TriggerReconcile(ctx, lockCluster.Name)).To(Succeed())

		By("verifying the queued backup starts once the upgrade lock is released")
		Eventually(func(g Gomega) {
			updated := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(admin.Get(ctx, clusterKey, updated)).To(Succeed())
			if updated.Annotations != nil {
				g.Expect(updated.Annotations).NotTo(HaveKey(constants.AnnotationTriggerBackup))
			}

			jobs := &batchv1.JobList{}
			g.Expect(admin.List(ctx, jobs,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster:   lockCluster.Name,
					constants.LabelOpenBaoComponent: backup.ComponentBackup,
				},
			)).To(Succeed())
			hasNewJob := false
			for i := range jobs.Items {
				if _, found := preTriggerJobUIDs[jobs.Items[i].UID]; found {
					continue
				}
				hasNewJob = true
				break
			}
			g.Expect(hasNewJob).To(BeTrue(), "queued backup should create a backup job after the upgrade lock is released")
			g.Expect(updated.Status.Backup).NotTo(BeNil())
			g.Expect(updated.Status.Backup.LastAttemptScheduledTime).NotTo(BeNil())

			// Backup execution is short in CI, so the lock may already be released by the time we observe the new Job.
			if updated.Status.OperationLock != nil {
				g.Expect(updated.Status.OperationLock.Operation).To(Equal(openbaov1alpha1.ClusterOperationBackup))
			}
		}, 5*time.Minute, 10*time.Second).Should(Succeed())
	})
})
