# Transcript View: Tiered Density — Design Spec

Date: 2026-07-27
Status: Draft for Jesse's review. **No implementation** — this is a design doc.
Scope: the session transcript pane only (`cmd/serf-hub/frontend/src/panes/session/transcript/**`).
Evidence: real screenshots in `assets/2026-07-27-transcript-tiered-density/`, taken from a live
hub serving this branch. Code citations are file:line against this worktree.

## TL;DR

The transcript renders every item type at nearly the same visual weight, so the agent's prose
drowns in its own activity log. The fix is not new components — it is assigning every existing
item type to one of three explicit density tiers and making each tier keep its own promise:

1. **Conversation** (user + agent prose) — the loudest, most readable thing.
2. **Activity** (tool calls, thinking, tasks, diffs) — subordinate; one quiet line when settled.
3. **Meta** (system notices, round timings, subagent notifications, failures) — caption-size,
   no card chrome by default; detail behind disclosure.

Plus two targeted repairs: the thinking block's expanded-column geometry bug, and a new speaker
treatment for user/agent messages (Jesse: "right now they have inline labels, which does not
work so well"). Footprint: ~8 components + 7 CSS modules + their tests, all under
`panes/session/transcript/`.

## The problem, from evidence

The current design is not un-designed — `docs/web-ui/decisions.md` records measured, chosen
rules for most of what is on screen. The clunk comes from chosen decisions applied too broadly,
and from item types added after the mockups that never got a hierarchy assignment.

1. **Flat hierarchy.** Thought lines, tool chevron rows, `Ran …` mono lines, "System steered"
   rows, and bordered round-timing boxes all sit at nearly the same size and weight
   (`evidence-tool-call-expanded.png`). Principle 1 of the design law ("user and assistant prose
   are the loudest, most readable thing; tool calls are visually subordinate",
   decisions.md:59-61) is not what the eye experiences.

2. **The agent "hero" fires too often.** `agentmessageitem.module.css:17-20` sets
   `--prose-font-size: var(--font-size-pane-title)` (16px) on every agent message fragment.
   Topic 04 chose "agent prose wins on size and space" for *replies*; in practice every mid-turn
   narrative fragment gets the hero treatment, dozens per session, so the signal cancels itself
   and eats vertical space (`evidence-user-and-agent-turns.png`).

3. **Speaker labels don't work.** User messages carry an inline "You" tag in a fixed 40px
   gutter column (`usermessageitem.module.css:112-126`, topic 03, kata 8v4n); agent messages
   carry no visible label at all (topic 04's "the hero needs no introduction",
   `agentmessageitem.module.css:26-33`).
   Result: the user's label floats disconnected from its text at a 76rem measure, and nothing
   marks where the agent's voice resumes (`evidence-user-and-agent-turns.png`). Jesse's note on
   the approval of this work: "agent messages and user messages need better treatments, too.
   right now they have inline labels, which does not work so well."

4. **Thinking blocks unfold into a right-side column** (`evidence-thinkblock-columns.png`).
   Root cause: `thinkblock.module.css:64-68` makes the open `<details>` a flex row (deliberate —
   the golden reference sits a short label beside the body), but the `<summary>` also carries a
   ~120-character nowrap preview (`ThinkBlock.tsx:81-84`, `THOUGHT_PREVIEW_MAX_LENGTH = 120`
   at line 74) with `flex: none` (`thinkblock.module.css:93-106`). The long preview claims most
   of the row and the body is squeezed into a narrow column beside it — exactly the stacked/
   side-by-side hybrid nobody designed.

5. **Subagent notifications are full cards.** `NotificationCard.tsx` renders every completion —
   including routine successes — with Card chrome, a title, an excerpt, a tone Chip, and a "Raw
   notification" disclosure (delegate inventory, `NotificationCard.tsx:145-152`,
   `notificationcard.module.css:73-92`). One per subagent completion, in the middle of the
   conversation. This item type postdates the mockups; it was never assigned a weight.

6. **Round-timing notices render as bordered boxes.** `SystemNoticeItem.tsx:158`
   (`RoundTimingsLine`) renders each round's timings inside the hairline-bordered "scaffold"
   box grammar (`systemnoticeitem.module.css:52-62,90-97`). Topic 07's chosen rule is a quiet
   one-liner for system churn (decisions.md:219-226, LIVE for other notices); round timings
   slipped past it inside a box.

7. **Turn failures are the loudest thing on screen** (`evidence-turn-failure.png`): a red
   "✕ Turn failed" line, a danger Chip, the raw provider message, a four-line boilerplate hint,
   and a primary Retry button (`TurnFailureEndCap.tsx:105-119`). The failure *fact* is
   high-signal; the scaffolding around it is not.

## Current architecture (inventory summary)

`Session.tsx` builds the transcript model; `TurnBlock.tsx` renders one block per turn, filtering
items through `visibleItems`, grouping runs of ≥3 adjacent settled tool calls into
`ToolCallCluster` (`toolGrouping.ts:74-80`), dispatching the rest through the item-renderer
registry (`types.ts:21-33`), and appending `TurnFailureEndCap` / `TurnSeparator`
(`TurnBlock.tsx:107-109`). Registered renderers: `UserMessageItem`, `AgentMessageItem`,
`ThinkBlock`, `ToolCallItem` (via `ToolRow`'s single-line grammar), `SystemNoticeItem`,
`SteeringItem`, `WarningItem`, `NotificationCard`. Column measure is `--session-measure: 76rem`
(`turnblock.module.css:13-19`); items are separated by per-item padding only, with the sole
structural break being the 32px exchange boundary on user messages
(`usermessageitem.module.css:45-47`).

## The design

### Tier table

| Tier | Item types | Rendering rule |
| --- | --- | --- |
| 1 — Conversation | `userMessage`, `agentMessage` | Loudest text on screen. Body-size prose, full-contrast ink for the agent, mid ink for the user. Speaker eyebrow at exchange boundaries. |
| 2 — Activity | `commandExecution` (tools), `reasoning`, task cards, diffs, galleries, subagent modules | Subordinate. One quiet line per item when settled; expansion on demand. No item gets two lines by default. |
| 3 — Meta | system notices incl. round timings, steering system rows, subagent notifications, compaction, turn failures | Caption-size, ink-mid/low, no boxes or cards by default. Facts visible; boilerplate and raw payloads behind disclosure. |

### Tier 1 — Conversation

**Agent prose drops to body size.** `agentmessageitem.module.css:.message` stops setting
`--prose-font-size: var(--font-size-pane-title)`; agent prose renders at `--font-size-body`
(14px), same as everything else. The hierarchy that survives is the contrast pair that was
*also* chosen in topic 04 and is genuinely load-bearing: agent prose `--ink-hi` against user
text `--ink-mid` (decisions.md:193). This is a deliberate revision of topic 04 Alt A ("wins on
size and space") — see the ratification list.

**Speaker eyebrows replace inline labels.** Both voices get the same geometry: a stacked
caption eyebrow above the content, not an inline tag beside it.

- Eyebrow style: `--font-size-caption`, `--font-weight-medium`, `--ink-low`, sentence case,
  no uppercase transform (uppercase is reserved for structural grouping eyebrows per
  design-system.md §Type; a speaker is a voice, not a grouping).
- User: eyebrow "You" above the message, replacing the 40px `.tag` gutter column; the message
  text reclaims the full content width.
- Agent: eyebrow "Agent · {model}" (e.g. "Agent · k3") above the first agent item of a reply
  run — cheap, always-accurate attribution that also helps when comparing across sessions.
- **Timing:** eyebrows render at *exchange boundaries only* — on the user message that opens
  an exchange (the same items that carry `data-opens-exchange` today) and on the first agent
  item following it. Subsequent agent fragments inside the exchange render unlabeled: they are
  continuous work, not new voices. This fires a handful of times per session, matching the
  existing 32px boundary mechanic (`usermessageitem.module.css:33-37` — "a turn is one LLM
  round-trip… marking each would slice one continuous piece of agent work into arbitrary
  slabs").
- The hover/focus message actions (`usermessageitem.module.css:78-88`) move into the eyebrow
  row — one predictable chrome location, and the message's first line stays clean.
- The SR-only "Agent" label (`agentmessageitem.module.css:34-44`) is superseded by the visible
  eyebrow; the text remains in the DOM, so screen-reader linear order is unchanged or better.
- The 32px exchange-boundary margin stays exactly as measured.

### Tier 2 — Activity

**ThinkBlock: preview only when collapsed.** The open `<details>` keeps its flex row (the
golden-reference geometry is correct for a *short* label), but `ThoughtLabel` drops the preview
segment whenever the disclosure is open: closed shows `Thought for 12s · <preview>`, open shows
`Thought for 12s` beside a body that now claims the full remaining row width. One-condition
change in `ThinkBlock.tsx` (`thoughtLabel`, line 81-84) plus a test that an open block's body
exceeds ~85% of the content column width. No CSS restructure.

**Settled tool calls are one line.** Today a settled call with a stated purpose renders two
lines: the purpose, then `verb target · duration` beneath it (ToolRow.tsx:118-140 composes
them as siblings that wrap). Change the settled-collapsed grammar to a single line:

```
› purpose — verb target · 90ms
```

purpose in the row's normal ink, everything after the em-dash demoted to `--ink-mid`, one line
with ellipsis (full text on `title`). Live/streaming rows, expanded bodies, failure glyphs,
clusters, and trailing affordances are unchanged. The ToolRow file comment's grammar
(`ToolRow.tsx:6-25`) is amended to match. Purpose-less calls are already one line.

Clustering (`toolGrouping.ts`, `ToolCallCluster.tsx`) is unchanged. Known limitation, recorded
as follow-up only: adjacency-only run detection (`toolGrouping.ts:53-65`) is defeated by
thought/tool alternation, so clusters rarely form in reasoning-heavy sessions.

### Tier 3 — Meta

**NotificationCard defaults to one line.** Collapsed: tone Chip (warning/error only — neutral
and success carry no tint, per the component's own existing rule) + one line of text —
`{agent} finished · {mandate excerpt} · {duration}` — + chevron. The Card chrome (border,
surface, padding) renders only when the disclosure is open; the excerpt and "Raw notification"
sections live inside it unchanged. Routine completions become one quiet line each; warnings and
errors keep their tone.

**Round timings lose the box.** `RoundTimingsLine` (`SystemNoticeItem.tsx:158`) stops using
the hairline-bordered scaffold grammar and renders the same collapsed line as plain text at
caption size, `--ink-mid`, no border or radius. Expanding still reveals the phase breakdown;
the box chrome is gone in both states. This brings round timings under topic 07's existing
quiet-one-liner rule. Other `SystemNoticeItem` kinds keep their current treatment — the box is
only wrong for a notice that fires once per round.

**Turn failures become compact.** `TurnFailureEndCap` collapses to a single head row:
failure glyph + `Turn failed` + danger Chip + the error message + the Retry action inline
(same `info.recoveryLabel`, same `threadsStore.send` re-issue path). The four-line hint moves
behind a "What can I do?" disclosure — the advice is real, but it is scaffolding, not fact.
The danger tone stays confined to the Chip, honouring the colour allowlist posture documented
in the component (TurnFailureEndCap.tsx:7-12).

**Unchanged here:** steering system rows and other system notices (already quiet one-liners,
topic 07 LIVE), `TurnSeparator` (already caption-size, borderless, pref-gated default-off,
`turnseparator.module.css:3-8`).

### Measure and rhythm

`--session-measure: 76rem` stays. The vertical-space win comes from the one-line rules above,
not from re-spacing or re-widening the column; touching the measure ripples into layoutguard
baselines and every pane.

## Decision revisions requiring Jesse's ratification

This spec knowingly revises recorded decisions. Each is a chosen, measured rule — the record
in `docs/web-ui/decisions.md` should be updated when this ships.

1. **Topic 04 Alt A** ("agent prose wins on size and space", decisions.md:187-195): revised to
   wins on *contrast* (ink-hi vs the user's ink-mid); size returns to body. Rationale: the size
   signal is applied per-fragment, dozens of times per session, and cancels itself.
2. **Topic 04's no-visible-agent-label rule** ("the hero needs no introduction",
   `agentmessageitem.module.css:26-33`, enacted per topic 04): revised — the agent gets a
   visible eyebrow at exchange boundaries.
   Per Jesse's note on this spec's approval.
3. **Topic 03 geometry** (40px inline "You" gutter column, kata 8v4n,
   `usermessageitem.module.css:112-126`): revised to the stacked eyebrow; the gutter column is
   removed. The contrast inversion (.tag ink-hi over .text ink-mid) is retired with it; the
   eyebrow is ink-low per the eyebrow idiom. Note for the steering work landing elsewhere:
   `steeringitem.module.css` still stacks its summary pre-8v4n-style (decisions.md:182-185) —
   when that work lands it should adopt this eyebrow, not the gutter.
4. **kgp2 think-block preview** (decisions.md:198-209, preview on the collapsed line): kept
   for the collapsed state, dropped from the open state. The open state was never specified;
   this specifies it.
5. **SystemNoticeItem scaffold box for round timings**: round timings join topic 07's quiet
   one-liner rule.

## Non-goals

Sidebar/navigator, composer, status row, steering items (owned by another worktree per
decisions.md), subagent module internals, live streaming behaviour, virtualization, the widget
library, the colour allowlist, tool-output body renderers. No new colours, no new type sizes,
no new motion. No changes to `visibleItems`, `toolGrouping`, or the item registry.

## Component-by-component changes

| File | Change | Acceptance |
| --- | --- | --- |
| `messages/agentmessageitem.module.css` | Delete the `--prose-font-size` override | Computed agent prose size equals `--font-size-body`; live and settled paths match |
| `messages/UserMessageItem.tsx` + css | Replace `.tag` gutter column with stacked "You" eyebrow on exchange openers; actions move to eyebrow row | No 40px column; eyebrow only when `data-opens-exchange`; hover actions still reachable by keyboard |
| `messages/AgentMessageItem.tsx` + css | Add "Agent · {model}" eyebrow on the first agent item of an exchange run; drop `.srOnly` | Eyebrow appears once per exchange; absent on continuation fragments |
| `messages/ThinkBlock.tsx` | Preview segment only when closed | Open block's body ≥ ~85% of content column width; collapsed line unchanged |
| `ToolRow.tsx` + `toolcallitem.module.css` | Settled rows compose purpose + summary on one line | One-line settled rows with ellipsis; two-line rendering gone; expanded bodies unchanged |
| `messages/NotificationCard.tsx` + css | Card chrome only when open; one-line collapsed row | Collapsed height = one text line; warning/error keep tone chip; raw disclosure still available when open |
| `messages/SystemNoticeItem.tsx` + css | `RoundTimingsLine` drops the scaffold box | No border/radius on the round-timings line in either state; other notice kinds unchanged |
| `TurnFailureEndCap.tsx` + `turnfailure.module.css` | Single head row with inline Retry; hint behind disclosure | Collapsed cap is one row tall; retry still re-issues the originating input; hint reachable |

Where does "first agent item of an exchange run" come from? `TurnBlock` already knows item
order and types; it passes an `opensExchange`-style flag to the first `agentMessage` that
follows a `userMessage` (mirroring the existing `data-opens-exchange` computation for user
messages, `UserMessageItem.tsx:147-153`). No model changes.

## Verification

- Update the affected vitest suites: ThinkBlock open-label, ToolRow one-line grammar,
  NotificationCard collapsed default, TurnFailureEndCap disclosure, SystemNoticeItem
  round-timings chrome, UserMessageItem eyebrow/actions, AgentMessageItem eyebrow.
  `requireClass` contract updates as classes are added/removed.
- `npm run typecheck`, `npm run lint`, `npm run layoutguard`, `npm run overflowguard` pass in
  `cmd/serf-hub/frontend`.
- Browser screenshots, both themes, before/after: a dense live session (activity tiers), an
  expanded think block (no column), a session with subagent completions (one-line
  notifications), a failed turn (compact cap). The `evidence-*` assets in this directory are
  the "before" set.
- Default tests stay deterministic per `AGENTS.md` — no live-provider assertions.

## Follow-ups (explicitly not this spec)

- Tool clustering defeated by thought/tool adjacency (`toolGrouping.ts:53-65`): candidate
  rule change is "settled reasoning items don't break a run," but that reorders rendered
  content and needs its own design.
- Steering item speaker treatment, when the steering work lands (see ratification item 3).
- Per-turn activity rollup (the "direction B" from the brainstorm): if wanted later, these
  tier definitions are its visual language.

## Evidence index

All in `assets/2026-07-27-transcript-tiered-density/`:

- `evidence-user-and-agent-turns.png` — inline "You" label, 16px agent hero fragments,
  two-line tool rows, expanded error output card.
- `evidence-thinkblock-columns.png` — settled think block unfolded into a right-side column.
- `evidence-tool-call-expanded.png` — flat hierarchy: thoughts, tool rows, output cards,
  steered rows at one weight; expanded output-card treatment.
- `evidence-turn-failure.png` — the loud failure block; also the user turn at 76rem measure.

A round-timings screenshot is not included: the notice is preference-gated (Settings →
Transcript → Round timings, default off per `TurnSeparator.tsx:9-13` and the ToolRow duration
comment at `ToolRow.tsx:60-65`) and was not rendering in the loaded window at capture time.
Its boxed chrome is cited from code instead (`systemnoticeitem.module.css:52-62,90-97`).
