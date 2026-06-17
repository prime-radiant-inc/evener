# Serf Web Hub — Mockup Targets (consolidated from the 6 persona panels)

Each TOPIC below becomes one self-contained HTML mockup file showing **≥4 distinct alternatives**,
built on the **golden example's correct `:root` tokens** (4-color system: blue=live/interactive,
amber=needs-you, red=error, neutral=done; mono only for machine text; one containment device;
conversation leads; never emphasize the user's own messages; minimal motion; honest liveness).
Each alternative is a real, reactable rendering — not a description.

Meta-finding (all panels): the shipped UI never adopted the golden grammar, and `style.css` tokens
conflate the 4 meanings. Many topics are "port the golden + explore variants," not greenfield.

## Foundation
1. **Color & status system** — fix the conflated palette (amber≠red, kill green-idle/purple-subagent),
   show the 4 meanings as swatches + every state glyph-paired (colorblind-safe). Alts: (a) strict
   golden 4-color remap; (b) + dual-channel glyph/shape per state; (c) + a11y contrast-fixed ramp
   (retire `--text-dim` as text); (d) tinted-vs-neutral "settled" study.
2. **Chrome & labels** — kill ALL-CAPS-mono labels (the "amateur tell"); top-bar = identity only.
   Alts: (a) sans sentence-case + hairline; (b) sans small-caps; (c) minimal tracked-uppercase sans
   for true dividers only; (d) consolidate meta into a `Details ⋯` overflow.

## Transcript grammar
3. **User message + steering** — demote the loud right-aligned bubble to a quiet `You` tag; handle
   multiple steers; neutral (not amber) steer tick. Alts: (a) golden left `You` tag; (b) gutter
   margin-note; (c) hairline rail only; (d) collapse-long-prompt + stacked "You steered ×N".
4. **Assistant hero & reading hierarchy** — make agent prose win (size+space+contrast); inline-code
   underline not filled chip; turn rhythm. Alts: (a) size bump + user muted; (b) whitespace-led;
   (c) first-sentence lede; (d) contrast-only.
5. **Thinking block** — reflow-free collapse, duration-ranked lines, gist preview, calm streaming.
   Alts: (a) reserved-slot collapse; (b) fixed-height peek window; (c) crossfade to "Thought for Ns";
   (d) duration-weighted prominence + noun-phrase gist.
6. **Tool calls & long output** — cluster summary leads with the consequential step; two-stage
   collapse (further once scrolled past); server-truncation/binary honesty. Alts: (a) mutating-step-
   first summary; (b) verb-count badges; (c) head+tail / error-aware peek; (d) peek/ride/drop tri-state
   + virtualized expand.
7. **System/skill churn & silent-success** — kill divider-weight + "N chars"; quiet one-liners;
   coalesce. Alts: (a) quiet `--ink-3` one-liner; (b) coalesced "N system events"; (c) ✓-only silent
   success; (d) move lifecycle to Details panel.

## Subagents
8. **Subagent module states** — stale-"running"/terminal-unknown, fan-out overflow grid, freeze-done
   churn, failed-child surfacing. Alts: (a) honest-clock demotion + `?` unknown state; (b) columnar
   overflow sorted by severity; (c) one module clock, done rows frozen; (d) glyph-strip sparkline.
9. **Subagent navigation & nesting** — back-out breadcrumb, nested subagents, in/out model. Alts:
   (a) parent breadcrumb banner; (b) slide-over (parent stays); (c) inline accordion expand;
   (d) tabbed session stack + worst-state rollup for nesting.

## Sidebar & wayfinding
10. **Sidebar IA** — resolve flat LIVE-rail vs ACTIVE tier duplication; dedup repeated sessions;
    rollup-magnitude; "you are here" selected row. Alts: (a) delete LIVE, ACTIVE is live home;
    (b) LIVE as count/filter chip; (c) cluster repeated-title sessions; (d) dot+count rollup + `.sel`.
11. **Cross-session attention triage** — aggregate "needs me now" across projects. Alts: (a) top
    "Needs you (N)" tier; (b) header badge + cycle (`n`); (c) sort-awaiting-to-top; (d) collapsed-rail
    attention dot.
12. **Test-runs bucket & finding old work** — make the bucket + RECENT/OLDER findable. Alts:
    (a) searchable bucket (⌘K scoped); (b) date sub-grouping; (c) outcome glyphs; (d) recently-viewed
    history + branch grouping.

## Liveness, motion, scroll
13. **Liveness & motion economy** — one loop max; "no updates for Ns" calm/concern banding; livebar
    placement. Alts: (a) single liveness source (kill cursor blink); (b) quantized "quiet for ~1m";
    (c) last-event label not raw counter; (d) ambient edge-sliver livebar.
14. **New-content pill & error-findability** — no-flicker sticky pill; attention-aware "↓ needs you"/
    "↓ error"; scroll-track markers. Alts: (a) sticky debounced count; (b) split calm/urgent color;
    (c) scrollbar minimap ticks; (d) next-attention cycler.

## Edge & error states
15. **Connection & main-agent errors** — daemon-disconnect vs tool-error; promote turn failure to
    chrome; retry/rate-limit; sans error voice. Alts: (a) chrome reconnect banner + queued send;
    (b) liveness-bar takeover; (c) turn-level red end-cap + Retry; (d) error taxonomy card (provider/
    harness/cancelled) + collapsed retry counter.
16. **Blocking needs-you: permission/approval & agent question** — resolve amber-vs-blue; don't let
    it hang off-screen. Alts: (a) amber container + blue button; (b) all-amber blocking; (c) docked
    approval/answer bar above composer; (d) inline diff/quick-reply-gated.
17. **Context pressure & compaction** (not in §7) — pressure gauge + compaction lifecycle. Alts:
    (a) quiet gauge coloring only near the edge; (b) compaction system-line + expand; (c) transcript
    watermark band; (d) pre-emptive amber nudge.
18. **Rich content: plan/todo · diff/patch · multi-image** — high-info blocks + empty/cold-start.
    (Built as one file with sub-sections, ≥4 alts across the set.) plan/todo: state-glyph checklist /
    progress chip / inline block / pinned rail. diff/patch: collapsed +N−N→unified (desaturated) /
    split / stat-only / inline-in-prose. multi-image: thumb strip / contact sheet / primary+more /
    provenance-grouped. cold-start: optimistic echo / skeleton turn / onboarding / narrated liveness.

Deferred to a later pass if scope is too large: pure-a11y retrofit page (focus ring, hit targets,
aria-live, reduced-motion) — captured here as cross-cutting requirements every mockup must honor.
