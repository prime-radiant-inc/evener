# Wave 8 T1 — periphery chokepoint — report

**Status:** DONE_WITH_CONCERNS (one cross-stream Go-shape finding for T5; see §Concerns)
**Branch:** `w8-periphery`  **Commit range:** `f2506b0fd..b40f00c09` (4 commits, off base `dedcf52dd`)
**Gates (final tree):** tsc 0 · vitest 0 (**223 files / 3217 tests**, baseline 217/3192 → +6 files, +25 tests) · biome ci 0 · `npm run build` 0 (dist/PLACEHOLDER restored, tree clean) · live smoke PASS.

T1 lands every chokepoint touch once against the locked seams so T2–T7 fill seams and never edit a
chokepoint again. No push, no merge.

## Commits

1. `f2506b0fd` single-pane route predicate + `/thread` and `/settings/providers` routing repoint
2. `fe99b0db3` pane-action + doc data seam stubs (paneActions, openDoc, docContent)
3. `049953558` model catalog widget seam + barrel export (+ dev gallery section)
4. `b40f00c09` shell mounts — single-pane chrome, pending chips, manifest

## Chokepoints landed (once)

- **`shell/routing.ts`** — `urlToPane` `/thread/{ref}` repointed transcript→**session** (single-pane
  is derived from the pathname via `isSinglePaneRoute`, not threaded into params, so `/s/{ref}` and
  `/thread/{ref}` dedup to the same session pane). Added `/settings/providers` → `{settings,
  section:"credentials"}` inbound redirect (triage #12). `paneToURL("transcript")` now returns **null**
  (transcript is open-beside-only, no URL, like doc) — this changed 3 routing tests.
- **`shell/AppShell.tsx`** — computes `singlePane = isSinglePaneRoute(pathname)`; sets
  `data-single-pane=""` on the shell root; gates the desktop rail with `&& !singlePane`. Updated the
  stale `openRouteAsPane` comment (transcript/doc are open-beside, never routed).
- **`panes/session/Session.tsx`** — mounts `<PendingChips sessionRef={ref} />` immediately above
  `<Composer>` (2-line change: import + mount).
- **`index.html`** — manifest link gains `crossorigin="use-credentials"` (floor §4.5 REQUIRED); added
  `<meta name="theme-color" content="#0a0a0e">` (floor §4.4 value) for T7 to re-sync.
- **`widgets/index.ts`** (barrel) — added `ModelCatalog` (merged type+value: single plain
  `export {}`) + `ModelCatalogEntry`/`ModelCatalogProps` (`export type`). Only ModelCatalog this pass;
  any later stream barrel need is a controller append (chokepoint), per plan step 5.
- `shell/paneRegistry.ts` — untouched (panes self-register); `src/protocol/reducer.ts`,
  `src/styles/tokens.css` — untouched (off-limits this wave).

## Seams shipped (compiling stubs, real signatures) → owning stream

- **`shell/singlePane.ts`** → drives AppShell (T6 fills `shell/singlePane/**`). Ships ONLY the locked
  `isSinglePaneRoute(pathname): boolean` (real, `^/thread/[^/]+$`). **Decision to confirm at T6
  dispatch:** I did NOT invent a second `applySinglePaneLayout(...)` stub with a guessed signature. The
  "layout application" is realized as (a) AppShell hiding the rail — real, observable — and (b) the
  `data-single-pane=""` root marker. T6 completes dockview tab-strip suppression + full-viewport CSS in
  `shell/singlePane/**` keyed off `[data-single-pane]`, WITHOUT touching AppShell/DockHost (both outside
  T6's manifest). The marker is the seam. If the controller prefers a filled-function seam instead,
  it's a one-line AppShell change at T6 dispatch.
- **`shell/paneActions.ts`** → T6. `openBeside(pane)`/`popOutPane(paneId)` no-op stubs (never throw —
  PIN-A contract). This file is in T6's manifest; T6 replaces the bodies.
- **`panes/doc/openDoc.ts`** → T5. `openDocBeside(params)` delegates `openBeside({type:"doc", params})`
  per the locked seam ("routes through openBeside"); no-ops in T1 (openBeside stub). Uses a namespace
  import so the delegation is spy-testable while the target is a no-op.
- **`protocol/docContent.ts`** → T5. `docImageURL` is **REAL** (pure `/doc/image?session=&path=` builder,
  both `encodeURIComponent`-escaped; matches `output_images.go:202` order/escaping). `readDocFile` is a
  **rejecting** stub (depends on the raw endpoint; PIN-C).
- **`widgets/modelCatalog/index.tsx`** → T2. Interim-Combobox picker (mirrors ModelField's
  value/onChange; `loadCatalog` → `catalog.models`, labels = `displayName`, qualified = `provider/model`).
  Fully working so nothing regresses mid-wave; T2 fills the rich catalog in-module. `ModelCatalogEntry`
  matches the pinned `/api/models` field set exactly. Added `dev/gallery-sections/modelCatalog.tsx`
  (widget-completeness guard).
- **`panes/session/pending/PendingChips.tsx`** → T4. Renders nothing. **Return type widened to
  `JSX.Element | null`** (seam block shorthand was `JSX.Element`): T4's real impl also returns null when
  there are no pending entries, so this is the honest type, and Biome `noUselessFragments` forbids the
  `<></>` alternative. T4 owns the file.

## Live smoke (real hub, isolated fake `$HOME`, `SERF_HUB_WEB=new`, port 9188)

Built the Go binary (embeds the fresh dist via `//go:embed all:frontend/dist`), ran under a `mktemp`
fake HOME (isolates the host-global `$HOME/.serf/hub.lock` flock), authenticated once via the printed
`/auth?token=` URL (token not reproduced here), drove Chrome:

- **`/thread/ref_smoke1`** → SPA served (my new `index-B9CKvbl-.js`); routes to the **session pane**
  ("Loading transcript…"); root carries `data-single-pane=""`; **zero** rail chrome (Sessions / New
  session / data-search-trigger all absent). Title stays the raw ref "ref_smoke1" (bonus: confirms
  §Ambiguities #3, fallback title persists).
- **`/s/ref_smoke1`** (contrast) → **no** marker, rail `Sessions</h2>` present.
- **`/settings/providers`** → "Providers & credentials" section `aria-current="page"` (redirect works).
- **`/new`** → interim model picker present ("(default)" + "Change model"); clicking opens it
  ("Loading models…" + "Cancel") — functional after T1's changes.
- Empty doc-open no-op: covered by unit test (no live producer wired yet — T3/T5).

Hub stopped; worktree tree clean.

## Concerns / cross-stream findings for the controller

1. **MW-B shipped shape ≠ plan recommendation (T5 blocker-adjacent).** The task briefing lists "the doc
   raw endpoint" as landed in main. It shipped as **raw file bytes + Content-Type** (`text/plain` or
   `application/octet-stream`), NOT the JSON envelope `{content, binary, mediaType, truncated,
   sizeBytes}` the plan's §Controller-scheduled-Go recommended (`cmd/serf-hub/doc_serve.go`
   `writeDocFileRaw`, line ~214). T1's locked `DocFileContent` interface is the CLIENT model; T5's
   `readDocFile` must construct it from the raw response + headers. **The raw endpoint carries no
   `truncated`/`sizeBytes` signal** — the >512 KiB truncation notice T5 must show (floor cross-cutting
   #9) has no wire source in the current endpoint. Controller decision at T5 dispatch: derive
   truncated/sizeBytes client-side (Content-Length + cap), or extend MW-B. `docImageURL` is unaffected
   (that endpoint is unchanged and already raw).
2. **singlePane seam is a marker, not a stub function** (see §Seams). Confirm the `[data-single-pane]`
   hook is what T6 should key its layout off, since T6 cannot touch AppShell/DockHost.
3. **`paneToURL("transcript") === null`.** The read-only transcript pane (T6) is open-beside-only. If
   the controller instead wants `/thread/{ref}` to be a read-only transcript (§Genuinely-open item 2 /
   §Ambiguities #1), the routing change is a trivial dispatch-time swap — flag before T6.
4. **`data-single-pane` on mobile:** T1 only suppresses the desktop flex-sibling rail; StackHost still
   receives its railSlot. The marker is set regardless. T6 decides mobile single-pane presentation.

## Notes for stream dispatch (per the plan's W6-close fold-in / trace-first items)

- `reducer.ts`/`tokens.css` untouched and off-limits — the escalation paths (scheduled main-writer /
  quiet window) stand.
- Baseline test file count is now **223** (was 217): the 6 new seam test files. Streams assert their own
  count goes up from there.
