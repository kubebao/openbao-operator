//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
)

const migrationSourceUIDAnnotation = "migration.kubebao.org/source-uid"

// An API migration import keeps a Shared Cluster's declared JWT mode, but only a
// principal granted the migrate verb may mark a create as an import.
func TestMigrationImportAdmission(t *testing.T) {
	c, scheme, config := startIsolatedEnv(t, isolatedEnvOptions{
		crdDirs: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	})
	namespace := createNamespace(t, ctx, c)
	installControllerJWTAdmission(t, c, namespace)

	impersonate := func(username string, verbs ...string) client.Client {
		role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: username, Namespace: namespace},
			Rules: []rbacv1.PolicyRule{{APIGroups: []string{"kubebao.org"}, Resources: []string{"openbaoclusters"}, Verbs: verbs}}}
		require.NoError(t, c.Create(ctx, role))
		binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: username, Namespace: namespace},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: username},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: username}}}
		require.NoError(t, c.Create(ctx, binding))
		impersonated := rest.CopyConfig(config)
		impersonated.Impersonate = rest.ImpersonationConfig{UserName: username}
		user, err := client.New(impersonated, client.Options{Scheme: scheme})
		require.NoError(t, err)
		return user
	}
	// The minimal Cluster selects custom executables; both users may use them so
	// each case isolates the migration rules.
	importer := impersonate("migration-importer", "create", "usecustomexecutables", "migrate")
	tenant := impersonate("cluster-creator", "create", "usecustomexecutables")

	shared := func(name, sourceUID string) *openbaov1alpha1.OpenBaoCluster {
		cluster := newMinimalClusterObj(namespace, name)
		cluster.Spec.ControllerJWTMode = ""
		if sourceUID != "" {
			cluster.Annotations = map[string]string{migrationSourceUIDAnnotation: sourceUID}
		}
		return cluster
	}
	createEventually := func(user client.Client, cluster *openbaov1alpha1.OpenBaoCluster, allowed bool, message string) {
		t.Helper()
		var err error
		eventuallyOrFail(t, func() bool {
			err = user.Create(ctx, cluster.DeepCopy(), &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			if allowed {
				return err == nil
			}
			return err != nil && strings.Contains(err.Error(), message)
		}, func() string { return "create " + cluster.Name + ": " + errString(err) })
	}

	createEventually(importer, shared("imported", "source-uid"), true, "")
	createEventually(tenant, shared("forged-import", "source-uid"), false, "reserved for API migration imports")
	createEventually(tenant, shared("ordinary-shared", ""), false, "spec.controllerJWTMode is required")
	createEventually(importer, shared("unmarked-shared", ""), false, "spec.controllerJWTMode is required")

	cluster := shared("imported-object", "source-uid")
	require.NoError(t, c.Create(ctx, cluster))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	cluster.Annotations[migrationSourceUIDAnnotation] = "another-source"
	requireAdmissionDenied(t, c.Update(ctx, cluster))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	delete(cluster.Annotations, migrationSourceUIDAnnotation)
	requireAdmissionDenied(t, c.Update(ctx, cluster)) // the marker is the Cluster's controller identity
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster))
	require.Empty(t, cluster.Spec.ControllerJWTMode, "an import keeps the source's declared mode")
}

// eventuallyOrFail polls like require.Eventually but reports the final state.
func eventuallyOrFail(t *testing.T, condition func() bool, describe func() string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(describe())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func installRestoreValidationPolicy(t *testing.T) {
	t.Helper()
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	for name, object := range map[string]client.Object{
		"openbao-validate-openbaorestore.yaml":         policy,
		"openbao-validate-openbaorestore-binding.yaml": binding,
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "policy", name))
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(data, object))
		require.NoError(t, k8sClient.Create(ctx, object))
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), object) })
	}
}

// An API migration re-creates finished restore records. Only a principal granted
// the migrate verb may mark one as an import, and only the marked record's first
// status write may carry the controller's recorded recovery evidence.
func TestRestoreMigrationImportAdmission(t *testing.T) {
	installRestoreExecutionPolicy(t)
	installRestoreValidationPolicy(t)
	namespace := newTestNamespace(t) // not approved for fresh restore targets

	const creatorName = "restore-creator"
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: creatorName, Namespace: namespace}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"kubebao.org"}, Resources: []string{"openbaorestores"}, Verbs: []string{"create", "get"}},
		{APIGroups: []string{"kubebao.org"}, Resources: []string{"openbaoclusters"}, Verbs: []string{"restore"}},
	}}
	require.NoError(t, k8sClient.Create(ctx, role))
	require.NoError(t, k8sClient.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: creatorName, Namespace: namespace},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: creatorName},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: creatorName}}}))
	impersonated := rest.CopyConfig(cfg)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: creatorName}
	creator, err := client.New(impersonated, client.Options{Scheme: k8sScheme})
	require.NoError(t, err)

	record := func(name string, marked bool) *openbaov1alpha1.OpenBaoRestore {
		restore := &openbaov1alpha1.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: openbaov1alpha1.OpenBaoRestoreSpec{Cluster: "target", Source: openbaov1alpha1.RestoreSource{
				Key: "snapshot", Target: openbaov1alpha1.BackupTarget{Bucket: "snapshots", Endpoint: "https://storage.example.com"},
			}}}
		if marked {
			restore.Annotations = map[string]string{migrationSourceUIDAnnotation: "source-uid"}
		}
		return restore
	}
	var probe error
	eventuallyOrFail(t, func() bool {
		probe = creator.Create(ctx, record("forged-import", true), &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return probe != nil && strings.Contains(probe.Error(), "reserved for API migration imports")
	}, func() string { return "forged restore import: " + errString(probe) })
	require.NoError(t, creator.Create(ctx, record("ordinary", false), &client.CreateOptions{DryRun: []string{metav1.DryRunAll}}))

	now := metav1.Now()
	resumed := openbaov1alpha1.OpenBaoRestoreStatus{Phase: openbaov1alpha1.RestorePhaseUnknown,
		AdministratorDisposition: openbaov1alpha1.RestoreAdministratorResume, CompletionTime: &now,
		Execution: &openbaov1alpha1.RestoreExecutionStatus{TargetUID: "source-cluster-uid", OperationID: "source-uid",
			Stage: openbaov1alpha1.RestoreExecutionStageTerminalObserved, JobName: "target-restore", JobUID: "job-uid",
			TerminalResult: openbaov1alpha1.RestoreExecutionResultSucceeded},
		Restart: &openbaov1alpha1.RestoreRestartStatus{CompletedAt: &now,
			Pods: []openbaov1alpha1.RestoreRestartPod{{Name: "target-0", UID: "pod-uid", StatefulSetUID: "sts-uid"}}}}

	// An unmarked record's status cannot carry the controller's evidence. This
	// also waits until the execution policy is enforced.
	plain := record("plain", false)
	require.NoError(t, k8sClient.Create(ctx, plain))
	plain.Status = resumed
	eventuallyOrFail(t, func() bool {
		probe = k8sClient.Status().Update(ctx, plain.DeepCopy(), &client.SubResourceUpdateOptions{
			UpdateOptions: client.UpdateOptions{DryRun: []string{metav1.DryRunAll}}})
		return probe != nil && strings.Contains(probe.Error(), "Only the controller can record")
	}, func() string { return "forged restore status: " + errString(probe) })
	requireAdmissionDenied(t, k8sClient.Status().Update(ctx, plain))

	imported := record("imported", true)
	require.NoError(t, k8sClient.Create(ctx, imported))
	imported.Status = resumed
	require.NoError(t, k8sClient.Status().Update(ctx, imported), "the first status write carries the recorded evidence")
	before := imported.DeepCopy()
	imported.Status.AdministratorDisposition = openbaov1alpha1.RestoreAdministratorAbandon
	requireAdmissionDenied(t, k8sClient.Status().Patch(ctx, imported, client.MergeFrom(before)))
	imported = before.DeepCopy()
	imported.Annotations[migrationSourceUIDAnnotation] = "another-source"
	requireAdmissionDenied(t, k8sClient.Patch(ctx, imported, client.MergeFrom(before)))
	imported = before.DeepCopy()
	delete(imported.Annotations, migrationSourceUIDAnnotation)
	requireAdmissionDenied(t, k8sClient.Patch(ctx, imported, client.MergeFrom(before)))

	fresh := newFreshRestoreRequest(namespace, "fresh")
	require.ErrorContains(t, k8sClient.Create(ctx, fresh, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}}),
		"restore-target-approved=true")
	fresh.Annotations = map[string]string{migrationSourceUIDAnnotation: "fresh-source-uid"}
	require.NoError(t, k8sClient.Create(ctx, fresh, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}}),
		"an import re-creates a record whose target approval was checked when it was created")
}
