# `ask_user`: interactive questions from the agent — design

- **Date:** 2026-07-03
- **Status:** approved design (brainstorm + research complete); pending /par review and Jesse's spec review
- **Scope decision (Jesse):** blocking interactive questions only. No parking in v1 — "we can always add parking later."
- **Policy decision (Jesse):** never auto-proceed. No timers, no synthetic answers, no AFK fallback. An unanswered question waits, visibly, until answered or interrupted.
- **Code anchors** cite the worktree at `ecdbd59bb`; line numbers may drift.

## 1. Problem

Serf's agent has no structured way to ask its user a question. Today the model asks in prose and ends its turn; the thread goes plain `idle`, so nothing distinguishes "done" from "waiting on you." The 2026-07-03 UX diagnostic found this **awaiting-state gap** is the root of the notification complaints: the NeedsYou sidebar tier, amber dot, tab badge, and OS notifications all exist and all starve, because nothing ever produces the `awaiting` status (`appwire/types.go` `ThreadStatusAwaiting` — fully plumbed, zero producers).

Meanwhile a dedicated model-invoked question tool became table stakes across the ecosystem in 2025–26 (Codex `request_user_input`, Gemini CLI `ask_user`, OpenCode `question`, Cline/Roo `ask_followup_question`, Cursor `AskQuestion`, Devin `ask_user_question`), and the HCI evidence says agents under-ask: misread intent shows up in ~27% of real coding-agent sessions, and unprompted guessing is the dominant failure mode, not over-asking.

`ask_user` gives the model a structured, blocking question form; answers return as the tool result; a pending question is the first producer of the `awaiting` status, which lights up the entire dormant needs-you chain by construction.

## 2. Goals and non-goals

**Goals**

1. The root agent asks 1–4 structured, mostly multiple-choice questions in one call and blocks until the user answers or interrupts.
2. Answers return as the tool result, per question, with a user annotation always possible on any choice.
3. A pending question produces `ThreadStatusAwaiting` → NeedsYou tier, badges, OS notification, deep link.
4. Renderers on both surfaces: web (activating the dormant scaffolding) and TUI (new overlay on existing widgets).
5. Invisible — not merely disabled — in non-interactive sessions and in all subagents.
6. Ordinary tool-pipeline citizenship: hooks fire, transcript records the call/result pair, no special-cased plumbing.

**Non-goals for v1** (§10 names each as a fast-follow with its trigger): parking / a question board / ask-and-continue; a durable ask store; withdraw/supersede; timers or auto-answers of any kind; evidence refs beyond prose; stakes-graded rendering; hub sidebar inline answering; wizard chains; images on answers; secret-masked answers.

## 3. Prior art distilled (what we copy, what we avoid)

We copy the convergent schema (questions[] + short `header` chip + 2–5 `{label, detail}` options + always-available free text), the convergent headless stance (remove the tool; never fabricate answers), and the convergent subagent stance (root-only, hard-enforced).

We deliberately avoid five documented failure modes:

| Failure (where) | Our counter |
|---|---|
| Silent AFK auto-continue after 60s (Claude Code; community blowup) | Never auto-proceed. No timer exists. The model may state a fallback (`if_unanswered`); it renders as a button the user taps — it never fires itself. |
| Headless auto-resolve with empty answers in ~37ms (Claude Code bug) | Tool unregistered when `NonInteractive`; exec-time guard returns an instructive error if somehow invoked. |
| Ask tool bypasses the needs-you notification path while permission prompts fire it (Claude Code #59908) | The pending ask **is** the producer of `awaiting`; notification triggers key off the status transition, one emission point. |
| Ask tool skips pre/post-tool hooks (Cursor, staff-confirmed) | `ask_user` is an ordinary registered tool; `execTool` runs PreToolUse/PostToolUse around it. Tested invariant. |
| Breaks when batched with parallel tool calls (Claude Code #60042) | Dispatch-alone rule, enforced: batched `ask_user` fails with an instructive error. |

We also decline Codex's plan-mode-only gating (users file issues begging for the tool everywhere): `ask_user` is available in every interactive root session.

## 4. Tool surface

### 4.1 Name

**`ask_user`** — verb_object, matching `read_file`/`use_skill`. Add `ask_user ↔ AskUserQuestion` to the Claude-compat name table (`agent/internal/toolname/toolname.go`) so existing Claude-style hook matchers apply.

### 4.2 Input schema

```jsonc
{
  "questions": [            // 1..4
    {
      "header": "DB choice",        // required, ≤12 chars; chip/tab label
      "question": "Which datastore for the ingest path?",  // required
      "options": [                   // required, 2..5
        { "label": "Postgres", "detail": "matches prod; heavier local setup" },
        { "label": "SQLite",   "detail": "zero setup; diverges from prod" }
      ],
      "multi_select": false,         // optional, default false
      "why": "the writer refactor depends on it; a wrong guess reworks writer + tests",   // optional, one line
      "if_unanswered": "default to Postgres and note the assumption in the PR"            // optional, one line
    }
  ]
}
```

- The model **must not** add an "Other"/free-text option: the renderer always appends a free-text row ("Something else…") and a "You decide" row to every question. The tool description says so (Codex/Gemini/OpenCode all auto-inject the escape rather than trusting the model to).
- `why` renders as a dim context line — the resumption cue that lets the user answer hours later without re-reading the transcript.
- `if_unanswered` renders as a one-tap **"do that"** button carrying the model's stated fallback. It is a button, never a timer.
- A recommended option, when the model has one, goes first with "(Recommended)" appended to its label — a description convention, not a schema field.

### 4.3 Answer payload (wire) and resolutions

Each question resolves to exactly one of:

| `resolution` | payload | meaning |
|---|---|---|
| `selected` | `selected: ["Postgres"]` (len 1, or 1..N when `multi_select`) | user picked option(s) |
| `free_text` | `text: "use RDS, not self-hosted"` | user typed their own answer |
| `delegated` | optional `text` (a leaning, e.g. "lean cheap") | "you decide" — the model chooses, honoring the leaning |
| `fallback` | — (valid only when `if_unanswered` present) | user tapped "do that": proceed with the stated fallback |
| `skipped` | — | user explicitly declined to answer this one |

Any resolution may carry `note: string` — the annotation (Jesse's hard requirement). A note can qualify or override the selection ("Postgres — but only the primary"); the tool description tells the model to read it before acting.

Alternatively the user dismisses the whole form with prose: the payload is `form_response: string` with no per-question answers (mutually exclusive; the daemon rejects mixed payloads). The model sees the prose as the reply.

### 4.4 Result to the model

The handler returns a `tool.StateResult`: readable text for the model, structured JSON in the `ToolState` side channel for renderers.

Text format (stable, tested):

```
User answered (3 questions):
1. [DB choice] → Postgres — note: "only the primary"
2. [Naming] → you decide — leaning: "short names"
3. [CI matrix] → skipped (no answer)
```

`fallback` renders as `→ do your stated fallback ("default to Postgres and note the assumption")`. `form_response` renders as `User replied instead of answering the form: "…"`. Multi-select joins labels with ", ".

`ToolState` carries `{answers: [...], form_response?: string}` verbatim so both renderers draw the resolved card without parsing prose.

### 4.5 Tool description (draft; Haiku-validate at implementation, house style)

> Ask the user structured questions and wait for the answers. Use when a decision is genuinely the user's — not resolvable from the request, the code, or evidence you can gather with tools.
>
> - `questions`: 1–4 per call, each with a short `header` (≤12 chars), the full `question`, and 2–5 `options` (`{label, detail}`). Set `multi_select` to allow several.
> - Do not add an "Other" or free-text option; the UI always offers one, plus "you decide".
> - Optional per question: `why` (one line: what the answer changes) and `if_unanswered` (the fallback you would take; the user can accept it with one tap).
> - Dispatch alone: this must be the only tool call in your response.
> - The call blocks until the user answers or interrupts. There is no timeout.
> - Each answer is a selection, free text, "you decide" (choose with your judgment, honoring any stated leaning), your stated fallback, or skipped. Any answer may carry a user note — read it; it can qualify or override the selection.
>
> First try to resolve the question yourself with tools. Batch related questions at a natural breakpoint into one call instead of asking serially. Put a recommended option first with "(Recommended)" appended.

### 4.6 System-prompt guidance

New prompt section (`agent/prompts/sections/ask-user.md.tmpl`), included only when the tool is registered (interactive root sessions), in the terse imperative of the job-control section:

> **Asking the user.** Ask when being right matters more than the interruption costs — not whenever you are unsure. First resolve what evidence can settle: read the file, run the test. Ask only what evidence cannot settle. Batch questions that share a breakpoint into one `ask_user` call (≤4) rather than asking serially. Write honest options — no straw men — and state `why` and `if_unanswered` when they help the user decide fast. The user's `note` on any answer can qualify or override the selection; honor it.

The existing non-interactive section (`agent/prompts/sections/non-interactive.md.tmpl`) already covers the inverse case and does not change.

## 5. Runtime semantics

### 5.1 Blocking

The handler registers like any core tool (`registerCoreTools` → new `registerAskTool`, `agent/session_tool_registry.go`) and blocks inside `Exec` on a per-call channel:

```
select {
case ans := <-pending.answered:  → build StateResult
case <-ctx.Done():               → canceled result ("question dismissed; turn interrupted")
}
```

Unbounded wait is deliberate and safe here: the wait is on a human, the thread status says so (§5.4), and `ctx` cancellation covers interrupt and shutdown. This follows the `shell` foreground-wait precedent (`turn/interrupt` → `SetCancelFunc` → ctx cancel already unblocks in-flight tool waits). Job-control's ≤60s clamp governs model-vs-machine waits and does not apply; the job-control doc gains a sentence saying so (§9).

**Dispatch-alone rule:** if the model batches `ask_user` with other tool calls in one response, the batch executor fails the `ask_user` call (the siblings run normally) with: `ask_user must be the only tool call in your response; re-issue it alone.` This structurally prevents the Claude Code parallel-batch bug and keeps "at most one pending ask per session" an invariant (single `pendingAsk` slot on `Session`, guarded by the session mutex).

### 5.2 Answer path

New appwire request **`thread/ask/answer`**:

```jsonc
{ "threadId": "...", "callId": "...",          // callId = the ask_user tool-call ID
  "answers": [ { "question_index": 0, "resolution": "selected", "selected": ["Postgres"], "note": "only the primary" } ],
  "formResponse": null }
```

Hub forwards to the owning daemon like `turn/steer`. The daemon handler calls a new external `Session` method `AnswerAsk(callID, payload) error` (synchronized like `Steer`) which:

1. Validates: `callId` matches the live pending ask (none, or a mismatch — e.g. after a daemon restart — → typed error `no_pending_ask`); every question resolved exactly once (answers cover all indices — `skipped` is the explicit opt-out — or `formResponse` alone); `selected` labels ⊆ that question's options, len 1 (or 1..N when `multi_select`); `fallback` only where `if_unanswered` exists; `free_text` requires non-empty `text`; `delegated` `text` (the leaning) is optional. Invalid → typed error `invalid_answer{reason}`; **the ask stays pending** and the client re-prompts. Malformed human input never reaches the model (the re-prompt loop lives below the model — a gap in every surveyed protocol).
2. First answer wins: resolving an already-resolved ask returns `already_answered`; the losing client re-renders from the `TOOL_CALL_END` notification it will receive anyway.
3. Hands the payload to the blocked handler over the channel. The tool handler — which **is** the session's own loop, blocked inside `execTool` — builds the result. This respects the mailbox invariant: the external caller only validates and hands off; the owning loop delivers.

### 5.3 Interrupt, shutdown, restart

- **Interrupt** (`turn/interrupt`) or **Close**: ctx cancels; the handler returns a canceled-result (`question dismissed; turn interrupted`); renderers clear the form on `TOOL_CALL_END`. Standard turn-interrupt semantics, nothing new.
- **Daemon restart with a pending ask:** the existing orphan repair (`agent/history_repair.go`) synthesizes the standard "tool result unavailable" error on the next activation; the model re-asks if the question still matters. Honest, cheap, and store-free. (Parking, when it arrives, adds durability; v1 does not need it.)
- **Answer submitted to a dead daemon:** hub surfaces the transport error to the client; nothing to clean up.

### 5.4 The `awaiting` status — one emission point

While an ask is pending the thread presents **`ThreadStatusAwaiting`**; on resolution it returns to the turn's normal status. One producer function (e.g. `session.setAwaitingInput(bool)`) flips it around the blocking wait and is the **only** place that produces `awaiting` — the structural fix for Claude Code's "permission prompts notify, questions don't" divergence. Implementation wires it through the existing status pipeline (the dead pass-through at `server/appwire_runtime.go` ~623 and `NormalizeState`, `cmd/serf-hub/internal/hubcore/tree.go:239`, already accept the value).

Downstream, for free once produced: NeedsYou sidebar tier + `◆N` badge + attention pill (`hubcore/tree.go` AttentionRank), and OS notifications. One trigger update ships with this feature: `assets/notifications.js` fires on `idle→awaiting` today; change to `*→awaiting` since our transition is `processing→awaiting`.

### 5.5 Hooks

`ask_user` runs through `execTool` like every tool: PreToolUse may deny or rewrite the questions; PostToolUse observes the result. Stated invariant with a test (the Cursor bug class).

## 6. Renderers

Both surfaces need no new event kinds, turn kinds, or notification methods for rendering: `TOOL_CALL_START` already carries the questions as `ArgumentsJSON`; `TOOL_CALL_END` carries the result (+ `ToolState` JSON). **Cold attach:** an unmatched `ask_user` call in the transcript renders as the answerable pending form **only when the thread status is `awaiting`** (a live pending ask exists). Unmatched call without `awaiting` — the daemon restarted while the question was pending — renders as a settled "question expired (session restarted); the agent will re-ask if it still matters" line, never an answerable form; a stale submission would get `no_pending_ask` anyway.

### 6.1 Web (`cmd/serf-hub/assets/`)

Activate the dormant scaffolding and build the designed-but-never-built answering layer:

- **Container:** `markAgentQuestion` (renderer.js:1928, amber "◆ Needs you" frame — built, never called) wraps the question card; `renderNeedsYouDock`/`jumpToAgentQuestion` (renderer.js:4179+) dock and deep-link it. CSS already shipped (`style.css` `.agent-question`, `.needs-you-dock`).
- **Answering (mockup 16 Alt D, `docs/web-ui/mockups/16-blocking-needs-you.html`):** per question — quick-reply chips for options (checkboxes when `multi_select`), a "Something else…" free-text row, a "You decide" row (optional leaning field), a "do that" button when `if_unanswered` is present, a skip affordance. Multi-question calls stack as one card, answerable in any order, with an answered-count line and a single **Send answers** action. Per the mockup's written guidance: amber owns the container, blue owns the primary action.
- **Annotation (split control):** the chip body answers bare — the fast path costs nothing. A low-contrast trailing `+` (or `.` with the chip focused) opens a one-line note field under the chip; **Send** carries `{selected, note}`. Never a modal.
- **Composer coexistence (the lens rule):** the form never traps input. Typing prose into the composer while an ask is pending submits it as `form_response` — the composer *is* the escape hatch. `Esc` collapses the card to a "◆ question waiting" chip; the dock and status keep it findable.
- **Resolution feedback:** on `TOOL_CALL_END` the card collapses to a neutral settled line echoing the answers ("→ Postgres · you decide · skipped"); if another client answered first, this same path renders it — the losing tab just sees the question settle.
- **Notification deep link:** clicking the OS notification lands on the session with `jumpToAgentQuestion` focused on the first unanswered question.

### 6.2 TUI (`cmd/serf-tui/`)

A focus-trapped question overlay (new entry in `focus_trap.go`'s priority list), built from existing widgets (`internal/tuipick`):

- One question at a time, **paged with a header-chip strip** when N>1, plus a **review step** before submit (the convergent Codex/Gemini/OpenCode shape): review lists `header → answer` pairs and warns on unanswered questions ("submit with 1 unanswered → it resolves as skipped").
- Options render via `PickerPanel` with two appended rows: "Something else…" and "You decide" (each opens `TextInputModal`). `.` on a highlighted option opens the note field (annotation). A "do that (fallback)" row appears when `if_unanswered` is present. Footer: `↑/↓ choose · enter answer · . note · tab next question · esc defer`.
- `Esc` defers: the overlay closes, the composer chrome shows `◆ question waiting — ctrl+q to answer` (binding chosen at implementation against the existing keymap; `ctrl+q` is the candidate), and typing in the composer submits as `form_response`, mirroring the web lens rule.
- Answers submit over the same `thread/ask/answer` RPC; the TUI renders resolution from `TOOL_CALL_END` like any tool.

## 7. Invisibility

Gated at the **registration seam** — the same mechanism that keeps root-only job tools out of subagents (`agent/subagents.go` `rootOnlySubagentTools`):

1. `registerAskTool` is skipped when `SessionConfig.NonInteractive` (`agent/session_config.go:88`) — the flag's first runtime consumer; today it changes prompt text only. One-shot `serf <prompt>` hardcodes `NonInteractive: true` (`cmd/serf/run.go:187`), so headless runs never see the tool. `serf serve --non-interactive` likewise.
2. `ask_user` joins the root-only list, so every subagent — typed or untyped, regardless of `delegation_allowance` — has it removed, and `grant_tools` cannot re-add it (root-only grants are already rejected, `agent/subagents.go:483`).
3. Unregistered = invisible **and** unexecutable: `rebuildToolDefsCache` and `Registry.ExecuteCall` read the same registry, so a hallucinated call hits the unknown-tool error.
4. Defense in depth (the Gemini pattern): the handler itself re-checks at exec time and returns `ask_user unavailable: no interactive user in this session; decide with your best judgment` — covers config drift and future registration refactors.

## 8. Testing

**Deterministic (agenttest scripted adapter; answers injected via `Session.AnswerAsk` — the same seam the RPC uses):**

- answer-in-flight: scripted model calls `ask_user`; test answers; result text matches §4.4 exactly; turn continues.
- mixed resolutions: selected+note, free_text, delegated+leaning, fallback, skipped in one call.
- `form_response` dismissal; mixed payload rejected.
- validation loop: invalid answer → `invalid_answer`, ask still pending, model saw nothing; then a valid answer lands.
- first-answer-wins: concurrent `AnswerAsk` calls — exactly one succeeds, loser gets `already_answered`.
- interrupt-cancels: ctx cancel while pending → canceled result, turn ends per interrupt semantics.
- dispatch-alone: batched `ask_user` fails with the instructive error; siblings unaffected.
- invisibility: `NonInteractive` and subagent sessions exclude `ask_user` from `cachedToolDefs`; forced execution returns the exec-time guard error.
- hooks: PreToolUse deny blocks the ask; PostToolUse observes the result.
- status: awaiting produced on block, cleared on resolution and on interrupt.
- restart staleness: with no live pending ask, `AnswerAsk` returns `no_pending_ask`; a transcript with an unmatched ask call and a non-`awaiting` status renders the expired line, not a form (JSDOM + TUI corpus cover the render).

**Fuzz:** answer-payload validation (`FuzzAnswerValidate`: random payloads against random question sets never panic, never accept an invalid payload; corpus under `agent/testdata/fuzz/`).

**Renderer:** JSDOM tests (pending form render from `TOOL_CALL_START`, chips/annotation/skip/send, resolved card from `TOOL_CALL_END`, cold-attach pending form, composer-as-answer routing); TUI sample-corpus renders (`tui_samples.go`: question overlay single + multi + review step, deferred chip) across themes.

**E2E scenario cards** (`test/scenarios/`, house template with falsification lines):

| card | falsification line |
|---|---|
| `ask-web-answer.md` | if the thread never shows `awaiting` or the answer does not appear as the tool result, the feature is broken |
| `ask-tui-answer.md` | if the overlay traps `esc` or the composer cannot submit prose as the answer, the lens rule is broken |
| `ask-interrupt-dismiss.md` | if interrupt leaves the form live or the turn hung, cancellation is broken |
| `ask-two-clients-first-wins.md` | if both clients' answers reach the model, or the loser gets no feedback, arbitration is broken |
| `ask-noninteractive-invisible.md` | if `ask_user` appears in the tool list of a `--non-interactive` or one-shot session, gating is broken |
| `ask-subagent-invisible.md` | if a delegate can call or see `ask_user`, root-only gating is broken |

**Pre-implementation spike (task 0):** prove `turn/interrupt` cancels an in-flight blocked tool wait end-to-end (shell's foreground wait says yes; verify before building on it).

**Implementation verification item:** confirm the goal engine's no-progress breaker does not count a turn blocked on `ask_user` as stalling; if it does, exempt pending-ask time from the breaker's accounting.

## 9. Documentation updates (same commits as the code they describe)

- `docs/architecture.md`: add `ask_user` to the tool inventory; one paragraph in "How a turn flows" on the blocking wait + awaiting status.
- `docs/job-control.md`: tool-availability matrix gains the `ask_user` row (root: yes; delegate: never); one sentence distinguishing human-waits (unbounded, ctx-cancelable) from the bounded machine-wait rule.
- `docs/hooks.md`: name-mapping table gains `ask_user ↔ AskUserQuestion`.
- `docs/web-ui/`: mark mockup 16 Alt A/C/D as shipped; note the notifications.js trigger change.
- New scenario cards indexed per `test/scenarios/README.md`.

## 10. Fast-follows (each named with its trigger; none block v1)

| follow-up | trigger | v1 affordance that keeps the door open |
|---|---|---|
| **Park / question board / durable askstore / mailbox delivery** | recurring want of ask-and-continue (Cursor 2.4 proved the value) | `callId`-keyed answer RPC, extensible `resolution` enum, board-capable renderers (multi-question card ≈ board of one call) |
| withdraw/supersede stale questions | park exists | `callId` addressing |
| stakes field + reversibility-proportional friction | destructive-action asks appear | schema is additive |
| evidence refs on options (file/diff/job) | options citing artifacts | `detail` is prose today; typed refs additive |
| hub sidebar inline answering + next-needs-you loop | multi-session answering pain | NeedsYou tier already aggregates |
| `ask_list` / decision-record query layer | cross-session re-asking observed | Q&A already durable + greppable in the transcript |
| images attached to answers | first real need (Cline/Roo precedent) | `ProcessInput` already carries images; RPC gains a field |
| distinct "question" notification sound (OpenCode precedent) | web notification channels get sounds | trigger table already keyed by transition |
| unattended-grace auto-resolve | real demand only; **user/project config, dark by default, never model-settable** | `if_unanswered` is already the disclosed fallback it would use |
| secret-masked answers | probably never — MCP URL-mode elicitation is the right path for credentials | — |

## 11. Out-of-repo coordination

A parallel session owns the broader web-UI/UX redesign (2026-07-03 diagnostic). This feature deliberately produces the `awaiting` status that work identified as the #1 gap; the only shared file surfaces are `renderer.js`/`notifications.js`/`style.css` activation points. Coordinate before merging if both land in the same window.
