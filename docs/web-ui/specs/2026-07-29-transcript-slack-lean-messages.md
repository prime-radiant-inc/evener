# Transcript Messages: Slack-lean Speaker Treatments — Design Spec

Date: 2026-07-29
Status: Draft for Jesse's review. **No implementation** — this is a design doc.
Scope: the session transcript pane (`cmd/serf-hub/frontend/src/panes/session/transcript/**`)
plus one new widget (`src/widgets/speakeravatar/`).
Evidence: rendered mockups in `assets/2026-07-29-transcript-slack-lean/`, produced from a
static page using the repo's real `tokens.css`/`global.css` and the real toolicon glyph
paths, screenshotted in both themes at desktop and 390px widths.

## TL;DR

Jesse picked **direction D ("Slack-lean") with a responsive gutter** from five mocked
directions (`directions-all.png`), answering his own framing question ("slack or chat
apps?") toward chat. Speaker identity stops being a faint stacked caption eyebrow and
becomes a one-line header — avatar tile + name + meta — at exchange boundaries; the agent's
side of the exchange indents to a content column so a whole turn of work reads as one
grouped run. Below ~700px the gutter collapses: the avatar moves inline into the header and
every row goes full-width (`mobile-d1-vs-d2.png`). Folded in, per Jesse: **system steering
moves from caption to body size**, the same as other messages.

## The decisions

1. **Speaker header (both speakers).** A flex row: 24px avatar tile, then `Name` at body
   size / medium weight / `--ink-hi`, then meta at caption / `--ink-low`:
   - User: `You` + message time (`12:41`, from `ItemModel.startedAt`, local HH:MM).
   - Agent: `Agent` + `<model label> · <time>` (the existing `agentLabel` plumbing,
     `TurnBlock.tsx:36-37`, plus the opening agent message's `startedAt`).
   The agent header fires exactly where today's eyebrow does — the exchange opener
   (`exchangeOpeners.ts`), a handful of times per session, never per turn.

2. **Avatars.** New `widgets/speakeravatar/`: a 24px rounded square (`--radius-control`),
   1px `--edge` border, `--surface-2` fill, glyph in `--ink-mid`. Glyphs come from the
   toolicon grammar (16-grid, stroke currentColor, 1.75): agent reuses the existing
   `skill` sparkle; user gets a new `person` kind added to `widgets/toolicon`. In the light
   theme `--surface-2` equals `--surface-1`, so the border does the work — same situation as
   the MANDATE box, already accepted. The WidgetGallery contract
   (`src/dev/WidgetGallery.test.tsx`) means the new widget directory ships with a gallery
   section.

3. **The gutter (desktop).** Above the breakpoint, agent-side content takes a 34px content
   column (24px avatar + 10px gap). Mechanism: `TurnBlock` wraps each rendered item in a
   row container classified by item type (see table) — avatar rows manage their own flex
   layout, margin rows span the full width, content rows get the indent. The indent is
   per-turn, which reads as continuous across turns because adjacent turns share it; an
   exchange-level wrapper was rejected because VirtualList windows per turn.
   Deliberate trade, stated: the avatar marks the exchange's *start*; a 60-row exchange
   loses the binding mid-run. Jesse accepted this when choosing D over A (rails, which bind
   continuously but which he did not pick).

4. **The breakpoint.** 700px, one CSS media query in `turnblock.module.css` (matching the
   overflowguard sweep's existing widths: 390/700/1024/1400 — 700 and 390 exercise the
   collapsed variant, 1024/1400 the guttered one). The collapsed variant is not a second
   layout: the header is a flex row at every width, so below 700px the avatar is simply
   inline in the header and no row carries an indent.

5. **User message text goes `--ink-hi`.** The approved mock renders it at full contrast,
   reversing the tiered-density demotion (`usermessageitem.module.css:66-73`, "the user
   knows what they said"). The demotion was doing hierarchy work the new header now does
   better: the boundary is scannable without muting the words. Recorded as a decisions.md
   reversal with this citation.

6. **Steering at body size.** `steeringitem.module.css`: `.summary` from
   `--font-size-caption` to `--font-size-body`, `.body` from `--font-size-ui` to
   `--font-size-body`. Tension noted: the row's header comment calls steering "routine
   bookkeeping, never an attention-worthy event" — body size makes it louder; Jesse asked
   for it explicitly ("system Steering should probably be the same font size as other
   messages"). Ink stays `--ink-mid`, disclosure stays collapsed-by-default: the quietness
   now comes from structure, not size.

7. **Everything else unchanged.** Tool rows, thinking rows (lightbulb + label, expanded
   markdown body), notification cards, turn separators, failure end-caps keep their current
   treatments; they inherit only the gutter indent per the classification below.

## Item-type classification (gutter contract)

| Item / chrome | Class | Why |
|---|---|---|
| userMessage | avatar row | header = avatar + `You` + time; text in the content column of the same flex row |
| agentMessage at exchange opener | avatar row | header = avatar + `Agent` + `model · time` |
| agentMessage mid-exchange | content | prose under the run |
| reasoning (thinking) | content | lightbulb row + body indent with the run |
| commandExecution + ToolCallCluster | content | tool rows indent with the run |
| steering (incl. notification cards) | margin | daemon bookkeeping spans full width, as in the approved mock |
| systemNotice, warning | margin | structural/meta rows stay at the margin |
| TurnSeparator, SeenDivider, TurnFailureEndCap | margin | transcript chrome, not speaker content |

## Footprint

- New: `widgets/speakeravatar/` (+ tests, gallery section), `person` kind in
  `widgets/toolicon` (+ test), a time-format helper (HH:MM local, with tests).
- Modified: `UserMessageItem` (+css), `AgentMessageItem` (+css), `TurnBlock` (item
  classification wrapper) + `turnblock.module.css`, `steeringitem.module.css`, and their
  test files.
- docs: decisions.md entries for D, the responsive gutter, the ink-hi reversal, and the
  steering size change.

## Verification

Per-task: targeted vitest suites + typecheck + biome. Final: full suite, layoutguard,
overflowguard (all four widths — the 390/700 legs are the collapsed-gutter contract, the
1024/1400 legs the guttered one), and a screenshot pass in the real harness (both themes,
390/700/1400) covering: an exchange boundary with both headers, a long agent run, a user
message with hover actions, steering at body size.

## Open questions for Jesse

1. **Timestamps: absolute HH:MM (mock) or relative ("2m ago")?** Mock shows absolute;
   relative ages on screen between refreshes. Default: absolute, matching Slack.
2. **Mid-exchange agent prose**: keep flat (no header) — assumed yes, matches the mock.
3. **Delegate/subagent rows** keep the status dot beside the branch icon (today's
   treatment), or drop the dot now that the header carries liveness? Default: keep.
