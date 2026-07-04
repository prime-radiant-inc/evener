# `ask_user`: interactive questions from the agent — design

- **Date:** 2026-07-03
- **Status:** approved design, reshaped per Jesse's simplification directive; pending Jesse's spec review
- **Scope decision (Jesse):** blocking interactive questions only. No parking in v1 — "we can always add parking later."
- **Policy decision (Jesse):** never auto-proceed. No timers, no synthetic answers, no AFK fallback. An unanswered question waits, visibly, until answered.
- **Shape decision (Jesse):** **communicate-twin.** `ask_user` posts structured questions and **ends the turn** exactly like `communicate(end_turn=true)`, leaving the session at rest in state `awaiting`. The answer arrives as the **next user message** (the form composes it; typed prose works identically). No mid-turn blocking, no new wire methods. An earlier blocking-in-handler design was fully specified and three-/par-rounds hardened (git history `771aeeb42..fc559fb83`); its complexity was the cost of the shape, and the shape was wrong. Findings that survive the reshape are folded below.
- **Code anchors** cite the worktree at `ecdbd59bb`; line numbers may drift.

## 1. Problem

Serf's agent has no structured way to ask its user a question. Today the model asks in prose and ends its turn; the thread state is plain `idle`, so nothing distinguishes "done" from "waiting on you." The 2026-07-03 UX diagnostic found this **awaiting-state gap** is the root of the notification complaints: the NeedsYou sidebar tier, amber dot, tab badge, and OS notifications all exist and all starve, because nothing ever produces the `awaiting` status (`appwire/types.go` `ThreadStatusAwaiting` — fully plumbed, zero producers).

Meanwhile a dedicated model-invoked question tool became table stakes across the ecosystem in 2025–26 (Codex `request_user_input`, Gemini CLI `ask_user`, OpenCode `question`, Cline/Roo `ask_followup_question`, Cursor `AskQuestion`, Devin `ask_user_question`), and the HCI evidence says agents under-ask: misread intent shows up in ~27% of real coding-agent sessions, and unprompted guessing is the dominant failure mode, not over-asking.

`ask_user` gives the model a structured question form and ends the turn in `awaiting` — the first real producer of the status the entire dormant needs-you chain keys on. The dormant plumbing assumed exactly this shape: a session at rest between turns, waiting on its user.

## 2. Goals and non-goals

**Goals**

1. The root agent asks 1–4 structured, mostly multiple-choice questions in one call; the call ends its turn; the session rests in `awaiting` until the user replies.
2. The user answers through a rendered form (chips, free text, "you decide", skip) or by simply typing — either way the answer is the next user message, and an annotation is always possible on any choice.
3. `awaiting` at rest flows through the existing turn-boundary state write to `/status` → hub prober → roster → NeedsYou tier, badges, OS notification, deep link. One trigger-table line changes; everything else is already built.
4. Renderers on both surfaces: web (activating the dormant scaffolding) and TUI (new overlay on existing widgets, opened by keypress only).
5. Invisible — not merely disabled — in non-interactive sessions and in all subagents, including bare-resumed ones.
6. Ordinary tool-pipeline citizenship: hooks fire, the transcript records the call and its ack, no special-cased plumbing.

**Non-goals for v1** (§10 names each as a fast-follow with its trigger): parking / ask-and-continue (a parked ask cannot end the turn — it needs the deferred-result shape; that is the upgrade path); a durable ask store; withdraw/supersede; timers or auto-answers of any kind; evidence refs beyond prose; stakes-graded rendering; hub sidebar inline answering; wizard chains; images on answers; secret-masked answers.

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
- One `ask_user` call per turn — it ends the turn, so "stacking questions in a single turn" means one call carrying up to 4 questions.

### 4.3 How answers come back: the reply-message contract

The user's **next message is the answer**. The form composes it in a stable format (every echoed model- or user-authored string renders Go-`%q`-quoted, so embedded quotes, commas, and newlines cannot corrupt the framing):

```
[answers]
1. [DB choice] → "Postgres" — note: "only the primary"
2. [Naming] → you decide — leaning: "short names" — note: "re-ask if it gets weird"
3. [CI matrix] → skipped (no answer) — note: "irrelevant after #2"
4. [Endpoint] → free text: "use RDS, not self-hosted"
```

Per question, exactly one resolution: a quoted selection (multi-select joins **quoted** labels: `→ "A", "B"` — unambiguous even when a label contains a comma), `free text: "…"`, `you decide` (optional `leaning`, then `note`, both quoted), `do your stated fallback ("…")` (only where `if_unanswered` exists), or `skipped (no answer)`. **Every** resolution line accepts the trailing `— note: "…"` suffix — the annotation is universal (Jesse's hard requirement), and renderers must offer the note affordance on every resolution path, question-level, not chip-only.

The user may instead ignore the form and type anything: free prose **is** a valid reply, delivered verbatim as the user message. There is no daemon-side answer validation and no invalid-answer state — a reply is a reply; the form's structure lives in the client.

### 4.4 Tool description (draft; Haiku-validate at implementation, house style)

> Ask the user structured questions. This ends your turn: the session waits, visibly, until the user replies — there is no timeout, and no other work happens in between. The reply arrives as the user's next message: either the numbered `[answers]` form (one resolution per question: a selection, free text, "you decide" — choose with your judgment, honoring any stated leaning —, your stated fallback, or skipped — proceed on your best judgment, state the assumption, and do not immediately re-ask) or free prose; treat either as the answer. Any answer may carry a user note — read it; it can qualify or override the selection.
>
> - `questions`: 1–4 per call, each with a short `header` (≤12 chars), the full `question`, and 2–5 `options` (`{label, detail}`, labels unique). Set `multi_select` to allow several; set `recommended: true` on at most one option and put it first.
> - Do not add an "Other" or free-text option; the UI always offers one, plus "you decide".
> - Optional per question: `why` (one line: what the answer changes) and `if_unanswered` (the fallback you would take; the user can accept it with one tap).
>
> First try to resolve the question yourself with tools. Batch related questions at a natural breakpoint into one call — this is your one ask for the turn.

### 4.5 System-prompt guidance

New prompt section (`agent/prompts/sections/ask-user.md.tmpl`), included only when the tool is registered (interactive root sessions), in the terse imperative of the job-control section:

> **Asking the user.** Ask when being right matters more than the interruption costs — not whenever you are unsure. First resolve what evidence can settle: read the file, run the test. Ask only what evidence cannot settle. Batch questions that share a breakpoint into one `ask_user` call (≤4); the call ends your turn and the reply arrives as the user's next message. Write honest options — no straw men — and state `why` and `if_unanswered` when they help the user decide fast. The user's `note` on any answer can qualify or override the selection; honor it.

The existing non-interactive section (`agent/prompts/sections/non-interactive.md.tmpl`) already covers the inverse case and does not change.

## 5. Runtime semantics

### 5.1 The call ends the turn

The handler registers like any core tool (`registerCoreTools` → new `registerAskTool`, `agent/session_tool_registry.go`). It validates its input (label uniqueness, one `recommended` per question — schema constraints like counts and lengths are already enforced by the registry's JSON-Schema validation), records the pending questions on the session, returns a short ack as its tool result ("questions posted; awaiting the user's reply"), and signals turn-end through a sibling of the `communicate` result seam (`deliverIfCommunicated` → `finishProcessingAtBoundary`, `agent/session_tools_communicate.go`, `agent/session_state.go`) — with one difference: the boundary state is **`SessionAwaiting`**, a new session state alongside idle/processing/closed, instead of idle.

Because the call completes normally (ack recorded), there is **no held-open tool call**: nothing for orphan repair to eat, no cancellation race, no lock protocol, no special interrupt handling. Batching semantics are inherited from `communicate` (the turn ends at the round boundary after the batch); the tool description still tells the model this is its one ask for the turn.

### 5.2 The answer is the next user message

The user answers through the existing input path — `turn/start` — carrying either the form-composed `[answers]` text (§4.3) or whatever they typed. `acceptUserInput` runs as for any turn; the session leaves `awaiting` for `processing`; the model reads the reply. **No new wire methods, no new capabilities, no daemon-side answer validation, no first-answer-wins machinery**: if two clients both answer, the first `turn/start` wins by existing semantics and the second client sees the turn begin (its form collapses, §6). A "stale" answer cannot exist — a message to a session is always a valid message.

### 5.3 While awaiting

The session is at rest, with two holds so nothing answers the question on the user's behalf:

- **Goal engine:** a session in `SessionAwaiting` is waiting on its user — the goal continuation gate does not kick a continuation turn, and the wait does not count against the no-progress breaker. (Without this, a running `/goal` would immediately drive a turn past the unanswered question.)
- **Job notifications:** the notify path does not drive a model turn while awaiting (`acceptNotificationInput`'s wake is gated on state); pending notifications queue durably as today and drain at the boundary after the user's reply, per existing drain rules.

Steering an awaiting session fails fast exactly as steering any idle session does today (`tui-steer-in-idle-fails-fast` is an existing scenario card); the composer needs no answer-vs-steer mode — typing **is** answering. Interrupt is a no-op (nothing is running).

### 5.4 The `awaiting` status, at rest

`SessionAwaiting` rides the **existing** turn-boundary state write (the same path that sets idle at turn end, `cmd/serf/serve.go` turn-end `SetState`), so it is at rest in the daemon's state string — which is exactly what every consumer already reads:

- `/status` returns it as-is → the hub prober (`hubcore/prober.go`) → roster (`roster.go:174`) → `/api/search`, the appwire tree, the sidebar NeedsYou tier + `◆N` badge + attention pill (`hubcore/tree.go` `NormalizeState`/`AttentionRank` — built, dormant), and the TUI dashboard (`hub_dashboard_view.go:308` already renders `awaiting`).
- The appwire snapshot's dead pass-through (`server/appwire_runtime.go` `appStatus` `case ThreadStatusAwaiting`) comes alive with no reordering: the session is not `processing` at rest, so the early-return that doomed the mid-turn design never engages.
- Live push: the turn-end status change already notifies subscribed clients through the existing projector path; no new event kinds.
- One trigger-table line ships with this feature: `assets/notifications.js` fires on `idle→awaiting` today; add `active→awaiting` (the turn-end transition). Consecutive asks are human-paced (each requires an intervening reply), so poll-window coalescing is not a concern.
- **Restore:** a restarted daemon re-derives `SessionAwaiting` at restore time with one check — the transcript's last turn ended via `ask_user` with no subsequent user turn. If the check is wrong in either direction nothing breaks (the form renders from the transcript and answering is always-valid `turn/start`); the check only keeps the needs-you chain lit across restarts.

Capabilities while awaiting are exactly the at-rest set: `Send` true (the session expects input), `Queue` false (`processing`-gated), and steering fails fast as on any idle session regardless of what the func-wired `Steer` capability advertises (existing behavior, existing scenario card). No capability fields are added.

### 5.5 Hooks

`ask_user` runs through `execTool` like every tool: PreToolUse may deny or rewrite the questions (a denied ask does not end the turn); PostToolUse observes the ack. Stated invariant with a test (the Cursor bug class).

## 6. Renderers

Rendering rides existing machinery end-to-end: `TOOL_CALL_START` carries the questions as `ArgumentsJSON`; the pending/resolved distinction is **transcript shape** — an `ask_user` turn with no subsequent user turn is pending; once a user turn follows, the card is resolved and echoes that reply. Cold attach and live attach use the same rule; no `ToolState` side channel is needed.

### 6.1 Web (`cmd/serf-hub/assets/`)

Activate the dormant scaffolding and build the designed-but-never-built answering layer:

- **Container:** `markAgentQuestion` (renderer.js:1928, amber "◆ Needs you" frame — built, never called) wraps the question card; `renderNeedsYouDock`/`jumpToAgentQuestion` (renderer.js:4179+) dock and deep-link it. CSS already shipped (`style.css` `.agent-question`, `.needs-you-dock`).
- **Answering (mockup 16 Alt D, `docs/web-ui/mockups/16-blocking-needs-you.html`):** per question — quick-reply chips for options (checkboxes when `multi_select`), a "Something else…" free-text row, a "You decide" row (optional leaning field), a "do that" button when `if_unanswered` is present, a skip affordance, and the `recommended` option first with a subtle tag. Multi-question calls stack as one card, answerable in any order, with an answered-count line and a single **Send answers** action that composes the §4.3 message and submits it as an ordinary `turn/start`. Resolution inputs are mutually exclusive per question: picking free-text, "you decide", "do that", or skip clears checked options (and vice versa), so the composed line always carries exactly one resolution. Per the mockup's written guidance: amber owns the container, blue owns the primary action.
- **Annotation (split control, question-level):** the chip body answers bare — the fast path costs nothing. A low-contrast trailing `+` (or `.` with the chip focused) opens a one-line note field; the note belongs to the **question** and attaches to whichever resolution is chosen — option picks, fallback, skip, "you decide", and free text are all annotatable (the hard requirement). Never a modal.
- **Composer coexistence:** the form never traps or steals input. The composer stays live; typed prose submits as the reply (that *is* the escape hatch — no modes, no indicator needed, because there is no in-flight turn to steer). `Esc` collapses the card to a "◆ question waiting" chip; the dock and status keep it findable. When any reply starts the turn — from this client or another — the card collapses to a neutral settled line echoing the reply.
- **Notification deep link:** clicking the OS notification lands on the session with `jumpToAgentQuestion` focused on the first question.

### 6.2 TUI (`cmd/serf-tui/`)

A question overlay built from existing widgets (`internal/tuipick`), added to `focus_trap.go`'s priority list — and, deliberately, **never auto-opened**. Every existing TUI overlay opens from a keypress; auto-trapping focus when an ask lands would steal keystrokes mid-composition. The ask renders as an in-transcript question card (mirroring the web) plus a persistent composer-chrome chip; the overlay opens on the chip's keybinding:

- The chip `◆ question waiting — ctrl+q to answer` (binding chosen at implementation against the existing keymap; `ctrl+q` is the candidate) shows whenever the session is awaiting; `ctrl+q` opens the overlay, `Esc` closes it back to the chip. Typing in the composer sends an ordinary reply.
- One question at a time, **paged with a header-chip strip** when N>1, plus a **review step** before submit (the convergent Codex/Gemini/OpenCode shape): review lists `header → answer` pairs and warns on unanswered questions ("submit with 1 unanswered → it resolves as skipped").
- Options render via `PickerPanel` with appended "Something else…", "You decide", and (when present) "do that (fallback)" rows (text entry via `TextInputModal`). `.` opens the **question-level note field** — the annotation attaches to whichever resolution is chosen. Footer: `↑/↓ choose · enter answer · . note · tab next question · esc defer`.
- Submit composes the §4.3 message and sends it as an ordinary `turn/start`; the card resolves from the transcript shape like any turn.

## 7. Invisibility

Gated at the **registration seam**, on the true root condition. Deliberately NOT via `rootOnlySubagentTools()`: that list's removal is allowance-gated (`agent/session_init.go:613` skips it when `delegation_allowance > 0` — correct for `delegate`/`job_watch`, which allowance legitimately grants), so a coordinator subagent would keep anything on it:

1. `registerAskTool` runs only when `!cfg.NonInteractive && !isSubagentSession`, where `isSubagentSession` is true when `cfg.spawn.parentSessionID != ""` (live spawns and job restores) **or** the restored `meta.IsSubagent` flag is set (`agent/schema/snapshot.go:54-56`). The runtime spawn carrier alone is not enough: `spawn` is `json:"-"` (never persisted) and a bare `serf serve --resume <delegate-id>` restores with an empty carrier (`agent/session_init.go:275-280` overwrites `cfg.spawn` only when the caller passes one) — adversarial review caught that this would have leaked the tool into a resumed subagent. `RestoreSessionFromMetaWithConfig` therefore derives the flag from `meta`, and the exec-time guard (point 4) consults the same predicate. A *forked root* stays a root: fork lineage lives in `meta.ParentSessionID` with `IsSubagent == false`, distinct from `spawn.parentSessionID` — verified. `NonInteractive` (`agent/session_config.go:88`) today drives prompt text and eval-mode task seeding (`agent/session_init.go:167`); this adds its first tool-availability consumer. One-shot `serf <prompt>` hardcodes `NonInteractive: true` (`cmd/serf/run.go:187`), so headless runs never see the tool. `serf serve --non-interactive` likewise.
2. `grant_tools` cannot re-add it: grants validate against the parent's registry and the protected set (`agent/subagents.go:483`); the rejection message gains an `ask_user`-appropriate variant (the existing text is delegation-specific).
3. Unregistered = invisible **and** unexecutable: `rebuildToolDefsCache` and `Registry.ExecuteCall` read the same registry, so a hallucinated call hits the unknown-tool error.
4. Defense in depth (the Gemini pattern): the handler re-checks at exec time and returns `ask_user unavailable: no interactive user in this session; decide with your best judgment` — covers config drift and future registration refactors.

## 8. Testing

**Deterministic (agenttest scripted adapter):**

- ask ends turn: scripted model calls `ask_user`; the ack is its tool result; the turn ends with session state `SessionAwaiting`; `ProcessInput` returns.
- reply resumes: a subsequent `ProcessInput` with the §4.3 `[answers]` text reaches the model verbatim as the user turn; state leaves awaiting.
- input validation: duplicate labels / two `recommended` on one question → instructive error result; the turn does **not** end; the session is not awaiting.
- goal hold: with a goal running, an awaiting session receives no continuation kick, and awaiting time does not trip the no-progress breaker.
- notification hold: a job finishing while awaiting queues its notification; no model turn is driven; the notification drains after the user's reply per existing rules.
- restore re-derivation: kill/restore with an unanswered ask at the transcript tail → session state is `SessionAwaiting`; with the ask answered → idle.
- invisibility: `NonInteractive` and subagent sessions exclude `ask_user` from `cachedToolDefs`; forced execution returns the exec-time guard error.
- resume gating: restoring a delegate's session id via bare `serve --resume` (empty spawn carrier, `meta.IsSubagent` set) still excludes `ask_user`.
- hooks: PreToolUse deny blocks the ask and the turn does not end; PostToolUse observes the ack.
- status: `/status` (the endpoint the hub prober consumes — not just the `appStatus` function) reports `awaiting` at rest and idle after the reply.

**Renderer:** JSDOM tests (pending card from `TOOL_CALL_START` + transcript shape; chips/annotation/skip/mutual-exclusion; composed `[answers]` message; card collapses when a user turn follows — including one sent by "another client"; cold-attach pending and resolved forms); TUI sample-corpus renders (`tui_samples.go`: in-transcript question card, waiting chip, overlay single + multi + review step — the overlay opens only by keypress, never from a notification) across themes.

**E2E scenario cards** (`test/scenarios/`, house template with falsification lines):

| card | falsification line |
|---|---|
| `ask-web-answer.md` | if the thread never shows `awaiting`, or the form's reply does not reach the model as the next user message, the feature is broken |
| `ask-tui-answer.md` | if the overlay auto-opens, traps `esc`, or the composer cannot submit prose as the reply, the lens rule is broken |
| `ask-cross-session-notify.md` | if a pending ask does not surface the session in the NeedsYou tier and raise the OS notification from a *different* session's viewport (the roster path), the needs-you chain is broken |
| `ask-two-clients.md` | if the second client's form does not collapse once the first client's reply starts the turn, multi-client handling is broken |
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
| **Park / ask-and-continue** | recurring want of asking without ending the turn (Cursor 2.4 proved the value) | the **deferred-tool-result shape**: the ask leaves its call unresolved, the turn continues or suspends, and an answer RPC resumes with a synthesized tool result — the machinery the blocking spec designed (git history) becomes the upgrade path |
| withdraw/supersede stale questions | park exists | — |
| stakes field + reversibility-proportional friction | destructive-action asks appear | schema is additive |
| evidence refs on options (file/diff/job) | options citing artifacts | `detail` is prose today; typed refs additive |
| hub sidebar inline answering + next-needs-you loop | multi-session answering pain | NeedsYou tier already aggregates; an inline reply is just `turn/start` |
| decision-record query layer | cross-session re-asking observed | Q&A is already durable + greppable in the transcript (ask call + reply turn) |
| images attached to answers | first real need (Cline/Roo precedent) | `turn/start` already carries images |
| distinct "question" notification sound (OpenCode precedent) | web notification channels get sounds | trigger table already keyed by transition |
| unattended-grace auto-resolve | real demand only; **user/project config, dark by default, never model-settable** | `if_unanswered` is already the disclosed fallback it would use |
| secret-masked answers | probably never — MCP URL-mode elicitation is the right path for credentials | — |

## 11. Out-of-repo coordination

A parallel session owns the broader web-UI/UX redesign (2026-07-03 diagnostic). This feature produces the `awaiting` status that work identified as the #1 gap; the shared file surfaces are `renderer.js`/`notifications.js`/`style.css` activation points. Coordinate before merging if both land in the same window.
