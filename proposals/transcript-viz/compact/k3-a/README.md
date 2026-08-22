# Compact realtime visualizations — proposals/transcript-viz/compact/k3-a

Round 3 answer to the owner's verdict "all too big": the same three visual ideas re-invented
as **always-on companion components** under hard pixel budgets, each shown *in situ* beside a
dimmed mock transcript column. All three replay the same deterministic simulated event stream
of session `0J8XKM` (278 turns, ~1,677 tool calls, 5 delegates/3 levels, 6 jobs, 2 watches
incl. a 47-fire runaway, 3 compaction folds, build-break → edit_file loop → retried 529
failure, 1.19M tokens, 6h42m). Transport (play/pause, 1×/4×/16×, restart, seek, clock) lives
in page chrome **outside** the component footprint. Animation only ever marks in-flight work
(running turn/tool/delegate/job) — completed history is static.

**User-prompt guarantee (all three):** every `USER_INPUT` and `STEERING` turn gets its own
marker that is rendered last (on top), at a fixed minimum glyph size with a ≥10px click
target, and never aggregates away or scrolls out — the component always shows the whole
session-so-far, so every prompt so far is always one click away. Clicking a marker scrolls
the mock transcript to that exact prompt and flashes it (verified in Chrome: idea-1 prompt
#205, idea-2 prompt #120, idea-3 prompt #260).

---

## Compact-1 — Micro Strata Rail (`idea-1.html`) · footprint: **120px wide × full viewport height** (vertical rail)

**Realtime question:** "What is it doing now, is anything looping/runaway/stalled, how far
through are we, where are the anomalies, what's about to fold?" — answered from the rail
alone. **Encoding:** five micro-lanes over 1-stratum-per-turn (WHO 6px kind color · TOOL
DENSITY 38px teal alpha ramp · DELEGATES 20px violet brackets, indent = tree depth · JOBS
14px green spans, red ✕ on failure, dashed = detached · EVENTS 24px watch ticks / error
diamonds / model-switch diamond), full-width violet bars for folds, a white head line with
edge caret for NOW, an animated dashed stratum for the in-flight turn, and a footer strip
with context micro-bar (fold threshold tick), budget micro-bar, live dot, turn number, and
three verdict glyphs (⟳ loop · ⚠ runaway · … stall) that light warn/bad. **Prompt markers:**
blue (user) / amber (steering) chevrons in the left gutter, min 9px tall with 12px hit
zones; hover names the prompt, click jumps the transcript; a proximity snap (±8px) keeps
them clickable no matter how dense the strata get. **Update model:** append-only — strata
appear at the head, spans extend, footer gauges move; the past never repaints. **Scaling:**
strata compress to texture but folds/brackets/anomalies stay separable at 278 turns (see
crop); prompts and verdicts are count-based, not density-based, so they never wash out.

## Compact-2 — Session Ribbon (`idea-2.html`) · footprint: **full width × 88px** (horizontal strip)

**Realtime question:** same five, answered from a bottom-edge ribbon that never takes the
transcript's space. **Encoding, top to bottom:** a 16px context-pressure band whose cliffs
*are* the folds (violet full-height notches repeat them); a 26px turn barcode (one column
per turn, kind colors, teal alpha = tool density, animated column = in-flight); a 14px actor
lane (violet delegate brackets on two depth rows, green job pills, red ✕); a 12px event lane
(amber watch ticks, red error diamonds, violet model-switch diamond); a white NOW caret; and
a fixed 118px side cluster with live dot, turn #, ctx %, verdict glyphs, and context/budget
micro-bars. **Prompt markers:** full-height 2px blue/amber ticks with a chevron cap, drawn
over all lanes, 10px hit columns with nearest-prompt snap. **Update model:** columns appear
at the caret, the context band grows and cliffs live, spans extend until done. **Scaling:**
at 278 turns each column is ~3.5px — individually resolvable; at thousands they merge into
an honest density texture while prompt ticks, fold notches, and the runaway cluster stay
distinct because they use different visual channels (full-height ticks, lane-crossing bars,
packed amber).

## Compact-3 — Session Coin (`idea-3.html`) · footprint: **300 × 220px** (corner badge)

**Realtime question:** same five, answered by a gauge cluster that floats over the transcript
corner. **Encoding:** an outer progress ring (turns done / total) carrying violet fold
notches, red error notches, an amber runaway arc segment, and a violet model-switch diamond;
an inner context ring that fills blue→violet toward the 93% fold threshold dot and drops
live at each fold (⛛×n counter); orbit dots for delegates (circles) and jobs (squares) —
dim outline = pending, animated blue dashes = active/running, green = done, red = failed; a
center readout showing the live two-letter tool glyph (`gr`, `ed`, `sh`…) or an alert
verdict (⟳ LOOP? · ⚠ RUNAWAY · … STALL?) with turn #; and a right micro-column with a
tok/min sparkline, ctx/budget bars, four verdict chips (⟳ ⚠ … ✕), wall clock, and active/
done actor counts. **Prompt markers:** 4px blue/amber dots on the outermost radius, 16px hit
circles; click jumps the transcript; clicking anywhere else on the ring jumps to the turn at
that angle. **Update model:** arcs extend, notches/dots land at their angles, center glyph
swaps per event. **Scaling:** the ring is O(markers), not O(turns) — a 5,000-turn session is
the same ring with the same 8 prompt dots and ~14 orbit dots; this is the most
length-immune of the three, trading per-turn texture for always-legible state.

---

### Verification evidence (Chrome, file://, 100% zoom)

Per idea: a tight element-crop of just the component at an early sim moment and at a late/
ended moment (animation advances meaningfully: e.g. rail `#11` mostly-empty vs `#278` full
strata with folds/brackets; ribbon `#15 ctx 30%` vs `#278 ctx 70%` dense barcode; coin `#11
composing` vs `#200 gr` incident frame with lit chips vs `#278 done`), plus a real click on
a prompt marker scrolling the transcript to that prompt (checked via DOM: badge, selection,
row position). Bugs found and fixed this round: 1px green job strokes too dim (brightened);
synthetic ENV/HOOK overrides colliding with prompt turns #120/#95 so their transcript rows
didn't read as user prompts (guarded); coin legend line colliding with orbit dots (removed);
coin sparkline samples not rebuilt on seek (fixed); dead code removed. Prior rounds
(`proposals/transcript-viz/k3-a/`, `realtime/k3-a/`) untouched; no git commands used.
