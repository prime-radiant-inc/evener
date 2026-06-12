# Job-Control Surface Ergonomics Addendum

Date: 2026-06-11
Status: approved direction (Jesse: "fold stuff in… best possible implementations that are as easy as possible for agents to use… look really hard at tool descriptions and prompting… look also at 'bad' or 'useless' parameters")
Companion to: `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md` (the mailbox spec) and `docs/superpowers/plans/2026-06-11-watch-mailbox.md` (the plan). This addendum becomes **Phase 1.9 — surface ergonomics**, executed after the Phase-1 merge and BEFORE Phases 2/3 launch (so no further tests get written against the old parameter surface).

## 0. Headline finding: the promised prompt section was never built

The 2026-06-08 design spec's DRY rule — "a shared `## Background jobs` system-prompt section carries the mental model once; tool descriptions stay short" — was never implemented. `agent/prompts/sections/` contains `delegation.md` (good when-to-delegate guidance, zero job mental model) and nothing about jobs. The descriptions carry everything, the mental model lives nowhere, and the costliest agent behaviors (polling for completion, arming watches for one-shot questions, parking turns on long waits) have no countervailing prompt. §1 supplies the section.

Design principles applied throughout (imported from the Claude Code harness's watch/wait stack, which serf's mailbox redesign independently converged with):
- **Cardinality-first**: the surface must make the agent ask "how many answers do I need?" — one (blocking read), recurring (watch), completion (automatic, free).
- **Wake-don't-poll**, with one taught recovery move for lost-wake paranoia.
- **Blocking is a bounded convenience, never parking.**
- **Silence is never success**: no parameter or default may silently narrow what the agent thinks it observed (the `limit_bytes` trap below).
- **Every event the model sees must be worth a model turn** (budget circuit breaker below).

## 1. New prompt section: `agent/prompts/sections/background-jobs.md`

Wiring: register alongside `delegation.md` (discover the section-assembly mechanism in `agent/prompts/templates/` + its Go loader at implementation time; include in the same profiles that get the job tools; the subagent variant omits the sidecar paragraph the way `delegation.md`'s availability note implies). Full text:

```markdown
## Background jobs

Shell commands and delegates can run as durable background jobs identified by a
`job_id`. Jobs outlive your turn, and Serf notifies you automatically when a
background job finishes — completion never needs polling, blocking, or a watch.

Pick the waiting primitive by how many answers you need:

- The result of a quick command now → plain `shell` (foreground).
- One signal ("the server printed ready") → `job_read_output` with `block=true`
  and `grep`. One bounded wait, nothing to clean up afterward.
- A recurring condition (every new match, periodic progress, event frames to an
  observer) → `job_watch`.
- "Tell me when it finishes" → nothing. The terminal notification is automatic.

Blocking waits are bounded conveniences measured in seconds, not parking: a
timeout leaves the job running and you free. Never hold your turn open for long
work — run it in the background, keep working, and act on the notification.

`job_list` is always current. If you have waited unusually long with no
notification, list jobs to re-orient before re-running anything.

Observer sidecars: start a delegate as the observer, then
`job_watch(target=<job>, ..., send={to: <observer job_id>})`. Each trigger
pushes the observer a bounded frame; the observer can read the watched job
directly with `job_read_output` and report to you with
`job_send_message(target="caller")`. Frames coalesce while the observer is
busy — it sees the latest state, not a backlog. Watching your own
assistant/tool events with delivery back to yourself is rejected: that is a
feedback loop, not observation.

`job_stop` cancels; it never deletes output or history. A finished delegate is
not gone — `job_send_message` resumes the same conversation as a new job.
```

(~30 lines. Each paragraph exists to prevent an observed expensive behavior: polling, one-shot watches, parked turns, lost-wake confusion, sidecar misassembly, self-watch loops, needless re-delegation.)

## 2. Parameter surgery

Bringup — no users, no backward compatibility (per Jesse's standing rule, breaking changes are the default here). Each change below lands with its validation, tests, description text (§3), and contract row (§5) in one commit.

| # | Change | Evidence | New behavior |
|---|---|---|---|
| P1 | **DELETE `job_list.cursor`** (and `next_cursor` from the result) | Handler never reads it; `NextCursor: nil` hardcoded (`agent/session_tools_jobs.go:348`). Schema-accepted, semantics-free. | `limit` (default 50, max 100) is the only paging; sessions don't accumulate more listable jobs than that in practice. |
| P2 | **DELETE `job_read_output.limit_bytes`; grep always scans the full retained output** | `limit_bytes` is the grep *scan budget* (`GrepLimitLineBytes`, `agent/internal/jobstore/output.go:157-163`; `0` ⇒ no scan) defaulting to 64KB against 8MB retention (`session_tools_jobs.go:20-22,180`) — a **silent-miss trap**: default grep can miss matches and the result is indistinguishable from "no match". Violates the contract's own no-silent-miss spirit (`docs/job-control.md:546`). | Scan is always full retained output; the result stays bounded by the existing `maxJobGrepMatches`/`maxJobGrepLineBytes` constants. NOTE for Jesse: this re-opens punch-item A5 (settled 2 days ago as "canonical inclusion") — deliberately, with the scan-trap as the new evidence; contract rows `:610`/`:640` rewritten. Track C's blocking-grep entry check inherits full-scan correctness automatically. |
| P3 | **DELETE `job_read_output.max_chars`** | Duplicates the registry-level result truncation (`jobToolResultMaxChars(reg, "job_read_output")`, `session_tools_jobs.go:47`); two caps, one master. | Registry truncation governs alone; `tail_bytes` remains the single content-window knob. |
| P4 | **REPLACE `job_watch.trigger{event,every}` with top-level `every` (integer)** | The nested object's `event` must restate a kind from `events` (`validateWatchEventArgs`), and trigger-only configs were a guard-bypass class the /par review caught (`newWatchConfig` injects trigger kinds, `job_watch.go:466-469`). Two ways to say one thing. | `every` is valid only when `events` has exactly one kind (else `invalid_request: every requires exactly one watched event kind`); fires on each Nth occurrence. Covers the only real use ("every 3rd assistant.message") with one flat param; the §6.1 loop guard evaluates `events` alone. Contract `:509` rewritten. |
| P5 | **DELETE `job_watch.send.include_frame`** | Frame metadata (job_id, trigger, delivery_id) is the delivery's entire value; opt-in means every sidecar config must remember a boolean or get context-free pings. The bytes are trivially small. | Frames always carry metadata. `include_excerpt` remains the only send option (job targets only, per mailbox spec §6.2; session targets carry `transcript_ref`). Contract `:516` area. |
| P6 | **REJECT contradictory foreground/background combos** | `shell`: `background=true` + `block_timeout_ms` — the timeout silently does nothing (`docs/job-control.md:181`). `delegate`: `block_timeout_ms` with `background` unset/true — same dead knob (`:292`). | `invalid_request: block_timeout_ms applies only to foreground waits (background=false)`. Contradictions loud at the boundary instead of silently half-obeyed. |

Deliberately **kept** (reviewed, not useless): `block`+`block_timeout_ms` pairing (consistent 5-tool idiom; bool reads better for models than magic zero); `job_stop.include_children` (real nested semantics); `job_watch.clear` (contract-settled unwatch, keyed (target, send.to)); `shell.description` (job labeling); `delegate.model` free-form (values are deployment-specific); the `events` kind names (contract-settled vocabulary).

## 3. Tool description rewrites (full replacement texts)

Principles: lead with what it does and when to reach for it; the sibling-tool decision boundary goes in the description (the mental model lives in §1's prompt section); defaults stated on the parameter, not in prose; no internal jargon; no warnings about races that no longer exist. These texts assume P1-P6 and the mailbox redesign are landed.

**`shell`:**
> Run a shell command. Foreground by default: returns stdout, stderr, and exit code inline, waiting up to `block_timeout_ms`; a command still running at the timeout is promoted to a durable background job — you get its `job_id`, the process is not killed. Set `background=true` to skip the wait and get a `job_id` immediately (a dev server, anything long). `max_runtime_ms` separately caps total process runtime. Serf notifies you automatically when a background job finishes. Prefer `rg`/`rg --files` for searching.

**`delegate`:**
> Start a NEW delegate conversation to do independent agentic work; returns a `job_id` (background by default — you are notified when it finishes). `delegate` never resumes an existing delegate: follow up on one with `job_send_message`. Optional: `agent_type` picks a role from the enum (described in your agents section); `model` and `reasoning_effort` override the defaults; `result_schema` requests a validated structured result; `background=false` waits inline up to `block_timeout_ms` (a timeout leaves the job running). Judge the work from its output, not from `status="completed"`.

**`job_send_message`:**
> Send a follow-up message to a delegate by `job_id` — or, from an observer, commentary to `caller`. A running delegate is steered mid-run; a finished one is resumed in the same conversation as a new job (new `job_id` returned, `background` default true). Set `on_finished="fail"` only when you require a currently live target: the call then fails with `target_terminal` instead of resuming.

**`job_watch`** (with the §1 section carrying the model, this gets *shorter* than today):
> Add a standing trigger on a running job or a visible session. For a one-time "did it print X yet", use `job_read_output` with `block` + `grep` instead — watches are for recurring conditions, and completion needs no watch at all (terminal notifications are automatic). Triggers, set only what you need: `output_match` (RE2 over the job's output; if the retained output already contains a match the watch fires immediately, then again on new matches — a finished job gets a one-shot catch-up scan), `progress_interval_ms` (periodic), `events` (kinds this session: %s; `every` = fire on each Nth occurrence, single-kind watches only). Delivery: omit `send` to be notified yourself; set `send.to` to an observer delegate's `job_id` (or `watched`) to push bounded trigger frames there — this also grants that observer read access to the watched job. Frames coalesce latest-wins while the target is busy. `include_excerpt` attaches an output excerpt (concrete job targets only). `clear=true` removes the watch for (target, send.to).

**`job_read_output`:**
> Read a job's output and status by `job_id` — reads never consume or acknowledge anything. Returns a bounded output tail (`tail_bytes`) for shell jobs or the report (and `structured_result`, when present) for delegates. `grep` searches the job's **entire retained output** and returns matching lines. `block=true` waits up to `block_timeout_ms`: with `grep`, until a match exists, the job ends, or the timeout elapses — the one-call way to wait for "ready"; without `grep`, until new output or terminal state.

**`job_list`:**
> List this session's durable jobs, newest first; filter by `status` or `type`. The result also includes your active watches. Always current — if you have waited a long time with no notification, list jobs to re-orient instead of re-running work. Terminal statuses: completed, failed, cancelled, stopped. A short job can finish before a running-only filter sees it; when recency matters, list unfiltered or read the job by id.

**`job_stop`:** (light trim of current)
> Request cancellation of a running job by `job_id`; stopping never deletes output or history. `block=true` waits briefly for the stop to finalize; `include_children=true` also stops the job's nested children. Stopped work normally lands as `status=cancelled`, `reason=stopped_by_parent`.

Haiku-validation gate (the 2026-06-08 method): before landing, run the three-small-model comprehension scenarios against the new descriptions + §1 section; fold ambiguity flags back in.

## 4. Imported watch/wait features (the "what should serf learn from you" set)

- **F1 — Watch delivery budget (circuit breaker).** Per watch config, count model-facing deliveries (caller notifications + rendered frames + sidecar sends). At a hard-coded cap (50), auto-clear the watch and emit one final notification: `watch cleared: <target> delivered 50 times; re-arm with a tighter condition (higher every, narrower output_match, or longer progress_interval_ms)`. No configuration knob (YAGNI). Mirrors the harness's auto-stop for too-chatty monitors; converts runaway watch spend into one actionable message. Lands with Phase 2 (it's a delivery-rail consumer; post-Phase-1 only).
- **F2 — Watch enumerability.** `job_list`'s result gains a `watches` array: `{target, condition (one-line summary), send_to, deliveries, created_at}` — populated from the jobManager's live configs. Agents can answer "what am I still watching?"; the hub dashboard gets it for free. Lands with F1 (shared accounting).
- **F3 — Terminal notifications carry a result excerpt.** `job_finished` notification blocks gain a bounded excerpt: shell → last ~400 chars of output; delegate → first ~400 chars of the report. Saves the `job_read_output` round trip in the common case (the harness's inline-final-report pattern); `watchFrameMessageWithDeliveryID`-style truncation discipline. Lands with Phase 2.

## 5. Contract touchpoints (merge into the task #14 one-pass sweep)

New rows beyond the mailbox spec's §8 table: `:509` (trigger→`every`, P4), `:516` (frame always included, P5), `:610`/`:640` (limit_bytes removed, full-scan grep — A5 re-decision, P2), `:181`/`:292` (combo rejection wording, P6), `job_list` result shape (cursor removal P1, watches array F2), notification block shape (F3 excerpt), and the availability/section note for the new prompt section if the contract references prompting (check `:804-813` region). Plus the already-queued Track E items (`:534` priority, `:110`, `:958`, `:946` grants carve-out, §8 blocking-grep row).

## 6. Sequencing & ownership

1. **Phase 1 merge + my review** (unchanged).
2. **Phase 1.9 (this addendum):** prompt section + P1-P6 + §3 descriptions + their contract rows + Haiku validation. One coordinator or inline; touches `definitions.go`, `session_tools_jobs.go`, `job_watch.go` (P4 validation), `agent/prompts/sections/`, contract, tests. Supersedes Track C's interim `DefJobReadOutput` description text (merge C's code first; §3 text wins).
3. **Phases 2-4** as planned, briefs updated to the new surface; F1-F3 ride Phase 2.
4. **Phase 5** sweep verifies §5's combined row list.
