# Repository Gate Cleanup Implementation Plan

> Execute this plan on `wip/repo-gate-cleanup` in the isolated
> `.worktrees/repo-gate-cleanup` worktree.

**Goal:** Restore `make test` under the host's ordinary trailing-slash
`TMPDIR`, eliminate the current 39 Go lint findings, and pass the canonical
merge approval gate.

**Constraints:** Keep changes behavior-preserving and minimal. Tests must drive
real runner behavior with fake external process boundaries. Do not add brittle
shell-source or wording assertions.

---

### Task 1: Normalize the module-test runner's owned temp root

**Files:**

- Modify: `scripts/run-module-tests-selftest.sh`
- Modify: `scripts/gate/run-module-tests.sh`

1. Add a self-test case that invokes the real runner with an existing
   trailing-slash `TMPDIR`, records the temp root received by each fake child,
   and rejects a doubled separator.
2. Run `scripts/run-module-tests-selftest.sh` and confirm the new case fails for
   the current runner.
3. Remove the optional trailing slash from the base directory before composing
   the `mktemp` template.
4. Run the self-test again and confirm it passes, including cleanup of all
   successful-run roots.
5. Commit the runner fix and its regression test.

### Task 2: Resolve the root and identifier lint findings

**Files:**

- Modify: `cmd/evener-hub/main.go`
- Modify: `cmd/evener-hub/frontend_hash.go`
- Modify: `runtime_pair_build_test.go`
- Modify: `tools/tool-fluency/cmd/evener-fluency/offline_coverage_test.go`
- Modify: `identifier/project.go`
- Modify: `identifier/project_test.go`

1. Apply the exact configured-linter transformations, propagating
   `printVersionInfo` write failures because that function already returns an
   error.
2. Format touched Go files.
3. Run focused tests for the changed production packages.
4. Run module-scoped lint for the root and identifier modules and confirm both
   are clean.
5. Commit the root and identifier cleanup.

### Task 3: Resolve the agent lint findings

**Files:**

- Modify only the agent files named by the captured 30-finding baseline.

1. Apply mechanical standard-library modernizations and performance forms.
2. Delete the unused `truncateCharsFromTail` wrapper after confirming no call
   sites remain.
3. Format touched Go files.
4. Run focused tests for changed production packages and their direct tests.
5. Run agent-module lint and confirm it is clean.
6. Commit the agent cleanup.

### Task 4: Verify and integrate

1. Run `scripts/run-module-tests-selftest.sh`.
2. Run `make lint` and confirm zero findings in all seven modules.
3. Run ordinary `make test` without rewriting the host `TMPDIR` and confirm all
   streams pass.
4. Run `make merge-approval-gate` and retain its complete verdict.
5. Review the diff for scope, merge the verified branch into `main`, and push
   `main`.
