# W7 close fix round — settings cluster — report

Worktree: `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/webui-w7-settings`, branch `w7-settings`.
Starting HEAD: `2a88e83a1` ("webui wave7: controller integration wiring").
Ending HEAD: `249943f94`.
Commit range: `2a88e83a1..249943f94` (6 commits, one per item; item 7 needed no code change).

Baseline confirmed green before any edit: `npx tsc --noEmit` clean, `npx vitest run` → 154 files / 2302 tests,
`npm run lint` (biome ci) clean — matches the brief's stated baseline exactly.

Final state after all 6 items: `npx tsc --noEmit` clean, `npx vitest run` → **154 files / 2332 tests** (+30 net:
+11 item 1, +0 item 2 (behavior-preserving, no new tests), +6 item 3 widget-level +5 item 3 collectionFields
(2 kept, 5 new, net +3) — see item-by-item counts below for the exact per-item deltas — +5 item 4, +1 item 5,
+4 item 6), `npm run lint` clean. `npm run build` run once at the end (see the "Build" section); it deleted
the tracked `dist/PLACEHOLDER` sentinel (vite's `emptyOutDir`), restored via `git restore
cmd/serf-hub/frontend/dist/PLACEHOLDER` per the brief's own instruction — tree confirmed clean after.

Every commit was gated with the AND-chained command the brief specifies
(`npx tsc --noEmit && npx vitest run && npm run lint && git commit ...`), never `;`-separated.

---

## Item 1 — Cross-client staleness subscriptions (headline fix)

**Finding.** `stores/extensions.ts` (marketplaces/plugins/launchLayer) and `stores/credentials.ts`
(provider instances) both ride the shared `AppwireClientLike` connection the same way `stores/tree.ts`
does, but neither ever called `client.onNotification(...)`. The hub genuinely broadcasts settings-mutation
notifications via `BroadcastAll` (confirmed in `cmd/serf-hub/app_rpc.go`: `notifyMarketplaceUpdated`,
`notifyPluginUpdated`, `notifyLaunchUpdated`, `notifyAuthUpdated`) — the wire carries the fix, the client
just never listened. A change in one browser tab (add a marketplace, install a plugin, edit a launch-config
directory list, sign in to a provider) left every *other* already-open tab showing stale data until a manual
re-open of the section. This is the exact "multiple browsers all updated" founding requirement the wave
was built against, violated.

**Change.** Wired `extensions.ts` and `credentials.ts` onto the identical debounced subscribe-and-refetch
pattern `tree.ts` already established for `serf/tree/changed` (module-level `connectionStore.subscribe` +
initial-client check, 250ms debounce per channel, `resetXStoreForTests` now also resets the wiring/timer
state). `extensions.ts` gets three *independent* debounce channels (marketplace/plugin/launchLayer are
unrelated fetches, unlike tree.ts's single shared refetch target) fanning out from one `handleNotification`.
`credentials.ts` gets one channel for `serf/auth/updated`.

**Notification-coverage table.** Enumerated every `NotificationName` in `src/protocol/types.gen.ts`
(28 total) and cross-referenced `docs/appwire-protocol.md`'s own Notifications table plus
`cmd/serf-hub/app_rpc.go`'s broadcast call sites for the ones that are plausibly marketplace/plugin/
settings-related:

| Store | Field(s) | Broadcast | Wired? |
|---|---|---|---|
| `extensionsStore` | `marketplaces` | `serf/marketplace/updated` (add/remove/refresh) | **Wired** (this task) |
| `extensionsStore` | `plugins` | `serf/plugin/updated` (install/upgrade/remove/enable/disable/setAutoUpgrade) | **Wired** (this task) |
| `extensionsStore` | `launchLayer` (backs pluginsDirs §13, skillsDirs §14, mcp.tsx's config-files + inline-servers §15 — all four are fields on the one global layer object, per the store's own doc comment) | `serf/launch/updated` (setLayer/trustRepo) | **Wired** (this task) |
| `credentialsStore` | `instances` (`activeSource`/`hasStoredOAuth`/`hasStoredFile`/`storedEmail` per `InstanceEntry`) | `serf/auth/updated` (login complete/logout/apiKey set/authorized device poll) | **Wired** (this task, see "beyond the named roster" below) |
| `launchConfigStore` (backs launchServer.tsx §9, project.tsx §18, inrepo.tsx §11) | *(no store-level cached state — `schema()` is the only cache, and it never changes; `getLayer`/`resolve`/`setLayer`/`trustRepo` are deliberately uncached, read-fresh-every-call by the store's own design)* | `serf/launch/updated` also fires for these mutations | **N/A — nothing to invalidate at the store layer.** The notification exists and is relevant, but this store holds no cached list/object a broadcast could go stale; each of its 3 consuming sections (launchServer/project/inrepo) manages its own **local** component state instead, populated once per mount via `useConnectedEffect`. Making *those* live-refresh on a same-cwd broadcast would be a real, separate feature (subscribing per-component, filtering by cwd/layer — the broadcast payload carries no fields to filter on even if we wanted to) — out of this item's "wire the store" scope; flagged here rather than silently skipped. |
| `settingsOverviewStore` (Agents/Hub/Storage, MCP's discovered-servers half) | `data` | *(none)* | **Correctly not wired — no broadcast exists.** `settingsOverview.ts`'s own top comment already states this explicitly: "There is also no push-notification-driven invalidation (no `serf/settings/\*` notification exists on the wire)." Confirmed against the full `NotificationName` union — no `serf/settings/*` member exists. Not inventing one. |

Every other `NotificationName` member (`thread/*`, `turn/*`, `item/*`, `warning`, `serf/steering/injected`,
`serf/job/*`, `serf/attention/changed`, `serf/task/updated`, `serf/sandbox/escalation/requested`,
`serf/tree/changed`) is session/thread-scoped, not settings-related, and was left alone.

**Beyond the named roster: `credentialsStore`/`serf/auth/updated`.** The item's text named "the
marketplaces/plugins stores at minimum" and asked me to "check the launch/inrepo/skills stores too" —
`credentialsStore` wasn't explicitly named. I wired it anyway: `serf/auth/updated` is a real broadcast,
`InstanceEntry`'s own `activeSource`/`hasStoredOAuth`/`hasStoredFile`/`storedEmail` fields are exactly what
an auth mutation changes, and the identical staleness bug applies (sign in to a provider in tab A, tab B's
already-loaded Credentials section shows the old auth state indefinitely). It's the same "wire every W7
store that has a corresponding broadcast" general mandate the item opens with, on a store the reviewers
plausibly just didn't happen to flag. Flagging this prominently per the brief's instruction to state
resolutions like this — happy to revert if the roster intentionally excluded it.

**RED evidence.** `src/stores/extensions.test.ts` / `src/stores/credentials.test.ts`, new
`describe("notification-triggered refetch", ...)` blocks, written and run *before* any wiring code existed
(5 of extensions.ts's 6 new tests failed; the "irrelevant notification" test is a true negative and
passed on both sides of the change, as expected — same for credentials.ts's analogous test). Full RED
transcripts are in the commit's own build history (not reproduced here); summary: 5 failures / 41
passing pre-fix on extensions.test.ts, all 41 passing post-fix; credentials.test.ts likewise.

**Mutation-verification.** Temporarily mutated `handleNotification` in both files to match a
`-MUTATED`-suffixed (nonexistent) method name instead of the real ones, re-ran both test files: 8 tests
failed (across both files' new describe blocks). Reverted, confirmed clean restore via `git diff`-backed
byte-identical file restore + green re-run.

**Files:** `src/stores/extensions.ts`, `src/stores/extensions.test.ts`, `src/stores/credentials.ts`,
`src/stores/credentials.test.ts`. Commit `d186c3cf6`.

---

## Item 2 — `dirListSetting` → `useConnectedEffect` migration

**Finding.** `DirListSetting`'s own mount effect hand-rolled the exact `tryStart`/
`connectionStore.subscribe`/cleanup dance `useConnectedEffect` (`src/panes/settings/sections/
useConnectedEffect.ts`) already generalizes and every sibling section (`agents.tsx`,
`CredentialsSection.tsx`, `launchServer.tsx`, `project.tsx`, `inrepo.tsx`) already uses.

**Change.** Replaced the 10-line hand-rolled `useEffect` with one `useConnectedEffect(() =>
extensionsStore.getState().fetchLaunchLayer(), [])` call (matches the "0-arg function where a 1-arg one
is expected" pattern the hook's own doc comment describes for callers with no local `useState` inside
their async closure — same category as `agents.tsx`/`CredentialsSection.tsx`). Dropped the now-unused
`useEffect`/`connectionStore` imports.

**Behavior-preserving, no new test needed.** Ran `dirListSetting.test.tsx` + `mcp.test.tsx` +
`marketplacesPlugins/**` before and after: all pass unmodified (77 tests, 7 files). The "client connects
after mount" race this effect guards against is already covered generically by
`useConnectedEffect.test.ts` (a dedicated file), so re-proving it per call site would be redundant —
matches the item's own "add a test only if the migration exposes an untested edge" instruction; it
didn't expose one.

**Note for a future pass (not in this item's named scope).** `mcp.tsx`'s `McpSection` has the *identical*
hand-rolled pattern for its own `fetchLaunchLayer()` call (lines ~107-116 pre-fix) — a third instance of
the "two streams independently hand-rolled" duplication the item's own framing names. Left as-is: the
item names only "the `dirListSetting` hook (plugins/skills dirs sections)."

**Files:** `src/panes/settings/sections/dirListSetting.tsx`. Commit `9e12bdf50`.

---

## Item 3 — CollectionEditor `renderAddField` slot + PathListEditor fold + envMap structured input

This item had three parts; all three landed in one commit per the brief's "commit per item" instruction.

### 3a. `renderAddField` slot (mandatory)

**Finding.** `CollectionEditor` (`widgets/collectioneditor`) unconditionally renders its own plain-Input
add field with no way to swap in a different control, forcing two independent streams into workarounds:
dirListSetting.tsx hand-rolled its whole list+add-row instead of reusing CollectionEditor (documented in
its own top comment, quoted verbatim as the reason not to build on CollectionEditor); collectionFields.tsx's
envMap field collapsed a real name/value pair into one delimited "NAME=value" text box.

**Change.** Added `renderAddField?: (props: {value, onChange, disabled}) => ReactNode` to
`CollectionEditorProps`. When given, it fully replaces the default add-row (Input *and* its submit
button) — the caller's returned markup must include its own `type="submit"` trigger, since
Enter-to-submit and the busy-disable behavior stay owned by CollectionEditor's own `<form>`/`draft`/
`busy` state either way. When absent, the default path is byte-identical to before (verified: every
pre-existing CollectionEditor-consuming test — `pathList`/`modelList`/`mcpServerList` fields, the
widget's own 15 pre-existing tests — passed unmodified).

**RED evidence.** 6 new tests in `collectioneditor.test.tsx` (`describe("renderAddField", ...)`),
written and run before the prop existed — all 6 failed (the custom field's own accessible name couldn't
be found; the widget had no way to render it). Implemented, re-ran: 20/20 pass.

**Mutation-verification.** Forced the `renderAddField ? ... : ...` branch to `false ? ... : ...` (always
default path) — cascaded to 14 failures across `collectioneditor.test.tsx`, `collectionFields.test.tsx`,
and `dirListSetting.test.tsx` (proving the slot is load-bearing for all 3 downstream consumers, not just
its own widget tests). Reverted, re-confirmed 57/57 green.

### 3b. PathListEditor fold

**Finding.** Investigated whether folding PathListEditor onto CollectionEditor+renderAddField would
"eliminate real duplication" (the item's own bar) or should be left alone (also explicitly permitted).
Found it decisively should fold: `dirListSetting.module.css`'s `.root`/`.list`/`.row`/`.content`/`.empty`
classes were **byte-for-byte identical** to `collectioneditor.module.css`'s own (confirmed via direct
diff of the two files) — not "similar," literally the same CSS, just renamed `addRow`→`addForm`.
Separately, PathListEditor's own top comment explained its inline-`FormRow`-for-errors choice existed
*solely* to reach `--danger` styling, because `token-contract.test.ts`'s semantic-color allowlist is a
"widget concept" (only `src/widgets/<name>/<name>.module.css` may use `--danger`/`--attention`/`--alive`)
and a pane-level stylesheet like dirListSetting's own can't add itself to it — but `collectioneditor` is
*already* on that allowlist as a widget. Folding doesn't just dedupe CSS, it removes a documented
workaround for an allowlist restriction the fold makes irrelevant.

**Change.** `PathListEditor` is now a thin `CollectionEditor<string>` instance: list rendering,
add-field draft/busy/error state, and Enter-to-submit are CollectionEditor's; `renderAddField` supplies
the exact same `FormRow`-wrapped `PathPicker` + `Add` button JSX PathListEditor used to hand-roll
(verbatim structure, so the visual layout is unchanged — confirmed via the pre-existing `.addRow`/
`.addField` CSS values matching CollectionEditor's own `.addForm`/`.addField` values exactly, so wrapping
one inside the other's outer flex-row is inert). ConfirmDialog-gated removal is unchanged, layered
outside CollectionEditor's own immediate-fire `onRemove` exactly as before. Removed the now-dead
`.root`/`.list`/`.row`/`.content`/`.empty` classes from `dirListSetting.module.css` and their
`requireClass` entries. `addPlaceholder` was made optional on `CollectionEditorProps` (it's genuinely
unused whenever `renderAddField` is given — both PathListEditor's and envMap's own renderAddField
closures carry their own placeholders).

**Fully behavior-preserving.** Every one of dirListSetting.test.tsx's 17 pre-existing `PathListEditor`/
`DirListSetting` tests and mcp.test.tsx's `PathListEditor`-backed "MCP config files" tests passed
**unmodified** — no test needed updating for this half of the item, which is itself the strongest
evidence the fold changed nothing observable.

### 3c. envMap structured input restoration

**Finding.** Evaluated restoring structured key/value input now that `renderAddField` exists. Concluded
yes for envMap: two boxes composed into CollectionEditor's single `draft` string via one code-owned "="
join point (the user never types the delimiter), so `handleAdd`'s existing "split on the first `=`"
parsing needs zero changes and a value containing "=" round-trips with no ambiguity at all — strictly
better than the old single-field "type NAME=value yourself" UX, for a small, clean addition. Concluded
**no** for `mcpServerList`'s own single "name command args..." field — its own top-of-file comment
already correctly frames that one as expressiveness-equivalent to 3 boxes (the legacy's own 3 separate
inputs are just concatenated by the user's shell-argument mental model anyway), not a step-down the way
envMap's combined field was, so restructuring it would add API surface without fixing anything real.
Left it alone.

**Change.** `EnvAddFields` (new, local to collectionFields.tsx): two `Input`s (Name/value, each
visually-hidden-labeled via a new `.visuallyHidden` class — same clip-rect technique already duplicated
in `collectioneditor.module.css`/`settings.module.css`/`Rail.module.css`, so a 4th copy is this
codebase's established idiom, not new duplication) that derive their displayed value by splitting the
composed `draft` string on its first "=" and re-compose on every keystroke; typing "=" into the *name*
box specifically is stripped (env var names can't contain one, so there's nothing legitimate to preserve,
and stripping keeps the first "=" always exactly the name/value boundary). Deliberately did **not**
disable the Add button on a blank name — kept the exact prior "composed non-empty" enable check, so
`handleAdd`'s existing blank-name rejection path (`"Use NAME=value."` inline error) stays reachable and
its existing test coverage carries over with an equivalent interaction (type only a value, submit,
observe the same error) rather than becoming dead code behind a disabled button.

**RED evidence.** Replaced 2 of `EnvMapField`'s 4 pre-existing tests (the ones that directly interacted
with the now-gone single "NAME=value" placeholder) with 5 new ones; kept the 2 that test the rendered
*list* (unaffected by the add-mechanism change). Ran before implementing: 5/6 new-and-changed tests
failed (16/21 passing file-wide). Implemented, re-ran: 21/21.

**Mutation-verification.** Stripped the `.replace(/=/g, "")` name-sanitization call — the "typing '='
into the name field does not leak into the value field" test failed as expected; reverted.

**Files:** `src/widgets/collectioneditor/index.tsx`, `src/widgets/collectioneditor/collectioneditor.test.tsx`,
`src/panes/settings/sections/dirListSetting.tsx`, `src/panes/settings/sections/dirListSetting.module.css`,
`src/panes/settings/sections/launchShared/collectionFields.tsx`,
`src/panes/settings/sections/launchShared/collectionFields.module.css`,
`src/panes/settings/sections/launchShared/collectionFields.test.tsx`. Commit `d4f8285ba`.

---

## Item 4 — ConfirmDialog `busy` prop across 5 sites

**Finding.** `ConfirmDialog` already has a `busy` prop (disables both buttons; `CredentialsSection.tsx`
already threads it correctly — confirmed this is the one pre-existing correct site, used as the template).
Counted exactly 5 ConfirmDialog *implementations* in the Extensions cluster (matching extensions.ts's own
"Marketplace & Plugins, Plugins/Skills directories, MCP servers" cluster framing, not just the
`marketplacesPlugins/` folder): `MarketplacesSection.tsx` ("Remove marketplace"), `BrowseSection.tsx`
("Install plugin"), `InstalledSection.tsx` ("Remove plugin"), `dirListSetting.tsx`'s `PathListEditor`
("Remove directory" — shared code, backs both pluginsDirs and skillsDirs sections, and mcp.tsx's config-files
list reuses it too), and `mcp.tsx`'s own inline "Remove MCP server" dialog. All 5 closed the dialog
**synchronously on click**, before the confirmed mutation even started — `setPendingX(null)` ran ahead of
the `try`/`await` in every one, so there was no way to ever observe an in-flight state at all, not just a
missing disabled attribute.

**Change.** All 5 now: set a local `busy` boolean before the `await`, thread it into `ConfirmDialog`'s
`busy` prop, and — for the 3 extensionsStore-backed sites (Marketplaces/Browse/Installed, whose mutations
genuinely reject on failure) — close the dialog only *inside* the success branch, staying open with the
error toast still visible on failure (matches `CredentialsSection`'s own precedent exactly). The other 2
sites (`PathListEditor`'s `onRemove`, `mcp.tsx`'s inline remove) route through handlers that swallow their
own failures into a toast rather than rejecting (`DirListSetting.handleRemove`, `mcp.tsx`'s
`handleRemoveConfig`/inline `handleConfirmRemoveServer`) — for these, the dialog closes once the attempt
settles either way, identical to their pre-existing close-on-completion semantics, just now genuinely
awaited (with a visible busy state) instead of fired-and-forgotten. `PathListEditor`'s `onRemove` contract
changed from `(path: string) => void` to `(path: string) => Promise<void>` to make this possible; its two
callers were converted from fire-and-forget `.catch()` chains to `async`/`await`/`try`/`catch`.

**RED-then-GREEN per site**, per the brief's "a test at one representative site plus a cheap assertion at
the others":
- **Representative site (`MarketplacesSection.test.tsx`)**: full round-trip — open, confirm, observe both
  buttons disabled *and the dialog still open* mid-flight, resolve the mutation, confirm it then closes.
  Written and run RED (failed: `expected false to be true` on the disabled check) before any
  implementation code existed; implementation reverted via a captured diff specifically so this could be
  proven RED honestly rather than written-to-pass. Reapplied, GREEN.
- **4 cheap-assertion sites** (`BrowseSection`/`InstalledSection`/`dirListSetting`/`mcp.test.tsx`): one
  assertion each — script the mutation to never resolve, confirm both dialog buttons are disabled
  mid-flight. Each written and confirmed RED before implementing that site, GREEN after.

**Files:** `src/panes/settings/sections/marketplacesPlugins/MarketplacesSection.tsx` (+.test.tsx),
`BrowseSection.tsx` (+.test.tsx), `InstalledSection.tsx` (+.test.tsx), `src/panes/settings/sections/
dirListSetting.tsx` (+.test.tsx), `src/panes/settings/sections/mcp.tsx` (+.test.tsx). Commit `eee47ed7b`.

---

## Item 5 — Install-confirm source line

**Finding.** The install-confirm dialog already named which marketplace a plugin comes from
(`Install "linter" from acme-plugins?`) but not what that marketplace actually *is* — a GitHub repo, a
raw URL, a local directory. `sourceLabel()` (`marketplacesPlugins/sourceLabel.ts`) already exists and is
already used for exactly this in `MarketplacesSection`'s own list rows.

**Change.** Added a second line to the dialog body: `Source: {sourceLabel(marketplace.source)}`, looked
up from the already-in-scope `marketplaces` array by name. Reused the `rowMeta` CSS class already present
in `marketplacesPlugins.module.css` and already imported into `BrowseSection.tsx` for this exact
semantic purpose (secondary/meta text) — no new CSS. Falls back to omitting the line (not a broken or
misleading one) if the marketplace can't be found by name (a same-session removal race).

**RED evidence.** New test asserting `within(dialog).getByText(/github: acme\/plugins/)` for
`MARKETPLACE_A`'s github source — failed before the change (`TestingLibraryElementError: Unable to find
an element`), passed after.

**Accessibility/tokens.** Plain visible text, no ARIA needed (Dialog reads its full children content
normally to assistive tech); `rowMeta`'s own colors are `var(--ink-mid)` and its font is
`var(--font-mono)`/`var(--font-size-caption)` — both existing design tokens, nothing hardcoded.

**Files:** `src/panes/settings/sections/marketplacesPlugins/BrowseSection.tsx` (+.test.tsx).
Commit `3bded4d2e`.

---

## Item 6 — Theme help copy vs. no-`prefers-color-scheme` (my resolution: implement the listener)

**The disagreement.** `theme.tsx`'s Color-theme help copy says "Both palettes ship; default follows your
OS preference." `prefs.ts`'s own pre-existing top comment stated plainly: "there is no
prefers-color-scheme media query in tokens.css... 'system' always renders the dark tokens today." The
code was flagged in-repo, in `theme.tsx`'s own doc comment, as a "Known copy gap (not this task's to
resolve)" — this task is what resolves it.

**My resolution: implement the listener, not soften the copy.** Both options were genuinely on the
table (the item text offers both explicitly). I chose to implement because, once actually sized rather
than assumed, it turned out to be small, fully isolated, and low-risk — not because "implement" is
reflexively the more virtuous choice:

- **Isolated to `prefs.ts` alone.** No `AppShell.tsx`/`Settings.tsx` chokepoint touch needed — the fix
  lives entirely inside `applyTheme`/a new lazily-installed `matchMedia` listener.
- **No new CSS.** `tokens.css` already has exactly the two document states this needs
  (`:root` = dark, `[data-theme="light"]` = light); `systemPrefersDark()` just picks which one applies
  for "system" instead of prefs.ts unconditionally clearing the attribute.
- **De-risked the "no matchMedia in jsdom" landmine empirically before writing any implementation**, not
  by assumption: wrote and ran a disposable probe test first, confirmed `typeof window.matchMedia ===
  "undefined"` in this project's actual vitest/jsdom environment (not a guess), then designed
  `systemPrefersDark()`'s fallback (defaults to dark — this file's own pre-existing behavior) and a
  lazily-retried listener installation (`ensureSystemSchemeListener`, guarded by a module-private flag
  that `resetPrefsStoreForTests` now also resets) specifically so a test could stub `matchMedia` *after*
  the module's own real, matchMedia-less import and still exercise live tracking — without reaching for
  `vi.resetModules()` + dynamic `import()` gymnastics, which `initPrefs`'s own doc comment already
  identifies as something this codebase deliberately avoids.
- **A real, if modest, feature**, not just copy-honesty: while "system" is selected, the OS flipping
  its preference now live-updates the open tab (a `matchMedia("(prefers-color-scheme: dark)")` change
  listener), not just a one-time resolution at load/selection time — matching the item's own "the small
  listener" phrasing literally, not just its spirit.

**A real bug I introduced and caught before shipping.** My first implementation collapsed explicit
"dark" into the same two-state resolution "system" uses (`removeAttribute` for dark, `setAttribute(...,
"light")` for light) — but explicit "dark" is supposed to literally `setAttribute("data-theme", "dark")`
(the original code did this; I'd missed that "dark via absence" only ever applied to *system-resolving-
to-dark*, never to an explicit pick). Running the *full* `prefs.test.ts` file (not just my new tests)
surfaced this immediately: 3 pre-existing tests broke, including the exact "dark: persists, updates
state, and sets data-theme on the document root" test that would have caught this in review anyway. Fixed
by keeping the explicit-value branch completely untouched and only adding a new branch for `"system"`.
This is exactly the value of "gate on the full suite, not just the new tests" — flagging it here per the
verification-before-completion discipline rather than glossing over a mistake that got caught.

**RED evidence.** 4 new tests in a `describe("system theme: live OS-scheme tracking", ...)` block using a
hand-rolled `fakeMatchMedia` test double (jsdom has none to stub a real one against). 2 of the 4 were
true RED (resolves-to-light, live-tracks-a-change); the other 2 (resolves-to-dark, ignores-non-system)
passed vacuously pre-fix since they matched the old no-op behavior by coincidence — expected and noted,
not a red flag, mirroring the same pattern in a couple of item 1's negative-assertion tests.

**Mutation-verification.** Two independent mutations, each killing exactly the test(s) built for it:
hardcoding `systemPrefersDark()` to always return `true` broke the 2 true-RED tests; removing
`handleSystemSchemeChange`'s `theme === "system"` guard broke the "does not react while an explicit theme
is selected" test. Both reverted, confirmed clean restore.

**Files:** `src/stores/prefs.ts`, `src/stores/prefs.test.ts`, `src/panes/settings/sections/theme.tsx`
(doc-comment only — the visible copy itself needed no change, since it's now true). Commit `249943f94`.

---

## Item 7 — Gitleaks pass on fixtures

**Exact command (matching CI's `.github/workflows/ci.yml` "Secret scan" step verbatim):**

```
go install github.com/zricethezav/gitleaks/v8@v8.30.1
export PATH="$(go env GOPATH)/bin:$PATH"
make secret-scan        # -> scripts/gitleaks-scan.sh repo -> gitleaks detect --no-git --redact --config .gitleaks.toml --source <repo root>
make fuzz-corpus-scan   # -> scripts/gitleaks-scan.sh corpus (every testdata/fuzz + fuzz/corpus dir, same config)
```

gitleaks was not preinstalled in this environment; installed the pinned CI version (`v8.30.1`) via `go
install` before running, so this is the same engine + ruleset + version CI gates on, not a substitute.

**Outcome: clean.**

```
$ make secret-scan
(exit 0, no output — run_quiet_lint suppresses output on success)

$ bash scripts/gitleaks-scan.sh repo   # same command, unwrapped, to show the real gitleaks output
    ...
    scanned ~47538793 bytes (47.54 MB) in 3.28s
    no leaks found
(exit 0)

$ make fuzz-corpus-scan
    ... (one gitleaks run per testdata/fuzz or fuzz/corpus directory, ~50 dirs)
    no leaks found   (every single one)
(exit 0)
```

Both scans cover the *whole working tree* (not just this branch's diff), so this comprehensively confirms
none of W7's secret-shaped test fixtures (credential-looking strings in credentials.test.ts, OAuth flow
IDs, API-key-shaped test values, etc. — none of which I added or touched this round) trip the
`.gitleaks.toml` ruleset, including its default-rule extension (`useDefault = true`) plus the existing
allowlist entries from prior rounds (fuzz-toolkit fixtures, the doctor attempt-id precedent from
`ce72bb32a`, etc.). **No allowlist changes and no fixture reshaping were needed.**

---

## Verification summary

| Item | Commit | Test delta | RED confirmed | Mutation-verified |
|---|---|---|---|---|
| 1. Cross-client staleness subscriptions | `d186c3cf6` | +11 (extensions.test.ts +6, credentials.test.ts +5) | Yes (5/6 + 4/5 real RED, rest true negatives) | Yes (8 failures, both files) |
| 2. `dirListSetting` → `useConnectedEffect` | `9e12bdf50` | +0 (behavior-preserving) | N/A (no behavior change) | N/A |
| 3. CollectionEditor slot + fold + envMap | `d4f8285ba` | +9 net (widget +6, collectionFields +3 net of 2 replaced) | Yes, all 3 sub-parts | Yes (2 independent mutations, 14 + 1 failures) |
| 4. ConfirmDialog `busy` × 5 | `eee47ed7b` | +5 (one per site) | Yes, all 5 sites individually | RED-then-GREEN per site is itself the mutation proof (implementation reverted via captured diff for the representative site) |
| 5. Install-confirm source line | `3bded4d2e` | +1 | Yes | N/A (single-assertion presence test; RED/GREEN is the proof) |
| 6. Theme prefers-color-scheme | `249943f94` | +4 | Yes (2/4 real RED) | Yes (2 independent mutations) |
| 7. Gitleaks | *(no commit — clean, no change needed)* | — | — | — |

Final: **154 test files / 2332 tests, all green**; `tsc --noEmit` clean; `npm run lint` (biome ci) clean.

## Build

Ran `npm run build` once at the end (per the brief): `tsc --noEmit && vite build`, succeeded (270 modules
transformed, built in 349ms). This overwrote `dist/` (vite's `emptyOutDir: true`) and deleted the tracked
`dist/PLACEHOLDER` sentinel file in the process (`git status` showed `D cmd/serf-hub/frontend/dist/
PLACEHOLDER`). Ran `git restore cmd/serf-hub/frontend/dist/PLACEHOLDER` per the brief's own instruction;
`git status --short` confirmed a fully clean tree afterward.

## File manifest compliance

Never touched `src/shell/AppShell.tsx`, `src/panes/settings/Settings.tsx`, `types.gen.ts`, any `.go` file,
or any wave-5 worktree. No item genuinely needed a chokepoint-file line (item 1's launchConfigStore
finding explicitly stayed inside its own store's file rather than reaching into the 3 sections that
consume it, let alone AppShell/Settings).
