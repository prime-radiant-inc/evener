# WS3: Job Ergonomics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The notification-driven job model works as its contract promises:
promotions read as "still running," watches can't silently go blind, and
notifications arrive exactly once, promptly, even to idle sessions.

**Architecture:** Implements the rescoped WS3 section of
`docs/superpowers/plans/2026-08-06-agentic-ux-remediation.md` (rewritten
2026-08-06 after auditing `docs/job-control.md`, which is normative and
binding). Explicitly out of scope by decision: NO `job_wait` tool (the
contract rejects a separate wait tool), NO `max_runtime_ms` schema exposure
(removed deliberately 2026-08-05 after agent abuse).

**Tech stack:** Go, `agent` module (job_shell, job_watch, jobs, jobstore,
session_tools_shell, session_tools_jobs, server notify path). Anchors
verified 2026-08-06; trust symbol names over line numbers.

**Sequencing:** launches only after WS2 (dispatch breaker) merges — both
touch the tool-round path.

## Global Constraints

- `docs/job-control.md` is normative. Any change that would alter its
  documented semantics (field meanings, promotion behavior, notification
  contract) requires updating that doc in the same task — and if a change
  would *contradict* it, STOP and escalate to Jesse instead.
- Never widen or add a timeout; no polling cadences, no sweep timers —
  await the real event (Jesse's standing rule).
- No mocked job store in integration tests; drive real short-lived jobs.
- Multi-module gates per commit, exit codes only, never `$?` after a pipe.
- Smallest reasonable change; match surrounding style.

---

### Task 1: Promotion legibility and stop attribution

**Files:**
- Modify: `agent/session_tools_shell.go` (`formatShellResult` ~:443-473)
- Modify: `agent/status.go` (job_status reason rendering) if needed for attribution
- Modify: `docs/job-control.md` only if any documented field wording changes (footer text is presentation, not contract)
- Test: footer unit tests in `agent/session_tools_shell_test.go` (or the file's existing test home)

- [ ] **Step 1 (failing tests):** three footer cases —
  (a) foreground promotion: footer states the command is STILL RUNNING as
  job_X, output accumulates durably and is readable with
  `read_transcript(transcript_ref="job:<id>")`, completion arrives by
  notification, and "do not relaunch or poll";
  (b) genuine `stopped/run_timeout` terminal result: wording attributes
  the stop to serf's runtime limit, not the command failing on its own;
  (c) zero-output stop adds that the process produced no output before the
  limit (a build may still have been compiling).
- [ ] **Step 2:** implement. Keep the machine-readable fields
  (`timed_out`, job id) exactly as documented in job-control.md:214 —
  wording changes only.
- [ ] **Step 3:** `job_status` for a `run_timeout` job carries the same
  attribution in its human rendering.
- [ ] **Step 4:** gates; commit
  (`fix(shell): promotion and run_timeout results say what actually happened`).

### Task 2: Byte-window watch scanner

**Files:**
- Modify: `agent/internal/jobstore/output.go` (replace the line-assembly matcher: `appendLineFragment` ~:202-215, `FeedAtWithProvenance`/`ScanRetained` matcher paths ~:100-139, `maxOutputMatcherLineBytes`)
- Modify: `agent/job_watch.go` (`prepareAttachScanLocked` ~:2549-2551 — loud on nil output)
- Test: `agent/internal/jobstore/output_match_test.go` (new/extended)

Jesse's design (2026-08-06): scan a rolling byte window over the raw
stream — carry the last 4KB plus each new chunk, run the pattern per
window; lines are not a concept.
- [ ] **Step 1 (failing tests, the majority of this task):**
  (a) a match inside a 10KB single-line output fires (today: silently
  never);
  (b) a match spanning a window seam fires exactly once;
  (c) offset dedup: re-fed overlapping windows never double-fire a match
  that ends before the scanned offset;
  (d) anchor semantics pinned: patterns compile in multiline mode;
  document and assert what `^`/`$` match at window edges (including the
  growing-window `$` case — pick the behavior, state it in the doc
  comment, test it);
  (e) match-length bound: a match longer than the window does not fire,
  and the bound is documented as a stated limit in the tool description /
  job-control doc where output_match is specified;
  (f) attach-time scan over retained output still works (level scan uses
  the same windowing).
- [ ] **Step 2:** implement the windowed matcher; keep the
  `WatchSendKey`/coalescing/provenance machinery untouched — this changes
  only how bytes are matched, not how sends are recorded.
- [ ] **Step 3:** `prepareAttachScanLocked`'s `run.output == nil` path
  becomes loud: the watch is recorded/reported as unable-to-scan rather
  than silently unfired.
- [ ] **Step 4:** update the output_match documentation (tool description
  and the job-control doc's watch section) for the window bound and
  anchor semantics — same task, same commit series.
- [ ] **Step 5:** gates (include a `-race` run on the jobstore package);
  commit (`fix(jobstore): byte-window output_match scanner — long lines can no longer blind a watch`).

### Task 3: Notification delivery — consume, coalesce, guaranteed wake

**Files:**
- Modify: `agent/session_tools_jobs.go` (`jobStatusTool` ~:391-412), `agent/jobs.go` (`armFinalizedJob` ~:1785-1874, notification state), `agent/session.go`/`server/server.go` (notify kick ~:659-676 / :667-676), jobstore terminal-notification state if a new consumed-state is recorded
- Test: notification integration tests (real session + real short jobs)

Three approved fixes:
- [ ] **Step 1 (failing test, consume):** agent reads a terminal
  `job_status`, then the session goes idle: no later notification turn
  for that job. The consumed state is durably recorded as its own value
  ("caller learned via status read") so the jobstore's told-the-caller
  invariant and serf-doctor diagnostics stay truthful — extend the
  terminal-notification-state vocabulary, and check `serf-doctor`'s
  readers tolerate the new value (loud, not confident-zero).
- [ ] **Step 2 (failing test, coalesce):** a watched job completing
  produces ONE notification turn carrying both the watch settlement and
  the terminal status — build both notices in `armFinalizedJob`, enqueue
  once. Pin that the watch-send terminal records (delivered/…) are
  unchanged — coalescing affects turns, not the durable watch ledger.
- [ ] **Step 3 (failing test, guaranteed wake):** with the notify channel
  slot artificially occupied, a notification enqueued to an idle session
  is still delivered promptly once the slot frees — arm a resend on
  channel availability when `pendingJobNotifs` goes non-empty and the
  kick was dropped. No timers.
- [ ] **Step 4:** gates (`-race` on the touched packages); update
  job-control.md's notification wording if any documented behavior
  changed; commit series, one commit per fix
  (`fix(jobs): status reads consume pending notifications` /
  `fix(jobs): one notification per completion` /
  `fix(server): notification wake is guaranteed, not best-effort`).

### Task 4: end_turn warning for running verification jobs

**Files:**
- Modify: `agent/session_tools_communicate.go`
- Test: communicate warning test with a live background job

- [ ] **Step 1 (failing test):** `communicate(end_turn=true)` while a
  session-launched job is still running returns a result carrying a
  structured warning line naming the running job ids; with no running
  jobs, no warning; `end_turn=false` never warns.
- [ ] **Step 2:** implement (warn-first per the 2026-08-06 ruling — the
  call still succeeds; no refusal path).
- [ ] **Step 3:** gates; commit
  (`feat(communicate): end_turn warns about still-running jobs`).

## Acceptance (whole workstream)

- The Task 2 scanner closes the 0/18-watches class: a fixture replaying
  03410Qj0SmX9L46Iv1Gb41's long-line shape fires its watch.
- A watched short job produces exactly one notification turn; a
  status-read job produces zero; a saturated notify channel delays
  delivery only until the slot frees.
- `serf-doctor transcript --health` on a fresh session shows
  `stale_notifications=0` where the old code produced them.
- No new tools, no schema changes to shell, job-control.md updated where
  wording is now sharper — and contradicted nowhere.
