package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// groupTransition declares a reviewed API-group move in the API contract. The
// checker compares a baseline CRD in From with the current CRD of the same plural
// in To. It accepts the move only when kind and served versions are unchanged;
// the ordinary schema comparison then applies to every field.
type groupTransition struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Release   string `json:"release"`
	Migration string `json:"migration"`
}

type crdShape struct {
	kind     string
	versions []string
}

func loadGroupTransitions(path string) ([]groupTransition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read API contract %s: %w", path, err)
	}
	var contract struct {
		GroupTransitions []groupTransition `json:"groupTransitions"`
	}
	if err := yaml.Unmarshal(data, &contract); err != nil {
		return nil, fmt.Errorf("parse API contract %s: %w", path, err)
	}
	for _, transition := range contract.GroupTransitions {
		if transition.From == "" || transition.To == "" || transition.Release == "" || transition.Migration == "" {
			return nil, fmt.Errorf("API contract %s: a group transition requires from, to, release, and migration", path)
		}
		if transition.From == transition.To {
			return nil, fmt.Errorf("API contract %s: group transition %s moves to itself", path, transition.From)
		}
	}
	return contract.GroupTransitions, nil
}

// applyGroupTransitions renames the baseline CRDs that each declared transition
// moves, and reports one api-group-moved change per CRD. It rejects a
// declaration that the current CRDs do not satisfy exactly.
func applyGroupTransitions(
	baseline, current []schemaNode,
	transitions []groupTransition,
) ([]schemaNode, []change, error) {
	currentShapes := crdShapes(current)
	var changes []change
	for _, transition := range transitions {
		baselineShapes := crdShapes(baseline)
		moved := make(map[string]string)
		for _, name := range sortedKeys(baselineShapes) {
			plural, ok := strings.CutSuffix(name, "."+transition.From)
			if !ok {
				continue
			}
			if _, served := currentShapes[name]; served {
				return nil, nil, fmt.Errorf(
					"group transition %s -> %s: current CRDs still serve %s",
					transition.From, transition.To, name,
				)
			}
			target := plural + "." + transition.To
			if _, exists := baselineShapes[target]; exists {
				return nil, nil, fmt.Errorf(
					"group transition %s -> %s: baseline already contains %s",
					transition.From, transition.To, target,
				)
			}
			want := baselineShapes[name]
			got, ok := currentShapes[target]
			if !ok {
				return nil, nil, fmt.Errorf(
					"group transition %s -> %s: current CRDs have no %s for %s",
					transition.From, transition.To, target, name,
				)
			}
			if got.kind != want.kind || strings.Join(got.versions, ",") != strings.Join(want.versions, ",") {
				return nil, nil, fmt.Errorf(
					"group transition %s -> %s: %s serves %s %v, but %s served %s %v",
					transition.From, transition.To, target, got.kind, got.versions, name, want.kind, want.versions,
				)
			}
			moved[name] = target
			changes = append(changes, change{
				Impact:         impactMigration,
				Classification: "api-group-moved",
				Kind:           want.kind,
				Version:        strings.Join(want.versions, ","),
				Path:           target,
				Detail: fmt.Sprintf(
					"%s moves to %s in %s; existing resources migrate with %s",
					name, target, transition.Release, transition.Migration,
				),
			})
		}
		if len(moved) == 0 {
			continue
		}
		renamed := make([]schemaNode, len(baseline))
		for i, node := range baseline {
			if target, ok := moved[node.CRD]; ok {
				node.CRD = target
			}
			renamed[i] = node
		}
		baseline = renamed
	}
	return baseline, changes, nil
}

func crdShapes(nodes []schemaNode) map[string]crdShape {
	versions := make(map[string]map[string]bool)
	kinds := make(map[string]string)
	for _, node := range nodes {
		if versions[node.CRD] == nil {
			versions[node.CRD] = make(map[string]bool)
		}
		versions[node.CRD][node.Version] = true
		kinds[node.CRD] = node.Kind
	}
	shapes := make(map[string]crdShape, len(kinds))
	for name, kind := range kinds {
		shapes[name] = crdShape{kind: kind, versions: sortedKeys(versions[name])}
	}
	return shapes
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
