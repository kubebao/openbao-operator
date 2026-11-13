package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDocumentationPublicationRequiresPublishedStable(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		job      string
	}{
		{workflow: "pages.yml", job: "deploy"},
		{workflow: "release.yml", job: "deploy-docs"},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", tc.workflow))
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Jobs map[string]struct {
					Steps []codeQLStep `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			checked := false
			published := false
			for _, step := range workflow.Jobs[tc.job].Steps {
				if step.Run == "bash hack/docs/check-published-stable.sh" {
					if step.ID != "stable" || step.Env["GH_TOKEN"] != "${{ github.token }}" || step.If != "" {
						t.Fatal("documentation publication must check stable release eligibility unconditionally")
					}
					checked = true
				}
				if strings.Contains(step.Run, "bash hack/docs/publish-gh-pages.sh") {
					if !checked || step.If != "steps.stable.outputs.published == 'true'" {
						t.Fatal("documentation publisher must wait for the configured stable release to be published")
					}
					published = true
				}
			}
			if !published {
				t.Fatal("documentation publisher step not found")
			}
		})
	}
}
