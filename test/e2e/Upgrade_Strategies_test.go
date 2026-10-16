//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/internal/platform/resourceidentity"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/internal/service/upgrade"
	"github.com/kubebao/openbao-operator/internal/service/upgrade/bluegreen"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
	"github.com/kubebao/openbao-operator/test/utils"
)

// === Shared Helpers ===

const (
	upgradeActionAnnotationKey = "openbao.org/upgrade-action"
	upgradeRunIDAnnotationKey  = "openbao.org/upgrade-run-id"
	rollbackRunID              = "rollback"
	invalidUpgradeJWTAuthRole  = "invalid-upgrade-role"
	e2eAdminPolicyName         = "e2e-admin"
	e2eAdminRoleName           = "e2e-admin"
)

type serviceAvailabilityStats struct {
	Samples                int
	Failures               int
	ConsecutiveFailures    int
	MaxConsecutiveFailures int
	LastFailure            string
}

func probeOpenBaoServiceHealthViaAPIProxy(ctx context.Context, cfg *rest.Config, namespace, serviceName string) (int, error) {
	if cfg == nil {
		return 0, fmt.Errorf("rest config is required")
	}
	if namespace == "" {
		return 0, fmt.Errorf("namespace is required")
	}
	if serviceName == "" {
		return 0, fmt.Errorf("service name is required")
	}

	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return 0, fmt.Errorf("failed to create API transport: %w", err)
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	url := fmt.Sprintf(
		"%s/api/v1/namespaces/%s/services/https:%s:%d/proxy/v1/sys/health?standbyok=true&perfstandbyok=true",
		cfg.Host,
		namespace,
		serviceName,
		constants.PortAPI,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build health probe request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("health probe request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	return resp.StatusCode, nil
}

func isOpenBaoServiceAvailableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusOK, http.StatusTooManyRequests, 472, 473:
		return true
	default:
		return false
	}
}

func findUpgradeExecutorJob(jobs []batchv1.Job, action bluegreen.ExecutorAction, runID string) *batchv1.Job {
	for i := range jobs {
		job := &jobs[i]
		if job.Annotations == nil {
			continue
		}
		if job.Annotations[upgradeActionAnnotationKey] != string(action) {
			continue
		}
		// Existing callers select an attempt within a single upgrade operation.
		_, attempt, scoped := strings.Cut(job.Annotations[upgradeRunIDAnnotationKey], "/")
		if !scoped {
			attempt = job.Annotations[upgradeRunIDAnnotationKey]
		}
		if attempt != runID {
			continue
		}
		return job
	}
	return nil
}

func jobFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	if job.Status.Failed > 0 {
		return true
	}
	for _, cond := range job.Status.Conditions {
		if cond.Status != "True" {
			continue
		}
		if cond.Type == batchv1.JobFailed {
			return true
		}
	}
	return false
}

func jobSucceeded(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	if job.Status.Succeeded > 0 {
		return true
	}
	for _, cond := range job.Status.Conditions {
		if cond.Status != "True" {
			continue
		}
		if cond.Type == batchv1.JobComplete {
			return true
		}
	}
	return false
}

func hasSucceededUpgradeAction(jobs []batchv1.Job, action bluegreen.ExecutorAction) bool {
	for i := range jobs {
		job := &jobs[i]
		if job.Annotations == nil {
			continue
		}
		if job.Annotations[upgradeActionAnnotationKey] != string(action) {
			continue
		}
		if jobSucceeded(job) {
			return true
		}
	}
	return false
}

func ptrTo[T any](v T) *T {
	return &v
}

type blueGreenRevisionSelector func(*openbaov1alpha1.BlueGreenStatus) string

func expectBlueGreenServiceSelectorDuringPhase(
	g Gomega,
	ctx context.Context,
	c client.Client,
	namespace string,
	clusterName string,
	phase openbaov1alpha1.BlueGreenPhase,
	revision blueGreenRevisionSelector,
	revisionMessage string,
) {
	updated := &openbaov1alpha1.OpenBaoCluster{}
	g.Expect(c.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, updated)).To(Succeed())
	g.Expect(updated.Status.BlueGreen).NotTo(BeNil(), "BlueGreen status should be initialized")

	if updated.Status.BlueGreen.Phase != phase {
		current := updated.Status.BlueGreen.Phase
		passed := phase == openbaov1alpha1.PhaseDemotingBlue && current == openbaov1alpha1.PhaseCleanup ||
			current == openbaov1alpha1.PhaseRestoringReadReplicas ||
			(current == openbaov1alpha1.PhaseIdle && updated.Spec.Version != "" &&
				updated.Status.CurrentVersion == updated.Spec.Version)
		if passed {
			StopTrying(fmt.Sprintf("upgrade advanced to %s without observing the Service selector in %s", current, phase)).Now()
		}
		g.Expect(current).To(Equal(phase), "the requested phase must be observed before checking its Service selector")
		return
	}

	g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty(), revisionMessage)

	svc := &corev1.Service{}
	g.Expect(c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      fmt.Sprintf("%s-public", clusterName),
	}, svc)).To(Succeed())
	g.Expect(svc.Spec.Selector).To(HaveKeyWithValue(
		constants.LabelOpenBaoRevision,
		revision(updated.Status.BlueGreen),
	))
}

func dumpKubectlOutput(args ...string) {
	cmd := exec.Command("kubectl", args...) // #nosec G204 -- E2E diagnostics command with fixed binary and controlled args.
	output, err := utils.Run(cmd)
	_, _ = fmt.Fprintf(GinkgoWriter, "\n$ kubectl %v\n", args)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl command failed: %v\n", err)
	}
	if output == "" {
		_, _ = fmt.Fprintln(GinkgoWriter, "(no output)")
		return
	}
	_, _ = fmt.Fprintln(GinkgoWriter, output)
}

func dumpRollingUpgradeDiagnostics(ctx context.Context, admin client.Client, namespace, clusterName string) {
	if namespace == "" || clusterName == "" {
		return
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "\n========== Rolling Upgrade Diagnostics (%s/%s) ==========\n", namespace, clusterName)
	dumpKubectlOutput("get", "openbaocluster", clusterName, "-n", namespace, "-o", "yaml")
	dumpKubectlOutput("get", "statefulset", clusterName, "-n", namespace, "-o", "yaml")
	dumpKubectlOutput("get", "pods", "-n", namespace, "-o", "wide")
	dumpKubectlOutput("get", "jobs", "-n", namespace, "-o", "wide")
	dumpKubectlOutput("get", "events", "-n", namespace, "--sort-by=.lastTimestamp")

	dumpKubectlOutput("logs", "deployment/openbao-operator-controller", "-n", operatorNamespace, "--tail=400")

	if admin == nil || ctx == nil {
		_, _ = fmt.Fprintln(GinkgoWriter, "Skipping step-down job diagnostics: client/context unavailable")
		return
	}

	jobList := &batchv1.JobList{}
	if err := admin.List(ctx, jobList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			constants.LabelOpenBaoCluster:   clusterName,
			constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
		},
	); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to list upgrade jobs for diagnostics: %v\n", err)
		return
	}

	for i := range jobList.Items {
		job := &jobList.Items[i]
		if job.Annotations == nil {
			continue
		}
		if job.Annotations[upgradeActionAnnotationKey] != string(upgrade.ExecutorActionRollingStepDownLeader) {
			continue
		}

		_, _ = fmt.Fprintf(GinkgoWriter, "\n------ Step-down job diagnostics: %s ------\n", job.Name)
		dumpKubectlOutput("describe", "job", job.Name, "-n", namespace)
		dumpKubectlOutput("get", "pods", "-n", namespace, "-l", fmt.Sprintf("job-name=%s", job.Name), "-o", "wide")
		dumpKubectlOutput("logs", fmt.Sprintf("job/%s", job.Name), "-n", namespace, "--all-containers=true")
	}
}

func dumpBlueGreenUpgradeDiagnostics(namespace, clusterName string) {
	if namespace == "" || clusterName == "" {
		return
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "\n========== Blue/Green Upgrade Diagnostics (%s/%s) ==========\n", namespace, clusterName)
	dumpKubectlOutput("get", "openbaocluster", clusterName, "-n", namespace, "-o", "yaml")
	dumpKubectlOutput("get", "statefulset", "-n", namespace, "-l", fmt.Sprintf("%s=%s", constants.LabelOpenBaoCluster, clusterName), "-o", "yaml")
	dumpKubectlOutput("get", "pods", "-n", namespace, "-l", fmt.Sprintf("%s=%s", constants.LabelOpenBaoCluster, clusterName), "-o", "wide")
	dumpKubectlOutput("get", "jobs", "-n", namespace, "-l", fmt.Sprintf("%s=%s", constants.LabelOpenBaoCluster, clusterName), "-o", "yaml")
	dumpKubectlOutput("get", "persistentvolumeclaims", "-n", namespace, "-l", fmt.Sprintf("%s=%s", constants.LabelOpenBaoCluster, clusterName), "-o", "wide")
	dumpKubectlOutput("logs", "-n", namespace, "-l", fmt.Sprintf("%s=%s", constants.LabelOpenBaoCluster, clusterName), "--all-containers=true", "--prefix=true", "--tail=100")
	dumpKubectlOutput("get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
	dumpKubectlOutput("logs", "deployment/openbao-operator-controller", "-n", operatorNamespace, "--tail=400")
}

func rollingPreUpgradeBackupJobName(clusterName string, generation int64) string {
	return fmt.Sprintf("pre-upgrade-backup-%s-gen%d", clusterName, generation)
}

func policyWriteCommand(policyName, policy string) string {
	return fmt.Sprintf(`
cat <<'EOF' >/tmp/policy.hcl
%s
EOF
bao policy write %s /tmp/policy.hcl
`, policy, policyName)
}

func newStaleRollingStepDownJob(cluster *openbaov1alpha1.OpenBaoCluster, podName, image string) *batchv1.Job {
	var backoffLimit int32

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      upgrade.ExecutorJobName(cluster.Name, upgrade.ExecutorActionRollingStepDownLeader, podName, "", ""),
			Namespace: cluster.Namespace,
			Annotations: map[string]string{
				constants.AnnotationOpenBaoOwnerUID: string(cluster.UID),
			},
			Labels: map[string]string{
				constants.LabelOpenBaoCluster:        cluster.Name,
				constants.LabelOpenBaoComponent:      upgrade.ComponentUpgrade,
				"e2e.openbao.org/stale-stepdown-job": "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         openbaov1alpha1.GroupVersion.String(),
					Kind:               "OpenBaoCluster",
					Name:               cluster.Name,
					UID:                cluster.UID,
					Controller:         ptrTo(true),
					BlockOwnerDeletion: ptrTo(true),
				},
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrTo(true),
						RunAsUser:    ptrTo(int64(100)),
						RunAsGroup:   ptrTo(int64(1000)),
						FSGroup:      ptrTo(int64(1000)),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "stale-stepdown",
							Image:   image,
							Command: []string{"/bin/sh", "-ec"},
							Args:    []string{"echo stale-stepdown-job"},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptrTo(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								RunAsNonRoot: ptrTo(true),
							},
						},
					},
				},
			},
		},
	}
}

// === Tests ===

var _ = Describe("Upgrade Strategies", Label("upgrade", "upgrades", "cluster", "slow"), func() {
	ctx := context.Background()

	// --- Rolling Upgrade ---
	Context("Rolling Upgrade", Label("rolling"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			upgradeCluster  *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			initialImage    string
			targetVersion   string
			targetImage     string
			admin           client.Client
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-upgrade", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			initialVersion = envOrDefault("E2E_UPGRADE_FROM_VERSION", defaultUpgradeFromVersion)
			targetVersion = envOrDefault("E2E_UPGRADE_TO_VERSION", defaultUpgradeToVersion)
			initialImage = fmt.Sprintf("openbao/openbao:%s", initialVersion)
			targetImage = fmt.Sprintf("openbao/openbao:%s", targetVersion)

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Upgrade test skipped: versions identical (%s)", initialVersion))
			}

			upgradeCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "upgrade-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    initialImage,
					Replicas: 3,
					ReadReplicas: &openbaov1alpha1.ReadReplicaConfig{
						Replicas: 1,
						Service: &openbaov1alpha1.ReadReplicaServiceConfig{
							Enabled: true,
						},
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						}, // Operator will auto-create upgrade role
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, upgradeCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))

				available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			_, err = tenantFW.WaitForStatefulSetReady(
				ctx,
				upgradeCluster.Name,
				3,
				framework.DefaultLongWaitTimeout,
				framework.DefaultPollInterval,
			)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
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
					Name:      resourceidentity.ReadReplicaStatefulSetName(upgradeCluster),
					Namespace: tenantNamespace,
				}, readSts)).To(Succeed())
				g.Expect(readSts.Status.ReadyReplicas).To(Equal(int32(1)))

				readSvc := &corev1.Service{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      resourceidentity.ReadReplicaServiceName(upgradeCluster),
					Namespace: tenantNamespace,
				}, readSvc)).To(Succeed())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		AfterEach(func() {
			if !CurrentSpecReport().Failed() {
				return
			}
			if upgradeCluster == nil {
				return
			}

			By("Collecting rolling upgrade diagnostics")
			dumpRollingUpgradeDiagnostics(ctx, admin, tenantNamespace, upgradeCluster.Name)
		})

		It("performs rolling upgrade", Label(
			"e2e-anchor",
			"case:upgrade-rolling-happy-path",
			"covers:lifecycle-job-owner-proof",
			"read-replicas",
			"read-replicas-rolling",
		), func() {
			cfg, err := ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			By("Writing a secret before upgrade")
			// Note: bao kv put/get automatically adds /data/ for KV v2, so use path without /data/
			secretPath := "secret/rolling-upgrade-test"
			secretData := map[string]string{"foo": "bar", "version": "v1"}
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   upgradeCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}

			// Enable KV engine (idempotent) - dev mode usually has it enabled at secret/
			// but we'll try to write directly first.
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(ctx, cfg, admin, tenantNamespace, initialImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, secretData)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed(), "Failed to write pre-upgrade secret")

			By("Triggering upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = targetImage
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			// Trigger reconcile to ensure upgrade manager processes the version change
			Expect(tenantFW.TriggerReconcile(ctx, upgradeCluster.Name)).To(Succeed())

			// Wait for upgrade to be initialized - Status.Upgrade must be set
			// This is the authoritative indicator that the upgrade manager has started the upgrade
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).NotTo(BeNil(), "Status.Upgrade should be set when upgrade is initialized")
				g.Expect(updated.Status.Upgrade.TargetVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Upgrade.FromVersion).To(Equal(initialVersion))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the steady read pool converges before voter rollout starts")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.ReadReplicas).NotTo(BeNil())
				g.Expect(updated.Status.ReadReplicas.ReadyReplicas).To(Equal(int32(1)))
				g.Expect(updated.Status.ReadReplicas.RegisteredReplicas).To(Equal(int32(1)))
				for _, condType := range []openbaov1alpha1.ConditionType{
					openbaov1alpha1.ConditionReadReplicasReady,
					openbaov1alpha1.ConditionReadServingAvailable,
					openbaov1alpha1.ConditionRaftMembershipReady,
				} {
					cond := meta.FindStatusCondition(updated.Status.Conditions, string(condType))
					g.Expect(cond).NotTo(BeNil(), "expected read-replica condition %s", condType)
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue), "expected read-replica condition %s to be true", condType)
				}

				readSts := &appsv1.StatefulSet{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      resourceidentity.ReadReplicaStatefulSetName(upgradeCluster),
					Namespace: tenantNamespace,
				}, readSts)).To(Succeed())
				g.Expect(readSts.Status.ReadyReplicas).To(Equal(int32(1)))

				readPods := &corev1.PodList{}
				g.Expect(admin.List(ctx, readPods,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster:      upgradeCluster.Name,
						constants.LabelOpenBaoWorkloadPool: constants.LabelValueOpenBaoWorkloadPoolReadReplica,
					},
				)).To(Succeed())
				g.Expect(readPods.Items).To(HaveLen(1))
				for _, pod := range readPods.Items {
					e2ehelpers.ExpectOpenBaoPodVersion(g, pod, targetVersion)
				}

				voterPods := &corev1.PodList{}
				g.Expect(admin.List(ctx, voterPods,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster:      upgradeCluster.Name,
						constants.LabelOpenBaoWorkloadPool: constants.LabelValueOpenBaoWorkloadPoolVoter,
					},
				)).To(Succeed())
				g.Expect(voterPods.Items).To(HaveLen(int(upgradeCluster.Spec.Replicas)))

				voterRolloutStarted := false
				for _, pod := range voterPods.Items {
					if pod.Labels[portopenbao.LabelVersion] == targetVersion {
						voterRolloutStarted = true
						break
					}
				}
				g.Expect(voterRolloutStarted).To(BeTrue(), "expected voter rollout to start after the steady read pool converged")
			}, 20*time.Minute, 5*time.Second).Should(Succeed())

			By("Monitoring rolling invariants during upgrade")
			var (
				seenUpgradeInProgress  bool
				lastPartition          = upgradeCluster.Spec.Replicas
				lastCompletedPodCount  = 0
				lastUpgradeStartedAt   int64
				lastResourceVersionInt int64
				consecutiveReadFailure = 0
			)

			monitorDeadline := time.Now().Add(20 * time.Minute)
			for time.Now().Before(monitorDeadline) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				// Cache-backed e2e reads can occasionally surface older snapshots while watches catch up.
				// Ignore non-forward resourceVersions so monotonic checks evaluate ordered observations.
				if currentRV, err := strconv.ParseInt(updated.ResourceVersion, 10, 64); err == nil {
					if currentRV <= lastResourceVersionInt {
						time.Sleep(5 * time.Second)
						continue
					}
					lastResourceVersionInt = currentRV
				}

				if updated.Status.Upgrade == nil {
					if seenUpgradeInProgress {
						break
					}
					time.Sleep(5 * time.Second)
					continue
				}

				seenUpgradeInProgress = true
				progress := updated.Status.Upgrade
				if progress.StartedAt != nil {
					startedAtUnix := progress.StartedAt.UnixNano()
					if lastUpgradeStartedAt == 0 || startedAtUnix != lastUpgradeStartedAt {
						lastUpgradeStartedAt = startedAtUnix
						lastPartition = updated.Spec.Replicas
						lastCompletedPodCount = 0
					}
				}

				Expect(progress.TargetVersion).To(Equal(targetVersion))
				Expect(progress.FromVersion).To(Equal(initialVersion))
				if progress.Failure != nil {
					Expect(progress.Failure.Reason).To(BeEmpty(), "rolling upgrade entered failed state: %s", progress.Failure.Message)
				}

				Expect(progress.CurrentPartition).To(BeNumerically(">=", 0))
				Expect(progress.CurrentPartition).To(BeNumerically("<=", lastPartition), "partition should move monotonically downward")
				lastPartition = progress.CurrentPartition

				Expect(len(progress.CompletedPods)).To(BeNumerically(">=", lastCompletedPodCount), "completed pod list should not shrink")
				lastCompletedPodCount = len(progress.CompletedPods)

				seenOrdinals := make(map[int32]struct{}, len(progress.CompletedPods))
				for _, ordinal := range progress.CompletedPods {
					Expect(ordinal).To(BeNumerically(">=", 0))
					Expect(ordinal).To(BeNumerically("<", updated.Spec.Replicas))
					_, duplicate := seenOrdinals[ordinal]
					Expect(duplicate).To(BeFalse(), "completed pods should not contain duplicate ordinals")
					seenOrdinals[ordinal] = struct{}{}
				}

				sts := &appsv1.StatefulSet{}
				Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, sts)).To(Succeed())
				Expect(sts.Status.ReadyReplicas).To(BeNumerically(">=", updated.Spec.Replicas-1), "rolling upgrade should keep at least quorum available")

				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, upgradeCluster.Name)
				if err == nil {
					_, err = e2ehelpers.ReadSecretViaJWT(ctx, cfg, admin, tenantNamespace, initialImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, "foo")
				}
				if err != nil {
					consecutiveReadFailure++
				} else {
					consecutiveReadFailure = 0
				}
				Expect(consecutiveReadFailure).To(BeNumerically("<=", 2), "data plane was unavailable for too long during rolling upgrade")

				time.Sleep(10 * time.Second)
			}

			Expect(seenUpgradeInProgress).To(BeTrue(), "rolling upgrade never entered in-progress state")
			Expect(time.Now()).To(BeTemporally("<", monitorDeadline), "timed out waiting for rolling upgrade to complete")

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).To(BeNil(), "Status.Upgrade should be cleared when upgrade completes")
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseRunning))
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

				// Strict verification: Check that all pods are running the target image
				podList := &corev1.PodList{}
				g.Expect(admin.List(ctx, podList, client.InNamespace(tenantNamespace), client.MatchingLabels(map[string]string{
					constants.LabelOpenBaoCluster:   upgradeCluster.Name,
					constants.LabelOpenBaoComponent: constants.ComponentOpenBaoCluster,
				}))).To(Succeed())
				g.Expect(podList.Items).NotTo(BeEmpty())

				for _, pod := range podList.Items {
					e2ehelpers.ExpectOpenBaoPodVersion(g, pod, targetVersion)
				}

				readSts := &appsv1.StatefulSet{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      resourceidentity.ReadReplicaStatefulSetName(upgradeCluster),
					Namespace: tenantNamespace,
				}, readSts)).To(Succeed())
				g.Expect(readSts.Status.ReadyReplicas).To(Equal(int32(1)))
			}, 20*time.Minute, 10*time.Second).Should(Succeed())

			By("Verifying rolling step-down jobs are deterministic and successful")
			jobs := &batchv1.JobList{}
			Expect(admin.List(ctx, jobs,
				client.InNamespace(tenantNamespace),
				client.MatchingLabels{
					constants.LabelOpenBaoCluster:   upgradeCluster.Name,
					constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
				},
			)).To(Succeed())

			stepDownRunIDs := map[string]struct{}{}
			for i := range jobs.Items {
				job := &jobs.Items[i]
				if job.Annotations == nil {
					continue
				}
				if job.Annotations[upgradeActionAnnotationKey] != string(upgrade.ExecutorActionRollingStepDownLeader) {
					continue
				}
				Expect(validateManagedLifecycleJobOwnerProof(job, upgradeCluster)).To(Succeed())

				Expect(job.Status.Failed).To(Equal(int32(0)), "step-down job should not fail: %s", job.Name)

				runID := job.Annotations[upgradeRunIDAnnotationKey]
				Expect(runID).NotTo(BeEmpty(), "step-down job should carry run ID annotation")
				_, duplicate := stepDownRunIDs[runID]
				Expect(duplicate).To(BeFalse(), "step-down jobs should not duplicate run IDs")
				stepDownRunIDs[runID] = struct{}{}
			}
			Expect(len(stepDownRunIDs)).To(BeNumerically("<=", int(upgradeCluster.Spec.Replicas)))

			By("Verifying secret persists after upgrade")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				val, err := e2ehelpers.ReadSecretViaJWT(ctx, cfg, admin, tenantNamespace, targetImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, "foo")
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read post-upgrade secret")
				g.Expect(val).To(Equal("bar"))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

			By("Verifying upgrade metrics reflect idle state")
			metricsOutput, metricErr := framework.WaitForControllerMetricSubstrings(
				operatorNamespace,
				3*time.Minute,
				"openbao_upgrade_in_progress{",
				fmt.Sprintf(`namespace="%s"`, tenantNamespace),
				fmt.Sprintf(`name="%s"`, upgradeCluster.Name),
				"} 0",
			)
			Expect(metricErr).NotTo(HaveOccurred(), "Last metrics output:\n%s", metricsOutput)
		})
	})

	Context("Rolling Snapshot Recovery", Label("rolling", "snapshot", "recovery"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			recoveryCluster *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			targetVersion   string
			initialImage    string
			targetImage     string
			admin           client.Client
			cfg             *rest.Config
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-rolling-snapshot", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			initialVersion = envOrDefault("E2E_UPGRADE_FROM_VERSION", defaultUpgradeFromVersion)
			targetVersion = envOrDefault("E2E_UPGRADE_TO_VERSION", defaultUpgradeToVersion)
			initialImage = fmt.Sprintf("openbao/openbao:%s", initialVersion)
			targetImage = fmt.Sprintf("openbao/openbao:%s", targetVersion)

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Rolling snapshot recovery test skipped: versions identical (%s)", initialVersion))
			}

			storage := newUpgradeStorage(ctx, admin, cfg)

			credentialsSecret := &corev1.Secret{
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

			brokenBackupPolicyRequest := openbaov1alpha1.SelfInitRequest{
				Name:      "override-backup-policy-broken",
				Operation: openbaov1alpha1.SelfInitOperationUpdate,
				Path:      "sys/policies/acl/openbao-operator-backup",
				Policy: &openbaov1alpha1.SelfInitPolicy{
					Policy: `path "sys/health" {
  capabilities = ["read"]
}`,
				},
			}

			adminRequests := e2ehelpers.CreateJWTPolicyRoleRequests(
				tenantNamespace,
				"default",
				e2eAdminPolicyName,
				e2eAdminRoleName,
				`path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}`,
			)

			recoveryCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rolling-snapshot-cluster",
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
						Requests: append(append([]openbaov1alpha1.SelfInitRequest{}, adminRequests...), append([]openbaov1alpha1.SelfInitRequest{brokenBackupPolicyRequest}, e2ehelpers.CreateE2ERequests(tenantNamespace)...)...),
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
					Backup: &openbaov1alpha1.BackupSchedule{
						Schedule: "*/5 * * * *",
						Image:    backupExecutorImage,
						Target: openbaov1alpha1.BackupTarget{
							Provider:     constants.StorageProviderS3,
							Endpoint:     upgradeStorageEndpoint(storage.Name),
							Bucket:       rustfsBucket,
							PathPrefix:   "clusters",
							UsePathStyle: true,
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
					},
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Image:              upgradeExecutorImage,
						PreUpgradeSnapshot: true,
						Strategy:           openbaov1alpha1.UpdateStrategyRollingUpdate,
					},
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, recoveryCluster)).To(Succeed())

			snapshotNetworkPolicy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-snapshot-network-policy", recoveryCluster.Name),
					Namespace: tenantNamespace,
				},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							constants.LabelOpenBaoCluster: recoveryCluster.Name,
						},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      constants.LabelOpenBaoComponent,
								Operator: metav1.LabelSelectorOpIn,
								Values:   []string{"backup"},
							},
						},
					},
					PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
					Egress: []networkingv1.NetworkPolicyEgressRule{
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"kubernetes.io/metadata.name": "kube-system",
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolUDP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(53)
										return &p
									}(),
								},
							},
						},
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"kubernetes.io/metadata.name": storage.Name,
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolTCP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(9000)
										return &p
									}(),
								},
							},
						},
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											constants.LabelOpenBaoCluster: recoveryCluster.Name,
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolTCP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(8200)
										return &p
									}(),
								},
							},
						},
					},
				},
			}
			Expect(admin.Create(ctx, snapshotNetworkPolicy)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.Upgrade).To(BeNil())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			_, err = tenantFW.WaitForStatefulSetReady(
				ctx,
				recoveryCluster.Name,
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

		It("retries a failed rolling pre-upgrade snapshot before starting rollout", Label(
			"case:upgrade-strategies-retries-a-failed-rolling-pre-upgrade-fade7ad8",
		), func() {
			By("Triggering a rolling upgrade with pre-upgrade snapshots enabled")
			var upgradeGeneration int64
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = targetImage
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
				upgradeGeneration = original.Generation + 1
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			expectedJobName := rollingPreUpgradeBackupJobName(recoveryCluster.Name, upgradeGeneration)

			By("Waiting for the first pre-upgrade snapshot job to fail while rollout stays blocked")
			var failedJobUID types.UID
			Eventually(func(g Gomega) {
				job := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: expectedJobName, Namespace: tenantNamespace}, job)).To(Succeed())
				g.Expect(jobFailed(job)).To(BeTrue(), "expected first pre-upgrade backup job to fail")
				failedJobUID = job.UID

				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).To(BeNil(), "rolling upgrade must stay blocked until snapshot succeeds")
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Repairing the backup policy in OpenBao without changing the cluster generation")
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   recoveryCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, recoveryCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				_, err = e2ehelpers.RunCommandViaJWT(
					ctx,
					cfg,
					admin,
					tenantNamespace,
					initialImage,
					baoAddr,
					"default",
					e2eAdminRoleName,
					bypassLabels,
					policyWriteCommand("openbao-operator-backup", `path "sys/storage/raft/snapshot" { capabilities = ["read"] }`),
				)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the failed snapshot job is recycled and a fresh attempt succeeds")
			var successfulJobUID types.UID
			Eventually(func(g Gomega) {
				job := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: expectedJobName, Namespace: tenantNamespace}, job)).To(Succeed())
				g.Expect(job.UID).NotTo(Equal(failedJobUID), "snapshot retry should recreate the deterministic job after deleting the failed attempt")
				g.Expect(jobSucceeded(job)).To(BeTrue(), "recreated snapshot job should succeed")
				g.Expect(job.Labels).To(HaveKeyWithValue(constants.LabelOpenBaoBackupType, constants.BackupTypePreUpgrade))
				g.Expect(job.Annotations).NotTo(HaveKey("openbao.org/backup-key"))
				successfulJobUID = job.UID
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())
			Expect(successfulJobUID).NotTo(BeZero())

			By("Verifying the rolling upgrade proceeds only after the snapshot succeeds")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Upgrade).To(BeNil())
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseRunning))
				degraded := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionDegraded))
				if degraded != nil {
					g.Expect(degraded.Message).NotTo(ContainSubstring("without openbao.org/backup-key"),
						"scheduled backup manager must ignore successful pre-upgrade snapshot Jobs")
				}
			}, 30*time.Minute, 10*time.Second).Should(Succeed())
		})
	})

	Context("Rolling Failure Recovery", Label("rolling", "recovery"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			recoveryCluster *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			targetVersion   string
			initialImage    string
			targetImage     string
			brokenTarget    string
			admin           client.Client
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-rolling-retry", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			initialVersion = envOrDefault("E2E_UPGRADE_FROM_VERSION", defaultUpgradeFromVersion)
			targetVersion = envOrDefault("E2E_UPGRADE_TO_VERSION", defaultUpgradeToVersion)
			initialImage = fmt.Sprintf("openbao/openbao:%s", initialVersion)
			targetImage = fmt.Sprintf("openbao/openbao:%s", targetVersion)
			brokenTarget = "openbao/openbao:retry-image-does-not-exist"

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Rolling retry recovery test skipped: versions identical (%s)", initialVersion))
			}

			recoveryCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rolling-retry-cluster",
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, recoveryCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.Upgrade).To(BeNil())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			_, err = tenantFW.WaitForStatefulSetReady(
				ctx,
				recoveryCluster.Name,
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

		AfterEach(func() {
			if !CurrentSpecReport().Failed() || recoveryCluster == nil {
				return
			}
			By("Collecting rolling retry diagnostics")
			dumpRollingUpgradeDiagnostics(ctx, admin, tenantNamespace, recoveryCluster.Name)
		})

		It("recovers a failed rolling upgrade after a retry request clears stale state", Label(
			"case:upgrade-strategies-recovers-a-failed-rolling-upgrade-after-5a3c4b87",
			"e2e-anchor",
			"covers:lifecycle-job-owner-proof",
		), func() {
			var failedPartition int32

			By("Triggering a rolling upgrade with a bad non-semver image tag")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = brokenTarget
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Expect(tenantFW.TriggerReconcile(ctx, recoveryCluster.Name)).To(Succeed())

			By("Waiting for the rolling upgrade to initialize")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).NotTo(BeNil())
				g.Expect(updated.Status.Upgrade.TargetVersion).To(Equal(targetVersion))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for the first target to run the broken image before expiring its readiness deadline")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(recoveryCluster), updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).NotTo(BeNil())
				g.Expect(updated.Status.Upgrade.Failure).To(BeNil())
				g.Expect(updated.Status.Upgrade.CompletedPods).To(BeEmpty())
				g.Expect(updated.Status.Upgrade.CurrentPartition).To(Equal(updated.Spec.Replicas))
				pod := &corev1.Pod{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Namespace: tenantNamespace, Name: fmt.Sprintf("%s-%d", recoveryCluster.Name, updated.Spec.Replicas-1),
				}, pod)).To(Succeed())
				g.Expect(pod.DeletionTimestamp).To(BeNil())
				g.Expect(pod.Spec.Containers).NotTo(BeEmpty())
				g.Expect(pod.Spec.Containers[0].Image).To(Equal(brokenTarget))
				g.Expect(pod.Status.ContainerStatuses).NotTo(BeEmpty())
				g.Expect(pod.Status.ContainerStatuses[0].Ready).To(BeFalse())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Forcing the real timeout/retry path without waiting ten minutes")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).NotTo(BeNil())
				if updated.Status.Upgrade.Failure == nil || updated.Status.Upgrade.Failure.Reason != upgrade.ReasonPodNotReady {
					original := updated.DeepCopy()
					updated.Status.Upgrade.StartedAt = ptrTo(metav1.NewTime(time.Now().Add(-(upgrade.DefaultPodReadyTimeout + time.Minute))))
					g.Expect(admin.Status().Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
					g.Expect(tenantFW.TriggerReconcile(ctx, recoveryCluster.Name)).To(Succeed())
				}
				g.Expect(updated.Status.Upgrade.Failure).NotTo(BeNil())
				g.Expect(updated.Status.Upgrade.Failure.Reason).To(Equal(upgrade.ReasonPodNotReady))
				g.Expect(updated.Status.Upgrade.CurrentPartition).To(BeNumerically(">", 0))
				failedPartition = updated.Status.Upgrade.CurrentPartition
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Injecting a stale deterministic step-down job for the retry cleanup path")
			targetPod := fmt.Sprintf("%s-%d", recoveryCluster.Name, failedPartition-1)
			staleJob := newStaleRollingStepDownJob(recoveryCluster, targetPod, initialImage)
			Expect(validateManagedLifecycleJobOwnerProof(staleJob, recoveryCluster)).To(Succeed())
			cfg, err := ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())
			controllerUser := fmt.Sprintf(
				"system:serviceaccount:%s:openbao-operator-controller",
				operatorNamespace,
			)
			controllerGroups := []string{
				"system:serviceaccounts",
				fmt.Sprintf("system:serviceaccounts:%s", operatorNamespace),
				"system:authenticated",
			}
			Expect(e2ehelpers.RunWithImpersonation(
				ctx,
				cfg,
				admin.Scheme(),
				controllerUser,
				controllerGroups,
				func(controllerClient client.Client) error {
					return controllerClient.Create(ctx, staleJob)
				},
			)).To(Succeed())
			staleJobUID := staleJob.UID

			By("Restoring the target image and requesting a rolling retry")
			retryToken := time.Now().UTC().Format(time.RFC3339Nano)
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Image = targetImage
				if updated.Spec.Upgrade == nil {
					updated.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{}
				}
				if updated.Spec.Upgrade.Requests == nil {
					updated.Spec.Upgrade.Requests = &openbaov1alpha1.UpgradeRequestConfig{}
				}
				updated.Spec.Upgrade.Requests.Retry = retryToken
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Expect(tenantFW.TriggerReconcile(ctx, recoveryCluster.Name)).To(Succeed())

			By("Verifying retry preparation clears failed status, records the handled request, and removes the stale step-down job")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Upgrade).NotTo(BeNil())
				g.Expect(updated.Status.Upgrade.Failure).To(BeNil())
				g.Expect(updated.Status.UpgradeRequests).NotTo(BeNil())
				g.Expect(updated.Status.UpgradeRequests.LastHandledRetry).To(Equal(retryToken))

				job := &batchv1.Job{}
				err := admin.Get(ctx, types.NamespacedName{Name: staleJob.Name, Namespace: tenantNamespace}, job)
				if err == nil {
					g.Expect(job.UID).NotTo(Equal(staleJobUID), "stale step-down job should be deleted before any replacement job is observed")
					return
				}
				g.Expect(client.IgnoreNotFound(err)).To(Succeed())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the rolling upgrade resumes and completes successfully")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: recoveryCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.Upgrade).To(BeNil())
				g.Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseRunning))
			}, 30*time.Minute, 10*time.Second).Should(Succeed())
		})
	})

	// --- Blue/Green Upgrade ---
	Context("Blue/Green Upgrade", Label("bluegreen"), Ordered, func() {
		var (
			tenantNamespace   string
			tenantFW          *framework.Framework
			upgradeCluster    *openbaov1alpha1.OpenBaoCluster
			initialVersion    string
			targetVersion     string
			admin             client.Client
			cfg               *rest.Config
			credentialsSecret *corev1.Secret
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-bluegreen", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			initialVersion = blueGreenUpgradeFromVersion()
			targetVersion = blueGreenUpgradeToVersion()

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Upgrade test skipped: versions identical (%s)", initialVersion))
			}

			storage := newUpgradeStorage(ctx, admin, cfg)

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

			// Augment the generated SelfInit requests with E2E test role/policy
			// Use e2eRequests directly; BootstrapJWTAuth handles the standard upgrade requests
			allRequests := e2ehelpers.CreateE2ERequests(tenantNamespace)

			upgradeCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bluegreen-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    fmt.Sprintf("openbao/openbao:%s", initialVersion),
					Replicas: 3,
					Service: &openbaov1alpha1.ServiceConfig{
						Type: corev1.ServiceTypeClusterIP,
					},
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
						Image:    upgradeExecutorImage,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote:        true,
							PreUpgradeSnapshot: true, // Enable pre-upgrade snapshot
							Verification: &openbaov1alpha1.VerificationConfig{
								MinSyncDuration: "30s",
							},
						},
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled:  true,
						Requests: allRequests,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
					},
					Backup: &openbaov1alpha1.BackupSchedule{
						Schedule: "0 0 * * *",
						Image:    backupExecutorImage,
						// JWTAuthRole not set - operator will auto-create backup role when OIDC is enabled
						Target: openbaov1alpha1.BackupTarget{
							Endpoint:     upgradeStorageEndpoint(storage.Name),
							Bucket:       rustfsBucket,
							Region:       "us-east-1",
							UsePathStyle: true,
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: credentialsSecret.Name,
							},
						},
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, upgradeCluster)).To(Succeed())

			// Create NetworkPolicy for upgrade snapshot jobs to access RustFS
			snapshotNetworkPolicy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-upgrade-snapshot-network-policy", upgradeCluster.Name),
					Namespace: tenantNamespace,
				},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							constants.LabelOpenBaoComponent: bluegreen.ComponentUpgradeSnapshot,
							constants.LabelOpenBaoCluster:   upgradeCluster.Name,
						},
					},
					PolicyTypes: []networkingv1.PolicyType{
						networkingv1.PolicyTypeEgress,
					},
					Egress: []networkingv1.NetworkPolicyEgressRule{
						// Allow DNS
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"kubernetes.io/metadata.name": "kube-system",
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolUDP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(53)
										return &p
									}(),
								},
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolTCP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(53)
										return &p
									}(),
								},
							},
						},
						// Allow access to RustFS
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"kubernetes.io/metadata.name": storage.Name,
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolTCP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(9000)
										return &p
									}(),
								},
							},
						},
						// Allow access to OpenBao cluster for snapshot API
						{
							To: []networkingv1.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											constants.LabelOpenBaoCluster: upgradeCluster.Name,
										},
									},
								},
							},
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Protocol: func() *corev1.Protocol {
										p := corev1.ProtocolTCP
										return &p
									}(),
									Port: func() *intstr.IntOrString {
										p := intstr.FromInt(8200)
										return &p
									}(),
								},
							},
						},
					},
				},
			}
			Expect(admin.Create(ctx, snapshotNetworkPolicy)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				stsList := &appsv1.StatefulSetList{}
				g.Expect(admin.List(ctx, stsList,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster: upgradeCluster.Name,
					},
				)).To(Succeed())
				g.Expect(stsList.Items).NotTo(BeEmpty(), "expected at least one StatefulSet for blue/green cluster")

				var totalReady int32
				for _, sts := range stsList.Items {
					totalReady += sts.Status.ReadyReplicas
				}
				g.Expect(totalReady).To(Equal(upgradeCluster.Spec.Replicas),
					"expected total ready replicas across StatefulSets to match desired cluster replicas")
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		It("executes Blue/Green upgrade cycle with pre-upgrade snapshot", Label(
			"e2e-anchor",
			"case:upgrade-bluegreen-snapshot-promotion",
		), func() {
			By("Writing a secret before upgrade")
			secretPath := "secret/bluegreen-upgrade-test"
			secretData := map[string]string{"foo": "bar", "version": "v1"}
			serviceName := fmt.Sprintf("%s-public", upgradeCluster.Name)
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   upgradeCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}

			// Enable KV engine (idempotent)
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, secretData)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed(), "Failed to write pre-upgrade secret")

			By("Ensuring the external Service exists for zero-downtime probing")
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: tenantNamespace,
				}, svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Continuously probing Service availability during the full Blue/Green upgrade")
			probeCtx, stopProbe := context.WithCancel(ctx)
			defer stopProbe()

			probeDone := make(chan struct{})
			var probeMu sync.Mutex
			probeStats := serviceAvailabilityStats{}

			probeOnce := func() {
				probeAttemptCtx, cancel := context.WithTimeout(probeCtx, 20*time.Second)
				defer cancel()

				statusCode, err := probeOpenBaoServiceHealthViaAPIProxy(probeAttemptCtx, cfg, tenantNamespace, serviceName)

				probeMu.Lock()
				defer probeMu.Unlock()

				probeStats.Samples++
				if err != nil {
					probeStats.Failures++
					probeStats.ConsecutiveFailures++
					if probeStats.ConsecutiveFailures > probeStats.MaxConsecutiveFailures {
						probeStats.MaxConsecutiveFailures = probeStats.ConsecutiveFailures
					}
					probeStats.LastFailure = err.Error()
					_, _ = fmt.Fprintf(GinkgoWriter, "service availability probe failed: %v\n", err)
					return
				}

				if !isOpenBaoServiceAvailableStatus(statusCode) {
					probeStats.Failures++
					probeStats.ConsecutiveFailures++
					if probeStats.ConsecutiveFailures > probeStats.MaxConsecutiveFailures {
						probeStats.MaxConsecutiveFailures = probeStats.ConsecutiveFailures
					}
					probeStats.LastFailure = fmt.Sprintf("unexpected status code: %d", statusCode)
					_, _ = fmt.Fprintf(GinkgoWriter, "service availability probe returned unexpected status: %d\n", statusCode)
					return
				}

				probeStats.ConsecutiveFailures = 0
			}

			go func() {
				defer close(probeDone)
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()

				probeOnce()
				for {
					select {
					case <-probeCtx.Done():
						return
					case <-ticker.C:
						probeOnce()
					}
				}
			}()
			defer func() {
				stopProbe()
				<-probeDone
			}()

			By("Triggering upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying pre-upgrade snapshot job is created")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.PreUpgradeSnapshotJobName).NotTo(BeEmpty(), "pre-upgrade snapshot job should be created")

				// Verify the snapshot job exists
				job := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      updated.Status.BlueGreen.PreUpgradeSnapshotJobName,
					Namespace: tenantNamespace,
				}, job)).To(Succeed())

				// Verify job has correct labels
				g.Expect(job.Labels).To(HaveKeyWithValue(constants.LabelOpenBaoComponent, bluegreen.ComponentUpgradeSnapshot))
				g.Expect(job.Labels).To(HaveKeyWithValue(constants.LabelOpenBaoCluster, upgradeCluster.Name))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for pre-upgrade snapshot job to complete")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())

				job := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      updated.Status.BlueGreen.PreUpgradeSnapshotJobName,
					Namespace: tenantNamespace,
				}, job)).To(Succeed())

				g.Expect(job.Status.Succeeded).To(BeNumerically(">", 0), "pre-upgrade snapshot job should succeed")
			}, 15*time.Minute, 30*time.Second).Should(Succeed())

			By("Waiting for upgrade to complete")
			phaseRank := map[openbaov1alpha1.BlueGreenPhase]int{
				openbaov1alpha1.PhaseIdle:           0,
				openbaov1alpha1.PhaseDeployingGreen: 1,
				openbaov1alpha1.PhaseJoiningMesh:    2,
				openbaov1alpha1.PhaseSyncing:        3,
				openbaov1alpha1.PhasePromoting:      4,
				openbaov1alpha1.PhaseDemotingBlue:   5,
				openbaov1alpha1.PhaseCleanup:        6,
			}
			highestObservedPhase := openbaov1alpha1.PhaseIdle
			highestObservedRank := phaseRank[highestObservedPhase]

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				currentPhase := updated.Status.BlueGreen.Phase
				if currentPhase == openbaov1alpha1.PhaseIdle && updated.Status.CurrentVersion == targetVersion {
					return
				}
				currentRank, knownPhase := phaseRank[currentPhase]
				g.Expect(knownPhase).To(BeTrue(), "phase should be known for monotonic ordering")
				g.Expect(currentRank).To(BeNumerically(">=", highestObservedRank),
					"phase regressed from %s to %s", highestObservedPhase, currentPhase)
				if currentRank > highestObservedRank {
					highestObservedRank = currentRank
					highestObservedPhase = currentPhase
				}
				// Verify intermediate states
				// Verify Blue pods are on initial version and Green pods (if any) are on target version
				blueRevision := updated.Status.BlueGreen.BlueRevision
				greenRevision := updated.Status.BlueGreen.GreenRevision

				if blueRevision != "" {
					bluePods := &corev1.PodList{}
					g.Expect(admin.List(ctx, bluePods,
						client.InNamespace(tenantNamespace),
						client.MatchingLabels{constants.LabelOpenBaoRevision: blueRevision},
					)).To(Succeed())
					for _, pod := range bluePods.Items {
						e2ehelpers.ExpectOpenBaoPodVersion(g, pod, initialVersion)
					}
				}

				if greenRevision != "" {
					greenPods := &corev1.PodList{}
					g.Expect(admin.List(ctx, greenPods,
						client.InNamespace(tenantNamespace),
						client.MatchingLabels{constants.LabelOpenBaoRevision: greenRevision},
					)).To(Succeed())
					for _, pod := range greenPods.Items {
						e2ehelpers.ExpectOpenBaoPodVersion(g, pod, targetVersion)
					}
				}

				g.Expect(currentPhase).To(BeElementOf(
					openbaov1alpha1.PhaseIdle,
					openbaov1alpha1.PhaseDeployingGreen,
					openbaov1alpha1.PhaseJoiningMesh,
					openbaov1alpha1.PhaseSyncing,
					openbaov1alpha1.PhasePromoting,
					openbaov1alpha1.PhaseDemotingBlue,
					openbaov1alpha1.PhaseCleanup,
				))
			}, 30*time.Minute, 30*time.Second).Should(Succeed())

			By("Verifying upgrade completed successfully with snapshots")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: upgradeCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.PreUpgradeSnapshotJobName).NotTo(BeEmpty(), "snapshot job name should be preserved")
			}, 30*time.Minute, 30*time.Second).Should(Succeed())

			stopProbe()
			<-probeDone
			probeMu.Lock()
			finalProbeStats := probeStats
			probeMu.Unlock()

			By("Verifying Service availability remained continuous during the upgrade")
			Expect(finalProbeStats.Samples).To(BeNumerically(">", 0), "expected at least one service availability sample")
			Expect(finalProbeStats.MaxConsecutiveFailures).To(BeNumerically("<=", 1),
				"service had prolonged unavailability during Blue/Green upgrade (samples=%d failures=%d maxConsecutive=%d lastFailure=%q)",
				finalProbeStats.Samples, finalProbeStats.Failures, finalProbeStats.MaxConsecutiveFailures, finalProbeStats.LastFailure)

			By("Verifying critical blue/green executor actions completed successfully")
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      upgradeCluster.Name,
						constants.LabelOpenBaoCluster:   upgradeCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())

				g.Expect(hasSucceededUpgradeAction(jobs.Items, bluegreen.ActionJoinGreenNonVoters)).To(BeTrue(), "join-green-non-voters action should succeed")
				g.Expect(hasSucceededUpgradeAction(jobs.Items, bluegreen.ActionWaitGreenSynced)).To(BeTrue(), "wait-green-synced action should succeed")
				g.Expect(hasSucceededUpgradeAction(jobs.Items, bluegreen.ActionPromoteGreenVoters)).To(BeTrue(), "promote-green-voters action should succeed")
				g.Expect(hasSucceededUpgradeAction(jobs.Items, bluegreen.ActionDemoteBlueNonVotersStepDown)).To(BeTrue(), "demote-blue-non-voters-stepdown action should succeed")
				g.Expect(hasSucceededUpgradeAction(jobs.Items, bluegreen.ActionRemoveBluePeers)).To(BeTrue(), "remove-blue-peers action should succeed")
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying secret persists after upgrade")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, upgradeCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				val, err := e2ehelpers.ReadSecretViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, "foo")
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read post-upgrade secret")
				g.Expect(val).To(Equal("bar"))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())
		})
	})

	Context("Blue/Green Syncing Gates", Label("bluegreen", "verification"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			gatedCluster    *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			targetVersion   string
			admin           client.Client
			cfg             *rest.Config
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-bluegreen-gates", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client
			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			initialVersion = blueGreenUpgradeFromVersion()
			targetVersion = blueGreenUpgradeToVersion()
			if initialVersion == targetVersion {
				Skip("Blue/green syncing gates test skipped")
			}

			gatedCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bluegreen-gates-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    fmt.Sprintf("openbao/openbao:%s", initialVersion),
					Replicas: 3,
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
						Image:    upgradeExecutorImage,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote: false,
							Verification: &openbaov1alpha1.VerificationConfig{
								MinSyncDuration: "10s",
								PrePromotionHook: &openbaov1alpha1.ValidationHookConfig{
									Image:   fmt.Sprintf("openbao/openbao:%s", initialVersion),
									Command: []string{"/bin/sh", "-ec"},
									Args:    []string{"echo pre-promotion-hook"},
								},
							},
						},
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
						Requests: append(e2ehelpers.CreateE2ERequests(tenantNamespace),
							e2ehelpers.CreateJWTPolicyRoleRequests(tenantNamespace, "default", "raft-reader", "raft-reader",
								`path "sys/storage/raft/configuration" { capabilities = ["read"] }`)...),
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, gatedCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: gatedCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterEach(func() {
			if CurrentSpecReport().Failed() && gatedCluster != nil {
				dumpBlueGreenUpgradeDiagnostics(tenantNamespace, gatedCluster.Name)
			}
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		It("holds in Syncing and creates fresh executor Jobs after rollback before manual promotion", Label(
			"case:upgrade-bluegreen-retry-identity", "covers:bluegreen-operation-identity", "covers:bluegreen-replica-drift",
		), func() {
			By("Triggering a blue/green upgrade with manual promotion")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: gatedCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for Syncing hold with the hook completed successfully")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: gatedCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseSyncing))
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
				g.Expect(updated.Status.BlueGreen.ValidationHook).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.ValidationHook.JobName).NotTo(BeEmpty())

				waitSyncedJobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, waitSyncedJobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      gatedCluster.Name,
						constants.LabelOpenBaoCluster:   gatedCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())
				waitSyncedJob := findUpgradeExecutorJob(waitSyncedJobs.Items, bluegreen.ActionWaitGreenSynced, "")
				g.Expect(waitSyncedJob).NotTo(BeNil())
				g.Expect(jobSucceeded(waitSyncedJob)).To(BeTrue(), "wait-green-synced should finish before the manual hold")

				hookJob := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      updated.Status.BlueGreen.ValidationHook.JobName,
					Namespace: tenantNamespace,
				}, hookJob)).To(Succeed())
				g.Expect(jobSucceeded(hookJob)).To(BeTrue(), "pre-promotion hook should succeed before promotion is approved")
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Consistently holding in Syncing while no promote request is set")
			Consistently(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: gatedCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseSyncing))
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
			}, time.Minute, 10*time.Second).Should(Succeed())

			By("Recording completed executor Jobs before rollback")
			beforeRollback := &openbaov1alpha1.OpenBaoCluster{}
			Expect(admin.Get(ctx, client.ObjectKeyFromObject(gatedCluster), beforeRollback)).To(Succeed())
			Expect(beforeRollback.Status.BlueGreen.BlueReplicas).To(Equal(gatedCluster.Spec.Replicas),
				"upgrade must capture the configured Blue population after bootstrap")
			Expect(beforeRollback.Status.BlueGreen.GreenReplicas).To(Equal(gatedCluster.Spec.Replicas))
			firstOperationID := beforeRollback.Status.BlueGreen.OperationID
			blueRevision := beforeRollback.Status.BlueGreen.BlueRevision
			greenRevision := beforeRollback.Status.BlueGreen.GreenRevision
			oldJobs := make(map[bluegreen.ExecutorAction]string)
			for _, action := range []bluegreen.ExecutorAction{bluegreen.ActionJoinGreenNonVoters, bluegreen.ActionWaitGreenSynced} {
				jobName := upgrade.ExecutorJobName(gatedCluster.Name, action, firstOperationID+"/", blueRevision, greenRevision)
				job := &batchv1.Job{}
				Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: jobName}, job)).To(Succeed())
				Expect(jobSucceeded(job)).To(BeTrue())
				oldJobs[action] = jobName
			}

			By("Recording Blue and Green data claims before rollback")
			blueStatefulSetName := gatedCluster.Name
			if blueRevision != "" {
				blueStatefulSetName += "-" + blueRevision
			}
			blueClaims := make(map[string]types.UID)
			greenClaims := make(map[string]types.UID)
			for ordinal := int32(0); ordinal < gatedCluster.Spec.Replicas; ordinal++ {
				for name, claims := range map[string]map[string]types.UID{
					fmt.Sprintf("data-%s-%d", blueStatefulSetName, ordinal):                 blueClaims,
					fmt.Sprintf("data-%s-%s-%d", gatedCluster.Name, greenRevision, ordinal): greenClaims,
				} {
					claim := &corev1.PersistentVolumeClaim{}
					Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
					Expect(claim.Status.Phase).To(Equal(corev1.ClaimBound))
					claims[name] = claim.UID
				}
			}

			By("Requesting rollback while increasing the desired replica count")
			rollbackToken := time.Now().UTC().Format(time.RFC3339Nano)
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(gatedCluster), updated)).To(Succeed())
				original := updated.DeepCopy()
				if updated.Spec.Upgrade.Requests == nil {
					updated.Spec.Upgrade.Requests = &openbaov1alpha1.UpgradeRequestConfig{}
				}
				updated.Spec.Upgrade.Requests.Rollback = rollbackToken
				updated.Spec.Replicas = 7
				updated.Spec.Version = initialVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", initialVersion)
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for rollback to finish before requesting another upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(gatedCluster), updated)).To(Succeed())
				g.Expect(updated.Status.UpgradeRequests).NotTo(BeNil())
				g.Expect(updated.Status.UpgradeRequests.LastHandledRollback).To(Equal(rollbackToken))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.OperationID).To(BeEmpty())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying rollback used the captured three-node populations")
			repairName := upgrade.ExecutorJobName(gatedCluster.Name, bluegreen.ActionRepairConsensus, firstOperationID+"/rollback", blueRevision, greenRevision)
			repairJob := &batchv1.Job{}
			Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: repairName}, repairJob)).To(Succeed())
			repairEnv := make(map[string]string)
			for _, variable := range repairJob.Spec.Template.Spec.Containers[0].Env {
				repairEnv[variable.Name] = variable.Value
			}
			Expect(repairEnv[constants.EnvClusterReplicas]).To(Equal("3"))
			Expect(repairEnv[constants.EnvUpgradeBlueReplicas]).To(Equal("3"))

			By("Verifying rollback retires Green data claims and preserves Blue data claims")
			for name := range greenClaims {
				claim := &corev1.PersistentVolumeClaim{}
				err := admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "discarded Green claim %s must be gone before rollback completes: %v", name, err)
			}
			for name, uid := range blueClaims {
				claim := &corev1.PersistentVolumeClaim{}
				Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
				Expect(claim.UID).To(Equal(uid))
				Expect(claim.DeletionTimestamp).To(BeNil())
			}

			By("Requesting the same target again while completed Jobs remain")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(gatedCluster), updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Replicas = 3
				updated.Spec.Version = targetVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the new attempt completes fresh join and sync Jobs while old Jobs remain")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, client.ObjectKeyFromObject(gatedCluster), updated)).To(Succeed())
				g.Expect(updated.Status.UpgradeRequests).NotTo(BeNil())
				g.Expect(updated.Status.UpgradeRequests.LastHandledRollback).To(Equal(rollbackToken))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				status := updated.Status.BlueGreen
				g.Expect(status.OperationID).NotTo(BeEmpty())
				g.Expect(status.OperationID).NotTo(Equal(firstOperationID))
				g.Expect(status.Phase).To(Equal(openbaov1alpha1.PhaseSyncing))
				g.Expect(status.BlueRevision).To(Equal(blueRevision))
				g.Expect(status.GreenRevision).To(Equal(greenRevision))
				g.Expect(status.ValidationHook).NotTo(BeNil())
				g.Expect(status.ValidationHook.Stage).To(Equal(openbaov1alpha1.BlueGreenValidationHookStageTerminalObserved))
				for action, oldName := range oldJobs {
					oldJob := &batchv1.Job{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: oldName}, oldJob)).To(Succeed())
					g.Expect(jobSucceeded(oldJob)).To(BeTrue())
					newName := upgrade.ExecutorJobName(gatedCluster.Name, action, status.OperationID+"/", blueRevision, greenRevision)
					g.Expect(newName).NotTo(Equal(oldName))
					newJob := &batchv1.Job{}
					g.Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: newName}, newJob)).To(Succeed())
					g.Expect(jobSucceeded(newJob)).To(BeTrue())
				}
			}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the retry uses fresh Green data claims while Blue claims remain unchanged")
			for name, uid := range greenClaims {
				claim := &corev1.PersistentVolumeClaim{}
				Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
				Expect(claim.UID).NotTo(Equal(uid), "retry must not reuse removed Green peer data")
				Expect(claim.Status.Phase).To(Equal(corev1.ClaimBound))
			}
			for name, uid := range blueClaims {
				claim := &corev1.PersistentVolumeClaim{}
				Expect(admin.Get(ctx, types.NamespacedName{Namespace: tenantNamespace, Name: name}, claim)).To(Succeed())
				Expect(claim.UID).To(Equal(uid))
				Expect(claim.DeletionTimestamp).To(BeNil())
			}

			By("Verifying native Raft membership includes all new Green non-voters before promotion")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, gatedCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				output, err := e2ehelpers.RunCommandViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage,
					baoAddr, "default", "raft-reader", map[string]string{
						constants.LabelOpenBaoCluster:   gatedCluster.Name,
						constants.LabelOpenBaoComponent: "backup",
					}, "bao read -format=json sys/storage/raft/configuration")
				g.Expect(err).NotTo(HaveOccurred())
				var response struct {
					Data portopenbao.RaftConfigurationResponse `json:"data"`
				}
				g.Expect(json.Unmarshal([]byte(output), &response)).To(Succeed())
				g.Expect(response.Data.Config.Servers).To(HaveLen(6))
				for ordinal := int32(0); ordinal < gatedCluster.Spec.Replicas; ordinal++ {
					address := fmt.Sprintf("%s-%s-%d.%s.%s.svc:8201", gatedCluster.Name, greenRevision,
						ordinal, gatedCluster.Name, tenantNamespace)
					found := false
					for _, peer := range response.Data.Config.Servers {
						if peer.Address == address {
							g.Expect(peer.Voter).To(BeFalse())
							found = true
						}
					}
					g.Expect(found).To(BeTrue(), "Green peer %s must be present in native Raft membership", address)
				}
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

			promoteToken := time.Now().UTC().Format(time.RFC3339Nano)

			By("Approving promotion via spec.upgrade.requests.promote")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: gatedCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				if updated.Spec.Upgrade == nil {
					updated.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{}
				}
				if updated.Spec.Upgrade.Requests == nil {
					updated.Spec.Upgrade.Requests = &openbaov1alpha1.UpgradeRequestConfig{}
				}
				updated.Spec.Upgrade.Requests.Promote = promoteToken
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the upgrade resumes and completes cleanly")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: gatedCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.CurrentVersion).To(Equal(targetVersion))
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())
				g.Expect(updated.Status.UpgradeRequests).NotTo(BeNil())
				g.Expect(updated.Status.UpgradeRequests.LastHandledPromote).To(Equal(promoteToken))
			}, 30*time.Minute, 10*time.Second).Should(Succeed())
		})
	})

	Context("Blue/Green Validation Hook Failure", Label("bluegreen", "verification", "failure"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			hookFailCluster *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			targetVersion   string
			initialImage    string
			targetImage     string
			admin           client.Client
			cfg             *rest.Config
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-bluegreen-hook-failure", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			initialVersion = blueGreenUpgradeFromVersion()
			targetVersion = blueGreenUpgradeToVersion()
			initialImage = fmt.Sprintf("openbao/openbao:%s", initialVersion)
			targetImage = fmt.Sprintf("openbao/openbao:%s", targetVersion)
			if initialVersion == targetVersion {
				Skip("Blue/green validation hook failure test skipped")
			}

			hookFailCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bluegreen-hook-failure-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    initialImage,
					Replicas: 3,
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
						Image:    upgradeExecutorImage,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote: true,
							AutoRollback: &openbaov1alpha1.AutoRollbackConfig{
								Enabled:             true,
								OnJobFailure:        true,
								OnValidationFailure: true,
							},
							Verification: &openbaov1alpha1.VerificationConfig{
								MinSyncDuration: "10s",
								PrePromotionHook: &openbaov1alpha1.ValidationHookConfig{
									Image:   initialImage,
									Command: []string{"/bin/sh", "-ec"},
									Args:    []string{"echo pre-promotion-hook-failed >&2; exit 1"},
								},
							},
						},
					},
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, hookFailCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: hookFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterEach(func() {
			if CurrentSpecReport().Failed() && hookFailCluster != nil {
				dumpBlueGreenUpgradeDiagnostics(tenantNamespace, hookFailCluster.Name)
			}
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		It("aborts before promotion when the pre-promotion hook fails", Label(
			"case:upgrade-strategies-aborts-before-promotion-when-the-pre-230729f6",
		), func() {
			By("Writing a secret before the failed validation hook upgrade")
			secretPath := "secret/bluegreen-hook-failure-test"
			secretData := map[string]string{"foo": "bar", "version": initialVersion}
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   hookFailCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, hookFailCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(ctx, cfg, admin, tenantNamespace, initialImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, secretData)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())

			By("Triggering a blue/green upgrade guarded by a failing pre-promotion hook")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: hookFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = targetImage
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for the pre-promotion hook to fail after green finishes syncing")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: hookFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.ValidationHook).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.ValidationHook.JobName).NotTo(BeEmpty())

				waitSyncedJobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, waitSyncedJobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      hookFailCluster.Name,
						constants.LabelOpenBaoCluster:   hookFailCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())
				waitSyncedJob := findUpgradeExecutorJob(waitSyncedJobs.Items, bluegreen.ActionWaitGreenSynced, "")
				g.Expect(waitSyncedJob).NotTo(BeNil(), "wait-green-synced job should exist before validation hook is evaluated")
				g.Expect(jobSucceeded(waitSyncedJob)).To(BeTrue(), "wait-green-synced should complete before the validation hook failure path runs")

				hookJob := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{
					Name:      updated.Status.BlueGreen.ValidationHook.JobName,
					Namespace: tenantNamespace,
				}, hookJob)).To(Succeed())
				g.Expect(jobFailed(hookJob)).To(BeTrue(), "pre-promotion hook should fail")
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the upgrade aborts safely before promotion")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: hookFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())
				g.Expect(updated.Status.BlueGreen.RollbackStartTime).To(BeNil(), "failed pre-promotion hook should abort before cutover, not trigger rollback")
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BreakGlass).To(BeNil())
			}, 30*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying promotion never starts after the validation hook fails")
			Consistently(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      hookFailCluster.Name,
						constants.LabelOpenBaoCluster:   hookFailCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())
				g.Expect(findUpgradeExecutorJob(jobs.Items, bluegreen.ActionPromoteGreenVoters, "")).To(BeNil(), "promotion job should never be created when the validation hook fails")
			}, time.Minute, 10*time.Second).Should(Succeed())

			By("Verifying the original cluster remains readable after the failed upgrade")
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, hookFailCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				val, err := e2ehelpers.ReadSecretViaJWT(ctx, cfg, admin, tenantNamespace, initialImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, "foo")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(val).To(Equal("bar"))
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed())
		})
	})

	// --- Failure Scenarios ---
	Context("Failure Scenarios", Label("failure", "bluegreen"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			failureCluster  *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			targetVersion   string
			admin           client.Client
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-failure", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			initialVersion = blueGreenUpgradeFromVersion()
			targetVersion = blueGreenUpgradeToVersion()
			if initialVersion == targetVersion {
				Skip("Failure test skipped")
			}

			// Deliberately remove upgrade capabilities so executor jobs fail and retry/abort logic is exercised.
			brokenPolicyRequest := openbaov1alpha1.SelfInitRequest{
				Name:      "override-upgrade-policy-broken",
				Operation: openbaov1alpha1.SelfInitOperationUpdate,
				Path:      "sys/policies/acl/openbao-operator-upgrade",
				Policy: &openbaov1alpha1.SelfInitPolicy{
					Policy: `path "sys/health" {
  capabilities = ["read"]
}`,
				},
			}

			maxFailures := int32(2)
			failureCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failure-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    fmt.Sprintf("openbao/openbao:%s", initialVersion),
					Replicas: 3,
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
						Image:    upgradeExecutorImage,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote:    true,
							MaxJobFailures: &maxFailures,
							AutoRollback: &openbaov1alpha1.AutoRollbackConfig{
								Enabled:      true,
								OnJobFailure: true,
							},
						},
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
						Requests: append([]openbaov1alpha1.SelfInitRequest{brokenPolicyRequest}, e2ehelpers.CreateE2ERequests(tenantNamespace)...),
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, failureCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: failureCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		It("induces executor failure and validates retry plus auto-abort behavior", Label(
			"e2e-anchor",
			"case:upgrade-bluegreen-executor-failure-auto-abort",
		), func() {
			var failedEarlyAction bluegreen.ExecutorAction

			By("Triggering a Blue/Green upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: failureCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for upgrade to enter an early execution phase")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: failureCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(BeElementOf(
					openbaov1alpha1.PhaseDeployingGreen,
					openbaov1alpha1.PhaseJoiningMesh,
					openbaov1alpha1.PhaseSyncing,
					openbaov1alpha1.PhaseIdle,
				))
			}, 15*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying an initial early-phase executor job fails due to induced policy restriction")
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      failureCluster.Name,
						constants.LabelOpenBaoCluster:   failureCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())

				candidates := []bluegreen.ExecutorAction{
					bluegreen.ActionJoinGreenNonVoters,
					bluegreen.ActionWaitGreenSynced,
				}

				var observed *batchv1.Job
				for _, action := range candidates {
					job := findUpgradeExecutorJob(jobs.Items, action, "")
					if job == nil || !jobFailed(job) {
						continue
					}
					failedEarlyAction = action
					observed = job
					break
				}

				g.Expect(observed).NotTo(BeNil(), "expected initial failed executor job for one of actions: %v", candidates)
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying retry job for that action is created and also fails")
			Eventually(func(g Gomega) {
				g.Expect(failedEarlyAction).NotTo(BeEmpty(), "failed early-phase action should be discovered before retry assertion")

				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      failureCluster.Name,
						constants.LabelOpenBaoCluster:   failureCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())

				retryJob := findUpgradeExecutorJob(jobs.Items, failedEarlyAction, "retry-1")
				g.Expect(retryJob).NotTo(BeNil(), "retry executor job should exist for action %s", failedEarlyAction)
				g.Expect(jobFailed(retryJob)).To(BeTrue(), "retry executor job should fail for action %s", failedEarlyAction)
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying threshold triggers early-phase abort and cluster returns to idle on original version")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: failureCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle), "upgrade should return to idle after abort")
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty(), "green revision should be cleaned up on abort")
				g.Expect(updated.Status.BlueGreen.RollbackStartTime).To(BeNil(), "early-phase failure should abort, not trigger rollback")
				g.Expect(updated.Status.BlueGreen.JobFailureCount).To(Equal(int32(0)))
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion), "current version should remain at initial version after abort")
			}, 30*time.Minute, framework.DefaultPollInterval).Should(Succeed())
		})
	})

	Context("Late-Phase Rollback Scenarios", Label("failure", "bluegreen", "rollback"), Ordered, func() {
		var (
			tenantNamespace     string
			tenantFW            *framework.Framework
			rollbackFailCluster *openbaov1alpha1.OpenBaoCluster
			initialVersion      string
			targetVersion       string
			admin               client.Client
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-failure-rollback", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			initialVersion = blueGreenUpgradeFromVersion()
			targetVersion = blueGreenUpgradeToVersion()
			if initialVersion == targetVersion {
				Skip("Late-phase rollback test skipped")
			}

			maxFailures := int32(2)
			autoPromote := false
			rollbackFailCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rollback-failure-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    fmt.Sprintf("openbao/openbao:%s", initialVersion),
					Replicas: 3,
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Strategy:    openbaov1alpha1.UpdateStrategyBlueGreen,
						Image:       upgradeExecutorImage,
						JWTAuthRole: portauth.RoleNameUpgrade,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote:    autoPromote,
							MaxJobFailures: &maxFailures,
							AutoRollback: &openbaov1alpha1.AutoRollbackConfig{
								Enabled:      true,
								OnJobFailure: true,
							},
						},
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled:  true,
						Requests: e2ehelpers.CreateE2ERequests(tenantNamespace),
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, rollbackFailCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		It("triggers late-phase rollback after promotion failures and recovers when auth is restored", Label(
			"case:upgrade-strategies-triggers-late-phase-rollback-after-promotion-c44db935",
		), func() {
			By("Triggering a Blue/Green upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for Syncing hold before sending a promote request")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseSyncing))
				g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying wait-green-synced completes before forcing promotion")
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      rollbackFailCluster.Name,
						constants.LabelOpenBaoCluster:   rollbackFailCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())
				waitSyncedJob := findUpgradeExecutorJob(jobs.Items, bluegreen.ActionWaitGreenSynced, "")
				g.Expect(waitSyncedJob).NotTo(BeNil(), "wait-green-synced job should exist before promoting")
				g.Expect(jobSucceeded(waitSyncedJob)).To(BeTrue(), "wait-green-synced job should succeed before promoting")
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			promoteToken := time.Now().UTC().Format(time.RFC3339Nano)

			By("Introducing a realistic temporary auth misconfiguration and sending a promote request")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Upgrade.JWTAuthRole = invalidUpgradeJWTAuthRole
				if updated.Spec.Upgrade.Requests == nil {
					updated.Spec.Upgrade.Requests = &openbaov1alpha1.UpgradeRequestConfig{}
				}
				updated.Spec.Upgrade.Requests.Promote = promoteToken
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying the promote request is recorded before promotion starts")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.UpgradeRequests).NotTo(BeNil())
				g.Expect(updated.Status.UpgradeRequests.LastHandledPromote).To(Equal(promoteToken))
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying promotion executor job fails")
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      rollbackFailCluster.Name,
						constants.LabelOpenBaoCluster:   rollbackFailCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())
				initialPromoteJob := findUpgradeExecutorJob(jobs.Items, bluegreen.ActionPromoteGreenVoters, "")
				g.Expect(initialPromoteJob).NotTo(BeNil(), "initial promote job should exist")
				g.Expect(jobFailed(initialPromoteJob)).To(BeTrue(), "initial promote job should fail")
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying retry promotion job fails and triggers rollback threshold")
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelAppInstance:      rollbackFailCluster.Name,
						constants.LabelOpenBaoCluster:   rollbackFailCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())
				retryPromoteJob := findUpgradeExecutorJob(jobs.Items, bluegreen.ActionPromoteGreenVoters, "retry-1")
				g.Expect(retryPromoteJob).NotTo(BeNil(), "retry promote job should exist")
				g.Expect(jobFailed(retryPromoteJob)).To(BeTrue(), "retry promote job should fail")
			}, 20*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Restoring JWT auth role so rollback automation can proceed")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				original := updated.DeepCopy()
				updated.Spec.Upgrade.JWTAuthRole = portauth.RoleNameUpgrade
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying rollback was initiated")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.RollbackStartTime).NotTo(BeNil())
			}, 15*time.Minute, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying rollback completes and cluster returns to stable initial version")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackFailCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BreakGlass).To(BeNil(), "rollback should recover without entering break glass")
			}, 30*time.Minute, framework.DefaultPollInterval).Should(Succeed())
		})
	})

	// --- Safe Mode (Chaos) ---
	Context("Safe Mode (chaos)", Label("chaos", "bluegreen"), Ordered, func() {
		var (
			tenantNamespace string
			tenantFW        *framework.Framework
			chaosCluster    *openbaov1alpha1.OpenBaoCluster
			initialVersion  string
			targetVersion   string
			admin           client.Client
			cfg             *rest.Config
		)

		BeforeAll(func() {
			var err error
			tenantFW, err = framework.NewSetup(ctx, "tenant-safemode-chaos", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			tenantNamespace = tenantFW.Namespace
			admin = tenantFW.Client

			cfg, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())

			initialVersion = blueGreenUpgradeFromVersion()
			targetVersion = blueGreenUpgradeToVersion()

			if initialVersion == targetVersion {
				Skip(fmt.Sprintf("Safe mode chaos test skipped: from version (%s) equals to version (%s). Set E2E_UPGRADE_TO_VERSION to a different version to test upgrades.", initialVersion, targetVersion))
			}

			// For safe mode test, we need to manually restrict the upgrade policy
			// to cause the repair job to fail. Bootstrap creates a full policy,
			// so we define a request that runs AFTER bootstrap to overwrite it.
			brokenPolicyRequest := openbaov1alpha1.SelfInitRequest{
				Name:      "override-upgrade-policy-broken",
				Operation: openbaov1alpha1.SelfInitOperationUpdate,
				Path:      "sys/policies/acl/openbao-operator-upgrade",
				Policy: &openbaov1alpha1.SelfInitPolicy{
					Policy: `path "sys/health" {
  capabilities = ["read"]
}`,
				},
			}

			e2eRequests := e2ehelpers.CreateE2ERequests(tenantNamespace)
			adminRequests := e2ehelpers.CreateJWTPolicyRoleRequests(
				tenantNamespace,
				"default",
				e2eAdminPolicyName,
				e2eAdminRoleName,
				`path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}`,
			)

			autoPromote := false
			chaosCluster = &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "safemode-chaos-cluster",
					Namespace: tenantNamespace,
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Profile:  openbaov1alpha1.ProfileDevelopment,
					Version:  initialVersion,
					Image:    fmt.Sprintf("openbao/openbao:%s", initialVersion),
					Replicas: 3,
					Upgrade: &openbaov1alpha1.UpgradeConfig{
						Strategy: openbaov1alpha1.UpdateStrategyBlueGreen,
						Image:    upgradeExecutorImage,
						BlueGreen: &openbaov1alpha1.BlueGreenConfig{
							AutoPromote: autoPromote,
							Verification: &openbaov1alpha1.VerificationConfig{
								MinSyncDuration: "30s",
							},
						},
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Enabled: true,
						Image:   configInitImage,
					},
					SelfInit: &openbaov1alpha1.SelfInitConfig{
						Enabled: true,
						OIDC: &openbaov1alpha1.SelfInitOIDCConfig{
							Enabled: true,
						},
						Requests: append(append([]openbaov1alpha1.SelfInitRequest{}, adminRequests...), append([]openbaov1alpha1.SelfInitRequest{brokenPolicyRequest}, e2eRequests...)...),
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
					DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
				},
			}
			Expect(admin.Create(ctx, chaosCluster)).To(Succeed())

			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.Initialized).To(BeTrue(), "cluster should be initialized")
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion), "current version should match initial version")
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil(), "blue/green status should be initialized")
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle), "initial blue/green phase should be Idle")
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		AfterAll(func() {
			if tenantFW != nil {
				cleanupUpgradeFixture(ctx, tenantFW)
			}
		})

		It("enters safe mode when rollback consensus repair job fails", Label(
			"case:upgrade-bluegreen-safe-mode-consensus-repair-failure",
		), func() {
			By("Writing a secret before upgrade")
			secretPath := "secret/safemode-test"
			secretData := map[string]string{"foo": "bar", "version": "v1"}
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   chaosCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}

			// Enable KV engine (idempotent)
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, chaosCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				err = e2ehelpers.WriteSecretViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, secretData)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, 10*time.Second).Should(Succeed(), "Failed to write pre-upgrade secret")

			By("Triggering a Blue/Green upgrade")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				original := updated.DeepCopy()
				updated.Spec.Version = targetVersion
				updated.Spec.Image = fmt.Sprintf("openbao/openbao:%s", targetVersion)

				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for Green revision to be created")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).NotTo(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
				g.Expect(updated.Status.BlueGreen.GreenRevision).NotTo(BeEmpty())
			}, 15*time.Minute, 10*time.Second).Should(Succeed())

			By("Verifying Blue pods are still on initial version and Green pods are on target version")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				blueRevision := updated.Status.BlueGreen.BlueRevision
				greenRevision := updated.Status.BlueGreen.GreenRevision

				// Verify Blue Pods
				bluePods := &corev1.PodList{}
				g.Expect(admin.List(ctx, bluePods,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{constants.LabelOpenBaoRevision: blueRevision},
				)).To(Succeed())
				g.Expect(bluePods.Items).NotTo(BeEmpty(), "Blue pods should exist")
				for _, pod := range bluePods.Items {
					e2ehelpers.ExpectOpenBaoPodVersion(g, pod, initialVersion)
				}

				// Verify Green Pods
				greenPods := &corev1.PodList{}
				g.Expect(admin.List(ctx, greenPods,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{constants.LabelOpenBaoRevision: greenRevision},
				)).To(Succeed())
				// Green pods might still be starting, but if they exist, they must be correct
				for _, pod := range greenPods.Items {
					e2ehelpers.ExpectOpenBaoPodVersion(g, pod, targetVersion)
				}
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Forcing rollback")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())

				original := updated.DeepCopy()
				if updated.Spec.Upgrade == nil {
					updated.Spec.Upgrade = &openbaov1alpha1.UpgradeConfig{}
				}
				if updated.Spec.Upgrade.Requests == nil {
					updated.Spec.Upgrade.Requests = &openbaov1alpha1.UpgradeRequestConfig{}
				}
				updated.Spec.Upgrade.Requests.Rollback = time.Now().UTC().Format(time.RFC3339Nano)

				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for rollback to start")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseRollingBack))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			var rollbackRepairJobName string
			By("Finding the rollback consensus repair job")
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(admin.List(ctx, jobs,
					client.InNamespace(tenantNamespace),
					client.MatchingLabels{
						constants.LabelOpenBaoCluster:   chaosCluster.Name,
						constants.LabelOpenBaoComponent: upgrade.ComponentUpgrade,
					},
				)).To(Succeed())

				job := findUpgradeExecutorJob(jobs.Items, bluegreen.ActionRepairConsensus, rollbackRunID)
				g.Expect(job).NotTo(BeNil(), "rollback repair job should exist")
				rollbackRepairJobName = job.Name
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Waiting for the rollback consensus repair job to fail")
			Eventually(func(g Gomega) {
				job := &batchv1.Job{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: rollbackRepairJobName, Namespace: tenantNamespace}, job)).To(Succeed())
				g.Expect(jobFailed(job)).To(BeTrue(), "rollback repair job should fail")
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying secret persists in safe mode")
			// Secret should still be there as we are technically still on the Blue cluster (or the active one)
			// even if we are in safe mode/break glass state.
			secretPath = "secret/safemode-test"
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, chaosCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				secretVal, err := e2ehelpers.ReadSecretViaJWT(ctx, cfg, admin, tenantNamespace, openBaoImage, baoAddr, "default", "e2e-test", secretPath, bypassLabels, "foo")
				g.Expect(err).NotTo(HaveOccurred(), "Failed to read secret in safe mode")
				g.Expect(secretVal).To(Equal("bar"))
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Asserting safe mode is set on the cluster")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BreakGlass).NotTo(BeNil(), "BreakGlass status should be set")
				g.Expect(updated.Status.BreakGlass.Active).To(BeTrue(), "BreakGlass should be active")
				g.Expect(updated.Status.BreakGlass.Reason).To(Equal(openbaov1alpha1.BreakGlassReasonRollbackConsensusRepairFailed), "BreakGlass reason should match")
				g.Expect(updated.Status.BreakGlass.Nonce).NotTo(BeEmpty(), "BreakGlass nonce should be set")
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		})

		It("acknowledges break glass and resumes rollback after the upgrade policy is repaired", Label(
			"case:upgrade-bluegreen-safe-mode-break-glass-recovery",
		), func() {
			By("Repairing the broken upgrade policy inside OpenBao")
			bypassLabels := map[string]string{
				constants.LabelOpenBaoCluster:   chaosCluster.Name,
				constants.LabelOpenBaoComponent: "backup",
			}
			Eventually(func(g Gomega) {
				baoAddr, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, admin, tenantNamespace, chaosCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				_, err = e2ehelpers.RunCommandViaJWT(
					ctx,
					cfg,
					admin,
					tenantNamespace,
					openBaoImage,
					baoAddr,
					"default",
					e2eAdminRoleName,
					bypassLabels,
					policyWriteCommand("openbao-operator-upgrade", `path "*" { capabilities = ["create", "read", "update", "delete", "list", "sudo"] }`),
				)
				g.Expect(err).NotTo(HaveOccurred())
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Acknowledging break glass with the current nonce")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BreakGlass).NotTo(BeNil())
				g.Expect(updated.Status.BreakGlass.Active).To(BeTrue())
				g.Expect(updated.Status.BreakGlass.Nonce).NotTo(BeEmpty())

				original := updated.DeepCopy()
				updated.Spec.BreakGlassAck = updated.Status.BreakGlass.Nonce
				g.Expect(admin.Patch(ctx, updated, client.MergeFrom(original))).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			Expect(tenantFW.TriggerReconcile(ctx, chaosCluster.Name)).To(Succeed())

			By("Verifying break glass deactivates and rollback resumes")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BreakGlass).NotTo(BeNil())
				g.Expect(updated.Status.BreakGlass.Active).To(BeFalse())
				g.Expect(updated.Status.BreakGlass.AcknowledgedAt).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.RollbackAttempt).To(BeNumerically(">=", 1))
			}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

			By("Verifying rollback completes and the cluster returns to the original version")
			Eventually(func(g Gomega) {
				updated := &openbaov1alpha1.OpenBaoCluster{}
				g.Expect(admin.Get(ctx, types.NamespacedName{Name: chaosCluster.Name, Namespace: tenantNamespace}, updated)).To(Succeed())
				g.Expect(updated.Status.BlueGreen).NotTo(BeNil())
				g.Expect(updated.Status.BlueGreen.Phase).To(Equal(openbaov1alpha1.PhaseIdle))
				g.Expect(updated.Status.BlueGreen.GreenRevision).To(BeEmpty())
				g.Expect(updated.Status.CurrentVersion).To(Equal(initialVersion))
				g.Expect(updated.Status.BreakGlass).NotTo(BeNil())
				g.Expect(updated.Status.BreakGlass.Active).To(BeFalse())
			}, 30*time.Minute, framework.DefaultPollInterval).Should(Succeed())
		})
	})

})
