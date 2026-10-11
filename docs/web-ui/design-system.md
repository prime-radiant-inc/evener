# Evener Web Hub — Design System & Style Guide (v3)

Status: **current editorial design system**. The canonical
system for the React + CSS Modules frontend under `cmd/evener-hub/frontend/src/`.
The real `/dev/widgets`, `/dev/type`, and `/dev/surfaces` galleries show it in both themes.

The approved [editorial-instrument design](../superpowers/specs/2026-09-09-tufte-webui-design.md)
supersedes the Beautiful UI aesthetic mandate for palette, typography, enclosure and elevation.
It does **not** replace the interaction law, widget APIs, semantic color meanings,
accessibility floors or honest-liveness rules. Source now implements tokens, shared
widgets, typography, inline tool/delegate hierarchy, shell, forms, composer and ledgers.
Some surfaces inherit shared styling rather than a separate redesign; see
[source coverage](#editorial-source-coverage). Release verification is recorded separately
under [acceptance and limits](#acceptance-and-limits); this guide is not a release certificate.

**Provenance and attribution.** The 2026-08-13 system adapted
[Beautiful UI](https://www.beautifului.dev), MIT License, Copyright (c) 2026 Shane Levine.
Its licensed attribution remains at `cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt`.
The interaction patterns and widget inventory below retain that history. Earlier visual
systems (the server-rendered hub, wave-2, Fjord/Ledger, Beautiful UI) are recorded in git
and [decisions.md](decisions.md), not repeated here as competing current mandates.
Source Serif 4 is self-hosted from `@fontsource-variable/source-serif-4` 5.3.0,
SIL Open Font License 1.1; the package notice and license are reproduced below.

---

## Design model: an editorial instrument

**Conversation supports understanding; evidence supports verification; controls support
intervention.** This is our design argument, inspired by Tufte, not a quotation or a claim
of measured usability improvement. Typography and spacing serve that division of work.
The reader should be able to follow a claim, inspect what happened, and act without
losing the conversation that made the evidence relevant.

- **Put evidence beside claims.** A tool row leads with authored intent, then the exact
  action and target, then native code, diff, output or structured evidence. Intent explains
  why; it does not replace the action or prove the result. This adds hierarchy rather than
  another generated summary for the reader to trust.
- **Disclose detail, not consequences.** Collapse bulky evidence to keep a conversation
  readable, but leave failure and actionable status legible. Disclosure inspects in place;
  Open navigates independently. Neither action should accidentally trigger the other.
  Progressive disclosure costs an extra action to inspect detail; it must not conceal
  the reason to inspect it.
- **Keep different facts distinct.** Delegate lifecycle, attention, immutable launch receipt
  and child-authored report answer different questions. A successful launch is not finished
  work; an earlier report is not current activity. Prefer an honest unknown to an inferred
  success. This is less reassuring than a single green badge, but more useful for supervision.
- **Preserve the path into evidence.** Nested transcript inspection retains the owner and
  immediate parent. Back returns to that parent, not an arbitrary focused pane or the root;
  when the exact originating surface survives, retain its identity even if a session and a
  read-only transcript share a ref. Context bookkeeping is the cost of letting a reader
  investigate without rebuilding their place. See
  [Open routing](../../cmd/evener-hub/frontend/src/panes/session/transcript/openTranscript.tsx)
  and [retained origins](../../cmd/evener-hub/frontend/src/shell/workspace.ts).
- **Let filename Open own its placement.** On a settled session route, opening
  or reopening a filename selects its document beside the exact living source
  promoted to main, including a secondary session or read-only transcript.
  The canonical document binding retains that source for Back. Reconnect and
  settled location refresh do not replay the old route over this selection.
  Route reconciliation waits until the opening stack finishes when document
  hydration publishes a reference before the exact source owner arrives.
  New or pending URL intent still takes precedence. Generic documents and
  stale or mismatched source bindings do not override route placement. See
  [document Open](../../cmd/evener-hub/frontend/src/panes/doc/openDoc.ts) and
  [route reconciliation](../../cmd/evener-hub/frontend/src/shell/AppShell.tsx).
- **Return to reading, not a fresh end landing.** The pane-lifetime transcript
  read view retains the source's measured entry and offset while a document is
  open. On readonly remount,
  [ReadOnlyThreadContent](../../cmd/evener-hub/frontend/src/panes/transcript/ReadOnlyThreadContent.tsx)
  admits that capture through the existing transcript registry before scroll
  initialization, including when content arrives after hydration. The shared
  body and VirtualList still own committed measurement and restoration. A
  retained away reader does not acquire end-follow intent from an unmeasured
  port. Fresh readers still land at the latest content, and newer reader
  movement supersedes the retained capture.
- **Reconstruct the host, not the workspace.** Switching from a living phone
  workspace to desktop, including its first desktop mount, restores dockview
  geometry around the exact living pane records, their current slots and the
  latest selection. Document Back owners, captured
  file references and reopen generations stay with those records, outside pane
  params and saved layouts. Phone-created panes survive; closed panes do not
  return from stale geometry. Cold layout restoration creates fresh records and
  honors route intent without resurrecting runtime owners. Programmatic slot
  moves do not count as user selection. DockHost moves only panes whose logical
  slots change, keeping the promoted main isolated and leaving unchanged-slot
  panes in their user-chosen groups. Params, document-binding and pane add/remove
  publications do not collect secondary splits into one group. Cold and live
  restore retain saved group geometry; live slot changes made on phone reconcile
  against saved placements without moving unaffected panes. See
  [desktop recovery](../../cmd/evener-hub/frontend/src/shell/DockHost.tsx).
- **Quiet the frame, not the controls.** Fine rules and aligned columns replace decorative
  enclosure. Fields still look editable, overlays retain boundaries, focus remains visible,
  and semantic color distinguishes attention, activity, failure and selection with text or
  shape as well as hue. Minimalism is not permission to hide an affordance.
- **Optimize readable density, not maximum density.** Prose gets a reading face and measure;
  operations use compact sans; machine evidence uses mono. Larger prose occupies more space
  but separates reading from scanning. Shared size and width preferences remain available.
  Phones reflow speaker rows, controls and evidence rather than shrinking a desktop page;
  wide evidence scrolls within its surface instead of widening the page or shrinking targets.
- **Keep session status inside its pane.** Every visible desktop session pane ends with its own
  flush status footer, never one strip spanning sibling panes. The footer leads with that
  session's working directory and repository/branch, then its Agents, Jobs, Watches and Tasks
  counts. A count click focuses the owning pane before opening the matching Overview category.
  The shared sidebar retains its scope breadcrumb and an accessible, visually hidden
  “Overview kind” label. About is the fifth category and has no footer counter.
  Phones omit the activity footer and the working-directory/repository row. The
  composer ends at the pane's bottom edge with only required safe-area clearance, which
  yields when the on-screen keyboard covers that area.
- **Keep session inspection together.** Both session action menus offer Overview, retaining
  the intended session's category choice. `/status` selects About without toggling a
  standalone Details pane. About reuses the shared Details renderer and its accounting.
  Phones move focus into the full-screen Overview after menu dismissal and contain Tab
  until close. Return to a visible, connected opener, then a visible control for the
  intended session, preferring the originating pane. Desktop remains nonmodal. Existing
  standalone Details views and the `/tasks` pane toggle retain their behavior.
- **Keep inspection readable at larger text sizes.** Desktop Overview stays 320px wide.
  Its category strip wraps without shortening labels or counts at narrow widths and XL
  text. About's model, session ID, branch and paths wrap as selectable text. Its body
  scrolls vertically while the page stays fixed. Phone controls retain the tap floor.
- **Preserve useful saved placements.** Workspace restore omits retired Activity
  panes through its unknown-pane handling. Registered panes keep their bodies and
  reachable tabs; valid session routes win before Welcome. Overview restores its
  own open/category choice independently. The retained recursive Activity Sheet
  and inline discovery keep their existing scope and renderer.

The provenance corrections and nested navigation/focus repairs change behavior. Folding,
disclosure persistence, keyboard rules, preferences and semantic hue roles are retained
contracts, not inventions of this redesign. Other surfaces gain a coherent shared system
without necessarily gaining a new workflow; the coverage section identifies that boundary.

**Review questions.** Can a reader distinguish intent from execution and a receipt from a
report? Is failure visible before expansion? Can they open evidence without toggling its
disclosure, then return to the immediate context? Are editable fields and keyboard focus
unmistakable in both themes? At phone widths and larger text settings, do prose, controls
and wide evidence remain usable without hiding content? Review the real components and
workflows, not only token swatches.

## Inline tools and delegates

**Tools stay in the conversation.** Use the shared
[ToolRow](../../cmd/evener-hub/frontend/src/panes/session/transcript/ToolRow.tsx)
grammar: authored intent first, then action/target and compact result metadata.
Descriptors supply content and native evidence, not independent row layouts.
Keep disclosure separate from file/transcript opening; retain existing folding and
disclosure persistence. Evidence stays in its native code, diff, output or structured
renderer rather than becoming a second prose summary.

**Delegates are durable collaborators, not launch-tool status.** The inline
[delegate renderer](../../cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx)
and [row model](../../cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts)
take lifecycle from the owning thread's stable delegate projection. Keep the immutable
launch receipt separate: a completed launch call does not prove the delegate completed.
Without authoritative owner state, only an explicitly in-flight launch proves activity;
otherwise show unknown rather than infer success or liveness from the receipt or child
transcript. Attention is a separate signal from lifecycle.

Use the current run's start for elapsed time, not the old launch receipt. Child-authored
words remain distinct from machine metadata and may remain visible on resumption; they
do not establish current lifecycle. Expanded activity shows the five most recent authored/activity
items, with full history behind Open transcript. Omit unavailable counts and timing;
distinguish unavailable activity from an empty loaded transcript. These are provenance
rules, not permission to fabricate a summary or rewrite stored evidence.

## Agent cascade

Desktop Agents rows open or reuse a read-only cascade in the secondary pane group.
Keep the original center conversation mounted with its composer and reading position.
The origin conversation never renders in the cascade — no column and no spine —
so a first drill shows only the subagent's conversation, and that lone column
fills the pane. Keep the selected child and immediate parent readable above the
removed origin; earlier ancestors become live 52px spines, and the spines are
the breadcrumb: the pane has no title and no separate path nav. The parent is
400px wide. The leaf flexes with a 440px minimum. Narrow desktop surfaces
scroll horizontally and reveal the selected leaf instead of squeezing either
reader. Parent and leaf transcript scrollers remain independent, each column's
activity controls sit below its transcript content, and the pane body hosting
the spines and columns never scrolls as a unit.

Only ancestor spines use the session list's
[StatusDot](../../cmd/evener-hub/frontend/src/widgets/statusdot/index.tsx):
a turning alive ring for active work, an amber dot for attention and a red diamond
for failure. Idle and ended states have no glyph. Under reduced motion the ring
is the static working dot. Each spine maps its own raw runtime status through
`cadenceStateForStatus`: `systemError` means failure and `closed` means ended;
unknown values remain quiet. Hover and visually hidden text preserve the runtime
label, including unknown states, rather than replacing it with the indicator's
family label. Readable parent and leaf columns keep their full status labels.
Counts, focus and activity ownership remain unchanged.

Ancestor controls open a scoped peek. Keyboard activation of a delegate replaces
the path after that ancestor. Tasks remains inside the peek. One Escape closes
only the topmost peek; the path and sidebar survive. Parent text selection never
retargets the selected-leaf footer or sidebar.

Only explicit drill, pop or return gestures animate geometry. Reconnect, status
and late ancestry updates preserve geometry and focus. Reduced motion makes
geometry changes immediate. Unknown ancestry stays visible rather than inventing
a root or discarding the retained path.

Return closes only the inspector and focuses its exact surviving source lifetime.
A session source receives editor focus without moving its reader. A retired source
is never recreated or replaced by a reused pane ID. Existing saved cascades without
separated-inspection intent retain their recorded Return behavior. Open conversation
opens an independent ordinary session pane. Saved intent preserves the selected
ref, edges, read-only source and origin locator without serializing transcript
payloads or image bytes. At the existing phone breakpoint, a saved cascade shows only its selected
read-only content and Return; ordinary Agents activation keeps transcript
navigation. See the [ownership contract](../product/session-activity.md#automatic-agent-cascade)
and [real Chrome guard](../../cmd/evener-hub/frontend/scripts/cascadeguard/README.md).

## Editorial source coverage

The [approved surface scope](../superpowers/specs/2026-09-09-tufte-webui-design.md#other-surfaces)
and [real-component SurfaceGallery](../../cmd/evener-hub/frontend/src/dev/SurfaceGallery.tsx)
provide the coverage references. Direct source work covers transcript tool/delegate rows,
shell/rail/mobile structure, welcome/spawn/settings/provider forms, composer/queue/AskDock/
attachments, and activity/task/detail ledgers. This extends beyond a token-only restyle.

Documents, read-only transcripts, menus, dialogs and notices inherit shared Markdown,
CodeBlock, PaneScaffold, palette, radius and overlay styling; this is not a separate
interaction rewrite for each surface. Gallery examples and deterministic tests support
source coverage, not a claim that every workflow, theme, viewport or assistive technology
has passed browser acceptance.

## Acceptance and limits

Release snapshot, 2026-09-10: the implementation is integrated and
[PR #1124](https://github.com/prime-radiant-inc/evener/pull/1124) is an open draft, not
merged-main acceptance. Source review, automation and hands-on review are separate evidence:

- **Source and automation:** source changes were reviewed and integrated. The latest
  unchanged browser suite and build passed; the final integrated twelve-gate run and its
  full-output and power-state audits passed. An earlier writer scroll-guard failure was not
  reproduced and remains unresolved; later passes do not explain it.
- **Hands-on panel:** phone and workspace reviewers endorsed their reviewed surfaces.
  The same tools reviewer still owes the mixed-origin native retest; its rejection remains
  open. Source regression tests for nested Open/Back repairs do not substitute for that retest.
- **Preview:** the detached preview still serves an older build; the authorized refresh
  has not happened. It is not evidence for the final integrated implementation.

No perceptual color/leading A/B study, real-device, Safari, assistive-technology or live-provider
validation is claimed. Automated contrast and geometry checks constrain the design; they do
not establish reading comfort or usability. The accessibility gaps in §8 remain documented.

## 1. Direction (the design law)

**Reading before chrome.** Warm neutral paper/ink, not sepia decoration. Dark remains the
default; `system`, `light`, and `dark` preferences keep their existing behavior. A page is
flat, a field is visibly editable, and a floating layer retains a boundary and depth.
Use whitespace, aligned columns and fine rules instead of nested boxes and tinted bands.

**Three faces with distinct jobs.** Source Serif 4 for reading prose and editorial page
headings; Inter for controls, labels, tables and compact operations; JetBrains Mono for code,
commands, paths and machine identifiers. Comparable operational figures use tabular Inter,
not monospace. Keep authored text and machine evidence visibly distinct.

**Geometry is not decoration.** Preserve the shared reading/wide measure, alignment,
font-size preferences, timestamp rail, safe-area/keyboard rules and 899px mobile boundary.
Do not shrink targets or hide overflow as a substitute for containing wide evidence.

**Color is meaning.** Attention = a human/action is needed; alive = active work;
danger = failure/destruction; accent = links, focus and selection. No decorative status
hues, invented activity, speculative progress bars or color-only distinctions.

**Motion is evidence.** Default none. Preserve measured cadence and reduced-motion
behavior. The one continuous motion allowed for agent liveness is `StatusDot`'s
working spinner: it turns only while the owning status authority reports active
work and becomes the static working dot under reduced motion. See §5 for the exact
budgets and exceptions.

**Signature — the cadence instrument (`<Cadence>` widget):** the full activity treatment used where
a surface has room and frame evidence: a state dot plus a 64×10px activity
trace of the last ~60s of frame arrivals as vertical ticks that fade with age. (The plan's
original sketch said 24×10; implementation landed on 64×10 for tick legibility and every
consumer + test pins 64 — recorded here so the doc matches the shipped truth.) Working = fresh
ticks (alive token); quiet = ticks visibly aging to `--ink-low`; needs-you = dot and trailing
edge in attention amber; failed = danger. It never animates on its own — it only re-renders
when frames actually arrive, so a busy agent shows a dense fresh trace and a stalled one shows
honest decay. **A trace with no in-window frames renders no SVG at all** — the dot alone.
Props: `{state: "idle"|"working"|"needs-you"|"failed"|"ended", frameTimes: number[], now:
number}`.

The browser rail uses a deliberately smaller status grammar instead of `<Cadence>` because its
navigation summaries carry no frame trace and every session row must stay one line. Its indicator
is `StatusDot`, with StatusDot's labels: amber dot = Needs you, red diamond = Failed, turning alive
ring = Working. The shape difference keeps needs-you and failed distinct without hue, and the
accessible name carries the state for assistive tech. Precedence is failed, needs-you, working. Idle and ended rows reserve
no indicator. The visible face is indicator, title, and age/actions only. Project,
host, branch, running jobs, subagent tally, watches, tier and age move into a shared `HoverCard` on
the title. Hovering the title or focusing the owning tree row exposes it; on a hoverless device a
long press on the title exposes it and a tap activates the row. Session pin stars
and textual state lines do not appear on the row.

Ancestor spines share this compact renderer, not the rail's status authority.
The rail retains navigation-summary arbitration, including running jobs and
descendants, and maps normalized `errored` and `ended` values through
`cadenceStateFor`. Spines use runtime metadata from their existing scoped activity
binding and the separate raw-status mapper. Sharing the glyph adds no activity
read, subscription or retry owner.

Session context cards float 12px beyond the sidebar's right edge, vertically centered on the
owning row and shifted to keep 8px of viewport clearance. An open card follows the row while
the sidebar is dragged to a new width. When the card cannot fit beside the
sidebar, it opens above or below the row with the same 12px gap. The card stays in the body portal
and does not reflow navigation or take focus. Scroll and viewport resize dismiss it and cancel
pending reveals, including long presses; a fresh gesture reveals it at the new position.
Containment applies when the card's dimensions fit the viewport. Other HoverCards and tooltips
keep their default placement.

---

## 2. Tokens as shipped

`src/styles/tokens.css` is the single source of every color, type, space, radius, and motion
value in the app. Consumers only ever write `var(--name)` — no component CSS branches on
theme; the light-theme block (`[data-theme="light"]`) redeclares every color token under the
same name, and `token-contract.test.ts` (§4) fails CI if a token exists in one theme's block
but not the other's.

**Color** — the warm neutral roles below are implemented in both theme blocks. All existing
token names are retained. The table is a reference; `tokens.css` is authoritative.

| Role | Dark | Light |
|---|---|---|
| Page `--surface-0` | `#191918` | `#FAF9F6` |
| Wells/rail `--surface-canvas` | `#1D1D1B` | `#F1F0EB` |
| Evidence/structural fill `--surface-1` | `#232320` | `#FCFBF8` |
| Floating layer `--surface-2` | `#232320` | `#FCFBF8` |
| Evidence inset `--surface-inset` | `#20201E` | `#F4F3EE` |
| Hover / selected `--hover-1/2` | `#2B2B28` / `#33332F` | `#F0EFE9` / `#E5E4DD` |
| Editable field `--field` | `#292926` | `#F4F3EF` |
| Fine / strong edge | `#34342F` / `#51514A` | `#DDDCD4` / `#B7B6AC` |
| High / medium / low ink | `#F2F1EB` / `#B0AFA6` / `#99998F` | `#252521` / `#5F5F57` / `#6D6D64` |

Card, InspectorCard and PaneScaffold share the page ground rather than manufacturing raised
surfaces. Evidence and overlays still have their own grounds. Fine borders are structure,
not text. Low ink clears 4.5:1 on page and evidence surfaces: dark 6.12/5.48, light 4.96/5.05.
Readable secondary metadata uses medium ink; low ink stays placeholders, disabled and timestamps.

The four semantic hues keep their names, roles, 15% OKLab `-bg` washes and 40% `-edge`
companions. Text uses `-ink`, never the bare hue. Light alive/danger/accent text colors are
`#12763B` / `#C51D23` / `#0064C2`, darkened to preserve AA on their own warm tinted grounds.
The tests compute every semantic pairing, including each hue's own wash, at ≥4.5:1.
Tooltips retain their inverted mini-palette. ANSI roles and values are unchanged.

Diff notation remains independent of semantic status. Dark add/delete stays `#19251A` /
`#170B17`; light is `#E9F4EE` / `#F5EAF0`. Background contrast against the page stays within
1.05–1.2, content ≥4.5, marker ≥3, with additions lighter in grayscale and explicit +/− signs.
The PWA manifest and HTML theme-color are synchronized to the dark page `#191918`.

**Type** — three shared families, no CDN font requests:

- `--font-sans`: `"Inter Variable"` plus system sans fallbacks. Controls, labels, operational tables.
- `--font-prose`: `"Source Serif 4 Variable", Georgia, "Times New Roman", serif`. Reading and page headings.
- `--font-mono`: `"JetBrains Mono Variable"` plus system monospace fallbacks. Machine evidence.

Inter and JetBrains Mono remain wired to their package Latin variable WOFF2 files. Source Serif 4
uses the package's actual exported normal CSS and `wght-italic.css` for real italics; all assets
are served locally by Vite/the built frontend, with package Unicode-range subsetting.

The existing `--font-size-caption/ui/body/pane-title/page-title/display` ramp remains
12/13/15/18/22/28px. **`--font-size-prose: calc(18px * var(--font-scale))` is new**, declared on
`body` alongside that ramp so S/M/L/XL (0.9/1/1.1/1.25) scale reading text too. Phone body remains
16px; editable phone controls use `max(16px, var(--font-size-body))`, even under S. The serif
reading step is 18px at M in both viewports. Weights are 400/500/600; leading body/ui/title is
1.6/1.4/1.25; display tracking remains −0.02em.

Markdown keeps the prose face but composes with its host's size (the root falls back to `1em`);
reading surfaces pin `--prose-font-size: var(--font-size-prose)` on the host that owns their
prose, and `--markdown-ink` stays the ink hook. Inline code stays JetBrains Mono at 0.86em with a quiet underline; fenced code
explicitly keeps the mono face. Markdown tables use Inter at body size, tabular figures,
wrapping labels and fine horizontal rules. PaneScaffold and EmptyState headings use the serif;
buttons, fields, dialog labels and other compact operational headings do not inherit it globally.
`faces.test.ts` pins the new contract and the unchanged operational sans rules; `/dev/type`
shows all three faces (including real serif italic), the ramp and both measures.

**The eyebrow recipe.** `--tracking-eyebrow` (0.06em) replaced `--tracking-micro` and the four
other tracking values that were in use, and it is now THE uppercase tracking in the app. The
recipe it backs is one rule with no variants: `--font-size-caption`, `--font-weight-medium` (or
semibold where a header band wants more), `--ink-mid` or darker, `text-transform: uppercase`,
`letter-spacing: var(--tracking-eyebrow)`, at most two words. It is reserved for short labels inside a
page (RecommendationCard's kicker and existing rail/settings section labels); InspectorCard
and Table/DiffTable headers now remain sentence-case. An eyebrow is NEVER a pane title: a PaneScaffold title
is the page's own heading, set sentence-case at `--font-size-pane-title`, semibold, `--ink-hi`
(§6). Literal `font-size` values outside `tokens.css`, `letter-spacing` outside the
`--tracking-*` tokens, and any uppercase rule that is not a complete eyebrow are all enforced by
`src/styles/token-contract.test.ts`.

**Faces and figures.** Mono is for machine text and nothing else: code, paths, shell command
summaries, diffs, and the identifiers in the Details panel. The chrome a reader looks at
constantly stays on the sans face with `font-variant-numeric: tabular-nums`, which buys column
alignment without switching faces: the model chip (`chrome/modelswitch.module.css:.value`), the
status row's percent/clock/queue figures, the rail's relative ages, and the turn footer.
`src/styles/faces.test.ts` pins those four rules off disk.

**Space & shape** — `--space-1` through `--space-9` (4/8/12/16/24/32/40/48/64px — IBM Carbon
Design System's own spacing progression, adopted because it's the only well-known scale that
exactly fits the plan's stated endpoints (4, 64) and step count (9) while staying on the 4px
grid; unchanged by the re-theme). Radii are restrained: `--radius-chip` 3px,
`--radius-control` 4px, `--radius-pane` 4px and `--radius-pill` 999px for switch tracks.
Structural Card/InspectorCard/PaneScaffold and tables are square; floating layers and fields
retain their token radii. Existing widget dimensions and touch targets are unchanged.

**Vertical rhythm** is four named steps over that same grid, so the transcript has a vocabulary
for how far apart two things sit: `--rhythm-line` (4px, inside one item, an intent line above
its call), `--rhythm-item` (8px, between items in a run of tool calls), `--rhythm-group` (16px,
between a run and the next speaker header, and above a turn footer) and `--rhythm-exchange`
(24px, above a user message). `src/styles/rhythm.test.ts` pins each step to the site that names
it.

**`--session-measure` is the app's one reading measure**: 44rem (704px; actual character count depends on the face and preference), declared on `<body>` and raised to 64rem under
`<body data-transcript-measure="wide">`. Settings → Theme → Transcript width is what writes that
attribute, through `stores/prefs.ts`. The transcript column, the composer, the cold start, the
spawn form and the settings content all read the same token, so they widen together and can
never drift apart the way a hand-copied literal did. The layoutguard case `transcript-measure`
fails when a plain agent paragraph runs past 100 characters per line at 1440 or 1920, or when
the column is not centred in its pane. Pane bodies pad `--space-5` on desktop and `--space-4`
below 900px (`panescaffold.module.css`).

**Motion** — `--motion-duration-attention` (200ms), `--motion-duration-overlay` (120ms),
`--motion-duration-hover` (150ms, NEW — color/background/border/shadow transitions on
hover/focus/press for interactive chrome; transitions name their properties explicitly, never
`transition: all`), `--motion-easing-standard` (`ease-out`). See §5 for the budget these back.

**Focus rings** — `--focus-ring` (2px solid accent) and `--focus-ring-danger` (2px solid danger).
Every interactive widget gets `outline: var(--focus-ring)` on `:focus-visible`; `--focus-ring-danger`
applies to destructive controls (a red cancel button's ring, for instance). The outline-offset
varies per site (positive, outside roomy controls; negative, for rows flush inside a clipping
container). One exception: `shell/palette/commandpalette.module.css` keeps a deliberate quiet
`2px var(--accent-edge)` ring on `.input:focus` — a full-strength `--focus-ring` on an actively-
typed text input is louder than the palette wants. Hand-rolled ring geometry (bare `px` + `solid`
outlines in any order/unit, the outline longhands, or the retired inset-ring `box-shadow` hack) is
contract-banned; see §4. Dropzone's dashed drag-target outline is signage, not a focus ring, and
holds the second (and last) contract exception.

**Stacking order** — The app's own z-index values are tokenized to keep stacking context
predictable. The ladder: `--z-raised` (1, local resize handles/overlay controls within a pane),
`--z-sticky-bar` (20, pane-level pinned bars), `--z-dialog` (1000, modal dialogs + scrim),
`--z-menu` (1020, menus, popovers, anchored floats), `--z-tooltip` (1030, tooltips beat menus),
`--z-toast` (1040, tops the app's own ladder). Raw z-index integers are contract-banned; see §4.
dockview's stylesheet sits outside the ladder: its drag overlay is 999 (`--z-dialog`'s 1000
clears it deliberately) and its drop-target container is 9999, which sits above everything for
the duration of a dock drag.

**Elevation** — `--shadow-card: none` in both themes. Keep the token for existing consumers,
not as a reason to reintroduce raised structure. Card uses padding and page ground;
InspectorCard uses a sentence-case label and fine property rules; PaneScaffold uses a page
heading and separating rule without a rounded outside border or tinted header. Table/DiffTable
use horizontal rules rather than framed grids. Do not remove a field's affordance along with
card decoration: Input, Select, Textarea and shared picker triggers keep a strong 1px border,
`--field` fill and `--shadow-inset-field`, with unchanged focus and phone sizing.

`--shadow-overlay` still embeds a 1px strong-edge ring and soft per-theme shadow for menus,
popovers, toasts, dialogs and sheets. Do not double that same edge with another border.
`--shadow-color` supports sheets' directional variants. Tooltips retain their inverted palette
and boundary. Flattening structure never means erasing a modal or interactive layer.

---

## 3. Widget inventory (locked API)

Every widget lives at `src/widgets/<name>/` (`index.tsx` + `<name>.module.css` +
`<name>.test.tsx`), is re-exported from the controller-owned barrel `src/widgets/index.ts`, and
has a gallery section at `src/dev/gallery-sections/<name>.tsx` showing every documented state in
both themes (enforced by `src/dev/WidgetGallery.test.tsx`'s completeness test — see §4).
`src/widgets/internal/` (currently just `requireClass`, a CSS-Modules type-safety helper) is
implementation machinery, not a widget: no gallery section, not in the barrel.

Every widget's public component and types are exported through that barrel,
`src/widgets/index.ts`; VisuallyHidden, Banner, MermaidDiagram, HoverCard, and the CloseIcon and
DirectoryIcon glyphs are in it like the rest. Widgets that compose a sibling
import it from its directory (`../button`), never through the barrel, because the barrel
re-exports them and would form a cycle. Many app modules still import widget directories and
widget-internal modules (`internal/requireClass`, `toast/store`, `disclosure/disclosureStore`)
directly; the barrel is the documented import path, not yet an enforced one.

This table is the actual shipped surface, not the plan's original sketch — a few shapes evolved
during implementation (noted inline); this table is the one to trust.

| Widget | Props | Notes |
|---|---|---|
| **Button** | `{variant?: "primary"\|"secondary"\|"quiet"\|"danger"\|"dangerQuiet"\|"link"; size?: "xs"\|"sm"\|"md"; icon?: ReactNode; children: ReactNode; onClick?; disabled?; type?: "button"\|"submit"\|"reset"; wrap?: boolean} & Omit<ButtonHTMLAttributes, those>` | `forwardRef<HTMLButtonElement>`; spreads unrecognized native attributes (aria-\*, data-\*, id, ...) onto the `<button>` — `className` stays computed-only, never caller-overridable. An `aria-disabled` refusal (either spelling) swallows the click while keeping focus; `isAriaRefusal` is exported for IconButton, which refuses the same way. `wrap` lets a long label break across lines (the welcome pane's "Jump back in: <session title>"): the size's height becomes a floor, the phone tap floor still applies, and `variant="link"` ignores it. Full width is the caller's layout (a flex column or grid stretches the button), not a prop. `variant="link"` is the inline text action (a task title in the composer's current-work line, a Retry beside an error, a "Show full note" toggle with `aria-expanded`): still a real `<button>`, in `--accent-ink` with an underline, font inherited from the surrounding text, no height or padding, `size` ignored; it inherits the surrounding `white-space`, so in a nowrap line a long label ellipsizes inside its own box and in prose it wraps; the focus ring is Button's. The canonical exemplar every other widget's file layout/CSS/test style mirrors. |
| **IconButton** | `{label: string; icon: ReactNode; variant?: IconButtonVariant; size?; onClick?; disabled?; type?} & Omit<ButtonHTMLAttributes, those \| "aria-label">` | Icon-only Button; `label` is required and becomes `aria-label` (no visible text). `IconButtonVariant` is Button's variants minus `link` (no text to underline). `forwardRef` + rest-spread, mirroring Button — reuses Button's CSS classes directly (read-only import), which does NOT carry over ref-forwarding/prop-spreading, so this is fixed independently. An `aria-disabled` refusal (either spelling) swallows the click and keeps focus, exactly as Button's does; both use Button's `isAriaRefusal`. |
| **CopyButton** | `{text: string; label?: string ("Copy"); variant? ("quiet"); size? ("xs")} & Omit<IconButtonProps, "label" \| "icon" \| "onClick">` | The standard copy-to-clipboard control: an IconButton with a copy glyph whose name and glyph switch to "Copied" for 2s after a successful write. Pass a specific `label` ("Copy output", "Copy raw JSON") so screen readers say what it copies. A missing `navigator.clipboard` or a rejected write is a silent no-op. |
| **OpenButton** | `{label?: string ("Open"); onClick?; tabIndex?; describedBy?: string; href?: string; word?: string ("open")}` | The standard "open out of this surface" affordance, one treatment everywhere: a quiet `sm` IconButton carrying the box-arrow **OpenIcon** glyph (exported alongside), tooltip "Open", accessible name `label` — keep it specific ("Open transcript", "Open beside: src/a.ts"), since many open controls share a screen. `href` renders an `<a>` instead (new tab, no opener, no referrer) showing `word` before the glyph — settings' "open in editor". Owns `stopPropagation` because it always rides something clickable (a disclosure head, a tool row, a tree row). |
| **Cadence** | `{state: "idle"\|"working"\|"needs-you"\|"failed"\|"ended"; frameTimes: number[]; now: number}` | The signature widget — see §1. Pure (no timers, no `Date.now()`); ticks render as SVG `<rect>`s, age→opacity in 4 buckets (15s each, half-open `Math.floor` boundaries); needs-you tints the freshest ticks amber too ("trailing edge"), not just the dot. |
| **Chip** | `{children: ReactNode; tone?: "neutral"\|"attention"\|"alive"\|"danger"; onRemove?: () => void}` | Small labeled pill; `onRemove` renders a remove button, `aria-label` derived from string children or `"Remove"`. |
| **Badge** | `{count: number; tone?: "neutral"\|"attention"\|"alive"\|"danger"}` | Numeric count indicator, caps display at "99+". |
| **StatusDot** | `{state: CadenceState}`; also exports `STATUS_LABEL: Record<CadenceState, string>` | The one session/agent status indicator (imports `CadenceState` from Cadence, doesn't redeclare it) — for tighter contexts than Cadence's full trace: rail rows, pane tabs, ancestor spines, the command palette, delegate rows. Carries its own accessible name since nothing else labels it standalone; the names (`Idle` / `Working` / `Needs you` / `Failed` / `Ended`) are the one status vocabulary, exported as `STATUS_LABEL` for text that names a session's state (a hover card's status line). `working` is an activity indicator: an open `--alive` ring that turns continuously (`--motion-duration-activity`, linear), the one continuous motion sanctioned for agent liveness (§5); under `prefers-reduced-motion: reduce` it is the static alive dot. `failed` is a `--danger` diamond (`data-shape="diamond"`, a 7px square turned 45°) so it never differs from needs-you by hue alone (§1); every other state is a round dot (`data-shape="dot"`: `--attention`, `--ink-low`). Callers that show only signal states (working / needs-you / failed) gate rendering themselves (`SIGNAL_STATES` in `shell/sessionStatus.ts`). |
| **FailureGlyph** | none | The `--danger` ✗ that marks something as failed, sized to sit on a line of text. A widget because `--danger` may only be referenced from an allowlisted widget stylesheet; carries its own accessible name ("Failed") since it is often a row's only failure signal. Passive: no interaction, no focus. |
| **SteeringGlyph** | none | The hollow ◇ that marks a system steering message, drawn as SVG because the subsetted Plex Sans has no U+25xx glyphs. Decorative (`aria-hidden`) — the row's own text says "System steered: …" — and inherits currentColor. |
| **Timestamp** | `{value: number; now: number}` (epoch ms) | A relative time ("5m ago", "now" under 10s or for future/clock-skew values) in a `<time>` whose native `title` carries the absolute instant (time-only same day, date+time across a day boundary) and whose `dateTime` is ISO. Pure render like Cadence and Loader: the caller owns `now`; a NaN `value` or `now` renders nothing. |
| **Meter** | `{label: string; value: number; max: number; tone?: "neutral"\|"attention"\|"alive"\|"danger"}` | `role="meter"`; `label` is required (not optional as an early sketch had it) since role=meter needs an accessible name and a Meter can't ship without one. Fill width via a `--fill` style custom property, not an inline style rule. |
| **Skeleton** | `{lines?: number}` (default 3) | Static bars, no shimmer (honest-liveness rule) — announces "Loading" once for AT; bars themselves are decorative. |
| **EmptyState** | `{title: string; hint?: string; action?: ReactNode; size?: "default"\|"display"\|"compact"}` | `size="display"` sets the title at `--font-size-display` for the one pane whose empty state IS the page (Welcome); `size="compact"` is the empty message inside a list or section (a settings collection with nothing in it, the activity sidebar's notes, the command palette's "No matches"): left-aligned, the collection row's padding, title in sans `--font-size-ui` `--ink-mid`, hint at caption in `--ink-low`; everything else keeps the centered `--font-size-pane-title` block. `action` is optional (an early plan sketch showed it required; a pane with nothing actionable — e.g. a read-only empty log — is an ordinary case, and every sibling slot-style prop this wave is optional, so this was kept optional as the more consistent, more correct shape). Not a loading state: "Loading…" is `Skeleton`/`Loader`. |
| **Table** | `{columns: TableColumn<Row>[]; rows: Row[]; rowKey(row); sortKey?; sortDir?: "ascending"\|"descending"; onSortChange?; filters?: {key,label,active}[]; onFilterToggle?; empty?}` | Controlled sort + filter; semantic `<table>`, `aria-sort` on sortable headers, filter chips compose Chip, horizontal overflow scrolls inside the widget. Ported from Beautiful UI's Records/Filter Table. |
| **DiffTable** | `{columns: {key,label}[]; rows: {key; cells: Record<string,{value; proposed?}>}[]}` | Tabular proposed edits: struck-through old value beside the new one on the neutral `--diff-add-bg` wash — same hue-gate exemption as DiffBlock. Ported from Beautiful UI's Diff Table. |
| **Loader** | `{label?; rail?; startedAt?; now?}` | Indeterminate-wait indicator (pixel grid + mm:ss elapsed); prop-driven like Cadence, no internal timers. Its animation is the sanctioned exception for user-initiated waits — never agent liveness — and lives entirely inside the reduced-motion gate. `rail` seats the grid in the transcript's icon-rail slot (the content-free "Thinking…" row's status glyph). Ported from Beautiful UI's Loading State. |
| **InsightCard** | `{insights: {title; body; series?: number[]}[]; page; onPageChange}` | Paged insights with an inline SVG sparkline (aria-hidden + visually-hidden min/max alternative); pagination composes IconButton. Ported from Beautiful UI's Insight Cards. |
| **RecommendationCard** | `{title; body; confidence?; onAccept?; onReject?; alternatives?}` | Agent-suggested action: micro-label eyebrow, confidence meter (`--accent` on `--field`; not the hue-gated Meter), Accept/Dismiss compose Button. Ported from Beautiful UI's Recommendation Card. |
| **ContextCard** | `{source; snippet; meta?; href?}` | Retrieved-knowledge chunk: inset card, ToolIcon source glyph, 3-line snippet clamp; renders as a link when `href` given. Ported from Beautiful UI's Context Cards. |
| **InspectorCard** | `{title; properties: {key; label; value; options?; onChange?}[]}` | Property inspector: sentence-case section label, hairline rows, editable rows compose Select, read-only values in mono. Ported from Beautiful UI's Fine-tune inspector. |
| **Card** | `{children: ReactNode}` | Passive flat section; padding and page ground, no shadow or outside border. |
| **PromptCard** | `{field: ReactNode; leading?: ReactNode; actions?: ReactNode; controlsTestId?; "data-testid"?; hidden?: boolean}` | The app's one prompt surface — the session composer and the spawn form both render it: a bordered card holding `field` (a `seamless` autoGrow Textarea) over a control row (`leading` slot, spacer, `actions` cluster). The card owns the border, padding and the `:focus-within` ring, so the field inside must draw neither; no state, no opinion about verbs. `hidden` also makes the card `inert`. |
| **Input** | `{value: string; onChange; placeholder?; disabled?; required?; autoComplete?; type?: "text"\|"password"\|"email"\|"search"\|"number"\|"tel"\|"url"; id?; name?; "aria-label"?; "aria-describedby"?}` | Controlled only; labeling is the consumer's job via `<label htmlFor>`. `aria-label` names a field with no visible label (the settings filter, an env-add row) in place of a hand-made visually-hidden `<label>`. |
| **Textarea** | `{value: string; onChange; placeholder?; disabled?; autoGrow?: boolean; rows?; minLines?; id?; name?; onKeyDown?; onFocus?; onBlur?; onPaste?; "aria-label"?; seamless?: boolean; monospace?: boolean}` | `forwardRef<HTMLTextAreaElement>`. `autoGrow` measures the rendered `scrollHeight` after each value change (so wrapped lines grow it too), clamped to half the viewport height. `minLines` sets the resting height in lines. `seamless` drops the field's own box and focus ring for a card that draws one (PromptCard), which must then own the focus affordance. `monospace` sets the mono face for text edited as code or config (the AGENTS.md editor, a credential's JSON); only the face changes, so the field keeps its control-size floor on phones. |
| **Select** | `{value: string; onChange; options: {value; label; disabled?: boolean}[]; disabled?; id?; name?}` | Native `<select>`, restyled — no custom listbox (the richer pickers, PathField and ModelCatalog, own their own combobox panels). A `disabled` option is listed but cannot be picked (an offline host in Spawn's host picker); say why in its `label`, since a native option has room for nothing else. |
| **SegmentedControl** | `{label: string; hideLabel?: boolean; value: T; options: readonly {value: T; label; accessibleLabel?; disabled?}[]; onChange(value: T); disabled?; size?: "sm"\|"md"; fullWidth?; id?; "aria-describedby"?}` (`T extends string`) | Two-to-six concise choices; horizontal radiogroup of native buttons with roving focus, neutral selected state, `md` default, and optional full-width track. `hideLabel` drops the visible label and names the group with `aria-label` instead. |
| **RadioGroup** | `{label: string; value: string; onChange(value: string); options: {value; label; accessibleLabel?; disabled?}[]; disabled?}` | Custom `role="radio"` buttons under an always-visible legend that names the group (required). Roving tabindex keeps the group one Tab stop; arrow keys move focus AND check, wrapping and skipping disabled options (the APG radiogroup bindings, unlike Menu/Tree's move-then-Enter); Home/End jump to the ends. |
| **Disclosure** | `{summary: ReactNode; children: ReactNode; disabled?; "data-testid"?} & ({id: string; defaultOpen?; open?: never; onOpenChange?: never} \| {open: boolean; onOpenChange(open: boolean); id?: never; defaultOpen?: never})` | Native `<details>/<summary>` with persistent store-backed or controlled state; disabled summaries are inert, removed from the tab order, and attenuated without dimming an open body. |
| **Switch** | `{checked: boolean; onChange: (checked: boolean) => void; pending?; disabled?; label: string}` | `role="switch"` on a real `<button>`, not a styled checkbox; `label` is required and always-visible, wired via `aria-labelledby`. `pending` (the switch's own activation started the work) is an `aria-disabled` refusal that keeps focus on the switch; `disabled` is the native attribute, inert and out of the tab order. |
| **FormRow** | `{label: string; htmlFor: string; help?: string; error?: string; children: ReactNode}` | The standard settings-field row: a `<label htmlFor>` naming the caller's own control in `children`, then a help line, or instead an `error` line (`role="alert"`, error-state styling). The messages' ids are `${htmlFor}-help` / `${htmlFor}-error` for the caller to wire `aria-describedby`. Not for Switch/RadioGroup, which carry their own visible label. |
| **CollectionEditor** | `{label: string; items: readonly T[]; getKey; renderItem; removeLabel(item); onRemove(item); emptyMessage: string; inheritedItems?; addPlaceholder?; addButtonLabel?; renderAddField?; onAdd(value): CollectionAddResult \| Promise<…>}` (generic over `T`) | The add-row/remove-row list editor for settings lists. `items` is fully caller-owned; the widget owns only the add draft, inline error and busy state. `onAdd` gets the trimmed non-blank value and resolves `{ok: true}` (clears the field) or `{ok: false, error}` (keeps it, shows the error). `onRemove` fires immediately with no built-in confirm — gate it behind a ConfirmDialog. `inheritedItems` render below as grayed "(default)" rows with no remove button. |
| **PathField** | `{value; onChange; kind?: "dir"\|"file"\|"outputFile" ("dir"); complete(prefix, includeFiles); directory?: {validatePath; createDirectory}; listRecents?; fallbackDir?; onPanelClose?; placeholder?; disabled?; ariaLabel?; id?}` (`directory` required unless `kind` is a file kind) | The closed state is the trigger: a control-shaped button showing the path (or `placeholder`, default "(default)") plus a chevron. `kind="dir"` opens the shared DirectoryPicker dialog; the file kinds open a completion popover that keeps literal-path entry. Wire-free: every filesystem operation is injected. Pass the field's name as `ariaLabel` so the accessible name says which field it is — see "Directory selection" below. |
| **DirectoryPicker** | `{value: string; fallbackDir?; complete(prefix, includeFiles); listRecents?; validatePath(path, kind); createDirectory(path); onPick(path); onClose()}` | The one directory-selection dialog, normally reached through `PathField kind="dir"`. Browsing, typing and New folder change a local draft; only "Use this folder" commits, once, through `onPick`, and only a validated directory can be picked. Callers key it by the committed value. Full contract under "Directory selection" below. |
| **ModelCatalog** (+ **ModelCatalogPanel**) | `{value: string; onChange(qualified); loadCatalog(): Promise<ModelCatalog>; revision?; onPickEntry?(entry); emptyLabel? ("(default)")}`; panel: `{loading; error; catalog; value; onPick(entry); variant?: "default"\|"sheet"}` | The rich model picker. `value` is a qualified `"provider/model"` or `""` for the harness default; `loadCatalog` is injected, so the widget is wire-free. The closed chip is the trigger and opens the panel in a Popover: an ARIA 1.2 combobox whose focus never leaves the search input, over one list of Recent, provider groups, and a dim line per unreachable provider. `ModelCatalogPanel` is the content alone, for a caller with its own trigger; `variant="sheet"` is the mobile bottom-sheet form (no search input, 48px rows). `ModelCatalog` is also the exported catalog shape type. |
| **Dropzone** | `{children: ReactNode; onFiles(files: File[]); disabled?}` | A generic drag-and-drop file target around `children`: calls `onFiles` with every dropped File in order, shows an active state while a drag is over it. No type/count/size opinion — the caller filters. Pointer-only by nature, so every caller must also offer a keyboard-reachable file-picker button. |
| **KeyHint** | `{keys: string[]; compact?: boolean}` | One `<kbd>` per key, "+"-separated; the literal key name `"Mod"` renders as ⌘ on Apple platforms, `Ctrl` elsewhere. `compact` renders one bare glyph run (⇧↵) for a hint inside another control (a Send button), with the same words visually hidden so the enclosing name is unchanged. `chordLabel(keys)` is exported alongside. |
| **Menu** | `{trigger: ReactNode; items: MenuEntry[]; triggerTabIndex?: number; variant?: "default"\|"quiet"}` (`MenuEntry` = `{id; label; onSelect; disabled?}` \| `{kind: "separator"; id}`) | Trigger + popup; roving tabindex among items (skipping disabled), no typeahead. Popup `role="menu"` gets `aria-labelledby` pointing at the trigger `<button>`'s own id (fix-wave — see §4). Traps focus (`FocusScope trap`). `triggerTabIndex={-1}` when nested in a roving-tabindex row; `variant="quiet"` drops the trigger's border for dense rows. |
| **Popover** | `{open; onClose; trigger: ReactElement; children; autoFocus? (true); closeOnScroll? (true); stretchTrigger? (false); "data-testid"?}` | The shared floating panel: `trigger` renders in flow and anchors a panel portaled to `document.body` at a fixed position (flip-then-clamp, shared with Menu), so opening never reflows the page. Outside click, Escape, and (unless `closeOnScroll={false}`) scroll or resize call `onClose`; toggling from the trigger is the caller's job. `autoFocus={false}` keeps focus in an anchoring input (the combobox pattern). |
| **Dialog** | `{open; onClose; title; children; footer?; size?: "default"\|"large"}` | Modal: centered, 120ms fade-scale, Escape/scrim-click close, trapped + restored focus. Shares its whole contract with Sheet via the internal `OverlayPanel`. `size="large"` fills almost the whole viewport for lightbox-style content. |
| **ConfirmDialog** | `{open; title; children; confirmLabel: string; cancelLabel? ("Cancel"); destructive? (true); busy? (false); onConfirm(); onCancel()}` | A Dialog pre-composed for "every destructive action confirms": body copy plus a Cancel / confirm-verb footer, danger-toned unless `destructive={false}` (a consequential but non-destructive action like Install). Controlled — key `open` off one "what's pending" state. Escape and scrim-click call `onCancel`, never `onConfirm`; `busy` disables both buttons against a double submit. |
| **Sheet** | `{side?: "right"\|"bottom"\|"left"; size?: "standard"\|"wide"; open; onClose; title; children; footer?; expandable?: {peekHeight: number; fullScreenFirst?: boolean}; panelClassName?; bodyClassName?}` | Same contract as Dialog (shared `OverlayPanel`); only geometry/slide-in animation differs. `bodyClassName` lands on the sheet's own body element, which is how the sessions drawer renders the Rail flush inside the sheet instead of as a bordered box nested in a bordered box. `expandable` adds a drag handle and a peek/full geometry (drag past a threshold or tap to toggle; `fullScreenFirst` opens full every time). |
| **FocusScope** | `{trap?: boolean; autoFocus?: boolean (true); children}` | The focus-management primitive Dialog/Sheet/Menu build on: moves focus in on mount, restores on unmount; traps Tab/Shift+Tab when `trap`. `autoFocus={false}` skips both the move-in and the restore, for a popover whose focus must stay in its anchoring input. Does not (yet) set `inert` on anything outside the scope — see §4. |
| **Tooltip** | `{label: string; children: ReactNode}` | Hover/focus-triggered, 300ms delay, neither triggered on touch (a tap fires both and dismisses neither) and hidden there via CSS. `aria-describedby` wired via `cloneElement` onto a single-element child — works for a native element or any widget that forwards a ref + spreads rest props (Button/IconButton both do, since the fix-wave in §4). |
| **HoverCard** | `{label: ReactNode; children: ReactNode \| (association) => ReactNode; focusTarget?: () => HTMLElement \| null; longPressEnabled?: boolean; sideAnchor?: () => SideAnchor \| null}` (`HoverCardProps`, barrel-exported) | Rich, non-interactive hover/focus description on the shared floating-bubble lifecycle. Use for structured context that would overload a compact surface; function children place `aria-describedby` on the actual trigger, while `focusTarget` can associate and trigger from an owning composite widget. On a touch device neither hover nor the focus a tap takes reveals it; `longPressEnabled` adds a long press that does, a plain tap activates the trigger's own control, and keyboard or assistive-tech focus still reveals it. `sideAnchor` supplies a caller-measured side placement; without it placement is unchanged. |
| **Toast** + `useToasts()` | `useToasts(): {push: (kind, text) => void}`; `<Toast/>` takes no props | Module-singleton queue (`useSyncExternalStore`), mounted once near the app root. 5s auto-dismiss, true pause/resume on hover (tracks remaining time, doesn't restart the full window — fix-wave, see §4). |
| **Banner** | `{tone: "attention"\|"danger"; message: string; action?: {label; onClick; inFlight?}}` | An overlay status strip for "the connection needs a human" moments (ConnectionBanner composes it): absolutely positioned full-width at the top of its positioning ancestor, so it floats over content instead of shifting layout — the ancestor must establish a positioning context. A polite `role="status"` live region; `inFlight` disables the action and appends "…" to its label. |
| **PaneScaffold** | `{title?; mobileTitle?; paneId?; publishTabTitle?; focused?; focusSelector?; scaffoldMarker?; cadence?; actions?; footer?; edgeFooter?; bodyScroll? (true); children}` | The standard pane chrome: header (title + cadence slot + actions) + scrollable body + optional padded footer + optional flush `edgeFooter`. `title` is omitted where the host already labels the pane (the cascade's tab); `mobileTitle ?? title` is published to the mobile top bar. `bodyScroll={false}` is for content that scrolls itself, and also drops the body's padding. Most-copied layout primitive in the app. |
| **CodeBlock** | `{text: string; copyText?; language?: string; ansi?: boolean; showLineNumbers?: boolean; copyLabel?; renderLine?(line, lineNumber); fold? (true)}` | Mono block with a CopyButton inset top-right (`copyLabel` names it, `copyText` overrides what it copies); no syntax highlighting (YAGNI this wave). Long lines wrap, never scroll sideways. Long content folds to its tail behind a "Show N earlier lines" control unless `fold={false}`, for a block that is the primary thing the reader opened. `ansi` renders SGR styling. |
| **ShellCommandBlock** | `{command: string; copyText?: string}` | A shell command as a CodeBlock: formatted across lines with indentation and token-kind spans, never tail-folded, with a "Copy command" button that copies the raw `command` (or `copyText`, for a caller that displays a modified command). |
| **Markdown** (+ **MarkdownBubble**) | `{source: string; live?: boolean; ref?: Ref<HTMLDivElement>}`; bubble: `{source; density?: "default"\|"compact"; dataTestId?}` | `marked` → DOMPurify-sanitized HTML → `innerHTML`; fenced code renders through CodeBlock's stylesheet; links open in a new tab with no opener access; mermaid fences render through MermaidDiagram. `live` closes constructs left open at a streaming tail so formatting renders mid-stream — settled renders must not pass it. MarkdownBubble wraps a Markdown in a bubble frame. |
| **MermaidDiagram** | `{source: string; renderTimeoutMs?: number (10000)}` (`MermaidDiagramProps`) | Renders one model-authored mermaid diagram inline through the two-layer sanitize in `security.ts`, themed to the resolved scheme. Once rendered the container is a button that opens the fullscreen DiagramViewer; while loading it is an inert box, and a failed or timed-out render falls back to the source in CodeBlock styling plus "Couldn't render this diagram." Exported from the barrel like every other widget; Markdown, a sibling, still imports it from `../mermaid`, and the mermaid guard entry (`src/dev/mermaidguard-entry.tsx`) imports the directory so its standalone bundle does not pull in the whole barrel. |
| **DiffBlock** | `{unified: string}` | Per-line domain notation on already-diffed text (dedicated add/remove tints plus `+`/`−` markers); does not compute a diff itself. |
| **Tree** | `{nodes: T[]; onActivate; onToggle; renderRow; releaseModifierKeys?: boolean; isSelected?: (node) => boolean \| undefined}` (generic over `T extends {id; children?; expanded?}`) | Keyboard-navigable (`role="tree"`), roving tabindex, Up/Down/Right/Left/Enter. Fully controlled — `renderRow(node, {depth, expanded, hasChildren, toggle, activate})` owns each row's visible content; Tree owns structure/ARIA/keyboard path only. `isSelected` adds `aria-selected`; `releaseModifierKeys` lets Alt-held arrows and Alt+Home/End through to the desktop global chords. |
| **VirtualList** | `{count; estimateSize; renderRow; dynamic?; getItemKey?; onChange?; onLayout?; anchorToEnd?; ref?: Ref<VirtualListHandle>}` | Wraps `@tanstack/react-virtual`; `ref` exposes `{scrollToIndex, getScrollElement, isLayoutCurrent, getVisibleRange, scrollToOffset?, isMeasurementReady?}` via the React 19 ref-as-prop pattern (not a `forwardRef` wrapper). Sizes come from `estimateSize` alone unless `dynamic` opts in to `measureElement`. `getItemKey` keys rows by identity, needed by any consumer that prepends; `anchorToEnd` keeps a live-append list pinned to its end while the reader is there (requires `getItemKey`). |
| **Chevron** | `{direction?: "right"\|"down"\|"left"\|"up" ("right"); size?: number (14)}` | The app's one chevron: every disclosure triangle, tree twisty and dropdown caret draws from it. A square block-level SVG box, so rotating it in CSS cannot overhang its layout box; a fixed direction draws a different path rather than transforming. Decorative, stroke in currentColor. |
| **ToolIcon** | `{kind: ToolIconKind; size?: number (14)}` | The transcript row-kind glyph: one line-art icon per tool family (terminal, file, edit, search, …, with `wrench` for every unregistered or MCP tool), plus `person`/`thought` and the status shapes `check`/`cross`/`alert`/`info`. Same 16-grid stroke grammar and square box as Chevron; single-colour, currentColor. |
| **SpeakerAvatar** | `{speaker: "user"\|"agent"; size?: number (24)}` | The transcript's square speaker tile at exchange-boundary headers: ToolIcon `person` for the user, `skill` for the agent, glyph scaled with the tile. Decorative (`aria-hidden`) because the header beside it names the speaker in words. |
| **SendIcon** | none | The paper airplane for the app's one "send it off" act, shared by the composer's Send and the spawn pane's Start. Decorative, stroke in currentColor so it takes the button variant's colour. |
| **VisuallyHidden** | `{children: ReactNode}` | Text for assistive tech only: the accessible name of an icon-only control, or a live-region announcement with no visible twin. Use it instead of a local `.srOnly` recipe. |
| **InlineNotice** | `{tone?: "neutral"\|"danger"; live?: "status"\|"alert"\|"off"; title?: ReactNode; action?: ReactNode; children: ReactNode} & Omit<HTMLAttributes<HTMLDivElement>, "className"\|"role"\|"title"\|"children">` | In-flow status/hint/error message with no box of its own: message wraps, `action` (Retry, Connect, a dismiss IconButton) holds the trailing edge and drops below on narrow rows; `title` heads a list body (Warnings, plugin errors). Ink follows §2: neutral `--ink-mid` (title `--ink-hi`), danger `--danger-ink` — never the bare hue, never `--ink-hi` for an error. `live` defaults by tone (danger → `role="alert"`, neutral → `role="status"`); `"off"` for a notice inside a region that already announces. `forwardRef` + rest-spread like Button (aria-label, data-testid, `tabIndex={-1}` for focus-on-error). No attention tone: no inline site expresses "a human is needed". On the semantic allowlist for its danger tone. Banner stays the overlay strip for connection state. |
| **Eyebrow** | `{as?: "div"\|"span"\|"h3"; children: ReactNode}` | The one eyebrow recipe (§2): `--font-size-caption`, `--font-weight-medium`, `--ink-mid`, uppercase, `--tracking-eyebrow`, at most two words, copy sentence-case in source. Titles a group inside a page, never a pane title. `as="h3"` when the label heads a section readers navigate by heading (cheatsheet groups, rail sections); `"span"` inline beside other text (a hover card's kind label). Layout around it (rules, margins, padding in a list) stays the caller's. |
| **PropertyList** | `{layout?: "inline"; rows: {label: string; value: ReactNode; mono?: boolean; wrap?: boolean}[]} \| {layout: "stacked"; rows: {label: string; value: ReactNode; mono?: boolean; help?: ReactNode}[]}` | Read-only label/value facts: §10's meta table idiom as a semantic `<dl>` (one `<div>` per term and its definitions). Values `--font-size-ui` `--ink-hi` in the UI face, `mono` for machine-shaped values (versions, sources, paths). `label` is the row key and must be unique. **Inline** (default): fixed 96px caption `--ink-mid` label column beside the value; values truncate with an ellipsis, except a row with `wrap`, whose value shows in full, breaking anywhere — for a value the reader is confirming before acting (the hosts deploy plan, the rail's session context card). **Stacked**: label over value, each value wrapping on its own line, rows further apart, and an optional per-row `help` sentence (caption, `--ink-mid`) under the value as a second `<dd>` of the same term — for settings overview fields (SettingsField and its consumers) and the session DetailsPanel. `help` exists only on stacked rows and `wrap` only on inline ones; the union type enforces it. |
| **ShortcutList** | `{items: {keys: string[]; label: ReactNode}[]}` | Reference list of keyboard shortcuts: a `<dl>` whose terms are KeyHint chords (right-aligned in a max-content column) and whose definitions are the action, `--font-size-ui` `--ink-mid`. One chord per item; the same chord may repeat with different meanings. Not for editable bindings (keybindings settings) or rows with alternative/multi-press chords (cheatsheet). |
| **ListRow** | `{label: ReactNode; leading?: ReactNode; chips?: ReactNode; meta?: ReactNode; variant?: "collection"\|"plain"} & ({onSelect: () => void; disabled?: boolean; action?: never} \| {onSelect?: undefined; disabled?: never; action?: ReactNode}) & Omit<HTMLAttributes, "children"\|"className"\|"onClick"\|"onSelect">` | The full-width list row. With `onSelect` the whole row is one real `<button>` (§10: one target on desktop and touch); without it a passive `<div>` that may carry ONE trailing `action` (§10 caps a row there; a cluster of row buttons belongs in the item's detail sheet, and an interactive row cannot take `action` at all). Layout: `leading` glyph on the label's line, `label` one truncating line followed by `chips`, `meta` one truncating line under the label. `variant="collection"` (default) is §10's collection row: framed `--surface-1` row, mono `--ink-mid` meta, trailing drill-in Chevron when interactive. `variant="plain"` is a row inside another surface (a picker's choice, an activity-sidebar entry, a hierarchy crumb): no frame, no chevron, denser padding, sans `--ink-low` meta. Both: `--hover-1` wash, inset focus ring, `--tap-min` floor on phones (interactive rows and all collection rows). `forwardRef` to the root (the button when interactive, for focus restore) and rest-spread of native attributes (aria-\*, data-\*, title) before its own type/className/onClick/disabled, mirroring Button. Callers own the `<ul>/<li>` and the gap between rows. |
| **CloseIcon** | none | The close ✕ glyph Dialog and Sheet draw in their close button (lives at `src/widgets/dialog/CloseIcon.tsx`), for any other icon-only close or dismiss control (the settings pane's close, Spawn's dismiss, an InlineNotice dismiss action). Decorative (`aria-hidden`), stroke in currentColor; the IconButton's `label` names the action. |
| **DirectoryIcon** | none | The folder glyph DirectoryPicker draws beside folder rows (exported from `src/widgets/directorypicker`), for a control that opens or names a directory (Spawn's working-directory row). Decorative (`aria-hidden`), stroke in currentColor. |

**ToolRunGroup is deliberately not a widget.** It lives at
`src/panes/session/transcript/ToolRunGroup.tsx` with its own stylesheet, not under
`src/widgets/`, because it is transcript grammar rather than a reusable primitive: no barrel
entry, no gallery section. It is what finally renders principle 2 of the mockup brief
(`decisions.md`, topic 06 Alt A). The rule, owned by `transcript/toolRuns.ts`: in a SETTLED
turn, three or more consecutive completed, non-failed tool calls whose renderer has opted in
collapse into one `<details>` row labelled `N steps · <last consequential summary>`. Folding is
opt-in: `fold: "quiet"` (the reads, searches, web fetches, transcript reads) folds and only
counts; `fold: "consequential"` (the edit tools, shell, worktree) folds and is the step the label
names, being a mutation; `fold: "never"` (delegate, ask_user, task_list, use_skill, the `job_*`
tools) and any descriptor with no policy, which is every unregistered or MCP tool, stay on their
own row, because a tool the UI does not know may have had a side effect the reader must see.
Scroll and focus anchors follow the same fold (`foldTurnEntries`): a folded run is one anchor. A live turn never
folds at all, and a failure, a call still in flight, an auto-expanding card or any non-tool entry
breaks the run rather than being spanned by it. Disclosure state goes through the shared
disclosure store, so a reader's choice survives re-projection and the transcript's
expand-all/collapse-all baselines reach it; the body mounts only while open.

**Task status renders through one glyph family, shared by both task surfaces.** `TaskCheck`
(`src/panes/session/transcript/tools/taskCheck.tsx`) is also transcript grammar rather than a
widget, but unlike ToolRunGroup it serves TWO surfaces: the transcript's inline task card
(`tools/taskCard.tsx`) and the tasks pane (`chrome/TasksPanel.tsx`), which the 2026-09
task-rendering rework put on one grammar. It is a 16px square box drawn in the same line-art
contract ToolIcon uses (stroke currentColor, 1.75, round caps/joins), the inner mark naming the
state: plus = added, check = done, ✕ = cancelled, arrow = started, empty box = pending (not
started yet — distinct from cancelled's ✕, so "won't happen" reads differently from "hasn't
happened yet"). The glyph alone carries subtle semantic colour — the one scoped, user-approved
exception to the cards' neutral ground (2026-07-31 tasklist renderer spec): done = `--alive`,
started = `--accent`, added/cancelled/pending = `--ink-mid`. Text stays on the ink scale;
done and cancelled read through strikethrough + low ink, never colour, and a cancelled glyph
stays neutral — the attention hues are off limits for a routine reprioritization.

The inline card is a WINDOW onto the plan, not a changelog (2026-09 rework, Jesse's ruleset):
it settles folded at every verbosity level (descriptor `foldByDefault` — the level's
expand-details default does not open it, only the reader's click does, which then persists
through the shared disclosure store), so a run of task updates reads as quiet one-liners. The
folded summary line names only the most recent update, mark + label (`→ Build the harness`);
opening the row swaps that line for a recap sentence of the call's whole change
(`Completed "Decide the folded line"; started "Build the harness"` — verbs Completed, Started,
Added, Dropped, sentence case) and the body shows at most three slots joined by a hairline
spine: most-recently-settled (done or cancelled; timestamps order it, list position breaks
ties), in-progress, next. A note renders only when THIS call added it, set in the prose face —
the note is the agent's own sentence. The footer keeps the aggregate sentence + meter and
carries the whole-list affordance: an "Open task list" control that opens (and focuses) the
session's Tasks pane and never closes it — an open operation, not the `/tasks` palette
command's toggle — hidden where no session owns the card. An update
that changes no task status renders nothing at all. The tasks pane keeps its grouped
full-list structure — settled/in-progress/open groups, timestamps, meta, prompt disclosure,
the full notes timeline — but renders its rows on the same TaskCheck family, its live row's
latest note bare in the prose face (the "latest" label chip is gone), and its notes timeline
in the same prose treatment.

---

## Directory selection: one shared interaction

Every web directory field **must use `DirectoryPicker`** from
`src/widgets/directorypicker`, normally through `PathField kind="dir"`.
This includes session start, schema-driven launch options, global/project settings,
plugin and skill directories, In-repo config, and local marketplace sources. Use the same
responsive dialog on desktop and mobile; do not add a directory popover, datalist,
or a feature-local browser. The working example is `/dev/widgets` → DirectoryPicker.

Directory mode requires injected `directory.validatePath` and
`directory.createDirectory` actions plus `complete`; the widget remains wire-free.
The TypeScript `PathFieldProps` contract requires these actions for directory mode.
Use the settings store's `directoryActions` or the caller's existing client closures.
Recent directories are optional and appear only where that history is meaningful.
Pass the field label through `ariaLabel` on labeled `PathField` controls so the
accessible name includes both the field name and selected path; a native label
alone overrides the button contents.

The behavioral contract is:

- Browsing, breadcrumbs, Up, recent locations, and typed paths change a local draft.
  **Go** (or Enter in Path) validates and navigates; **Use this folder** commits once.
  Cancel, Escape, and outside dismissal preserve the committed value.
- **New folder** explicitly creates a child of the viewed directory, displays errors
  inline, and navigates into the result. Creation does not select the directory,
  submit a settings row, save configuration, or start a session.
- Only validated directories can be confirmed. A failed or pending child listing
  does not invalidate a directory. Stale responses cannot overwrite newer navigation.
- External committed-value changes reset the draft. Custom callers key the picker
  by that value; `PathField` does this itself.
- Keep long paths readable by wrapping. Preserve the shared modal focus scope,
  restore focus to the trigger on close, and select Path text on its first focus.
  Opening does not force the mobile keyboard up. Mobile confirmation stays above
  the keyboard inset; desktop uses the roomy dialog.
- Paths belong to the supported Linux/macOS hub filesystem, regardless of browser OS.

`PathField kind="file"` and `kind="outputFile"` retain file completion and literal
file-path entry. Their internal completion panel is not a directory-selection API.
Collection **Add** and form **Save** remain separate actions after directory
confirmation; changing the picker must not bypass their domain validation.

Tests must exercise draft-versus-commit behavior, cancellation, creation/errors,
external value changes, and real browser geometry. Do not encode the retired
browse-immediately-commits behavior in new tests. This contract supersedes older
path-picker behavior in dated plans and legacy parity checklists.

---

## 4. The color-is-attention rule, machine-enforced

**The rule:** chroma is scarce and means something specific. `--attention`/`--alive`/`--danger`
each carry exactly one meaning everywhere in the app (a human is needed / the agent is working /
something failed); reaching for one outside a widget with a genuine matching state is a bug, not
a style choice. `--accent` is different in kind, not degree — see below.

**Enforcement:** `src/styles/token-contract.test.ts` reads every `.module.css` + `global.css`
under `src/` directly off disk (`node:fs`, not Vite's `?raw` import — see the file's own header
comment for why: under vitest's default config, a `.css?raw` import silently returns an empty
string, a real upstream issue this project works around rather than papering over) and runs
six independent checks:

1. **File naming.** Every stylesheet besides `tokens.css` is named `global.css` or
   `<name>.module.css` — the convention the rest of the contract, and the whole widget
   directory layout, assumes holds.
2. **No chromatic literal outside `tokens.css`.** Two mechanisms: hex / `rgb()` / `hsl()` /
   `oklch()` / `oklab()` / `lab()` / `lch()` scanned across whole files (comments included —
   these forms are distinctive enough not to false-positive on a selector or class name); the
   148 CSS named colors (`red`, `white`, `black`, ...; not `transparent`/`currentColor`, which
   aren't chromatic) scanned only inside extracted declaration *values*, after stripping block
   comments — named colors are ordinary English words that legitimately appear in class names
   and font stacks, so this one has to be scoped narrowly to avoid false positives (a class
   literally named `.red` is not a violation). `color-mix()` composing existing `var(--token)`
   values is never a violation, at any scope — it introduces no new color.
3. **The three attention-family vars stay on a reviewed allowlist.** Currently: `cadence`,
   `button` (danger variant), `chip`/`badge`/`toast` (tone props), `statusdot` (state color),
   `meter` (danger/attention fill), `dialog` (danger footer). A
   widget earns a place on this list only when it has a state that genuinely needs one of the
   three hues — never for decoration. **`--accent` is deliberately exempt from this check
   entirely** — it's interaction chrome by definition (every interactive widget needs an accent
   `:focus-visible` ring; accent also carries selection and links), so gating it would grow the
   allowlist by one entry per interactive widget forever while protecting nothing. The
   color-is-attention thesis guards the three *attention-class* hues' meanings; focus/selection
   chrome was never the thing it was protecting.
4. **Dark and light blocks declare identical color-token name sets.** A token declared in only
   one theme's block silently breaks the other (falls back to the wrong hue, or resolves to
   nothing) — checked by extracting both blocks via brace-depth counting and diffing their
   declared names.
5. **Z-index values are tokenized.** Every `z-index` declaration uses `var(--z-*)` (from the
   ladder above), `0` (reset), or `auto` (default) — never raw integers. Keeps stacking context
   predictable across the app.
6. **Focus rings use tokens, not hand-rolled geometry.** Every `:focus-visible` outline is
   `var(--focus-ring)` or `var(--focus-ring-danger)`, never an outline shorthand carrying its own
   length + line style (any order, any unit), never the outline longhands, and never the retired
   inset-ring `box-shadow` hack (an inset spread-only shadow in any color, or a shadow colored
   with bare `--accent`/`--danger`; `-bg`/`-edge` mixes in a shadow are tinting and stay legal).
   Two exact-path exceptions: `shell/palette/commandpalette.module.css` keeps a quiet
   `2px var(--accent-edge)` ring for active typing — softer than a full `--focus-ring` but still
   present — and `widgets/dropzone/dropzone.module.css` keeps its dashed accent drag-target
   outline, which is drop-here signage, not a focus ring.
7. **Every `var(--name)` without a fallback resolves to a declaration** somewhere under
   `src/` (tokens.css or a module's own local property). Exempt: dockview's `--dv-*` and the
   runtime-set names JS or an inline style declares (`--keyboard-inset`, `--rail-width`,
   `--tap-min`, `--fill`, `--markdown-ink`, `--prose-ink`,
   `--density-scale`, `--font-scale`). Four undefined tokens had shipped before this check
   (`--radius-sm` twice, `--edge-hi`, `--font-size-title`), each silently falling back to the
   property's initial value.
8. **No literal `font-size` in px outside `tokens.css`.** Only `var(--font-size-*)`, `inherit`,
   and relative units (`em`, `%`) are legal; inline code is sized relative to its line, which
   is why `em` stays.
9. **`letter-spacing` only through the two tracking tokens** (`--tracking-display`,
   `--tracking-eyebrow`), `inherit`, or `normal`.
10. **Uppercase means the eyebrow recipe.** Any rule block with `text-transform: uppercase`
    must also declare `font-size: var(--font-size-caption)` and
    `letter-spacing: var(--tracking-eyebrow)`, and must not set `color: var(--ink-low)`.

Every mechanism above is poison-tested against hand-written snippets proving both what it
catches and what it must not flag (see the test file itself) — not just asserted to work.

**Ruling (Jesse, 2026-07-26, kata 9jew): dedicated diff colors are syntax/domain notation, not
status.** DiffBlock uses exactly `--diff-add-bg` and `--diff-del-bg` in both themes. They are
quiet, conventionally green/rose structural washes, with the `+`/`−` marker carrying the
meaning independently of color; their foregrounds remain the existing neutral ink tokens.
DiffBlock must not expose or reuse `--alive`/`--danger`, and it must not join the semantic
allowlist or create a broader status hue family. This ruling supersedes mockup 19's historical
palette details as an implementation authority: the mockup may show the visual intent, but it
cannot reopen the semantic-color contradiction. Recorded here so the token contract and the
widget stay aligned.

---

## 5. Motion budget

The law widened 2026-08-13 from "default none" to **"no idle motion"**: decorative idle
animation stays banned, but input-response transitions on interactive chrome are now
budgeted. Three budgets, all on `--motion-easing-standard` (`ease-out`):

- **Attention onset** (`--motion-duration-attention`, 200ms) — a state crossing into
  needs-you. Cadence's dot, StatusDot, and Switch all use it for their own state-driven color
  transitions.
- **Overlay fade-scale** (`--motion-duration-overlay`, 120ms) — Dialog, Sheet, and Menu's
  open/close.
- **Hover/focus/press response** (`--motion-duration-hover`, 150ms, NEW) — color, background,
  border, and shadow transitions on interactive chrome (buttons, inputs, rows, chips, ...)
  triggered by hover, focus, or press. Transitions name their properties explicitly; a blanket
  `transition: all` is never used.

Forbidden: decorative idle pulses, shimmer loops on live data, and animation that invents
progress. The two sanctioned continuous animations below are the only exceptions. Every
widget with motion of its own respects
`prefers-reduced-motion: reduce` (currently: Cadence, Dialog, Disclosure, Menu, SegmentedControl,
SelectionQuote, Sheet, StatusDot, Switch) — collapses to instant, no exceptions.

**Agent liveness (Jesse, 2026-10-11):** session and agent status is always `StatusDot`, and its
`working` state is an activity indicator: the dot becomes an open alive ring that turns
continuously. This is the one continuous motion allowed for agent liveness, and only on
StatusDot. It plays wherever a StatusDot shows `working` (rail rows, while the navigation summary
reports active work including running jobs or descendants; pane tabs; ancestor spines; the command
palette; delegate rows), and stops the moment the status authority stops reporting work. It
communicates state, not frame throughput or percent complete. Needs-you (a solid dot) and failed
(a diamond) stay static and shape-distinct. Under `prefers-reduced-motion: reduce` the ring does
not render; working is the static alive dot. One turn takes `--motion-duration-activity` (800ms)
on linear easing, so the rotation's speed stays constant.

One approved exception (2026-09-11): the transcript's content-free "Thinking…" placeholder,
shown for a live reasoning item while the Reasoning content flag is off, uses the catalog
`Loader`'s pulsing pixel grid (Jesse's call). It renders no reasoning text, disappears once the
thought is no longer the turn's current activity, and is the only animation the transcript itself
adds. Unlike Cadence it is not activity-gated, so it keeps pulsing if the stream stalls; Cadence's
decaying trace remains the honest liveness signal. It is a user-facing wait placeholder, not an
agent-status indicator; agent status is StatusDot's job. Every other idle pulse or shimmer stays
banned.

### Spatial motion (2026-09-29, zoom activity surfaces)

A fourth budget, `--motion-duration-spatial` (240ms, still `--motion-easing-standard`), for
user-initiated geometry changes: the activity sidebar sliding in, a zoom column opening, a column
collapsing to a spine. Spatial motion answers *where did it go* — a panel that arrives from the
edge it lives on, a column that narrows into the spine it became. Motion that doesn't preserve
spatial continuity is decoration and stays banned. Data arriving (a job finishing, a watch firing)
never animates geometry: state change is what the attention hues are for. Architecture: one library
(`motion`), one wrapper module (`src/motion`), one reduced-motion switch (`MotionProvider`,
`reducedMotion="user"`), durations from tokens, never literals. Components import the wrapper, never
the library.

---

## 6. Copy rules

Sentence case for all UI copy; no ALL-CAPS **in copy** (button labels, headings, messages,
hints). Active-voice labels ("Save changes", not "Changes saved" or "Save Changes"). Mono is
for machine text only — code, tool output, paths, commands, identifiers — never chrome labels,
captions, or any text a human authored. (This tripped up even this wave's own gallery scaffold
once: three caption labels shipped on `--font-mono` in the foundation task and were caught and
fixed in wave-close review — see git history for `gallery-section.module.css` and
`theme-flip.module.css`. If it happened once, watch for it.)

**The eyebrow recipe (2026-09-06, replacing the 2026-07-24 "section eyebrow" clarification
and the separate micro-label pattern it was distinguished from).** Small-caps *eyebrows* are a
sanctioned pattern, distinct from ALL-CAPS copy: the copy stays sentence-case in the source and
the transform is presentation-only. There is now exactly one recipe, one tracking token and one
size for it: `--font-size-caption`, `--font-weight-medium` (or semibold where a header band
wants more), `--ink-mid` or darker, `text-transform: uppercase`,
`letter-spacing: var(--tracking-eyebrow)` (0.06em, which replaced a spread of
0.02/0.04/0.05/0.08em), and **at most two words**. An eyebrow titles a container INSIDE a page:
RecommendationCard's kicker and existing rail/settings section labels. InspectorCard and
Table/DiffTable column labels are sentence-case, not eyebrows. Three-word labels were the tell that the rule was being
broken, so "Agents & models" became **"Agent setup"**.

Never an eyebrow: buttons, sentences, and above all **titles**. A pane title is the page's own
heading, sentence-case in `--font-prose` at `--font-size-pane-title`, semibold, `--ink-hi` (§2); a session title is
the user's own prompt and is never transformed at all. Before this, both rendered as a 12px
uppercase micro-label, which turned a whole prompt into a shouted sentence.

---

## 7. The system voice

Three voices appear in a transcript: the human, the agent, and the system
steering the agent. The first two are marked. This section marks the third.

**The rule: a glyph in the gutter means the agent's instructions changed. An
empty gutter means it is a passive fact.**

The transcript already has a 10px glyph gutter, sized to `SteeringGlyph` and
`FailureGlyph`'s own SVG (`viewBox="0 0 10 10"` — neither widget declares a wider
box) — `toolcallitem`'s `.row` and `systemnoticeitem`'s `.failure` share one
`display: flex; align-items: baseline; gap: var(--space-2)` grammar. This section
assigns that column.

| gutter | member | treatment |
|---|---|---|
| `◇` | **steering** | `SteeringGlyph`, `--ink-mid` for the whole row, kind from the wire, chevron trailing |
| `✗` | **failure** | `FailureGlyph` in `--danger`, text in `--ink-hi` |
| *(empty)* | **lifecycle fact** | `--ink-low` one-liner; a run of 3+ collapses into one disclosure |
| `▸` box | **scaffolding** | hairline-bordered box: the system prompt, compaction summaries, round timings |

Notification cards sit outside the rule — a card is not a row and has no gutter.

**Steering labels come from the wire, never from the text.** `SteeringInjectedData.Kind`
(`agent/events/payloads.go`) is set at each injection site and reaches the
renderer on both the live and reload paths. A steer with no kind renders
`System steered` with no colon: a colon promises a value, and the UI does not
guess at one.

**The two sides cannot drift.** `make generate` emits the Go enum into
`types.gen.ts` as `STEERING_KINDS` plus the union `SteeringKind`, and
`SteeringItem.tsx` types its label map as `Record<LabelledKind, string>` over that
union. Adding a kind in Go and regenerating fails `tsc` with a missing-key error
naming the kind, until it is given a label, suppressed, or routed to a card. This
is the only mechanism enforcing that — deliberately, since a second one covering
the same property would be worse than either alone.

Pattern-matching is the alternative this forecloses. `steeringClassify.ts`
inferred a kind from 8 text patterns, against the seventeen kinds the daemon
actually names today — one pattern matched `/reading without writing/`, a string
that appears nowhere in the Go source, and nothing failed when it went stale.
The file's own header now explains why it stopped inferring one; that silent
gap, a renderer's idea of what the daemon says drifting from what it actually
says, is the failure mode this rule exists to prevent.

**Why `--ink-mid` and not `--ink-low`.** Every other quiet system row uses
`--ink-low`. Measured against `--surface-1` that token is 4.72:1 in dark and
4.76:1 in light since the 2026-09-06 raise (§2), so it clears the 4.5:1 AA
floor it used to sit under, but its role is still placeholder and
hairline-adjacent chrome rather than text a reader is meant to read. A reader
scanning steering is auditing which kind fired, so the kind is the payload rather than furniture, and
it sits one ink step up at 6.51:1 / 5.84:1. That step also separates a steer
from the lifecycle line beneath it by weight as well as by glyph.

**The glyph is a hollow diamond, drawn as SVG rather than set as a character.**
It was first tried as the reference mark ※, but at the row's actual 10px ship
size a faithful ※ collapses into a shape indistinguishable in monochrome from
`FailureGlyph`'s ✗ — a mark that can appear in the very same gutter column, the
two meanings then separated only by hue. A diamond has no such collision at any
size. SVG rather than the character ◇ (U+25C7) because `global.css`'s
`unicode-range` (`global.css:23-24`) subsets IBM Plex Sans to a range with no
U+25xx block at all, so a literal ◇ would be the one glyph in the app rendering
from a system fallback font. `SteeringGlyph` draws it, inherits `currentColor`,
and — unlike `FailureGlyph` — carries no accessible name, because the row's own
text already says "System steered: <kind>".

---

## 8. Known gaps (documented, not fixed — wave-close adjudication)

Two items reviewed at wave-close and deliberately left as documented gaps rather than quick
fixes, because the "quick fix" in both cases risked being wrong in a way that's worse than the
current gap:

- **FocusScope doesn't set `inert` on anything outside the trapped scope.** Tab-trapping
  (`trap=true`) covers keyboard navigation, which is what this project's tests exercise and
  what the large majority of real interaction is. The residual gap is a screen reader's virtual
  cursor (or touch exploration) reaching content outside the scope that a sighted keyboard user
  would never land on. A correct fix needs to know what "outside the scope" even means for a
  given consumer: Dialog/Sheet's `FocusScope` has no DOM siblings at all (the scrim wraps it
  alone), so there's nothing to make `inert` there; Menu's `FocusScope` sibling IS the trigger
  button, which needs to stay clickable to close the menu on a second click — naively making it
  `inert` would break that. The real fix is portal-rendering overlay content up to a stable
  app-root position (none of Dialog/Sheet/Menu do this — they render inline in the component
  tree today) and inerting siblings AT THAT level, which is a real architectural change, not a
  FocusScope-local one. Flagged for a future pass alongside adopting portals, not bolted on now.
- **Tooltip's timer and `aria-describedby` wiring stay fully active on touch devices**, even
  though the visual bubble is CSS-hidden there (`@media (hover: none)`, since a tap has no
  `mouseleave` to dismiss an open tooltip with). This looks like wasted work worth suppressing
  via a `matchMedia('(hover: none)')` gate, but doing that would also suppress the
  `aria-describedby` association for a touch/AT user navigating by focus (e.g. VoiceOver swipe
  navigation on a touchscreen) — who would genuinely benefit from the description being
  announced even though they'll never see the visual bubble. Suppressing the "dead" wiring and
  removing a real accessibility benefit for exactly the users who might need it most is a worse
  trade than leaving admittedly-redundant code running. A narrower alternative was considered —
  gate only the mouse path (`onMouseEnter`/`onMouseLeave`, which never fires on a real touch
  device anyway) behind `matchMedia('(hover: none)')` while leaving `onFocus`/`onBlur` fully
  ungated, since Tooltip already wires all four as independent handlers — but it's flagged for
  the same follow-up pass rather than made now, without real-device AT verification that it
  doesn't change touch+AT behavior in some non-obvious way. Left as-is, flagged for a more
  careful pass that can validate actual AT behavior on a real touch+screen-reader device, not
  reasoned about in the abstract.

---

## 9. Command surfaces: palette vs. composer

**The principle (2026-08-14): the palette is where you go; the composer is where you act on this
session.** Every command in the registry (`shell/palette/commands.ts`) is tagged with a surface —
`commandSurface()`, derived from the same `scope` field that already decided whether a session
needs to be focused. An **app-global** command (new session, spawn, theme, dashboard, search,
help, upgrade, next-needs-you, open settings) needs no session and stays palette-native: it is
listed, filtered, and run entirely inside the command palette (`Mod+K`), exactly as before. A
**session** command — every mutation or read that acts on the focused session, built-in (goal,
model, reasoning effort, status, compact, clear, steer, queue, interrupt, shutdown, aside,
drain-as-steer, copy-id, tasks, project) or a plugin's own slash command — runs ONLY from that
session's composer, never the palette.

**The composer is the session's own command line, Slack-model.** Its inline `/` menu
(`slashCompletion.ts`'s `mergeSlashCommands`) lists the session-scoped built-ins merged with the
plugin catalog — one list, each row stating what it does or, for a plugin command with no
description, naming its plugin provenance. Submitting a message that PARSES as a known built-in
invocation (a leading `/name` with optional args) runs that command's RPC instead of sending the
text — a literal message that happens to start with a recognized `/command` executes rather than
sends, matching the muscle memory Slack and Discord users already have. Feedback is a toast plus
whatever live chrome the mutation already drives (the goal chip, the status row); the draft clears
on success and is preserved verbatim on failure, so a rejected command never costs the user their
typed text. Anything that does NOT parse as a known built-in — an unrecognized `/foo`, or a
plugin's own slash command — sends as an ordinary chat message: that's the escape hatch, and it's
deliberate, not a gap.

**The palette delists every session command and offers a handoff instead.** Typing a `/`-prefixed
filter that matches a session command's name (built-in or plugin) shows exactly ONE row —
"Continue in the composer: /goal …" — carrying the raw text as typed. Activating it inserts that
text into the focused session's composer and moves focus there, closing the palette; the palette
itself never executes a session mutation or makes a wire call for one. With no session focused,
the same row explains that there's nowhere to hand off to yet, rather than silently doing nothing.

## 10. Collection pages: segmented workspaces and detail sheets

**The pattern (2026-08-29): when one page holds several same-weight collections, segment them —
never stack them.** The first collection page this shipped on is Settings → Marketplaces &
Plugins (`panes/settings/sections/marketplacesPlugins/`), which previously stacked three
sections (registered marketplaces, the browse tree, the installed list) down one long scroll.
It is the reference implementation for the two idioms below, with Settings → Providers &
credentials (`panes/settings/sections/credentials/InstanceSheet.tsx`) the reference for the
sheet-as-editor form; any future page with the same shape (several sibling lists, plus per-item
detail and actions) should reuse them rather than inventing a third layout.

**One list at a time, chosen by a page-level SegmentedControl.** Each sibling collection becomes
a segment; the segment labels carry the counts (`Installed (7)`, `Marketplaces (3)`), and the
per-section headers — title plus count — are deleted, because duplicating that identity under
the segment control is noise. The default segment is the one the user maintains most (Installed,
not Browse). Switching segments is a page-level navigation act: page-scoped overlays owned by
the outgoing segment close (see the sheet rule below), while per-segment UI state that is
expensive to rebuild (the browse tree's expansion and its lazy catalog cache) is lifted to the
page so it survives the round trip.

**Rows are single tappable targets; the detail sheet is the item's editor.** A collection row
carries identity and status only — `StatusDot`, name, state chips, one mono meta line
(`@ marketplace · v1.2.0`) — and a trailing chevron; it is one full-width `<button>`, so the
whole row is the target on desktop and touch alike. A row NEVER grows a trailing cluster of
small action buttons (the pre-redesign installed row had four): everything about the item lives
in its **detail sheet**, a `Sheet` with `side="right"` on desktop and `side="bottom"` at the
mobile breakpoint (chosen via `useIsMobile`, the same source the shell uses), `size="wide"`
when it carries a form. The sheet is the item's editor, not an inspector (2026-09-07): every
authored, editable fact renders as a prefilled form field in place — `FormRow` over `Input` /
`Select`, one column — with a dirty-gated **Save** as the footer's primary `Button`, and
renaming is editing the name field. Read-only facts keep the meta-table idiom below.
Display-only content a row does not carry (a plugin's catalog description) is pulled lazily
through the browse cache — one store-level entry per marketplace, so re-open is free.
A separate `Dialog` is reserved for write-only secret entry (an API key, a credential JSON) and
multi-step flows (OAuth); it never exists to edit a field the sheet could show. Binary state
(Enabled, Auto-upgrade) is a `Switch` row that applies immediately, disabled while its RPC is
in flight; the destructive action keeps its `ConfirmDialog` even though that nests a second
modal over the sheet — `OverlayPanel` instances stack in DOM order, each traps and restores
focus down the stack, and its `preventDefault` on Escape is what keeps the settings pane's own
document-level Escape handler from closing the pane out from under an open overlay. Closing a
sheet with unsaved edits discards them silently; the sheet reseeds only when a different item
opens or its own save lands, so another client's refresh never clobbers a draft.

**The meta table idiom.** Inside a detail sheet, read-only facts render as label/value rows: a
fixed-width (96px) caption-color label column, values in the UI font, and `var(--font-mono)` for
anything machine-shaped — versions, sources, paths — truncating with ellipsis rather than
wrapping. This is the same vocabulary as the list row's meta line, one zoom level up.

**A detail sheet is only as alive as its subject.** The detail sheet reads its entity from the
store rather than a prop snapshot, so cross-client changes land while it is open; when the
entity disappears from the store (its own Remove completing, or another client's), the sheet
closes itself instead of offering actions on a ghost — except when the disappearance is the
sheet's own rename landing, where the section re-selects the item under its new name and the
sheet stays open — and a failed Remove keeps the sheet and dialog open for retry. Segments own
their overlays: switching away closes the sheet, coming back does not reopen it.

## 11. Mobile forms and honest cold starts

Mobile forms use settings-style rows when several related choices must remain
scannable on a narrow screen. A row fills the available width, is at least
48px tall, uses sentence-case sans labels, and truncates its value without
shrinking the label or the hit target. Interactive rows expose the whole row
as one control; read-only facts do not show a misleading caret. Use existing
surface, edge, ink, spacing, and tap-size tokens. Do not introduce mobile-only
colors, type tokens, chip backgrounds, or monospace labels.

Editable text is 16px on phones, and it gets there through the ramp rather
than per-field: `tokens.css`'s below-900px block sets `--font-size-body` to
16px and every field takes the body size, so iOS Safari has nothing to
auto-zoom into. That is what let the viewport meta stop locking zoom (WCAG
1.4.4 resize text): `index.html` no longer carries
`maximum-scale=1, user-scalable=no`, and `src/styles/viewport-pin.test.ts`
fails if either comes back. An auto-growing textarea has a real content-driven
minimum and maximum, keeps the resize behavior accessible, and reserves space
for any pinned actions below it. The field remains the same semantic textarea
and preserves keyboard submission, attachment, paste, and screen-reader
behavior across breakpoints.

When a form has one primary completion action, the mobile action band may be
fixed above `env(safe-area-inset-bottom)`. It is a raised surface with a top
edge and no shadow; the primary control is at least 52px tall and adjacent
secondary controls are at least 44px. The form body reserves the band's space
so content is never covered, and the desktop action layout remains unchanged.

Mobile choice controls use the existing bottom `Sheet` pattern. Sheet options
are at least 48px tall, expose selected state semantically, restore focus to
the invoking row after Escape or selection, and retain the shared focus trap
and scrim behavior. A feature should reuse its existing catalog/path panel
inside the sheet rather than create a second picker implementation.

Loading treatment follows the honest-liveness rule. A first-turn skeleton may
reserve the shape of the response only after a send or active first turn and
only until the first authoritative transcript item. It must disappear on an
authoritative frame, terminal/error/cancel state, session change, or pending
failure; it must never appear for an untouched empty session. Reuse the static
`Skeleton` widget, keep its accessible loading status and decorative bars, and
do not add shimmer, pulse, or other motion that implies live data (the one
motion exception, the Reasoning placeholder's `Loader` grid, is recorded in the
motion budget above).

Below 700px both speaker rows become a grid rather than a flex row: the avatar
and the speaker header share the first row and the prose spans the full pane
underneath them, instead of being indented into the avatar's column for its
whole height. Measured before this, agent prose got 260px of a 375px screen,
28 characters a line.

The sessions drawer renders the Rail flush inside the Sheet body (Sheet's
`bodyClassName`, §3): no inner surface box, no inner radius, because the sheet
already frames it.


## Source Serif 4 license notice

From `@fontsource-variable/source-serif-4` 5.3.0, distributed unmodified.

```text
Google Inc.

This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
http://scripts.sil.org/OFL


-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
```
