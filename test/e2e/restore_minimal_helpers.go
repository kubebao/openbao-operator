//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	helpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

// Keep both namespaces enrolled until requests and clusters finish deletion.
// Never acknowledge an uncertain restore or remove a finalizer during teardown.
func cleanupMinimalFixtures(ctx context.Context, c client.Client, fixtures ...*framework.Framework) error {
	// Stop new runs before listing requests. Source deletion still cancels any
	// reservation whose child creation overlaps this initial observation.

	for _, f := range fixtures {
		if f == nil {
			continue
		}
		clusters := &api.OpenBaoClusterList{}
		if err := c.List(ctx, clusters, client.InNamespace(f.Namespace)); err != nil {
			return err
		}
		for i := range clusters.Items {
			cluster := &clusters.Items[i]
			if cluster.Spec.Backup == nil || cluster.Spec.Backup.RestoreTest == nil {
				continue
			}
			before := cluster.DeepCopy()
			cluster.Spec.Backup.RestoreTest = nil
			if err := c.Patch(ctx, cluster, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				return err
			}
		}
	}

	for _, f := range fixtures {
		if f == nil {
			continue
		}
		requests := &api.OpenBaoRestoreList{}
		if err := c.List(ctx, requests, client.InNamespace(f.Namespace)); err != nil {
			return err
		}
		for i := range requests.Items {
			if err := deleteMinimalFixtureObject(ctx, c, &requests.Items[i]); err != nil {
				return err
			}
		}
	}

	for _, f := range fixtures {
		if f == nil {
			continue
		}
		clusters := &api.OpenBaoClusterList{}
		if err := c.List(ctx, clusters, client.InNamespace(f.Namespace)); err != nil {
			return err
		}
		for i := range clusters.Items {
			if err := deleteMinimalFixtureObject(ctx, c, &clusters.Items[i]); err != nil {
				return err
			}
		}
	}

	for _, f := range fixtures {
		if f == nil {
			continue
		}
		tenant := &api.OpenBaoTenant{ObjectMeta: metav1.ObjectMeta{Name: f.TenantName, Namespace: f.OperatorNamespace}}
		if err := deleteMinimalFixtureObject(ctx, c, tenant); err != nil {
			return err
		}
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.Namespace}}
		if err := deleteMinimalFixtureObject(ctx, c, namespace); err != nil {
			return err
		}
	}

	return nil
}

func deleteMinimalFixtureObject(ctx context.Context, c client.Client, obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)
	expectedUID := obj.GetUID()
	if err := c.Get(ctx, key, obj); err != nil {
		return client.IgnoreNotFound(err)
	}

	uid := obj.GetUID()
	if expectedUID != "" && expectedUID != uid {
		return fmt.Errorf("fixture %T %s was replaced", obj, key)
	}

	if err := c.Delete(ctx, obj, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if err := wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if obj.GetUID() != uid {
			return false, fmt.Errorf("fixture %T %s was replaced during deletion", obj, key)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("wait for fixture %T %s deletion: %w", obj, key, err)
	}

	return nil
}

type minimalRestoreFixture struct {
	client                  client.Client
	config                  *rest.Config
	sourceFW, destinationFW *framework.Framework
	source                  *api.OpenBaoCluster
	template                *api.RestoreClusterTemplate
	snapshot                api.RestoreSource
	storageNamespace        *corev1.Namespace
}

func newMinimalRestoreFixture(ctx context.Context) *minimalRestoreFixture {
	const image = "openbao/openbao:2.7.1"
	f := &minimalRestoreFixture{}
	ginkgo.DeferCleanup(func() {
		if skipCleanup {
			return
		}
		if err := cleanupMinimalFixtures(ctx, f.client, f.sourceFW, f.destinationFW); err != nil {
			skipCleanup = true
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if f.storageNamespace != nil && f.storageNamespace.UID != "" {
			err := deleteMinimalFixtureObject(ctx, f.client, f.storageNamespace)
			if err != nil {
				skipCleanup = true
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	})
	var err error
	f.config, err = ctrlconfig.GetConfig()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	scheme := runtime.NewScheme()
	gomega.Expect(clientgoscheme.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(api.AddToScheme(scheme)).To(gomega.Succeed())
	f.client, err = client.New(f.config, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	apiService := &corev1.Service{}
	gomega.Expect(f.client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "kubernetes"}, apiService)).To(gomega.Succeed())
	gomega.Expect(apiService.Spec.IPFamilies).NotTo(gomega.BeEmpty())
	families := append([]corev1.IPFamily(nil), apiService.Spec.IPFamilies[:1]...)
	policy := corev1.IPFamilyPolicySingleStack
	f.sourceFW, err = framework.New(ctx, f.client, "minimal-source", operatorNamespace)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	f.destinationFW, err = framework.New(ctx, f.client, "minimal-destination", operatorNamespace)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	// RustFS runs as root. Keep its fixture namespace separate from the
	// restricted source and destination namespaces.
	f.storageNamespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.sourceFW.Namespace + "-storage"}}
	gomega.Expect(f.client.Create(ctx, f.storageNamespace)).To(gomega.Succeed())
	storage := minimalRestoreStorageConfig(f.storageNamespace.Name)
	gomega.Expect(helpers.EnsureRustFS(ctx, f.client, f.config, storage)).To(gomega.Succeed())

	ginkgo.By("preparing compatible Transit credentials in both namespaces")
	infra := helpers.InfraBaoConfig{
		Namespace: f.sourceFW.Namespace,
		Name:      "transit",
		// The infrastructure helper uses file storage, which 2.7 removes.
		// Source and restore targets still use the 2.7 image below.
		Image: "openbao/openbao:2.6.4",
	}
	gomega.Expect(helpers.EnsureInfraBao(ctx, f.config, f.client, infra)).To(gomega.Succeed())
	address := fmt.Sprintf("https://transit.%s.svc:8200", f.sourceFW.Namespace)
	result, err := helpers.ConfigureInfraBaoTransit(ctx, f.config, f.client, f.sourceFW.Namespace, "transit", image, address, "recovery")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(result.Phase).To(gomega.Equal(corev1.PodSucceeded), result.Logs)
	token, err := helpers.ReadInfraBaoRootToken(ctx, f.client, f.sourceFW.Namespace, "transit")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ca, err := helpers.ReadInfraBaoTLSCACert(ctx, f.client, f.sourceFW.Namespace, "transit")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, namespace := range []string{f.sourceFW.Namespace, f.destinationFW.Namespace} {
		gomega.Expect(helpers.EnsureInfraBaoSealCredentialsSecret(ctx, f.client, namespace, "transit-auth", token, ca, nil)).To(gomega.Succeed())
		gomega.Expect(f.client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "storage",
				Namespace: namespace,
			},

			Data: map[string][]byte{
				"accessKeyId":     []byte(rustfsAccessKey),
				"secretAccessKey": []byte(rustfsSecretKey),
			},
		})).To(gomega.Succeed())
	}
	prepareMinimalDestinationNetwork(ctx, f.client, f.destinationFW.Namespace, f.sourceFW.Namespace, f.storageNamespace.Name)
	setMinimalDestinationApproval(ctx, f.client, f.destinationFW.Namespace, true)

	requests := helpers.CreateE2ERequests(f.sourceFW.Namespace)
	f.source = &api.OpenBaoCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "source",
			Namespace: f.sourceFW.Namespace,
		},
		Spec: api.OpenBaoClusterSpec{
			Profile:  api.ProfileDevelopment,
			Version:  "2.7.1",
			Image:    image,
			Replicas: 1,
			InitContainer: &api.InitContainerConfig{
				Enabled: true,
				Image:   configInitImage,
			},
			Storage: api.StorageConfig{Size: "1Gi"},
			Service: &api.ServiceConfig{
				IPFamilyPolicy: ptr.To(corev1.IPFamilyPolicySingleStack),
				IPFamilies:     apiService.Spec.IPFamilies[:1],
			},
			TLS: api.TLSConfig{
				Enabled:        true,
				Mode:           api.TLSModeOperatorManaged,
				RotationPeriod: "720h",
			},
			SelfInit: &api.SelfInitConfig{
				Enabled:  true,
				OIDC:     &api.SelfInitOIDCConfig{Enabled: true},
				Requests: requests,
			},
			Unseal: &api.UnsealConfig{
				Type:                 "transit",
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "transit-auth"},
				Transit: &api.TransitSealConfig{
					Address:   address,
					MountPath: "transit",
					KeyName:   "recovery",
					TLSCACert: "/etc/bao/seal-creds/ca.crt",
				},
			},
			Network: &api.NetworkConfig{
				APIServerCIDR:        apiServerCIDR,
				APIServerEndpointIPs: apiServerEndpointIPs,
				APIServerEgress:      framework.APIServerEgressForNetworkProfile(),
				EgressRules: []networkingv1.NetworkPolicyEgressRule{{
					To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "transit"}}}},
					Ports: []networkingv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 8200)},
				}},
			},
			Backup: &api.BackupSchedule{
				Schedule: "0 0 1 1 *",
				Image:    backupExecutorImage,
				Target: api.BackupTarget{
					Provider:             "s3",
					Endpoint:             minimalRestoreStorageEndpoint(f.storageNamespace.Name),
					Bucket:               rustfsBucket,
					PathPrefix:           "minimal/" + f.sourceFW.Namespace,
					UsePathStyle:         true,
					CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage"},
				},
			},
			DeletionPolicy: api.DeletionPolicyDeleteAll,
		},
	}
	gomega.Expect(f.client.Create(ctx, f.source)).To(gomega.Succeed())
	gomega.Expect(f.client.Create(ctx, newBackupNetworkPolicy(f.sourceFW.Namespace, f.source.Name, f.storageNamespace.Name, 9000, "backup"))).To(gomega.Succeed())
	f.sourceFW.WaitForCondition(f.source.Name, api.ConditionAvailable, metav1.ConditionTrue)
	gomega.Expect(triggerManualBackup(ctx, f.client, f.source.Namespace, f.source.Name)).To(gomega.Succeed())
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(f.client.Get(ctx, client.ObjectKeyFromObject(f.source), f.source)).To(gomega.Succeed())
		g.Expect(f.source.Status.Backup).NotTo(gomega.BeNil())
		g.Expect(f.source.Status.Backup.LatestSnapshot).NotTo(gomega.BeNil())
	}, 5*time.Minute, 2*time.Second).Should(gomega.Succeed())
	summary := f.source.Status.Backup.LatestSnapshot
	f.snapshot = api.RestoreSource{
		Target: *f.source.Spec.Backup.Target.DeepCopy(),
		Key:    f.source.Status.Backup.LastBackupName,

		ExpectedClusterID: summary.ClusterID,
		ExpectedVersion:   summary.Version,
		ExpectedDigest:    summary.Digest,
		ExpectedSize:      summary.Size,
	}
	sourcePod := &corev1.Pod{}
	gomega.Expect(f.client.Get(ctx, client.ObjectKey{Namespace: f.source.Namespace, Name: f.source.Name + "-0"}, sourcePod)).To(gomega.Succeed())
	assertNetworkProfile(*sourcePod, os.Getenv("E2E_NETWORK_PROFILE"))
	if len(sourcePod.Status.PodIPs) == 2 {
		// The Kubernetes API Service can remain single-stack in a dual-stack
		// cluster. Use the actual Pod addresses to require both families.
		secondary := corev1.IPv6Protocol
		if families[0] == corev1.IPv6Protocol {
			secondary = corev1.IPv4Protocol
		}
		families = []corev1.IPFamily{secondary, families[0]}
		policy = corev1.IPFamilyPolicyRequireDualStack
	}
	f.template = &api.RestoreClusterTemplate{
		Version: "2.7.1",
		Image:   image,
		Storage: f.source.Spec.Storage,
		TLS:     f.source.Spec.TLS,
		Service: &api.RestoreTargetServiceConfig{IPFamilyPolicy: &policy, IPFamilies: families},

		InitContainer: f.source.Spec.InitContainer.DeepCopy(),
		Unseal:        *f.source.Spec.Unseal.DeepCopy(),
	}
	return f
}

func minimalRestoreStorageConfig(namespace string) helpers.RustFSConfig {
	config := helpers.DefaultRustFSConfig()
	config.Namespace, config.Name = namespace, rustfsName
	config.AccessKey, config.SecretKey = rustfsAccessKey, rustfsSecretKey
	config.Buckets = []string{rustfsBucket}
	return config
}

func minimalRestoreStorageEndpoint(namespace string) string {
	return fmt.Sprintf("http://%s-svc.%s.svc:9000", rustfsName, namespace)
}

func (f *minimalRestoreFixture) newRequest(name string, lifecycle api.RestoreTargetLifecycle) *api.OpenBaoRestore {
	return &api.OpenBaoRestore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.destinationFW.Namespace},
		Spec: api.OpenBaoRestoreSpec{
			Cluster: name, Source: *f.snapshot.DeepCopy(), ClusterTemplate: f.template.DeepCopy(),
			TargetLifecycle: lifecycle, Force: true, Image: backupExecutorImage,
		},
	}
}
