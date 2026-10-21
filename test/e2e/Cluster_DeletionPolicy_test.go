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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	e2ehelpers "github.com/kubebao/openbao-operator/test/e2e/helpers"
)

func hasOwnerReferenceWithUID(obj metav1.Object, uid types.UID) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

var _ = Describe("Cluster Lifecycle: Deletion Policy", Label("lifecycle", "cluster", "deletion"), Ordered, func() {
	ctx := context.Background()

	type deletionExpectations struct {
		retainPVC       bool
		retainUnsealKey bool
		retainRootToken bool
	}

	var (
		f               *framework.Framework
		c               client.Client
		runAsController func(func(client.Client) error) error
	)

	waitForStatefulSetCreated := func(key types.NamespacedName) {
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(c.Get(ctx, key, sts)).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	}

	waitForOwnedSecret := func(key types.NamespacedName, clusterUID types.UID) {
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			g.Expect(c.Get(ctx, key, secret)).To(Succeed())
			g.Expect(hasOwnerReferenceWithUID(secret, clusterUID)).To(
				BeTrue(),
				"expected Secret %q to be owned by the cluster before deletion",
				key.Name,
			)
		}, 10*time.Minute, framework.DefaultPollInterval).Should(Succeed())
	}

	assertSecretDeleted := func(key types.NamespacedName) {
		Eventually(func() bool {
			err := c.Get(ctx, key, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(
			BeTrue(),
			"expected Secret %q to be deleted",
			key.Name,
		)
	}

	assertSecretRetainedAndOrphaned := func(key types.NamespacedName, clusterUID types.UID) {
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			g.Expect(c.Get(ctx, key, secret)).To(Succeed())
			g.Expect(secret.OwnerReferences).To(
				BeEmpty(),
				"expected retained Secret %q to be orphaned during finalization",
				key.Name,
			)
			g.Expect(hasOwnerReferenceWithUID(secret, clusterUID)).To(
				BeFalse(),
				"expected retained Secret %q to no longer reference the deleted cluster",
				key.Name,
			)
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
	}

	assertPVCState := func(key types.NamespacedName, wantRetained bool) {
		if wantRetained {
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, key, &corev1.PersistentVolumeClaim{})).To(Succeed())
			}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(
				Succeed(),
				"expected PVC %q to be retained",
				key.Name,
			)
			return
		}

		Eventually(func() bool {
			err := c.Get(ctx, key, &corev1.PersistentVolumeClaim{})
			return apierrors.IsNotFound(err)
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(
			BeTrue(),
			"expected PVC %q to be deleted",
			key.Name,
		)
	}

	assertStatefulSetDeleted := func(key types.NamespacedName) {
		Eventually(func() bool {
			err := c.Get(ctx, key, &appsv1.StatefulSet{})
			return apierrors.IsNotFound(err)
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(
			BeTrue(),
			"expected StatefulSet %q to be deleted",
			key.Name,
		)
	}

	assertClusterDeleted := func(key types.NamespacedName) {
		Eventually(func() bool {
			err := c.Get(ctx, key, &openbaov1alpha1.OpenBaoCluster{})
			return apierrors.IsNotFound(err)
		}, framework.DefaultLongWaitTimeout, framework.DefaultPollInterval).Should(
			BeTrue(),
			"expected OpenBaoCluster %q to be fully deleted",
			key.Name,
		)
	}

	assertDeletionPolicy := func(clusterName string, policy openbaov1alpha1.DeletionPolicy, want deletionExpectations) {
		By(fmt.Sprintf("creating development cluster %q with deletionPolicy=%s", clusterName, policy))
		cluster, err := f.CreateDevelopmentCluster(ctx, framework.DevelopmentClusterConfig{
			Name:                 clusterName,
			Replicas:             1,
			Version:              openBaoVersion,
			Image:                openBaoImage,
			ConfigInitImg:        configInitImage,
			APIServerCIDR:        apiServerCIDR,
			APIServerEndpointIPs: apiServerEndpointIPs,
			DeletionPolicy:       policy,
		})
		Expect(err).NotTo(HaveOccurred())

		clusterKey := types.NamespacedName{Name: clusterName, Namespace: f.Namespace}
		statefulSetKey := clusterKey
		dataPVCKey := types.NamespacedName{Name: fmt.Sprintf("data-%s-0", clusterName), Namespace: f.Namespace}
		unsealKey := types.NamespacedName{Name: clusterName + "-unseal-key", Namespace: f.Namespace}
		rootTokenKey := types.NamespacedName{Name: clusterName + "-root-token", Namespace: f.Namespace}
		tlsCAKey := types.NamespacedName{Name: clusterName + "-tls-ca", Namespace: f.Namespace}
		tlsServerKey := types.NamespacedName{Name: clusterName + "-tls-server", Namespace: f.Namespace}

		By("waiting for the controller to add the cluster finalizer")
		Eventually(func(g Gomega) {
			current := &openbaov1alpha1.OpenBaoCluster{}
			g.Expect(c.Get(ctx, clusterKey, current)).To(Succeed())
			g.Expect(current.Finalizers).To(ContainElement(openbaov1alpha1.OpenBaoClusterFinalizer))
			cluster = current
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		clusterUID := cluster.UID

		By("waiting for stateful resources and recoverability secrets to be created")
		waitForStatefulSetCreated(statefulSetKey)
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, dataPVCKey, &corev1.PersistentVolumeClaim{})).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
		waitForOwnedSecret(unsealKey, clusterUID)
		waitForOwnedSecret(rootTokenKey, clusterUID)

		By("waiting for TLS Secrets so deletion can assert they are garbage collected")
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, tlsCAKey, &corev1.Secret{})).To(Succeed())
			g.Expect(c.Get(ctx, tlsServerKey, &corev1.Secret{})).To(Succeed())
		}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())

		By("deleting the OpenBaoCluster and waiting for finalization to complete")
		if policy == openbaov1alpha1.DeletionPolicyRetain {
			By("holding operator finalization while real foreground GC processes the parent")
			setRestoreHold := func(hold string) {
				Eventually(func() error {
					return runAsController(func(controller client.Client) error {
						current := &openbaov1alpha1.OpenBaoCluster{}
						if err := controller.Get(ctx, clusterKey, current); err != nil {
							return err
						}
						if current.Annotations == nil {
							current.Annotations = map[string]string{}
						}
						if hold == "" {
							delete(current.Annotations, constants.AnnotationRestoreHold)
						} else {
							current.Annotations[constants.AnnotationRestoreHold] = hold
						}
						return controller.Update(ctx, current)
					})
				}, framework.DefaultWaitTimeout, framework.DefaultPollInterval).Should(Succeed())
			}
			Expect(c.Get(ctx, clusterKey, cluster)).To(Succeed())
			original := cluster.DeepCopy()
			if cluster.Annotations == nil {
				cluster.Annotations = map[string]string{}
			}
			cluster.Annotations[constants.AnnotationRestoreHold] = "e2e-foreground-retention"
			Expect(c.Patch(ctx, cluster, client.MergeFrom(original), client.DryRunAll)).To(
				MatchError(ContainSubstring("Only the controller can change a restore hold")),
			)
			setRestoreHold("e2e-foreground-retention")
			Expect(c.Get(ctx, clusterKey, cluster)).To(Succeed())
			Expect(c.Delete(ctx, cluster, client.DryRunAll)).To(
				MatchError(ContainSubstring("Only the controller can change a restore hold")),
			)
			Expect(runAsController(func(controller client.Client) error {
				return controller.Delete(ctx, cluster, client.PropagationPolicy(metav1.DeletePropagationForeground))
			})).To(Succeed())
			// TLS deletion proves real GC has processed the foreground owner.
			// The operator cannot orphan recovery Secrets while its restore hold remains.
			assertSecretDeleted(tlsCAKey)
			assertSecretDeleted(tlsServerKey)
			Consistently(func(g Gomega) {
				for _, key := range []types.NamespacedName{unsealKey, rootTokenKey} {
					secret := &corev1.Secret{}
					g.Expect(c.Get(ctx, key, secret)).To(Succeed())
					g.Expect(secret.DeletionTimestamp.IsZero()).To(BeTrue())
					g.Expect(hasOwnerReferenceWithUID(secret, clusterUID)).To(BeTrue())
				}
			}, 5*time.Second, time.Second).Should(Succeed())
			setRestoreHold("")
		} else {
			Expect(c.Delete(ctx, cluster)).To(Succeed())
		}
		assertClusterDeleted(clusterKey)
		assertStatefulSetDeleted(statefulSetKey)

		By("verifying PVC cleanup matches the deletion policy")
		assertPVCState(dataPVCKey, want.retainPVC)

		By("verifying recoverability secret cleanup matches the deletion policy")
		if want.retainUnsealKey {
			assertSecretRetainedAndOrphaned(unsealKey, clusterUID)
		} else {
			assertSecretDeleted(unsealKey)
		}
		if want.retainRootToken {
			assertSecretRetainedAndOrphaned(rootTokenKey, clusterUID)
		} else {
			assertSecretDeleted(rootTokenKey)
		}

		By("verifying TLS Secrets are garbage collected for all deletion policies")
		assertSecretDeleted(tlsCAKey)
		assertSecretDeleted(tlsServerKey)
	}

	BeforeAll(func() {
		var err error
		f, err = framework.NewSetup(ctx, "cluster-delete", operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		c = f.Client
		cfg, err := ctrlconfig.GetConfig()
		Expect(err).NotTo(HaveOccurred())
		deployment, err := getControllerDeployment(ctx, c, operatorNamespace)
		Expect(err).NotTo(HaveOccurred())
		serviceAccount := deployment.Spec.Template.Spec.ServiceAccountName
		Expect(serviceAccount).NotTo(BeEmpty())
		runAsController = func(action func(client.Client) error) error {
			return e2ehelpers.RunWithImpersonation(ctx, cfg, c.Scheme(),
				fmt.Sprintf("system:serviceaccount:%s:%s", deployment.Namespace, serviceAccount),
				[]string{"system:serviceaccounts", "system:serviceaccounts:" + deployment.Namespace, "system:authenticated"},
				action)
		}
	})

	AfterAll(func() {
		if f == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		_ = f.Cleanup(cleanupCtx)
	})

	It("retains PVCs and recoverability secrets when policy is Retain", Label(
		"case:deletion-policy-retain",
		"covers:deletion-policy",
		"covers:pvc-retention",
		"covers:recoverability-secret-retention",
	), func() {
		assertDeletionPolicy("retain-cluster", openbaov1alpha1.DeletionPolicyRetain, deletionExpectations{
			retainPVC:       true,
			retainUnsealKey: true,
			retainRootToken: true,
		})
	})

	It("deletes PVCs and Secrets when policy is DeletePVCs", Label(
		"case:deletion-policy-delete-pvcs",
		"covers:deletion-policy",
		"covers:pvc-cleanup",
		"covers:recoverability-secret-cleanup",
	), func() {
		assertDeletionPolicy("delete-pvcs-cluster", openbaov1alpha1.DeletionPolicyDeletePVCs, deletionExpectations{})
	})

	It("deletes PVCs and Secrets when policy is DeleteAll", Label(
		"case:deletion-policy-delete-all",
		"covers:deletion-policy",
		"covers:pvc-cleanup",
		"covers:recoverability-secret-cleanup",
		"covers:tls-secret-cleanup",
	), func() {
		assertDeletionPolicy("delete-all-cluster", openbaov1alpha1.DeletionPolicyDeleteAll, deletionExpectations{})
	})
})
