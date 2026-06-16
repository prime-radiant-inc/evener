# Serf Web Hub — UX Plan & Implementation Status

Status: **in progress** (2026-06-16). Companion to [design-system.md](design-system.md).
Implementation lives on branch `agent-liveness-and-thinking`.

---

## 1. The problems (from live recon + the product owner)

Driving a real gpt-5.5 (serf-harness) session through the hub surfaced these, which the redesign
targets. Top priorities first.

1. **Subagent visibility.** A spawned subagent rendered as one line that *stayed "● running"
   after it finished*. No completion state, duration, result preview, or link to its transcript.
   Root cause: the job-ref dot only flips on a `JOB_FINISHED` event that often never arrives.
2. **Sidebar clutter.** Real projects buried under ~25 identical `serf-e2e-*` test folders; all
   ALL-CAPS low-contrast monospace; live and ancient given equal weight.
3. **Scroll-jump.** The transcript yanks to the bottom while you read up, because auto-scroll is
   an unconditional `scrollTop = scrollHeight` on every append (`renderer.js`).
4. **Stalled-vs-working ambiguity.** The "working" dot is pure CSS on a timer — a hung agent
   looks identical to a busy one. (Confirmed: the previously-templated `RunningFor` was also
   dead for live sessions because `Turn.StartedAt` was never set.)
5. **Transcript scannability.** Tool-call rows led with the command and buried the *purpose*;
   identical `· 1ms` on every row; system/skill churn given divider-weight; thinking discarded.

The fixes are the [design-system](design-system.md) component grammar + the work below.

---

## 2. Design approach (decided)

- **Vertical slice → golden example → generalize.** We mocked the live-session workspace,
  iterated it to a restrained, content-led [golden example](examples/01-golden-live-session.html),
  proved it against [hard cases](examples/02-hard-cases.html), and are extracting the
  [style guide](design-system.md) + reusable component exemplars from it.
- **3 directions explored** ([refined-terminal](examples/direction-a-refined-terminal.html),
  [calm-product](examples/direction-b-calm-product.html),
  [bold-spine](examples/direction-c-bold-spine.html)); converged on a restrained synthesis (the
  golden example) after feedback: kill motion, stop double-containing, demote the user's own
  messages, a strict 3–4 color semantic system, controls consolidated at the bottom.

---

## 3. Subagent surfacing

- One inline **"Subagents (N)" module** aggregating the turn's fan-out: each row shows
  spawned→running→done/failed with last-action, duration, result preview, and an always-visible
  (dim) `view →` link into the subagent's transcript.
- **Fix the stale "running":** reconcile completion from a successful `job_read_output` for that
  job id, not only `JOB_FINISHED`.
- Under load (heavy fan-out): keep the columnar grid in overflow ("+N more · expand", not a
  run-on line); surface a child failure at the module level (red), don't average it into a tally.
- Sidebar mirrors this: subagents de-weighted under their parent with a terminal-state glyph; the
  rollup must be able to surface a red (failed) child, not hide it behind "N✓".

## 4. Scroll behavior

Stick-to-bottom **only when already at bottom** (measure `scrollHeight - scrollTop - clientHeight
< ~50px` *before* the DOM mutation; scroll after only if it was true). Otherwise show a floating
"↓ N new" pill (attention-aware: "↓ needs you" when the new content needs the user). Local echo
of the user's own send stays unconditional. Compensate scroll anchor on replace/prepend.

## 5. Liveness (honest working-vs-stalled) {#liveness}

**Key finding — we do NOT need to invent a protocol.** The Codex wire protocol (vendored at
`inspo/codex/codex-rs/protocol/src/protocol.rs`; docs at developers.openai.com/codex/app-server)
already defines everything, and Serf's appwire layer is modeled on its lifecycle shape:

- **Reasoning/thinking:** `ReasoningContentDelta` (summary) / `ReasoningRawContentDelta` (raw) /
  `AgentReasoningSectionBreak` → app-server wire `item/reasoning/summaryTextDelta` /
  `textDelta` / `summaryPartAdded`. Hosted models (gpt-5.5) emit the **summary** stream.
- **Token usage while streaming:** `TokenCount` (incl. `reasoning_output_tokens`) →
  `thread/tokenUsage/updated`. No Codex heartbeat exists (confirmed first- and third-party).
- **Turn timing:** `TurnStarted.started_at`, `TurnComplete.duration_ms` already exist.

The reason client-side stall detection looked unreliable was the **silent think phase** — but
reasoning *is* frames; Serf was just discarding them. **Forwarding reasoning (which we need for
live thinking anyway) makes a client-side "time since last frame" timer honest.** So:

1. Forward reasoning summary deltas as appwire `item/reasoning/summaryTextDelta` → **live
   thinking**.
2. Client tracks last-frame time → honest **"still working · no updates for Ns"** (never a
   reassuring animation during a hang). Not claiming "stalled" without evidence.
3. `Turn.StartedAt` populated → honest turn-elapsed.

Residual gap: a genuinely silent long tool (no stdout, no tokens). Narrow; revisit later
(optionally forward `TokenCount`) rather than building a heartbeat now.

---

## 6. Implementation status (branch `agent-liveness-and-thinking`)

Each commit passed the pre-commit gate (lint/build/test); TDD throughout.

| Commit | Increment |
|---|---|
| `c3b99ad7` | `Turn.StartedAt` in the appwire projector (`startedTurn` helper) → honest turn-elapsed |
| `db9174a0` | Reasoning → appwire `item/reasoning/summaryTextDelta`; reasoning is a first-class in-progress item (`EventReasoningSummaryDelta`, `ReasoningSummaryDeltaData`, `ReasoningSummaryDeltaParams`) |
| `cc34e524` | Serf harness **emits** reasoning on `llm.StreamEventReasoningDelta` (was fed only to the accumulator and discarded) |
| `09f3d552` | Route renderer jstests through a shared `jstest/load-renderer.js` bundle loader (centralizes script load order) |
| `f8e572f4` | Split `renderer.js`: extract `renderer-format.js` (stateless helpers) |
| `1ceed61c` | Split `renderer.js`: extract `renderer-tools.js` (tool-output renderers; `toolRendererFor` public) |
| `4b9561b1` | Split `renderer.js`: extract `renderer-panels.js` (tasks/details panels + chrome) |
| `c6f7eb19` | appwire maps reasoning notifications → `REASONING_START`/`REASONING_DELTA` client events |
| `278f7cc1` | Render live thinking as a quiet collapsible `.think` block (renderer + `style.css`) |

**Serf-harness backend for live thinking is complete**, and the renderer is now modular
(`renderer.js` 3630 → ~2170 lines). The no-bundler modules share `window.SerfRendererInternal`
and load in dependency order (`renderer-format` → `renderer-tools` → `renderer-panels` →
`renderer`) in both `templates/app.html` and `jstest/load-renderer.js`. `renderer.js` retains the
stateful `SerfRenderer` core + bootstrap (this is where the thinking block lands). Remaining:

4. **Codex-adapter parity:** `cmd/serf-hub/internal/appsource/codex_source.go` `mapNotification`
   (~L703) forward `item/reasoning/summaryTextDelta` (+ `summaryPartAdded`, `item/started` type
   `reasoning`); `codex_mapping.go` `mapCodexTurn` set `StartedAt` from Codex `started_at`.
5. ~~**Web render (the visible payoff):**~~ **DONE** (`c6f7eb19`, `278f7cc1`). `appwire.js`
   `eventsFromNotification` maps `item/started` (type `reasoning`) → `REASONING_START` and
   `item/reasoning/summaryTextDelta` → `REASONING_DELTA` (mirrors the agentMessage path,
   `markLiveItem` for liveness). `renderer.js` `handle()` streams these into a quiet collapsible
   `.think` block (open while live; collapses to "Thought for Ns" + preview on
   `ASSISTANT_TEXT_START` / `TURN_COMPLETED`; empty thoughts removed). Styles in `style.css`
   (`.think`/`.think-body`/`.pv`). Tests: `test-appwire-lifecycle-notifications.js`,
   `test-renderer-thinking.js`. Visually smoke-tested with the real assets.
6. **Client liveness timer:** `renderer.js` track `lastFrameAt` on every notification; honest
   "no updates for Ns" past ~20–30s while active. (Reasoning frames now flow, so the timer is
   honest during the think phase.)

Tests: JS harness `cmd/serf-hub/jstest/` (`run-all.sh`; mirror
`test-appwire-lifecycle-notifications.js` / `test-renderer.js`). Web changes need a rebuild —
assets are embedded: `make build-hub` + restart.

**Adding a renderer module:** define it as an IIFE that imports what it needs from
`window.SerfRendererInternal` (destructured at the top, so call sites stay unchanged) and
publishes its exports back onto the same object via `Object.assign`. Add it to the `<script>` list
in `templates/app.html` *and* to `RENDERER_FILES` in `jstest/load-renderer.js`, both in dependency
order before `renderer.js`. The const-import + same-name-function collision means an incomplete
extraction fails loudly as a `SyntaxError`, so `node --check` catches it before the gate.

---

## 7. Edge cases the design must still define

See [design-system.md §7](design-system.md#7-what-still-needs-rules-deferred-from-the-review-panels).
Each needs a written rule + an exemplar in the golden set before the style guide is "done."

---

## 8. Aside (non-UI, worth a separate look)

Serf gpt-5.5 sessions receive the ~6 KB superpowers "You have superpowers" preamble injected as a
`STEERING` message at session start, causing the agent to burn a turn loading skills and merely
*planning* before doing the actual work. Out of scope for the UI overhaul, but it degrades every
session and is worth fixing independently.
