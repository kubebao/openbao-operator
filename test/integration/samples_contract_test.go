//go:build integration
// +build integration

package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSamplesPassCurrentCRDAndAdmissionContracts(t *testing.T) {
	policyProbeNamespace := newTestNamespace(t)
	waitForOpenBaoClusterAdmissionPolicies(t, policyProbeNamespace)

	samplesRoot := filepath.Join("..", "..", "config", "samples")
	var samplePaths []string
	if err := filepath.WalkDir(samplesRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
			samplePaths = append(samplePaths, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk samples: %v", err)
	}
	sort.Strings(samplePaths)

	validated := 0
	for _, samplePath := range samplePaths {
		yamlBytes, err := os.ReadFile(samplePath)
		if err != nil {
			t.Fatalf("read sample %s: %v", samplePath, err)
		}

		objects := parseYAMLToUnstructured(t, yamlBytes, func(obj *unstructured.Unstructured) bool {
			if obj.GroupVersionKind().Group != "kubebao.org" {
				return false
			}
			switch obj.GetKind() {
			case "OpenBaoCluster", "OpenBaoRestore", "OpenBaoTenant":
				return true
			default:
				return false
			}
		})
		if len(objects) == 0 {
			continue
		}

		relativePath, err := filepath.Rel(samplesRoot, samplePath)
		if err != nil {
			t.Fatalf("resolve relative sample path %s: %v", samplePath, err)
		}
		t.Run(relativePath, func(t *testing.T) {
			namespace := newTestNamespace(t)
			for _, object := range objects {
				candidate := object.DeepCopy()
				candidate.SetNamespace(namespace)
				if candidate.GetKind() == "OpenBaoTenant" {
					candidate.SetName("sample-tenant")
					if err := unstructured.SetNestedField(
						candidate.Object,
						namespace,
						"spec",
						"targetNamespace",
					); err != nil {
						t.Fatalf("set sample tenant target namespace: %v", err)
					}
				}
				if err := k8sClient.Create(ctx, candidate); err != nil {
					t.Fatalf(
						"sample %s does not satisfy the current CRD and admission policies: %v",
						relativePath,
						err,
					)
				}
			}
		})
		validated += len(objects)
	}

	if validated == 0 {
		t.Fatal("no OpenBao custom-resource samples were validated")
	}
}
