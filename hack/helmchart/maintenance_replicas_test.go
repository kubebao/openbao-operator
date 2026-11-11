package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	syaml "sigs.k8s.io/yaml"
)

func TestHelmMaintenanceReplicas(t *testing.T) {
	for _, mode := range []string{"multi", "single"} {
		for _, replicas := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/%d", mode, replicas), func(t *testing.T) {
				rendered := renderChart(t, "--set", "tenancy.mode="+mode,
					"--set", "tenancy.targetNamespace=openbao",
					"--set", fmt.Sprintf("controller.replicas=%d", replicas),
					"--set", fmt.Sprintf("provisioner.replicas=%d", replicas))
				count := 0
				for _, document := range bytes.Split(rendered, []byte("\n---")) {
					var object struct {
						Kind string `json:"kind"`
						Spec struct {
							Replicas *int `json:"replicas"`
						} `json:"spec"`
					}
					if err := syaml.Unmarshal(document, &object); err != nil {
						t.Fatal(err)
					}
					if object.Kind != "Deployment" {
						continue
					}
					count++
					if object.Spec.Replicas == nil || *object.Spec.Replicas != replicas {
						t.Fatalf("Deployment must retain requested replicas=%d", replicas)
					}
				}
				want := 2
				if mode == "single" {
					want = 1
				}
				if count != want {
					t.Fatalf("rendered %d Deployments, want %d", count, want)
				}
			})
		}
	}
}

func TestHelmRejectsInvalidMaintenanceValues(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not available")
	}
	for _, value := range []string{
		"controller.replicas=-1",
		"provisioner.replicas=-1",
		"healthProbes.readinessPeriodSeconds=0",
	} {
		t.Run(value, func(t *testing.T) {
			chart := filepath.Join("..", "..", "charts", "openbao-operator")
			cmd := exec.CommandContext(t.Context(), "helm", "template", "test", chart,
				"--namespace", "openbao", "--set", value)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "minimum") {
				t.Fatalf("%s must fail schema validation: error=%v\n%s", value, err, output)
			}
		})
	}
}
