# spawn-stale-model-cleared: spawn form drops retired model + sweeps siblings

**What this covers**: katas `bvfh` (commit `a1bb5df`) + `hnvv`
(commits `552a788`, `af47928`). Before bvfh, the `/new` form pre-
filled `serf-hub.spawn-defaults.global.model` blindly — when the
harness retired the model, the user submitted, got a 503, and had no
inline signal. After bvfh, the form validates against the harness
list and shows an inline notice. After hnvv, the sweep also walks
per-project blobs and strips stale `.model` keys (preserving sibling
defaults like `working_dir`).

## Pre-state

- Hub running with `--serf` resolvable (so `/api/models` enumerates).
  This matters — kata `6bdb` shows that without `serf` on PATH or
  `--serf`, the model list is empty and validation silently skips.
- Browser authed (cookie set via `/auth?token=...`).

## Steps

1. Open `/new` in a tab. Confirm the form renders with a chip
   labelled `(pick a model)` if no model default is set.
2. In DevTools console:
   ```js
   localStorage.setItem("serf-hub.spawn-defaults.global.model", "openai/gpt-5-mini");
   localStorage.setItem(
     "serf-hub.spawn-defaults./tmp/some-project",
     JSON.stringify({model: "openai/gpt-5-mini", working_dir: "/tmp/some-project"})
   );
   ```
   `openai/gpt-5-mini` is a retired model name. Substitute any
   provider/model the current harness doesn't enumerate.
3. Navigate to `/new` again (full reload, not just SPA — the spawn.js
   init must fire).
4. Wait ~2-3 seconds for the harness model list fetch to resolve and
   the sweep to run.
5. In DevTools:
   ```js
   JSON.stringify({
     modelDisplay: document.querySelector('[data-chip-value-model]')?.textContent.trim(),
     globalDefault: localStorage.getItem("serf-hub.spawn-defaults.global.model"),
     perProject: localStorage.getItem("serf-hub.spawn-defaults./tmp/some-project"),
     notice: document.querySelector('[data-model-prefill-notice]')?.textContent?.trim(),
   })
   ```

## Expected

- `modelDisplay`: `(pick a model)`. Chip cleared.
- `globalDefault`: `null`. Global key removed.
- `perProject`: `{"working_dir":"/tmp/some-project"}` — `.model`
  stripped; siblings preserved.
- `notice`: contains the literal substring
  `Discarded last-used model \`openai/gpt-5-mini\` — no longer
  offered by this hub.`
- Browser console (`{prefix}-console.txt`) shows a line like
  `Cleared N stale spawn-form model default(s).`
- Falsification: chip still shows `openai/gpt-5-mini` or localStorage
  still contains it — the validate+sweep regressed.

## Cleanup

```js
localStorage.removeItem("serf-hub.spawn-defaults.global.model");
localStorage.removeItem("serf-hub.spawn-defaults./tmp/some-project");
```

## Sharp edges

- The validation is async — it depends on `/api/models?cwd=...`
  succeeding. If the hub can't enumerate models (kata `6bdb`,
  missing `--serf`), validation silently skips and stale defaults
  survive. Verify the model list endpoint first if the test fails
  unexpectedly.
- The sweep filters `serf-hub.spawn-defaults.` prefix and skips
  known global scalar keys (`global.model`, `global.working_dir`,
  `global.last-working-dir`). A key seeded with the wrong prefix
  won't be swept — confirm prefix when seeding.
- The notice card uses the `diagnostic` styling family with
  `severity: note` and `source: hub`. The default hint text for
  hub-source diagnostics ("Check the hub process...") trails the
  notice — verbose but not wrong. Worth a cosmetic kata if it
  bothers a reviewer.
