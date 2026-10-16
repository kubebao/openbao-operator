//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
)

var _ = Describe("Self-init Secret inputs", Label("lifecycle", "cluster", "self-init-inputs"), func() {
	It("requires inputs before initialization and restarts without the bootstrap Secret", Label(
		"case:self-init-secret-input-lifecycle", "covers:self-init-secret-inputs", "covers:bootstrap-secret-removal",
	), func(ctx SpecContext) {
		f, err := framework.NewSetup(ctx, "secret-inputs", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			Expect(f.Cleanup(cleanupCtx)).To(Succeed())
		})
		cfg, err := ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())
		clientset, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		password := "bootstrap-canary-" + uuid.NewString()
		cluster := secretInputE2ECluster(f.Namespace)

		By("blocking startup before OpenBao initializes when the Secret is absent")
		Expect(f.Client.Create(ctx, cluster)).To(Succeed())
		podKey := client.ObjectKey{Namespace: f.Namespace, Name: cluster.Name + "-0"}
		pod := &corev1.Pod{}
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, podKey, pod)).To(Succeed())
			g.Expect(pod.Status.InitContainerStatuses).To(HaveLen(1))
			g.Expect(pod.Status.InitContainerStatuses[0].RestartCount).To(BeNumerically(">=", 1))
			for _, status := range pod.Status.ContainerStatuses {
				g.Expect(status.State.Running).To(BeNil())
			}
		}, 3*time.Minute, time.Second).Should(Succeed())
		firstRestarts := pod.Status.InitContainerStatuses[0].RestartCount

		By("keeping startup blocked when the Secret lacks the selected key")
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-login", Namespace: f.Namespace},
			Data: map[string][]byte{"unselected": []byte(password)}}
		Expect(f.Client.Create(ctx, secret)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, podKey, pod)).To(Succeed())
			g.Expect(pod.Status.InitContainerStatuses[0].RestartCount).To(BeNumerically(">", firstRestarts))
			for _, status := range pod.Status.ContainerStatuses {
				g.Expect(status.State.Running).To(BeNil())
			}
		}, 2*time.Minute, time.Second).Should(Succeed())

		// Keep the original bootstrap Pod available for credential-leak checks.
		// Pause only this cluster, after the missing-input workload exists.
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		beforePause := cluster.DeepCopy()
		cluster.Spec.Paused = true
		Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(beforePause))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
			condition := meta.FindStatusCondition(cluster.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Reason).To(Equal("Paused"))
		}, time.Minute, time.Second).Should(Succeed())
		bootstrapUID := pod.UID

		By("consuming the selected key without giving the controller Secret read access")
		secret.Data["password"] = []byte(password)
		Expect(f.Client.Update(ctx, secret)).To(Succeed())
		_, err = f.WaitForStatefulSetReady(ctx, cluster.Name, 1, 5*time.Minute, 2*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Client.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.UID).To(Equal(bootstrapUID))
		Expect(pod.Status.ContainerStatuses).To(HaveLen(1))
		Expect(pod.Status.ContainerStatuses[0].Name).To(Equal(constants.ContainerBao))
		Expect(pod.Status.ContainerStatuses[0].RestartCount).To(BeZero())
		controller := controllerJWTPod(ctx, f.Client)
		review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               "system:serviceaccount:" + controller.Namespace + ":" + controller.Spec.ServiceAccountName,
			ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: f.Namespace, Verb: "get", Resource: "secrets", Name: secret.Name},
		}}
		Expect(f.Client.Create(ctx, review)).To(Succeed())
		Expect(review.Status.Allowed).To(BeFalse())
		sts := &appsv1.StatefulSet{}
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), sts)).To(Succeed())
		api, stop := secretInputE2EAPI(ctx, f, cluster.Name, password)
		Eventually(api.login, time.Minute, time.Second).Should(Succeed())
		stop()

		By("checking generated resources, events, configuration, and normal logs for the canary")
		rendered, err := exec.CommandContext(ctx, "kubectl", "exec", "-n", f.Namespace, pod.Name,
			"-c", constants.ContainerBao, "--", "cat", "/etc/bao/rendered-config/config.hcl").Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rendered)).To(ContainSubstring(constants.PathSelfInitInputs))
		Expect(strings.Contains(string(rendered), password)).To(BeFalse(), "credential leaked into rendered configuration")
		configMaps := &corev1.ConfigMapList{}
		Expect(f.Client.List(ctx, configMaps, client.InNamespace(f.Namespace))).To(Succeed())
		events := &corev1.EventList{}
		Expect(f.Client.List(ctx, events, client.InNamespace(f.Namespace))).To(Succeed())
		for _, object := range []any{cluster, configMaps, events, sts} {
			encoded, encodeErr := json.Marshal(object)
			Expect(encodeErr).NotTo(HaveOccurred())
			Expect(strings.Contains(string(encoded), password)).To(BeFalse(), "credential leaked into a generated resource")
		}
		for _, source := range []struct{ namespace, pod, container string }{
			{f.Namespace, pod.Name, "bao-config-init"}, {f.Namespace, pod.Name, constants.ContainerBao},
			{controller.Namespace, controller.Name, controller.Spec.Containers[0].Name},
		} {
			logs, logErr := clientset.CoreV1().Pods(source.namespace).GetLogs(source.pod,
				&corev1.PodLogOptions{Container: source.container}).DoRaw(ctx)
			Expect(logErr).NotTo(HaveOccurred())
			Expect(strings.Contains(string(logs), password)).To(BeFalse(), "credential leaked into normal logs")
		}
		Expect(f.Client.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.UID).To(Equal(bootstrapUID), "leak checks must inspect the original bootstrap Pod")
		Expect(pod.Status.ContainerStatuses).To(HaveLen(1))
		Expect(pod.Status.ContainerStatuses[0].RestartCount).To(BeZero(), "leak checks must retain the original main-container logs")

		By("completing bootstrap and removing the Secret input projections")
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		beforeResume := cluster.DeepCopy()
		cluster.Spec.Paused = false
		Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(beforeResume))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
			g.Expect(cluster.Status.SelfInitialized).To(BeTrue())
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), sts)).To(Succeed())
			encoded, encodeErr := json.Marshal(sts.Spec.Template)
			g.Expect(encodeErr).NotTo(HaveOccurred())
			g.Expect(string(encoded)).NotTo(ContainSubstring(constants.PathSelfInitInputs))
			g.Expect(sts.Status.ObservedGeneration).To(BeNumerically(">=", sts.Generation))
			g.Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))
			g.Expect(sts.Status.UpdateRevision).NotTo(BeEmpty())
			g.Expect(sts.Status.CurrentRevision).To(Equal(sts.Status.UpdateRevision))
		}, 3*time.Minute, time.Second).Should(Succeed())

		By("deleting the bootstrap Secret and replacing the initialized Pod")
		Expect(f.Client.Delete(ctx, secret)).To(Succeed())
		Expect(f.Client.Get(ctx, podKey, pod)).To(Succeed())
		originalUID := pod.UID
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		beforeRestart := cluster.DeepCopy()
		cluster.Spec.Runtime = &openbaov1alpha1.RuntimeConfig{RestartAt: time.Now().UTC().Format(time.RFC3339Nano)}
		Expect(f.Client.Patch(ctx, cluster, client.MergeFrom(beforeRestart))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, podKey, pod)).To(Succeed())
			g.Expect(pod.UID).NotTo(Equal(originalUID))
			g.Expect(pod.Status.ContainerStatuses).NotTo(BeEmpty())
			for _, status := range pod.Status.ContainerStatuses {
				g.Expect(status.Ready).To(BeTrue())
			}
		}, 5*time.Minute, 2*time.Second).Should(Succeed())
		api, stop = secretInputE2EAPI(ctx, f, cluster.Name, password)
		defer stop()
		Eventually(api.login, time.Minute, time.Second).Should(Succeed())
	}, SpecTimeout(15*time.Minute))
})

func secretInputE2ECluster(namespace string) *openbaov1alpha1.OpenBaoCluster {
	return &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: "secret-inputs", Namespace: namespace},
		Spec: openbaov1alpha1.OpenBaoClusterSpec{
			Profile: openbaov1alpha1.ProfileDevelopment, Version: openBaoVersion, Image: openBaoImage, Replicas: 1,
			InitContainer: &openbaov1alpha1.InitContainerConfig{Enabled: true, Image: configInitImage},
			TLS:           openbaov1alpha1.TLSConfig{Enabled: true, Mode: openbaov1alpha1.TLSModeOperatorManaged, RotationPeriod: "720h"},
			Storage:       openbaov1alpha1.StorageConfig{Size: "1Gi"}, DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
			Network: &openbaov1alpha1.NetworkConfig{APIServerCIDR: apiServerCIDR, APIServerEndpointIPs: apiServerEndpointIPs,
				APIServerEgress: framework.APIServerEgressForNetworkProfile()},
			SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true,
				OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}, Requests: []openbaov1alpha1.SelfInitRequest{
					{Name: "admin-auth", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "sys/auth/userpass",
						AuthMethod: &openbaov1alpha1.SelfInitAuthMethod{Type: "userpass"}},
					{Name: "admin-user", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "auth/userpass/users/validator",
						Data: &apiextensionsv1.JSON{Raw: []byte(`{}`)},
						DataFrom: map[string]openbaov1alpha1.SelfInitDataSource{"password": {
							SecretKeyRef: openbaov1alpha1.SelfInitSecretKeyReference{Name: "bootstrap-login", Key: "password"},
						}},
					},
				}},
		}}
}

func secretInputE2EAPI(ctx context.Context, f *framework.Framework, name, password string) (*controllerJWTAPI, func()) {
	ca := &corev1.Secret{}
	Expect(f.Client.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: name + "-tls-ca"}, ca)).To(Succeed())
	roots := x509.NewCertPool()
	Expect(roots.AppendCertsFromPEM(ca.Data["ca.crt"])).To(BeTrue())
	address, stop, err := startTLSPodPortForward(f.Namespace, name+"-0")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(stop)
	api := &controllerJWTAPI{ctx: ctx, address: "https://" + address, password: password,
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, ServerName: "openbao-cluster-" + name + ".local", MinVersion: tls.VersionTLS12,
		}}},
	}
	return api, func() { api.http.CloseIdleConnections(); stop() }
}
