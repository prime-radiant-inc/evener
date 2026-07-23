# Wave 8 completion-A — adversarial review

Branch `w8-completion`, HEAD `424e2e467`, BASE `57de2dd36`. Six commits
(`9f58a2588..424e2e467`). Reviewer verified every dockview source citation
against `node_modules/dockview-core` 7.0.2, re-ran all gates from the worktree,
and checked scope/disjointness. Two Jesse-approved items: (A) enable dockview
popout, (B) exact doc-viewer truncation notice.

## VERDICT: APPROVED

No Critical or Important findings. Three Minor items, all non-blocking (report
transcription staleness + one untested defensive fallback branch). The
implementation is correct, the shell/theme/CSP reasoning holds against the real
dockview source and the app's real CSP, and every gate is green.

## Gate summary (re-run by reviewer, all green)

- `go build ./...` clean; `go test ./cmd/serf-hub/...` ok (main pkg 35.0s);
  `make lint` PASS (7 modules, 42s). Exit 0.
- `npx tsc --noEmit` clean (exit 0).
- `npx vitest run` (bare): **243 files / 3485 tests passed**, exit 0.
- `npm run lint` (biome ci, 668 files) clean; `npm run build` ok;
  `dist/PLACEHOLDER` restored → working tree clean.

## Per-probe outcomes

**P0 scope/manifest — PASS.** 18 files, all in-manifest: Go = the SPA-serving
layer (`web.go` route registration, `webnext.go` shell) + `doc_serve.go` raw
branch only. `writeDocPage` (doc_serve.go:261-270) and `writeDocMarkdownPage`
(:279-294) are byte-untouched — verified by reading; the raw change re-stats via
`docRawTotalSize` so nothing in the shared HTML path moves. Frontend confined to
shell/paneActions.ts, DockHost.tsx, PopoutHeaderAction.{tsx,test.tsx}, popout
tests, protocol/docContent.{ts,test.ts}, panes/doc/**. `popoutDormant.test.ts`
deleted (premise inverted). NOT touched: stores/reducer.ts, protocol/model.ts,
chrome/StatusRow, internal/appprojector/**, appwire types. Disjoint from the
timestamps merge set (appwire/appprojector/apptranscript/web_format/hubapi) —
`git diff --name-only` grep for those dirs returns empty, so the controller's
zero-conflict expectation over 183271f0e holds.

**P1 popout shell — PASS.** `TestWeb_PopoutShell_RequiresAuth` drives the REAL
`s.Handler()` (full chain `record(auth(httpsec.CSPMiddleware(mux)))`, web.go:227):
anon→401, authed→200. `/popout.html` is NOT in `isAuthExempt`
(auth_token.go:109-116 lists only /auth, /api/health, four icon assets). CSP is
structurally guaranteed on every 200 through Handler() — CSPMiddleware sets the
header before `next.ServeHTTP` and unconditionally wraps the mux. Shell content
matches every dockview requirement, each citation re-verified in node_modules:
popoutWindow.js:82 (`url`), :83 (assertSameOriginPopoutUrl), :19-31 (rejects
non-http(s)/cross-origin), :95 (window.open), :128 (load), :134 (title
overwrite), :135 (body.appendChild — shell has `<body>`), :136→dom.js:135-171
(addStyles clones sheets: href→`<link>` :140-149, inline→`<style>` :151-169).
Default popoutUrl `/popout.html` confirmed at dockviewComponent.js:669 and in the
`DockviewPopoutGroupOptions.popoutUrl` doc (`Defaults to '/popout.html'`). Shell
is inert (no `id="root"`, no webassets, no `<script>`) — test asserts all three.

**P2 affordance — PASS.** `PopoutHeaderAction` is wired only in DockHost.tsx via
`rightHeaderActionsComponent`, so it exists only on the desktop dockview host;
the mobile StackHost mounts no dockview and its absence test
(StackHost.test.tsx) is real (renders StackHost, asserts no "Pop out" button).
Click invokes `popOutPane(activePanel.id)` — the group's focused panel
(`activePanel` is the real IDockviewHeaderActionsProps field, framework.d.ts:29;
`location?` is optional there, :33). `IconButton label="Pop out"` maps to
`aria-label` (widgets/iconbutton/index.tsx:73), so `getByRole("button",{name:"Pop
out"})` resolves by accessible name — copy register correct. Guards render `null`
for no-activePanel and for `location.type` `popout`/`floating` — both are real
members of the `DockviewGroupLocation` union (dockviewGroupPanelModel.d.ts:142-149,
grid|floating|popout). Replacement suite is meaningful: affordance-exists +
focused-pane-id (unit) + live-dockview-host wiring (DockHost.test.tsx renders the
real host and finds the button) + mobile-absence.

**P3 theme inheritance — PASS.** Event-ordering claim verified against source:
`onDidOpen` fires at popoutWindow.js:119 (with the real `externalWindow`), and
dockview's own `load` handler is registered at :128 inside the Promise executor
constructed at :123 — both AFTER :119. The implementer's load listener
(registered in onDidOpen) therefore fires before dockview's addStyles, but that
is harmless: CSS is declarative, so `data-theme` set first + tokens.css cloned
second resolves to light correctly. `onDidOpen` is only reached when
`window.open` returned a real window (:96-101 returns null earlier on a blocked
popup), so the hook never sees a null window. `inheritOpenerTheme` copies
`data-theme` when present and no-ops when absent — matching the app's real theme
contract (prefs.ts:229-231: light sets `data-theme="light"`, dark REMOVES the
attribute; `[data-theme="light"]` selector lives in styles/tokens.css). dockview
passes its OWN `theme` classname to the popout container (dockviewComponent.js:667)
but not the `<html>` attribute, so the fix is genuinely needed. addPopoutGroup
2-arg shape + `onDidOpen:{id,window}` verified (component.api.d.ts:580;
dockviewComponent.d.ts:44-47). RED + mutation (removing setAttribute fails both
the helper and the load-wiring test) are credible. Live-theme-flip divergence is
RECORDED in the report (§divergence), not coded — ThemeFlip.tsx is a pre-existing
dev harness, not in the diff.

**P4 truncation — PASS.** Go: `totalSize > docFileMaxBytes` (strict) — exactly-cap
is NOT truncated (no headers), over-cap emits `X-Doc-Truncated:true` +
`X-Doc-Total-Size:<true stat size>`, under-cap emits neither; three tests pin the
boundary and the exact total, and the signal derives from `docStat().Size()`
(independent of read length), which is why it is exact at the cap. Frontend:
headers are now the source of truth — `truncated = header==="true"`, `totalBytes`
from `X-Doc-Total-Size` (NaN-guarded → undefined); the old `sizeBytes >= cap`
derivation is deleted. An absent `X-Doc-Truncated` → `truncated=false` → the
`content.truncated &&` guard suppresses the notice entirely (no fabrication); a
malformed total → `undefined` → graceful fallback (no crash). Same-origin fetch
exposes custom response headers, so no CORS-header-visibility issue. Notice math
checks out: formatDocBytes(524288)="512 KiB", formatDocBytes(2097152)="2 MiB",
and the DocPane test asserts the exact string (green).

**P5 CSP compatibility — PASS.** httpsec.go:33-42 sets
`style-src 'self' 'unsafe-inline' https://fonts.googleapis.com`. dockview's
cloned `<link href>` sheets are same-origin (/webassets/*.css) → permitted by
`'self'`; the Google-Fonts link → permitted by the explicit fonts.googleapis.com
source (font-src covers gstatic). Cloned inline `<style>` carry no nonce (paneActions
passes none, so addStyles adds none, dom.js:163) → permitted by `'unsafe-inline'`
(no nonce/hash in the policy, so unsafe-inline is not neutralized). The popout
document is served through the same CSPMiddleware, so the policy applies. Report's
"no nonce plumbing required" is correct for the current policy.

**P6 gates — PASS.** See gate summary. Measured vitest 3485 matches the task's
expected 243/3485 exactly.

## TDD RED evidence

Per-net RED is credible and consistent with the code:
- popout shell: pre-route GET /popout.html → catch-all `/` → handleIndex 404
  (newWebEnabled() false in the test server) — matches the claimed 404 RED; anon
  401 comes from AuthGuard ahead of the mux, proving non-exemption.
- PopoutHeaderAction: module-absent import failure is a standard RED.
- truncation Go/FE: the rewritten header-source assertions fail against the old
  body>=cap derivation; the "no false positive at exactly cap" test is the exact
  bug the item removes.
- theme: undefined `inheritOpenerTheme`/undefined captured onDidOpen → the RED.
Mutation nets are structurally sound (each described mutation would flip a green
assertion to red). Not independently re-run via revert, as the suite is green and
the task did not request destructive re-execution.

## Findings

### Minor
1. **Report vitest count is stale (3482 vs actual 3485).** The gate section
   labels itself "tip 82c5f3905; theme addendum 9964cd9c8" but the count 3482
   was measured at 82c5f3905, before the addendum's +3 theme tests. Measured tip
   is 3485 (= baseline 3477 + 8 net). Report accuracy only; the real gate is
   green and matches the task's expected number.
2. **Report header "Four commits, 9f58a2588..82c5f3905" is stale.** The branch is
   six commits (through 424e2e467); the body documents the two later commits, but
   the header line predates the theme addendum. Documentation only.
3. **DocPane fallback branch is untested.** `content.truncated===true &&
   content.totalBytes===undefined` renders `Showing the first 512 KiB.` (no "of
   X"). The mutation net covers the primary branch; the defensive fallback has no
   direct DocPane test. Low value — the server always sends the total alongside
   the truncation flag (writeDocFileRaw sets both) — but an adversarial reviewer
   notes the branch is uncovered. Optional hardening, not a defect.

### Important
None.

### Critical
None.

## Note (not a finding, for the record)
`readDocFile` fills the cap via a single `f.Read` into a 512 KiB buffer
(doc_serve.go:185-190) — pre-existing, unchanged by this branch. The new
truncation signal is robust to a short read (it derives from `docStat().Size()`,
not the read length); only the head length and the constant "512 KiB" label would
be affected by a short read, and both are pre-existing. Not introduced here.
