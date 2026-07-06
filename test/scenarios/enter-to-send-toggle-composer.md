# enter-to-send-toggle-composer: the Enter-to-send Settings toggle live-changes composer keybinding behavior

**What this covers**: the Enter-to-send toggle (`cmd/serf-hub/assets/settings-display.js`,
default OFF) and its consumer in the composer's keydown handler
(`cmd/serf-hub/assets/renderer.js` `bindInputForm`), resolving the
Shift+Enter/steer keybind collision (commit `4510a984`).

## Pre-state

- Hub running, browser authenticated, a session open with an active or
  recently-active turn (steer only enables with `activeTurnId` set, per
  `web-steer-live-turn.md`'s pattern) — or just a session where you only need
  to check newline-vs-send, which works regardless of turn state.

## Steps

1. With the toggle OFF (default — confirm via `/_partials/settings/display`
   showing `data-composer="enterToSend"` unchecked / "OFF"), focus the
   textarea, type text, press bare Enter. Confirm a newline is inserted (no
   submit).
2. Press Shift+Enter with the queue empty and a turn active. Confirm it
   triggers the steer path (same textarea contents, steer button behavior).
3. Open Settings → Display, toggle "Enter sends" ON.
4. Back in the composer: type text, press bare Enter. Confirm it submits
   (send POST fires, textarea clears).
5. Press Shift+Enter. Confirm a newline is inserted instead of steering.
6. Confirm the steer BUTTON (click, not keybind) still works in this mode.

## Expected

- Step 1: `textarea.value` contains an embedded `\n`, no `/s/<id>/send` (or
  appwire `turn/send`) request fired.
- Step 2: a `.steering` entry appears in the conversation (per
  `web-steer-live-turn.md`'s Path A assertions).
- Step 4: request fires, `textarea.value === ""` after submit.
- Step 5: `textarea.value` contains an embedded `\n`, no steer request fired,
  event's `defaultPrevented === false` at the keydown listener (default
  newline insertion is left alone).
- Step 6: steer button click still POSTs to the steer endpoint regardless of
  the toggle — only the KEYBOARD path changes.
- Falsification for any step: the OFF-mode and ON-mode behaviors are swapped,
  or Shift+Enter does nothing in either mode, or the steer button stops
  working after toggling.

## Cleanup

- Toggle Enter-to-send back to OFF (its default) if using a persistent
  browser profile.

## Sharp edges

- This is the mirror image of `web-steer-live-turn.md`'s Path A — that card
  assumes the toggle's default (OFF) state; this card is specifically about
  what changes when the toggle flips.
- **This run's actual coverage**: the `claude-in-chrome` browser tool was not
  connected in this session, so this card's live keyboard-interaction steps
  were **not driven live**. Backing evidence used instead:
  - `cmd/serf-hub/jstest/test-composer-shortcuts.js` loads the real
    `renderer.js` in JSDOM and drives literal `KeyboardEvent`s at the
    textarea for both toggle states. It asserts, with the toggle OFF
    (default, absent from `localStorage`): bare Enter does NOT submit
    (submit count stays at whatever Cmd/Ctrl+Enter produced), Shift+Enter
    clicks the steer trigger; and with the toggle ON
    (`localStorage.setItem("serf-hub.composer", '{"enterToSend":true}')`):
    bare Enter submits, Shift+Enter does NOT steer and leaves
    `defaultPrevented === false` (so the browser's native newline insertion
    proceeds) — this is the exact behavior this card describes, exercised
    against production code, not a mock. It also asserts the composer's
    `kbd` hint text swaps (`⌘↵`/`⇧↵` when OFF, `↵`/hidden when ON) and that
    pane-iframe mode ignores both shortcuts. This test passed as part of the
    `make lint` jstest gate.
  - `GET /_partials/settings/display` (real HTTP call against the isolated
    hub) confirmed the server-rendered toggle markup and its default state
    and help text:
    ```
    <dt id="lbl-composer-enter-to-send">Enter sends</dt>
    <input type="checkbox" data-composer="enterToSend" ...>
    <span class="state" aria-hidden="true">OFF</span>
    <p class="help">Default off: ⌘/Ctrl-Enter sends, Enter inserts a newline.
    On: Enter sends, Shift-Enter inserts a newline (the steer keyboard
    shortcut is unavailable in this mode — the steer button still works).</p>
    ```
    confirming the shipped copy matches this card's expected behavior
    description.
  - If re-running with a working browser: perform steps 1-6 for real against
    a live turn and replace this note with the observed request/DOM
    evidence.
