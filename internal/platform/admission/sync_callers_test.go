package admission

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncCallerSettingsRejectInvalidConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, key, value string
	}{
		{"pvc CEL", pvcBindingSyncCallersEnv, `true`},
		{"pvc null", pvcBindingSyncCallersEnv, `null`},
		{"pvc list", pvcBindingSyncCallersEnv, `[]`},
		{"pvc wildcard caller", pvcBindingSyncCallersEnv, `{"*":["tenant"]}`},
		{"pvc empty caller", pvcBindingSyncCallersEnv, `{"": ["tenant"]}`},
		{"pvc newline caller", pvcBindingSyncCallersEnv, `{"syncer\n":["tenant"]}`},
		{"pvc wildcard namespace", pvcBindingSyncCallersEnv, `{"syncer":["*"]}`},
		{"pvc empty namespaces", pvcBindingSyncCallersEnv, `{"syncer":[]}`},
		{"pod CEL", podMetadataSyncCallersEnv, `{'syncer': {'tenant': ['example.com/key']}}`},
		{"pod null", podMetadataSyncCallersEnv, `null`},
		{"pod wildcard caller", podMetadataSyncCallersEnv, `{"*":{"tenant":["example.com/key"]}}`},
		{"pod invalid namespace", podMetadataSyncCallersEnv, `{"syncer":{"bad namespace":["example.com/key"]}}`},
		{"pod empty namespace map", podMetadataSyncCallersEnv, `{"syncer":{}}`},
		{"pod empty key list", podMetadataSyncCallersEnv, `{"syncer":{"tenant":[]}}`},
		{"pod invalid key", podMetadataSyncCallersEnv, `{"syncer":{"tenant":["invalid/key/extra"]}}`},
		{"pod operator annotation", podMetadataSyncCallersEnv, `{"syncer":{"tenant":["openbao.org/owner-uid"]}}`},
		{"pod vcluster annotation", podMetadataSyncCallersEnv, `{"syncer":{"tenant":["vcluster.loft.sh/uid"]}}`},
		{"pod Kubernetes annotation", podMetadataSyncCallersEnv, `{"syncer":{"tenant":["kubernetes.io/key"]}}`},
		{"pod Kubernetes subdomain", podMetadataSyncCallersEnv, `{"syncer":{"tenant":["node.kubernetes.io/key"]}}`},
		{"pod k8s annotation", podMetadataSyncCallersEnv, `{"syncer":{"tenant":["k8s.io/key"]}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(pvcBindingSyncCallersEnv, "")
			t.Setenv(podMetadataSyncCallersEnv, "")
			t.Setenv(tt.key, tt.value)
			_, err := installationFromEnvironment()
			require.ErrorContains(t, err, tt.key)
		})
	}
}

func TestSyncCallerContractsRequireTrustedScope(t *testing.T) {
	t.Setenv(pvcBindingSyncCallersEnv, `{"syncer":["tenant-a"]}`)
	t.Setenv(podMetadataSyncCallersEnv, `{"syncer":{"tenant-a":["example.com/host-id"],"tenant-b":["example.com/zone"]}}`)
	var dep Dependency
	for _, candidate := range DefaultDependencies() {
		if candidate.PolicyName == dependencyOpenBaoLockManagedResourceMutations {
			dep = candidate
		}
	}
	require.NotEmpty(t, dep.PolicyName)
	for _, tt := range []struct {
		name, variable, expression string
		allowed                    bool
	}{
		{"PVC trusted", pvcSyncVariable, ` { "syncer" : [ "tenant-a" ] } `, true},
		{"Pod trusted", podSyncVariable, ` {"syncer": {"tenant-b":["example.com/zone"], "tenant-a":["example.com/host-id"]}}`, true},
		{"PVC extra caller", pvcSyncVariable, `{"syncer":["tenant-a"],"unapproved":["tenant-a"]}`, false},
		{"PVC extra namespace", pvcSyncVariable, `{"syncer":["tenant-a","tenant-b"]}`, false},
		{"Pod extra namespace", podSyncVariable, `{"syncer":{"tenant-c":["example.com/host-id"]}}`, false},
		{"Pod extra key", podSyncVariable, `{"syncer":{"tenant-a":["example.com/host-id","example.com/other"]}}`, false},
		{"Pod crossed namespace keys", podSyncVariable, `{"syncer":{"tenant-a":["example.com/zone"],"tenant-b":["example.com/host-id"]}}`, false},
		{"PVC removed allowance", pvcSyncVariable, `{}`, false},
		{"Pod arbitrary CEL", podSyncVariable, `true ? {} : {}`, false},
		{"PVC empty expression", pvcSyncVariable, ``, false},
		{"unrelated validation", "is_operator_controller", `true`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy, binding := releaseFixture(t, dep, "")
			found := false
			for i := range policy.Spec.Variables {
				if policy.Spec.Variables[i].Name == tt.variable {
					policy.Spec.Variables[i].Expression = tt.expression
					found = true
				}
			}
			require.True(t, found)
			status, err := CheckDependencies(context.Background(), newAdmissionClient(t, policy, binding), []Dependency{dep}, []string{""})
			require.NoError(t, err)
			require.Equal(t, tt.allowed, status.OverallReady, status.SummaryMessage())
		})
	}
}
