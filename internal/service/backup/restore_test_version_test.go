package backup

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/kubebao/openbao-operator/api/v1alpha1"
)

func TestRestoreTestUsesSnapshotVersion(t *testing.T) {
	for _, tc := range []struct {
		name, sourceVersion, pin, image, issue string
	}{
		{name: "minimum", sourceVersion: "2.7.0"},
		{name: "future-patch", sourceVersion: "2.7.99"},
		{name: "future-minor", sourceVersion: "2.10.0"},
		{name: "future-major", sourceVersion: "10.0.0"},
		{name: "build-metadata", sourceVersion: "2.7.2+build.1"},
		{name: "pin", sourceVersion: "2.7.2", pin: "2.7.2"},
		{name: "custom-image", sourceVersion: "2.7.2", pin: "2.7.2", image: "registry.example/bao@sha256:pinned"},
		{name: "pin-mismatch", sourceVersion: "2.7.2", pin: "2.7.1", issue: "does not match"},
		{name: "unversioned-image", sourceVersion: "2.7.2", image: "registry.example/bao:custom", issue: "custom restore test images"},
		{name: "older", sourceVersion: "2.6.99", issue: "stable OpenBao release"},
		{name: "prerelease", sourceVersion: "2.8.0-rc.1", issue: "stable OpenBao release"},
		{name: "missing", issue: "stable OpenBao release"},
		{name: "malformed", sourceVersion: "latest", issue: "stable OpenBao release"},
		{name: "noncanonical", sourceVersion: "v2.7.2", issue: "stable OpenBao release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newRestoreTestSource("source")
			cluster.Spec.Version = "2.8.0" // The live version must not replace an older backup's version.
			cluster.Status.Backup.LatestSnapshot.Version = tc.sourceVersion
			config := &cluster.Spec.Backup.RestoreTest.ClusterTemplate
			config.Version, config.Image = tc.pin, tc.image
			original := config.DeepCopy()
			c := newTestClient(t, cluster)
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cluster), cluster))
			require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
			children := &api.OpenBaoRestoreList{}
			require.NoError(t, c.List(t.Context(), children))
			if tc.issue != "" {
				require.Empty(t, children.Items)
				require.Nil(t, cluster.Status.Backup.RestoreTest.Active)
				require.Nil(t, cluster.Status.Backup.RestoreTest.LastScheduledAt)
				require.Equal(t, "NoCompatibleSnapshot", cluster.Status.Backup.RestoreTest.Conditions[0].Reason)
				require.Contains(t, cluster.Status.Backup.RestoreTest.Conditions[0].Message, tc.issue)
				return
			}
			require.Len(t, children.Items, 1)
			child := children.Items[0]
			require.Equal(t, tc.sourceVersion, child.Spec.ClusterTemplate.Version)
			require.Equal(t, tc.sourceVersion, child.Spec.Source.ExpectedVersion)
			require.Equal(t, tc.image, child.Spec.ClusterTemplate.Image)
			require.Equal(t, original, &cluster.Spec.Backup.RestoreTest.ClusterTemplate)

			before := cluster.DeepCopy()
			cluster.Status.Backup.LatestSnapshot.Version = "2.9.0"
			require.NoError(t, c.Status().Patch(t.Context(), cluster, client.MergeFrom(before)))
			require.NoError(t, newBackupManager(c).reconcileRestoreTest(t.Context(), logr.Discard(), cluster, time.Now()))
			require.NoError(t, c.List(t.Context(), children))
			require.Len(t, children.Items, 1)
			require.Equal(t, tc.sourceVersion, children.Items[0].Spec.ClusterTemplate.Version,
				"a later backup must not change an active restore request")
		})
	}
}
