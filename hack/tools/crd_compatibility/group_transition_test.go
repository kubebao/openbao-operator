package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var openBaoToKubeBao = []groupTransition{{
	From: "openbao.org", To: "kubebao.org", Release: "0.7.0", Migration: "cmd/api-migration",
}}

func groupNodes(group string) []schemaNode {
	return []schemaNode{
		{CRD: "openbaoclusters." + group, Kind: "OpenBaoCluster", Version: "v1alpha1", Path: "spec", Type: "object"},
		{
			CRD: "openbaoclusters." + group, Kind: "OpenBaoCluster", Version: "v1alpha1",
			Path: "spec.replicas", Type: "integer",
		},
		{CRD: "openbaotenants." + group, Kind: "OpenBaoTenant", Version: "v1alpha1", Path: "spec", Type: "object"},
	}
}

func TestGroupTransitionMapsDeclaredCRDs(t *testing.T) {
	t.Parallel()

	baseline, moves, err := applyGroupTransitions(groupNodes("openbao.org"), groupNodes("kubebao.org"), openBaoToKubeBao)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 {
		t.Fatalf("want one move per CRD, got %#v", moves)
	}
	for _, move := range moves {
		if move.Impact != impactMigration || move.Classification != "api-group-moved" {
			t.Fatalf("unexpected move %#v", move)
		}
	}
	changes := append(moves, compareSnapshots(baseline, groupNodes("kubebao.org"))...)
	if hasBlockingChanges(changes) {
		t.Fatalf("declared move with identical schemas must pass: %#v", changes)
	}
}

func TestGroupTransitionKeepsSchemaEnforcement(t *testing.T) {
	t.Parallel()

	current := groupNodes("kubebao.org")[:2]
	current = append(current, schemaNode{
		CRD: "openbaotenants.kubebao.org", Kind: "OpenBaoTenant", Version: "v1alpha1", Path: "spec", Type: "string",
	})
	baseline, _, err := applyGroupTransitions(groupNodes("openbao.org"), current, openBaoToKubeBao)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, compareSnapshots(baseline, current), impactBreaking, "type-changed", "spec")
}

func TestGroupTransitionWithoutDeclarationRemovesFields(t *testing.T) {
	t.Parallel()

	baseline, moves, err := applyGroupTransitions(groupNodes("openbao.org"), groupNodes("kubebao.org"), nil)
	if err != nil || len(moves) != 0 {
		t.Fatalf("no declaration must not map CRDs: moves=%#v err=%v", moves, err)
	}
	changes := compareSnapshots(baseline, groupNodes("kubebao.org"))
	assertChange(t, changes, impactBreaking, "field-removed", "spec.replicas")
}

func TestGroupTransitionRejectsMismatchedDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		baseline []schemaNode
		current  []schemaNode
		want     string
	}{
		{
			name:     "destination group does not match",
			baseline: groupNodes("openbao.org"),
			current:  groupNodes("example.org"),
			want:     "current CRDs have no openbaoclusters.kubebao.org",
		},
		{
			name:     "source group still served",
			baseline: groupNodes("openbao.org"),
			current:  append(groupNodes("kubebao.org"), groupNodes("openbao.org")...),
			want:     "current CRDs still serve openbaoclusters.openbao.org",
		},
		{
			name:     "baseline already has the destination",
			baseline: append(groupNodes("openbao.org"), groupNodes("kubebao.org")...),
			current:  groupNodes("kubebao.org"),
			want:     "baseline already contains openbaoclusters.kubebao.org",
		},
		{
			name:     "kind changed",
			baseline: groupNodes("openbao.org"),
			current: func() []schemaNode {
				nodes := groupNodes("kubebao.org")
				for i := range nodes[:2] {
					nodes[i].Kind = "BaoCluster"
				}
				return nodes
			}(),
			want: "serves BaoCluster",
		},
		{
			name:     "served versions changed",
			baseline: groupNodes("openbao.org"),
			current: append(groupNodes("kubebao.org"), schemaNode{
				CRD: "openbaotenants.kubebao.org", Kind: "OpenBaoTenant", Version: "v1beta1", Path: "spec", Type: "object",
			}),
			want: "[v1alpha1 v1beta1]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := applyGroupTransitions(tt.baseline, tt.current, openBaoToKubeBao)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadGroupTransitionsRequiresCompleteDeclarations(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing migration": "groupTransitions:\n- {from: openbao.org, to: kubebao.org, release: 0.7.0}\n",
		"moves to itself":   "groupTransitions:\n- {from: a.org, to: a.org, release: 0.7.0, migration: cmd/x}\n",
	}
	for name, contract := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "contract.yaml")
			if err := os.WriteFile(path, []byte(contract), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadGroupTransitions(path); err == nil {
				t.Fatal("incomplete declaration was accepted")
			}
		})
	}
}

// The repository contract declares exactly the reviewed openbao.org move.
func TestRepositoryContractDeclaresAPIGroupMove(t *testing.T) {
	t.Parallel()

	transitions, err := loadGroupTransitions(filepath.Join("..", "..", "..", defaultContractPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 || transitions[0] != openBaoToKubeBao[0] {
		t.Fatalf("unexpected group transitions: %#v", transitions)
	}
}
