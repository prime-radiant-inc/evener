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
- A recommended option, when the model has one, goes first and sets `recommended: true` (optional bool, at most one per question, handler-validated). A schema field on purpose: appending "(Recommended)" to the label would pollute the answer-identity key that `selected` validation and the §4.4 echo match against.
- Option labels must be unique within a question (handler-validated at execution; a violation returns an instructive error result to the model) — `selected` matches by label.

### 4.3 Answer payload (wire) and resolutions

Each question resolves to exactly one of:

| `resolution` | payload | meaning |
|---|---|---|
| `selected` | `selected: ["Postgres"]` (len 1, or 1..N when `multi_select`) | user picked option(s) |
| `free_text` | `text: "use RDS, not self-hosted"` | user typed their own answer |
| `delegated` | optional `text` (a leaning, e.g. "lean cheap") | "you decide" — the model chooses, honoring the leaning |
| `fallback` | — (valid only when `if_unanswered` present) | user tapped "do that": proceed with the stated fallback |
| `skipped` | — | user explicitly declined to answer this one |

Any resolution may carry `note: string` — the annotation (Jesse's hard requirement). A note can qualify or override the selection ("Postgres — but only the primary"); the tool description tells the model to read it before acting. Renderers must offer the note affordance on **every** resolution path: the note control is question-level, not a property of option chips, so fallback ("do that"), skip, "you decide" (whose *leaning* is a separate field), and free text are all annotatable (§6).

Alternatively the user dismisses the whole form with prose: the payload is `formResponse: string` (camelCase everywhere — wire and `ToolState`) with no per-question answers (mutually exclusive; the daemon rejects mixed payloads). The model sees the prose as the reply.

### 4.4 Result to the model

The handler returns a `tool.StateResult`: readable text for the model, structured JSON in the `ToolState` side channel for renderers.

Text format (stable, tested; **every echoed model- or user-authored string renders Go-`%q`-quoted**, so embedded quotes, commas, and newlines cannot corrupt the framing — this text is the only representation the model reads):

```
User answered (4 questions):
1. [DB choice] → "Postgres" — note: "only the primary"
2. [Naming] → you decide — leaning: "short names" — note: "re-ask if it gets weird"
3. [CI matrix] → skipped (no answer) — note: "irrelevant after #2"
4. [Endpoint] → free text: "use RDS, not self-hosted"
```

Every resolution line accepts the trailing `— note: "…"` suffix — the annotation is universal, on `skipped` and `fallback` too; on `delegated`, `leaning` renders before `note`, both quoted. `fallback` renders as `→ do your stated fallback ("default to Postgres and note the assumption")`. `formResponse` renders as `User replied instead of answering the form: "…"`. Multi-select joins **quoted** labels — `→ "A", "B"` — unambiguous even when a label contains a comma.

`ToolState` carries `{answers: [...], formResponse?: string}` (camelCase, byte-identical keys to the wire payload) so both renderers draw the resolved card without parsing prose.

### 4.5 Tool description (draft; Haiku-validate at implementation, house style)

> Ask the user structured questions and wait for the answers. Use when a decision is genuinely the user's — not resolvable from the request, the code, or evidence you can gather with tools.
>
> - `questions`: 1–4 per call, each with a short `header` (≤12 chars), the full `question`, and 2–5 `options` (`{label, detail}`). Set `multi_select` to allow several.
> - Do not add an "Other" or free-text option; the UI always offers one, plus "you decide".
> - Optional per question: `why` (one line: what the answer changes) and `if_unanswered` (the fallback you would take; the user can accept it with one tap).
> - Dispatch alone: this must be the only tool call in your response.
> - The call blocks until the user answers or interrupts. There is no timeout.
> - Each answer is a selection, free text, "you decide" (choose with your judgment, honoring any stated leaning), your stated fallback, or skipped (the user declined: proceed on your best judgment, state the assumption you make, and do not immediately re-ask). Any answer may carry a user note — read it; it can qualify or override the selection.
>
> First try to resolve the question yourself with tools. Batch related questions at a natural breakpoint into one call instead of asking serially. When you have a recommendation, put that option first and set `recommended: true`. The user may ignore the form and reply in prose; treat that prose as the reply.

### 4.6 System-prompt guidance

New prompt section (`agent/prompts/sections/ask-user.md.tmpl`), included only when the tool is registered (interactive root sessions), in the terse imperative of the job-control section:

> **Asking the user.** Ask when being right matters more than the interruption costs — not whenever you are unsure. First resolve what evidence can settle: read the file, run the test. Ask only what evidence cannot settle. Batch questions that share a breakpoint into one `ask_user` call (≤4) rather than asking serially. Write honest options — no straw men — and state `why` and `if_unanswered` when they help the user decide fast. The user's `note` on any answer can qualify or override the selection; honor it.

The existing non-interactive section (`agent/prompts/sections/non-interactive.md.tmpl`) already covers the inverse case and does not change.

## 5. Runtime semantics

### 5.1 Blocking

The handler registers like any core tool (`registerCoreTools` → new `registerAskTool`, `agent/session_tool_registry.go`) with **`ReadOnly: false`**, deliberately: the read-batch executor may run ReadOnly tools concurrently in goroutines (`agent/session_tool_round.go:130-174`), and a blocking ask must execute serially on the turn goroutine. It blocks inside `Exec` on the session's single pending-ask slot:

```go
type pendingAsk struct {
    callID    string
    questions []askQuestion
    resolved  bool               // the linearization point, guarded by s.mu
    answered  chan answerPayload // buffered, capacity 1
}
```

```
select {
case ans := <-pending.answered:  → build StateResult
case <-ctx.Done():               → under s.mu: if a validated answer already won the race
                                   (resolved set by AnswerAsk; payload buffered), drain and
                                   use it; otherwise set resolved and return the canceled
                                   result ("question dismissed; turn interrupted")
}
```

The `resolved` flag under `s.mu` is the single linearization point: `AnswerAsk` sets it and sends on the buffered channel (never blocks); cancellation sets it and beats any later answer. **Lock protocol (normative):** the handler installs the slot and sets awaiting under `s.mu`, then **releases `s.mu` before blocking** on the select — it never parks holding the lock (`s.mu` also guards steering, queueing, snapshots, and Close; holding it would deadlock `AnswerAsk` itself and starve §5.5's steering). It re-acquires `s.mu` only inside the `ctx.Done` branch. Cleanup — clear the slot, write the tombstone, clear awaiting — runs in a **`defer`**, so a handler panic cannot strand the session stuck-awaiting (`execTool` has no recover, and `ReadOnly:false` keeps the ask off the panic-recovering read-batch goroutines). After the slot clears, the session retains the resolved ask's `callID` as a **tombstone** until the next ask installs: a late `AnswerAsk` against the tombstone deterministically returns `already_answered` instead of racing between `already_answered` and `no_pending_ask` on the handler's clear. This delivery is a **new** synchronous handoff to a handler blocked mid-round: serf's mailboxes deliver only at round boundaries, and the `shell` foreground-wait precedent covers only the cancellation half (`turn/interrupt` → `SetCancelFunc` → ctx cancel already unblocks in-flight tool waits). The semantics above are therefore normative, not an analogy.

Unbounded wait is deliberate and safe here: the wait is on a human, the thread status says so (§5.4), and `ctx` cancellation covers interrupt and shutdown. Job-control's ≤60s clamp governs model-vs-machine waits and does not apply; the job-control doc gains a sentence saying so (§9).

**Dispatch-alone rule:** if the model batches `ask_user` with other tool calls in one response, the batch executor fails the `ask_user` call (the siblings run normally). The check sits at the **top of `execToolBatch`, before the parallel/serial branch** — the read-batch split exists only inside the parallel branch (`agent/session_tool_round.go:130`), and `parallel` defaults false, so most profiles take the plain serial else-branch. The error: `ask_user must be the only tool call in your response; re-issue it alone.` This structurally prevents the Claude Code parallel-batch bug and keeps "at most one pending ask per session" an invariant (single `pendingAsk` slot on `Session`, guarded by the session mutex).

### 5.2 Answer path

New appwire request **`thread/ask/answer`**:

```jsonc
{ "threadId": "...", "callId": "...",          // callId = the ask_user tool-call ID
  "answers": [ { "questionIndex": 0, "resolution": "selected", "selected": ["Postgres"], "note": "only the primary" } ],
  "formResponse": null }
```

**Call-ID identity:** the pending ask registers under the same ID every surface sees. When the provider sends an empty tool-call ID, `Registry.ExecuteCall` synthesizes one (`agent/internal/tool/registry.go:449-451`) while `EventToolCallStart` emits the raw ID — the normalization lives in `execTool` (before the START emit at `agent/session_tools.go:349` and the `ctxToolCallID` value at `:395`), not in the registry, so START, END, and `AnswerAsk` addressing always agree.

**Routing inventory** (the full fan-out a new hub-routed method needs; `turn/steer` is the template): `appwire/types.go` method constant + `appwire/protocol.go` catalog entry (cross-check tests enforce that pair — **capability fields have no such cross-check**), an `appwire.Client` method (shared by the TUI and the hub's `LocalDaemonSource`), the `appsource.Source` interface and its implementations, a daemon func seam on `server.Server` wired in `cmd/serf/serve.go` (the steerFunc pattern), `assets/appwire.js`, and a `docs/appwire-protocol.md` regeneration. Capability plumbing is wider than it looks and already drifts: the web client copies caps through **two** hardcoded allowlists (`renderer.js` `refreshCapabilitiesForStatus` ~:289-292 and the `SESSION_START` block ~:943-946), `/status` carries a separate hardcoded `ActionCapabilities` struct that is already missing `Goal` (`server/server_handlers.go:273-282`), and the hub tree hardcodes caps in `LocalDaemonSource.threadFromEntry` (`local_daemon.go:542-553`). `Answer` must thread through each; implementation should add (or explicitly decline) a capability-field cross-check.

The hub's action gate **fails closed** (`threadActionAvailable` default → `Unavailable`, `cmd/serf-hub/app_compact.go:59-94`), so `ThreadCapabilities` gains **`Answer`**. Its truth is the pending-ask accessor (§5.4) read at capability-computation time. Capabilities are **never pushed** (`ThreadStatusChangedParams` carries only `Status`), so a live client learns of a new ask from the `awaiting` **status push**, which triggers its existing capability refetch (`refreshCapabilitiesForStatus`) — the status push is the load-bearing live signal; the capability is its refetched consequence and the answer-mode cue on read. The gate's check-then-forward is a tolerated TOCTOU: an ask that resolves inside the window passes the gate and is then rejected by `AnswerAsk`'s validation, which is the **authoritative** guard.

The daemon handler calls a new external `Session` method `AnswerAsk(callID, payload) error` (an external method under `s.mu`, per §5.1's linearization) which:

1. Validates: `callId` matches the live pending ask (none, or a mismatch — e.g. after a daemon restart — → typed error `no_pending_ask`); every question resolved exactly once (answers cover all indices — `skipped` is the explicit opt-out — or `formResponse` alone); `selected` labels ⊆ that question's options, len 1 (or 1..N when `multi_select`); `fallback` only where `if_unanswered` exists; `free_text` requires non-empty `text`; `delegated` `text` (the leaning) is optional. Invalid → typed error `invalid_answer{reason}`; **the ask stays pending** and the client re-prompts. Malformed human input never reaches the model (the re-prompt loop lives below the model — a gap in every surveyed protocol).
2. First answer wins: resolving an already-resolved ask returns `already_answered`; the losing client re-renders from the `TOOL_CALL_END` notification it will receive anyway.
3. Hands the payload to the blocked handler per §5.1's linearization (set `resolved`, buffered send). The handler — the session's own loop, blocked inside `execTool` — builds the result. The invariant's spirit holds: the external caller only validates and hands off; the owning loop delivers to the model.

### 5.3 Interrupt, shutdown, restart

- **Interrupt** (`turn/interrupt`) or **Close**: ctx cancels; the handler returns a canceled-result (`question dismissed; turn interrupted`); renderers clear the form on `TOOL_CALL_END`. Standard turn-interrupt semantics, nothing new.
- **Daemon restart with a pending ask:** the existing orphan repair (`agent/history_repair.go`) synthesizes the standard "tool result unavailable" error on the next activation; the model re-asks if the question still matters. Honest, cheap, and store-free. (Parking, when it arrives, adds durability; v1 does not need it.)
- **Answer submitted to a dead daemon:** hub surfaces the transport error to the client; nothing to clean up.

### 5.4 The `awaiting` status — one emission point, three plumbing legs

While an ask is pending the thread presents **`ThreadStatusAwaiting`**; on resolution it returns to the active-turn status. **Awaiting is not a stored flag: it is slot presence.** `PendingAsk().ok`, derived at read time, is the sole definition of awaiting; the ask handler is the slot's only writer. That single ownership is the structural fix for Claude Code's "permission prompts notify, questions don't" divergence — and `EventAwaitingInput` is a push hint, never a source of truth.

The consumers are dormant but real (NeedsYou tier, `◆N` badge, attention pill — `hubcore/tree.go` AttentionRank/NormalizeState — and OS notifications). The **producer side does not exist and must be built**; adversarial review confirmed a pending ask is mid-turn, where every existing leg reports `active`:

**Source of truth: the pending-ask slot, not the event.** The session exposes a lock-leaf accessor — `Session.PendingAsk() (callID string, ok bool)`, taking only `s.mu`, briefly — and `cmd/serf/serve.go` wires it to the server as a func seam (the `SetCancelFunc` pattern). Round-2 adversarial review proved the event channel alone cannot carry this state: `sendEvent` **drops on a full buffer** (`agent/session_events.go:96-102`), and one dropped, never-re-asserted awaiting event would silently reproduce the exact "questions don't notify" bug this feature exists to fix. The event is a push optimization; every read path consults the accessor and self-heals.

1. **Snapshot + poll + capabilities** — `appStatus()` returns `active` for any in-flight turn before its `awaiting` case can match (`server/appwire_runtime.go:614-616`); it and `appCapabilities` gain the pending-ask input via the func seam, with awaiting beating `processing`. The Bridge does **not** write awaiting into the shared state string — that would add a second async writer racing the turn-end `SetState` (a buffered clear processed after turn-end would stick stale). Caution: `appStatus` is a shared pure function with several callers; reordering is safe only while the ask is `awaiting`'s sole producer — say so at its definition.
2. **Live push** — new event kind `EventAwaitingInput {Pending bool}` in `agent/events` (the one exception to §6's no-new-events economy: the projector emits `NotifyThreadStatusChanged` only at session-start / user-input / goal-continuation / session-end, `internal/appprojector/appwire_projection.go:90,132,179,642`), plus a projector case emitting `threadStatus(awaiting)` on pending and `threadStatus(active)` on clear. Best-effort **by design**: it only prompts clients to refetch; a dropped event costs latency, never correctness.
3. **Test readiness** — the accessor doubles as the deterministic-test synchronization point (§8): tests wait on `PendingAsk()` before answering, closing the install-vs-answer race.

Capabilities while an ask is pending: `Send` stays false (a turn is in flight); `Steer`, `Queue`, `Interrupt` stay true (§5.5); the new `Answer` capability (§5.2) is true. Producing `awaiting` also stops a real mislabeling: today the web liveness watchdog would call a blocked ask "working · quiet ~2m", then "may be stalled" (the quiet-state machine exits only when state ≠ `active`, `renderer.js` ~2050-2102) — with `awaiting` pushed, it exits correctly.

One trigger update ships with this feature: `assets/notifications.js` fires on `idle→awaiting` today; change to `*→awaiting` since our transition is `processing→awaiting`.

### 5.5 Steering while a question is pending

`turn/steer` and `turn/queue` remain available during a pending ask (capabilities unchanged) with their normal semantics: they append to their queues and drain at the next boundary — which is **after** the ask resolves. That deferral is intended, and now stated. The composer must never silently convert intended steering into an answer: while an ask is pending the composer defaults to **answer mode with a visible indicator** ("answering ◆") — flipping only if the composer is empty when the ask lands; text already mid-composition stays in steer mode, with the indicator offering the switch — and one explicit affordance (web: the existing steer control; TUI: a mode toggle in the overlay/chip footer) sends the same text as a steer instead. Prose submits as `form_response` only from answer mode. The web's optimistic-send reconciliation gains the answer method alongside steer/queue.

### 5.6 Hooks

`ask_user` runs through `execTool` like every tool: PreToolUse may deny or rewrite the questions; PostToolUse observes the result. Stated invariant with a test (the Cursor bug class).

## 6. Renderers

Rendering rides the existing tool events — `TOOL_CALL_START` already carries the questions as `ArgumentsJSON`; `TOOL_CALL_END` carries the result (+ `ToolState` JSON) — with no new turn kinds and no per-question notification methods. The complete new wire surface is exactly: one event kind + projector case + the pending-ask func seam feeding `appStatus`/`appCapabilities` (§5.4), and the `thread/ask/answer` method + `Answer` capability (§5.2). **Cold attach:** an unmatched `ask_user` call in the transcript renders as the answerable pending form **only when the thread status is `awaiting`** (a live pending ask exists). Unmatched call without `awaiting` — the daemon restarted while the question was pending — renders as a settled "question expired (session restarted); the agent will re-ask if it still matters" line, never an answerable form; a stale submission would get `no_pending_ask` anyway.

### 6.1 Web (`cmd/serf-hub/assets/`)

Activate the dormant scaffolding and build the designed-but-never-built answering layer:

- **Container:** `markAgentQuestion` (renderer.js:1928, amber "◆ Needs you" frame — built, never called) wraps the question card; `renderNeedsYouDock`/`jumpToAgentQuestion` (renderer.js:4179+) dock and deep-link it. CSS already shipped (`style.css` `.agent-question`, `.needs-you-dock`).
- **Answering (mockup 16 Alt D, `docs/web-ui/mockups/16-blocking-needs-you.html`):** per question — quick-reply chips for options (checkboxes when `multi_select`), a "Something else…" free-text row, a "You decide" row (optional leaning field), a "do that" button when `if_unanswered` is present, a skip affordance. Multi-question calls stack as one card, answerable in any order, with an answered-count line and a single **Send answers** action. Per the mockup's written guidance: amber owns the container, blue owns the primary action.
- **Annotation (split control, question-level):** the chip body answers bare — the fast path costs nothing. A low-contrast trailing `+` (or `.` with the chip focused) opens a one-line note field; the note belongs to the **question** and attaches to whichever resolution is chosen — option picks, the fallback "do that" button, skip, "you decide", and free text are all annotatable (the hard requirement; round 2 caught that chip-only wiring missed three of five paths). **Send** carries `{resolution…, note}`. Never a modal.
- **Composer coexistence (the lens rule):** the form never traps input — and never steals it. While an ask is pending the composer shows the answer-mode indicator and prose submits as `form_response`; one explicit affordance sends the same text as a steer instead (§5.5). `Esc` collapses the card to a "◆ question waiting" chip; the dock and status keep it findable.
- **Resolution feedback:** on `TOOL_CALL_END` the card collapses to a neutral settled line echoing the answers ("→ Postgres · you decide · skipped"); if another client answered first, this same path renders it — the losing tab just sees the question settle.
- **Notification deep link:** clicking the OS notification lands on the session with `jumpToAgentQuestion` focused on the first unanswered question.

### 6.2 TUI (`cmd/serf-tui/`)

A question overlay built from existing widgets (`internal/tuipick`), added to `focus_trap.go`'s priority list — and, deliberately, **never auto-opened**. Every existing TUI overlay opens from a keypress; none opens from the notification dispatcher, and auto-trapping focus when an ask lands would steal keystrokes from a user mid-steer, violating the lens rule. Instead the ask renders as an in-transcript question card (mirroring the web) plus a persistent composer-chrome chip, and the overlay opens on the chip's keybinding:

- One question at a time, **paged with a header-chip strip** when N>1, plus a **review step** before submit (the convergent Codex/Gemini/OpenCode shape): review lists `header → answer` pairs and warns on unanswered questions ("submit with 1 unanswered → it resolves as skipped").
- Options render via `PickerPanel` with two appended rows: "Something else…" and "You decide" (each opens `TextInputModal`). `.` opens the **question-level note field** — the annotation attaches to whichever resolution is chosen, so fallback, skip, and "you decide" are annotatable too, not just option picks. A "do that (fallback)" row appears when `if_unanswered` is present. Footer: `↑/↓ choose · enter answer · . note · tab next question · esc defer`.
- The chip `◆ question waiting — ctrl+q to answer` (binding chosen at implementation against the existing keymap; `ctrl+q` is the candidate) shows whenever an ask is pending; `ctrl+q` opens the overlay and `Esc` closes it back to the chip. The composer follows the §5.5 answer-mode rule (visible indicator; explicit toggle to steer).
- The transcript reducer already retains `ThreadItem.Raw` (`cmd/serf-tui/internal/transcript/types.go:46`, `reducer.go:475`); the ask renderer parses the `ToolState` JSON from it for the resolved card, as the web reads `data.raw`.
- Answers submit over the same `thread/ask/answer` RPC; the TUI renders resolution from `TOOL_CALL_END` like any tool.

## 7. Invisibility

Gated at the **registration seam**, on the true root condition. Deliberately NOT via `rootOnlySubagentTools()`: that list's removal is allowance-gated (`agent/session_init.go:613` skips it when `delegation_allowance > 0` — correct for `delegate`/`job_watch`, which allowance legitimately grants), so a coordinator subagent would keep anything on it. Adversarial review caught this; `ask_user` needs a gate no allowance can re-open:

1. `registerAskTool` runs only when `!cfg.NonInteractive && !isSubagentSession`, where `isSubagentSession` is true when `cfg.spawn.parentSessionID != ""` (live spawns and job restores) **or** the restored `meta.IsSubagent` flag is set (`agent/schema/snapshot.go:54-56`). The runtime spawn carrier alone is not enough: `spawn` is `json:"-"` (never persisted) and a bare `serf serve --resume <delegate-id>` restores with an empty carrier (`agent/session_init.go:275-280` overwrites `cfg.spawn` only when the caller passes one) — which would have leaked the tool into a resumed subagent; round-2 review caught it. `RestoreSessionFromMetaWithConfig` therefore derives the flag from `meta`, and the exec-time guard (point 4) consults the same predicate. A *forked root* stays a root: fork lineage lives in `meta.ParentSessionID` with `IsSubagent == false`, distinct from `spawn.parentSessionID` — verified. `NonInteractive` (`agent/session_config.go:88`) today drives prompt text and eval-mode task seeding (`agent/session_init.go:167`); this adds its first tool-availability consumer. One-shot `serf <prompt>` hardcodes `NonInteractive: true` (`cmd/serf/run.go:187`), so headless runs never see the tool. `serf serve --non-interactive` likewise.
2. `grant_tools` cannot re-add it: grants validate against the parent's registry and the protected set (`agent/subagents.go:483`); the rejection message gains an `ask_user`-appropriate variant (the existing text is delegation-specific).
3. Unregistered = invisible **and** unexecutable: `rebuildToolDefsCache` and `Registry.ExecuteCall` read the same registry, so a hallucinated call hits the unknown-tool error.
4. Defense in depth (the Gemini pattern): the handler itself re-checks at exec time and returns `ask_user unavailable: no interactive user in this session; decide with your best judgment` — covers config drift and future registration refactors.

## 8. Testing

**Deterministic (agenttest scripted adapter; answers injected via `Session.AnswerAsk` — the same seam the RPC uses):**

- answer-in-flight: scripted model calls `ask_user`; the answering goroutine first waits on `Session.PendingAsk()` — the readiness signal (the scripted adapter returns before the handler installs the slot, so answering without it races `no_pending_ask`) — then answers; result text matches §4.4 exactly; turn continues.
- mixed resolutions: selected+note, free_text, delegated+leaning, fallback, skipped in one call.
- `form_response` dismissal; mixed payload rejected.
- validation loop: invalid answer → `invalid_answer`, ask still pending, model saw nothing; then a valid answer lands.
- first-answer-wins: concurrent `AnswerAsk` calls — exactly one succeeds, loser gets `already_answered`.
- interrupt-cancels: ctx cancel while pending → canceled result, turn ends per interrupt semantics.
- dispatch-alone, both branches: a batched ask fails identically under a parallel-capable profile and a `parallel=false` profile (the serial else-branch).
- invisibility: `NonInteractive` and subagent sessions exclude `ask_user` from `cachedToolDefs`; forced execution returns the exec-time guard error.
- resume gating: restoring a delegate's session id via bare `serve --resume` (empty spawn carrier, `meta.IsSubagent` set) still excludes `ask_user`.
- hooks: PreToolUse deny blocks the ask; PostToolUse observes the result.
- status: awaiting produced on block, cleared on resolution, on interrupt, and on handler panic (the defer); `appStatus` reports `awaiting` while processing.
- status source of truth: with the session event channel saturated (events dropped), snapshot/poll/capabilities still report awaiting via the accessor.
- steer-during-ask: `Steer` during a pending ask defers (drains after resolution) and never resolves the ask.
- answer-vs-interrupt race: concurrent cancel + valid answer resolve deterministically per §5.1 (an answer that wins the linearization point is used; a later one gets `already_answered`; a later cancel still uses the buffered answer).
- callId identity: an empty provider tool-call ID never yields a form whose `callId` differs from the result's.
- goal: `ask_user` rounds do not reset the goal no-progress breaker.
- capability: `thread/ask/answer` is `Unavailable` when no ask is pending; `Answer` advertises only while one is.
- restart staleness: with no live pending ask, `AnswerAsk` returns `no_pending_ask`; a transcript with an unmatched ask call and a non-`awaiting` status renders the expired line, not a form (JSDOM + TUI corpus cover the render).

**Fuzz:** answer-payload validation (`FuzzAnswerValidate`: random payloads against random question sets never panic, never accept an invalid payload; corpus under `agent/testdata/fuzz/`).

**Renderer:** JSDOM tests (pending form render from `TOOL_CALL_START`, chips/annotation/skip/send, resolved card from `TOOL_CALL_END`, cold-attach pending form, composer-as-answer routing); TUI sample-corpus renders (`tui_samples.go`: in-transcript question card, waiting chip, overlay single + multi + review step — the overlay opens only by keypress, never from a notification) across themes.

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

**Goal-engine interaction (specified):** the breaker folds per *completed* continuation turn, so a blocked ask cannot "count as stalling." The real risk is the inverse: `callsMadeProgress` treats any non-ReadOnly call as progress (`agent/session_goal.go:131-144`), so an ask-looping goal would never trip the no-progress breaker. Exclude `ask_user` from `callsMadeProgress` alongside `task_list`.

## 9. Documentation updates (same commits as the code they describe)

- `docs/architecture.md`: add `ask_user` to the tool inventory; one paragraph in "How a turn flows" on the blocking wait + awaiting status.
- `docs/job-control.md`: tool-availability matrix gains the `ask_user` row (root: yes; delegate: never); one sentence distinguishing human-waits (unbounded, ctx-cancelable) from the bounded machine-wait rule.
- `docs/hooks.md`: name-mapping table gains `ask_user ↔ AskUserQuestion`.
- `docs/appwire-protocol.md`: regenerated for `thread/ask/answer` + the `Answer` capability (the protocol catalog cross-check tests enforce the pairing).
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
