//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubebao/openbao-operator/test/e2e/framework"
	"github.com/kubebao/openbao-operator/test/utils"
)

var _ = Describe("OpenShift Platform", Label("openshift", "platform"), Ordered, func() {
	var (
		k8sClient client.Client
		f         *framework.Framework
	)

	BeforeAll(func() {
		if !utils.IsOpenShiftCluster() {
			Skip("cluster is not OpenShift (security.openshift.io API group not detected)")
		}

		cfg, scheme, err := buildSuiteClientConfig()
		Expect(err).NotTo(HaveOccurred())

		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		if f != nil {
			_ = f.Cleanup(f.Ctx)
		}
	})

	It("does not pin runAsUser/runAsGroup in operator Deployments", Label(
		"case:platform-openshift-does-not-pin-runasuser-runasgroup-in-ee682f26",
	), func() {
		ctx := context.Background()
		names := []types.NamespacedName{
			{Namespace: operatorNamespace, Name: "openbao-operator-controller"},
			{Namespace: operatorNamespace, Name: "openbao-operator-provisioner"},
		}

		for _, nn := range names {
			deploy := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, nn, deploy)
			if apierrors.IsNotFound(err) {
				// Provisioner is not deployed in single-tenant mode; controller is always expected.
				if nn.Name == "openbao-operator-provisioner" {
					continue
				}
			}
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("failed to get Deployment %s/%s", nn.Namespace, nn.Name))
			Expect(deploy.Spec.Template.Spec.Containers).NotTo(BeEmpty())

			sc := deploy.Spec.Template.Spec.Containers[0].SecurityContext
			if sc == nil {
				continue
			}

			Expect(sc.RunAsUser).To(BeNil(), "operator Deployment must not pin runAsUser on OpenShift")
			Expect(sc.RunAsGroup).To(BeNil(), "operator Deployment must not pin runAsGroup on OpenShift")
		}
	})

	It("creates StatefulSet pod template without pinned UID/GID/FSGroup", Label(
		"case:platform-openshift-creates-statefulset-pod-template-without-pinned-30198dd7",
	), func() {
		ctx := context.Background()
		var err error
		f, err = framework.New(ctx, k8sClient, "openshift-platform", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())

		clusterName := "cluster"
		By("creating a development cluster on OpenShift")
		_, err = f.CreateDevelopmentCluster(ctx, framework.DevelopmentClusterConfig{
			Name:                 clusterName,
			Replicas:             1,
			Version:              openBaoVersion,
			Image:                openBaoImage,
			ConfigInitImg:        configInitImage,
			APIServerCIDR:        apiServerCIDR,
			APIServerEndpointIPs: apiServerEndpointIPs,
		})
		Expect(err).NotTo(HaveOccurred())

		By("verifying the StatefulSet pod template leaves UID, GID, and FSGroup unpinned")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: f.Namespace}, sts)
			if apierrors.IsNotFound(err) {
				g.Expect(err).NotTo(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())

			podSC := sts.Spec.Template.Spec.SecurityContext
			g.Expect(podSC).NotTo(BeNil())
			g.Expect(podSC.RunAsNonRoot).To(Equal(ptrBool(true)))
			g.Expect(podSC.RunAsUser).To(BeNil())
			g.Expect(podSC.RunAsGroup).To(BeNil())
			g.Expect(podSC.FSGroup).To(BeNil())

			// Spot-check container-level hardening exists.
			g.Expect(sts.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			csc := sts.Spec.Template.Spec.Containers[0].SecurityContext
			if csc != nil {
				g.Expect(csc.AllowPrivilegeEscalation).ToNot(Equal(ptrBool(true)))
			}
		}, "2m", "2s").Should(Succeed())
	})
})

func ptrBool(v bool) *bool { return &v }
