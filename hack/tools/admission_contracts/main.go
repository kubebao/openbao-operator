// Command admissioncontracts embeds the release's admission specifications.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

type specification struct {
	Policy  admissionv1.ValidatingAdmissionPolicySpec        `json:"policy"`
	Binding admissionv1.ValidatingAdmissionPolicyBindingSpec `json:"binding"`
}

type contract struct {
	specification
	Kustomize *specification `json:"kustomize"`
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := os.Chdir(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	entries, err := filepath.Glob("config/policy/*.yaml")
	if err != nil {
		return err
	}
	contracts := map[string]contract{}
	bindings := map[string]admissionv1.ValidatingAdmissionPolicyBindingSpec{}
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			var policy admissionv1.ValidatingAdmissionPolicy
			if err := yaml.UnmarshalStrict(data, &policy); err != nil {
				return fmt.Errorf("decode %s: %w", path, err)
			}
			contracts[policy.Name] = contract{specification: specification{Policy: policy.Spec}}
		case "ValidatingAdmissionPolicyBinding":
			var binding admissionv1.ValidatingAdmissionPolicyBinding
			if err := yaml.UnmarshalStrict(data, &binding); err != nil {
				return fmt.Errorf("decode %s: %w", path, err)
			}
			if _, exists := bindings[binding.Spec.PolicyName]; exists {
				return fmt.Errorf("duplicate binding for %s", binding.Spec.PolicyName)
			}
			bindings[binding.Spec.PolicyName] = binding.Spec
		}
	}
	for name, item := range contracts {
		binding, ok := bindings[name]
		if !ok {
			return fmt.Errorf("missing binding for %s", name)
		}
		item.Binding = binding
		contracts[name] = item
		delete(bindings, name)
	}
	if len(contracts) == 0 || len(bindings) != 0 {
		return fmt.Errorf("policy and binding inventory do not match")
	}
	variants, err := renderKustomizeContracts()
	if err != nil {
		return err
	}

	if len(variants) != len(contracts) {
		return fmt.Errorf("rendered admission inventory differs from source inventory")
	}
	for name, item := range contracts {
		variant := variants[name]
		if variant == nil || variant.Policy.MatchConstraints == nil || variant.Binding.PolicyName != name {
			return fmt.Errorf("incomplete Kustomize admission contract for %s", name)
		}
		item.Kustomize = variant
		contracts[name] = item
	}
	data, err := json.MarshalIndent(contracts, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile("internal/platform/admission/zz_generated.contracts.json", data, 0o644)
}

func renderKustomizeContracts() (map[string]*specification, error) {
	// Render only local repository files. This command does not contact an API
	// server or read kubeconfig. Keep the renderer pinned by the Make target.
	rendered, err := exec.Command("kustomize", "build", "config/default").Output()
	if err != nil {
		return nil, fmt.Errorf("render Kustomize admission contracts: %w", err)
	}
	variants := map[string]*specification{}
	decoder := k8syaml.NewYAMLToJSONDecoder(bytes.NewReader(rendered))
	for {
		var document json.RawMessage
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(document, &meta); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(meta.Metadata.Name, "openbao-operator-")
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			var policy admissionv1.ValidatingAdmissionPolicy
			if err := json.Unmarshal(document, &policy); err != nil {
				return nil, err
			}
			if variants[name] == nil {
				variants[name] = &specification{}
			}
			variants[name].Policy = policy.Spec
		case "ValidatingAdmissionPolicyBinding":
			var binding admissionv1.ValidatingAdmissionPolicyBinding
			if err := json.Unmarshal(document, &binding); err != nil {
				return nil, err
			}
			name = strings.TrimPrefix(binding.Spec.PolicyName, "openbao-operator-")
			if variants[name] == nil {
				variants[name] = &specification{}
			}
			binding.Spec.PolicyName = name
			variants[name].Binding = binding.Spec
		}
	}
	return variants, nil
}
