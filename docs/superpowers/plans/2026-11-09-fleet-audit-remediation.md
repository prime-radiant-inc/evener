# Fleet Audit Remediation Plan

> **For agentic workers:** this is a program plan. Each workstream below is cut
> into PR-sized slices. Before implementing a slice in the Evener repo, write
> its own task-level plan with superpowers:writing-plans, then execute with
> superpowers:subagent-driven-development or superpowers:executing-plans.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the runtime defects and workflow rules that made the
2026-11-04..09 Evener fleet spend most of its effort on review loops, polling,
and re-grounding, and give us a repeatable measurement that shows whether the
fixes worked.

**Architecture:** Two layers. The workflow layer (AGENTS.md, the `par` and
`implementing-features` skills in primeradiant-ops, the standalone shepherd-pr
repo) decides how much review and verification happens; the Evener runtime
decides whether agents can wait, see completions on time, and trust their
journals and tools. Evener's system prompt already tells agents to end the turn
instead of polling (`agent/prompts/system.md.tmpl`, "Background work"), and the
fleet polled 5,869 times anyway, so waiting fixes are enforced in the runtime,
not added as more prompt text.

**Tech Stack:** Go (agent runtime, hub), Markdown skills, Python (shepherd-pr
scripts).

**Spec:** the audit,
`~/.local/state/evener/projects/home-jesse-git-prime-radiant-inc-evener-88XN49DJLY/sessions/034uPBIBjrZOC5lhcAHdBa/artifacts/fleet-audit-problems.md`,
plus the code-grounding notes in "Root causes" below. Product behavior changes
also follow `docs/product/friction.md` (R03, R05, C02) and
`docs/product/README.md`.

## Global Constraints

- Do not modify the superpowers repo or the simplify-code plugin.
- Evener changes follow AGENTS.md: own worktree per PR, TDD, behavior tests
  only (never assert on copy, including the new hint and error text below),
  deterministic default tests with a scripted provider at the LLM boundary,
  product docs updated in the same PR, merge through the merge queue.
- Consequential product choices (listed under "Decisions") are settled with
  Jesse before their slice starts; record each decision in
  `docs/product/friction.md` or the owning guide.
- Model-facing guidance that the runtime can enforce is enforced in the runtime.
  Prompt text alone is not a fix for a behavior the fleet already ignored.

## Review Focus

- A notification turn that yields to newer notifications must not drop or
  duplicate the batch it was already handling, and must not starve its own
  in-flight tool batch.
- Idempotent job-event recovery must still deliver a genuinely new terminal
  generation (`TerminalGen` bump after a rerun) to the parent.
- A parked or refused repeated `job_status` must still let an agent check a
  delegate Evener reports as quiet, which the system prompt tells it to do.
- Quota parking must stop the notification re-kick loop without losing the
  notifications themselves; they are delivered once the provider recovers or
  the user switches models.
- Removing the vendored shepherd-pr from primeradiant-ops must leave every
  harness that loads primeradiant-ops with a working shepherd-pr (standalone
  plugin installed) or a clear install pointer.

---

## Root causes (code-grounded, 2026-11-09)

| Audit finding | Root cause | Evidence |
|---|---|---|
| Notifications delivered hours late (§7) | A turn started by a notification never yields to newer notifications; they wait until the turn ends. One 2.46 h sleep/poll turn in `034rn5pK` held every completion until 08:18. | `agent/session_lifecycle.go:2116-2123` (`kind != EntryNotification` guard) |
| `job_status` polling (§4) | No wait primitive; the identical-success breaker only appends a nudge, never parks. | `agent/internal/tool/definitions.go` `DefJobStatus`; `agent/internal/tool/breaker.go` identical-success path |
| 20.7 h / 14 h / 3.1 h jobs, 1,699 starts/hour (§4, §9) | Background shell jobs have no default deadline and no per-session or host concurrency cap. | `agent/job_shell.go` (120 s foreground then promote); `docs/job-control.md` ("Shell-process concurrency remains a separate standing gap") |
| 73 quota failures at ~14/min; 4h46m stall (§7) | Usage-limit 429 is a permanent error; with no `model_fallbacks` the turn fails, and each incoming notification starts another failing turn. Goal blocking on it is R05 (decided, unimplemented). | `llm/errors.go` `quotaExceededError`; `agent/session_goal.go:427` `terminateGoalOnError` |
| `jobs.jsonl` repeated up to 8.7× (§6) | Every child restore re-forwards all its terminal jobs to the parent; `forwardEvent` appends unconditionally. Synthetic start drops `background`. | `agent/jobs_nested.go:564` `recoverForwardedTerminalEvents`, `:681` `forwardEvent`; probably `:630` `recoverForwardedPendingNotifications` |
| Watch budget / re-feeds / restart loss (§6, §10) | Lifetime budget of 50 deliveries; at-least-once send rail re-feeds on settle failure; restart ends watches (R03). | `agent/watch_types.go:65`; `agent/watch_send_rail.go` settle; `agent/jobs.go` `clearUnrestoredActiveWatches` |
| `apply_patch` "wanted == got" (§6) | Diagnostic shows only the block's first line; blank context lines without a leading space are dropped from the match. | `agent/internal/tool/apply_patch.go:395` `mismatchLineForMissingSequence`, `:449` `replacementEntriesFromHunk` |
| Shell operators in `cwd` (§6) | Error says only "does not exist". | `agent/session_tools_shell.go:397` `resolveShellWorkingDir` |
| 40% of greps truncated (§5) | Registry line cap (200 lines / 20k chars) applies on top of `max_results`, so raising it can be futile. | `agent/internal/tool/registry.go` grep cap |
| Repeated reads, truncation (§5) | No unchanged-file hint; truncation note points at `read_transcript`, not `offset`/`limit`. | `registry.go` truncation; `session_tool_artifacts.go` |
| Unknown tools `exec_command`, `job-notification` (§6) | Levenshtein suggester cannot map other harnesses' names. | `agent/internal/tool/repair/suggest.go` |
| `target_busy`, wrong-role reviewers (§6, addendum) | Busy error carries no reason or retry hint; `explorer` is read-only and briefs ask it to write. | `agent/delegate_send.go:62`; `internal/bundled/agents/` |
| 30-minute doctor census (§7) | Parses every transcript of ~80k sessions serially; `since` filter applies after parsing; no context. | `agent/doctor/sessions.go:88` `ListSessions`, `:149` `sessionRow` |
| No-op `use_skill` reloads (addendum) | Renders and records an obligation before returning the "already active" stub. | `agent/session_skill_activation.go` |
| Compaction "on a timer" (addendum) | Model-initiated; runs even below the pressure floor. Lowest priority. | `agent/session_tools_compact.go` |
| Review is 96% of delegates; 28-round council; 19 repair rounds (§1-3) | Workflow rules: `par` says "after any significant work" with no stop rule; `implementing-features` stage 4 says "revise and re-review" with no cap and its red flag says to run par and simplify-code again on repeating findings; shepherd-pr says "run affected tests and required repository gates" each round. | primeradiant-ops `skills/par`, `skills/implementing-features`; `~/git/shepherd-pr/skills/shepherd-pr/SKILL.md` |
| Two shepherd-pr copies | Evener loads both the standalone plugin and the copy vendored in primeradiant-ops; the fleet called scripts from both. | `~/.config/evener/plugins/cache/{primeradiant/primeradiant-ops,prime-radiant-marketplace/shepherd-pr}` |
| Manager churn (§10) | Manager prompt section is protocol only; nothing on narration, stale frames, or sweep timers. Watch unreliability (above) drove the sweep timers. | `agent/prompts/system.md.tmpl` "Manager mode" |

**Not defects (audit corrections):** duplicated transcript entries are
compaction's retained-tail copies marked `original_ordinal`, which every real
reader skips, so the 526 vs 332 compaction count and ~4% token inflation come
from reading raw JSONL. Empty `.api.jsonl` means `api_log` is off (default in
`hub.toml`). The frozen cache in `034sbQCH` (lunaroute `glm-5.3-vision`)
matches a short backend cache TTL; unverifiable without `api_log`. Both
duplicate-session pairs look like human relaunches; C02 already tracks
creation idempotency.

---

## Decisions (settle with Jesse before the owning slice)

- [x] **D1 Waiting.** Decided 2026-11-09: an agent that legitimately needs to
  wait ends its turn; Evener adds no wait primitive. As in Claude Code, the
  runtime refuses wait-shaped actions instead: a repeated identical
  `job_status` on an unchanged running target, and shell commands that only
  sleep. Condition waits use `job_watch` (`output_match`), which runs without
  model turns. Open detail: the exact sleep rule (see W1.2). Owns W1.2.
- [x] **D2 Long-running jobs.** Decided 2026-11-09: no forced deadline and no
  "expected" acknowledgement (what is right at 30 minutes is not right at 3,000).
  Every 30 minutes while a session owns a shell job running longer than 30
  minutes, Evener sends that session one reminder listing all such jobs. An
  active turn receives it with its next model request; an idle session is woken
  for it. The per-session concurrency cap is deferred: revisit if the W6.1
  follow-up still shows load spikes after the workflow rules land. Owns W1.3.
- [x] **D3 Usage-limit behavior.** Decided 2026-11-09: on a usage-limit 429
  with no eligible fallback, the session enters a visible "waiting for quota
  until <reset>" state: no provider calls, notifications held rather than
  starting failing turns, an active goal stays active. It resumes on its own at
  the reset time. The human can change the model while it waits; that resumes
  immediately on the new model with the held notifications and goal. The state
  shows wherever sessions are listed. Implements R05 for the quota case.
  When the 429 names no reset time, the existing pause stays (resume on a
  model switch or a user message); automatic paced recovery for that case is
  open, tracked under R05. One-shot `evener run` keeps fail-fast (ruling
  2026-11-09, to confirm with Jesse): there is no human to switch models.
  No default fallback chain: which model runs the work stays the human's
  choice; the existing `model_fallbacks` setting remains available. Owns W1.4.
- [x] **D4 Watch budget.** Decided 2026-11-09: remove the 50-delivery lifetime
  budget. A watch is rate limited to at most one delivery per minute; fires
  inside the window coalesce into the next delivery. The watch lives until its
  target ends or it is cleared. Owns W2.2.
- [x] **D5 Local `implementing-features` copy.** Decided 2026-11-09: delete
  `~/.config/evener/skills/implementing-features` once W5.2 lands, so Evener
  loads the primeradiant-ops version. Verify afterwards that a new Evener
  session's skill inventory resolves `implementing-features` to the plugin
  path. Owns W5.2.
- [x] **D6 API logging.** Decided 2026-11-09: record one lunaroute
  `glm-5.3-vision` session with API logging on to settle the cache-freeze
  question. `api_log` is a per-launch control ("Log API requests", debug
  logging group; `cmd/evener-hub/internal/launchconfig/schema.go`), so it is
  set when the session is launched, not on a running one. Owns W6.2.

---

## Workstream 5: Workflow rules (do first — largest token share, no runtime risk)

### W5.1 shepherd-pr repo (`~/git/shepherd-pr`)

**Files:** Modify `skills/shepherd-pr/SKILL.md`; release per the repo's process.

- [ ] Add to "Observe", after the background-job table: once a settle or watch
  batch is running in the background, end the turn and let its completion wake
  you; do not check it with status tools in between.
- [ ] Rewrite "Triage and verify" step 4: each round runs the tests for that
  round's findings and the affected packages; the repository's heavy gates run
  once before the push and are not re-run on code unchanged since they passed;
  the acceptance set is per round and does not accumulate earlier rounds' tests.
  The full suite is CI's job when the repository says so.
- [ ] Add to "Triage and verify" step 1: fetch only the failed job's log and
  filter it (`gh run view --log-failed` piped to a search) rather than pulling
  whole logs into context.
- [ ] Run the repo's own tests (`docs/testing.md`), commit, release.

### W5.2 primeradiant-ops (`~/git/cc-plugin-primeradiant-ops`)

**Files:** Delete `skills/shepherd-pr/` and any sync tooling for it; modify
`skills/par/SKILL.md`, `skills/implementing-features/SKILL.md`, README/plugin
manifest entries that list shepherd-pr.

- [ ] Pull origin/main first (local checkout is two sync commits behind).
- [ ] Remove the vendored shepherd-pr and its sync path; point readers at the
  standalone plugin (`prime-radiant-inc/shepherd-pr`) wherever the README or
  `implementing-features` names it.
- [ ] `par`: keep the reviewer brief verbatim. Add a section for the agent
  running par (the judge):
  - Run par once on the finished change. After fixes, one verifier checks the
    fix delta against the accepted findings; that is not a new par.
  - Before spawning, check for a par already running on the same commit.
  - The judge rules on every finding against the code; a split verdict is
    settled by the judge, not by adding seats.
  - No accepted significant findings: review is done.
  - Two rounds that each produce new accepted significant findings: stop and
    bring it to Jesse.
- [ ] `implementing-features`: stage 4 re-review covers only the revised
  requirements, with one verifier, under the same stop rule and cap; a spec
  marked ready is reopened only when implementation shows a defect. Replace the
  red flag "Scope a family sweep, run par and simplify-code before the next fix"
  with: fix the class at its root and verify with the class's tests; the
  shepherding round's review covers it.
- [ ] Bump version, commit, release. Then D5.

### W5.3 Evener AGENTS.md

**Files:** Modify `AGENTS.md` (new "Agent workflow budget" section, about 15
lines); `docs/developing-evener/README.md` if it indexes AGENTS.md sections.

- [ ] Review budget: par once before the PR, then RoboRev is the review; no
  extra reviewer fan-out per round.
- [ ] Spec review: a READY spec stays READY unless implementation shows a
  defect; three rounds without convergence go to Jesse.
- [ ] Duplicate delegates: check running delegates before spawning one on the
  same question or commit.
- [ ] Waiting: long work runs as a background job, then end the turn; no
  `sleep` or poll loops; every server or loop you start gets a time bound.
- [ ] Host load (extends "one heavy gate at a time"): no stress or CPU-burner
  jobs; `make lint` once before push, not per edit.
- [ ] Scope fidelity: "find"/"research"/"audit" asks do not become
  implementation PRs without asking; do not commit to a repository the request
  did not name; a layout redesign does not change workflow.
- [ ] Missing skill: say so; do not search the filesystem for it.
- [ ] Docs-only PR; land per the spec-PR rule.

---

## Workstream 1: Waiting and notifications (Evener runtime)

### W1.1 Notification turns yield to newer notifications

**Files:** Modify `agent/session_lifecycle.go` (round loop guard at
2116-2123, `acceptNotificationInput` at 2515); test in the session lifecycle
test file that already drives `EntryNotification` turns; update
`docs/job-control.md` delivery section and `docs/product/subsystems.md` row.

- [ ] Failing test `TestNotificationTurnDeliversLaterCompletionBeforeTurnEnds`:
  scripted provider; job A finishes and starts a notification turn whose model
  keeps issuing tool calls; job B finishes during round 2; assert B's
  notification reaches the model by the next round boundary (the next request
  contains it) and is recorded delivered exactly once; A's batch is not
  redelivered.
- [ ] Implement: at the round boundary of a notification turn, drain newly
  pending notifications into the next model request (inject, not yield), so
  the turn keeps its batch and still sees later arrivals.
- [ ] Pass, docs, commit, PR.

### W1.2 Refuse wait-shaped actions so agents end the turn (D1)

**Files:** `agent/internal/tool/breaker.go` (identical-success path),
`agent/session_tools_shell.go` (command admission), `DefJobStatus`
description in `agent/internal/tool/definitions.go`; tests beside each;
`docs/job-control.md`.

- [ ] Failing test `TestRepeatedIdenticalJobStatusOnRunningJobIsRefused`: the
  second identical call on a running target whose state has not changed is
  refused with a distinct error kind; a call after the target's state changes
  is allowed; a first check of a delegate Evener reports as quiet is allowed.
- [ ] Failing test `TestSleepOnlyShellCommandIsRefused`: a command that only
  sleeps (`sleep N`, a `sleep` loop with no other work) is refused before it
  runs; a command that sleeps as part of real work is not (D1 option a: loops
  that do work between sleeps are not refused; W1.3's long-running job
  reminders surface them instead).
- [ ] Rewrite the wait guidance where the agent reads it at the moment it acts,
  in "when you want X, do Y" form: the `job_status` and `job_list`
  descriptions, the repeated-poll refusal, the breaker's repetition nudge, and
  the sleep refusal each say: nothing else to do until it finishes → end your
  turn with `communicate(end_turn=true, end_reason="waiting_on_work")`; its
  completion wakes you; to wake on a condition, set `job_watch` with
  `output_match`. Drop `job_list`'s "if you have waited a long time" clause.
- [ ] In `agent/prompts/system.md.tmpl` "Background work", replace "Start long
  work in the background only when you have other work to do meanwhile" with
  the table's rule: long work runs as a background job; then do other work or
  end your turn. Update prompt golden output.
- [ ] Implement. Pass, docs, commit, PR.

### W1.3 Long-running job reminders (D2)

**Files:** job manager timer and notification enqueue in `agent/jobs.go`
(reuse the existing notification path so delivery, wake and dedupe behave like
job completions); `docs/job-control.md`; `docs/product/subsystems.md` row.

Each reminder lists every running shell job older than 30 minutes with its
command, age and time since its last output, and says in "when X, do Y" form:
if one of these is unexpected, stop it with `job_stop`.

- [ ] Failing test `TestIdleSessionIsWokenWithLongRunningJobReminder` (fake
  clock): one job crosses 30 minutes while the session is idle; the session is
  woken once with a reminder naming that job; at 60 minutes, again.
- [ ] Failing test `TestLongRunningJobReminderIsOnePerSession`: three jobs past
  30 minutes produce one reminder listing all three, not three.
- [ ] Failing test `TestActiveTurnReceivesLongRunningJobReminder`: during an
  active turn the reminder arrives with the next model request and does not
  start a second turn.
- [ ] Failing test `TestNoReminderAfterLongJobsFinish`: once no job is older
  than 30 minutes, the cadence stops; a job finishing between ticks is not
  listed.
- [ ] Implement. Pass, docs, commit, PR.

### W1.4 Usage-limit 429: wait instead of re-kick (after D3; implements R05 for quota)

**Files:** `agent/session_goal.go` (`terminateGoalOnError`),
`agent/session_model_call.go` (fallback path), notification dispatch in
`agent/session_lifecycle.go`; `docs/product/friction.md` R05;
`docs/product/subsystems.md`.

- [ ] Failing test `TestUsageLimitHoldsNotificationsUntilRecovery`: scripted
  provider returns usage-limit 429 with a reset time; three job notifications
  arrive; assert no further model requests until the reset (fake clock) or a
  model switch, then one turn delivers all three exactly once.
- [ ] Failing test `TestUsageLimitKeepsGoalWaiting`: active goal stays
  waiting, not blocked, and continues after recovery; a cancelled goal does
  not resume (R05 acceptance).
- [ ] Failing test `TestModelSwitchWhileWaitingForQuotaResumesImmediately`:
  while waiting, the human switches model through the existing session
  control; the next model request goes to the new model without waiting for
  the reset, carrying the held notifications once, and the goal continues.
- [ ] Failing test `TestWaitingForQuotaIsVisibleInSessionState`: the session
  state read by clients reports the waiting state and the reset time, and
  clears it on resume.
- [ ] Update the affected client surfaces that list sessions (hub web, native)
  to show the state; follow `docs/developing-evener/design-system.md`.
- [ ] Implement per D3. Pass, record the quota case in the owning guide and
  keep R05 open for the non-quota transient cases (e.g. exhausted 503
  retries), commit, PR.

---

## Workstream 2: Journals and watches (Evener runtime)

### W2.1 Idempotent forwarded job recovery

**Files:** `agent/jobs_nested.go` (`recoverForwardedTerminalEvents`,
`recoverForwardedPendingNotifications`, synthetic start event); test in the
nested-jobs test file.

- [ ] Failing test `TestChildRestoreDoesNotReforwardDeliveredTerminalJobs`:
  child with two finished jobs forwarded to the parent; restore the child
  twice; parent store holds exactly one start and one finish per
  `(job_id, terminal_gen)`; forwarded start keeps `background`.
- [ ] Failing test `TestChildRestoreForwardsNewTerminalGeneration`: a rerun
  bumps `TerminalGen`; restore forwards that generation once.
- [ ] Same check for pending-notification recovery; extend tests if it
  duplicates.
- [ ] Implement: skip records the parent already folded for that
  `(job_id, terminal_gen)`. Pass, commit, PR.

### W2.2 Watch delivery (after D4)

**Files:** `agent/watch_types.go`, `agent/watch_clear.go`,
`agent/watch_send_rail.go`; `docs/job-control.md`; friction R03 untouched
unless this slice implements it.

- [ ] Failing test `TestWatchSettleIsIdempotentAcrossRetry`: a settle failure
  followed by retry delivers the frame once to the model.
- [ ] Failing test `TestWatchSurvivesBeyondFiftyDeliveries` (fake clock): 60
  fires spaced over an hour all deliver and the watch stays active.
- [ ] Failing test `TestWatchFiresWithinRateWindowCoalesce`: five fires inside
  one minute produce one delivery carrying all five events, in order.
- [ ] Verify the manager error "hub watch cannot combine ordinary watch
  arguments" no longer occurs on main (the manager prompt now says hub watch
  creates take `note` and `every`); if it does, add a failing test first.
- [ ] Implement, pass, commit, PR. R03 (restart) stays its own slice.

---

## Workstream 3: Tool friction (Evener runtime)

One PR per bullet. Each test asserts behavior or structured fields, never the
hint text.

- [ ] **W3.1 apply_patch.** `TestApplyPatchKeepsBareBlankContextLine` (hunk
  whose blank context line has no leading space applies);
  `TestApplyPatchMismatchReportsFirstDifferingLine` (structured mismatch
  carries the line index of the first differing line, not the block's first
  line). `agent/internal/tool/apply_patch.go`.
- [ ] **W3.2 shell cwd.** `TestShellCwdWithShellOperatorsGetsPathHint`: a
  `cwd` that fails ordinary path validation and contains `|`, `&&`, `>` or `;`
  fails with a distinct error kind saying `cwd` is one directory path; an
  existing directory whose name contains those characters is still accepted.
  `agent/session_tools_shell.go`.
- [ ] **W3.3 grep cap.** `TestGrepRaisedMaxResultsIsNotRecappedByRegistry`:
  `max_results=400` returns 400 matches (within the char bound).
  `agent/internal/tool/registry.go`.
- [ ] **W3.4 read_file.** `TestRepeatReadOfUnchangedFileIsMarkedUnchanged`
  (structured flag when mtime and size match the session's last read of the
  same range). Truncation note offers `offset`/`limit`.
- [ ] **W3.5 unknown tools.** `TestUnknownToolSuggestsShellForExecCommand`
  (suggestion list contains `shell`). Alias table in
  `agent/internal/tool/repair/suggest.go`.
- [ ] **W3.6 delegate.** `TestDelegateSendTargetBusyCarriesReason` (busy error
  has a reason enum and retry-after). Clarify `explorer` as read-only in its
  role description.
- [ ] **W3.7 doctor census.** `TestDoctorSessionsSinceSkipsOldTranscriptsUnread`
  (sessions older than `since` are not parsed: count reads via the fs seam) and
  context cancellation stops the walk. Default `since` for the tool.
  `agent/doctor/sessions.go`.
- [ ] **W3.8 use_skill.** Make a redundant `use_skill` cheaper without removing
  the delivery obligation that re-delivers a skill body after compaction
  removes it (`TestSkillDelivery_RevalidateAfterFold` must keep passing).
  Short-circuit only the redundant render; test that the obligation and
  revalidation behavior are unchanged.
- [ ] **W3.9 compact_context (lowest priority).** Below the pressure floor the
  call is a no-op with a structured "not needed" result.

---

## Workstream 4: Manager prompt (Evener built-in)

**Files:** `agent/prompts/system.md.tmpl` "Manager mode"; prompt golden tests.

- [ ] Add: a frame that needs nothing gets the one-line acknowledgement the
  Reporting section already defines; do not narrate routine child milestones;
  do not build sweep timers to compensate for watches (after W2.2 lands);
  before starting a review panel on a shared stack, confirm the stack is ready
  and give panelists separate work.
- [ ] Update golden output, commit, PR. Lands after W2.2 so the sweep-timer
  rule matches working watches.

---

## Workstream 6: Measurement

### W6.1 Fleet metrics script

The audit's numbers were computed ad hoc. Make them repeatable so we can
compare a week after W5 and W1 land.

- [ ] Ask the toolsmith for `fleet-audit-metrics`: input a state dir and a time
  window; output per-session and fleet totals for review-delegate share,
  duplicate briefs, `job_status` calls and identical-call runs, `sleep` jobs
  and minutes, jobs over 10 minutes, `jobs.jsonl` lines per job id,
  notification pending→delivered latency, tool errors by class, compactions.
  It must skip `original_ordinal` transcript entries and dedupe jobs by id.
- [ ] Baseline: run it on 2026-11-04..09; it should reproduce the audit's
  corrected figures.
- [ ] Follow-up run one week after W5 + W1.1 + W2.1 land; record the
  comparison in this plan.

### W6.2 Cache freeze (after D6)

- [ ] If D6 says yes: one lunaroute session with `api_log = true`, compare
  request prefixes across a >2 min gap; file an issue with the result.

---

## Order

1. W5.1, W5.2, W5.3 (workflow; parallel; no runtime risk).
2. W6.1 baseline (before runtime fixes land, so the comparison is honest).
3. W1.1 and W2.1 (confirmed bugs, no decisions needed).
4. D1–D4 with Jesse, then W1.2, W1.3, W1.4, W2.2.
5. W3 slices, any order.
6. W4 after W2.2.
7. W6.1 follow-up.
