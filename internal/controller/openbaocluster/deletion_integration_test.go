//go:build integration
// +build integration

package openbaocluster

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

var _ = Describe("OpenBaoCluster Deletion", func() {
	Context("When reconciling deletion flows", func() {
		ctx := context.Background()

		newReconciler := func() *testCompositeReconciler {
			parent := &OpenBaoClusterReconciler{
				Client: k8sClient,
				ControllerRuntime: ControllerRuntime{
					APIReader: k8sClient,
				},
				Applications: newTestOpenBaoClusterApplications(testApplicationsOptions{}),
			}
			return &testCompositeReconciler{parent: parent}
		}

		createMinimalCluster := func(name string) *openbaov1alpha1.OpenBaoCluster {
			ensureTenantNamespaceProvisioned(ctx, "default")
			cluster := &openbaov1alpha1.OpenBaoCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: openbaov1alpha1.OpenBaoClusterSpec{
					Version:  "2.4.4",
					Image:    "openbao/openbao:2.4.4",
					Replicas: 3,
					Profile:  openbaov1alpha1.ProfileDevelopment,
					TLS: openbaov1alpha1.TLSConfig{
						Enabled:        true,
						RotationPeriod: "720h",
					},
					Storage: openbaov1alpha1.StorageConfig{
						Size: "10Gi",
					},
					InitContainer: &openbaov1alpha1.InitContainerConfig{
						Image: "openbao/openbao-init:latest",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			return cluster
		}

		AfterEach(func() {
			var clusterList openbaov1alpha1.OpenBaoClusterList
			err := k8sClient.List(ctx, &clusterList)
			Expect(err).NotTo(HaveOccurred())
			for i := range clusterList.Items {
				_ = k8sClient.Delete(ctx, &clusterList.Items[i])
			}
		})

		It("honors DeletionPolicy Retain by preserving PVCs and verifying GC cleanup via OwnerReferences", func() {
			cluster := createMinimalCluster("test-delete-retain")
			cluster.Spec.DeletionPolicy = openbaov1alpha1.DeletionPolicyRetain
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}

			reconciler := newReconciler()

			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			}, sts)
			Expect(err).NotTo(HaveOccurred())

			foundOwnerRef := false
			for _, ref := range sts.OwnerReferences {
				if ref.Kind == "OpenBaoCluster" && ref.Name == cluster.Name {
					foundOwnerRef = true
					break
				}
			}
			Expect(foundOwnerRef).To(BeTrue(), "StatefulSet should have OwnerReference for GC cleanup")

			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "data-test-delete-retain-0",
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						"openbao.org/cluster": cluster.Name,
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			result, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(k8sClient.Get(ctx, req.NamespacedName, cluster)).To(Succeed())
			Expect(cluster.Finalizers).To(ContainElement(openbaov1alpha1.OpenBaoClusterFinalizer))
			Expect(k8sClient.Get(ctx, req.NamespacedName, sts)).To(Succeed())
			Expect(sts.DeletionTimestamp).NotTo(BeNil())
			Expect(sts.Finalizers).To(ContainElement(metav1.FinalizerDeleteDependents))
			// Envtest has no garbage collector. Complete its foreground
			// StatefulSet deletion after observing the held cluster finalizer.
			sts.Finalizers = nil
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      pvc.Name,
				Namespace: pvc.Namespace,
			}, &corev1.PersistentVolumeClaim{})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
