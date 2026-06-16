# Serf — Live Session Workspace Mockup Brief (SHARED)

You are designing ONE screen: the **live session workspace** of "Serf," an agentic coding tool used by external customers to watch AI agents work in real time. This brief defines the EXACT content and structure every direction must render, so three visual directions can be compared apples-to-apples. You own only the *visual register* (type, color, spacing, motifs, motion) — NOT the content or the information architecture, which are fixed below.

The screen is a two-column app at 1440×900, **dark-first**:
- LEFT: a sidebar (sessions navigator), ~300px.
- RIGHT: the session workspace — title bar, scrolling transcript, composer.

## Non-negotiable design principles (all directions must honor)
1. **Conversation-first.** User + assistant prose are the loudest, most readable thing. Tool calls are visually subordinate.
2. **Tool calls collapse once scrolled past.** Show one CLUSTER of already-finished tool calls collapsed to a single summary line ("✓ 4 steps · …  expand"). Show ONE currently-active tool call expanded.
3. **Subagents are first-class, aggregated.** A single inline "Subagents (N)" panel shows each subagent's lifecycle (running with last-action + elapsed; done with result + duration), with a link affordance into its own transcript. Subagent accent color is distinct from the main agent.
4. **Status lives on a left rail / glyph**, color-coded: working, done, needs-you/error, idle. Scannable down the column.
5. **Steering (a human message mid-run) is LOUD** — the highest-signal interruption; make it unmissable and clearly human-authored.
6. **Liveness is visible.** A working indicator at the streaming tail shows elapsed time and "waiting on N subagents" when the main agent is blocked. Include a "↓ N new" jump-to-latest pill (the user has scrolled up; new content is below).
7. **Mono only for machine text** (paths, commands, model IDs, IDs, counts, code). Sans for everything human (labels, prose, buttons, nav).
8. **Sidebar declutters by recency & kind:** groups LIVE / RECENT / OLDER / TEST RUNS; subagents are de-weighted (dim, single-line, indented under their parent) with a completion glyph; the ~26 disposable test sessions are bucketed into one collapsed "TEST RUNS" group.

## EXACT sidebar content (render all of it)
Top actions row: `＋ New`   `Search ⌘K`   `Settings`

**LIVE (2)**
- ● **Refactor auth token cache** — `serf` · working 1:12   ← SELECTED/active (blue/working color)
- ● **Audit error handling** — `kimi` · needs you   ← awaiting (red/attention color)

**RECENT**
- ▾ **SERF** · 5m   *(has a live child — show a live rollup dot)*
    - ● Refactor auth token cache · 1:12   *(working; this is the open one — mark as selected)*
        - ⤷ trace-callers · ✓ done   *(subagent: dim, indented, connector rail, done glyph)*
        - ⤷ find-tests · ⟳ running   *(subagent: running glyph)*
        - +2 subagents · 2✓   *(collapsed overflow of additional finished subagents)*
    - ∘ Fix flaky CI · 3h   *(idle/ended session)*
- ▸ **KIMI-EFFORT** · 2h
- ▸ **PRIME-RADIANT** · 1d

**OLDER**
- ▸ **serf-docs** · 9d

▸ **TEST RUNS (26)**   *(one collapsed bucket; muted)*

## EXACT workspace content (render in this order, top → bottom)

**Title bar:** `Refactor auth token cache`  ·  model chip `gpt-5.5`  ·  right-side actions: `Details`  `Interrupt`  `Compact`

**Transcript (a "↓ 2 new" jump-to-latest pill floats at bottom-right of this pane):**

1. **User message:** "The auth token cache invalidates too aggressively — valid tokens get evicted early. Find the root cause and fix it, and check how callers use it too."

2. **Thinking block (collapsed, quiet, distinct from assistant prose):** label "Thought for 6s" + faint one-line preview "The eviction probably keys off the wrong timestamp…" with an expand affordance.

3. **Assistant message:** "I'll trace how the cache is used, then fix the eviction logic. Looking at the cache and its callers now."

4. **Collapsed tool cluster (scrolled-past, ONE line):** `✓ 4 steps · read cache.go · grep callers · ran tests · exit 0`  + an `expand` affordance. (This demonstrates principle #2.)

5. **Subagents panel (inline, aggregated, LIVE):** header `Subagents (2) · ⟳ 1 running · ✓ 1 done · 1:04`, then two rows:
   - `✓ trace-callers` · "found 7 call sites" · `3.1s` · (link: view →)
   - `⟳ find-tests` · "running · reading cache_test.go" · `0:48` · (link: view →)

6. **Assistant message (currently streaming — show a caret/cursor):** "Root cause: `evictExpired()` compares against `lastUsed` instead of `expiresAt`, so any token idle longer than the sweep interval is dropped even when still valid. Patching it now▌"

7. **Active tool call (expanded, RUNNING):** `⟳ Patch cache.go` — purpose primary ("Fix the eviction comparison"), command demoted to a mono chip `edit cache.go · evictExpired()`, a running spinner/indicator.

8. **Steering message (LOUD, human):** "↳ steer: also add a regression test for the idle-but-valid case"

**Liveness indicator** (at the streaming tail, before composer): `● working · 1:12 · waiting on 1 subagent`

**Composer (bottom):** textarea placeholder "Message the agent…"; left: model chip `gpt-5.5`, `+` attach; right: `Send` (primary) and a `Send as steer ⌥↵` secondary.

## Technical requirements
- ONE self-contained `.html` file. All CSS inline in a `<style>` block. Minimal vanilla JS only for: expand/collapse the tool cluster + thinking block, and a hover state on the jump pill. No build step, no external JS libs.
- Fonts: load distinctive web fonts via Google Fonts `<link>` (or system fallback). Pick fonts true to your direction — NOT Inter/Roboto/Arial/Space Grotesk. Mono face for machine text.
- Must look polished and intentional at exactly 1440×900, dark theme. Real spacing, real hierarchy, hover/active states, a tasteful page-load reveal is welcome.
- Accessibility: legible contrast (body text ≥ 4.5:1), focus-visible on the composer.
- This is a production-credible coding tool, not an art piece — be distinctive but usable. Avoid AI-slop (no purple-gradient-on-dark cliché, no evenly-timid palette).
