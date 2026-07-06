# Serf Web Hub — Design System & Style Guide

Status: **draft / in progress** (2026-06-16). The canonical reference implementation is
[`examples/01-golden-live-session.html`](examples/01-golden-live-session.html); hard cases
are in [`examples/02-hard-cases.html`](examples/02-hard-cases.html). Open them in a browser —
they are self-contained.

This guide captures the *target* system. The current shipping UI
(`cmd/serf-hub/assets/style.css`, `renderer.js`) already has a solid token foundation; the
work is to enforce the rules below and rebuild the transcript's information design around them.

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
- Scale: `--fs-xs 11` / `--fs-sm 12` / `--fs-base 13` / `--fs-md 15` (hero prose) / `--fs-lg 18`.
- Numbers (durations, counts, relative times) are sans with `font-variant-numeric: tabular-nums`.

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

### Spacing, radius, motion

- Spacing scale: `--s1 4` / `--s2 8` / `--s3 12` / `--s4 16` / `--s5 24` / `--s6 32`. Snap to it.
- Radius: **two only** — `--r 5px` (rectangles) and pill (`999px`, chips/dots). Retire the rest.
- Motion: one gentle breathe (~2.6s) on the live dot; a soft cursor fade. A one-time staggered
  load reveal is fine. Everything inside `@media (prefers-reduced-motion: reduce)` → none.

### Retired from the old UI

ALL-CAPS + heavy letter-spacing as a default label treatment; monospace for chrome/labels; the
imperceptible 5% paper-grain `--noise` texture; the 6-radius spread; `--purple`/`--cyan` as
ambient hues; `--text-dim`-on-dark for primary navigation (failed contrast).

---

## 4. Component grammar (the transcript)

Design rule: **one scannable primary line per entry; status on a left rail or glyph; machine
detail demoted to a mono chip; deep detail hidden until expanded.** Content column capped ~720px.

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
| **Plan / tasks** | ONE **living** card per session — progress (`31/46` + thin neutral meter) + the active task (the one blue ⟳) | `✓ N done` recede-count; `K up next` | Up next in full + the done pile folded behind its count; the whole plan in the sidebar | a single neutral **left rail, no box** (rail = "status", box = "needs-you"). It is rebuilt and floated to the live frontier on each edit — **never one card per edit.** |
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
card disclosures (`raw notification`, `full excerpt`, `show raw error`) read `label … ▸` with the
chevron at the right edge. A left-hung ▸ offsets every row and ragged-edges the column; on the
right it's a quiet "there's more." (On phones the hover-only timing meta is hidden so the command
and file paths get the full row width instead of wrapping mid-word in a squeezed column.)

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

Project header: name (sans, not ALL-CAPS-mono) · relative age · count; rollup dot only when
something is live/needs-you.

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
- **Reading scale (sized to actually read on a phone).** A clear, descending hierarchy — the
  conversation leads, machine text is legible (never the old 10px squint), metadata is quietest:

  | level | size | what |
  |---|---|---|
  | hero | 14 | assistant prose — the thing you read, the largest text |
  | primary | 13 | the agent's tool **purpose**; the user's message |
  | machine | 12 | commands, diffs, output, code (mono) |
  | meta | 11 | the demoted command under a purpose; timings, counts |

  The hero must stay larger than the tool purpose, which must stay larger than the command — an
  inverted step (a 12px hero under a 13px purpose) reads as "the tool matters more than the
  answer." Editable fields are pinned to **16px** so iOS never zoom-jumps on focus.
- **Editable fields ≥ 16px.** iOS Safari zooms the page into any focused field whose text is
  smaller than 16px and does not zoom back out — so the composer, prompt, and search inputs are all
  16px on phone.

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
layer above is in place, and the agent-question edge case from §7 has shipped. The rest of §7's
deferred list is still open.

**Dev loop:** set `SERF_HUB_ASSETS_DIR=<repo>/cmd/serf-hub` when launching `serf-hub` to serve
`assets/` and re-parse `templates/` from disk — CSS/JS edits take effect on reload (templates on
restart) with no rebuild. Unset in production; assets ship embedded.
