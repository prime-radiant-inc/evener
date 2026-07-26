# System steering voice — design

Status: **implemented**. Branch `kh-steering-voice`,
worktree `.claude/worktrees/kh-steering-voice`.

## Problem

The transcript has three voices — the human, the agent, and the system steering the
agent — and marks two of them. A daemon-originated steer renders as
`SteeringItem.tsx`'s `SteeringDivider`: a bare `<summary>` carrying a classified
label and nothing else.

`steeringitem.module.css`'s `.summary` declares `cursor`, `--font-sans`,
`--font-size-caption`, `--ink-low`. So do `systemnoticeitem.module.css`'s
`.summary`, `.scaffoldSummary`, and `.timingsSummary`. Four identical declaration
sets, four different meanings. "Tasks done" reads exactly like "Model switched to
opus-5", though one changed the agent's instructions and the other reports a fact.

`docs/web-ui/design-system.md` has no section on any of this, which is why the
four stylesheets drifted into the same place independently.

The label is also a guess. `steeringClassify.ts` pattern-matches prose to infer a
kind, and `SteeringInjectedData` (`agent/events/payloads.go:199`) carries no kind
for it to read. Two consequences, both live today:

- The classifier knows 8 patterns. There are 18 steering injection sites — and
  three of those are invisible to a `grep '\.Steer('`, reaching the queue through
  the `toolDeps.steer` indirection instead. Everything the classifier misses
  collapses into `kind: "unknown"`, labelled "steering injected".
- Its `read-only` rule matches `/reading without writing|reading for \d+ turns/`.
  **That string does not exist in the Go source.** It is inherited from the
  legacy `renderer-format.js` port and classifies a message the daemon never
  sends. Nothing failed when it went stale.

## Decisions (made in visual brainstorming, in Jesse's words where short)

- Scope is the **system voice family**, not steering alone — steering is the
  directive member of a family that already includes lifecycle facts, failures
  and scaffolding.
- The reader's job is **auditing serf itself**: which kind fired, and when. The
  kind is the payload; the family mark is secondary.
- Mark: **gutter glyph ◇**, text `System steered: $kind`, **disclosure chevron
  after** the text.
- The glyph was first chosen as **※ (U+203B, the reference mark)**, picked from
  candidates rendered as text characters at reading size. Once built and rendered
  at its actual 10px ship size it was replaced, on Jesse's approval, with a hollow
  diamond: a faithful reference mark collapses at that size into a small ✕, and
  `FailureGlyph` is ✗ in the same gutter column. Two marks of the same shape,
  separated only by hue, fails both the design system's own rule that colour
  carries attention rather than identity, and any reader who cannot see the hue.
  Shape distinctness beat semantic fit — the row says "System steered" in words,
  so the glyph only has to mark the family.
- The whole line is **one colour**. Jesse specified option B's eyebrow colour
  (`--ink-low`); on being shown that `--ink-low` measures 2.97:1 dark / 3.64:1
  light — under the 4.5:1 AA floor, as this repo already records in
  `usermessageitem.module.css` and `toolcallitem.module.css` — he selected
  `--ink-mid` (6.86:1 / 6.56:1).
- The kind comes from **a wire field**, not the classifier.
- Pre-`Kind` transcripts **claim nothing**: no wire kind renders
  `◇ System steered ▸` with no kind. The prose classifier is deleted rather than
  kept as a fallback. No backward compatibility.
- "Steering injected" → **"Message sent"**, scoped to `agent-message`: a parent
  steering a running child (`subagents.go:885`), and a `delegate_send` reaching a
  caller or a running delegate (`job_delegate.go`) — every site an agent-to-agent
  message is the one case where those words are true.

## 1. The grammar (new design-system.md §7)

**A glyph in the gutter means the agent's instructions changed. An empty gutter
means it is a passive fact.**

The transcript already has a 10px glyph gutter, sized to `SteeringGlyph` and
`FailureGlyph`'s own SVG (neither declares a wider box): `toolcallitem`'s `.row`
and `systemnoticeitem`'s `.failure` share one `display:flex; align-items:baseline;
gap:var(--space-2)` grammar. §7 formalises it and assigns the column.

| gutter | member | treatment | status |
|---|---|---|---|
| `◇` | steering | `--ink-mid`, kind from the wire, chevron trails | new |
| `✗` | failure | `FailureGlyph`, `--danger` glyph, `--ink-hi` text | shipped, unchanged |
| *(empty)* | lifecycle fact | `--ink-low` one-liner | shipped, unchanged |
| `▸` box | scaffolding | hairline-bordered box | shipped, unchanged |

Notification cards are outside the rule: a card, not a row, so it has no gutter.

Three of the four treatments do not move. The new surface is ◇ and one ink step
on one row type.

## 2. The wire field

```go
// agent/events/payloads.go, alongside Source — same optional/additive shape
Kind string `json:"kind,omitempty"`
```

Two insertion points cover all 18 sites:

- `steeringMessage.Kind`, carried through the single drain emitter at
  `session_queue.go:674` (`consumeSteeringMessage`). Covers the 8 `s.Steer()`
  callers plus the 3 that reach the queue through `toolDeps.steer`
  (`session_tool_registry.go:32`, wired at `:171`), which gains a kind parameter.
- The 7 emitters that construct `SteeringInjectedData` directly.

`prependSteering` (`session_queue.go:683`) is not a site: it re-queues
already-built `steeringMessage` entries, which carry their kind already.

`maybeInjectTaskReminder` (`session_tools.go:902`) has two returning triggers
behind one return value, so it returns `(text, kind)`. The call site must not
infer the kind from the text it just received — that would rebuild the
classifier in Go.

`internal/appprojector/appwire_projection.go:620` passes `Kind` through to the
thread item, alongside the existing `Source`.

### Kinds

| kind | label | site |
|---|---|---|
| `interrupted` | Interrupted | `session_lifecycle.go:645` |
| `agent-message` | Message sent | `subagents.go:885`, `job_delegate.go:541-557` (delegate_send caller alias), `job_delegate.go:1573` (delegate_send to a running child) |
| `hook-context` | Hook context | `session_queue.go:190`, `session_init.go:1294` |
| `precompact-hook` | Pre-compact hook | `session_compaction.go:159` |
| `compact-nudge` | Compaction nudge | `session_self_compact.go:116` |
| `image-description` | Image description | `session_tool_round.go:278` |
| `no-tool-calls` | No tool calls | `session_tool_round.go:34` |
| `loop-detected` | Loop detection | `session_tool_round.go:338` |
| `tasks-done` | Tasks done | `session_tools_task.go:224` |
| `task-nudge` | Task nudge | `session_tool_round.go:370`, kind decided in `session_tools.go:921` |
| `task-inactive` | Task list idle | `session_tool_round.go:370`, kind decided in `session_tools.go:931` |
| `note-handoff` | Note to self | `session_compaction.go:166` |
| `goal-objective` | Goal objective | `session_compaction.go:170` |
| `transcript-pointer` | Transcript pointer | `session_compaction.go:90` |
| `current-task` | *(suppressed)* | `subagents.go:696`, `session_init.go:241`, `session_tools_task.go:194,217` |
| `task-list` | *(suppressed)* | `session_namer.go:292` |
| `notification` | *(card)* | `session_lifecycle.go:1338`, `session_tools_communicate.go:139` (observer callback) |

`note-handoff` and `goal-objective` were added mid-implementation: `runPreCompactHook`
(`session_compaction.go:151`) merges three genuinely different sources — plugin
PreCompact output, the pinned-note handoff, and the active goal objective — and
each keeps its own kind rather than being merged under one label.

`current-task` and `task-list` stay suppressed — the tasks panel owns that surface
(parity-m4 §8:209-217). `notification` keeps its card.

The `contextmgr` strategies (`strategy_ooda.go:72`,
`strategy_recursive_distill.go:96`, `strategy_memory_crystals.go:84`) append
`TurnSteering` turns to history without emitting `EventSteeringInjected`. They
never reach the transcript and are out of scope; adding a kind to them would be
dead code.

## 3. Frontend

`SteeringItem.tsx` keeps its `source === "user"` branch unchanged — a human's own
steer still renders through `UserMessageView`.

Treatment routing moves from inferred kind to wire kind:

```
kind absent          → divider, no kind shown  ("System steered")
current-task | task-list → suppress
notification         → card
everything else      → divider, "System steered: <label>"
```

`steeringClassify.ts` loses `classifyStripped` and every prose pattern in it.
It keeps three things, which are parsing rather than guessing:

- `stripSystemReminder` — text cleanup for the divider body.
- `parseJobNotification` / `parseObserverCallback` and their helpers — these read
  `<job-notification …>` markup and a fixed `Observer callback:\n` header to build
  a card's fields. Structured markup cannot false-positive the way
  `/completed all tasks/` can, so this stays content-driven and is what a
  `notification` kind routes *into*.

**Recorded consequence:** a pre-`Kind` transcript whose steer carried a
`<job-notification>` block still routes to a card, because the card's trigger is
the markup, not the kind. A pre-`Kind` steer of any other type renders
`◇ System steered ▸`. This is the "claim nothing" decision applied consistently:
what can be read is read, what must be guessed is not.

### The glyph

Inline SVG in a new `widgets/steeringglyph/`, mirroring `widgets/failureglyph/`.

Not a text character: `global.css:23-24` subsets IBM Plex Sans to a `unicode-range`
running `… U+2039-203A, U+2044 …`. **U+25C7 falls in that gap.** A literal ◇ would
render from a system fallback font — the only glyph in the app doing so, with
metrics that vary by platform.

Unlike `FailureGlyph`, it takes no accessible name and is `aria-hidden`: the row's
own text ("System steered: Tasks done") is the `<summary>`'s accessible name and
already says everything the glyph says. `FailureGlyph` carries a name because it is
often the only failure signal on its row; here it never is.

The glyph is `--ink-mid` via `currentColor`, so it needs no token-contract
allowlist entry (§4 gates the three attention hues; this uses none).

### Copy

`System steered: <label>`, labels in sentence case. With no kind, `System steered`
with no colon — a colon promises a value.

## 4. Testing

- `steeringClassify.test.ts`: delete the cases covering deleted patterns; keep and
  extend notification-block parsing.
- `SteeringItem.test.tsx`: one case per treatment branch, plus the absent-kind row
  rendering "System steered" with no colon, plus a pre-`Kind` steer carrying a
  `<job-notification>` block still reaching a card.
- Go: each of the 17 kinds is asserted at its site(s) (see the Kinds table above —
  a kind and a call site are not 1:1; `agent-message`, `hook-context`, and
  `current-task` each have more than one). `TestEverySteeringKindHasAProducer` is
  the kind → producer net: every kind in the enum is produced somewhere, catching
  a kind going stale the way `read-only` did.
  `TestNoProducerPassesEmptySourceAndEmptyKindToTrySteerEnqueue` is the producer →
  kind net added when the review found three live producers still emitting no
  kind: no call site may carry neither a source nor a kind. A precise count of
  raw injection call sites (as opposed to kinds) isn't tracked by either test and
  drifts with the code, so this spec doesn't assert one.
- `token-contract.test.ts` needs no new allowlist entry; confirm it still passes.
- Gallery section for `SteeringGlyph` per §3's completeness test.

## Out of scope

- Whether the user's own steer should be distinguishable from a fresh prompt.
  Today only the missing 32px exchange break separates them
  (`opensExchange={false}`). Real question, different scope — Jesse chose the
  system-voice family, not a full voice audit.
- `--ink-low` remains below AA wherever else it is used (lifecycle lines,
  scaffold summaries). This spec moves one row type off it and does not relitigate
  the rest.
