//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	adapterauth "github.com/kubebao/openbao-operator/internal/adapter/auth"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	"github.com/kubebao/openbao-operator/test/e2e/helpers"
)

// This qualification needs a disposable, single-node source Kind cluster with
// the operator already installed. It deliberately stops that node after backup.
// KUBECONFIG selects the recovery cluster; its CNI must enforce NetworkPolicy.
var _ = Describe("Restore across independent Kubernetes clusters", Ordered, Serial,
	Label("restore-cross-cluster", "dr", "restore", "slow"), func() {
		ctx := context.Background()
		var sourceClient, destinationClient client.Client
		var sourceConfig, destinationConfig *rest.Config
		var sourceFW, destinationFW *framework.Framework
		var source *api.OpenBaoCluster
		var template *api.RestoreClusterTemplate
		var snapshot api.RestoreSource
		var sourceKind, infrastructureNamespace, sourceIssuer, destinationIssuer string
		var storageService *corev1.Service
		var stopped bool
		password := uuid.NewString()
		const image = "openbao/openbao:2.7.1"

		BeforeAll(func() {
			path := os.Getenv("E2E_CROSS_CLUSTER_SOURCE_KUBECONFIG")
			sourceKind = os.Getenv("E2E_CROSS_CLUSTER_SOURCE_KIND")
			if path == "" && sourceKind == "" {
				Skip("requires a disposable source Kind cluster; see restore-cross-cluster.md")
			}
			Expect(path).NotTo(BeEmpty())
			Expect(sourceKind).To(HavePrefix("restore-source-"))
			configFile, err := clientcmd.LoadFromFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(configFile.CurrentContext).To(Equal("kind-" + sourceKind))
			Expect(crossClusterCommand("docker", "inspect", sourceKind+"-control-plane", "--format", `{{index .Config.Labels "io.x-k8s.kind.cluster"}}`)).To(Equal(sourceKind))
			Expect(strings.Fields(crossClusterCommand("kind", "get", "nodes", "--name", sourceKind))).To(Equal([]string{sourceKind + "-control-plane"}))

			sourceConfig, err = clientcmd.BuildConfigFromFlags("", path)
			Expect(err).NotTo(HaveOccurred())
			sourceConfig.Timeout = 5 * time.Second
			destinationConfig, err = ctrlconfig.GetConfig()
			Expect(err).NotTo(HaveOccurred())
			scheme := runtime.NewScheme()
			Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
			Expect(api.AddToScheme(scheme)).To(Succeed())
			sourceClient, err = client.New(sourceConfig, client.Options{Scheme: scheme})
			Expect(err).NotTo(HaveOccurred())
			destinationClient, err = client.New(destinationConfig, client.Options{Scheme: scheme})
			Expect(err).NotTo(HaveOccurred())

			By("proving the Kubernetes clusters have distinct identities and signing keys")
			sourceSystem, destinationSystem := &corev1.Namespace{}, &corev1.Namespace{}
			Expect(sourceClient.Get(ctx, client.ObjectKey{Name: "kube-system"}, sourceSystem)).To(Succeed())
			Expect(destinationClient.Get(ctx, client.ObjectKey{Name: "kube-system"}, destinationSystem)).To(Succeed())
			Expect(sourceSystem.UID).NotTo(Equal(destinationSystem.UID))
			var sourceKeys, destinationKeys string
			sourceIssuer, sourceKeys = crossClusterIssuer(ctx, sourceConfig)
			destinationIssuer, destinationKeys = crossClusterIssuer(ctx, destinationConfig)
			Expect(sourceIssuer).NotTo(Equal(destinationIssuer))
			Expect(sourceKeys).NotTo(Equal(destinationKeys))
			AddReportEntry("independent-control-planes", map[string]string{
				"sourceUID": string(sourceSystem.UID), "destinationUID": string(destinationSystem.UID),
				"sourceIssuer": sourceIssuer, "destinationIssuer": destinationIssuer,
				"sourceJWKSHash": sourceKeys, "destinationJWKSHash": destinationKeys,
			})

			sourceFW, err = framework.New(ctx, sourceClient, "cross-source", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			destinationFW, err = framework.New(ctx, destinationClient, "cross-recovery", operatorNamespace)
			Expect(err).NotTo(HaveOccurred())
			infrastructureNamespace = destinationFW.Namespace + "-infra"
			Expect(framework.EnsureRestrictedNamespace(ctx, destinationClient, infrastructureNamespace)).To(Succeed())
			Expect(ensureRustFS(ctx, destinationClient, destinationConfig)).To(Succeed())
			Expect(helpers.EnsureInfraBao(ctx, destinationConfig, destinationClient, helpers.InfraBaoConfig{
				Namespace: infrastructureNamespace, Name: "transit", Image: "openbao/openbao:2.6.4",
			})).To(Succeed())
			sealAddress := "https://transit." + infrastructureNamespace + ".svc:8200"
			result, err := helpers.ConfigureInfraBaoTransit(ctx, destinationConfig, destinationClient,
				infrastructureNamespace, "transit", image, sealAddress, "recovery")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Phase).To(Equal(corev1.PodSucceeded), result.Logs)
			token, err := helpers.ReadInfraBaoRootToken(ctx, destinationClient, infrastructureNamespace, "transit")
			Expect(err).NotTo(HaveOccurred())
			ca, err := helpers.ReadInfraBaoTLSCACert(ctx, destinationClient, infrastructureNamespace, "transit")
			Expect(err).NotTo(HaveOccurred())
			for _, f := range []*framework.Framework{sourceFW, destinationFW} {
				Expect(helpers.EnsureInfraBaoSealCredentialsSecret(ctx, f.Client, f.Namespace, "transit-auth", token, ca, nil)).To(Succeed())
				Expect(f.Client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "storage", Namespace: f.Namespace},
					Data: map[string][]byte{"accessKeyId": []byte(rustfsAccessKey), "secretAccessKey": []byte(rustfsSecretKey)},
				})).To(Succeed())
			}
			prepareMinimalDestinationNetwork(ctx, destinationClient, destinationFW.Namespace, infrastructureNamespace, rustfsName)
			setMinimalDestinationApproval(ctx, destinationClient, destinationFW.Namespace, true)

			By("making backup storage and Transit reachable independently of the source Kubernetes cluster")
			nodeIP := crossClusterNodeIP(ctx, destinationClient)
			storageService = crossClusterNodePort(ctx, destinationClient, rustfsName, "cross-source-storage", "rustfs-svc")
			sealService := crossClusterNodePort(ctx, destinationClient, infrastructureNamespace, "cross-source-transit", "transit")
			sourceAPI := &corev1.Service{}
			Expect(sourceClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "kubernetes"}, sourceAPI)).To(Succeed())
			sourceSeal := &api.UnsealConfig{
				Type:                 "transit",
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "transit-auth"},
				Transit: &api.TransitSealConfig{
					Address:       fmt.Sprintf("https://%s:%d", nodeIP, sealService.Spec.Ports[0].NodePort),
					MountPath:     "transit",
					KeyName:       "recovery",
					TLSCACert:     "/etc/bao/seal-creds/ca.crt",
					TLSServerName: "transit." + infrastructureNamespace + ".svc",
				},
			}

			source = &api.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: sourceFW.Namespace},
				Spec: api.OpenBaoClusterSpec{
					Profile:           api.ProfileDevelopment,
					Version:           "2.7.1",
					Image:             image,
					Replicas:          1,
					ControllerJWTMode: api.ControllerJWTModeTarget,
					InitContainer:     &api.InitContainerConfig{Enabled: true, Image: configInitImage},
					Storage:           api.StorageConfig{Size: "1Gi"},
					TLS:               api.TLSConfig{Enabled: true, Mode: api.TLSModeOperatorManaged, RotationPeriod: "720h"},
					Unseal:            sourceSeal,
					DeletionPolicy:    api.DeletionPolicyDeleteAll,
					Network: &api.NetworkConfig{
						APIServerCIDR:        sourceAPI.Spec.ClusterIP + "/32",
						APIServerEndpointIPs: []string{crossClusterNodeIP(ctx, sourceClient)},
						EgressRules: []networkingv1.NetworkPolicyEgressRule{{
							To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: nodeIP + "/32"}}},
							Ports: []networkingv1.NetworkPolicyPort{
								{Port: &intstr.IntOrString{Type: intstr.Int, IntVal: sealService.Spec.Ports[0].NodePort}},
								{Port: &intstr.IntOrString{Type: intstr.Int, IntVal: storageService.Spec.Ports[0].NodePort}},
							},
						}},
					},
					SelfInit: &api.SelfInitConfig{
						Enabled:  true,
						OIDC:     &api.SelfInitOIDCConfig{Enabled: true},
						Requests: crossClusterAdminRequests(password),
					},
					Backup: &api.BackupSchedule{
						Schedule: "0 0 1 1 *",
						Image:    backupExecutorImage,
						Target: api.BackupTarget{
							Provider:             "s3",
							Endpoint:             fmt.Sprintf("http://%s:%d", nodeIP, storageService.Spec.Ports[0].NodePort),
							Bucket:               rustfsBucket,
							PathPrefix:           sourceFW.Namespace,
							UsePathStyle:         true,
							CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage"},
						},
					},
				},
			}
			Expect(sourceClient.Create(ctx, source)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(sourceClient.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
				g.Expect(meta.IsStatusConditionTrue(source.Status.Conditions, string(api.ConditionAvailable))).To(BeTrue())
			}, 5*time.Minute, 2*time.Second).Should(Succeed())
			Expect(triggerManualBackup(ctx, sourceClient, source.Namespace, source.Name)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(sourceClient.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
				g.Expect(source.Status.Backup).NotTo(BeNil())
				g.Expect(source.Status.Backup.LatestSnapshot).NotTo(BeNil())
			}, 5*time.Minute, 2*time.Second).Should(Succeed())
			summary := source.Status.Backup.LatestSnapshot
			snapshot = api.RestoreSource{Target: *source.Spec.Backup.Target.DeepCopy(), Key: source.Status.Backup.LastBackupName,
				ExpectedClusterID: summary.ClusterID, ExpectedVersion: summary.Version, ExpectedDigest: summary.Digest, ExpectedSize: summary.Size}
			snapshot.Target.Endpoint = rustfsEndpoint
			template = &api.RestoreClusterTemplate{Version: source.Spec.Version, Image: source.Spec.Image,
				Storage: source.Spec.Storage, TLS: source.Spec.TLS, InitContainer: source.Spec.InitContainer.DeepCopy(), Unseal: *sourceSeal.DeepCopy()}
			template.Unseal.Transit.Address = sealAddress
			AddReportEntry("pinned-source-snapshot", snapshot)

			By("stopping the disposable source control plane and its workloads after backup")
			crossClusterCommand("docker", "stop", sourceKind+"-control-plane")
			stopped = true
			Expect(crossClusterCommand("docker", "inspect", sourceKind+"-control-plane", "--format", "{{.State.Running}}")).To(Equal("false"))
			Expect(sourceClient.Get(ctx, client.ObjectKey{Name: "kube-system"}, &corev1.Namespace{})).NotTo(Succeed())
		})

		newRequest := func(name string, lifecycle api.RestoreTargetLifecycle) *api.OpenBaoRestore {
			return &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: destinationFW.Namespace}, Spec: api.OpenBaoRestoreSpec{
				Cluster: name, Source: *snapshot.DeepCopy(), ClusterTemplate: template.DeepCopy(),
				TargetLifecycle: lifecycle, Force: true, Image: backupExecutorImage,
			}}
		}

		It("confirms and cleans a disposable restore while the source Kubernetes cluster is stopped", Label("case:restore-cross-cluster-disposable"), func() {
			request := newRequest("disposable", api.RestoreTargetLifecycleDisposable)
			Expect(destinationClient.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseCompleted), request.Status.Message)
				g.Expect(request.Status.Target.AppliedAt).NotTo(BeNil())
				g.Expect(request.Status.Target.Cleanup).To(Equal(api.RestoreTargetCleanupComplete))
			}, 8*time.Minute, 2*time.Second).Should(Succeed())
			Expect(request.Status.Target.BootstrapClusterID).NotTo(Equal(snapshot.ExpectedClusterID))
			Expect(request.Status.SubmissionClaim.Digest).To(Equal(snapshot.ExpectedDigest))
			for _, object := range []client.Object{&api.OpenBaoCluster{}, &corev1.PersistentVolumeClaim{}} {
				name := request.Spec.Cluster
				if _, pvc := object.(*corev1.PersistentVolumeClaim); pvc {
					name = "data-" + name + "-0"
				}
				Expect(apierrors.IsNotFound(destinationClient.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: name}, object))).To(BeTrue())
			}
			Expect(deleteMinimalFixtureObject(ctx, destinationClient, request)).To(Succeed())
		})

		It("retains restored data and resumes after administrator repair of destination JWT trust", Label("case:restore-cross-cluster-retain"), func() {
			request := newRequest("retained", api.RestoreTargetLifecycleRetain)
			Expect(destinationClient.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Target).NotTo(BeNil())
				g.Expect(request.Status.Target.AppliedAt).NotTo(BeNil(), request.Status.Message)
			}, 8*time.Minute, 2*time.Second).Should(Succeed())
			target := &api.OpenBaoCluster{}
			Expect(destinationClient.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, target)).To(Succeed())
			originalPod := &corev1.Pod{}
			Expect(destinationClient.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name + "-0"}, originalPod)).To(Succeed())
			admin := crossClusterAdministrator(ctx, destinationClient, target, password)
			Eventually(admin.login, time.Minute, time.Second).Should(Succeed())
			data, err := admin.read("secret/recovery-proof")
			Expect(err).NotTo(HaveOccurred())
			Expect(data["value"]).To(Equal("from-source-snapshot"))
			config, err := admin.read("auth/jwt-operator/config")
			Expect(err).NotTo(HaveOccurred())
			Expect(config["bound_issuer"]).To(Equal(sourceIssuer))

			By("showing Resume retains the hold while restored source trust rejects the destination controller")
			before := request.DeepCopy()
			request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/Resume"}
			Expect(destinationClient.Patch(ctx, request, client.MergeFrom(before))).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				condition := meta.FindStatusCondition(request.Status.Conditions, constants.RestoreRecoveryReleasedConditionType)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Reason).To(Equal("OperatorAccessUnavailable"), request.Status.Message)
				g.Expect(request.Status.Restart).To(BeNil())
				g.Expect(request.Status.AdministratorDisposition).To(BeEmpty())
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(target), target)).To(Succeed())
				g.Expect(target.Annotations[constants.AnnotationRestoreHold]).To(Equal(string(request.UID)))
				pod := &corev1.Pod{}
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(originalPod), pod)).To(Succeed())
				g.Expect(pod.UID).To(Equal(originalPod.UID))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("repairing issuer, signing-key trust, controller audience, and lifecycle subjects through independent administrator access")
			controller := controllerJWTPod(ctx, destinationClient)
			audience, err := portauth.ControllerJWTAudience(target, "")
			Expect(err).NotTo(HaveOccurred())
			keys, err := adapterauth.FetchJWKSKeys(ctx, destinationConfig, destinationConfig.Host+"/openid/v1/jwks")
			Expect(err).NotTo(HaveOccurred())
			// Kind requires authentication to read JWKS. The administrator imports
			// the destination public keys; the restored target needs no API token.
			admin.write("auth/jwt-operator/config", map[string]any{
				"bound_issuer": destinationIssuer, "jwt_validation_pubkeys": keys,
				"jwks_url": "", "jwks_ca_pem": "", "oidc_discovery_url": "",
			})
			admin.setAudiences([]string{audience})
			crossClusterRepairSubjects(admin, target, controller)
			clientset, err := kubernetes.NewForConfig(destinationConfig)
			Expect(err).NotTo(HaveOccurred())
			admin.expectJWT(controllerJWTFor(ctx, clientset, controller, audience), true)
			admin.expectJWT(controllerJWTFor(ctx, clientset, controller, "urn:openbao:controller:"+string(source.UID)), false)
			Eventually(func(g Gomega) {
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseCompleted), request.Status.Message)
				g.Expect(request.Status.AdministratorDisposition).To(Equal(api.RestoreAdministratorResume))
				g.Expect(request.Status.Restart).NotTo(BeNil())
				g.Expect(request.Status.Restart.CompletedAt).NotTo(BeNil())
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(target), target)).To(Succeed())
				g.Expect(target.Annotations[constants.AnnotationRestoreHold]).To(BeEmpty())
				g.Expect(target.Status.OperationLock).To(BeNil())
				pod := &corev1.Pod{}
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(originalPod), pod)).To(Succeed())
				g.Expect(pod.UID).NotTo(Equal(originalPod.UID))
			}, 5*time.Minute, 2*time.Second).Should(Succeed())

			By("confirming normal management keeps the resumed Pod and service registration works")
			resumedPod := &corev1.Pod{}
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(target), sts)).To(Succeed())
				g.Expect(sts.Spec.UpdateStrategy.Type).To(Equal(appsv1.RollingUpdateStatefulSetStrategyType))
				g.Expect(sts.Status.ObservedGeneration).To(Equal(sts.Generation))
				g.Expect(sts.Status.CurrentRevision).To(Equal(sts.Status.UpdateRevision))
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(originalPod), resumedPod)).To(Succeed())
				g.Expect(resumedPod.Labels["openbao-active"]).To(Equal("true"))
			}, time.Minute, time.Second).Should(Succeed())
			Consistently(func(g Gomega) {
				pod := &corev1.Pod{}
				g.Expect(destinationClient.Get(ctx, client.ObjectKeyFromObject(resumedPod), pod)).To(Succeed())
				g.Expect(pod.UID).To(Equal(resumedPod.UID))
				g.Expect(pod.DeletionTimestamp).To(BeNil())
				for _, status := range pod.Status.ContainerStatuses {
					g.Expect(status.RestartCount).To(BeZero())
					g.Expect(status.Ready).To(BeTrue())
				}
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			By("reading the restored data after managed restart with the source still unavailable")
			restarted := crossClusterAdministrator(ctx, destinationClient, target, password)
			Eventually(restarted.login, time.Minute, time.Second).Should(Succeed())
			data, err = restarted.read("secret/recovery-proof")
			Expect(err).NotTo(HaveOccurred())
			Expect(data["value"]).To(Equal("from-source-snapshot"))
			Expect(destinationClient.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: "data-" + target.Name + "-0"}, &corev1.PersistentVolumeClaim{})).To(Succeed())
			Expect(crossClusterCommand("docker", "inspect", sourceKind+"-control-plane", "--format", "{{.State.Running}}")).To(Equal("false"))
			Expect(deleteMinimalFixtureObject(ctx, destinationClient, request)).To(Succeed())
		})

		AfterAll(func() {
			if stopped {
				crossClusterCommand("docker", "start", sourceKind+"-control-plane")
			}
			if CurrentSpecReport().Failed() {
				skipCleanup = true
				return
			}
			if destinationFW != nil {
				Expect(cleanupMinimalFixtures(ctx, destinationClient, destinationFW)).To(Succeed())
			}
			if storageService != nil {
				Expect(destinationClient.Delete(ctx, storageService)).To(Succeed())
			}
			if infrastructureNamespace != "" {
				Expect(deleteMinimalFixtureObject(ctx, destinationClient, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: infrastructureNamespace}})).To(Succeed())
			}
		})
	})

func crossClusterCommand(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput() // #nosec G204 -- explicit local disposable Kind fixture
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "%s: %s", name, out)
	return strings.TrimSpace(string(out))
}

func crossClusterIssuer(ctx context.Context, config *rest.Config) (string, string) {
	c, err := kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())
	discovery, err := c.CoreV1().RESTClient().Get().AbsPath("/.well-known/openid-configuration").DoRaw(ctx)
	Expect(err).NotTo(HaveOccurred())
	var document struct{ Issuer string }
	Expect(json.Unmarshal(discovery, &document)).To(Succeed())
	keys, err := c.CoreV1().RESTClient().Get().AbsPath("/openid/v1/jwks").DoRaw(ctx)
	Expect(err).NotTo(HaveOccurred())
	return document.Issuer, fmt.Sprintf("%x", sha256.Sum256(keys))
}

func crossClusterNodeIP(ctx context.Context, c client.Client) string {
	nodes := &corev1.NodeList{}
	Expect(c.List(ctx, nodes)).To(Succeed())
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				return address.Address
			}
		}
	}
	Fail("recovery cluster has no internal node address")
	return ""
}

func crossClusterNodePort(ctx context.Context, c client.Client, namespace, name, existing string) *corev1.Service {
	original := &corev1.Service{}
	Expect(c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: existing}, original)).To(Succeed())
	port := original.Spec.Ports[0]
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeNodePort, Selector: original.Spec.Selector,
		Ports: []corev1.ServicePort{{Name: port.Name, Port: port.Port, TargetPort: intstr.FromInt32(port.Port)}},
	}}
	Expect(c.Create(ctx, service)).To(Succeed())
	return service
}

func crossClusterAdminRequests(password string) []api.SelfInitRequest {
	return []api.SelfInitRequest{
		{Name: "admin-policy", Operation: api.SelfInitOperationUpdate, Path: "sys/policies/acl/jwt-validation-admin",
			Policy: &api.SelfInitPolicy{Policy: `path "*" { capabilities = ["create", "read", "update", "delete", "list", "sudo"] }`}},
		{Name: "admin-auth", Operation: api.SelfInitOperationUpdate, Path: "sys/auth/userpass", AuthMethod: &api.SelfInitAuthMethod{Type: "userpass"}},
		{Name: "admin-user", Operation: api.SelfInitOperationUpdate, Path: "auth/userpass/users/validator",
			Data: helpers.MustJSON(map[string]any{"password": password, "token_policies": []string{"jwt-validation-admin"}, "token_ttl": "1h"})},
		{Name: "validation-engine", Operation: api.SelfInitOperationUpdate, Path: "sys/mounts/secret", SecretEngine: &api.SelfInitSecretEngine{Type: "kv"}},
		{Name: "validation-data", Operation: api.SelfInitOperationUpdate, Path: "secret/recovery-proof", Data: helpers.MustJSON(map[string]any{"value": "from-source-snapshot"})},
	}
}

func crossClusterAdministrator(ctx context.Context, c client.Client, target *api.OpenBaoCluster, password string) *controllerJWTAPI {
	ca := &corev1.Secret{}
	Expect(c.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name + "-tls-ca"}, ca)).To(Succeed())
	roots := x509.NewCertPool()
	Expect(roots.AppendCertsFromPEM(ca.Data["ca.crt"])).To(BeTrue())
	address, stop, err := startTLSPodPortForward(target.Namespace, target.Name+"-0")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(stop)
	admin := &controllerJWTAPI{ctx: ctx, address: "https://" + address, password: password,
		http: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, ServerName: "openbao-cluster-" + target.Name + ".local", MinVersion: tls.VersionTLS12,
		}}},
	}
	DeferCleanup(admin.http.CloseIdleConnections)
	return admin
}

func crossClusterRepairSubjects(admin *controllerJWTAPI, target *api.OpenBaoCluster, controller corev1.Pod) {
	for _, kind := range []string{"operator", "backup", "restore", "upgrade"} {
		path := "auth/jwt-operator/role/openbao-operator"
		subject := "system:serviceaccount:" + controller.Namespace + ":" + controller.Spec.ServiceAccountName
		if kind != "operator" {
			path += "-" + kind
			subject = "system:serviceaccount:" + target.Namespace + ":" + target.Name + "-" + kind + "-serviceaccount"
		}
		role, err := admin.read(path)
		Expect(err).NotTo(HaveOccurred())
		role["bound_subject"] = subject
		if claims, ok := role["bound_claims"].(map[string]any); ok && claims["sub"] != nil {
			claims["sub"] = []string{subject}
		}
		admin.write(path, role)
	}
}
