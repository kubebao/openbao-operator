//go:build e2e

package e2e

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/platform/constants"
	"github.com/kubebao/openbao-operator/test/e2e/framework"
	"github.com/kubebao/openbao-operator/test/e2e/helpers"
)

var _ = Describe("Minimal fresh restore and scheduled restore tests", Label("restore-minimal", "dr", "backup", "restore", "e2e-anchor"), func() {
	ctx := context.Background()
	Context("Fresh target journeys", Ordered, func() {
		var c client.Client
		var sourceFW, destinationFW *framework.Framework
		var source *api.OpenBaoCluster
		var template *api.RestoreClusterTemplate
		var snapshot api.RestoreSource
		var fixture *minimalRestoreFixture
		BeforeAll(func() {
			fixture = newMinimalRestoreFixture(ctx)
			c = fixture.client
			sourceFW, destinationFW = fixture.sourceFW, fixture.destinationFW
			source, template, snapshot = fixture.source, fixture.template, fixture.snapshot
		})
		newRequest := func(name string, lifecycle api.RestoreTargetLifecycle) *api.OpenBaoRestore {
			return fixture.newRequest(name, lifecycle)
		}

		It("restores a static-sealed snapshot with the original key and removes the disposable target", Label("case:restore-minimal-static"), func() {
			By("backing up a source with an operator-generated static key")
			staticSource := source.DeepCopy()
			staticSource.ObjectMeta = metav1.ObjectMeta{Name: "static-source", Namespace: sourceFW.Namespace}
			staticSource.Status = api.OpenBaoClusterStatus{}
			staticSource.Spec.Unseal = &api.UnsealConfig{Type: "static"}
			staticSource.Spec.Backup.RestoreTest = nil
			Expect(c.Create(ctx, staticSource)).To(Succeed())
			Expect(c.Create(ctx, newBackupNetworkPolicy(sourceFW.Namespace, staticSource.Name, fixture.storageNamespace.Name, 9000, "backup"))).To(Succeed())
			sourceFW.WaitForCondition(staticSource.Name, api.ConditionAvailable, metav1.ConditionTrue)
			Expect(triggerManualBackup(ctx, c, staticSource.Namespace, staticSource.Name)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(staticSource), staticSource)).To(Succeed())
				g.Expect(staticSource.Status.Backup).NotTo(BeNil())
				g.Expect(staticSource.Status.Backup.LatestSnapshot).NotTo(BeNil())
			}, 5*time.Minute, 2*time.Second).Should(Succeed())

			By("preparing the original key as an administrator-owned destination Secret")
			key := &corev1.Secret{}
			Expect(c.Get(ctx, client.ObjectKey{Namespace: sourceFW.Namespace, Name: staticSource.Name + "-unseal-key"}, key)).To(Succeed())
			key.ObjectMeta = metav1.ObjectMeta{Name: "static-restore-key", Namespace: destinationFW.Namespace}
			Expect(c.Create(ctx, key)).To(Succeed())
			request := newRequest("static-recovery", api.RestoreTargetLifecycleDisposable)
			request.Spec.ClusterTemplate.Unseal = api.UnsealConfig{
				Type:                 "static",
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: key.Name},
			}
			summary := staticSource.Status.Backup.LatestSnapshot
			request.Spec.Source.Key = staticSource.Status.Backup.LastBackupName
			request.Spec.Source.ExpectedClusterID = summary.ClusterID
			request.Spec.Source.ExpectedVersion = summary.Version
			request.Spec.Source.ExpectedDigest = summary.Digest
			request.Spec.Source.ExpectedSize = summary.Size
			Expect(c.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseCompleted), request.Status.Message)
				g.Expect(request.Status.Target.AppliedAt).NotTo(BeNil())
				g.Expect(request.Status.Target.Cleanup).To(Equal(api.RestoreTargetCleanupComplete))
			}, 6*time.Minute, 2*time.Second).Should(Succeed())
			Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster}, &api.OpenBaoCluster{}))).To(BeTrue())
			Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: "data-" + request.Spec.Cluster + "-0"}, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(key), key)).To(Succeed())
			Expect(key.OwnerReferences).To(BeEmpty())
			Expect(c.Delete(ctx, request)).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(request), &api.OpenBaoRestore{}))
			}, time.Minute, time.Second).Should(BeTrue())
		})

		It("reuses a reader account across scheduled restores and removes disposable targets", Label("case:restore-minimal-scheduled"), func() {
			By("preparing an administrator-owned reader account and separate destination credential reference")
			readerAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name: "snapshot-reader", Namespace: destinationFW.Namespace,
				Annotations: map[string]string{"e2e.identity/reader": "true"},
			}}
			Expect(c.Create(ctx, readerAccount)).To(Succeed())
			readerUID := readerAccount.UID
			storage := &corev1.Secret{}
			Expect(c.Get(ctx, client.ObjectKey{Namespace: destinationFW.Namespace, Name: "storage"}, storage)).To(Succeed())
			Expect(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "storage-reader", Namespace: destinationFW.Namespace}, Data: storage.Data})).To(Succeed())
			By("refusing manual and scheduled target creation before namespace approval")
			setMinimalDestinationApproval(ctx, c, destinationFW.Namespace, false)
			Eventually(func() error {
				return c.Create(ctx, newRequest("unapproved", "Disposable"), client.DryRunAll)
			}, time.Minute, time.Second).Should(MatchError(ContainSubstring("openbao.org/restore-target-approved")))
			Expect(c.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
			before := source.DeepCopy()
			source.Spec.Backup.Target.WorkloadIdentity = &api.WorkloadIdentityConfig{
				ServiceAccountAnnotations: map[string]string{"e2e.identity/writer": "true"},
				PodLabels:                 map[string]string{"e2e.identity/writer": "true"},
			}
			source.Spec.Backup.RestoreTest = &api.RestoreTest{
				EverySuccessfulBackups: 1,
				Namespace:              destinationFW.Namespace,

				ClusterTemplate:      *template.DeepCopy(),
				CredentialsSecretRef: &corev1.LocalObjectReference{Name: "storage-reader"},
				ServiceAccountName:   readerAccount.Name,
				CleanupAfterSeconds:  30,
			}
			Expect(c.Patch(ctx, source, client.MergeFrom(before))).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
				g.Expect(source.Status.Backup.RestoreTest).NotTo(BeNil())
				g.Expect(source.Status.Backup.RestoreTest.Active).To(BeNil())
				g.Expect(meta.FindStatusCondition(source.Status.Backup.RestoreTest.Conditions, "Passed")).To(HaveField("Reason", "DestinationRejected"))
			}, time.Minute, time.Second).Should(Succeed())
			children := &api.OpenBaoRestoreList{}
			Expect(c.List(ctx, children, client.InNamespace(destinationFW.Namespace))).To(Succeed())
			Expect(children.Items).To(BeEmpty())

			By("approving the prepared namespace and completing the restore test")
			setMinimalDestinationApproval(ctx, c, destinationFW.Namespace, true)
			By("checking destination Service families before the disposable target is removed")
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
				g.Expect(source.Status.Backup.RestoreTest.Active).NotTo(BeNil())
				name := source.Status.Backup.RestoreTest.Active.Name
				child := &api.OpenBaoRestore{}
				g.Expect(c.Get(ctx, client.ObjectKey{Namespace: destinationFW.Namespace, Name: name}, child)).To(Succeed())
				g.Expect(child.Spec.ClusterTemplate.Service).To(Equal(template.Service))
				target := &api.OpenBaoCluster{}
				g.Expect(c.Get(ctx, client.ObjectKey{Namespace: destinationFW.Namespace, Name: name}, target)).To(Succeed())
				g.Expect(target.Spec.Service).NotTo(BeNil())
				g.Expect(target.Spec.Service.IPFamilies).To(Equal(template.Service.IPFamilies))
				service := &corev1.Service{}
				g.Expect(c.Get(ctx, client.ObjectKey{Namespace: destinationFW.Namespace, Name: name + "-public"}, service)).To(Succeed())
				g.Expect(service.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
				g.Expect(service.Spec.IPFamilyPolicy).To(Equal(template.Service.IPFamilyPolicy))
				g.Expect(service.Spec.IPFamilies).To(Equal(template.Service.IPFamilies))
				g.Expect(service.Spec.ClusterIPs).To(HaveLen(len(template.Service.IPFamilies)))
				AddReportEntry("scheduled-restore-service-families", map[string]any{
					"sourceFamilies": source.Spec.Service.IPFamilies,
					"targetFamilies": service.Spec.IPFamilies, "targetClusterIPs": service.Spec.ClusterIPs,
				})
			}, 8*time.Minute, time.Second).Should(Succeed())
			Eventually(func(g Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(c.List(ctx, jobs, client.InNamespace(destinationFW.Namespace), client.MatchingLabels{"openbao.org/component": "restore"})).To(Succeed())
				g.Expect(jobs.Items).NotTo(BeEmpty())
				job := &jobs.Items[0]
				g.Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal(readerAccount.Name))
				g.Expect(job.Spec.Template.Labels).NotTo(HaveKey("e2e.identity/writer"))
				credentialSecrets := []string{}
				for _, volume := range job.Spec.Template.Spec.Volumes {
					if volume.Secret != nil {
						credentialSecrets = append(credentialSecrets, volume.Secret.SecretName)
					}
				}
				g.Expect(credentialSecrets).To(ContainElement("storage-reader"))
			}, 8*time.Minute, time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
				g.Expect(source.Status.Backup.RestoreTest).NotTo(BeNil())
				g.Expect(source.Status.Backup.RestoreTest.Last).NotTo(BeNil(), "conditions: %v", source.Status.Backup.RestoreTest.Conditions)
				g.Expect(source.Status.Backup.RestoreTest.Last.Outcome).To(Equal(api.RestoreTestPassed))
				g.Expect(source.Status.Backup.RestoreTest.Active).To(BeNil())
			}, 10*time.Minute, 2*time.Second).Should(Succeed())
			last := source.Status.Backup.RestoreTest.Last
			Expect(last.Namespace).To(Equal(destinationFW.Namespace))
			Expect(last.Key).NotTo(BeEmpty())
			Expect(last.Digest).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`))
			Expect(last.Reason).To(Equal("SnapshotApplied"))
			Expect(last.Message).NotTo(BeEmpty())
			Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{
				Namespace: destinationFW.Namespace,
				Name:      last.Name,
			}, &api.OpenBaoRestore{}))).To(BeTrue())
			Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{
				Namespace: destinationFW.Namespace,
				Name:      last.Name,
			}, &api.OpenBaoCluster{}))).To(BeTrue())
			Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{
				Namespace: destinationFW.Namespace,
				Name:      "data-" + last.Name + "-0",
			}, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			By("reusing the same reader account for a second generated target after cleanup")
			firstRun := last.Name
			Eventually(func() bool {
				roleGone := apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: destinationFW.Namespace, Name: firstRun + "-restore-serviceaccount-role"}, &rbacv1.Role{}))
				bindingGone := apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: destinationFW.Namespace, Name: firstRun + "-restore-serviceaccount-rolebinding"}, &rbacv1.RoleBinding{}))
				return roleGone && bindingGone
			}, time.Minute, time.Second).Should(BeTrue())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(readerAccount), readerAccount)).To(Succeed())
			Expect(readerAccount.UID).To(Equal(readerUID))
			Expect(readerAccount.OwnerReferences).To(BeEmpty())
			Expect(readerAccount.Annotations).To(Equal(map[string]string{"e2e.identity/reader": "true"}))
			Expect(c.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
			before = source.DeepCopy()
			source.Spec.Backup.RestoreTest.CleanupAfterSeconds = 0
			Expect(c.Patch(ctx, source, client.MergeFrom(before))).To(Succeed())
			By("taking a new backup with the writer before the second disposable restore")
			Expect(triggerManualBackup(ctx, c, source.Namespace, source.Name)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(source), source)).To(Succeed())
				g.Expect(source.Status.Backup.RestoreTest.Last.Name).NotTo(Equal(firstRun))
				g.Expect(source.Status.Backup.RestoreTest.Last.Outcome).To(Equal(api.RestoreTestPassed))
				g.Expect(source.Status.Backup.RestoreTest.Active).To(BeNil())
			}, 12*time.Minute, 2*time.Second).Should(Succeed())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(readerAccount), readerAccount)).To(Succeed())
			Expect(readerAccount.UID).To(Equal(readerUID))
			Expect(readerAccount.OwnerReferences).To(BeEmpty())
		})

		It("retains an applied target until the administrator accepts a paused handoff", Label("case:restore-minimal-retain"), func() {
			request := newRequest("retained", "Retain")
			Expect(c.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Target).NotTo(BeNil(), request.Status.Message)
				g.Expect(request.Status.Target.AppliedAt).NotTo(BeNil(), request.Status.Message)
			}, 8*time.Minute, 2*time.Second).Should(Succeed())
			Expect(request.Status.Target.BootstrapClusterID).NotTo(Equal(snapshot.ExpectedClusterID))
			Expect(request.Status.SubmissionClaim.Digest).To(Equal(snapshot.ExpectedDigest))
			cluster := &api.OpenBaoCluster{}
			Expect(c.Get(ctx, client.ObjectKey{
				Namespace: request.Namespace,
				Name:      request.Spec.Cluster,
			}, cluster)).To(Succeed())
			service := &corev1.Service{}
			Expect(c.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Spec.Cluster + "-public"}, service)).To(Succeed())
			Expect(service.Spec.IPFamilies).To(Equal(template.Service.IPFamilies))
			Expect(service.Spec.IPFamilyPolicy).To(Equal(template.Service.IPFamilyPolicy))
			By("checking ready client Service backends for each destination family")
			Eventually(func(g Gomega) {
				slices := &discoveryv1.EndpointSliceList{}
				g.Expect(c.List(ctx, slices, client.InNamespace(request.Namespace),
					client.MatchingLabels{discoveryv1.LabelServiceName: service.Name})).To(Succeed())
				for _, family := range template.Service.IPFamilies {
					found := false
					for _, slice := range slices.Items {
						if slice.AddressType != discoveryv1.AddressType(family) {
							continue
						}
						for _, endpoint := range slice.Endpoints {
							if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready &&
								endpoint.TargetRef != nil && endpoint.TargetRef.Name == request.Spec.Cluster+"-0" {
								found = true
							}
						}
					}
					g.Expect(found).To(BeTrue(), "no ready target backend for %s", family)
				}
			}, time.Minute, time.Second).Should(Succeed())
			AddReportEntry("fresh-restore-service-families", map[string]any{
				"sourceFamilies": source.Spec.Service.IPFamilies,
				"targetFamilies": service.Spec.IPFamilies, "targetClusterIPs": service.Spec.ClusterIPs,
			})
			Expect(cluster.Annotations[constants.AnnotationRestoreHold]).To(Equal(string(request.UID)))
			before := request.DeepCopy()
			request.Annotations = map[string]string{constants.AnnotationRestoreAcknowledge: string(request.UID) + "/Abandon"}
			Expect(c.Patch(ctx, request, client.MergeFrom(before))).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseUnknown))
				g.Expect(request.Status.AdministratorDisposition).To(Equal(api.RestoreAdministratorAbandon))
				g.Expect(request.Status.Target.AppliedAt).NotTo(BeNil())
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
				g.Expect(cluster.Spec.Paused).To(BeTrue())
				g.Expect(cluster.Annotations[constants.AnnotationRestoreHold]).To(BeEmpty())
			}, time.Minute, 2*time.Second).Should(Succeed())
		})

		It("cleans a disposable target when the source object is missing", Label("case:restore-minimal-missing-source"), func() {
			request := newRequest("missing-source", "Disposable")
			request.Spec.Source.Key = "missing-snapshot"
			Expect(c.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseFailed), request.Status.Message)
				g.Expect(request.Status.Target.Cleanup).To(Equal(api.RestoreTargetCleanupComplete))
				g.Expect(request.Status.SubmissionClaim).To(BeNil())
			}, 8*time.Minute, 2*time.Second).Should(Succeed())
		})

		It("rejects changed snapshot bytes before submission", Label("case:restore-minimal-digest"), func() {
			request := newRequest("wrong-digest", "Disposable")
			request.Spec.Source.ExpectedDigest = "sha256:" + strings.Repeat("0", 64)
			Expect(c.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Target).NotTo(BeNil())
				g.Expect(request.Status.Target.Cleanup).To(Equal(api.RestoreTargetCleanupComplete), request.Status.Message)
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseFailed))
				g.Expect(request.Status.SubmissionClaim).To(BeNil())
			}, 5*time.Minute, 2*time.Second).Should(Succeed())
		})

		It("cleans a cancelled disposable target and its data volume", Label("case:restore-minimal-cancel"), func() {
			request := newRequest("cancelled", "Disposable")
			request.Spec.CleanupAfterSeconds = 300
			Expect(c.Create(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Target).NotTo(BeNil())
				g.Expect(request.Status.Target.AppliedAt).NotTo(BeNil(), request.Status.Message)
			}, 5*time.Minute, 2*time.Second).Should(Succeed())
			By("withdrawing approval without blocking cleanup of the admitted target")
			setMinimalDestinationApproval(ctx, c, destinationFW.Namespace, false)
			Eventually(func() error {
				return c.Create(ctx, newRequest("approval-withdrawn", "Disposable"), client.DryRunAll)
			}, time.Minute, time.Second).Should(MatchError(ContainSubstring("openbao.org/restore-target-approved")))
			Expect(c.Delete(ctx, request)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(request), &api.OpenBaoRestore{}))).To(BeTrue())
				key := client.ObjectKey{
					Namespace: request.Namespace,
					Name:      request.Spec.Cluster,
				}
				g.Expect(apierrors.IsNotFound(c.Get(ctx, key, &api.OpenBaoCluster{}))).To(BeTrue())
				key.Name = "data-" + request.Spec.Cluster + "-0"
				g.Expect(apierrors.IsNotFound(c.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			}, 3*time.Minute, 2*time.Second).Should(Succeed())
		})

	})
	Context("Source identity deadline", Ordered, func() {
		var fixture *minimalRestoreFixture
		BeforeAll(func() { fixture = newMinimalRestoreFixture(ctx) })

		It("does not confirm application when the source identity differs", Label("case:restore-minimal-identity"), func() {
			request := fixture.newRequest("wrong-identity", "Disposable")
			request.Spec.Source.ExpectedClusterID = "different-source-id"
			Expect(fixture.client.Create(ctx, request)).To(Succeed())
			waitForHeldRestore(ctx, fixture.client, request)
			Expect(request.Status.SubmissionClaim).NotTo(BeNil())
			claimedAt := request.Status.SubmissionClaim.ClaimedAt.DeepCopy()
			Eventually(func(g Gomega) {
				g.Expect(fixture.client.Get(ctx, client.ObjectKeyFromObject(request), request)).To(Succeed())
				g.Expect(request.Status.Target.AppliedAt).To(BeNil())
				g.Expect(request.Status.Target.Cleanup).To(Equal(api.RestoreTargetCleanupComplete))
				g.Expect(request.Status.Phase).To(Equal(api.RestorePhaseFailed))
				key := client.ObjectKey{
					Namespace: request.Namespace,
					Name:      request.Spec.Cluster,
				}
				g.Expect(apierrors.IsNotFound(fixture.client.Get(ctx, key, &api.OpenBaoCluster{}))).To(BeTrue())
				key.Name = "data-" + request.Spec.Cluster + "-0"
				g.Expect(apierrors.IsNotFound(fixture.client.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			}, 13*time.Minute, 2*time.Second).Should(Succeed())
			Expect(request.Status.SubmissionClaim.ClaimedAt.Equal(claimedAt)).To(BeTrue())
			Expect(request.Status.CompletionTime).NotTo(BeNil())
			Expect(request.Status.CompletionTime.Time).To(BeTemporally(">=", claimedAt.Add(10*time.Minute)),
				"a wrong source identity must retain the full application deadline")
			AddReportEntry("restore-identity-deadline", map[string]any{
				"claimedAt": claimedAt.Time, "completedAt": request.Status.CompletionTime.Time,
				"elapsedSeconds": request.Status.CompletionTime.Sub(claimedAt.Time).Seconds(),
			})
			Expect(fixture.client.Delete(ctx, request)).To(Succeed())
		})

	})
})

func setMinimalDestinationApproval(ctx context.Context, c client.Client, namespace string, approved bool) {
	ns := &corev1.Namespace{}
	Expect(c.Get(ctx, client.ObjectKey{Name: namespace}, ns)).To(Succeed())
	before := ns.DeepCopy()
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	if approved {
		ns.Labels["openbao.org/restore-target-approved"] = e2eStringTrue
	} else {
		delete(ns.Labels, "openbao.org/restore-target-approved")
	}
	Expect(c.Patch(ctx, ns, client.MergeFrom(before))).To(Succeed())
}

// This fixture exercises a prepared namespace on Cilium. JWT discovery shares
// the API endpoint; this allowance cannot restrict traffic to a URL path.
func prepareMinimalDestinationNetwork(ctx context.Context, c client.Client, namespace, sealNamespace, storageNamespace string) {
	base := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prepared-boundary",
			Namespace: namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{
					{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": operatorNamespace}}},
					{PodSelector: &metav1.LabelSelector{}},
				},
				Ports: []networkingv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 8200)},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				namespaceEgressRule("kube-system", corev1.ProtocolUDP, 53), namespaceEgressRule("kube-system", corev1.ProtocolTCP, 53),
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": sealNamespace}},
						PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "transit"}},
					}},
					Ports: []networkingv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 8200)},
				},
			},
		},
	}
	for _, ip := range apiServerEndpointIPs {
		base.Spec.Egress = append(base.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: helpers.HostCIDR(ip)}}},
			Ports: []networkingv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 6443), networkPolicyPort(corev1.ProtocolTCP, 443)},
		})
	}
	Expect(c.Create(ctx, base)).To(Succeed())
	helper := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prepared-executors",
			Namespace: namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{constants.LabelOpenBaoComponent: "restore"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{namespaceEgressRule(storageNamespace, corev1.ProtocolTCP, 9000), namespaceEgressRule(namespace, corev1.ProtocolTCP, 8200)},
		},
	}
	Expect(c.Create(ctx, helper)).To(Succeed())
	if framework.APIServerEgressForNetworkProfile() == nil {
		return
	}
	// Restore destinations retain administrator-managed isolation. Cilium's
	// CIDR rules exclude API-server nodes, so executors need an entity allowance.
	apiServer := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata": map[string]interface{}{
			"name": "prepared-executor-api", "namespace": namespace,
		},
		"spec": map[string]interface{}{
			"endpointSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{constants.LabelOpenBaoComponent: "restore"},
			},
			"enableDefaultDeny": map[string]interface{}{"ingress": false, "egress": false},
			"egress": []interface{}{map[string]interface{}{
				"toEntities": []interface{}{"kube-apiserver"},
				"toPorts": []interface{}{map[string]interface{}{
					"ports": []interface{}{map[string]interface{}{"port": "6443", "protocol": "TCP"}},
				}},
			}},
		},
	}}
	Expect(c.Create(ctx, apiServer)).To(Succeed())
}
