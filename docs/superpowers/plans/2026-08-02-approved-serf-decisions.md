# Approved Serf Decisions Implementation Plan

> For the implementation agent: execute this plan in the current worktree. Do
> not broaden it into SDD, Kata, or launcher work.

**Goal:** Implement the Serf-owned portions of the approved decision record in
`docs/superpowers/specs/2026-08-02-approved-serf-decisions-design.md`, using
small behavioral tests and preserving already-landed behavior.

**Constraints:** Read `AGENTS.md` and `docs/testing.md` first. Preserve
unrelated changes. Keep human-readable delegate handoffs unparsed and
unstructured. Do not add automatic cleanup, artifact copying, retry/resume,
registries, locks, dependency installation, SDD/Kata semantics, or launcher
configuration. The public transcript reader must remain deterministic and the
API-log redaction projection must remain intact.

## Task 1: bounded brace-pattern helper

**Files:**

- Add `agent/internal/globpattern/expand.go` and focused tests.
- Update `agent/execenv/local.go` and `agent/execenv/securepath_browse.go`.
- Update the native grep path and ripgrep argument construction tests.
- Update `agent/internal/tool/definitions.go` descriptions and relevant
  `agent/execenv` tests.

**Behavior:** Expand nested `{a,b}` alternatives with a fixed cap, reject
malformed braces, preserve ordinary glob syntax and valid no-match behavior,
and apply the same expansion to file globbing and grep filters in both
sandboxed and unsandboxed execution. Pass each ripgrep alternative as its own
`-g` argument. Return expansion errors instead of silently ignoring malformed
filters.

**Tests:** Start with failing helper tests for simple, nested, malformed, and
cap cases; then add integration coverage for `glob` and native/ripgrep-style
grep filters.

## Task 2: one public transcript reader

**Files:**

- Update `agent/session_tools_transcript.go` and
  `agent/internal/tool/definitions.go`.
- Update `agent/prompts/sections/transcripts.md`, compaction/context prompt
  references, and transcript definition tests.
- Preserve the internal historical API-log redaction/projection tests.

**Behavior:** Register only `read_transcript` and
`find_session_transcripts`; route session and job refs through
`read_transcript`; remove public API-log options and `max_bytes`; use fixed
16 KiB expansion pages with continuation offsets; retain clear invalid-request
errors for unsupported public arguments. Keep any private doctor/internal
API-log machinery needed by existing forensic behavior out of the callable
model surface.

**Tests:** Add a public-definition assertion that the removed fields and old
tool are absent, verify both ref kinds still read, verify fixed-page
continuation behavior, and verify API-log-shaped public calls are rejected
without opening the transcript. Keep historical projection coverage.

## Task 3: scratch handoff and retention

**Files:**

- Update `agent/session_prompts.go` and its focused tests.
- Update `agent/execenv/local.go`, `agent/session_lifecycle.go`, and
  scratch-lifecycle tests only as needed to stop normal teardown from removing
  a handed-off scratch directory.

**Behavior:** Preserve the existing per-sandbox-session scratch capability and
read-only write boundary. Add final-handoff instructions naming absolute
scratch/artifact paths and manual cleanup. Do not add a structured result field
or parser. Retain cleanup as an explicit/manual operation and keep setup
rollback for allocations never handed to a delegate.

**Tests:** Assert the prompt contains the absolute path, write boundary, and
handoff instructions. Assert normal environment/session teardown leaves a
handed-off scratch directory available until explicitly cleaned.

## Task 4: worktree and environment evidence

**Files:**

- Update `agent/job_delegate.go`, `agent/session_tools_jobs.go`, and
  `agent/job_notify.go` for exact observed `HEAD` reporting.
- Add or update focused worktree-report tests.
- Add the one fresh-worktree dependency sentence to the focused delegation
  prompt, if it is not already present.

**Behavior:** Report the exact observed revision alongside existing worktree
  provenance. Keep existing distinct terminal statuses and separate teardown
  wording. Do not create a new lifecycle schema when existing status/reason,
  phase, output, and prose already carry the approved evidence. Leave PATH
  setup and dependency installation to the caller/launcher/worktree manager.

## Verification and handoff

1. Run focused tests after each task and commit each coherent slice.
2. Run `go test ./...` in this worktree after implementation.
3. Review the diff for scope contamination and verify the main worktree's
   unrelated deletion remains untouched.
4. Add substantive implementation/evidence comments to only the relevant open
   Katas; do not close unfinished Katas.
5. Request a read-only `gpt-5.6-sol` reviewer at maximum reasoning effort. The
   reviewer must inspect the spec first, then this plan, then the code/tests,
   report findings with exact paths/lines, and give an explicit verdict.

