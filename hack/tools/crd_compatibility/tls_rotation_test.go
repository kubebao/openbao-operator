package main

import "testing"

func TestTLSRotationPeriodDiagnosticFixCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*schemaNode, *schemaNode)
		blocked bool
	}{
		{"exact presence guard", nil, false},
		{"different rule", func(_, current *schemaNode) {
			current.CEL[0].Rule = "self.tls.mode != 'OperatorManaged' || has(self.tls.rotationPeriod)"
		}, true},
		{"different metadata", func(_, current *schemaNode) {
			current.CEL[0].OptionalOldSelf = "true"
		}, true},
		{"additional rule change", func(_, current *schemaNode) {
			current.CEL = append(current.CEL, celRule{Rule: "self.replicas > 1"})
		}, true},
		{"different resource", func(old, current *schemaNode) {
			old.CRD, current.CRD = "widgets.example.com", "widgets.example.com"
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			old := schemaNode{
				CRD:     "openbaoclusters.kubebao.org",
				Kind:    "OpenBaoCluster",
				Version: "v1alpha1",
				Path:    "spec",
				Type:    "object",
				CEL: []celRule{{
					Rule:    "self.tls.mode != 'OperatorManaged' || size(self.tls.rotationPeriod) > 0",
					Message: "spec.tls.rotationPeriod is required when spec.tls.mode is OperatorManaged",
				}},
			}
			current := old
			current.CEL = append([]celRule(nil), old.CEL...)
			current.CEL[0].Rule = "self.tls.mode != 'OperatorManaged' || " +
				"(has(self.tls.rotationPeriod) && size(self.tls.rotationPeriod) > 0)"
			if tt.mutate != nil {
				tt.mutate(&old, &current)
			}

			changes := compareSnapshots([]schemaNode{old}, []schemaNode{current})
			if hasBlockingChanges(changes) != tt.blocked {
				t.Fatalf("blocking changes = %t, want %t; changes: %#v", hasBlockingChanges(changes), tt.blocked, changes)
			}
			if !tt.blocked {
				assertChange(t, changes, impactCompatible, "cel-diagnostic-fix", "spec")
			}
		})
	}
}
