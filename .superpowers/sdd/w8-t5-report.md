# W8-T5 — native doc/image viewer pane — report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w8-doc` (base `e3b9c188c`)
**Commits:** `e7be13a95` (data layer) → `ee5d09823` (pane) — range `e3b9c188c..ee5d09823`
**Tests:** 226 files / 3260 passed (baseline 223 / 3217 → **+3 files, +43 tests**); tsc 0, vitest 0, biome ci 0, build 0, dist/PLACEHOLDER restored (tree clean).

## What shipped

Manifest: `panes/doc/**` + `src/protocol/docContent.ts` (both as assigned; the extended-manifest `docContent.ts` seam is exclusively mine).

Data layer (`src/protocol/docContent.ts`, commit 1):
- Filled `readDocFile(session, path)` (kept the locked 2-arg seam signature; tests mock `globalThis.fetch`). Fetches `/doc/file?format=raw` with `{credentials:"same-origin"}` so the hub auth cookie rides along.
- `binary`/`mediaType` from the deliberately un-sniffed `Content-Type` (`application/octet-stream` vs `text/plain`, charset stripped). `text` decoded from the body for text; `""` for binary.
- Added `DocFileError` (carries `kind` + `status`) and `errorKindForStatus`: 403→`forbidden`, 404→`not-found`, else→`error` — mirrors `handleDocFile`'s guard/status contract (`doc_serve.go:57-73`) exactly. Added `docFileRawURL` + exported `DOC_FILE_MAX_BYTES`.
- Left `docImageURL` as T1 shipped it (kept its two tests).

Pane (`panes/doc/**`, commit 2):
- `DocPane.tsx` — image mode: `<img src={docImageURL()}>` fit-to-pane, click → `Dialog` lightbox; `onError`→ "Image not available". File mode: `readDocFile` → `Skeleton` (loading) / `EmptyState` (error, mapped by kind) / binary notice / **shown** truncation notice (`Chip tone="attention"` + text) / `Markdown` widget for `.md`/`.markdown` / escaped `<pre>` otherwise. No iframe.
- `docFile.ts` — pure `filenameOf` / `isMarkdownPath` (case-insensitive `.md`/`.markdown` only, floor §1.3:239) / `formatDocBytes` (mirrors Go `formatDocBytes`).
- `index.tsx` — self-registers `"doc"` (`registerPane<DocParams>`, lazy `DocPane` chunk), split from the component per the welcome pattern. `openDoc.ts` imports `./index` so registration self-triggers (AppShell is off-limits and never imports doc — verified: it eager-imports only welcome/session/settings/spawn).
- Markdown sanitization uses the **existing** `Markdown` widget (DOMPurify + escaped-HTML renderer). **No new dependency** — `dompurify@^3.4.12` and `marked@^18.0.6` are already in `package.json`, so the DOMPurify NEEDS_CONTEXT trigger did not fire. Barrel needed no edit (`Markdown` already exported). Conscious security improvement over the legacy (no client sanitizer, floor §1.4:258-261).

Tests are TDD RED-first and mutation-verified: I confirmed the nets bite by mutating (a) the truncation boundary `>=`→`>` (cap test failed), (b) 403→forbidden mapping (403 test failed), (c) markdown-vs-`<pre>` selection (h1 test failed), and (d) removing `openDoc.ts`'s `import "./index"` (registration test failed with `no pane registered for "doc"`), then reverting each. Fixtures match the real endpoint's header behavior read from `doc_serve.go` in-worktree.

## CONCERN #1 (primary) — truncation data-path diverges from the adjudication, by wire necessity

The adjudicated data path says: *"sizeBytes from Content-Length … check Content-Length before reading; over the cap, abort the body read and surface the truncation notice with the header's size."* **This is not implementable against the shipped MW-B endpoint** (`770800fe8`, `writeDocFileRaw` in `doc_serve.go:214`):

- The server **truncates server-side**: `readDocFile` (Go, `:172`) reads into a fixed `docFileMaxBytes` (512 KiB) buffer, so `writeDocFileRaw` never emits more than 512 KiB. `Content-Length` (when present at all — Go uses chunked transfer for bodies over ~2 KiB, omitting it) therefore **never exceeds the cap** and **never reports the true file size**. The "over the cap" branch can never fire.
- The raw response carries **no truncation flag and no true-size header** (confirmed against `doc_serve_test.go` — its raw tests assert body verbatim + 403/404 parity, nothing about size/truncation).

So the client cannot (a) learn the true size of a >512 KiB file or (b) detect truncation via `Content-Length`. I implemented the only honest signal available: **`sizeBytes` = actual received byte count** (`arrayBuffer().byteLength`, robust to the missing/chunked `Content-Length`), and **`truncated` = `sizeBytes >= 512 KiB`**. This satisfies the *goal* of floor cross-cutting #9 (the legacy silently truncated; the notice now shows) but diverges from the specified *mechanism*, and the notice honestly says "Showing the first 512 KiB" rather than claiming a true size it cannot know. Edge case: a file of exactly 512 KiB is flagged as truncated (false positive) — acceptable for a "showing first 512 KiB" notice.

**Recommendation (controller/Jesse call):** if an exact-size or false-positive-free notice is wanted, MW-B needs a small additive Go change — e.g. have `readDocFile` peek one byte past the cap (or `stat` the file) and set `X-Doc-Truncated: true` + true `X-Doc-Size`. My `readDocFile` would then read those headers in a one-line follow-up. I did **not** halt the stream for this: the pane is honest and functional either way, so this is a flag, not a blocker.

## Other conscious divergences (for T8's sweep)

- **Image lightbox:** the plan says "inside the M4 lightbox", but that lightbox lives in T3's `panes/session/transcript/flow/ImageGallery.tsx` (off my manifest, concurrent stream) and is not a shared widget. I reused the shared **`Dialog`** widget directly (the same primitive ImageGallery builds on), self-contained in `panes/doc/**`. A doc pane is already a dedicated surface; the click-to-zoom `Dialog` matches the M4 posture without crossing into T3.
- **Registration trigger:** `openDoc.ts` → `import "./index"` (not an AppShell eager import). The plan mandates "panes self-register, no AppShell edit"; every `openDocBeside` producer imports the opener, so `"doc"` is registered before dockview builds the panel. Caveat shared with T6's `transcript` pane (same self-register-not-in-AppShell shape): if `DockHost` ever restores a persisted dockview layout containing a doc/transcript pane at startup *before* any opener import runs, `paneFor` would throw. `paneToURL` returns `null` for doc (not URL-persisted), so this only matters if dockview's own layout serialization restores non-URL panes at boot — a shell/DockHost (T6) concern, flagged for the controller.
- **`sizeBytes` semantics:** for a truncated file this is the received (capped) count, not the true file size (unknowable per Concern #1). The binary notice shows this received size too.

## Cross-stream pins

- **PIN-C (readDocFile ↔ MW-B):** confirmed my fixtures against the landed MW-B endpoint (`doc_serve.go` + `doc_serve_test.go`) — the raw variant's guard chain (403 escape / 404 missing / 404 unknown-or-non-local session / 405 non-GET) and Content-Type behavior are what I test against. The one mismatch is Concern #1 (no size/truncation header).
- **PIN-A (openDocBeside producer):** unchanged — T1's `openDocBeside` still routes `{type:"doc", params}` through `openBeside`; existing `openDoc.test.ts` stays green. T3's file/image tool cards are the concurrent producers; the doc code is (correctly) tree-shaken from the build until T3 imports the opener.

## Floor §1 coverage notes (for T8)

Text / markdown / binary / image rendering, the mode-selection rules (§1.3-1.4), the containment guard/status contract surfaced as honest error states (§1.1-1.2), the `/doc/image` raw-bytes use (§1.5), the DOMPurify sanitization improvement (§1.4), and the shown truncation notice (cross-cutting #9, with Concern #1's caveat) are all built. `/doc/image`'s SVG-excluded / 8 MiB-cap / ETag behavior is server-side and unchanged (surfaced to the pane only as an `onError` fallback). Live end-to-end proof against a real hub is T8's responsibility (I ran unit/component gates only).
