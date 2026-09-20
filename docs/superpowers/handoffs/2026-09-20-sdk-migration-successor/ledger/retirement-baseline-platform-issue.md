## Baseline residuals

The one-line replay-test fix for #1879 is intentionally scoped to `TestRetirementStartReplayExecutesOnce`: it joins the registered session senders before the final retirement claim. This issue tracks separate macOS agent-package test failures that reproduce unchanged at both the candidate and parent baselines. It does not claim one shared root cause.

The retirement replay report records the narrowed package failure log at `/tmp/retirement-replay-unchanged-failures-7fbd9a767.log` and the parent-baseline comparison at `/tmp/retirement-replay-unchanged-failures-6cf3f026.log`. The candidate branch is based on `6cf3f0263887914e87dbc70d558979ecea143abb`; the same narrowed run at that parent reports the same four failure families. Source comparison found no changes in the failing evidence or preservation tests; the only changed file is the admission test containing the #1879 replay fix.

### 1. Worktree porcelain assertions

- `TestRetirementAutonomousReLockRetryRefusedRearms` — `agent/session_retirement_evidence_test.go:1037`
- `TestRetirementAutonomousReLock/{claim-first,resume-first,pending-retry,retry-first}` — `agent/session_retirement_evidence_test.go:1115` or `:1174`

Each failure reports `no porcelain entry for ...` even though the emitted porcelain listing contains the expected worktree with its `HEAD` and branch. The candidate and parent logs both reproduce this family.

### 2. macOS `/var` symlink sandbox path

- `TestRetirementSafetyEscalation/{pending,resolved-rerun,pre-attach}` — `agent/session_retirement_evidence_test.go:1331`

Each failure reports `original read did not settle through grant`. The tool result is a sandbox denial because `/var` is a symlink to `private/var`; the test's original denied read therefore does not reach the expected grant-settled state. `claim-first` and `close` pass in the same run. The test should establish the supported path boundary explicitly without weakening the sandbox policy assertion.

### 3. Scratch retention pin manifest

- `TestScratchRetentionTerminalReleaseAllowsCollection` — `agent/session_retirement_preservation_test.go:522`

The sweep repeatedly reports that retention manifests for temporary sandbox paths have no reference to their pin. This is a separate retention/cleanup contract from the porcelain and symlink families.

## Acceptance criteria

Investigate the three mechanisms independently, preserve meaningful lifecycle and safety assertions, and make the narrowed agent-package suite deterministic on macOS. A fix should demonstrate the exact baseline families above no longer fail on the current parent; do not widen timeouts, bypass the `/var` policy, or weaken the retention assertion as a test-only workaround.

This issue is separate from #1879's one-line replay-test fix and should not be used to claim that fix is incomplete.
