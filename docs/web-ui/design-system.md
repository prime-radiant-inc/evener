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
| **Tool call (done)** | the **purpose** ("Check kernel version"), not the command | verb+args as mono chip; "exit 0" | full output | neutral; collapses once scrolled past |
| **Tool cluster (scrolled-past)** | one line: "✓ 4 steps · …" | — | the individual calls | neutral box (the one device) |
| **Tool call (running)** | purpose | mono chip | live output | blue left-rail tick |
| **Tool call (error)** | error summary promoted to primary | mono chip | stderr (truncated, see below) | red left-rail (one device — no extra box) |
| **Subagents** | one module: "Subagents (N) · ⟳ running · ✓ done · ✕ failed" | per-row: name · result · duration · view→ | each subagent's transcript | one neutral box; rows separated by space (no gridlines) |
| **Steering** | quiet: dim "You steered" + muted text, thin amber tick | — | — | demoted (it's the user's message); amber tick only |
| **Image** | inline thumbnail + caption (filename · dims) | provenance (user-pasted / tool-read / generated) | lightbox | neutral card |
| **Long output** | first N lines + "expand · N more lines" | byte/line count | full (with escape hatch for huge) | neutral box; blue "expand" affordance |
| **Liveness** | pinned strip: "working · 1:12 · waiting on N subagents" | — | — | not in scroll flow; always reachable |
| **System / lifecycle** | quiet dim one-liner (plugin loaded, skill activated) | — | — | never divider-weight; no "N chars" |

Status hierarchy: **running (blue) draws the eye; done (neutral) recedes; needs-you (amber) and
error (red) stand out.** Pair every status color with a glyph so it is colorblind-safe.

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
  does **not** live here — no duplication.)
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
the agent asking the user a question; empty/just-started session; stalled agent; daemon
disconnect; multi-image gallery; silent-success tool; interrupted turn; failed-then-retried
tool; and **error-findability** (scroll-track markers + an attention-aware "jump to latest").
