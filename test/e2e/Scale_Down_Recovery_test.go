//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Scale-down recovery", Label("lifecycle", "cluster", "scaling", "recovery"), func() {
	run := func(ctx SpecContext, readReplica bool) {
		f, err := framework.NewSetup(ctx, "scale-recovery", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		cluster := &openbaov1alpha1.OpenBaoCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "scale-recovery", Namespace: f.Namespace},
			Spec: openbaov1alpha1.OpenBaoClusterSpec{
				Profile: openbaov1alpha1.ProfileDevelopment, Version: openBaoVersion, Image: openBaoImage, Replicas: 3,
				InitContainer: &openbaov1alpha1.InitContainerConfig{Enabled: true, Image: configInitImage},
				TLS:           openbaov1alpha1.TLSConfig{Enabled: true, Mode: openbaov1alpha1.TLSModeOperatorManaged, RotationPeriod: "720h"},
				Storage:       openbaov1alpha1.StorageConfig{Size: "1Gi"},
				Service:       &openbaov1alpha1.ServiceConfig{Type: corev1.ServiceTypeClusterIP},
				Network: &openbaov1alpha1.NetworkConfig{
					APIServerCIDR: apiServerCIDR, APIServerEndpointIPs: apiServerEndpointIPs,
					APIServerEgress: framework.APIServerEgressForNetworkProfile(),
				},
				DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			},
		}
		workloadName, current, retainedMembers, retainedVoters := cluster.Name, int32(3), 2, 2
		if readReplica {
			cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{Replicas: 2}
			workloadName, current, retainedMembers, retainedVoters = cluster.Name+"-read", 2, 4, 3
		}
		victim := fmt.Sprintf("%s-%d", workloadName, current-1)
		const role = "scale-recovery"
		policy := fmt.Sprintf(`path "sys/storage/raft/configuration" { capabilities = ["read"] }
path "sys/storage/raft/autopilot/state" { capabilities = ["read"] }
path "sys/health" { capabilities = ["read"] }
path "sys/step-down" { capabilities = ["sudo", "update"] }
path "sys/storage/raft/remove-peer" {
  capabilities = ["update"]
  allowed_parameters = { "server_id" = [%q] }
}`, victim)
		cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
			Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true},
			Requests: e2ehelpers.CreateJWTPolicyRoleRequests(f.Namespace, "default", role, role, policy),
		}
		DeferCleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			latest := &openbaov1alpha1.OpenBaoCluster{}
			if f.Client.Get(cleanupCtx, client.ObjectKeyFromObject(cluster), latest) == nil && latest.Spec.Paused {
				before := latest.DeepCopy()
				latest.Spec.Paused = false
				Expect(f.Client.Patch(cleanupCtx, latest, client.MergeFrom(before))).To(Succeed())
			}
			Expect(f.Cleanup(cleanupCtx)).To(Succeed())
		})

		By("creating a disposable cluster with a victim-specific Raft maintenance role")
		Expect(f.Client.Create(ctx, cluster)).To(Succeed())
		_, err = f.WaitForStatefulSetReady(ctx, cluster.Name, 3, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		if readReplica {
			f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionReadReplicasReady, metav1.ConditionTrue)
		}
		f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionRaftMembershipReady, metav1.ConditionTrue)
		workloadKey := client.ObjectKey{Namespace: f.Namespace, Name: workloadName}
		initial := &appsv1.StatefulSet{}
		Expect(f.Client.Get(ctx, workloadKey, initial)).To(Succeed())
		Expect(initial.Spec.Replicas).To(HaveValue(Equal(current)))
		retainedUIDs := map[string]types.UID{}
		for ordinal := int32(0); ordinal < current-1; ordinal++ {
			pod := &corev1.Pod{}
			Expect(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: fmt.Sprintf("%s-%d", workloadName, ordinal)}, pod)).To(Succeed())
			retainedUIDs[pod.Name] = pod.UID
		}
		if readReplica {
			for ordinal := int32(0); ordinal < cluster.Spec.Replicas; ordinal++ {
				pod := &corev1.Pod{}
				key := client.ObjectKey{Namespace: f.Namespace, Name: fmt.Sprintf("%s-%d", cluster.Name, ordinal)}
				Expect(f.Client.Get(ctx, key, pod)).To(Succeed())
				retainedUIDs[pod.Name] = pod.UID
			}
		}
		command := func(script string) (string, error) { return scaleRecoveryJWTCommand(ctx, f, cluster, role, script) }
		readState := func() (portopenbao.RaftConfigurationResponse, portopenbao.RaftAutopilotStateResponse, error) {
			output, err := command("bao read -format=json sys/storage/raft/configuration\nbao read -format=json sys/storage/raft/autopilot/state")
			var membership struct {
				Data portopenbao.RaftConfigurationResponse `json:"data"`
			}
			var health struct {
				Data portopenbao.RaftAutopilotStateResponse `json:"data"`
			}
			if err != nil {
				return membership.Data, health.Data, err
			}
			decoder := json.NewDecoder(strings.NewReader(output))
			if err := decoder.Decode(&membership); err != nil {
				return membership.Data, health.Data, err
			}
			err = decoder.Decode(&health)
			return membership.Data, health.Data, err
		}

		By("pausing only this cluster and waiting for the controller to acknowledge the pause")
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		before := cluster.DeepCopy()
		cluster.Spec.Paused = true
		Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(before))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
			condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Reason).To(Equal("Paused"))
			g.Expect(condition.ObservedGeneration).To(Equal(cluster.Generation))
		}, time.Minute, time.Second).Should(Succeed())

		By("removing the highest ordinal peer through its constrained test JWT role")
		membership, _, err := readState()
		Expect(err).NotTo(HaveOccurred())
		victimFound := false
		for _, peer := range membership.Config.Servers {
			if peer.NodeID == victim {
				victimFound = true
				Expect(peer.Voter).To(Equal(!readReplica))
				if peer.Leader {
					_, err = command("bao operator step-down")
					Expect(err).NotTo(HaveOccurred())
				}
			}
		}
		Expect(victimFound).To(BeTrue(), "the highest ordinal must be a real native Raft member")
		Eventually(func(g Gomega) {
			config, _, err := readState()
			g.Expect(err).NotTo(HaveOccurred())
			for _, peer := range config.Config.Servers {
				if peer.NodeID == victim {
					g.Expect(peer.Leader).To(BeFalse())
				}
			}
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
		_, err = command(fmt.Sprintf("bao write sys/storage/raft/remove-peer server_id=%q", victim))
		Expect(err).NotTo(HaveOccurred())

		By("observing the removed victim unready while every retained native member remains healthy")
		Eventually(func(g Gomega) {
			pod := &corev1.Pod{}
			g.Expect(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: victim}, pod)).To(Succeed())
			g.Expect(pod.DeletionTimestamp).To(BeNil())
			g.Expect(isPodReady(pod)).To(BeFalse())
			config, health, err := readState()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(config.Config.Servers).To(HaveLen(retainedMembers))
			voters := 0
			for _, peer := range config.Config.Servers {
				g.Expect(peer.NodeID).NotTo(Equal(victim))
				g.Expect(health.Servers).To(HaveKey(peer.NodeID))
				g.Expect(health.Servers[peer.NodeID].Healthy).To(BeTrue())
				if peer.Voter {
					voters++
				}
			}
			g.Expect(voters).To(Equal(retainedVoters))
			observed := &appsv1.StatefulSet{}
			g.Expect(f.Client.Get(ctx, workloadKey, observed)).To(Succeed())
			g.Expect(observed.Spec.Replicas).To(HaveValue(Equal(current)))
			g.Expect(observed.Status.ReadyReplicas).To(Equal(current - 1))
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("resuming with one fewer desired replica and preserving the workload template and surviving Pods")
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		before = cluster.DeepCopy()
		cluster.Spec.Paused = false
		if readReplica {
			cluster.Spec.ReadReplicas.Replicas--
		} else {
			cluster.Spec.Replicas--
		}
		Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(before))).To(Succeed())
		Eventually(func(g Gomega) {
			observed := &appsv1.StatefulSet{}
			g.Expect(f.Client.Get(ctx, workloadKey, observed)).To(Succeed())
			g.Expect(observed.UID).To(Equal(initial.UID))
			g.Expect(observed.Spec.Replicas).To(HaveValue(Equal(current - 1)))
			g.Expect(observed.Spec.Template).To(Equal(initial.Spec.Template), "recovery must not apply a different image or template")
			g.Expect(observed.Status.ReadyReplicas).To(Equal(current - 1))
			g.Expect(observed.Status.ObservedGeneration).To(BeNumerically(">=", observed.Generation))
			removed := &corev1.Pod{}
			g.Expect(apierrors.IsNotFound(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: victim}, removed))).To(BeTrue())
			for name, uid := range retainedUIDs {
				pod := &corev1.Pod{}
				g.Expect(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: name}, pod)).To(Succeed())
				g.Expect(pod.UID).To(Equal(uid))
				g.Expect(isPodReady(pod)).To(BeTrue())
			}
		}, 5*time.Minute, framework.DefaultPollInterval).Should(Succeed())
		f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		if readReplica {
			f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionReadReplicasReady, metav1.ConditionTrue)
		}
		By("checking the client Service remains responsive after recovery")
		output, err := command(fmt.Sprintf("BAO_ADDR=https://%s-public.%s.svc:8200 bao status -format=json", cluster.Name, f.Namespace))
		Expect(err).NotTo(HaveOccurred())
		var status struct {
			Initialized bool `json:"initialized"`
			Sealed      bool `json:"sealed"`
		}
		Expect(json.Unmarshal([]byte(output), &status)).To(Succeed())
		Expect(status.Initialized).To(BeTrue())
		Expect(status.Sealed).To(BeFalse())
		membership, health, err := readState()
		Expect(err).NotTo(HaveOccurred())
		Expect(membership.Config.Servers).To(HaveLen(retainedMembers))
		for _, peer := range membership.Config.Servers {
			Expect(peer.NodeID).NotTo(Equal(victim))
			Expect(health.Servers).To(HaveKey(peer.NodeID))
			Expect(health.Servers[peer.NodeID].Healthy).To(BeTrue())
		}
	}
	It("finishes a voter decrement after native peer removal", Label(
		"case:scale-down-voter-interruption", "covers:interrupted-scale-down",
	), func(ctx SpecContext) { run(ctx, false) })
	It("finishes a read-replica decrement after native peer removal", Label(
		"case:scale-down-read-replica-interruption", "covers:interrupted-scale-down",
	), func(ctx SpecContext) { run(ctx, true) })
})

func scaleRecoveryJWTCommand(ctx context.Context, f *framework.Framework, cluster *openbaov1alpha1.OpenBaoCluster, role, command string) (string, error) {
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return "", err
	}
	address, err := e2ehelpers.ResolveActiveOpenBaoAddress(ctx, f.Client, f.Namespace, cluster.Name)
	if err != nil {
		return "", err
	}
	script := fmt.Sprintf(`set -eu
export BAO_ADDR=%q BAO_CACERT=/ca/ca.crt BAO_TLS_SERVER_NAME=%q BAO_CLIENT_TIMEOUT=10s
BAO_TOKEN=$(bao write -field=token auth/jwt-operator/login role=%q jwt=@/jwt/token)
export BAO_TOKEN
%s`, address, "openbao-cluster-"+cluster.Name+".local", role, command)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "scale-client-" + uuid.NewString()[:8], Namespace: f.Namespace,
			Labels: map[string]string{constants.LabelOpenBaoCluster: cluster.Name, constants.LabelOpenBaoComponent: constants.ComponentBackup}},
		Spec: corev1.PodSpec{
			ServiceAccountName: "default", AutomountServiceAccountToken: ptr.To(false), RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(100)),
				RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000)),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Volumes: []corev1.Volume{
				{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: cluster.Name + "-tls-ca", Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
				}}},
				{Name: "jwt", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
					ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "openbao-internal", ExpirationSeconds: ptr.To(int64(3600)), Path: "token"},
				}}}}},
			},
			Containers: []corev1.Container{{Name: "bao", Image: openBaoImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-ec"}, Args: []string{script},
				VolumeMounts: []corev1.VolumeMount{{Name: "ca", MountPath: "/ca", ReadOnly: true}, {Name: "jwt", MountPath: "/jwt", ReadOnly: true}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			}},
		},
	}
	defer func() { _ = e2ehelpers.DeletePodBestEffort(ctx, f.Client, f.Namespace, pod.Name) }()
	result, err := e2ehelpers.RunPodUntilCompletion(ctx, cfg, f.Client, pod, 2*time.Minute)
	if err != nil {
		return "", err
	}
	if result.Phase != corev1.PodSucceeded {
		return "", fmt.Errorf("authenticated scale-down client failed: %s", result.Logs)
	}
	return strings.TrimSpace(result.Logs), nil
}
