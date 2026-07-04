# `ask_user`: interactive questions from the agent — design

- **Date:** 2026-07-03
- **Status:** approved design, reshaped per Jesse's simplification directive; pending Jesse's spec review
- **Scope decision (Jesse):** blocking interactive questions only. No parking in v1 — "we can always add parking later."
- **Policy decision (Jesse):** never auto-proceed. No timers, no synthetic answers, no AFK fallback. An unanswered question waits, visibly, until answered.
- **Shape decision (Jesse):** **asks end the turn at their round's boundary.** The model batches `ask_user` calls — several allowed — and optionally a `communicate` into a single round; when that round's calls have executed, the turn ends and the session rests in `awaiting`. `communicate` composes rather than collides: its message delivers alongside the questions. Asking always yields the floor — ask-then-keep-working is cross-turn park, a named fast-follow (§10). The answer arrives as the **next user message** (the form composes it; typed prose works identically). No mid-turn blocking, no new wire methods. An earlier blocking-in-handler design was fully specified and three /par rounds hardened (git history `771aeeb42..fc559fb83`); its complexity was the cost of the shape, and the shape was wrong. Findings that survive the reshapes are folded below.
- **Code anchors** cite the worktree at `ecdbd59bb`; line numbers may drift.

## 1. Problem

Serf's agent has no structured way to ask its user a question. Today the model asks in prose and ends its turn; the thread state is plain `idle`, so nothing distinguishes "done" from "waiting on you." The 2026-07-03 UX diagnostic found this **awaiting-state gap** is the root of the notification complaints: the NeedsYou sidebar tier, amber dot, tab badge, and OS notifications all exist and all starve, because nothing ever produces the `awaiting` status (`appwire/types.go` `ThreadStatusAwaiting` — fully plumbed, zero producers).

Meanwhile a dedicated model-invoked question tool became table stakes across the ecosystem in 2025–26 (Codex `request_user_input`, Gemini CLI `ask_user`, OpenCode `question`, Cline/Roo `ask_followup_question`, Cursor `AskQuestion`, Devin `ask_user_question`), and the HCI evidence says agents under-ask: misread intent shows up in ~27% of real coding-agent sessions, and unprompted guessing is the dominant failure mode, not over-asking.

`ask_user` lets the model yield the floor with structured questions: the round that asks is the turn's last, and the session rests in `awaiting` — the first real producer of the status the entire dormant needs-you chain keys on. The dormant plumbing assumed exactly this shape: a session at rest between turns, waiting on its user.

## 2. Goals and non-goals

**Goals**

1. The root agent asks structured, mostly multiple-choice questions — 1–4 per call, several calls per round, optionally alongside a `communicate` whose message still delivers; the asking round ends the turn, and the session rests in `awaiting` until the user replies.
2. The user answers through a rendered form (chips, free text, "you decide", skip) or by simply typing — either way the answer is the next user message, and an annotation is always possible on any choice.
3. `awaiting` at rest flows through the existing turn-boundary state write to `/status` → hub prober → roster → NeedsYou tier, badges, OS notification, deep link. One trigger-table line changes; everything else is already built.
4. Renderers on both surfaces: web (activating the dormant scaffolding) and TUI (new overlay on existing widgets, opened by keypress only).
5. Invisible — not merely disabled — in non-interactive sessions and in all subagents, including bare-resumed ones.
6. Ordinary tool-pipeline citizenship: hooks fire, the transcript records the call and its ack, no special-cased plumbing.

**Non-goals for v1** (§10 names each as a fast-follow with its trigger): park / ask-and-continue — asking always yields the floor; the deferred-result shape is the upgrade path; a durable ask store; withdraw/supersede; timers or auto-answers of any kind; evidence refs beyond prose; stakes-graded rendering; hub sidebar inline answering; wizard chains; images on answers; secret-masked answers.

## 3. Prior art distilled (what we copy, what we avoid)

We copy the convergent schema (questions[] + short `header` chip + 2–5 `{label, detail}` options + always-available free text), the convergent headless stance (remove the tool; never fabricate answers), and the convergent subagent stance (root-only, hard-enforced).

We deliberately avoid five documented failure modes:

| Failure (where) | Our counter |
|---|---|
| Silent AFK auto-continue after 60s (Claude Code; community blowup) | Never auto-proceed. No timer exists. The model may state a fallback (`if_unanswered`); it renders as a button the user taps — it never fires itself. |
| Headless auto-resolve with empty answers (Claude Code bug) | Tool unregistered when `NonInteractive`; exec-time guard returns an instructive error if somehow invoked. |
| Ask tool bypasses the needs-you notification path while permission prompts fire it (Claude Code #59908) | A pending ask **is** the `awaiting` state, written at the turn boundary all cross-session surfaces already read. There is no separate notification path to diverge. |
| Ask tool skips pre/post-tool hooks (Cursor, staff-confirmed) | `ask_user` is an ordinary registered tool; `execTool` runs PreToolUse/PostToolUse around it. Tested invariant. |
| Held-open calls and blocked connections (Claude Code parallel-batch bug #60042; MCP breaking-changing away from blocked elicitation) | Nothing is held open. The call completes with an ack and the turn ends; the reply is a new turn. |

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
        { "label": "Postgres", "detail": "matches prod; heavier local setup", "recommended": true },
        { "label": "SQLite",   "detail": "zero setup; diverges from prod" }
      ],
      "multi_select": false,         // optional, default false
      "why": "the writer refactor depends on it; a wrong guess reworks writer + tests",   // optional, one line
      "if_unanswered": "default to Postgres and note the assumption in the PR"            // optional, one line
    }
  ]
}
```

- The model **must not** add an "Other"/free-text option: the renderer always appends a free-text row ("Something else…") and a "You decide" row to every question (Codex/Gemini/OpenCode all auto-inject the escape rather than trusting the model to).
- `why` renders as a dim context line — the resumption cue that lets the user answer hours later without re-reading the transcript.
- `if_unanswered` renders as a one-tap **"do that"** button carrying the model's stated fallback. It is a button, never a timer.
- `recommended` (optional bool, at most one per question, handler-validated) marks the model's suggestion; that option goes first. A schema field on purpose: appending "(Recommended)" to the label would pollute the label the reply echoes.
- Option labels must be unique within a question (handler-validated at execution; a violation returns an instructive error result to the model, and the turn does not end).
- Multiple `ask_user` calls may share the asking round (each carrying 1–4 questions); they accumulate into one pending set and the user's single reply resolves them all. Asking ends the turn at that round's boundary; a `communicate` in the same round composes — its message delivers alongside the questions (§5.1).

### 4.3 How answers come back: the reply-message contract

The user's **next message is the answer**. The form composes it in a stable format (every echoed model- or user-authored string renders Go-`%q`-quoted, so embedded quotes, commas, and newlines cannot corrupt the framing):

```
[answers]
1. [DB choice] → "Postgres" — note: "only the primary"
2. [Naming] → you decide — leaning: "short names" — note: "re-ask if it gets weird"
3. [CI matrix] → skipped (no answer) — note: "irrelevant after #2"
4. [Endpoint] → free text: "use RDS, not self-hosted"
```

Per question, exactly one resolution: a quoted selection (multi-select joins **quoted** labels: `→ "A", "B"` — unambiguous even when a label contains a comma), `free text: "…"`, `you decide` (optional `leaning`, then `note`, both quoted), `do your stated fallback ("…")` (only where `if_unanswered` exists), or `skipped (no answer)`. Question numbering is global across the turn's pending set in posting order — spanning multiple `ask_user` calls — and every line carries the header, so the reply stays unambiguous even when clients render the calls as separate cards. **Every** resolution line accepts the trailing `— note: "…"` suffix — the annotation is universal (Jesse's hard requirement), and renderers must offer the note affordance on every resolution path, question-level, not chip-only.

The user may instead ignore the form and type anything: free prose **is** a valid reply, delivered verbatim as the user message. There is no daemon-side answer validation and no invalid-answer state — a reply is a reply; the form's structure lives in the client.

### 4.4 Tool description (draft; Haiku-validate at implementation, house style)

> Ask the user structured questions. Asking yields the floor: when the round containing your `ask_user` call(s) completes, your turn ends and the session waits visibly for the reply (no timeout). Do the work that does not need answers first, then batch every question this decision point needs — several `ask_user` calls may share the round, and a `communicate` in the same round still delivers its message. The answers arrive in the user's next message: either the numbered `[answers]` form (one resolution per question: a selection, free text, "you decide" — choose with your judgment, honoring any stated leaning —, your stated fallback, or skipped — proceed on your best judgment, state the assumption, and do not immediately re-ask) or free prose; treat either as the reply to everything you asked. Any answer may carry a user note — read it; it can qualify or override the selection.
>
> - `questions`: 1–4 per call, each with a short `header` (≤12 chars), the full `question`, and 2–5 `options` (`{label, detail}`, labels unique). Set `multi_select` to allow several; set `recommended: true` on at most one option and put it first.
> - Do not add an "Other" or free-text option; the UI always offers one, plus "you decide".
> - Optional per question: `why` (one line: what the answer changes) and `if_unanswered` (the fallback you would take; the user can accept it with one tap).
>
> First try to resolve the question yourself with tools. Asking is how you end your turn when only the user can unblock the rest — finish the answer-independent work before you ask.

### 4.5 System-prompt guidance

New prompt section (`agent/prompts/sections/ask-user.md.tmpl`), included only when the tool is registered (interactive root sessions), in the terse imperative of the job-control section:

> **Asking the user.** Ask when being right matters more than the interruption costs — not whenever you are unsure. First resolve what evidence can settle: read the file, run the test. Ask only what evidence cannot settle. Asking ends your turn at that round's boundary — finish the answer-independent work first, then batch every question this breakpoint needs into the asking round (≤4 per call, several calls if needed, a `communicate` message alongside if you have one). The user's next message covers everything you asked. Write honest options — no straw men — and state `why` and `if_unanswered` when they help the user decide fast. The user's `note` on any answer can qualify or override the selection; honor it.

The existing non-interactive section (`agent/prompts/sections/non-interactive.md.tmpl`) already covers the inverse case and does not change.

## 5. Runtime semantics

### 5.1 Asking ends the turn at its round's boundary

The handler registers like any core tool (`registerCoreTools` → new `registerAskTool`, `agent/session_tool_registry.go`). It validates its input (label uniqueness, one `recommended` per question — schema constraints like counts and lengths are already enforced by the registry's JSON-Schema validation), adds the questions to the round's **pending set**, and returns a short ack as its tool result. The calls themselves are ordinary: several may share a round, siblings execute normally, and a `communicate` in the same round records its message exactly as today.

The one new mechanism is a single check at the round boundary — the same place `deliverIfCommunicated` already decides whether the turn ends: **if the round posted questions, the turn ends**, and the boundary state is **`SessionAwaiting`** — a new session state alongside idle/processing/closed — instead of idle. `communicate` **composes, never collides**: an explicit `communicate` in the asking round contributes its user-facing message, and its `end_turn` value is moot because the asks already end the turn (Jesse's directive: multiple asks and a communicate in one round cause the turn end, together). A model that asks in what it meant as a mid-turn round simply ends its turn early — the tool description says asking yields the floor, and the boundary enforces it; remaining work continues after the reply.

**Stop hooks are not consulted at an ask-ending boundary.** Pending questions are a stronger stop than a hook's veto: a `Blocked` result would force the model onward to answer its own questions, and a persistent one would exhaust the round cap and strand the session idle instead of awaiting (`agent/session_tool_round.go:385-398` remains communicate-only). Hooks still see every ask through Pre/PostToolUse (§5.5). An **interrupted** turn ends idle as today even if it posted questions — the user is demonstrably present and steering; the cards remain rendered and answerable (§6), and any reply still resolves them. Because no call is ever held open, there is nothing for orphan repair to eat, no cancellation race, no lock protocol. The pending set exists only to inform the boundary check and §4.3's numbering; the transcript remains the questions' durable home (§6 renders from it; §5.4 restores from it).

### 5.2 The answer is the next user message

The user answers through the existing input path — `turn/start` — carrying either the form-composed `[answers]` text (§4.3) or whatever they typed. One reply resolves the **entire** pending set (every card, across all of the turn's `ask_user` calls), and the per-turn pending set clears when the user turn is accepted. `acceptUserInput` runs as for any turn; the session leaves `awaiting` for `processing`; the model reads the reply. **No new wire methods, no new capabilities, no daemon-side answer validation, no first-answer-wins machinery**: if two clients both answer, the first `turn/start` wins by existing semantics and the second client sees the turn begin (its form collapses, §6). A "stale" answer cannot exist — a message to a session is always a valid message.

### 5.3 While awaiting

The session is at rest, and nothing may answer the question on the user's behalf. Round 4 proved the naive holds destroy the state they protect — `processOneInput` sets `SessionProcessing` unconditionally *before* dispatch (`agent/session_lifecycle.go:679`) and the notification no-op path finishes at `SessionIdle` (`:1108-1109`), so a delegate finishing while you read the question would silently turn the needs-you lights off. The holds therefore sit **before** any state transition:

- **Entry gate (autonomous wakes):** while `SessionAwaiting`, non-user entry kinds — `EntryNotification`, `EntryContinuation`, `EntryWatchDelivery` — are refused at the `ProcessInputKind` boundary *before* the Processing transition: no state flip, no wire flicker, no re-fired OS notification. Refused notifications stay durably queued (jobstore/watch-outbox durability untouched) and drain at the boundary after the user's reply. `EntryUserInput` is always accepted.
- **Drain-ladder gate (the asking turn's own boundary):** when a turn concludes with questions pending, the turn-tail drain ladder (`selectDrainNextAction`, `agent/session_lifecycle.go:367-383`) holds its follow-up, notification, and goal rungs — a job notification that arrived *during* the asking turn must not drive the model past the unanswered questions. **The queued-input rung stays live by design:** a message the user queued mid-turn drains as the next user turn and *is* the reply (their words are never held hostage; the model reads them per §4.3 and may re-ask; the card resolves echoing them; the session correctly never rests in `awaiting` in that case — the user is demonstrably present).
- **Goal engine — all four kick paths, not one:** `armGoalContinuation`, `settleGoalOnIdle` (fires at the drain tail even when the arm is gated, `agent/session_goal.go:206-220` via `session_lifecycle.go:627`), `SetGoal`'s at-rest immediate kick (`session_goal.go:43-55` — `/goal` issued while a question is pending arms instead of kicking), and a restored active goal's startup kick. All arm-don't-kick while a question is pending; the armed goal kicks once the reply resolves it. Awaiting time never counts against the no-progress breaker. (Gated on the pending-ask set, not raw state: a session generally awaiting with nothing pending — attention-status-model v5's "async wakes re-arm by design" — lets the goal kick normally; only a genuine unanswered question holds it.)

Steering an awaiting session fails fast exactly as steering any idle session does today (`tui-steer-in-idle-fails-fast` is an existing scenario card); the composer needs no answer-vs-steer mode — typing **is** answering. Interrupt is a no-op (nothing is running). **Compact and Clear are live at rest** and touch the transcript the pending question lives in: v1 refuses `Compact` while a question is pending (gated on the pending-ask set, not the awaiting rest state — a session generally awaiting with nothing pending lets `Compact` proceed) with an instructive error ("a question is pending; reply or clear first" — protecting the pending-ask tail through compaction, goal-objective-style, is the fast-follow), and allows `Clear`, which dismisses the question along with the history and rests idle — an explicit, visible user abandonment.

### 5.4 The `awaiting` status, at rest

`SessionAwaiting` rides the **existing** turn-boundary state write (the same path that sets idle at turn end, `cmd/serf/serve.go` turn-end `SetState` — verified to write `string(sess.State())` verbatim), so it is at rest in the daemon's state string — which is exactly what every consumer already reads. **The string value is normative:** `SessionAwaiting SessionState = "awaiting"`, byte-equal to `appwire.ThreadStatusAwaiting` — the constant's string is load-bearing (`SessionProcessing` is `"active"`, not `"processing"`, and every pass-through switch on the journey defaults unknown strings to idle):

- `/status` returns it as-is → the hub prober (`hubcore/prober.go`) → roster (`roster.go:174`) → `/api/search`, the appwire tree, the sidebar NeedsYou tier + `◆N` badge + attention pill (`hubcore/tree.go` `NormalizeState`/`AttentionRank` — built, dormant), and the TUI dashboard (`hub_dashboard_view.go:308` already renders `awaiting`).
- The appwire snapshot's dead pass-through (`server/appwire_runtime.go` `appStatus` `case ThreadStatusAwaiting`) comes alive with no reordering: the session is not `processing` at rest, so the early-return that doomed the mid-turn design never engages.
- Live push: verified stronger than hoped — the turn-end event already carries the session's *actual* state to the projector, which passes `awaiting` through (`agent/session_lifecycle.go:632-638`, `internal/appprojector/appwire_projection.go:617-618`). The push leg needs zero new code, and `notifications.js`'s title count and favicon already handle `awaiting`; only the one trigger line is new.
- **Superseded by the merged attention-status-model v5 (§11 reconciliation):** notifications.js no longer polls or fires on a raw-state trigger table at all — the hub broadcasts `serf/attention/changed` on every attention-*level* transition, and the client fires on a transition *into* `needs_you`. Ask-produced `awaiting` normalizes to `needs_you` exactly like any other producer (`attentionLevel`, `cmd/serf-hub/internal/hubcore/attention.go`); no ask-specific wiring exists or is needed.
- **Restore:** a restarted daemon re-derives the at-rest state at restore time from the transcript tail via a single unified function (`deriveRestoredState`, `agent/session_tools_ask.go`) that generalizes §6's pending definition into attention-status-model v5's resume rule: an unanswered ask (a *completed* ack — ack present inside the round's aggregated `TurnToolResults`, inspected by per-result tool name — with no later user turn) rests `awaiting`, and so, more broadly, does *any* clean turn the agent moved last in (a plain final response, or any other completed tool round) — the ask case is that general rule's specific instance, not a separate one. Trailing steering/checkpoint/summary turns don't count, and a turn whose only tool results are error placeholders (a crash-interrupted round, orphan-repaired) is never mistaken for a completion, ask or otherwise. Two touchpoints, because the wire has its own memory: the Session's state **and the server's** — serve.go writes `SetState(string(sess.State()))` after restore, and the Bridge/projector `SessionStart` handlers carry the restored state instead of today's hardcoded idle (`server/bridge.go:28-32`, `appwire_projection.go:83,90`; round 4 caught that without this, `/status` reads idle until the next turn ends and the needs-you chain stays dark across every restart). If the derivation is wrong in either direction nothing breaks (the form renders from the transcript and answering is always-valid `turn/start`); the check keeps the chain lit.

Capabilities while awaiting are exactly the at-rest set: `Send` true (the session expects input), `Queue` false (`processing`-gated), and steering fails fast as on any idle session regardless of what the func-wired `Steer` capability advertises (existing behavior, existing scenario card). No capability fields are added.

### 5.5 Hooks

`ask_user` runs through `execTool` like every tool: PreToolUse may deny or rewrite the questions (a denied ask posts nothing — and if nothing else was posted that round, the turn does not end); PostToolUse observes the ack. Stop hooks are not consulted at an ask-ending boundary (§5.1) and are unchanged everywhere else. Stated invariant with a test (the Cursor bug class).

## 6. Renderers

Rendering rides existing machinery end-to-end: `TOOL_CALL_START` carries the questions as `ArgumentsJSON`; the pending/resolved distinction is **transcript shape**, defined precisely (round 4 broke the naive rule): **pending** = a *completed* `ask_user` call — its ack present among the round's aggregated `TurnToolResults` (a round's results collapse into one turn; inspect per-result tool names) — with no later `TurnUserInput`/`userMessage`. Steering, checkpoint, summary, and system turns do **not** resolve it (a task-nudge steering turn can trail the ask inside its own turn: `injectPostToolSteering` runs before `deliverIfCommunicated`, `agent/session_lifecycle.go:886` vs `:902`; steering projects as its own item type, not `userMessage`). An ask whose turn was interrupted before the ack exists is never pending — no ghost cards. Multiple pending cards from one turn aggregate in the dock/board with §4.3's global numbering; once a user turn follows, **all** earlier pending cards resolve and echo that reply. Cold attach and live attach use the same rule; no `ToolState` side channel is needed.

### 6.1 Web (`cmd/serf-hub/assets/`)

Activate the dormant scaffolding and build the designed-but-never-built answering layer:

- **Container:** `markAgentQuestion` (renderer.js:1928, amber "◆ Needs you" frame — built, never called) wraps the question card; `renderNeedsYouDock`/`jumpToAgentQuestion` (renderer.js:4179+) dock and deep-link it. CSS already shipped (`style.css` `.agent-question`, `.needs-you-dock`).
- **Answering (mockup 16 Alt D, `docs/web-ui/mockups/16-blocking-needs-you.html`):** per question — quick-reply chips for options (checkboxes when `multi_select`), a "Something else…" free-text row, a "You decide" row (optional leaning field), a "do that" button when `if_unanswered` is present, a skip affordance, and the `recommended` option first with a subtle tag. Multi-question calls stack as one card, answerable in any order, with an answered-count line and a single **Send answers** action that composes the §4.3 message and submits it as an ordinary `turn/start`. Resolution inputs are mutually exclusive per question: picking free-text, "you decide", "do that", or skip clears checked options (and vice versa), so the composed line always carries exactly one resolution. Per the mockup's written guidance: amber owns the container, blue owns the primary action.
- **Annotation (split control, question-level):** the chip body answers bare — the fast path costs nothing. A low-contrast trailing `+` (or `.` with the chip focused) opens a one-line note field; the note belongs to the **question** and attaches to whichever resolution is chosen — option picks, fallback, skip, "you decide", and free text are all annotatable (the hard requirement). Never a modal.
- **Composer coexistence:** the form never traps or steals input. The composer stays live; typed prose submits as the reply (that *is* the escape hatch — no modes, no indicator needed, because there is no in-flight turn to steer). `Esc` collapses the card to a "◆ question waiting" chip; the dock and status keep it findable. When any reply starts the turn — from this client or another — the card collapses to a neutral settled line echoing the reply. Submit discipline (round 4): before sending, the form re-checks the transcript — if a user turn already follows the ask (a suspended tab coming back hours later), it collapses instead of sending; and a `turn/start` Conflict (another client won the race — the daemon's reservation is atomic, so the loser errors, never queues) is never auto-retried: the composed text drops into the composer for the user to decide.
- **Notification deep link:** clicking the OS notification lands on the session with `jumpToAgentQuestion` focused on the first question.

### 6.2 TUI (`cmd/serf-tui/`)

A question overlay built from existing widgets (`internal/tuipick`), added to `focus_trap.go`'s priority list — and, deliberately, **never auto-opened**. Every existing TUI overlay opens from a keypress; auto-trapping focus when an ask lands would steal keystrokes mid-composition. The ask renders as an in-transcript question card (mirroring the web) plus a persistent composer-chrome chip; the overlay opens on the chip's keybinding:

- The chip `◆ question waiting — ctrl+q to answer` (binding chosen at implementation against the existing keymap; `ctrl+q` is the candidate) shows whenever the session is awaiting; `ctrl+q` opens the overlay, `Esc` closes it back to the chip. Typing in the composer sends an ordinary reply.
- One question at a time, **paged with a header-chip strip** when N>1, plus a **review step** before submit (the convergent Codex/Gemini/OpenCode shape): review lists `header → answer` pairs and warns on unanswered questions ("submit with 1 unanswered → it resolves as skipped").
- Options render via `PickerPanel` with appended "Something else…", "You decide", and (when present) "do that (fallback)" rows (text entry via `TextInputModal`). `.` opens the **question-level note field** — the annotation attaches to whichever resolution is chosen. Footer: `↑/↓ choose · enter answer · . note · tab next question · esc defer`.
- Submit composes the §4.3 message and sends it as an ordinary `turn/start`, following the same discipline as the web (§6.1: re-check the transcript before sending; never auto-retry a Conflict); the card resolves from the transcript shape like any turn.

## 7. Invisibility

Gated at the **registration seam**, on the true root condition. Deliberately NOT via `rootOnlySubagentTools()`: that list's removal is allowance-gated (`agent/session_init.go:613` skips it when `delegation_allowance > 0` — correct for `delegate`/`job_watch`, which allowance legitimately grants), so a coordinator subagent would keep anything on it:

1. `registerAskTool` runs only when `!cfg.NonInteractive && !isSubagentSession`, where `isSubagentSession` is true when `cfg.spawn.parentSessionID != ""` (live spawns and job restores) **or** the restored `meta.IsSubagent` flag is set (`agent/schema/snapshot.go:54-56`). The runtime spawn carrier alone is not enough: `spawn` is `json:"-"` (never persisted) and a bare `serf serve --resume <delegate-id>` restores with an empty carrier (`agent/session_init.go:275-280` overwrites `cfg.spawn` only when the caller passes one) — adversarial review caught that this would have leaked the tool into a resumed subagent. `RestoreSessionFromMetaWithConfig` therefore derives the flag from `meta`, and the exec-time guard (point 4) consults the same predicate. A *forked root* stays a root: fork lineage lives in `meta.ParentSessionID` with `IsSubagent == false`, distinct from `spawn.parentSessionID` — verified. `NonInteractive` (`agent/session_config.go:88`) today drives prompt text and eval-mode task seeding (`agent/session_init.go:167`); this adds its first tool-availability consumer. One-shot `serf <prompt>` hardcodes `NonInteractive: true` (`cmd/serf/run.go:187`), so headless runs never see the tool. `serf serve --non-interactive` likewise.
2. `grant_tools` cannot re-add it — via a **new** check, because no existing one fires: the current validation rejects only root-only-list tools (`ask_user` deliberately isn't on that list, per this section's opening) and consults the *parent's* registry (`agent/subagents.go:483`), where `ask_user` is legitimately registered — so as-is a grant would silently succeed, saved only by the child's registration seam. Implementation adds `ask_user` to a protected-grants set with its own rejection message; tested (§8).
3. Unregistered = invisible **and** unexecutable: `rebuildToolDefsCache` and `Registry.ExecuteCall` read the same registry, so a hallucinated call hits the unknown-tool error.
4. Defense in depth (the Gemini pattern): the handler re-checks at exec time and returns `ask_user unavailable: no interactive user in this session; decide with your best judgment` — covers config drift and future registration refactors.

## 8. Testing

**Deterministic — Session level (agenttest scripted adapter):**

- boundary rule: a round that posts questions ends the turn — the model gets no further round — with session state `SessionAwaiting`; a turn without questions settles per inbox semantics (attention-status-model v5, §11 reconciliation: idle, unless the turn produced output with nothing else in flight, in which case it also rests `awaiting` — the identical wire value, distinguished from an ask only by the pending set).
- early ask: an ask posted in what the model intended as a mid-turn round still ends the turn at that round's boundary.
- multiple asks: two `ask_user` calls in one round accumulate into one pending set; one boundary; the reply's numbering spans both calls in call order.
- ask + communicate: a round with `ask_user` calls and a `communicate` delivers the communicate message **and** rests awaiting, regardless of the communicate's `end_turn` value.
- stop hooks: a Blocked-returning Stop hook does not prevent an ask-ending boundary; the session rests awaiting.
- reply resumes: a subsequent `ProcessInput` with the §4.3 `[answers]` text reaches the model verbatim as the user turn; the pending set clears (`askPendingCount` reaches 0 — the durable proof the ask resolved). The raw state leaves awaiting for the reply's own processing and then follows inbox semantics at that reply turn's own settle — typically back to `awaiting` again, since a reply is itself a clean, output-producing turn; that re-arm is a separate, legitimate event, not the original question resurrecting.
- interrupt after ask: an interrupted turn that posted questions ends idle (user present); the cards remain rendered and answerable.
- input validation: duplicate labels / two `recommended` on one question → instructive error result; nothing is posted; a round whose only ask was denied/invalid does not end the turn.
- entry gate: with a pending ask, `ProcessInputKind(EntryNotification/EntryContinuation/EntryWatchDelivery)` is refused **before any state transition** — state remains `SessionAwaiting`, the notification stays queued, and it drains after the reply. (Gated on the pending set, not raw state: a session generally awaiting with nothing pending lets these wakes through — attention-status-model v5's "async wakes re-arm by design" — only a genuine question is a stronger stop.)
- boundary drain gate: a job notification arriving *during* the asking turn does not drive a turn at the asking turn's boundary; queued user input from mid-turn **does** drain as the reply (documented semantics).
- goal holds, all four paths: a running goal does not kick at the asking boundary; `settleGoalOnIdle` does not fire into awaiting; `SetGoal` while awaiting arms without kicking; a restored active goal does not kick into an awaiting session; awaiting time never trips the no-progress breaker.
- compact/clear: `Compact` while awaiting is refused with the instructive error; `Clear` dismisses the questions and rests idle.
- restore re-derivation: kill/restore with an unanswered ask at the transcript tail (including a trailing steering turn) → session state is `SessionAwaiting`; with the ask answered, or unacked (interrupted) → idle.
- invisibility: `NonInteractive` and subagent sessions exclude `ask_user` from `cachedToolDefs`; forced execution returns the exec-time guard error.
- resume gating: restoring a delegate's session id via bare `serve --resume` (empty spawn carrier, `meta.IsSubagent` set) still excludes `ask_user`.
- grant guard: `grant_tools: ["ask_user"]` on a delegate is rejected with the ask-specific message.
- hooks: PreToolUse deny posts nothing; PostToolUse observes the ack.

**Deterministic — serve level** (the real-daemon harness, `cmd/serf/serve_goal_test.go` precedent — agenttest drives the Session directly and cannot reach these seams):

- `/status` (the endpoint the hub prober consumes — not just the `appStatus` function) reports `awaiting` at rest, `idle` after the reply, and **still `awaiting`** after a job completes while awaiting (the wire must not flicker).
- restore: a restarted daemon's `/status` reports `awaiting` immediately (the serve-level `SetState` after restore + Bridge/projector SessionStart carrying restored state), not idle-until-next-turn.

**Renderer:** JSDOM tests (pending card from `TOOL_CALL_START` + transcript shape; multi-card aggregation with global numbering; chips/annotation/skip/mutual-exclusion; composed `[answers]` message; submit discipline — re-check collapses instead of sending, a Conflict drops the text into the composer, never auto-retried; card collapses when a user turn follows — including one sent by "another client"; cold-attach pending and resolved forms); TUI sample-corpus renders (`tui_samples.go`: in-transcript question card, waiting chip, overlay single + multi + review step — the overlay opens only by keypress, never from a notification) across themes.

**E2E scenario cards** (`test/scenarios/`, house template with falsification lines):

| card | falsification line |
|---|---|
| `ask-web-answer.md` | if the thread never shows `awaiting`, or the form's reply does not reach the model as the next user message, the feature is broken |
| `ask-tui-answer.md` | if the overlay auto-opens, traps `esc`, or the composer cannot submit prose as the reply, the lens rule is broken |
| `ask-cross-session-notify.md` | if a pending ask does not surface the session in the NeedsYou tier and raise the OS notification from a *different* session's viewport (the roster path), the needs-you chain is broken |
| `ask-two-clients.md` | if the second client's form does not collapse once the first client's reply starts the turn, or a losing/stale submit produces a second user message, multi-client handling is broken |
| `ask-restart-rederive.md` | if a restart with an unanswered ask loses the `awaiting` status or the answerable form, restore re-derivation is broken |
| `ask-noninteractive-invisible.md` | if `ask_user` appears in the tool list of a `--non-interactive` or one-shot session, gating is broken |
| `ask-subagent-invisible.md` | if a delegate can call or see `ask_user`, root-only gating is broken |

## 9. Documentation updates (same commits as the code they describe)

- `docs/architecture.md`: add `ask_user` to the tool inventory; extend the turn-end/communicate paragraph in "How a turn flows" with the awaiting boundary state. (No mailbox-section change: this design adds no new delivery path.)
- `docs/job-control.md`: tool-availability matrix gains the `ask_user` row (root: yes; delegate: never); one sentence on the notification hold while awaiting.
- `docs/hooks.md`: name-mapping table gains `ask_user ↔ AskUserQuestion`.
- `docs/web-ui/`: mark mockup 16 Alt A/C/D as shipped; note the notifications.js trigger addition.
- New scenario cards indexed per `test/scenarios/README.md`.

## 10. Fast-follows (each named with its trigger; none block v1)

| follow-up | trigger | path |
|---|---|---|
| **Park / ask-and-continue** | wanting to ask without yielding the floor — the model keeps working and answers land mid-turn (Cursor 2.4's pattern) | the **deferred-tool-result shape**: an ask leaves a call unresolved and an answer RPC resumes with a synthesized tool result — the machinery the blocking spec designed (git history) is the upgrade path |
| withdraw/supersede stale questions | park exists | — |
| stakes field + reversibility-proportional friction | destructive-action asks appear | schema is additive |
| evidence refs on options (file/diff/job) | options citing artifacts | `detail` is prose today; typed refs additive |
| hub sidebar inline answering + next-needs-you loop | multi-session answering pain | NeedsYou tier already aggregates; an inline reply is just `turn/start` |
| decision-record query layer | cross-session re-asking observed | Q&A is already durable + greppable in the transcript (ask call + reply turn) |
| images attached to answers | first real need (Cline/Roo precedent) | `turn/start` already carries images |
| distinct "question" notification sound (OpenCode precedent) | web notification channels get sounds | trigger table already keyed by transition |
| unattended-grace auto-resolve | real demand only; **user/project config, dark by default, never model-settable** | `if_unanswered` is already the disclosed fallback it would use |
| secret-masked answers | probably never — MCP URL-mode elicitation is the right path for credentials | — |

## 11. Coordination with the attention & status model (lands first)

The attention/status-model workstream (`docs/superpowers/specs/2026-07-03-attention-status-model-design.md` v5, on `attention-status-model-spec`) merges to main **before** this branch. The designs compose — theirs upgrades `idle → awaiting` for every normally-completed turn at the drain settle; ours sets `awaiting` at the boundary for asking rounds, so their upgrade no-ops on ask turns — but the rebase must reconcile these, in order of importance:

1. **Precedence baked upstream, not a rebase contingency.** Their delegating-parent wire projection reports `active` while live children or undelivered job notifications exist. A parent with a **pending ask** holds its wakes (§5.3), so a projection that outranked awaiting would read blue forever — the flip condition ("wake-turn completes") could never fire, since that very wake-turn is refused until the answer arrives. This is not a contingency this branch had to add: the shipped `WireState()` guard was **idle-only from the start** (it upgrades `SessionIdle` only, never `SessionAwaiting`), so a pending ask was never at risk of being masked — awaiting already outranked the projection by construction. Their spec's v5 made that precedence normative (a colleague flagged, post-merge, that the guard's comment claimed awaiting and autonomy could never coexist — an invitation for a future widening to recreate exactly this deadlock; the comment was corrected to state the precedence explicitly, not the behavior). `TestWireState_AwaitingOutranksAutonomy` (main `27cc21b91`, mutation-checked: fails under a widened guard) pins it, and it passes unchanged on the merged tree.
2. **`SessionAwaiting` constant:** both add it (same name, string `"awaiting"`). Whoever lands second dedupes the constant and its string-pin test.
3. **`session_lifecycle.go` conflicts:** their settle-upgrade lives in the same drain-loop region as our entry/drain gates and goal guards. Mechanical merge; re-run the full ask test suite after.
4. **notifications.js:** they delete the 5s poll (broadcast-driven). On rebase, drop our `active→awaiting` trigger line + its JSDOM test; verify their transition-into-`needs_you` broadcast fires for ask-produced awaiting (it should — it keys on the transition, not the producer) and adapt the test to that path.
5. **Resume derivation:** unify into one function — their "agent moved last → awaiting" is the general rule; fold in our `!IsError` refinement so an orphan-repaired crash tail doesn't read as amber (or accept their looser inbox semantics deliberately; decide at rebase).
6. **Their `errored` lane helps us:** `systemError` stops normalizing into `"awaiting"`, so errors no longer masquerade as questions in the NeedsYou tier. No action, just don't fight it.
