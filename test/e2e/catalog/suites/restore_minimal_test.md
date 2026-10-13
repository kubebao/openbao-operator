# Minimal fresh restore and scheduled restore tests

Source: `test/e2e/restore_minimal_test.go`

Note: recorded checkpoints are best-effort extracts from literal `By(...)` calls visible to `ginkgo outline`.

## Cases

| Case ID | Spec | State | Covers | Labels |
| --- | --- | --- | --- | --- |
| `restore-minimal-cancel` | cleans a cancelled disposable target and its data volume | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |
| `restore-minimal-missing-source` | cleans a disposable target when the source object is missing | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |
| `restore-minimal-identity` | does not confirm application when the source identity differs | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |
| `restore-minimal-digest` | rejects changed snapshot bytes before submission | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |
| `restore-minimal-static` | restores a static-sealed snapshot with the original key and removes the disposable target | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |
| `restore-minimal-retain` | retains an applied target until the administrator accepts a paused handoff | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |
| `restore-minimal-scheduled` | reuses a reader account across scheduled restores and removes disposable targets | active | _none_ | `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor` |

## `restore-minimal-cancel`

Path: `Minimal fresh restore and scheduled restore tests > cleans a cancelled disposable target and its data volume`

State: `active`

Generated fallback ID: `restore-minimal-cleans-a-cancelled-disposable-target-and-e2b004fa`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`

Recorded checkpoints:
- withdrawing approval without blocking cleanup of the admitted target


## `restore-minimal-missing-source`

Path: `Minimal fresh restore and scheduled restore tests > cleans a disposable target when the source object is missing`

State: `active`

Generated fallback ID: `restore-minimal-cleans-a-disposable-target-when-the-c26b3f85`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`


## `restore-minimal-identity`

Path: `Minimal fresh restore and scheduled restore tests > does not confirm application when the source identity differs`

State: `active`

Generated fallback ID: `restore-minimal-does-not-confirm-application-when-the-8f108f5b`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`


## `restore-minimal-digest`

Path: `Minimal fresh restore and scheduled restore tests > rejects changed snapshot bytes before submission`

State: `active`

Generated fallback ID: `restore-minimal-rejects-changed-snapshot-bytes-before-submission-029fe870`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`


## `restore-minimal-static`

Path: `Minimal fresh restore and scheduled restore tests > restores a static-sealed snapshot with the original key and removes the disposable target`

State: `active`

Generated fallback ID: `restore-minimal-restores-a-static-sealed-snapshot-with-bdf5828c`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`

Recorded checkpoints:
- backing up a source with an operator-generated static key
- preparing the original key as an administrator-owned destination Secret


## `restore-minimal-retain`

Path: `Minimal fresh restore and scheduled restore tests > retains an applied target until the administrator accepts a paused handoff`

State: `active`

Generated fallback ID: `restore-minimal-retains-an-applied-target-until-the-5e6efe95`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`


## `restore-minimal-scheduled`

Path: `Minimal fresh restore and scheduled restore tests > reuses a reader account across scheduled restores and removes disposable targets`

State: `active`

Generated fallback ID: `restore-minimal-reuses-a-reader-account-across-scheduled-38eda393`

Covers: _none_

Labels: `restore-minimal`, `dr`, `backup`, `restore`, `e2e-anchor`

Recorded checkpoints:
- preparing an administrator-owned reader account and separate destination credential reference
- refusing manual and scheduled target creation before namespace approval
- approving the prepared namespace and completing the restore test
- reusing the same reader account for a second generated target after cleanup
- taking a new backup with the writer before the second disposable restore
