# WS9: serf-doctor Batch Study Tooling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The next fleet-wide session study is one command plus judgment on
flagged sessions: serf-doctor gains session enumeration, per-session
mechanical health metrics, and a batch runbook driver emitting deduped
Findings.

**Architecture:** Three new read-only surfaces over the existing canonical
readers (`transcript`, `apilog`, `jobstore` folds, `schema.SessionMeta`) —
never hand-parsed JSONL, per the doctoring-serf hard gate. Everything
supports `--json`. New runbooks codify the 2026-08-05 study's detectors.

**Tech stack:** Go: `agent/doctor` (analysis), `cmd/serf-doctor` (CLI),
`internal/bundled/skills/doctoring-serf` (runbooks + skill doc). Fixture
session dirs use the `--state-dir` scratch-root shape (state dir *is* the
bucket; see `references/data-model.md`).

**Context (verified):** existing subcommands and selector handling live in
`cmd/serf-doctor/main.go` (e.g. `cmdAPILog` at :271); analysis in
`agent/doctor/` (e.g. `apilog.go:116`). Session metas enumerate via
`schema.ListSessionMetas(bucketDir)`; transcripts read via the `transcript`
package; jobs via `jobstore` folds (`Fold`, `FoldWatches`, raw terminal
scan). The Finding contract and runbook format are
`internal/bundled/skills/doctoring-serf/references/finding-contract.md` and
`writing-runbooks.md` — read both before Tasks 3 and 5.

## Global Constraints

- Read-only: no doctor command mutates session state, ever.
- Healthy ⇒ zero findings; a metric is not a finding. `--health` prints
  metrics; only `audit` emits Findings, and only confirmed, actionable ones.
- Every new command: `--json` structured output, `--state-dir` support, and
  selector semantics identical to existing subcommands (bare id searched
  across buckets).
- Unparseable input is a loud error naming the file — never a confident
  zero (the study's central lesson).
- Golden/fixture tests use synthetic sessions written through serf's own
  writer types where practical; never copy real session content into the
  repo.
- Multi-module gates before every commit.

---

### Task 1: `serf-doctor sessions` — enumeration

**Files:**
- Create: `agent/doctor/sessions.go`, `agent/doctor/sessions_test.go`
- Modify: `cmd/serf-doctor/main.go` (subcommand + help)

**Interfaces:**
- Produces: `doctor.ListSessions(opts)` returning per-session rows:
  session id, bucket, started/last-activity timestamps (from transcript
  header + file mtimes), model(s) (meta + any mid-session switches visible
  in meta), turn count, transcript bytes, `IsSubagent`,
  `ParentSessionID`, delegate/observer counts (meta `ObservedBy`, jobs
  fold), and an outcome hint: the final `communicate`-family call's
  end_turn/status when the last assistant turn carries one, else `none`.
  CLI: `serf-doctor sessions [--since DUR] [--bucket B | --all] [--json]`,
  sorted by last activity, human table by default.
- Consumes: `schema.ListSessionMetas`, transcript reader, existing bucket
  resolution used by `locate`.

- [ ] **Step 1: Failing test** over a fixture state dir with three synthetic
  sessions (one subagent child, one outside the `--since` window): assert
  filtering, ordering, parent linkage, and the outcome hint.
- [ ] **Step 2: Implement; test green.**
- [ ] **Step 3: CLI wiring + help text; `--json` snapshot test.**
- [ ] **Step 4: Commit** (`feat(serf-doctor): sessions enumeration for batch studies`).

### Task 2: `serf-doctor transcript --health` — mechanical per-session metrics

**Files:**
- Create: `agent/doctor/health.go`, `agent/doctor/health_test.go`
- Modify: `cmd/serf-doctor/main.go` (flag on the transcript subcommand)

**Interfaces:**
- Produces: `doctor.TranscriptHealth(sel)` → struct with:
  - `tool_calls` / `tool_errors` by tool name; errors sub-keyed by a coarse
    error class (schema-rejection / not-found / denied / timeout / other,
    classified from result text markers — document each marker in a table
    in the code);
  - `longest_identical_run`: length, tool, whether the run's results were
    errors (signature = tool name + SHA256[:8] of arguments, matching the
    runtime's loop-detector signature at `agent/session_tool_round.go:325`);
  - `truncation_warnings`: count of tool results containing the registry
    truncation banner (`agent/internal/tool/registry.go:596`);
  - `steering` counts by kind (the `events.SteeringKind*` vocabulary);
  - `jobs`: counts by terminal reason (`run_timeout`, etc.) and
    zero-output-bytes terminal jobs (jobstore folds);
  - `stale_notifications`: notification steering turns after the final
    end_turn=true communicate;
  - `user_corrections`: USER_INPUT/STEERING turns that follow a completed
    end_turn=true communicate (proxy metric; named as such in output).
- Consumes: Task 1's session resolution helpers where shared; transcript +
  jobstore readers.

- [ ] **Step 1: Failing tests** with a synthetic transcript exercising each
  metric (a 4-call identical failing run, one truncated result, one stale
  notification, one post-done user message) and a jobs.jsonl with one
  run_timeout zero-output job.
- [ ] **Step 2: Implement; green.**
- [ ] **Step 3: CLI flag + `--json`; human output is a compact table.**
- [ ] **Step 4: Commit** (`feat(serf-doctor): transcript --health mechanical session metrics`).

### Task 3: `serf-doctor audit` — batch runbook driver

**Files:**
- Create: `agent/doctor/audit.go`, `agent/doctor/audit_test.go`
- Modify: `cmd/serf-doctor/main.go`

**Interfaces:**
- Produces: `serf-doctor audit --runbook NAME --sessions <sel,...> |
  --since DUR [--json]`. Read
  `references/finding-contract.md` first; Findings emitted must satisfy it
  (signature, severity, category, title, description, evidence with the
  reproducing serf-doctor invocation, suggestedFix routing). The driver:
  resolves the session set (reusing Task 1), runs the runbook's mechanical
  checks per session, dedups Findings by `signature` across sessions
  (one Finding, N affected sessions listed in evidence), and prints a
  summary table (pattern × session count) plus the Finding JSON.
- Mechanical check vocabulary (this is the runbook-executable subset —
  prose steps in a runbook are for LLM operators and are listed as
  `manual` in the driver's output, never silently skipped): thresholds over
  Task 2 health metrics and apilog summary fields, expressed in the runbook
  markdown as a fenced `audit:` YAML block (`metric`, `op`, `value`,
  `severity`, `title`). Define the block schema in
  `writing-runbooks.md` as part of this task.
- Consumes: Tasks 1 + 2.

- [ ] **Step 1: Failing test:** a fixture runbook with two checks
  (`jobs.run_timeout >= 5` high, `longest_identical_run.errors && length >= 3`
  medium) over a two-session fixture set where one session trips both;
  assert dedup, the summary table, and contract-valid Finding JSON.
- [ ] **Step 2: Implement; green.**
- [ ] **Step 3: Document the `audit:` block in `writing-runbooks.md`.**
- [ ] **Step 4: Commit** (`feat(serf-doctor): audit — batch runbook driver with deduped findings`).

### Task 4: `serf-doctor apilog --health`

**Files:**
- Modify: `agent/doctor/apilog.go`, `cmd/serf-doctor/main.go`
- Test: extend `agent/doctor` apilog tests

**Interfaces:**
- Produces: one-line verdict + `--json`: attempts, empty count (labeled
  `recorded_empty` — and when WS1's `--recompute` has merged, a
  `recomputed_nonempty` figure; if WS1 has not merged when this task runs,
  emit `recorded_empty` with a caveat string naming the WS1 plan, and leave
  a `TODO(ws1)`-free seam: the field list is additive), retry-storm count
  (attempt groups with ≥3 attempts), unsettled groups, error counts by
  class (quota / permanent / retryable).
- Consumes: existing apilog summarization internals.

- [ ] **Step 1: Failing test** over a fixture log with a 4-attempt group,
  an unsettled tail, and a 403; assert the verdict fields.
- [ ] **Step 2: Implement; green; commit**
  (`feat(serf-doctor): apilog --health one-line session API verdict`).

### Task 5: Study-pattern runbooks + skill doc update

**Files:**
- Create: `internal/bundled/skills/doctoring-serf/runbooks/error-loop.md`,
  `runbooks/stale-notification.md`, `runbooks/run-timeout-waste.md`,
  `runbooks/truncation-waste.md`
- Modify: `internal/bundled/skills/doctoring-serf/SKILL.md` (tool table:
  sessions / --health / audit rows; caveat that pre-WS1 logs need
  `--recompute` for apilog empties)

**Interfaces:**
- Consumes: Task 3's `audit:` block schema; `writing-runbooks.md` (read it
  first — runbook authoring rules apply).
- Produces: each runbook defines its mechanical `audit:` checks (thresholds
  chosen from the study's observed distributions: identical-error run ≥3;
  run_timeout jobs ≥5 per session; truncation warnings ≥3; ≥2 stale
  notifications) plus the prose diagnosis/repair guidance and the Finding
  routing per the contract.

- [ ] **Step 1: Write the four runbooks; run each through
  `serf-doctor audit` against the Task 3 fixtures (extend fixtures per
  runbook so each has one tripping and one healthy session).**
- [ ] **Step 2: Update SKILL.md's command table.**
- [ ] **Step 3: Gates green; commit**
  (`feat(doctoring-serf): standing runbooks for the session-study failure patterns`).

## Acceptance (whole workstream)

- `serf-doctor sessions --since 120h --json | wc -l` reproduces the study's
  464-session enumeration in one command (manual check against local state).
- `serf-doctor audit --runbook error-loop --since 120h` surfaces the known
  loop sessions (034163AU8MmLapfXKT7nMu at minimum) with contract-valid
  Findings.
- All commands loud-error on unreadable files; zero-finding healthy fixture
  stays zero across all four runbooks.
