# spawn-keyboard-contract: Enter picks a model, Enter types a newline, only ⌘/Ctrl+Enter spawns

**What this covers**: kata `rjc5`. The spawn pane's whole keyboard
contract, which no card covered after `spawn-picker-enter-noop` was
retired under kata `v0hg` (it asserted a `<form>` implicit-submit hazard
that is structurally impossible — there is no form). Three claims, each
independently falsifiable:

- **Enter in the model picker selects, and stops there.** The picker's
  own keydown consumes it: `pickAt(activeIndex)` then `preventDefault`
  (`widgets/modelCatalog/index.tsx:204-208`), with an exactly-typed id
  or display name as the fallback answer when nothing is highlighted
  (`:209-216`). Picking closes the popover and reports the model up
  through `ModelField` → `handleModelChange`
  (`panes/spawn/Spawn.tsx:358-361`).
- **Bare Enter in the prompt textarea inserts a newline.**
  `handlePromptKeyDown` returns without touching an Enter that carries
  no modifier (`Spawn.tsx:363-369`), so the browser's own default action
  runs. Nothing above the textarea claims the key either: the app's only
  global keydown listeners are ⌘K for the palette
  (`shell/AppShell.tsx:266-281`) and ⌘B for the rail
  (`shell/rail/RailHost.tsx:54-66`).
- **Only ⌘/Ctrl+Enter spawns.** That chord is the one path from a
  keystroke to `handleSpawn` (`Spawn.tsx:365-368`); the `Spawn` button
  is the only other way in.

The reason a stray keypress cannot spawn is structural, not defensive:
`Spawn.tsx` renders no `<form>` and no `onSubmit`, so there is no
implicit submit for a keypress to bubble into, and the picker's trigger
is `<button type="button">` (`modelCatalog/index.tsx:380-382`). Scope
that claim to `Spawn.tsx` and mean it — the pane's Advanced options do
reach a real form, `CollectionEditor`'s add row
(`widgets/collectioneditor/index.tsx:145`), which is where Enter-to-add
is the wanted behaviour. See Sharp edges. Part A checks the structure
directly, and it is what makes the absence assertions in Part B
meaningful rather than lucky.

Unit coverage: `widgets/modelCatalog/modelCatalog.test.tsx:398` ("Enter
picks the highlighted option") pins the picker half in jsdom. This card
proves the same key does not also start a session, which is the half a
component test cannot see.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. This
card names only `spawn-prompt-card`, `spawn-submit`, the picker's ARIA
roles (`role="combobox"` with `aria-label="Model"`, `role="listbox"`,
`role="option"`), and the trigger's own screen-reader text
(`— change model`, `modelCatalog/index.tsx:397`). Anything else, grep
`data-testid` in `cmd/serf-hub/frontend/src` rather than inventing one.

## Pre-state

- A hub built from the branch under test and started on an isolated
  `$HOME` with `-addr 127.0.0.1:0`, reading the port back from the hub's
  own `listening on 127.0.0.1:<port>` log line — never a port written
  into this card or handed over in a dispatch prompt. Full recipe: the
  Setup checklist in `docs/agentic-testing.md`. Assert `location.port`
  inside every `eval` before trusting what it returns.
- `make build-web` **before** the hub binary, or the hub embeds
  `dist/PLACEHOLDER` and serves no app at all.
- At least one launchable model in the picker (`GET $HUB/api/models`
  non-empty). Note one qualified `provider/model` id from it; the steps
  below call it `$MODEL`.
- A working directory that exists, so the cwd preflight in `handleSpawn`
  neither aborts nor opens the "create it?" dialog.
- Your own Chrome profile (`set_profile <worktree-name>`) before the
  first `use_browser` call of the run (kata `8ecz`).

## Steps

### Part A — the structural guard (browser-free, run it first)

1. Confirm the surfaces this card drives still have nothing that could
   implicitly submit, and that the one form the pane can reach is the
   Advanced-options add row and nothing else:
   ```bash
   cd cmd/serf-hub/frontend/src
   # (a) the two files this card drives own no form and no submit handler
   grep -n "<form\|onSubmit" panes/spawn/Spawn.tsx widgets/modelCatalog/index.tsx
   # (b) nothing under either directory renders a <form> of its own
   grep -rn "<form" --include="*.tsx" panes/spawn/ widgets/modelCatalog/
   # (c) the only form the pane can reach arrives through an imported
   #     widget, and it is that widget's add row
   grep -n "CollectionEditor<" panes/spawn/AdvancedOptions.tsx
   grep -n "<form" widgets/collectioneditor/index.tsx
   # (d) the picker's trigger could not submit one even from inside it
   grep -n 'type="button"' widgets/modelCatalog/index.tsx
   ```

### Part B — the live contract (browser)

2. Navigate to `/auth?token=$TOKEN&next=/new` and wait for
   `[data-testid="spawn-prompt-card"]`. Set the working directory to the
   path from Pre-state that exists, and leave Advanced options closed
   (see Sharp edges — that section is the one part of this pane with a
   real form in it, and it is not what this card is about).
3. Snapshot the hub's session set, so "nothing spawned" is a comparison
   rather than a feeling:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/tree" \
     | jq -c '[.. | objects | .ref? // empty] | unique'
   ```
4. Open the picker and pick with Enter. The trigger is the only button
   on the page carrying the screen-reader text `— change model`; assert
   that before clicking it, because the pane also renders a hidden
   mobile config block (`[data-testid="spawn-mobile-config"]`):
   ```javascript
   (() => {
     const triggers = Array.from(document.querySelectorAll("button"))
       .filter((b) => b.textContent.includes("change model"));
     if (triggers.length !== 1) return { port: location.port, triggers: triggers.length };
     triggers[0].click();
     return { port: location.port, triggers: 1 };
   })()
   ```
   Then type a few characters of `$MODEL` into the focused
   `[role="combobox"][aria-label="Model"]` input, confirm a
   `[role="option"]` row is highlighted (the input's
   `aria-activedescendant` names it), and press **Enter as a real key
   event** (see Sharp edges: a dispatched `KeyboardEvent` is not enough
   for the newline step, so use one mechanism throughout).
5. Read the outcome, then re-run step 3's snapshot:
   ```javascript
   ({
     port: location.port,
     path: decodeURIComponent(location.pathname),
     // The trigger's own text is the value plus the screen-reader
     // "— change model" suffix: assert `contains`, never `equals`.
     trigger: Array.from(document.querySelectorAll("button"))
       .find((b) => b.textContent.includes("change model"))?.textContent,
     listbox: !!document.querySelector('[role="listbox"]'),
     options: document.querySelectorAll('[role="option"]').length,
     toasts: document.querySelector('[aria-label="Notifications"]')?.textContent ?? "",
   })
   ```
6. The fall-through case, which is where an implicit submit would show
   itself: click the trigger again to re-open the picker, type a string
   no model matches (`zzzz-no-such-model`), and press Enter. There is no
   pickable row, so `pickAt` returns false and no exact match is found —
   the handler calls no `preventDefault` at all and the key reaches the
   document. Read the same probe as step 5, then re-snapshot step 3.
7. Close the picker (`Escape`), **click the prompt textarea to focus
   it** — picking deliberately does not return focus to the trigger — and
   type `line one`, press Enter, type `line two`:
   ```javascript
   ({
     port: location.port,
     path: decodeURIComponent(location.pathname),
     value: document.querySelector('[aria-label="Prompt"]')?.value,
   })
   ```
   Re-snapshot step 3.
8. With the same textarea focused, press ⌘Enter (macOS) or Ctrl+Enter.
   Wait for the URL to leave `/new`, then re-snapshot step 3. The one new
   ref names the session this card spawned; its part after `local:` is
   the `$SID` Cleanup shuts down.

## Expected

- **Step 1 (structure)**: (a) and (b) print nothing and exit non-zero;
  (c) shows the `CollectionEditor` call sites in `AdvancedOptions.tsx`
  and the one `<form onSubmit=…>` at `collectioneditor/index.tsx:145`;
  (d) shows `type="button"` on the trigger. Falsify: any hit from (a) or
  (b). A form wrapped around the prompt or the model field reintroduces
  exactly the hazard kata `t13x` removed, and every absence assertion
  below stops proving anything until it is gone again — so if this fails,
  stop and report it rather than continuing to Part B.
- **Step 4-5 (picker Enter selects)**: exactly one trigger matched.
  `listbox` is false (the popover closed), `trigger` contains the
  `provider/model` id of the highlighted row, `path` is still `/new`,
  and the step-3 snapshot is **identical** to the baseline. Falsify:
  `path` decoding to `/s/local:<id>`, a new ref in the snapshot, or the
  trigger still showing the previous value.
- **Step 6 (unmatched Enter does nothing)**: `trigger` unchanged from
  step 5, the picker still open (`listbox` true with `options` 0 — the
  list is always rendered while the panel is open, it just has no
  pickable rows), `path` still `/new`, snapshot still identical, no
  toast.
  Falsify: any new ref, or a navigation. That combination — a keypress
  the app deliberately does not consume, on a pane with a model chosen
  and a prompt that could be submitted — is the exact shape of the old
  accidental-submit bug.
- **Step 7 (bare Enter types)**: `value` is exactly `line one\nline two`
  — one literal newline, both lines present. `path` still `/new` and the
  snapshot still identical. Falsify: a value with the newline missing
  (something consumed the key), or any navigation/new ref (something
  submitted on it).
- **Step 8 (the chord spawns)**: `path` decodes to `/s/local:<SID>`, the
  step-3 snapshot gains exactly one ref, and the new session's transcript
  carries the two-line prompt as its first `USER_INPUT`. Falsify: no
  navigation (then the chord is broken and steps 5-7 proved nothing —
  every one of them would also pass if spawning were dead), or more than
  one new ref.

## Cleanup

- `POST $HUB/api/sessions/local:$SID/shutdown` for the session step 8
  spawned. The old `$HUB/s/$SID/shutdown` shim is gone and 404s
  silently, leaving the daemon running.
- Kill the hub by the PID you captured — never `pkill -f serf-hub`,
  which also kills every concurrent agent's test hub — and `rm -rf` your
  own `$run` directory.

## Sharp edges

- **A synthetic `KeyboardEvent` cannot prove step 7.** An event built
  with `new KeyboardEvent("keydown", …)` and `dispatchEvent` is
  untrusted, so the browser performs no default action: React's handler
  runs, but no newline is inserted and `value` never changes. Drive
  these keys through the browser tool's real keyboard input. If you only
  have synthetic events, the honest degraded assertion is
  `event.defaultPrevented === false` after dispatch — that proves the
  handler declined to consume Enter, which is the app's half of the
  contract, and says nothing about the newline. Report it as the
  weaker check rather than as step 7.
- **Assert the positive edge before the absence.** "No session was
  created" is only meaningful once the keypress has demonstrably landed:
  wait for the trigger's value to change (step 4), for the typed query
  to appear in the combobox input (step 6), or for the textarea value to
  grow (step 7), and only then compare snapshots. A snapshot taken
  before the app reacted at all passes for the wrong reason.
- **Picking does not restore focus to the trigger.** `pick` closes the
  popover without refocusing, on purpose — a keyboard user tabbing
  onward should not be yanked back (`modelCatalog/index.tsx:358-364`).
  `Escape` and outside-click go through `closePicker`, which does
  refocus. So after step 4 focus is not on the textarea; click it before
  typing or step 7 measures an unfocused field.
- **⌘/Ctrl+Enter is gated by the same guards as the button.**
  `handleSpawn` returns immediately when a spawn cannot succeed — a
  re-entrant call (`busyRef`), or a hub with no default model and
  nothing picked (`modelRequired`, kata `xgk8`) — with no toast in the
  second case, because the field's own inline note already says why.
  Pick a model in step 4 and the chord in step 8 has a clear path;
  skip it on a hub with no default and step 8 fails for a reason that
  has nothing to do with the keyboard.
- **A pending image attachment turns the chord into a toast.** With an
  attachment still encoding, `handleSpawn` pushes `Image attachment is
  still processing.` and returns. Attach nothing in this card.
- **The post-spawn URL percent-escapes the colon.** `paneToURL` builds
  `/s/${encodeURIComponent(ref)}` (`shell/routing.ts:93-96`), so
  `location.pathname` reads `/s/local%3A<SID>` after step 8 while a
  hand-typed one keeps the literal colon. Decode before comparing.
- **Advanced options is the one place on this pane with a real form, and
  it is meant to be there.** Every list field there is a
  `CollectionEditor` (`panes/spawn/AdvancedOptions.tsx:356,423,461,495`)
  whose add row is a `<form onSubmit=…>`
  (`widgets/collectioneditor/index.tsx:145`) — Enter in that row adds the
  item, exactly as documented at `:86`. The `modelList` field even puts a
  `ModelCatalog` inside it (`AdvancedOptions.tsx:436-445`), and Enter
  there still picks rather than submits for two independent reasons: the
  picker's panel is portaled to `document.body` by `Popover`
  (`widgets/popover/index.tsx:181`) so the input is not a DOM descendant
  of the form, and the picker's own Enter case calls `preventDefault`
  when it picks. Keep Advanced options closed for this card; if you want
  to exercise that composition, it is a different scenario, and
  `dirListSetting.test.tsx`'s "Enter on a directory row descends without
  submitting the add row" is its unit-level precedent.
- **The desktop and mobile config blocks both live in the DOM.** Only
  the desktop one renders the `ModelCatalog` trigger; the mobile rows
  open a `Sheet` holding the panel directly
  (`panes/spawn/MobileSettingRows.tsx:285-306`). At a desktop viewport
  the sheet is closed, so exactly one `— change model` button and one
  `[role="combobox"]` exist — step 4 asserts that rather than assuming
  it, because a second match means you are driving the wrong one.
