package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorInputDoesNotApplySharedBundleCRs(t *testing.T) {
	for _, document := range []string{
		"apiVersion: openbao.org/v1alpha1\nkind: OpenBaoCluster\nmetadata: {name: old}\n",
		"apiVersion: kubebao.org/v1alpha1\nkind: OpenBaoTenant\nmetadata: {name: new}\n",
		"apiVersion: v1\nkind: List\nitems:\n- apiVersion: kubebao.org/v1alpha1\n  kind: OpenBaoRestore\n  metadata: {name: history}\n",
		"apiVersion: openbao.org/v1alpha1\nkind: OpenBaoClusterList\nitems:\n- apiVersion: openbao.org/v1alpha1\n  kind: OpenBaoCluster\n  metadata: {name: old}\n",
		"apiVersion: v1\nkind: List\nitems:\n- apiVersion: kubebao.org/v1alpha1\n  kind: OpenBaoRestoreList\n  items:\n  - apiVersion: kubebao.org/v1alpha1\n    kind: OpenBaoRestore\n    metadata: {name: history}\n",
		"apiVersion: v1\nkind: List\nitems: [invalid]\n",
		"apiVersion: v1\nkind: List\nitems: invalid\n",
		"apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata: {name: platform}\n",
		"apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: operator}\n",
	} {
		require.Error(t, validateOperatorManifestData([]byte(document)))
	}
	// CRD declarations, operator Deployments, and credentials remain valid
	// operator inputs. Only identity fields are inspected or reported.
	require.NoError(t, validateOperatorManifestData([]byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: openbaoclusters.kubebao.org}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: operator}
---
apiVersion: v1
kind: Secret
metadata: {name: operator-credentials}
stringData: {token: secret-value}
`)))
}
