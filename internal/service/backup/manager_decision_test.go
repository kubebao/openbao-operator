package backup

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
)

func TestDecideBackup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		observation backupObservation
		want        backupDecisionKind
	}{
		{
			name: "observes active owned operation before configuration",
			observation: backupObservation{
				ownsLock: true,
				jobs:     backupJobObservation{hasActive: true, operationActive: true},
			},
			want: backupDecisionObserve,
		},
		{
			name: "finalizes terminal owned operation",
			observation: backupObservation{
				ownsLock: true,
				jobs:     backupJobObservation{mostRecentTerminal: &batchv1.Job{}},
			},
			want: backupDecisionFinalize,
		},
		{
			name: "finalizes owned operation without job",
			observation: backupObservation{
				ownsLock: true,
			},
			want: backupDecisionFinalize,
		},
		{
			name: "idles when backup is not configured",
			observation: backupObservation{
				due: true,
			},
			want: backupDecisionIdle,
		},
		{
			name: "observes active job before blockers",
			observation: backupObservation{
				configured: true,
				due:        true,
				jobs:       backupJobObservation{hasActive: true},
				blocker:    backupBlockedByRestore,
			},
			want: backupDecisionObserve,
		},
		{
			name: "blocks for restore before creation",
			observation: backupObservation{
				configured: true,
				due:        true,
				blocker:    backupBlockedByRestore,
			},
			want: backupDecisionBlocked,
		},
		{
			name: "blocks for precondition before creation",
			observation: backupObservation{
				configured: true,
				due:        true,
				blocker:    backupBlockedByPrecondition,
			},
			want: backupDecisionBlocked,
		},
		{
			name: "creates due backup",
			observation: backupObservation{
				configured: true,
				due:        true,
			},
			want: backupDecisionCreate,
		},
		{
			name: "finalizes terminal job before idle wait",
			observation: backupObservation{
				configured: true,
				jobs:       backupJobObservation{mostRecentTerminal: &batchv1.Job{}},
			},
			want: backupDecisionFinalize,
		},
		{
			name: "idles until next schedule",
			observation: backupObservation{
				configured: true,
			},
			want: backupDecisionIdle,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			decision := decideBackup(test.observation)
			if decision.kind != test.want {
				t.Fatalf("decision kind = %d, want %d", decision.kind, test.want)
			}
		})
	}
}
