# Session Rail — Transcript Scrollbar + Comprehension View

The Session Rail is a 156px vertical canvas that replaces the session
transcript's native scrollbar in the evener-hub web UI. It encodes, at
once: **when** things happened (true elapsed-time axis, idle voids literal),
**what they cost** (per-turn token strata + cumulative burn line), and
**what went wrong** (error ticks, failed jobs) — and because it *is* the
scrollbar, every glance is one drag or click away from the exact turn.

## What you see

The rail shows only what a live observer at this instant could know — zero
retrospective ink. Sessions start blank; turns, anchors, gaps, and totals
draw in only when their real timestamp arrives.

| Encoding | Meaning |
|----------|---------|
| **IN strata** (blue bars) | Input tokens per turn, log-width scaled |
| **OUT strata** (green bars) | Output tokens per turn, log-width scaled |
| **Σ burn line** (white) | Cumulative token burn, auto-scaled to burn-so-far |
| **Top-cost diamonds** (red) | The 5 costliest turns so far, ranked |
| **Result cliffs** (red bars) | Tool result byte sizes, log-width scaled |
| **Error ticks** (red ×) | Turns with errors or failed tool calls |
| **Prompt anchors** (◆) | User input and user steering — clickable to jump |
| **Job lanes** (green/red) | Background job intervals, colored by outcome |
| **Idle voids** (hatched) | Gaps ≥10 minutes of real silence |
| **Now line** (white hairline) | The current moment; descends in first 10 min, then pins to bottom |
| **Viewport thumb** | The visible transcript range — drag to scroll |

## How to use it

- **Drag the thumb** to scroll the transcript (the rail *is* the scrollbar).
- **Click anywhere** on the rail to jump the viewport there.
- **Click a ◆ anchor** to jump to that user prompt's exact turn.
- **Click a red anomaly** (top-cost diamond or error tick) to jump to that turn.
- **Wheel** over the rail to scroll.
- **Toggle axis** between TRUE TIME (elapsed time) and TURN INDEX (linear
  turn count) — true-time compresses as the session runs long.

## Responsive behavior

The rail width adapts to the pane width via container queries (not window
width — a narrow dockview split on a wide monitor gets the tablet treatment):

| Pane width | Rail width | Encoding |
|------------|-----------|----------|
| ≥900px | 156px | Full (all encodings above) |
| 560–899px | 96px | Reduced (2 axis ticks, larger tap targets) |
| <560px | 40px | Minimal strip (burn line + errors + anchors, drag works) |

## Comprehension View (⌘⇧R or button)

A full-screen overlay showing the session tree's rails side-by-side on one
shared live clock:

- **Parent** session is always leftmost.
- **Subagents** ordered by most-recent-activity with 60s hysteresis (so the
  order doesn't flicker).
- All rails re-scale together so **NOW-lines stay aligned**.
- **END caps** appear only when a session's real end passes.
- Click any rail's anchor/anomaly/point to exit and open that session at the
  exact turn.
- **Escape** to exit.

## Live-faithful semantics

The core principle: the rail shows only what a live observer at this
instant could know.

1. Sessions **start blank**; everything appears only when its real
   timestamp arrives.
2. The axis spans `[session start, max(now, start+10min)]` and
   **re-scales continuously** — you watch the rail compress as a session
   runs long.
3. **No final-total denominators**: "Σ 421,121 tokens so far", never
   "421,121 / 813,765".
4. **Idle voids** hatch only after 10 real silent minutes.
5. **END caps** appear only when the end actually passes.

## Feature flag

The rail is **default-ON on desktop** (≥900px viewport). Toggle it in
Settings → Appearance → Session Rail. On mobile/tablet it defaults OFF
but can be enabled manually.

## Design spec

See `docs/superpowers/specs/2026-08-22-session-rail-design.md` for the full
design spec and `proposals/transcript-viz/combined/index.html` on branch
`wip/session-rail` for the verified reference implementation.
