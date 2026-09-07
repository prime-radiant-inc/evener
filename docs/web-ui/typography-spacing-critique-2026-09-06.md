# Web UI typography, spacing and balance: critique and recommendations

Status: **implemented**, 2026-09-06, by
`docs/superpowers/plans/2026-09-06-webui-typography-spacing.md` on branch
`claude/evener-webui-typography-spacing-3d4fd4`; the decisions it settled,
including the two earlier rulings it revises, are recorded in
`decisions.md`'s 2026-09-06 entry. The measurements below are what the app
looked like before that work, and are kept as the record of why. This
document is the result of reading `design-system.md`, `decisions.md`,
`ux-plan-2026-07.md`, the token and widget stylesheets under
`cmd/evener-hub/frontend/src`, and measuring the running app (Vite dev
server against a throwaway hub, a real glm-5.2-vision session, plus
`/dev/surfaces`) at 1440×900 and 375×812 in both themes.

Paths below are relative to `cmd/evener-hub/frontend/src/` unless they start
with `docs/`.

## Overall impression

The system is disciplined and the reading experience is still poor. Those two
facts are not in tension: the discipline is about *tokens* (no literals, AA
contract, one focus ring, a 4px grid), and it is real and worth keeping. The
poverty is about *hierarchy*, *measure* and *rhythm*, which no token contract
enforces. Every screen is set in the same three sizes (12, 13, 14px), demoted
only by ink, in one line-height, at whatever width the pane happens to be.
The result reads as a flat grey list on desktop, and the phone gets the same
list with the text made smaller by a wasted gutter.

The biggest single opportunity is the transcript: it is the product's reading
surface and it is currently set like a dashboard sidebar.

## What was measured

| Fact | Value | Where |
|---|---|---|
| Type ramp | 12 / 13 / 14 / 16 / 20 px, body 14 | `styles/tokens.css:336-343` |
| Line heights | 1.5 body, 1.3 title, nothing else | `styles/tokens.css:137-138` |
| Explicit `font-size` declarations by token | caption 208, ui 132, body 31, pane-title 9, page-title 2 | grep over `src/**/*.css` |
| Literal (off-ramp) sizes | 9px ×1, 10.5px ×1, 11px ×8, 11.5px ×2, 12px ×2, 13px ×2, 16px ×2 | see §Bugs |
| Transcript measure | `--session-measure: 76rem` = 1216px | `panes/session/transcript/turnblock.module.css:15` |
| Agent prose line length, 1440 viewport, plain paragraph | **149 characters/line** (bubble 1087px, 14px Inter) | measured in `/dev/surfaces` |
| Agent prose line length, real session, 1440 | bubble 1005px, ~125 chars/line | measured live |
| Agent prose width, 375 viewport | **260px of 375** (69% of the screen), 28 chars/line | measured live |
| Composer field | 13px, 1108px wide on desktop, 13px on the phone | `widgets/textarea/textarea.module.css:17` |
| Pane title | 12px, uppercase, 0.08em tracking, `--ink-mid` | `widgets/panescaffold/panescaffold.module.css:24-37` |
| Session pane title after turn 1 | the whole prompt, set in that 12px uppercase micro-label | observed: "CREATE A SMALL PYTHON SCRIPT NAMED FIZZBUZZ.PY IN THIS DIRECTORY THAT PRINTS…" |
| `text-transform: uppercase` sites | 21 | grep |
| Tracking values in use | −0.02em, 0.02em ×4, 0.04em ×5, 0.05em ×2, 0.08em ×7 | grep |
| `color: var(--ink-low)` on text | 96 declarations; `--ink-low` computes to 3.08:1 dark / 3.51:1 light on `--surface-1` today (the repo's comments record 2.97 / 3.64 from an earlier palette) | WCAG ratio computed from `tokens.css` values |
| Spacing discipline | 662 token uses vs 48 raw px | grep |
| Breakpoints | 899px ×22, 900px ×3, 700px ×5, 559, 520, 479, 399, 34rem | grep |
| Rail | 280px; quiet row 28px, signal row 38px; label 13px; brand 12px `--ink-mid` | `shell/rail/*.module.css`, `widgets/tree/tree.module.css` |
| Spawn form | 720px, left-pinned inside a 1160px pane at 1440 (440px dead) | `panes/spawn/spawn.module.css:5` |
| Undefined tokens referenced | `--radius-sm` ×2, `--edge-hi`, `--font-size-title` | see §Bugs |

## Findings

### 1. The ramp has no hierarchy in it (🔴)

Five steps with a ratio of about 1.08 between them is not a scale, it is one
size with rounding error. Body 14px is the largest text most people ever see;
16px appears on nine rules and 20px on two. Then 340 of 382 explicit size
declarations pick *caption* or *ui*, so almost everything on screen is 12 or
13px. The design law says "quiet through ink, never size" (`thinkblock.module.css`
header), and that is a fine rule for demoting a thought below its answer. It
cannot do the other job, which is giving the eye landmarks: a speaker header
at 14px/500 next to body at 14px/400 is a landmark only if you are already
looking for it.

Markdown inside agent prose has the same problem one level down: `h3` is body
size (`widgets/markdown/markdown.module.css:47-49`), `h2` is 16px against 14px
body. A heading in an agent's answer is nearly invisible.

### 2. Line length is unbounded on desktop and starved on the phone (🔴)

`--session-measure` is 76rem. The root font-size is 16px, so the column is
1216px, wider than the pane at 1440 and effectively full-bleed. A plain
paragraph measures 149 characters per line; the comfortable range is 45–75,
and chat products that must also show code settle around 90–100 in a ~48rem
column. Nothing in the app currently bounds prose at all: the bubble's
`max-width: 92%` is 92% of whatever the pane is.

On the phone the opposite happens. The opener message is a flex row of avatar
+ column (`agentmessageitem.module.css:.opener`), the column gets the bubble at
92%, and the bubble has 24px of its own padding, so prose gets 260px of a
375px screen. 28 characters per line is below the mobile floor (35–45), and
the left third of the screen is empty below the avatar.

### 3. Vertical rhythm has one step (🔴)

Message rows pad 4px, tool calls pad 8px, and the only deliberate rhythm cue in
the transcript is "8px between calls vs 0 inside a call" (`toolcallitem.module.css`
header comment). The 2026-07-30 ruling against "dead air" at exchange boundaries
(`usermessageitem.module.css` header) removed the last larger step. With 149-char
lines and 4px gaps there is now nothing between a user message and the previous
turn's footer except a 24px avatar. Principle 2 of the brief, folding a run of
finished tool calls into one line, is recorded as "ABSENT, unexplained" in
`decisions.md` topic 06; its absence is most of why a working session reads as
a column of identical rows.

### 4. Micro-labels are doing a heading's job (🟡)

The Beautiful UI micro-label (11.5–12px, uppercase, 0.08em) was ported as the
*pane title*. On a dashboard card that pattern labels a container that sits
inside a page with a real heading. Here the pane is the page, so the page's
title is its smallest text: "START AN AGENT", "GENERAL", "THEME". Once a
session has a title, that title is the user's whole prompt, and it renders in
the same uppercase micro-label, which `design-system.md` §6 explicitly forbids
("never for … sentences, or anything longer than ~2 words"). Both
"START AN AGENT" and "AGENTS & MODELS" are three words. The frontend-design
brief lists tracked-out all-caps eyebrows above every heading as the commonest
tell of a templated page; this UI has 21 of them and four different tracking
values.

### 5. Mono leaks into chrome (🟡)

Principle 7 (mono only for machine text) is mostly honoured, but the places it
leaks are the ones you look at constantly: the model id in the status row and
composer chip (`chrome/modelswitch.module.css:.value`), the effort/model pair
in the spawn card ("(default) ˅ · (default)" reads like debug output), and the
rail's relative timestamps (`RailRow.module.css:.time`). Tabular figures in the
sans face (`font-variant-numeric: tabular-nums`, used on 3 rules today) give
alignment without switching faces.

### 6. The three rail icons are not in the font (🟡)

`Rail.tsx:1408-1427` renders ⚙ ⌕ ☰ as text. `global.css` subsets Inter to Latin
(the same reason `SteeringGlyph` is drawn as SVG, per `design-system.md` §7),
so these three glyphs come from whatever system fallback has them, at whatever
stroke weight it has. They sit next to SVG chevrons and the SVG open-box icon.

### 7. `--ink-low` is a text colour in practice (🟡)

The token is documented for "placeholders, disabled, timestamps" and measured
under AA. It is nonetheless the colour of 96 text rules: rail section titles,
speaker meta, thought summaries, the liveness line, hints, "Configured plugins:
none", the welcome chord hints. Most of those are 12px too, so the two
demotions compound. `decisions.md` topic 01 records the rule "dimmest tone is
hairline-and-chrome, never body text" as LIVE; the grep says otherwise.

### 8. Desktop balance (🟡)

- Spawn: a 720px form pinned to the left edge of a 1160px pane, under a
  12px uppercase title. The phone version of this same form has a real
  heading ("What should the agent do?", 16px/600) and a subtitle; desktop
  hides both (`spawn.module.css:.mobilePromptIntro { display: none }`). The
  phone found the right answer and desktop does not use it.
- Settings: a 200px nav and an unbounded content column, so label/value
  hairlines span 900px+ at 1440 and read as a spreadsheet.
- Session: the transcript column is centred at 1216px, which at 1440 with a
  280px rail is wider than the pane, so it is not centred at all; at 1920 it
  would suddenly be. The composer aligns to the same literal.
- The pane body padding is 16px at every width. At 1440 that is tight against
  a 1px hairline; the panes read as browser default margins.

### 9. Mobile (🟡, with one 🔴)

- 🔴 The session composer is 13px on the phone (only the spawn textarea gets
  16px, `spawn.module.css:332`), and `index.html` sets
  `maximum-scale=1, user-scalable=no` to stop iOS zooming into it. That
  disables pinch-zoom for the whole app (WCAG 1.4.4). The fix is a 16px
  field, not a viewport lock.
- The spawn pane's top-bar title is the string "new" (`Spawn.tsx:860`,
  `mobileTitle="new"`): a route slug as a title.
- The spawn placeholder repeats the heading and subtitle above it almost
  word for word.
- The Sessions drawer renders the rail as an inset box inside the Sheet (a
  darker rounded panel with the brand row and button in it, then a second
  box for the tree) even though `Rail.module.css` says the mobile rail should
  fill the sheet flush.
- The rail's empty state says "Start a session from the command line to see
  it here" directly under a "+ New session" button.
- Agent prose at 260px wide (finding 2).

### 10. What works

- Token discipline: 93% of spacing declarations use the grid, no colour
  literals outside `tokens.css`, one focus ring, contract-tested AA for the
  `-ink` hues, a real z ladder. Keep all of it; the recommendations below
  are token *values* and a few new tokens, so the machinery carries them.
- The mobile spawn form (`MobileSettingRows.module.css`): 48px rows, 16px
  labels, sentence case, one chevron. It is the best-set screen in the app
  and the template for the rest of mobile.
- The rail's signal rows: amber "home · your move" under the title is exactly
  the kind of quiet, worded state the brief asked for.
- Inline code as an underline rather than a chip; italic intent lines on tool
  rows; the `corner-shape: squircle` progressive enhancement.

## Recommendations

Ordered by what a reader gains. Each is scoped so it can be one PR.

### R1. Cut a real type ramp, and set the body at 15px (desktop) / 16px (phone)

Proposed `tokens.css` body block, ratio ≈ 1.2 with a 4px line grid:

```css
body {
  --font-scale: 1;
  --font-size-caption: calc(12px * var(--font-scale));    /* timestamps, meta */
  --font-size-ui: calc(13px * var(--font-scale));         /* dense chrome: rail rows, status row, tables, chips */
  --font-size-body: calc(15px * var(--font-scale));       /* prose, composer, forms, settings values */
  --font-size-pane-title: calc(18px * var(--font-scale)); /* pane/sheet/dialog titles, sentence case, 600 */
  --font-size-page-title: calc(22px * var(--font-scale)); /* settings section headings, spawn heading */
  --font-size-display: calc(28px * var(--font-scale));    /* welcome hero only */
  --line-height-ui: 1.4;      /* 13px → 18px */
  --line-height-body: 1.6;    /* 15px → 24px, on the grid */
  --line-height-title: 1.25;
}
@media (max-width: 899px) {
  body { --font-size-body: calc(16px * var(--font-scale)); }
}
```

Buttons and inputs stay at `--font-size-ui` in their 24/28/32px boxes, so no
control geometry changes. Markdown headings become 22 / 18 / 15-semibold, so an
`h3` in agent prose is finally distinguishable from a paragraph. The existing
S/M/L/XL preference keeps scaling the whole ramp.

Why 15 and not 14: Inter at 14px on a 1440 display is a settings-dialog size.
Reading products that also carry code (GitHub, Slack, Linear's content views,
both major chat assistants) sit at 15–16. The `ux-plan-2026-07` note that "it
is not typography" was about *information* the old build showed; it did not
find the current sizes good, only more consistent.

### R2. Bound the measure

```css
--session-measure: 44rem;        /* 704px: ~90 chars at 15px, room for a 100-col code block */
--session-measure-wide: 64rem;   /* opt-in "Wide transcript" preference, same Theme section as density */
```

Apply it to `.turn`, the composer `.measure` and `.coldStart` (they already
share the literal), drop the bubble's `max-width: 92%` in favour of 100% of
the column, and centre the column with `margin-inline: auto` so it is centred
at every width rather than only above 1500px. Add a layoutguard case that
fails when a plain paragraph exceeds 100 characters per line at 1440 and 1920.

On the phone, below 700px, let the opener bubble span the pane: keep the
avatar in the header row but make `.column` start at the pane edge (a
`grid-template-columns: auto 1fr` with the bubble on the full second row).
That returns ~70px to prose and lifts it to ~38 characters per line.

### R3. Rebuild the rhythm on four steps

Name them so the transcript has a vocabulary for "how far apart":

```css
--rhythm-line: var(--space-1);   /* 4: inside one item (intent → call) */
--rhythm-item: var(--space-2);   /* 8: between items in a run */
--rhythm-group: var(--space-4);  /* 16: between a run and the next speaker header, above a turn footer */
--rhythm-exchange: var(--space-5); /* 24: above a user message */
```

This revisits the 2026-07-30 "no dead air" ruling. That ruling was made when
messages had no bubbles and a 14px header; the header alone cannot mark a
boundary when lines are 100+ characters. 24px above a user message, with the
new column width, is not dead air, it is a paragraph break. Pane body padding
goes to `--space-5` on desktop, `--space-4` on the phone. Rail rows go to 32px
quiet / 44px signal with `--space-5` between sections.

### R4. Give panes a heading and demote micro-labels back to cards

`PaneScaffold` title becomes `--font-size-pane-title`, 600, sentence case,
`--ink-hi`, with the cadence beside it. Session titles (a user sentence) render
at `--font-size-body`, 500, truncated, never transformed. The micro-label
stays for `InspectorCard`, `RecommendationCard`, `Table` headers and the
Dialog header band, where it labels a container inside a page.

Collapse the eyebrow spread to one token and one rule:

```css
--tracking-eyebrow: 0.06em;   /* replaces 0.02 / 0.04 / 0.05 / 0.08 */
```

12px, 500, uppercase, `--ink-mid` (not `--ink-low`), two words maximum,
enforced by the copy rule in §6 and a test that every `text-transform:
uppercase` rule sits on a class named `eyebrow` or `microLabel`. Rename
"Agents & models" to "Models", "Start an agent" to "New session", so the
labels that remain fit the rule.

### R5. Faces and figures

- Model: show the catalog display name in sans ("GLM 5.2 Vision"); the
  qualified id goes in the tooltip and the Details sheet. Effort in sans.
- Rail timestamps and turn-footer figures: sans with
  `font-variant-numeric: tabular-nums`. Mono stays for code, paths, shell
  summaries, diffs, and identifiers in Details.
- Replace ⚙ ⌕ ☰ with 16px SVG icons through `IconButton`, matching
  `widgets/chevron` and `OpenIcon` stroke.

### R6. Raise `--ink-low` until it is readable, then stop using it for reading

Dark `#6C6F75` → `#8A8E95` computes to 4.72:1 on `--surface-1`, 5.40:1 on
`--surface-0`, and 3.89:1 on the pressed/selected wash `--hover-2`. Light
`#86898F` → `#6F737A` computes to 4.76:1 on white, 4.57:1 on `--surface-0`,
3.91:1 on `--hover-2`. So the raised value fixes resting text but not text
that stays `--ink-low` on a selected row; those sites (rail row timestamps,
palette result meta) need `--ink-mid` regardless. The contract test in
`styles/token-contract.test.ts` is the authority; add `--ink-low` to its
AA-pair checks against `--surface-0/1` when the value changes. Then move the
most-read sites (speaker meta, thought summary, rail section titles, liveness
line) to `--ink-mid` anyway, so `--ink-low` is left with placeholders,
disabled controls and hairline-adjacent chrome, which is what its comment says.

### R7. Desktop layout balance

- Spawn: centre the form (`margin-inline: auto`), show the heading and
  subtitle on desktop at `--font-size-page-title` / body, stack the directory
  card, prompt card and options with `--space-5`, and drop the uppercase pane
  title once the heading exists.
- Settings: cap the content column at 44rem, or render label/value pairs as a
  two-column definition list with a 160px label column; section title at
  `--font-size-page-title`.
- Welcome: the display size for one line, the two buttons, and the chord hints
  as a small two-column table instead of three centred rows.

### R8. Mobile specifics

- Session composer at 16px on the phone; then remove `maximum-scale=1,
  user-scalable=no` from `index.html`.
- `mobileTitle="New session"`; remove the duplicated placeholder copy.
- Drawer: render the Rail flush inside the Sheet body (no inner surface box,
  no inner radius); the sheet already frames it.
- Rail empty-state copy: "No sessions yet. Start one above." or nothing.
- Keep 44/48px targets as they are; they are already right.

### R9. Transcript grammar (larger, already in the design law)

- Fold a run of finished, non-failing tool calls into one row that names the
  consequential step and the count ("Read 4 files, edited 2 · 1.8s"), expanding
  to the rows. This is principle 2 and `decisions.md` topic 06 Alt A; nothing
  in the rhythm work above substitutes for it.
- Agent prose as a document, not a bubble: with the column bounded (R2), the
  neutral wash behind agent text stops doing any work and mostly adds a slab.
  Keep the accent wash for the user's own words, which are short and benefit
  from a shape. This reverses the 2026-07-30 bubble decision for the agent
  side only, and it is the one recommendation here that is taste rather than
  measurement; it is the convention both major chat assistants converged on
  for long technical answers.

As shipped: agent prose is a document (`agentmessageitem.module.css`, the
`.bubble` wrapper keeps only its layout role; recorded in `decisions.md`'s
2026-09-06 entry). The run label grammar
shipped as `N steps · <last consequential summary>` rather than the
sketched "Read 4 files, edited 2 · 1.8s": the count and the consequential
step survived, the per-run duration did not.

### R10. Enforce it the way colour is enforced

Extend `styles/token-contract.test.ts`:

1. Every `var(--name)` without a fallback must resolve to a declaration
   somewhere under `src/`. This would have caught the four undefined tokens
   in §Bugs.
2. No literal `font-size: <n>px` outside `tokens.css`.
3. `letter-spacing` only through `--tracking-*` tokens.
4. `text-transform: uppercase` only on a class whose name contains `eyebrow`
   or `microLabel`.

Add a `/dev/type` specimen route beside `/dev/widgets`: the ramp, the four
rhythm steps and a 100-character paragraph at each measure, in both themes,
so a ramp change is reviewed as a picture rather than a diff.

## Bugs found on the way

| Site | Problem |
|---|---|
| `panes/session/composer/composer.module.css:65` | `var(--radius-sm)` is not a token; the attachment tile renders square |
| `panes/session/composer/currentwork.module.css:81` | same `--radius-sm` |
| `panes/session/composer/currentwork.module.css:72` | `var(--edge-hi)` is not a token; the link underline falls back to full ink |
| `panes/spawn/MobileSettingRows.module.css:57` | `var(--font-size-title)` is not a token; the caret gets the UA default 16px by accident |
| `panes/session/transcript/messages/steeringitem.module.css:72` | 9px text |
| `panes/session/transcript/tools/delegateStatus.module.css` | 10.5, 11, 11.5, 12, 13px literals (nine rules), a private ramp |
| `panes/session/chrome/activitypanel.module.css:127,190,201,214` | 11px literals |
| `panes/session/composer/composer.module.css:128` | 11px literal, mono, on the image dimensions overlay |
| `widgets/panescaffold` + session title | a whole sentence rendered as an uppercase micro-label (§6 violation) |
| `shell/rail/Rail.tsx:1408-1427` | text-glyph icons outside the subsetted font |
| `index.html` viewport meta | pinch-zoom disabled app-wide |

## Suggested order

1. Bugs + R5 + R6 + R8 copy fixes: small, token-level, no layout risk.
2. R1 + R2: the ramp and the measure, one PR, verified by the layoutguard
   sweep at 390/700/1024/1400 plus a new 1920 leg.
3. R3 + R4 + R7: rhythm, pane headings, spawn/settings layout.
4. R9: tool-call clustering and the agent-side bubble, each with its own spec
   since both touch recorded decisions.
5. R10 alongside 2, so the ramp cannot fragment again.
