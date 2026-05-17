# spawn-picker-enter-noop: Enter in model picker selects model, does NOT submit form

**What this covers**: kata `t13x`, commit `956df7f`. Root cause for
the original xj9j symptom. The model picker's search `<input>` lives
inside the spawn `<form>` and had no `keydown` handler — pressing
Enter inside it triggered the form's implicit submit. The fix adds a
keydown listener: Enter selects the first visible model AND prevents
the bubble; Escape dismisses the picker.

## Pre-state

- Hub running with `--serf` so model list enumerates.
- `/new` open in a browser tab.
- Type a non-empty prompt into the textarea (so the empty-prompt
  guard from xj9j doesn't mask this test): e.g.
  `document.querySelector('textarea[name=prompt]').value = "marker text"`.

## Steps

1. Click `button[data-chip="model"]` to open the model picker.
2. Verify the picker is open and the search input has focus:
   ```js
   !!document.querySelector('.chip-picker-search')
   ```
3. Type a substring that uniquely matches one model — e.g. `haiku`
   for `claude-haiku-4-5-20251001`. Use the `type` browser action
   or DevTools input dispatch.
4. Press Enter.
5. Read the current state:
   ```js
   ({
     modelValue: document.querySelector('input[name=model]').value,
     pickerOpen: !!document.querySelector('.chip-picker'),
     url: location.href,
     promptIntact: document.querySelector('textarea[name=prompt]').value
   })
   ```

## Expected

- `modelValue`: the first matching model's `provider/model` string
  (e.g. `anthropic/claude-haiku-4-5-20251001`).
- `pickerOpen`: false. Picker closed itself on selection.
- `url`: still ends in `/new` — NO redirect to `/s/<id>`.
- `promptIntact`: the marker text is still there. The form did NOT
  submit.
- Falsification: URL changed to `/s/<id>` or prompt got cleared, or
  modelValue is still empty.

## Cleanup

- None unless you spawned something accidentally. The point of this
  test is that NOTHING gets spawned.

## Sharp edges

- The Enter behavior selects "first visible model in active
  provider's list" — if your search query matches multiple
  providers but only one is currently active, the picker may switch
  active providers before the Enter fires (see the `input` handler
  in `openModelPicker`). The test's predicted model assumes a stable
  active-provider after the type.
- Escape inside the search input dismisses the picker (the Escape
  branch of the same keydown handler). Worth a separate "Escape
  closes picker without submitting" assertion if you want full
  coverage.
- The defensive guard from `xj9j` (empty prompt → error banner)
  would also have caught the regression's symptom even if `t13x`
  hadn't been fixed — so this scenario's marker-text check
  specifically validates that the FIX is in place, not just that
  the symptom is masked.
