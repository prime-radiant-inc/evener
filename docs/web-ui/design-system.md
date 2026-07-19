# Serf Web Hub — Design System & Style Guide

Status: **current** (updated 2026-07-11; originally drafted 2026-06-16). Static references are
[`examples/01-golden-live-session.html`](examples/01-golden-live-session.html) and
[`examples/02-hard-cases.html`](examples/02-hard-cases.html) (self-contained; open in a
browser). Note the examples still use the golden draft's token *names* (`--fs-*`, `--s1..6`,
`--r`); the shipping names in §3 are canonical.

This guide describes the SHIPPING system in `cmd/serf-hub/assets/style.css` + `renderer.js`.
Where a rule has not landed yet it is marked as deferred (§7).

---

## 1. Audience & register

Serf's web hub is a **customer-facing product** for power users who watch AI agents work in
real time. It must feel **crafted** (first impressions matter) while staying **information-dense
and fast** (people live in it all day). Register: a refined, dark-first developer tool —
evolve the existing Tokyo-Night aesthetic, do not rebrand.

---

## 2. Core principles

These were derived with the product owner and override convenience. When in doubt, re-read them.

1. **Color means "needs your eye"; absence of color means "settled."** A *done/successful*
   thing is the expected state — it must **recede** (neutral), not glow. Saturated color is a
   scarce signal reserved for: live/active (blue), needs-you (amber), error (red).
2. **Emphasis is scarce.** A box, a tint, or a rail is emphasis — spend it only on what the
   user needs to *see* (the agent's finding, a subagent result, something needing attention).
   **Never emphasize the user's own messages** — including mid-turn steers. The user knows what
   they said; echoing it loudly is wasted attention.
3. **One containment device per element.** Use a box **or** a rail, never both. Avoid stacking
   containers (a bordered box with internal gridlines is two devices).
4. **Conversation leads; tool calls are subordinate.** Prose (user + agent) is the loud,
   readable layer. Tool calls collapse to a quiet line and **collapse further once scrolled
   past**. Scannability comes from emphasizing the conversation, not from louder tool rows.
5. **Minimal, purposeful motion.** A little, never a lot. No blinking/flashing. One gentle
   "alive" cue (a slow-breathing dot) and a soft (non-blinking) streaming cursor — nothing more.
   Honor `prefers-reduced-motion`.
6. **Mono is for machine text only.** File paths, commands, identifiers, code, raw tool output.
   Everything a human authored — labels, prose, buttons, nav, durations, relative times — is
   sans. (Treating monospace + ALL-CAPS as the house style was the single biggest "amateur"
   tell in the old UI.)
7. **Liveness must be honest.** Never show a reassuring "working" animation that looks identical
   to a hung agent. See [ux-and-implementation-plan.md](ux-and-implementation-plan.md#liveness).

---

## 3. Tokens

Defined in the golden example's `:root`. Semantic names carry meaning; raw hues do not.

### Color — four meanings, each exactly one

| Token | Hue | Meaning — the ONLY thing it marks |
|---|---|---|
| `--accent` | blue `#7aa2f7` | **live / active + interactive**: working/running status, links, primary action, focus ring |
| `--attention` | amber `#e2b06a` | **needs you / awaiting input** (a question, a permission prompt, a blocked session) |
| `--error` | red `#f7768e` | **failed-to-run / broke** — the agent or a tool itself errored |
| `--done` | neutral `#7e8593` | **done / settled** — the expected, finished state. Recedes. (This is `--ink-3`, not a color.) |
| `--success` | dim green `#8aa873` | reserved for *genuine good-news highlights only*, used sparingly — NOT for ordinary "done" |

> A subagent that *ran fine but found bad news* (e.g. a failing test it was asked to check) is
> **done (neutral)** with the bad news in its result text — **not** red. Red means the subagent
> or tool itself failed to run.

Neutral ramp: `--bg #0e0f13`, `--surface #16181d`, `--surface-2 #1c1f26`, rules `--line #23262e`
/ `--hair #2d313b`; text `--ink #e8e9ee` (primary), `--ink-2 #abb1be` (secondary, AA), `--ink-3
#7e8593` (tertiary, AA ~4.8:1), `--ink-4 #4b5060` (**hairlines / non-text only** — never words).

### Type

- **Sans (everything human):** Hanken Grotesk. **Mono (machine text only):** JetBrains Mono.
- Shipping scale (M preset, px): `--text-2xs 10` / `--text-xs 11` / `--text-sm 12` /
  `--text-base 13` / `--text-md 14` / `--text-lg 16` / `--text-xl 18` / `--text-2xl 22`.
- **The transcript uses ONE reading size + two exceptions** (2026-07-11 round 2, tightened
  from the four-step scale): ALL flowing reading text — assistant prose, tool intents and
  result text, thinking summaries and bodies, user messages, system asides, plan rows —
  sits at `--text-base`; mono machine text (commands, code, paths, diffs, raw output) is
  `--text-sm`; true meta (timestamps, the YOU tag, durations, badges, fold heads) is
  `--text-xs`. Line-height across the column is `--leading-normal`. **Prose dropped from
  `--text-lg` to `--text-base`**: the larger hero step made the column read as
  differently-sized fragments; contrast and paragraph rhythm carry the prose's weight now,
  not size. `--text-2xs` remains reserved for non-transcript chrome (sidebar counts,
  pickers) — never transcript text.
- Leading: `--leading-tight 1.3` / `--leading-snug 1.5` / `--leading-normal 1.6` /
  `--leading-relaxed 1.7`.
- Numbers (durations, counts, clock stamps, relative times) are sans with
  `font-variant-numeric: tabular-nums` — never mono (mono is machine text only).

### Font-size presets

Four user-selectable presets scale every `--text-*` token (base values: `--text-2xs 10` /
`--text-xs 11` / `--text-sm 12` / `--text-base 13` / `--text-md 14` / `--text-lg 16` /
`--text-xl 18` / `--text-2xl 22`, all px) via `body[data-font-size]`:

| Preset | Scale | Setting |
|---|---|---|
| S | ~90% | Settings → Appearance → Font size |
| M | 100% (default) | " |
| L | ~115% | " |
| XL | ~130% | " |

Persisted per-browser in `localStorage` (`serf-hub.appearance.fontSize`), applied via a
`body[data-font-size="…"]` attribute redefining the `--text-*` custom properties (they cascade
to every descendant) — no per-element JS resize logic.

### Spacing, radius, elevation, motion

- Shipping spacing scale (px): `--space-1 2` / `--space-2 4` / `--space-3 8` / `--space-4 12` /
  `--space-5 16` / `--space-6 24` / `--space-7 32` / `--space-8 48` / `--space-9 64`. Snap to it;
  1px hairlines stay literal.
- Radius: **two only** — `--radius-md 5px` (rectangles) and `--radius-pill 999px` (chips/dots).
- Elevation: flat surfaces separated by hairline rules and background steps (`--bg` →
  `--bg-raised` → `--surface`), not drop shadows; shadows appear only on true overlays
  (lightbox, drawers).
- Motion: three durations on one easing — `--motion-fast 110ms` / `--motion-base 180ms` /
  `--motion-slow 260ms` on `--ease`; `--pulse-cycle` for the one sanctioned breathe. Everything
  inside `@media (prefers-reduced-motion: reduce)` → none. (See §9.)

### The transcript marker gutter

`.conversation` defines `--gutter` (20px desktop; 12px phone; 8px in a narrow pane): a fixed
left gutter for **markers and rails only**. Every transcript entry's TEXT starts at the same
x=0 column line; anything that annotates the entry (the tool ✕/… status glyph, the thinking ✦,
a body's border-left rail) hangs into the gutter with a negative margin. No entry may
reintroduce a per-kind text indent — differentiation is weight/color/size, never indent.

### Retired from the old UI

ALL-CAPS + heavy letter-spacing as a default label treatment; monospace for chrome/labels; the
imperceptible 5% paper-grain `--noise` texture; the 6-radius spread; `--purple`/`--cyan` as
ambient hues; `--text-dim`-on-dark for primary navigation (failed contrast).

---

## 4. Component grammar (the transcript)

Design rule: **one scannable primary line per entry; status on a left rail or glyph; machine
detail demoted to a mono chip; deep detail hidden until expanded.** Content column capped ~720px.

**Breakpoint ladder + wide band (2026-07-19 addendum).** Phone ≤767px; tablet 768–1199px
(side panes hidden, sidebar auto-rails — see §5); desktop 1200–1799px; wide ≥1800px.
The prose measure holds 720px at **every** width; the machine bleed (`--measure-machine`)
is 1000px below the wide band and 1200px at/above it. Left edges never move.

| Entry | Primary (glance) | Secondary | Hidden until expanded | Color / containment |
|---|---|---|---|---|
| **User prompt** | quiet: a dim `You` tag + muted text | turn index | — | none. Demoted on purpose. |
| **Assistant prose** | the **hero** — sans, `--fs-md`, full contrast | — | — | none; wins via size+space |
| **Thinking** | quiet collapsed line "Thought for Ns" + faint preview | — | full reasoning (streams live) | none; quietest. Collapsible. |
| **Tool call (done)** | the **purpose** ("Check kernel version"), not the command | verb+args as a quiet mono line; **success is silent** — no "exit 0", and **no ✓ glyph either** (a done row leads with its content; the status slot stays empty so rows align) | full output | neutral; collapses once scrolled past |
| **Tool cluster (scrolled-past)** | one line: "✓ 4 steps · …" | — | the individual calls | neutral box (the one device) |
| **Tool call (running)** | purpose | mono chip | live output | blue left-rail tick |
| **Tool call (error)** | error summary promoted to primary | mono chip | stderr (truncated, see below) | red left-rail (one device — no extra box) |
| **Subagents** | a living **delegation rail** (sibling of the plan card): "Subagents · ⟳ N running · ✓ M done"; a lone subagent is just one row | per-row two lines: task name leads; a **live activity** line beneath (the child's latest step + step count), pushed over the socket (subscribe to the child thread — no polling) and **aged honestly** (fresh → dim → `quiet Ns` amber) | each subagent's transcript (the row is the door) | one neutral **left rail, no box**; **fixed spawn-order** (rows never reshuffle live); done fold behind `✓ N done`. A job's notification card and its rail row share a `job_id` and are **tied**: the row pulls the notification's headline, and each carries a quiet cross-link (`↑ in rail` / `report ↓`) that scrolls to + flashes the other |
| **Job / watch notification** | glyph + title ("Job completed"); **done glyph is neutral ✓** | a quiet dim secondary (job kind · `exit N` · reason); the message prose + a facts list | machine metadata (ids, transcript ref, bytes) in a "raw notification" disclosure | one neutral box + a left rail in the tone colour (neutral for done). **No chip wall.** |
| **Plan / tasks** | one inline change card per successful `task_list` mutation — progress (`31/46` + thin neutral meter) followed by only tasks added or whose status changed in that call | task notes attached to changed rows | the whole plan in the sidebar; no inline full-plan disclosure | a single neutral **left rail, no box** (rail = "status", box = "needs-you"). Each card stays at its tool call's conversation position. |
| **Steering** | quiet: dim "You steered" + muted text, thin amber tick | — | — | demoted (it's the user's message); amber tick only |
| **Image** | inline thumbnail + caption (filename · dims) | provenance (user-pasted / tool-read / generated) | lightbox | neutral card |
| **Long output** | first N lines + "expand · N more lines" | byte/line count | full (with escape hatch for huge) | neutral box; blue "expand" affordance |
| **Liveness** | pinned strip: "working · 1:12 · waiting on N subagents" | — | — | not in scroll flow; always reachable |
| **System / lifecycle** | quiet dim one-liner (plugin loaded, skill activated) | — | — | never divider-weight; no "N chars" |

Status hierarchy: **running (blue) draws the eye; done (neutral) recedes; needs-you (amber) and
error (red) stand out.** Pair every status color with a glyph so it is colorblind-safe.
**Done recedes to *absence*, not a green check:** a successful tool row shows **no glyph at all** —
only a **failure (✕, red)** sits in the gutter. A transcript of successes is then a calm column of
content with the occasional red ✕ standing out; you never scan past a wall of ✓✓✓.

**Disclosure placement.** Expand affordances in the transcript sit on the **right**, not the left:
the tool-row caret is right-aligned (so the status/glyph leads a clean, aligned left edge), and
card disclosures (`raw notification`, `full excerpt`, `show raw error`) read `label … ›` with the
chevron at the right edge. A left-hung chevron offsets every row and ragged-edges the column; on the
right it's a quiet "there's more." (On phones the timing meta is out of the flow entirely so
commands and file paths get the full row width; a tap on the row brings it back.)

**Timing metadata is on-demand.** Per-row timestamps/runtimes (`.tool-call .tool-meta`:
`03:19:32 PM · 1ms`) and the per-turn badge (`.assistant-message .turn-meta`: duration ·
tokens · cost) rest at `opacity: 0` and reveal on row hover, keyboard `:focus-within`, or tap
(sticky `:hover`) on touch. `opacity` — never `display`/`visibility` — so they stay in the
accessibility tree. A column of permanent clock stamps was ambient noise competing with the
content; the data is one hover away, not gone. (This supersedes the 2026-07-01
"always-visible" revert: the accessibility concern is answered by the a11y-tree +
focus-within pairing, not by permanence.)

### Transcript row anatomy (reference)

What each element of the two workhorse rows uses. Text column starts at x=0; the marker
gutter (`--gutter`) is to its left (§3).

**Tool call row (`.tool-call`)**
- gutter: `.tool-status` glyph — `…` pending (`--text-dim`), *empty/quiet* when done, `✕`
  `--error` on failure. Mono, `--text-xs`.
- `.tool-intent` (when the agent stated a purpose): sans `--text-base`, `--text` — the primary
  line.
- `.tool-command` = `.verb` + `.target` + `.result-detail`: verb/target mono, result sans.
  Mono `--text-sm` whether primary (no purpose, target `--text`) or demoted under a purpose
  (one truncated line, `--text-dim`, same x=0 column, no sub-indent) — dim color, not a
  smaller size, carries the demotion.
- `.tool-meta` top-right: sans `--text-xs` `tabular-nums`, `--text-muted`, hover/focus-reveal.
- `.tool-body` / `.diff-body` / `.shell-output`…: full-width below; rails hang in the gutter,
  boxed machine output is mono `--text-sm` with its own contained `overflow-x`.

**Thinking row (`.think`)**
- gutter: `.think-glyph` ✦ (breathes while streaming).
- `.think-label` "Thought for Ns" + `.pv` gist: sans `--text-base`, tier-colored
  (`--text` long / `--text-muted` short), gist italic.
- `.think-body`: expanded reasoning, `pre-wrap` italic `--text-muted`, text at x=0 with a
  1px rail in the gutter.

### The notification / job card (worked example)

The job- and watch-notification card is the canonical test of principles #1, #3, and #6. It is a
single neutral box with a tone-coloured left rail — **one** containment device. Inside:

- **Header:** a tone glyph + the title. A *completed* job is the expected state, so its glyph is a
  **neutral ✓** and its rail is neutral — not a green dot. Only a warning (amber ⚠) or a failure
  (red ✕) spends colour. The job kind and the failure signal (`delegate`, `exit 1`, the reason)
  ride a **quiet dim secondary**, not bordered chips.
- **Body:** the agent's `communicate` message as prose (markdown), then a compact **facts**
  definition list for the structured extras (concerns, artifacts; commit/tests/status only as a
  fallback when there's no message). Mono is used **only** for machine values (commit hashes,
  filenames) — never for the labels.
- **Demoted to the "raw notification" disclosure:** job/delivery ids, transcript refs, byte counts,
  triggers — the plumbing. It is discoverable, not displayed. The daemon's boilerplate prose
  ("Job <id> completed…") is suppressed; the title already says it.

Anti-pattern (retired): a wall of bordered `label value` mono chips for every attribute, a green
"done" dot, and structured fields as `status x` / `commits y` mono run-on lines.

---

## 5. Sidebar

Project-first, ranked by recency, with disclosure folding. Tiers top→bottom:

- **ACTIVE** — projects with a live/needs-you session; auto-expanded; rollup dot (blue=live,
  amber=needs-you). Sessions listed; **subagents de-weighted** to dim, single-line, indented
  rows with a terminal-state glyph (`✓` neutral / `⟳` blue / `✕` red), collapsing to "+N
  subagents" past ~3.
- **RECENT** — touched in ~last day; collapsed.
- **OLDER** — collapsed.
- **TEST RUNS (N)** — auto-bucket the disposable `serf-e2e-*` single-session sprawl into one
  collapsed group so it stops drowning real projects.

Session row anatomy (`.sb-row`, reference — note: the sidebar is under active restyling in a
parallel 2026-07-11 pass; verify against `style.css` if this drifts): status dot/glyph (blue
live · amber needs-you · neutral otherwise) · title sans `--text-base` `--text` (muted until
hover/selected) · right-aligned relative age sans `--text-2xs` `--text-muted` `tabular-nums`
(opacity-swapped for row actions on hover) · subagent children indented, dim, `--text-2xs`
terminal-state glyph rows folding to "+N subagents".

Project header: disclosure chevron (`›`, muted, rotates 90° when `aria-expanded="true"`,
`--motion-fast`) · name (sans, not ALL-CAPS-mono) · relative age · count; rollup dot only when
something is live/needs-you. The chevron appears on **every** project header, including
collapsed archived stubs — the whole header is the toggle; the chevron is decorative
(`aria-hidden`). Vertical rhythm: symmetric `--space-2` header padding plus a `--space-2`
top margin between a project and whatever precedes it — group separation reads as a subtle
rhythm break, not a void. Tap floors are untouched (32px desktop / 52px mobile min-height).

---

## 6. Controls layout

- **Top bar = identity only:** session title + a single `Details ⋯` overflow. (The model chip
  does **not** live here — no duplication.) The mobile nav toggle (`☰`) is a **quiet bare glyph**,
  not a filled, bordered, shadowed box — it floats over the stable header, so it recedes like the
  rest of the identity bar (a faint backdrop blur keeps it legible on the rare page where content
  sits beneath it). Title + actions stay on **one row** (the title ellipsizes; actions never wrap
  under it).
- **Bottom = all live controls, by the composer where hands/eyes already are:** model chip, `+`
  attach, `Interrupt`, `Send as steer`, `Send` (primary blue). Every control is a real
  `<button>` with a visible `:focus-visible` ring and ≥30px hit target.

### Composer anatomy (shipping, 2026-07-12 round 4)

Top to bottom inside `.workspace-input` (the dock):

- **The dock is the container.** A hairline top rule + a background step (`--bg-raised`)
  separate it from the transcript — that is the composer's ONE containment device
  (principle #3). On **phone** the inner `.input-card` is a transparent, borderless,
  unpadded pass-through: no box, no radius, and **no focus-within outline** — focus reads
  from the dock's top-rule accent tint plus the caret, and each control keeps its own
  `:focus-visible` ring. On **desktop** the card keeps a subtle raised background,
  `--radius-md`, and the quiet focus-within outline: in the wide two-pane layout the dock
  spans the window, so the card earns its keep by locating the input; removing it there is
  a separate call.
- **Current-task strip** (the tasks trigger, phone only): the dock's own TOP line, above
  the status rail — "what the agent is doing right now": `▸ 2/5 Refactor session config`
  (count badge + the in-progress task's subject), ONE truncated line, full-width,
  `--tap-min` tall, tapping opens the tasks panel. The trigger carries `data-tasks-signal`
  (`none` / `active` / `done`, set by the badge updater) and the strip renders **only while
  `active`** (an unfinished list); at rest ("42/42 forever", or no tasks) it is hidden — a
  settled count is noise, and the plan card in the transcript + the desktop trigger keep
  the panel discoverable. Because the "tasks" label word and the glyph are visual-only, the
  updater writes a full `aria-label` ("tasks 2/5 — …"). This strip REPLACED the round-3
  badge-only `tasks 1/3` rail item, so tasks never show twice. On desktop the trigger stays
  a rail item (count badge + prose) as before.
- **Status rail** (`.input-status-rail`, above the textarea on phone): ONE calm secondary
  line — status dot + state word · branch ref (truncated; on phone the "branch" label word
  is dropped, the ref is self-evident) · `ctx` + compact `16k / 262k` numbers · the honest
  quiet-liveness item when the agent has been silent.
- **Textarea**: bare — transparent background, no border/outline at rest, 16px on phone
  (iOS zoom guard).
- **Action row** (`.input-controls`) is the last content band: `+` attach · model chip ·
  stop `■` · `steer` · send `↑` (the one blue disc on phone). The home indicator and the
  corner curves only constrain the **edges** of the bottommost band, so the row is inset
  **horizontally** (`env(safe-area-inset-left/right) + --space-4`, on top of the dock's
  `--space-3`) and keeps only a small fixed `--space-2` bottom pad — it does **not** stack
  `env(safe-area-inset-bottom)` under itself (round 3 did, which floated the controls
  ~80px above the physical bottom with a dead band below). The controls sit down in the
  gutter zone alongside the home indicator, which overlays dock background between them —
  Safari's own toolbar pattern. On phone, **stop and steer render only while they are
  live** (`[disabled]` → `display: none`): a dimmed disc at rest is one more object at the
  bottom of the screen for nothing.

---

## 7. What still needs rules (deferred from the review panels)

The golden + hard-case examples prove the happy/short path and the four hard cases (heavy
subagent fan-out with a failure, error + truncated stderr, inline image, very long output). The
style guide still needs written rules + exemplars for: main-agent error promotion (to the
chrome, not just a row); nested subagents; multiple steers in one turn; a permission/approval
prompt (interactive **and** needs-you — decide which color wins); plan/todo list; diff/patch;
empty/just-started session; stalled agent; daemon disconnect; multi-image gallery;
silent-success tool; interrupted turn; failed-then-retried tool; and **error-findability**
(scroll-track markers + an attention-aware "jump to latest").

**Resolved: the agent asking the user a question.** Mockup 16
(`mockups/16-blocking-needs-you.html`) resolved this exactly as its own recommendation footer
prescribed: Alt A's amber-container/blue-button split is the base grammar (needs-you claims the
frame, the one button that unblocks stays blue/primary), Alt C's docked bar is why the ask can
never hang off-screen, and Alt D's quick-reply chips are the answering surface; Alt B (all-amber)
was rejected. Shipped as the `ask_user` feature (`renderer.js` `markAgentQuestion` +
`renderNeedsYouDock`/`jumpToAgentQuestion`). `notifications.js`'s transition table, which already
fired on `idle→awaiting`, now also fires on `active→awaiting` so a question posted mid-turn
raises the OS notification too.

---

## 8. Mobile (≤767px)

> Breakpoint context (2026-07-19): this section is the **phone** band. The full ladder is
> phone ≤767px · tablet 768–1199px · desktop 1200–1799px · wide ≥1800px (§4).

The hub must work on a phone, not just survive on one. The desktop two-pane layout collapses to
a single-pane workspace; the sidebar becomes an off-canvas drawer behind the header hamburger.

- **One column.** `#workspace` is the full viewport. The sidebar is `position: fixed`,
  `translateX(-100%)`, revealed by `body.app[data-sidebar-open]` over a scrim. Anything that is
  only meaningful in the two-pane layout (the sidebar drag-resizer, side panes, the pane
  splitter) is **`display: none`** here — an in-flow, full-height element left behind shoves the
  workspace a whole viewport down (the "blank phone screen" bug).
- **Reclaim width.** Desktop reserves a wide left gutter (tool indent + 32px card/system
  indents). On phone that wastes a quarter of the screen — tighten conversation padding to
  `--s3` and pull the indents in (`--tool-indent: 22px`, cards/system to `--s4`). Prose and
  cards use the **full** width.
- **Never scroll sideways.** Long unbroken machine tokens (identifiers in inline code, paths,
  notification chips) must wrap (`overflow-wrap: anywhere`); flexed labels need `min-width: 0`.
  The conversation column itself is `overflow-x: clip` as a permanent guard — a `<pre>` of raw
  output keeps its **own** contained `overflow-x: auto`, but nothing forces a column-wide
  horizontal scrollbar.
- **Touch.** `--tap-min: 44px` on phone (desktop is 32px); suppress sticky `:hover` backgrounds
  under `@media (hover: none)` so a tapped row doesn't stay lit.
- **Reading scale on a phone (2026-07-11 round 2).** The transcript follows the same
  one-size rule as desktop — flowing text `--text-base`, mono machine text `--text-sm`,
  meta `--text-xs`. Compact density shrinks only the app **chrome** (`body` drops to
  `--text-sm`); the `.conversation` column keeps `--text-base` and is never re-tiered
  per-kind. Editable fields are pinned to **16px** so iOS never zoom-jumps on focus.
- **Editable fields ≥ 16px.** iOS Safari zooms the page into any focused field whose text is
  smaller than 16px and does not zoom back out — so the composer, prompt, and search inputs are all
  16px on phone.
- **Short forms.** For creation flows like `/new`, use the mobile form rules in §11.

## 9. Motion

Principle #5 (minimal, purposeful motion) stands, sharpened — **motion marks state changes; it
never runs ambiently.** A looping animation that plays while the agent is "working" is forbidden:
it is indistinguishable from a hang (principle #7). Everything here collapses to instant under
`@media (prefers-reduced-motion: reduce)`.

- **One easing.** `--ease: cubic-bezier(0.22, 0.61, 0.36, 1)` (crisp ease-out: reacts fast,
  settles soft) on three durations `--motion-fast 110 / -base 180 / -slow 260`. `--ease-emphasis`
  (faint overshoot) is reserved for press/pop moments — used sparingly.
- **Where motion is allowed:** opening a session (one-shot transcript + welcome reveal); a
  control acknowledging a press (small settle); a quiet hover wash on an interactive row; a
  panel/drawer sliding; the "↓ N new" pill popping in; the streaming caret and the single live-dot
  breathe. That is the whole budget.
- **Where it is forbidden:** per-token reflow on live append; any infinite loop that implies
  progress; blinking/flashing; motion on the user's own messages.

## 10. Implementation status & dev workflow

The shipping UI now carries the token foundation **and** the conversation-first grammar: assistant
prose is the reading hero (size + leading + paragraph rhythm); tool calls are quiet one-line rows
whose verbose per-call intent recedes to a single dim clamped breadcrumb (full text on
hover/expand); turn boundaries breathe; mobile is single-column and overflow-proof; the motion
layer above is in place, and the agent-question edge case from §7 has shipped. The 2026-07-11
typography pass landed the one-column marker gutter and hover/focus-revealed timing metadata;
round 2 (same day) squashed the column to ONE flowing reading size (`--text-base`, prose
included) with mono `--text-sm` and meta `--text-xs` as the only exceptions, and normalized
every transcript disclosure to the sidebar chevron idiom — a `›` glyph, `--text-muted`,
`--text-md`, rotating 90° when open, on a hit target of at least 24px (`.tool-disclosure`,
`summary` fold markers, and the shared `.fold-chevron` span for text-labelled folds). All
locked by `cmd/serf-hub/jstest/test-transcript-typography.js`. The rest of §7's deferred list is still
open.

**Dev loop:** set `SERF_HUB_ASSETS_DIR=<repo>/cmd/serf-hub` when launching `serf-hub` to serve
`assets/` and re-parse `templates/` from disk — CSS/JS edits take effect on reload (templates on
restart) with no rebuild. Unset in production; assets ship embedded.

## 11. Mobile forms

For short, committed creation flows like `/new`, the mobile form is a single column with one clear hierarchy: the prompt first, the config as a stack of full-width rows, and a pinned bottom action band.

### Form rows

On phone, configuration options become full-width rows rather than a pile of chips:

- **Min-height:** `48px` (on top of `--tap-min` 44px).
- **Label:** sans, sentence case, `--text-base` minimum, left.
- **Value:** sans, `--text-base` or `--text-md`, right, truncated.
- **Caret:** at the far right; the whole row is the hit target.
- **Separator:** 1px `--line` hairline.
- **No** ALL-CAPS, monospace, letter-spacing, or per-row boxes.

This is the same settings-row idiom the user already knows from system settings, rendered in the existing Serf neutral palette.

### Auto-expanding text inputs

Textareas that grow with content stay visible without a manual resize handle or a permanent oversized field:

- **Min-height:** `96px`.
- **Max-height:** `40vh` or `8` lines, whichever is smaller.
- **Font-size:** `16px` so iOS never auto-zooms on focus.
- **Motion:** height changes only when `prefers-reduced-motion` allows.

### Bottom action band

The primary action for a mobile form lives in a fixed bottom band:

- Sits on top of `env(safe-area-inset-bottom)`, not floating above it.
- Background: `--bg-raised`; top border: 1px `--line`.
- Primary button: at least `52px` tall, accent background.
- Secondary attach/action button: at least `44px` tall.
- No shadows unless the band is a true overlay.

### Mobile pickers

When a form row opens a selector, it uses a **bottom sheet** anchored to the bottom of the viewport. Sheet rows are `48px` minimum, sans labels, grouped by plain headings, with a large `Done` action.

### Retired for mobile forms

The ALL-CAPS-mono `<details>` summary, the 10px chip labels, the 9px caret-only hit target, and keyboard-hint labels inside buttons.
