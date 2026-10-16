//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
)

var _ = Describe("Controller JWT lifecycle", Label("lifecycle", "controller-jwt", "slow"), Serial, func() {
	It("renews Target credentials and recovers an initialized migration from an older snapshot", Label(
		"case:controller-jwt-renewal-migration-recovery",
		"covers:controller-jwt-renewal", "covers:controller-jwt-migration", "covers:controller-jwt-recovery",
	), func(ctx SpecContext) {
		f, err := framework.NewSetup(ctx, "controller-jwt", operatorNamespace)
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
		controller := controllerJWTPod(ctx, f.Client)
		for _, env := range controller.Spec.Containers[0].Env {
			if env.Name == "OPENBAO_JWT_AUTH_STRATEGY" {
				Expect(env.Value).To(Or(BeEmpty(), Equal("inline")), "this lifecycle scenario validates the default inline transport")
			}
		}

		By("bootstrapping Target mode with the operator-generated roles and policy approval")
		target, targetAPI := newControllerJWTCluster(ctx, f, "target", openbaov1alpha1.ControllerJWTModeTarget)
		targetAudience, err := portauth.ControllerJWTAudience(target, "")
		Expect(err).NotTo(HaveOccurred())
		targetAPI.expectAudiences([]string{targetAudience}, controller)
		targetAPI.repairAutopilot(ctx, f, target)
		started := time.Now()
		targetUID := target.UID
		targetCredential := controllerJWTCredentialFor(ctx, clientset, controller, targetAudience)
		targetToken := targetCredential.Token
		sharedToken := controllerJWTFor(ctx, clientset, controller, portauth.TokenAudienceOpenBaoInternal)
		targetAPI.expectJWT(targetToken, true)
		targetAPI.expectJWT(sharedToken, false)
		initialPolicyRevision := target.Status.Workload.PolicyRevision

		By("deleting an operational policy while the original Target credential ages")
		targetAPI.request(http.MethodDelete, "sys/policies/acl/openbao-operator", nil, http.StatusNoContent)

		By("initializing an existing-style Shared cluster and saving its actual Raft snapshot")
		legacy, legacyAPI := newControllerJWTCluster(ctx, f, "migrate", "")
		legacyUID := legacy.UID
		legacyAPI.expectAudiences([]string{portauth.TokenAudienceOpenBaoInternal}, controller)
		legacyAPI.expectJWT(sharedToken, true)
		legacyAPI.write("secret/validation", map[string]any{"value": "before-migration"})
		snapshot := legacyAPI.request(http.MethodGet, "sys/storage/raft/snapshot", nil, http.StatusOK)
		Expect(snapshot).NotTo(BeEmpty())
		legacy.Spec.ControllerJWTMode = openbaov1alpha1.ControllerJWTModeTarget
		legacyAudience, err := portauth.ControllerJWTAudience(legacy, "")
		Expect(err).NotTo(HaveOccurred())
		legacyToken := controllerJWTFor(ctx, clientset, controller, legacyAudience)

		By("preparing the existing role, switching the CR, then removing shared trust")
		legacyAPI.setAudiences([]string{portauth.TokenAudienceOpenBaoInternal, legacyAudience})
		legacyAPI.expectJWT(legacyToken, true)
		Expect(f.Client.Patch(ctx, legacy, client.RawPatch("application/merge-patch+json",
			[]byte(`{"spec":{"controllerJWTMode":"Target"}}`)))).To(Succeed())
		legacyAPI.setAudiences([]string{legacyAudience})
		legacyAPI.expectAudiences([]string{legacyAudience}, controller)
		legacyAPI.expectJWT(sharedToken, false)
		legacyAPI.expectJWT(targetToken, false)
		targetAPI.expectJWT(legacyToken, false)
		legacyAPI.repairAutopilot(ctx, f, legacy)
		legacyAPI.write("secret/validation", map[string]any{"value": "after-migration"})

		By("restoring the pre-migration snapshot while the CR remains in Target mode")
		legacyAPI.request(http.MethodPost, "sys/storage/raft/snapshot-force", snapshot, http.StatusNoContent, http.StatusOK)
		Eventually(legacyAPI.login, 2*time.Minute, 2*time.Second).Should(Succeed())
		Eventually(func() (string, error) {
			data, err := legacyAPI.read("secret/validation")
			if err != nil {
				return "", err
			}
			value, _ := data["value"].(string)
			return value, nil
		}, 2*time.Minute, 2*time.Second).Should(Equal("before-migration"))
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(legacy), legacy)).To(Succeed())
		Expect(legacy.UID).To(Equal(legacyUID))
		Expect(legacy.Spec.ControllerJWTMode).To(Equal(openbaov1alpha1.ControllerJWTModeTarget))
		legacyAPI.expectAudiences([]string{portauth.TokenAudienceOpenBaoInternal}, controller)
		legacyAPI.expectJWT(legacyToken, false)
		legacyAPI.expectJWT(sharedToken, true)
		legacyAPI.write("sys/storage/raft/autopilot/configuration", map[string]any{"min_quorum": 99})
		Expect(f.TriggerReconcile(ctx, legacy.Name)).To(Succeed())
		Consistently(func() (any, error) {
			data, err := legacyAPI.read("sys/storage/raft/autopilot/configuration")
			return data["min_quorum"], err
		}, 70*time.Second, 5*time.Second).Should(Equal(float64(99)), "the controller must not fall back to the restored shared role")

		By("repairing restored audience trust through the independent administrator login")
		legacyAPI.setAudiences([]string{legacyAudience})
		legacyAPI.expectJWT(legacyToken, true)
		legacyAPI.expectJWT(sharedToken, false)
		legacyAPI.expectJWT(targetToken, false)
		legacyAPI.repairAutopilot(ctx, f, legacy)

		By("observing periodic policy repair during the original Target credential's lifetime")
		targetAPI.waitForOperatorPolicy(6 * time.Minute)
		periodicRepairElapsed := time.Since(started)
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(target), target)).To(Succeed())
		Expect(target.Status.Workload.PolicyRevision).To(Equal(initialPolicyRevision),
			"periodic repair must not depend on a desired policy-bundle change")

		By("keeping the same controller process alive beyond the initial JWT's ten-minute lifetime")
		Eventually(func() bool {
			return time.Since(started) >= 10*time.Minute && time.Now().After(targetCredential.ExpirationTimestamp.Add(5*time.Second))
		}, 12*time.Minute, 2*time.Second).Should(BeTrue())
		// The generated role retains its production clock-skew and expiration
		// leeway. Require actual rejection after that window, not just exp < now.
		Eventually(func() (bool, error) { return targetAPI.jwtRejected(targetToken) },
			90*time.Second, 2*time.Second).Should(BeTrue(), "OpenBao must reject the expired credential")
		currentController := controllerJWTPod(ctx, f.Client)
		Expect(currentController.UID).To(Equal(controller.UID))
		Expect(currentController.Status.ContainerStatuses[0].RestartCount).To(Equal(controller.Status.ContainerStatuses[0].RestartCount))
		Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(target), target)).To(Succeed())
		Expect(target.UID).To(Equal(targetUID))
		// The original credential is now expired; subsequent controller operations
		// can succeed only with a renewed credential and the same strict role.
		targetAPI.expectJWT(targetToken, false)
		targetAPI.expectAudiences([]string{targetAudience}, controller)
		targetAPI.repairAutopilot(ctx, f, target)

		By("repairing a deleted operational policy using the renewed Target credential")
		targetAPI.request(http.MethodDelete, "sys/policies/acl/openbao-operator", nil, http.StatusNoContent)
		// Both strategies are already approved. An idle strategy change changes
		// the desired bundle and bypasses its successful verification cache.
		// It does not change the Pod-bound credential's cache key or lifetime.
		Expect(f.Client.Patch(ctx, target, client.RawPatch("application/merge-patch+json",
			[]byte(`{"spec":{"upgrade":{"strategy":"BlueGreen"}}}`)))).To(Succeed())
		targetAPI.waitForOperatorPolicy(2 * time.Minute)
		Eventually(func(g Gomega) {
			g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(target), target)).To(Succeed())
			g.Expect(target.Status.Workload.PolicyRevision).NotTo(Equal(initialPolicyRevision))
			g.Expect(target.Status.Workload.PolicyReconciliation.LastError).To(BeNil())
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
		targetAPI.repairAutopilot(ctx, f, target)
		finalController := controllerJWTPod(ctx, f.Client)
		Expect(finalController.UID).To(Equal(controller.UID))
		Expect(finalController.Status.ContainerStatuses[0].RestartCount).To(Equal(controller.Status.ContainerStatuses[0].RestartCount))
		AddReportEntry("controller-jwt-validation", map[string]any{
			"targetUID": targetUID, "migratedUID": legacyUID, "controllerPodUID": controller.UID,
			"elapsedSeconds": int(time.Since(started).Seconds()), "transport": "inline",
			"snapshotRestore": "pre-migration role restored and repaired", "policyRepair": "verified after JWT expiration",
			"initialJWTExpiresAt":                targetCredential.ExpirationTimestamp.Time,
			"periodicPolicyRepairElapsedSeconds": int(periodicRepairElapsed.Seconds()),
		})
	}, SpecTimeout(25*time.Minute))
})

func controllerJWTPod(ctx context.Context, c client.Client) corev1.Pod {
	pods := &corev1.PodList{}
	Expect(c.List(ctx, pods, client.InNamespace(operatorNamespace),
		client.MatchingLabels{"app.kubernetes.io/component": "controller"})).To(Succeed())
	Expect(pods.Items).To(HaveLen(1))
	Expect(pods.Items[0].Status.ContainerStatuses).NotTo(BeEmpty())
	return pods.Items[0]
}

func controllerJWTFor(ctx context.Context, c kubernetes.Interface, pod corev1.Pod, audience string) string {
	return controllerJWTCredentialFor(ctx, c, pod, audience).Token
}

func controllerJWTCredentialFor(ctx context.Context, c kubernetes.Interface, pod corev1.Pod, audience string) authenticationv1.TokenRequestStatus {
	response, err := c.CoreV1().ServiceAccounts(pod.Namespace).CreateToken(ctx, pod.Spec.ServiceAccountName,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
			Audiences: []string{audience}, ExpirationSeconds: ptr.To(int64(600)),
			BoundObjectRef: &authenticationv1.BoundObjectReference{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID},
		}}, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(response.Status.ExpirationTimestamp.Time).To(BeTemporally("<", time.Now().Add(11*time.Minute)))
	return response.Status
}

func (a *controllerJWTAPI) waitForOperatorPolicy(timeout time.Duration) {
	Eventually(func() error {
		data, err := a.read("sys/policies/acl/openbao-operator")
		if err != nil {
			return err
		}
		policy, _ := data["policy"].(string)
		if policy == "" {
			return fmt.Errorf("operational policy has not been repaired")
		}
		return nil
	}, timeout, 2*time.Second).Should(Succeed())
}

type controllerJWTAPI struct {
	ctx      context.Context
	address  string
	http     *http.Client
	password string
	token    string
}

func newControllerJWTCluster(ctx context.Context, f *framework.Framework, name string, mode openbaov1alpha1.ControllerJWTMode) (*openbaov1alpha1.OpenBaoCluster, *controllerJWTAPI) {
	password := uuid.NewString()
	userData, err := json.Marshal(map[string]any{"password": password, "token_policies": []string{"jwt-validation-admin"}, "token_ttl": "1h"})
	Expect(err).NotTo(HaveOccurred())
	cluster := &openbaov1alpha1.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.Namespace}, Spec: openbaov1alpha1.OpenBaoClusterSpec{
		Profile: openbaov1alpha1.ProfileDevelopment, Version: openBaoVersion, Image: openBaoImage, Replicas: 1,
		ControllerJWTMode: mode, ReconcilePolicies: true,
		InitContainer: &openbaov1alpha1.InitContainerConfig{Enabled: true, Image: configInitImage},
		TLS:           openbaov1alpha1.TLSConfig{Enabled: true, Mode: openbaov1alpha1.TLSModeOperatorManaged, RotationPeriod: "720h"},
		Storage:       openbaov1alpha1.StorageConfig{Size: "1Gi"}, DeletionPolicy: openbaov1alpha1.DeletionPolicyDeleteAll,
		Network: &openbaov1alpha1.NetworkConfig{APIServerCIDR: apiServerCIDR, APIServerEndpointIPs: apiServerEndpointIPs,
			APIServerEgress: framework.APIServerEgressForNetworkProfile()},
		SelfInit: &openbaov1alpha1.SelfInitConfig{Enabled: true, OIDC: &openbaov1alpha1.SelfInitOIDCConfig{Enabled: true}, Requests: []openbaov1alpha1.SelfInitRequest{
			{Name: "admin-policy", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "sys/policies/acl/jwt-validation-admin",
				Policy: &openbaov1alpha1.SelfInitPolicy{Policy: `path "*" { capabilities = ["create", "read", "update", "delete", "list", "sudo"] }`}},
			{Name: "admin-auth", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "sys/auth/userpass",
				AuthMethod: &openbaov1alpha1.SelfInitAuthMethod{Type: "userpass"}},
			{Name: "admin-user", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "auth/userpass/users/validator", Data: &apiextensionsv1.JSON{Raw: userData}},
			{Name: "validation-data", Operation: openbaov1alpha1.SelfInitOperationUpdate, Path: "sys/mounts/secret",
				SecretEngine: &openbaov1alpha1.SelfInitSecretEngine{Type: "kv"}},
		}},
	}}
	Expect(f.Client.Create(ctx, cluster)).To(Succeed())
	_, err = f.WaitForStatefulSetReady(ctx, name, 1, 5*time.Minute, 2*time.Second)
	Expect(err).NotTo(HaveOccurred())
	f.WaitForCondition(name, openbaov1alpha1.ConditionAvailable, metav1.ConditionTrue)
	Eventually(func(g Gomega) {
		g.Expect(f.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		g.Expect(cluster.Status.Workload).NotTo(BeNil())
		g.Expect(cluster.Status.Workload.PolicyRevision).NotTo(BeEmpty())
		g.Expect(cluster.Status.Workload.PolicyReconciliation).NotTo(BeNil())
		g.Expect(cluster.Status.Workload.PolicyReconciliation.LastError).To(BeNil())
	}, 3*time.Minute, 2*time.Second).Should(Succeed())
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
	DeferCleanup(api.http.CloseIdleConnections)
	Eventually(api.login, time.Minute, time.Second).Should(Succeed())
	return cluster, api
}

func (a *controllerJWTAPI) exchange(method, path string, body []byte, token string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(a.ctx, method, a.address+"/v1/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("X-Vault-Token", token)
	request.Header.Set("Content-Type", "application/json")
	if path == "sys/storage/raft/snapshot-force" {
		request.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := a.http.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	result, err := io.ReadAll(io.LimitReader(response.Body, 64*1024*1024))
	return result, response.StatusCode, err
}

func (a *controllerJWTAPI) request(method, path string, body []byte, want ...int) []byte {
	result, status, err := a.exchange(method, path, body, a.token)
	Expect(err).NotTo(HaveOccurred(), "request %s", path)
	Expect(want).To(ContainElement(status), "status for %s", path)
	return result
}

func (a *controllerJWTAPI) write(path string, data map[string]any) {
	body, err := json.Marshal(data)
	Expect(err).NotTo(HaveOccurred())
	a.request(http.MethodPost, path, body, http.StatusOK, http.StatusNoContent)
}

func (a *controllerJWTAPI) read(path string) (map[string]any, error) {
	body, status, err := a.exchange(http.MethodGet, path, nil, a.token)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("read %s: HTTP %d", path, status)
	}
	var response struct{ Data map[string]any }
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (a *controllerJWTAPI) login() error {
	payload, err := json.Marshal(map[string]string{"password": a.password})
	if err != nil {
		return err
	}
	body, status, err := a.exchange(http.MethodPost, "auth/userpass/login/validator", payload, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("administrator login: HTTP %d", status)
	}
	var response struct {
		Auth struct {
			Token string `json:"client_token"`
		}
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return err
	}
	if response.Auth.Token == "" {
		return fmt.Errorf("administrator login returned no token")
	}
	a.token = response.Auth.Token
	return nil
}

func (a *controllerJWTAPI) expectJWT(jwt string, allowed bool) {
	status, err := a.jwtLoginStatus(jwt)
	Expect(err).NotTo(HaveOccurred())
	if allowed {
		Expect(status).To(Equal(http.StatusOK))
	} else {
		Expect(status).To(Or(Equal(http.StatusBadRequest), Equal(http.StatusForbidden)))
	}
}

func (a *controllerJWTAPI) jwtLoginStatus(jwt string) (int, error) {
	payload, err := json.Marshal(map[string]string{"jwt": jwt, "role": portauth.RoleNameOperator})
	if err != nil {
		return 0, err
	}
	_, status, err := a.exchange(http.MethodPost, "auth/jwt-operator/login", payload, "")
	return status, err
}

func (a *controllerJWTAPI) jwtRejected(jwt string) (bool, error) {
	status, err := a.jwtLoginStatus(jwt)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusBadRequest, http.StatusForbidden:
		return true, nil
	case http.StatusOK:
		return false, nil
	default:
		return false, fmt.Errorf("JWT login returned HTTP %d instead of a credential validation result", status)
	}
}

func (a *controllerJWTAPI) expectAudiences(audiences []string, controller corev1.Pod) {
	role, err := a.read("auth/jwt-operator/role/openbao-operator")
	Expect(err).NotTo(HaveOccurred())
	Expect(role["bound_audiences"]).To(ConsistOf(audiences))
	Expect(role["bound_subject"]).To(Equal("system:serviceaccount:" + controller.Namespace + ":" + controller.Spec.ServiceAccountName))
	Expect(role["token_policies"]).To(ContainElement(portauth.PolicyNameApproval))
}

func (a *controllerJWTAPI) setAudiences(audiences []string) {
	role, err := a.read("auth/jwt-operator/role/openbao-operator")
	Expect(err).NotTo(HaveOccurred())
	role["bound_audiences"] = audiences
	a.write("auth/jwt-operator/role/openbao-operator", role)
}

func (a *controllerJWTAPI) repairAutopilot(ctx context.Context, f *framework.Framework, cluster *openbaov1alpha1.OpenBaoCluster) {
	a.write("sys/storage/raft/autopilot/configuration", map[string]any{"min_quorum": 99})
	Expect(f.TriggerReconcile(ctx, cluster.Name)).To(Succeed())
	Eventually(func() (any, error) {
		data, err := a.read("sys/storage/raft/autopilot/configuration")
		return data["min_quorum"], err
	}, 2*time.Minute, 2*time.Second).Should(Equal(float64(1)), "controller Raft maintenance must restore the declared quorum")
}
