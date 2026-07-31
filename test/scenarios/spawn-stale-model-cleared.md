# spawn-stale-model-cleared: spawn pane drops retired model + sweeps siblings

**What this covers**: katas `bvfh` (commit `a1bb5df`) + `hnvv`
(commits `552a788`, `af47928`). Before bvfh, the `/new` form pre-filled
its remembered model blindly — when the harness retired the model, the
user submitted, got a 503, and had no inline signal. After bvfh, the
pane validates the remembered value against the live harness list and
shows an inline notice. After hnvv, the sweep also walks the
per-project blobs and strips stale `.model` keys, preserving sibling
defaults.

The legacy `spawn.js` is gone (`660376f78`) but its **key layout was
carried forward verbatim**, on purpose — `panes/spawn/spawnDefaults.ts:1-25`
says so in its own header ("matching the legacy spawn.js key layout").
So this card's storage assertions are unchanged; only the DOM half was
rewritten.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map and the "Seeding preferences before the first load" recipe.
Two things this card used to assume are wrong now and are the likely
cause of a false pass: there is no `[data-chip-value-model]` and no
`[data-model-prefill-notice]` anywhere in the tree. The notice is a
`role="status"` region (`panes/spawn/Spawn.tsx:490-501`) and the model
control is the shared ARIA combobox in `widgets/modelCatalog/` — its
**closed trigger is a `<button>` whose text is the qualified
`provider/model`, or the empty label when nothing is set**
(`widgets/modelCatalog/index.tsx:380-398`).

**Namespace warning**: these keys are `serf-hub.spawn-defaults.*`, a
*different* namespace from the `serf.prefs.*` flat keys the runbook's
seeding section describes. Unlike `serf.prefs.*`, they are read on the
spawn pane's own mount, not once at module load — but the sweep still
runs mount-only (deps `[]`, `Spawn.tsx:275`), so seed before you open
`/new` and do a full reload rather than an SPA navigation.

## Pre-state

- Hub running on an isolated `$HOME` and a kernel-assigned port (see the
  Setup checklist in `docs/agentic-testing.md`), with `--serf`
  resolvable so the harness enumerates models. This matters — kata
  `6bdb`: with no `serf` on PATH and no `--serf`, the list is empty and
  `validateSerfLaunchModel` fails open, so nothing is ever classified
  stale and the sweep silently does nothing.
- A frontend built with `make build-web` before the hub binary.
- Browser authed via `/auth?token=...`.

## Steps

### Browser-free (pick a genuinely stale value — this is the precondition, and it is where the card usually goes wrong)

1. `GET /api/models` (bare array; add `?diagnostics=1` for the envelope)
   and choose a `provider/model` string where the **provider appears**
   and the **model does not**. That distinction is the whole
   classification: `modelValidityAgainstList`
   (`panes/spawn/spawnDefaults.ts:166-176`) returns `"stale"` only when
   the provider was seen, `"unknown"` when it wasn't — and `unknown` is
   deliberately left untouched, because the hub can't prove it wrong.
   A value with no `/` at all is `"malformed"` and is also swept.
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/models" \
     | jq -r '[.[].provider] | unique'          # providers that ARE enumerated
   ```
   `openai/gpt-5-mini` works when `openai` is live; substitute whatever
   holds on this hub. If you pick a provider the hub doesn't enumerate,
   the sweep correctly does nothing and the card reads as a regression
   that isn't there.

### Browser

2. Open `/new` once so the origin is right, then seed both key shapes
   and reload:
   ```js
   // GLOBAL_MODEL_KEY - a plain string, never JSON (spawnDefaults.ts:20)
   localStorage.setItem("serf-hub.spawn-defaults.global.model", "openai/gpt-5-mini");
   // A per-project blob, keyed by working dir (defaultsKeyFor, :71-74).
   // access_mode is a real declared sibling (SpawnDefaultsBlob, :27-33) -
   // it is what proves the sweep strips .model surgically rather than
   // deleting the whole blob.
   localStorage.setItem(
     "serf-hub.spawn-defaults./tmp/some-project",
     JSON.stringify({model: "openai/gpt-5-mini", access_mode: "full"})
   );
   ```
3. Full reload of `/new` (not an SPA navigation — the sweep effect is
   mount-only).
4. Wait for the sweep's own `model/list` RPC to resolve
   (`Spawn.tsx:261-270`) — a second or two. It is an **appwire call over
   the WebSocket with no harness/cwd**, not the `/api/models?cwd=…` REST
   call the picker uses for enrichment; if the socket never hydrates,
   nothing is swept.
5. Read the result:
   ```js
   ({
     port: location.port,
     mounted: !!document.querySelector('[data-testid="spawn-prompt-card"]'),
     // Scope to the top-level Model field's own container rather than
     // scanning every button on the page: Advanced options can render its
     // own model-valued fields from the daemon schema, and those are the
     // same widget (Spawn.tsx:620-631).
     trigger: document.getElementById("spawn-model-label")
       ?.parentElement?.querySelector("button")?.textContent.trim(),
     globalDefault: localStorage.getItem("serf-hub.spawn-defaults.global.model"),
     perProject: localStorage.getItem("serf-hub.spawn-defaults./tmp/some-project"),
     notice: document.querySelector('[role="status"]')?.textContent?.trim(),
   })
   ```

## Expected

- **Step 5 `globalDefault`**: `null`. The global key is removed outright
  (`sweepStaleModels`, `spawnDefaults.ts:186-193`).
- **Step 5 `perProject`**: `{"access_mode":"full"}` — `.model` stripped,
  the sibling preserved, the blob NOT deleted (`:220-223`; a blob left
  with no other fields *is* deleted, which is why the sibling matters).
- **Step 5 `trigger`**: no longer contains `openai/gpt-5-mini`. It reads
  either `(default) — change model` or `Choose a model — change model`
  — both are correct, and which one you get is not this card's business:
  the empty label is `(default)` (`widgets/modelCatalog/index.tsx:328`)
  unless the hub confirmed it has no default model to fall back on, in
  which case the spawn pane overrides it with `Choose a model`
  (`MODEL_CHOOSE_LABEL`, `Spawn.tsx:101,630`, kata `xgk8`). The `— change
  model` suffix is the trigger's screen-reader text
  (`index.tsx:397`) and is what makes the button findable without a
  testid.
- **Step 5 `notice`**: contains the literal substring
  `Discarded last-used model openai/gpt-5-mini — no longer offered by
  this hub.` — **no backticks around the model name**
  (`Spawn.tsx:492`). The region also holds a `Dismiss notice` icon
  button (`:494`).
- Falsification: the trigger still shows `openai/gpt-5-mini`;
  `globalDefault` still contains it; `perProject` still has a `model`
  key, or lost `access_mode`; or the notice never appears despite the
  storage having been cleared (the sweep ran but the pane didn't tell
  the user — that is the bvfh regression, not merely a cosmetic one).

## Cleanup

```js
localStorage.removeItem("serf-hub.spawn-defaults.global.model");
localStorage.removeItem("serf-hub.spawn-defaults./tmp/some-project");
```

Then kill the hub by the PID you captured and remove your `$run` dir.

## Sharp edges

- **The notice only fires for the model this pane actually pre-filled.**
  `sweepStaleModels` returns every discarded value, but the notice is set
  only when `defaults.model` — the value the pane resolved for *this*
  working directory — is among them (`Spawn.tsx:265-268`). Sweeping a
  stale model out of some *other* project's blob clears storage silently
  and correctly. Assert the notice against the key the pane resolved,
  not against any key you seeded.
- **Any subsequent model pick clears the notice.**
  `handleModelChange` calls `setStaleNotice(null)` for any non-empty
  value (`Spawn.tsx:359-361`), so opening the picker to "check the
  chip" and picking something destroys the evidence. Read the notice
  before touching the picker.
- The sweep filters on the `serf-hub.spawn-defaults.` prefix and skips
  the three known global scalar keys — `global.model`,
  `global.working_dir`, `global.last-working-dir` (`SCALAR_KEYS`,
  `spawnDefaults.ts:19-25`) — because those are plain strings, not
  `{model}` blobs. A key seeded with the wrong prefix, or a blob seeded
  under one of the scalar names, won't be swept.
- Validation is asynchronous and **fails open at every layer**: a
  rejected `model/list` leaves the defaults alone (`Spawn.tsx:270`), and
  an unenumerable provider classifies as `unknown` rather than `stale`.
  So "nothing was swept" is the same observation for a regression and
  for a hub that couldn't answer. Step 1 exists to tell those apart —
  do not skip it.
- The unit-level half of this rule is already pinned by
  `panes/spawn/spawnDefaults.test.ts` (`describe("modelValidityAgainstList
  …")` at `:142`, `describe("sweepStaleModels …")` at `:160`). If the
  browser half fails but those pass, suspect the wiring in `Spawn.tsx`
  or the `model/list` round-trip, not the classification.
