//go:build e2e

package e2e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubebao/openbao-operator/test/utils"
)

func TestE2EFixtureSingleTenantRestoresModeBeforePreservingResources(t *testing.T) {
	root, err := utils.GetProjectDir()
	require.NoError(t, err)
	for _, directory := range []string{root, filepath.Join(root, "test", "e2e")} {
		t.Run(filepath.Base(directory), func(t *testing.T) {
			t.Chdir(directory)
			assertSingleTenantModeRestoredBeforePreservation(t)
		})
	}
}

func assertSingleTenantModeRestoredBeforePreservation(t *testing.T) {
	t.Helper()
	root, err := utils.GetProjectDir()
	require.NoError(t, err)
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "test", "e2e", "Tenant_Isolation_test.go"), nil, 0)
	require.NoError(t, err)
	calls := func(node ast.Node, name string) bool {
		found := false
		ast.Inspect(node, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if ok {
				if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == name {
					found = true
				}
			}
			return true
		})
		return found
	}
	var hooks []*ast.BlockStmt
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "AfterAll" {
			return true
		}
		for _, arg := range call.Args {
			if hook, ok := arg.(*ast.FuncLit); ok && calls(hook.Body, "restoreMultiTenantMode") {
				hooks = append(hooks, hook.Body)
			}
		}
		return true
	})
	require.Len(t, hooks, 1, "inspect the real single-tenant cleanup hook")
	restore, rollout, preserve := -1, -1, -1
	for index, statement := range hooks[0].List {
		switch statement := statement.(type) {
		case *ast.ExprStmt:
			if calls(statement, "restoreMultiTenantMode") {
				restore = index
			}
			if calls(statement, "waitForControllerRolloutAndLeader") {
				rollout = index
			}
		case *ast.IfStmt:
			ast.Inspect(statement.Cond, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && literal.Value == `"E2E_SKIP_CLEANUP"` {
					preserve = index
				}
				return true
			})
		}
	}
	require.NotEqual(t, -1, restore, "restore controller mode unconditionally")
	require.NotEqual(t, -1, rollout, "wait for the restored controller before other suites run")
	require.NotEqual(t, -1, preserve, "retain the resource-preservation path")
	require.Less(t, restore, rollout)
	require.Less(t, rollout, preserve, "E2E_SKIP_CLEANUP must preserve resources after restoring controller mode")
}
