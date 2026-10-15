package apicontract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A manifest classification alone does not make Ginkgo serialize shared
// controller mutations or Gateway API installation. Check the runtime decorators.
func TestUpgradeParallelismPreservesSerialMutators(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "test/e2e/suites.yaml"))
	require.NoError(t, err)
	var manifest struct {
		Lanes []struct {
			ID    string `yaml:"id"`
			Nodes int    `yaml:"parallelNodes"`
		} `yaml:"ciLanes"`
		Suites []struct {
			ID        string   `yaml:"id"`
			Isolation string   `yaml:"isolation"`
			Files     []string `yaml:"files"`
		} `yaml:"suites"`
	}
	require.NoError(t, yaml.Unmarshal(data, &manifest))
	for _, id := range []string{"upgrade-rolling", "upgrade-bluegreen-core"} {
		found := false
		for _, lane := range manifest.Lanes {
			if lane.ID == id {
				found = true
				require.Equal(t, 2, lane.Nodes, id)
			}
		}
		require.True(t, found, id)
	}
	for _, id := range []string{"upgrade-operation-lock", "upgrade-gateway"} {
		found := false
		for _, suite := range manifest.Suites {
			if suite.ID != id {
				continue
			}
			found = true
			require.Equal(t, "serial", suite.Isolation, id)
			for _, file := range suite.Files {
				assertSerialDescribe(t, filepath.Join(root, file), id == "upgrade-operation-lock")
			}
		}
		require.True(t, found, id)
	}
}

func assertSerialDescribe(t *testing.T, path string, ordered bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	containers := 0
	for _, decl := range file.Decls {
		vars, ok := decl.(*ast.GenDecl)
		if !ok || vars.Tok != token.VAR {
			continue
		}
		for _, spec := range vars.Specs {
			for _, value := range spec.(*ast.ValueSpec).Values {
				call, ok := value.(*ast.CallExpr)
				if !ok {
					continue
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok || fn.Name != "Describe" {
					continue
				}
				containers++
				decorators := map[string]bool{}
				for _, arg := range call.Args {
					if id, ok := arg.(*ast.Ident); ok {
						decorators[id.Name] = true
					}
				}
				require.True(t, decorators["Serial"], "%s must retain the worker barrier", path)
				if ordered {
					require.True(t, decorators["Ordered"], "%s must preserve operation-lock case order", path)
				}
			}
		}
	}
	require.Positive(t, containers, path)
}
