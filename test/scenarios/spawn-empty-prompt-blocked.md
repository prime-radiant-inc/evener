# spawn-empty-prompt-blocked: empty/whitespace prompt is rejected inline

**What this covers**: kata `xj9j`, commit `7743e7f`. Before the fix,
the spawn form would happily POST `prompt: ""` to `/api/spawn`,
spawning a session with 0 turns that the user couldn't tell apart
from a stuck/dead one. The fix adds a guard in the submit handler
that short-circuits before any fetch, surfaces a `[data-spawn-error]`
diagnostic, and re-focuses the textarea.

## Pre-state

- Hub running with auth set up.
- `/new` open in a browser tab.

## Steps

1. With the prompt textarea empty (or containing only whitespace
   like `"   \n  "`), trigger form submission. Three ways:
   - Click the spawn button. Easiest.
   - Press `⌘↵` (`Cmd+Enter`) — the form has a keyboard shortcut.
   - From DevTools console:
     ```js
     document.querySelector('[data-spawn-form]')
       .dispatchEvent(new Event('submit', {bubbles: true, cancelable: true}));
     ```
2. Read `document.querySelector('[data-spawn-error]')?.textContent`.
3. Confirm the page did NOT navigate to a `/s/<id>` URL.
4. Confirm the textarea got focused (visual or via
   `document.activeElement === document.querySelector('textarea[name=prompt]')`).

## Expected

- The page stays on `/new`. No redirect.
- An error card appears with text containing
  `Prompt is empty. Type something before spawning.`
- The category is `Hub error` / source `hub` (or `spawn error`).
- No request was sent to `/api/spawn`. (Check the network tab or
  hub log — no `/api/spawn` line should appear after the submit.)
- Falsification: a redirect to `/s/<id>` happens, or a 0-turn
  session is created. The guard regressed.

## Cleanup

- Dismiss the error card if a follow-up scenario uses the same tab
  (it's idempotent, but clean is nice).

## Sharp edges

- The guard only `trim()`s for the empty-check; whitespace inside a
  real prompt is preserved in the actual payload. If the user
  intentionally types `"   write me a haiku   "`, the guard does
  NOT eat the surrounding whitespace.
- Root cause for the original symptom (model picker search Enter
  bubbling to form submit) is covered separately in
  `spawn-picker-enter-noop.md` (kata `t13x`).
