//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Cluster Networking", Label("networking", "critical"), func() {
	run := func(tlsMode openbaov1alpha1.TLSMode) {
		ctx := context.Background()
		f, err := framework.NewSetup(ctx, "network", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			Expect(f.Cleanup(cleanupCtx)).To(Succeed())
		})
		cfg, err := ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		By("bootstrapping three Raft voters with the generated address environment")
		const serverName = "openbao-cluster-network.local"
		if tlsMode == openbaov1alpha1.TLSModeExternal {
			Expect(e2ehelpers.EnsureExternalTLSSecrets(ctx, f.Client, f.Namespace, "network", 3, serverName)).To(Succeed())
		}
		cluster, err := f.CreateDevelopmentCluster(ctx, framework.DevelopmentClusterConfig{
			TLSMode: tlsMode,
			Name:    "network", Replicas: 3, Version: openBaoVersion, Image: openBaoImage,
			ConfigInitImg: configInitImage, APIServerCIDR: apiServerCIDR,
			APIServerEndpointIPs: apiServerEndpointIPs,
		})
		Expect(err).NotTo(HaveOccurred())
		profile := os.Getenv("E2E_NETWORK_PROFILE")
		By("configuring client and read-replica Services for the requested IP families")
		families := []corev1.IPFamily{corev1.IPv4Protocol}
		if profile == "ipv6" || profile == "dual-ipv6" {
			families[0] = corev1.IPv6Protocol
		}
		policy := corev1.IPFamilyPolicySingleStack
		if strings.HasPrefix(profile, "dual-") {
			policy = corev1.IPFamilyPolicyRequireDualStack
			secondary := corev1.IPv6Protocol
			if families[0] == corev1.IPv6Protocol {
				secondary = corev1.IPv4Protocol
			}
			families = append(families, secondary)
		}
		original := cluster.DeepCopy()
		cluster.Spec.Service = &openbaov1alpha1.ServiceConfig{IPFamilyPolicy: &policy, IPFamilies: families}
		cluster.Spec.ReadReplicas = &openbaov1alpha1.ReadReplicaConfig{
			Replicas: 1,
			Service:  &openbaov1alpha1.ReadReplicaServiceConfig{Enabled: true, IPFamilyPolicy: &policy, IPFamilies: families},
		}
		Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(original))).To(Succeed())
		_, err = f.WaitForStatefulSetReady(ctx, cluster.Name, 3, 10*time.Minute, framework.DefaultPollInterval)
		Expect(err).NotTo(HaveOccurred())
		f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
		f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionReadReplicasReady, metav1.ConditionTrue)
		f.WaitForCondition(cluster.Name, openbaov1alpha1.ConditionRaftMembershipReady, metav1.ConditionTrue)
		for _, name := range []string{cluster.Name + "-public", cluster.Name + "-read"} {
			service := &corev1.Service{}
			Expect(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: name}, service)).To(Succeed())
			Expect(service.Spec.IPFamilies).To(Equal(families))
			Expect(service.Spec.ClusterIPs).To(HaveLen(len(families)))
		}
		peerService := &corev1.Service{}
		Expect(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: cluster.Name}, peerService)).To(Succeed())
		Expect(peerService.Spec.IPFamilies).To(Equal(families))

		var pods corev1.PodList
		Expect(f.Client.List(ctx, &pods, client.InNamespace(f.Namespace), client.MatchingLabels{
			constants.LabelOpenBaoCluster: cluster.Name, constants.LabelOpenBaoComponent: "openbaocluster",
		})).To(Succeed())
		Expect(pods.Items).To(HaveLen(4))
		advertised := make([]string, 0, len(pods.Items))
		var leaderCommands []string
		for _, pod := range pods.Items {
			assertNetworkProfile(pod, profile)
			advertised = append(advertised, "https://"+net.JoinHostPort(pod.Status.PodIP, "8200"))
			hostname := fmt.Sprintf("%s.%s.%s.svc", pod.Name, cluster.Name, f.Namespace)
			tlsName := hostname
			if tlsMode == openbaov1alpha1.TLSModeExternal {
				tlsName = serverName
			}
			address := "https://" + tlsName + ":8200/v1/sys/leader"
			connectDNS := tlsName + ":8200:" + net.JoinHostPort(hostname, "8200")
			leaderCommands = append(leaderCommands, "curl --fail --silent --show-error --max-time 10 --cacert /bao-ca/ca.crt --connect-to "+connectDNS+" "+address+"; printf '\\n'")
			// Connect to every literal address while retaining the DNS identity
			// for certificate verification. This exercises the secondary family.
			for _, podIP := range pod.Status.PodIPs {
				connect := tlsName + ":8200:" + net.JoinHostPort(podIP.IP, "8200")
				leaderCommands = append(leaderCommands, "curl --fail --silent --show-error --max-time 10 --cacert /bao-ca/ca.crt --connect-to "+connect+" "+address+"; printf '\\n'")
			}
		}

		// Use a real listening target and an unselected client as a positive
		// control. A failed connection alone cannot prove policy enforcement.
		By("starting an unrelated HTTP target outside the generated egress allowlist")
		target := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "network-target", Namespace: f.Namespace, Labels: map[string]string{"app": "network-target"}},
			Spec: corev1.PodSpec{
				AutomountServiceAccountToken: ptr.To(false),
				SecurityContext:              networkTestPodSecurity(),
				Containers: []corev1.Container{{
					Name: "target", Image: openBaoImage, Command: []string{"bao"},
					Args:            []string{"server", "-dev", "-dev-listen-address=[::]:18200"},
					SecurityContext: networkTestContainerSecurity(),
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/v1/sys/health", Port: intstr.FromInt32(18200)},
					}},
				}},
			},
		}
		Expect(f.Client.Create(ctx, target)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(target), target)).To(Succeed())
			g.Expect(isPodReady(target)).To(BeTrue())
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		targetURL := "http://" + net.JoinHostPort(target.Status.PodIP, "18200") + "/v1/sys/health"

		runClient := func(name, script string, selected bool) string {
			labels := map[string]string{}
			if selected {
				labels[constants.LabelOpenBaoCluster] = cluster.Name
				labels[constants.LabelOpenBaoComponent] = "backup"
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.Namespace, Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: networkTestPodSecurity(),
					Containers: []corev1.Container{{
						Name: "curl", Image: "curlimages/curl:8.16.0", Command: []string{"/bin/sh", "-ec", script},
						SecurityContext: networkTestContainerSecurity(),
						VolumeMounts: []corev1.VolumeMount{
							{Name: "bao-ca", MountPath: "/bao-ca", ReadOnly: true},
							{Name: "kube-ca", MountPath: "/kube-ca", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "bao-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: cluster.Name + constants.SuffixTLSCA,
							Items:      []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
						}}},
						{Name: "kube-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
						}}},
					},
				},
			}
			result, runErr := e2ehelpers.RunPodUntilCompletion(ctx, cfg, f.Client, pod, 2*time.Minute)
			Expect(runErr).NotTo(HaveOccurred())
			Expect(result.Phase).To(Equal(corev1.PodSucceeded), result.Logs)
			return result.Logs
		}

		By("checking CA-verified DNS connections and advertised leader URLs from a selected helper Pod")
		output := runClient("network-leaders", strings.Join(leaderCommands, "\n"), true)
		decoder := json.NewDecoder(strings.NewReader(output))
		for range leaderCommands {
			var leader struct {
				LeaderAddress string `json:"leader_address"`
			}
			Expect(decoder.Decode(&leader)).To(Succeed(), output)
			Expect(advertised).To(ContainElement(leader.LeaderAddress))
		}
		Expect(decoder.Decode(&struct{}{})).To(Equal(io.EOF))

		By("checking API server access with the Kubernetes CA")
		runClient("network-api", "curl --fail --silent --show-error --max-time 10 --cacert /kube-ca/ca.crt https://kubernetes.default.svc/healthz", true)
		By("proving the unrelated target is reachable from an unselected client")
		runClient("network-control", "curl --fail --silent --show-error --max-time 10 "+targetURL, false)
		By("proving the generated helper policy blocks a fresh connection to the same target")
		runClient("network-denied", "set +e; curl --silent --show-error --max-time 5 "+targetURL+"; code=$?; test \"$code\" = 28", true)
		By("checking the OpenBao workload policy against the same listening target")
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, "kubectl", "-n", f.Namespace, "exec", pods.Items[0].Name, "-c", "openbao", "--",
			"env", "BAO_CLIENT_TIMEOUT=5s", "bao", "status", "-address=http://"+net.JoinHostPort(target.Status.PodIP, "18200"))
		denied, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred())
		Expect(probeCtx.Err()).NotTo(HaveOccurred(), "kubectl exec itself must remain reachable")
		Expect(string(denied)).To(ContainSubstring("context deadline exceeded"))
	}

	It("advertises valid Pod IP URLs and enforces workload egress policy", Label(
		"case:network-addresses-and-policy", "e2e-anchor", "covers:pod-ip-advertisement", "covers:network-policy-enforcement",
	), func() { run(openbaov1alpha1.TLSModeOperatorManaged) })

	It("uses External TLS with only the stable server name", Label(
		"case:network-external-tls-and-policy", "e2e-anchor", "covers:pod-ip-advertisement", "covers:network-policy-enforcement",
	), func() { run(openbaov1alpha1.TLSModeExternal) })
})

func assertNetworkProfile(pod corev1.Pod, profile string) {
	primary, err := netip.ParseAddr(pod.Status.PodIP)
	Expect(err).NotTo(HaveOccurred())
	if profile == "" {
		return
	}
	expectIPv6 := profile == "ipv6" || profile == "dual-ipv6"
	Expect(primary.Is6()).To(Equal(expectIPv6), "primary Pod IP for %s: %s", profile, pod.Status.PodIP)
	if strings.HasPrefix(profile, "dual-") {
		Expect(pod.Status.PodIPs).To(HaveLen(2))
		secondary, parseErr := netip.ParseAddr(pod.Status.PodIPs[1].IP)
		Expect(parseErr).NotTo(HaveOccurred())
		Expect(secondary.Is6()).NotTo(Equal(primary.Is6()))
	} else {
		Expect(pod.Status.PodIPs).To(HaveLen(1))
	}
}

func networkTestPodSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(100)), RunAsGroup: ptr.To(int64(1000)),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func networkTestContainerSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}
