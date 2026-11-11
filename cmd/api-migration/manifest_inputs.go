package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

// Operator checkpoints must not create CRs before import or recreate retired
// source objects. Render configured inputs without applying anything, then
// reject workload CRs and GitOps controllers that could resume reconciliation.
func (p *Procedure) validateOperatorInputs(ctx context.Context) error {
	for _, input := range []struct{ label, path string }{
		{"sourceManifests", p.plan.Config.SourceManifests},
		{"destinationManifests", p.plan.Config.DestinationManifests},
	} {
		if err := validateOperatorInput(ctx, input.label, input.path); err != nil {
			return err
		}
	}
	return nil
}

func validateOperatorInput(ctx context.Context, label, path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	var data []byte
	if info.IsDir() {
		data, err = exec.CommandContext(ctx, "kubectl", "kustomize", path).Output()
		if err != nil {
			// Renderer output can contain Secret values; do not echo it.
			return fmt.Errorf("render %s: %w; check the operator-only input with kubectl kustomize", label, err)
		}
	} else if data, err = os.ReadFile(path); err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	if err := validateOperatorManifestData(data); err != nil {
		return fmt.Errorf("%s must contain only the operator installation: %w", label, err)
	}
	return nil
}

func validateOperatorManifestData(data []byte) error {
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var inspect func(map[string]any) error
	inspect = func(object map[string]any) error {
		u := &unstructured.Unstructured{Object: object}
		if u.IsList() || strings.HasSuffix(u.GetKind(), "List") {
			items, found, err := unstructured.NestedSlice(object, "items")
			if err != nil || !found {
				return errors.New("a manifest list must contain an items array")
			}
			for _, item := range items {
				child, ok := item.(map[string]any)
				if !ok {
					return errors.New("a manifest list contains a non-object item")
				}
				if err := inspect(child); err != nil {
					return err
				}
			}
			return nil
		}
		switch u.GetKind() {
		case clusterKind, tenantKind, restoreKind:
			return fmt.Errorf("found %s %s/%s; keep OpenBao resources out of checkpoint inputs", u.GetKind(), u.GetNamespace(), u.GetName())
		}
		switch u.GroupVersionKind().Group {
		case argoApplication.Group, fluxKustomization.Group, fluxHelmRelease.Group, "source.toolkit.fluxcd.io":
			return fmt.Errorf("found GitOps object %s %s/%s; keep controllers suspended and apply only operator resources", u.GetKind(), u.GetNamespace(), u.GetName())
		}
		return nil
	}
	for {
		var object map[string]any
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return errors.New("cannot decode the manifests; check the rendered operator input")
		}
		if err := inspect(object); err != nil {
			return err
		}
	}
}
