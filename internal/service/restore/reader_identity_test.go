package restore

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestRestoreReaderAccountKeepsIndependentTargetGrants(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: "snapshot-reader", Namespace: "recovery", UID: "reader-uid",
		Labels:      map[string]string{"administrator": "owned"},
		Annotations: map[string]string{"azure.workload.identity/client-id": "reader"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(account).Build()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(account), account))
	expected := account.DeepCopy()
	m := NewManager(c, scheme, nil, nil, "")
	for _, name := range []string{"first-target", "second-target"} {
		cluster := &api.OpenBaoCluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: account.Namespace, UID: types.UID("target-" + name)}}
		request := &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Name: name + "-request", Namespace: account.Namespace},
			Spec: api.OpenBaoRestoreSpec{Cluster: name, ServiceAccountName: account.Name, Image: "restore:dev",
				Source: api.RestoreSource{Target: api.BackupTarget{WorkloadIdentity: &api.WorkloadIdentityConfig{
					ServiceAccountAnnotations: account.Annotations, PodLabels: map[string]string{"azure.workload.identity/use": "true"},
				}}}},
		}
		require.NoError(t, m.ensureRestoreResources(t.Context(), logr.Discard(), request, cluster))
		request.Status.Execution = newRestoreExecutionStatus(request)
		job, err := m.buildRestoreJob(request, cluster, "")
		require.NoError(t, err)
		require.Equal(t, account.Name, job.Spec.Template.Spec.ServiceAccountName)
		require.Equal(t, "true", job.Spec.Template.Labels["azure.workload.identity/use"])
		role := &rbacv1.Role{}
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: account.Namespace, Name: name + "-restore-serviceaccount-role"}, role))
		require.Equal(t, []string{request.Name}, role.Rules[1].ResourceNames)
		require.Equal(t, []string{request.Name}, role.Rules[2].ResourceNames)
		binding := &rbacv1.RoleBinding{}
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: account.Namespace, Name: name + "-restore-serviceaccount-rolebinding"}, binding))
		require.Equal(t, account.Name, binding.Subjects[0].Name)
		require.Equal(t, cluster.UID, binding.OwnerReferences[0].UID)
	}
	roles := &rbacv1.RoleList{}
	require.NoError(t, c.List(t.Context(), roles))
	require.Len(t, roles.Items, 2)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(account), account))
	require.Equal(t, expected, account, "reader account ownership, metadata, and UID must remain unchanged")
}

func TestRestoreExistingReaderAccountValidation(t *testing.T) {
	for _, state := range []string{"missing", "wrong identity", "deleting"} {
		t.Run(state, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, api.AddToScheme(scheme))
			account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "recovery"}}
			if state == "deleting" {
				now := metav1.Now()
				account.DeletionTimestamp = &now
				account.Finalizers = []string{"test"}
			}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if state != "missing" {
				builder = builder.WithObjects(account)
			}
			c := builder.Build()
			request := &api.OpenBaoRestore{ObjectMeta: metav1.ObjectMeta{Namespace: account.Namespace},
				Spec: api.OpenBaoRestoreSpec{ServiceAccountName: account.Name, Source: api.RestoreSource{Target: api.BackupTarget{
					WorkloadIdentity: &api.WorkloadIdentityConfig{ServiceAccountAnnotations: map[string]string{"reader": "required"}},
				}}},
			}
			err := NewManager(c, scheme, nil, nil, "").ensureRestoreServiceAccount(t.Context(), logr.Discard(), request, &api.OpenBaoCluster{})
			require.Error(t, err)
		})
	}
}

func TestFreshTargetAuthorizesOnlySelectedRestoreSubject(t *testing.T) {
	_, request := freshTargetFixture(t)
	request.Spec.ServiceAccountName = "snapshot-reader"
	cluster := projectRestoreTarget(request)
	subjects := cluster.Spec.SelfInit.OIDC.AdditionalSubjects
	require.NotNil(t, subjects)
	require.Equal(t, []api.KubernetesServiceAccountSubject{"system:serviceaccount:recovery:snapshot-reader"}, subjects.Restore)
	require.Empty(t, subjects.Backup)
	require.Empty(t, subjects.Upgrade)
	require.Empty(t, subjects.Operator)
}
