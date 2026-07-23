# Wave 8 completion — popout enable + exact doc-truncation notice

Branch `w8-completion` off `57de2dd36`. Four commits, `9f58a2588..82c5f3905`.
All gates green at the tip (see foot). Baseline re-verified before edits: Go
`go build ./...` + `go test ./cmd/serf-hub/...` clean; frontend 243 files /
3477 tests.

## ITEM A — enable popout

### dockview evidence (read from node_modules, dockview-core 7.0.2)

What the popped-out document must provide, cited file:line:

- `dockview-core/dist/esm/dockview/dockviewComponent.js:669` — the default
  `popoutUrl` is `'/popout.html'` (per-call override → global option →
  this default).
- `dockview-core/dist/esm/popoutWindow.js:82` — `open()` reads
  `this.options.url`; `:83` calls `assertSameOriginPopoutUrl(url)`.
- `popoutWindow.js:19-31` — `assertSameOriginPopoutUrl` rejects anything whose
  protocol isn't `http:`/`https:` or whose origin ≠ opener origin (blocks
  `about:blank`, `data:`, `blob:`). ⇒ the shell MUST be a real same-origin
  served page.
- `popoutWindow.js:95` — `window.open(url, target, features)`; `:128` — waits
  for the popout's `load` event.
- `popoutWindow.js:134` — sets `externalDocument.title = document.title` (the
  OPENER's title, so a served `<title>` is a pre-load declaration only).
- `popoutWindow.js:135` — `externalDocument.body.appendChild(container)` ⇒ the
  shell MUST have a `<body>` to append into.
- `popoutWindow.js:136` → `dom.js:135-171` `addStyles(externalDocument,
  window.document.styleSheets, …)` — dockview CLONES the opener's stylesheets
  into the popout: href sheets become `<link>` (dom.js:140-149), inline sheets
  are copied as `<style>` from `cssRules` (dom.js:151-169). **⇒ the shell needs
  NO app CSS of its own; dockview provides the styling.** This is why the shell
  is "nothing more" than charset + title + body.

CSP check: `httpsec.go` sets `style-src 'self' 'unsafe-inline' …` and
`script-src 'self' 'unsafe-inline'`, so dockview's cloned `<link>`/`<style>`
elements are permitted in the popout document (which is served through the same
CSP middleware). No nonce plumbing required.

### Go (commit 9f58a2588)

`cmd/serf-hub/webnext.go` — `popoutShellHTML` =
`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>serf</title></head><body></body></html>`
served by `servePopoutShell` (Content-Type `text/html; charset=utf-8`,
`Cache-Control: no-store`). Registered in `web.go` unconditionally alongside
`/webassets/` (inert; only the SPA's dockview requests it). NOT added to
`hubedge.isAuthExempt` — same-origin `window.open` carries the SameSite=Lax
cookie, so the normal auth path applies.

RED evidence: `TestWeb_PopoutShell_ServesMinimalSameOriginDocument` and the
authed leg of `TestWeb_PopoutShell_RequiresAuth` both failed `code=404`
(route absent → catch-all `/` → `handleIndex` 404) before the handler existed.
GREEN after. The anon leg (401) is served by `AuthGuard` before the mux, proving
the shell is not exempt.

Mutation proof (inert-shell net): injecting
`<script src="/webassets/app.js">` into the body → the "must be inert (no app
assets or scripts)" assertion FAILS (`popout_shell_test.go:52`). Reverted → PASS.

### Frontend (commit 8dd58bb04)

Affordance placement — **justification**: floor §3.8
(`parity-m8-periphery.md:686`) states popout is "Not applicable / no legacy
behavior to port … a new dockview-native capability", so the floor is SILENT on
where a "Pop out" control lives. Per the task's fallback rule ("the honest
minimal home is the pane/tab context the shell already owns"), the affordance is
a dockview **`rightHeaderActionsComponent`** wired in `DockHost.tsx` — the one
module that "talks to the real dockview library" and owns all group/tab chrome.
A group-header right action is dockview's canonical "pop out this group"
affordance, renders per group, and is structurally absent on mobile (StackHost
mounts no dockview). `DockHost.tsx` is not a chokepoint; `paneActions.ts` is
explicitly in scope.

New `src/shell/PopoutHeaderAction.tsx`: an `IconButton` (label `"Pop out"` →
`aria-label`; `variant="quiet"`, `size="sm"`; inline 16×16 stroke-grammar icon
matching StackHost's BackIcon) whose click calls `popOutPane(activePanel.id)`.
Renders `null` when there is no `activePanel`, or when the group is already a
`popout`/`floating` group (no re-popout). Tokens-only (reuses button.module.css
via IconButton; icon uses `currentColor`); no new CSS, so no new `requireClass`.

`popoutDormant.test.ts` DELETED (its dormancy premise is gone) and replaced by:
- `PopoutHeaderAction.test.tsx` (4 tests) — affordance exists + invokes
  `popOutPane` with the focused pane id; the two absence guards.
- `DockHost.test.tsx` +1 — the "Pop out" button renders in the LIVE dockview
  group header (proves the wiring, not just the isolated component).
- `StackHost.test.tsx` +1 — no "Pop out" button on the mobile stack.

The `popOutPane` DORMANT comment in `paneActions.ts` was rewritten to the new
truth (served shell + PopoutHeaderAction caller), WHAT/WHY, no history.

RED evidence: `PopoutHeaderAction.test.tsx` failed to resolve
`./PopoutHeaderAction` (module absent) before the component existed.

Mutation proofs:
- Wiring net: removing `rightHeaderActionsComponent={PopoutHeaderAction}` from
  DockHost → the live-host "Pop out" test FAILS. Reverted → PASS.
- Focused-pane net: `popOutPane(activePanel.id)` → `popOutPane("WRONG")` → the
  `toHaveBeenCalledWith("pane_doc_1")` assertion FAILS. Reverted → PASS.

## ITEM B — exact truncation notice

### Go (commit 0940f8eae) — raw branch only

`doc_serve.go`: the `?format=raw` branch now calls
`writeDocFileRaw(w, data, docRawTotalSize(abs, len(data)))`.
`docRawTotalSize` re-stats the file (`docStat`) for its true `info.Size()` —
chosen over "read cap+1" because only stat yields the true TOTAL the notice
needs (read-cap+1 tells truncated-or-not but not the total). Re-statting keeps
the change raw-only: threading a size out of the shared `readDocFile` would
touch the HTML path too. `writeDocFileRaw` emits
`X-Doc-Truncated: true` + `X-Doc-Total-Size: <bytes>` iff
`totalSize > docFileMaxBytes` (strictly greater ⇒ a cap-sized file is NOT
truncated). `writeDocPage`/`writeDocMarkdownPage` untouched.

Verified empirically that a single `f.Read` fills the 512 KiB buffer for
cap-or-larger regular files, so the served head is exactly `docFileMaxBytes`.

RED evidence: `TestDocFile_Raw_OverCapEmitsTruncationHeaders` failed
`X-Doc-Truncated="" want true` before the change. The exactly-cap and under-cap
tests were green already (no headers today) and pin the boundary going forward.

Mutation proof (strict-boundary net): `totalSize > docFileMaxBytes` →
`>=` makes `TestDocFile_Raw_ExactlyCapIsNotTruncated` FAIL
(`X-Doc-Truncated="true"` for a cap-sized file). Reverted → PASS. The over-cap
test pins the exact total value.

### Frontend (commit 82c5f3905)

`docContent.ts`: `DocFileContent` gains `totalBytes?: number`. `readDocFile`
now reads `truncated` from the `X-Doc-Truncated` header (the old
`sizeBytes >= DOC_FILE_MAX_BYTES` derivation is DELETED) and `totalBytes` from
`X-Doc-Total-Size` (NaN-guarded). `sizeBytes` stays received-bytes. Header/const
comments updated to the new contract.

`DocPane.tsx`: the notice is now exact —
`Showing the first 512 KiB of <formatDocBytes(totalBytes)>.` — reusing the
existing `formatDocBytes` (docFile.ts, mirrors the Go formatter), design-system
copy, existing `CLASS.notice`/`noticeText`. Falls back to
`Showing the first 512 KiB.` if `totalBytes` is somehow absent.

RED evidence: 3 tests failed against the old code — the two rewritten
`docContent.test.ts` header tests (`totalBytes` undefined / body-derived
`truncated`) and the `DocPane.test.tsx` exact-notice test (old text
`"…— this file was truncated."`). GREEN after.

Mutation proofs:
- Notice net: dropping the `of ${formatDocBytes(totalBytes)}` clause → the
  DocPane "Showing the first 512 KiB of 2 MiB." assertion FAILS. Reverted → PASS.
- Header-read net: reverting `truncated` to `sizeBytes >= DOC_FILE_MAX_BYTES` →
  the "no header means not truncated even at cap (no false positive)"
  docContent test FAILS. Reverted → PASS. (This is the exact false positive the
  item eliminates.)

## Concerns

1. **Light-theme users get a dark popout.** RESOLVED per controller ruling —
   see "Addendum — popout theme inheritance" below (commit `9964cd9c8`). Kept
   here for the trail: dockview clones the opener's *stylesheets* into the
   popout (dom.js addStyles) but NOT the root `data-theme` attribute. tokens.css
   keys light off `[data-theme="light"]` on `<html>` (`stores/prefs.ts:230,232`);
   the served shell has no such attribute, so a light-mode user's popout fell
   back to the `:root` DARK base — the same cross-document theme-sync gap floor
   §3.8:673-679 flags, resurfacing because a popout is again a separate document.

2. **No live-browser drive of the popup handshake.** The real
   `window.open('/popout.html')` + dockview DOM-move was not exercised in a
   browser (needs a running new-SPA hub). It is covered by: direct dockview
   source evidence for every shell requirement; a full-middleware httptest of
   the served shell (real `Handler()` incl. auth + CSP); a real-dockview jsdom
   integration test proving the affordance renders in the live host; and the CSP
   compatibility check above.

## Addendum — popout theme inheritance (controller-ruled, commit 9964cd9c8)

Controller adjudicated Concern 1 under the design-system authority: build the
minimal fix. Frontend-only, one commit.

Evidence for the hook: `addPopoutGroup(item, options?: DockviewPopoutGroupOptions)`
(`component.api.d.ts:580`); `DockviewPopoutGroupOptions.onDidOpen?: (event: { id:
string; window: Window }) => void` (`dockviewComponent.d.ts:44-47`). onDidOpen
fires at `popoutWindow.js:119` — BEFORE the popout navigates to `/popout.html`
(the load handler is at `:128`), so the copy is deferred to the popout window's
`load`.

`paneActions.ts`: `popOutPane` now passes `onDidOpen` to `addPopoutGroup`; on the
popout window's `load` it calls new `inheritOpenerTheme(document.documentElement,
popoutWindow.document.documentElement)` — a pure helper that copies the opener
root's `data-theme` when present and does nothing when absent (dark default
carries no attribute). Since dockview already clones the tokens.css rules
(including `[data-theme="light"]`), the copied attribute makes them match.

RED-first (`paneActions.test.ts`): `inheritOpenerTheme(...)` was undefined →
TypeError; the wiring test's captured `onDidOpen` was undefined → threw; the
`addPopoutGroup` call-shape assertion failed (no options arg). GREEN after.
Tests: opener `data-theme="light"` → popout root gets `"light"`; opener with none
→ popout root has no `data-theme`; plus a wiring test driving the real
`onDidOpen`→`load`→copy path against a fake popout window.

Mutation proof (theme-copy net): removing the `setAttribute` in
`inheritOpenerTheme` → both the "copies light" helper test AND the
`popOutPane`-load wiring test FAIL. Reverted → PASS.

**Conscious divergence (→ wave divergence ledger + M9 observation):** live theme
changes while a popout is already open do NOT re-sync into it — inheritance is
open-time only. This is exactly the cross-document theme-sync gap floor
§3.8:673-679 documents; out of scope by controller ruling, recorded here rather
than coded.

## Gate (tip 82c5f3905; theme addendum 9964cd9c8)

- `go build ./...` clean; `go test ./cmd/serf-hub/...` ok.
- frontend `npx tsc --noEmit` clean; `npx vitest run` 243 files / 3482 tests
  passed; `npm run lint` (biome ci, 668 files) clean; `npm run build` ok
  (`dist/PLACEHOLDER` restored). Each of the four commits was gated the same way.
