# turn-meta-badge-always-visible: the per-turn duration/tokens/cost badge renders always-visible, not hover/focus-reveal

**What this covers**: the `.turn-meta` badge `cmd/serf-hub/assets/renderer.js`
attaches to the assistant message that closes a turn, once `turn/completed`
lands over appwire. ⚠️ Plan correction (already applied before Phase T ran,
commit `09ead1c4`): earlier spec drafts described this badge as
hover/focus-reveal, matching `.tool-call .tool-meta`'s OLD behavior. Both were
reverted to always-visible for accessibility. This card asserts the
CURRENT, SHIPPED behavior: no `tabindex`, no hover/focus CSS gate, the badge
is in the DOM and visually rendered at rest.

## Pre-state

- Hub running, isolated instance recommended.
- A completed turn on a live session (real model call, so `durationMs`,
  `usage`, and `cost` are real numbers, not fixture data).
- Browser authenticated against the hub.

## Steps

1. Send one prompt to completion.
2. In the browser, locate the assistant message that closed the turn:
   `document.querySelector('.assistant-message[data-turn-id]')`.
3. Find its `.turn-meta` child. Without moving the mouse or focusing
   anything, read `getComputedStyle(meta)`.
4. Confirm the badge has no `tabindex` attribute and the CSS rule for
   `.assistant-message .turn-meta` in `style.css` carries no `:hover`,
   `:focus`, `:focus-within`, or `opacity: 0` gate.

## Expected

- Step 3: `getComputedStyle(meta).display !== "none"`,
  `getComputedStyle(meta).visibility === "visible"`, and
  `getComputedStyle(meta).opacity` is the rule's steady-state value (not
  `"0"`) — all true AT REST, before any hover/focus interaction.
  Falsification: the badge is present in the DOM but
  `display:none`/`visibility:hidden`/`opacity:0` at rest, and only changes on
  `:hover`/`:focus` — that would mean the hover-reveal behavior described in
  the superseded spec draft crept back in.
- Step 4: no `tabindex` attribute (confirms it isn't a focusable
  reveal-on-focus target) and the stylesheet rule contains no interaction
  pseudo-classes.

## Cleanup

- Shut down the spawned session.

## Sharp edges

- Do **not** write or assert a hover-to-reveal or focus-to-reveal transition
  here — per the Y2 task brief's plan correction, there is none. If a future
  spec draft resurfaces that wording, it is stale; defer to commit `09ead1c4`
  and the CSS comment at `cmd/serf-hub/assets/style.css` immediately above
  `.assistant-message .turn-meta`.
- **This run's actual coverage**: the `claude-in-chrome` browser tool was not
  connected in this session (three failed `tabs_context_mcp` attempts), so
  steps 2-3 (live DOM + computed-style inspection in a real browser) were
  **not driven live**. Substituted evidence, all against real shipped
  artifacts (not simulated):
  - `cmd/serf-hub/assets/style.css` around `.assistant-message .turn-meta`
    was read directly. The rule is:
    ```css
    /* Per-turn duration/tokens/cost badge: always-visible and subtle, mirroring
       .tool-call .tool-meta (reverted to always-visible on 2026-07-01 for
       accessibility — no hover/focus reveal here either). */
    .assistant-message .turn-meta {
      margin-left: var(--space-2);
      color: var(--text-muted);
      font-family: var(--font-mono);
      font-size: var(--text-xs);
      white-space: nowrap;
    }
    ```
    No `:hover`/`:focus`/`opacity`/`visibility` present anywhere the
    selector is referenced (the only other rule touching `.turn-meta` is the
    Show-cost gate on `.turn-meta .cost`, unrelated to at-rest visibility).
  - `cmd/serf-hub/jstest/test-turn-meta-badge.js` loads the actual
    `renderer.js` (not a mock) in JSDOM, drives a real
    `TURN_STARTED` → `ASSISTANT_TEXT_*` → `TURN_COMPLETED` event sequence,
    and asserts the resulting `.assistant-message[data-turn-id]` gains a
    `.turn-meta` child containing the formatted duration (`4.2s`), token
    counts (`↑100 ↓50`), and cost (`~$0.01`) text — i.e. the badge is
    unconditionally attached and populated the moment `turn/completed`
    lands, with no interaction step in between. This test passed as part of
    the `make lint` jstest gate.
  - If re-running with a working browser: perform steps 2-4 for real against
    a live turn and record the actual `getComputedStyle` values here.
