//go:build integration
// +build integration

package openbaocluster

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
)

var _ = Describe("OpenBaoCluster Reconcile", func() {
	Context("When reconciling cluster state", func() {
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

		createMinimalCluster := func(name string, paused bool) *openbaov1alpha1.OpenBaoCluster {
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
					Paused:   paused,
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

		It("adds a finalizer to new clusters", func() {
			cluster := createMinimalCluster("test-finalizer", false)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}

			_, err := newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			updated := &openbaov1alpha1.OpenBaoCluster{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			}, updated)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Finalizers).To(ContainElement(openbaov1alpha1.OpenBaoClusterFinalizer))
		})

		It("reconciles an unpaused cluster and updates status conditions", func() {
			cluster := createMinimalCluster("test-unpaused", false)
			cluster.Spec.SelfInit = &openbaov1alpha1.SelfInitConfig{
				Enabled: true,
			}
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}

			_, err := newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			_, err = newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			updated := &openbaov1alpha1.OpenBaoCluster{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			}, updated)
			Expect(err).NotTo(HaveOccurred())

			Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseInitializing))

			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			Expect(available).NotTo(BeNil())
			Expect(available.Status).To(Equal(metav1.ConditionFalse))
			Expect(available.Reason).To(Equal("NoReplicasReady"))

			degraded := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionDegraded))
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
			Expect(degraded.Reason).To(Equal("Reconciling"))

			upgrading := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionUpgrading))
			Expect(upgrading).NotTo(BeNil())
			Expect(upgrading.Status).To(Equal(metav1.ConditionFalse))

			backingUp := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionBackingUp))
			Expect(backingUp).NotTo(BeNil())
			Expect(backingUp.Status).To(Equal(metav1.ConditionFalse))

			tlsReady := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionTLSReady))
			Expect(tlsReady).NotTo(BeNil())
			Expect(tlsReady.Status).To(Equal(metav1.ConditionTrue))
			Expect(tlsReady.Reason).To(Equal("Ready"))
		})

		It("honors spec.paused and sets paused conditions", func() {
			cluster := createMinimalCluster("test-paused", true)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}

			_, err := newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			_, err = newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			updated := &openbaov1alpha1.OpenBaoCluster{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			}, updated)
			Expect(err).NotTo(HaveOccurred())

			Expect(updated.Status.Phase).To(Equal(openbaov1alpha1.ClusterPhaseInitializing))

			available := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionAvailable))
			Expect(available).NotTo(BeNil())
			Expect(available.Status).To(Equal(metav1.ConditionUnknown))
			Expect(available.Reason).To(Equal("Paused"))

			degraded := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionDegraded))
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
			Expect(degraded.Reason).To(Equal("Paused"))

			tlsReady := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionTLSReady))
			Expect(tlsReady).NotTo(BeNil())
			Expect(tlsReady.Status).To(Equal(metav1.ConditionUnknown))
			Expect(tlsReady.Reason).To(Equal("Paused"))
		})

		It("does not create workload resources when cluster is paused", func() {
			cluster := createMinimalCluster("test-paused-no-tls", true)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}

			_, err := newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			_, err = newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			caSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name + constants.SuffixTLSCA,
				Namespace: cluster.Namespace,
			}, caSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			serverSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name + constants.SuffixTLSServer,
				Namespace: cluster.Namespace,
			}, serverSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			statefulSet := &appsv1.StatefulSet{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			}, statefulSet)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			headlessService := &corev1.Service{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			}, headlessService)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			configMap := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name + constants.SuffixConfigMap,
				Namespace: cluster.Namespace,
			}, configMap)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("reports image version mismatches without mutating existing StatefulSet pods", func() {
			// Stay within one forwarding protocol generation so this fixture reaches
			// the image/tag check instead of the upgrade compatibility check.
			const (
				clusterName     = "test-image-version-mismatch"
				stableImage     = "openbao/openbao:2.6.4"
				mismatchedImage = "openbao/openbao:2.6.3"
				stableVersion   = "2.6.4"
				targetVersion   = "2.6.5"
			)

			cluster := createMinimalCluster(clusterName, false)
			cluster.Finalizers = append(cluster.Finalizers, openbaov1alpha1.OpenBaoClusterFinalizer)
			cluster.Spec.Version = targetVersion
			cluster.Spec.Image = mismatchedImage
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())

			cluster.Status.Initialized = true
			cluster.Status.CurrentVersion = stableVersion
			Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())

			labels := map[string]string{
				constants.LabelOpenBaoCluster:      cluster.Name,
				constants.LabelOpenBaoComponent:    constants.ComponentOpenBaoCluster,
				constants.LabelOpenBaoWorkloadPool: constants.LabelValueOpenBaoWorkloadPoolVoter,
			}
			replicas := int32(3)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: cluster.Name,
					Replicas:    &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: labels,
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: labels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  constants.ContainerBao,
									Image: stableImage,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, sts)
			})

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}
			parent := &OpenBaoClusterReconciler{
				Client: k8sClient,
				ControllerRuntime: ControllerRuntime{
					APIReader: k8sClient,
				},
				Applications: newTestOpenBaoClusterApplications(testApplicationsOptions{}),
			}

			result, err := (&openBaoClusterWorkloadReconciler{parent: parent}).Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			currentSTS := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, currentSTS)).To(Succeed())
			Expect(currentSTS.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			Expect(currentSTS.Spec.Template.Spec.Containers[0].Image).To(Equal(stableImage))

			updated := &openbaov1alpha1.OpenBaoCluster{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Workload).NotTo(BeNil())
			Expect(updated.Status.Workload.LastError).NotTo(BeNil())
			Expect(updated.Status.Workload.LastError.Reason).To(Equal(ReasonImageVersionMismatch))
			Expect(updated.Status.CurrentVersion).To(Equal(stableVersion))

			statusReconciler := &openBaoClusterStatusReconciler{parent: parent}
			_, err = statusReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = statusReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, req.NamespacedName, updated)).To(Succeed())
			degraded := meta.FindStatusCondition(updated.Status.Conditions, string(openbaov1alpha1.ConditionDegraded))
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal(ReasonImageVersionMismatch))
			Expect(updated.Status.CurrentVersion).To(Equal(stableVersion))
		})

		It("creates CA and server TLS Secrets for a new cluster", func() {
			cluster := createMinimalCluster("test-tls-secrets", false)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cluster.Name,
					Namespace: cluster.Namespace,
				},
			}

			_, err := newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			_, err = newReconciler().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			caSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name + constants.SuffixTLSCA,
				Namespace: cluster.Namespace,
			}, caSecret)
			Expect(err).NotTo(HaveOccurred())
			Expect(caSecret.Data).To(HaveKey("ca.crt"))
			Expect(caSecret.Data).To(HaveKey("ca.key"))

			serverSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      cluster.Name + constants.SuffixTLSServer,
				Namespace: cluster.Namespace,
			}, serverSecret)
			Expect(err).NotTo(HaveOccurred())
			Expect(serverSecret.Data).To(HaveKey("tls.crt"))
			Expect(serverSecret.Data).To(HaveKey("tls.key"))
			Expect(serverSecret.Data).To(HaveKey("ca.crt"))
		})
	})
})
