# Web Rewrite Wave 8 — Report (M8 periphery, the final build wave)

Status: COMPLETE (pre-merge). T1 chokepoint, four batch-1 streams (T2 catalog, T3 transcript, T5 doc
viewer, T6 single-pane) + reviews, an absorb of settled main (MW-A/MW-B), two batch-2 streams (T4
chrome, T7 polish), an eight-item fix round, two producer/location follow-ups (T3b, T4b), three
controller wiring commits, and this close task (T8: four micro-items, the 192-unit parity sweep, a
real-hub live proof, full gates, and this report). Wave branch: `w8-periphery`; HEAD before this task
`2400349fb`; this task adds the micro-items commit (`ba37a142b`) plus this report/artifacts commit.
**NOT yet merged to integration — that serial step (and its focused re-review) is the controller's,
outside this task's scope.**

## What shipped

- **T1 — periphery chokepoint** (`fe99b0db3`..`96f6e332c`): the controller-owned seams every stream
  fills — `shell/paneActions.ts` (openBeside/popOutPane), `panes/doc/openDoc.ts`,
  `protocol/docContent.ts`, `shell/singlePane.ts` (`isSinglePaneRoute` + `[data-single-pane]` marker),
  `widgets/modelCatalog/index.tsx`, `panes/session/pending/PendingChips.tsx` — plus the routing
  repoint (`/thread/{ref}` → session single-pane; `/settings/providers` → `/credentials`), the
  `PendingChips` mount in `Session.tsx`, and the `index.html` `crossorigin="use-credentials"` manifest
  attribute. Live-smoked on a fake-`$HOME` hub.
- **T5 — native doc/image viewer pane** (merge `bb1271656`): `panes/doc/**` — a React doc pane that
  renders binary-notice / DOMPurify-sanitized-markdown / escaped-`<pre>` by the pinned mode rules, and
  image mode via `<img src={docImageURL()}>` against raw `/doc/image`; SHOWS a >512 KiB truncation
  notice (beyond-parity); self-registers the `"doc"` pane type. Floor §1 (40 items).
- **T2 — rich model catalog** (merge `c98f62701`): `widgets/modelCatalog/**` — provider-grouped options
  with capability badges (tools/vision/web-search/reasoning), input/output cost, context window, and a
  Recent section, all from the live `/api/models` envelope; swapped in at BOTH sites (spawn
  `ModelField` + settings `fields.tsx`) as one-import changes preserving `value`/`onChange`. Triage #1.
- **T6 — single-pane + read-only transcript pane + open-beside/popout** (merge `fdcd92d7c`):
  `shell/singlePane/**` (chrome-strip layout), `panes/transcript/**` (read-only transcript pane, no
  composer), `shell/paneActions.ts` bodies (openBeside dedup + mobile-degrade; popOutPane). Floor §2
  (36) + the portable slice of §3.
- **T3 — transcript parity** (merge `0713970d8`): `panes/session/transcript/**` — the four deferred
  clusters: `ItemModel.error` tool-error rendering (force-open on failure, glyph-less success), task/
  plan update cards (`taskCard`), steering/notification classification (`steeringClassify` widening the
  2-way split to current-task/nudge/full-list/notification/loop; per-block notification cards),
  turn-failure end-cap (taxonomy badge + hint off real `TurnError`/`DiagnosticCause`). Triage #2.
- **Absorb of settled main** (`47012b40c`): **MW-B** raw-file data path (`/doc/file?format=raw` →
  `{content,binary,mediaType,truncated,sizeBytes}`, guard reused, `770800fe8`) and **MW-A** projector
  terminal-error status (`SettledToolStatus(isError)`→`failed`, `4e6936fcf`) — the two
  controller-scheduled main-writer Go tasks — plus the legacy-hydration fix and `nosniff` on the raw
  response.
- **T7 — settings polish + PWA + auth periphery** (merge `67041e8cb`): dir-list "N entries" count
  header, `withBusy` on the non-destructive per-row buttons (Marketplaces Refresh, Installed
  Enable/Disable/Auto-upgrade/Upgrade), Installed plugin status dot (`broken`→`failed` translation),
  PWA re-brand (`manifest.webmanifest` `#0a0a0e`→`#0e1116`), and a `/settings/providers` redirect
  jsdom net. Triage #9-#11 + floor §4/§5 (the WS-401 hint was already shipped a prior wave). Sonnet.
- **T4 — session chrome + optimistic-pending chips** (merge `4ebd3b649`): the epoch-clock guard,
  reasoning-effort fallback + honest none-vs-`(default)`, model-switch busy-gate + Escape/outside-click
  dismiss, `PendingChips` (send/steer/drain beside the composer), palette `/tasks` trigger. Triage
  #3-#8 (with #7 location + #8 showCost dispositioned below).
- **Fix round** (`a2ce6b761`..`b1c151e75`, merge `38f760024`; two sanctioned chokepoint edits only):
  (1) AppShell now boot-registers the `doc`+`transcript` pane types so `restoreLayout` no longer
  discards a whole persisted layout (D3); (2) `Session.tsx:182` wires `sessionRef` into `TurnBlock` so
  the turn-failure Retry/Reconnect button is not dark (D4); (3) popout kept **dormant** (dockview 7.0.2
  blocks the `about:blank` override; served same-origin shell reserved for Jesse) with a static guard;
  (4) doc-pane zoom button given an action-named aria-label (D6); (5) lightbox convergence assessed →
  DIVERGE (net line increase, YAGNI); (6) answerable-denied-ask verified already-resolved; (7)
  task_list "note-only" fixture corrected to a wire-true reopen (D8); (8) catalog enrichment scoped to
  the spawn harness+cwd.
- **Controller wiring** (three commits): `9b14e3aaf` (index.html `theme-color` `#0e1116` re-sync +
  three-way tokens/manifest/index.html drift lock in `pwa-manifest-colors.test.ts`); `6c2e51b1e`
  (thread location facts `cwd`/`gitBranch`/`projectPath` on `ThreadModel` via `reducer.ts` + the
  epoch-anchor source guard); `2e2878e3c` (retarget the T4 epoch net to the guarded reducer + a direct
  defense-in-depth render).
- **T3b — open-beside producers** (merge `2400349fb`): file tool cards (`read_file`/`edit_file`/
  `write_file`) build an Open-beside affordance → `openBeside({type:"doc"})` (cwd-relativized,
  out-of-cwd gated, image-kind by extension); subagent rows' "Open transcript" →
  `openBeside({type:"transcript"})`. Closes the pre-sweep's PIN-A producer gaps (D1/D2).
- **T4b — status-row location cluster** (merge `a8e685c9c`): `LocationCluster` (branch/project/cwd)
  in `StatusRow`, honest-absence guarded, wire-honest "project" label. Triage #7.

**Pre-sweep + count-variance** (`ccca2bf3f`/`52107aac2`, `1b9fd617b`/`104cd285f`): the stable partition
(129 rows swept) and the deterministic explanation of the reviewers' baseline disagreement (two
`readdirSync`-driven contract tests, `token-contract`/`requireclass-contract`, add one case per new
CSS/CSS-importing file — invisible to a diff-scoped hand count).

## Key stories

**The PIN-A producer gap, and the standing probe that now prevents recurrence.** The pre-sweep's
headline was that several merged, tested surfaces (the doc pane, the read-only transcript pane) were
*built but unreachable* — no producer called `openDocBeside`/`openBeside`, and a persisted layout
containing either type would throw at boot and `clear()` the whole workspace (D1/D2/D3). T3b wired the
producers (file/image cards, subagent rows) and the fix round boot-registered both pane types. The gap
can no longer silently reopen: `ToolCallItem.test.tsx:287-364` locks a real "Open beside" producer's
existence (out-of-cwd, no-ref, and grep-opt-out mutation nets), and `paneRestore.test.ts` imports the
real AppShell boot and asserts `restoreLayout` survives a doc+transcript layout — both now failing
loudly if a future change drops the producer or the registration. **Live-proven**: opening two doc
panes and reloading the browser preserved the whole layout (`j4-layout-persistence-after-reload.png`).

**Wire-honesty over legacy mimicry.** Two W8 labels intentionally diverge from the legacy text because
the new wire carries different (more honest) facts: the status-row location cluster says **"project"**,
not the legacy "worktree", because the appwire `Thread` carries no worktree path — `ProjectPath =
project.CanonicalPath` (a hub-resolved canonical root) and `Path = filepath.Base(cwd)`; "worktree"
would be a lie (T4b P3). And a tool-error now renders because the projector was fixed to stamp a
terminal-error status at the source (MW-A), not just papered over in the reducer.

**The architecture replacement is the divergence.** M8 is the wave that deletes the old machinery:
`panes.js` (iframe + postMessage + localStorage) → dockview-native tabs/splits/serialization;
`thread.html` (a second standalone HTML document) → the same React app in a chrome-stripped layout
mode; `doc_serve.go`'s HTML pages → a native React pane reading raw content. So the bulk of the sweep's
"diverged" rows are one of a handful of wholesale rulings (§Ambiguities #1-#4), not per-row losses —
no user-observable capability is dropped, and two are conscious *improvements* (DOMPurify markdown
sanitization the legacy lacked; a truncation notice the legacy never showed).

## Final parity sweep (192 units: 159 floor + 10 cross-cutting + 7 open-question + 4 clusters + 12 triage)

Method: 129 **stable** rows carried from `.superpowers/sdd/w8-presweep.md` (verified at the pre-sweep
tip, `0713970d8`); 63 **deferred** rows swept here against the final tree with the T4/T7/T3b/T4b + fix
round all merged. Test-count mechanism per `.superpowers/sdd/count-variance-report.md`.

| Group | Units | Met | Diverged | Gap | Owner |
|---|---|---:|---:|---:|---|
| §1 Doc/image viewer | 40 | 26 | 14 | 0 | T5 (stable) |
| §2 Standalone thread doc | 36 | 6 | 30 | 0 | T6/T1 (33 stable) + 3 deferred (chrome) |
| §3 panes.js UX | 48 | 4 | 44 | 0 | T6 (41 stable) + 7 deferred (producers) |
| §4 PWA manifest | 17 | 15 | 2 | 0 | T7 (deferred) |
| §5 Auth cookie flow | 18 | 18 | 0 | 0 | T7 (deferred) |
| Cross-cutting (10) | 10 | 3 | 7 | 0 | mixed |
| Open-question (7) | 7 | 4 | 3 | 0 | mixed |
| Deferred transcript clusters (4) | 4 | 4 | 0 | 0 | T3 (stable) |
| Schedule-W8 triage (12) | 12 | 11 | 1 | 0 | T2/T3/T4/T4b/T7/T1 |
| **TOTAL** | **192** | **~91** | **~101** | **0** | |

(The ~91/~101 split carries the pre-sweep's qualitative section verdicts for the 129 stable rows; the
63 deferred rows are precise per-row: **47 met / 16 diverged / 0 gap**.) The high divergence share is
the architecture-replacement rulings above; every diverged row cites a ruling in the ledger. **Three
pre-sweep low gaps are now closed**: G1 (observer prose, Item 0c), G2 (dead `modelLabel`, Item 0a), G3
(mobile single-pane hide, live Journey 5 mobile).

### 63-row deferred sweep — verified-met highlights (code + test file:line)
- **§4 PWA (15 met)** — server route/injection/headers preserved and live-verified: token-injected
  `start_url` `/auth?token=…&next=%2F`, alphabetized keys, `application/manifest+json; charset=utf-8`,
  `Cache-Control: no-store`, 4 icons, 4 auth-exempt icon paths; the frontend consumer
  (`index.html` `<link rel="manifest" crossorigin="use-credentials">` + `theme-color`) present;
  `pwa-manifest-colors.test.ts` locks tokens↔manifest↔index.html.
- **§5 Auth (18 met)** — entirely server-side (`hubedge/auth_token.go`), verified unchanged and
  live-probed (401 wall on `/`, 200 on exempt icon, 401 on gated manifest); the one frontend item (the
  WS-401 connection hint, `src/auth.ts` `checkAuthStatus` 401→unauthenticated + `ConnectionBanner`)
  shipped a prior wave.
- **§3.7 producers (3 met)** — file cards `fsTools.tsx:39`/`editTools.tsx:77,94` +
  `fileOpenBeside.tsx:78`; subagent `subagentModule.tsx:141`; both converge on the `openBeside` seam
  (`ToolCallItem.test.tsx:287-364`).
- **§2.5 turns item (met)** — `.status-item.turns` absent everywhere in the new StatusRow (no turns
  count is rendered), so the ThreadDocumentMode "absent" contract holds trivially (open-q #4 resolved).
- **Triage #3-#11 (met)** — PendingChips (`def2eef36`), model-switch busy-gate (`2b9d14d8d`, live),
  picker Escape/outside-click (`2b9d14d8d`), `DEFAULT_EFFORT_LEVELS` (`21df762ee`, traced YES), location
  cluster (`abeabadef`), dir count (`981cc277c`), withBusy (`c479652dd`/`244e52ec4`), status dot
  (`449bfa0ad`).

## Divergence ledger (consciously-diverged, with ruling cited)

1. **panes.js §3.1-3.6 (37 rows) NOT ported** — iframe/postMessage/localStorage replaced by
   dockview-native (§Ambiguities #4). Includes the max-3-pane cap (§3.2) + auto-open-observer (§3.7):
   dockview manages space.
2. **thread.html standalone-document quirks (§2.3-2.7) / doc HTML pages (§1.6)** — no second document,
   `<head>`, favicon, font links, htmx poll, or legacy composer DOM; the same React app renders in a
   restricted layout (§13 / §Ambiguities #1).
3. **Thread-document-mode hide NOT replicated on `/thread/{ref}`** — `/thread/{ref}` resolves to the
   session pane, so `StatusRow` + `LocationCluster` render where the legacy `input_strip.html:5`
   `{{if not .ThreadDocumentMode}}` hid them. Real divergence, disclosed and deferred (T4b review P4);
   flagged for M9 ratification. **Live-confirmed** (`j5-thread-single-pane-desktop.png`: cwd cluster
   present on `/thread`).
4. **"project" label vs legacy "worktree"** — wire-honest; the wire carries no worktree path (T4b P3).
5. **§1.4 markdown = DOMPurify security IMPROVEMENT** — the legacy had no client sanitizer (beyond
   parity).
6. **§1.3 >512 KiB truncation notice SHOWN** (cross-cutting #9) — the legacy silently truncates
   (beyond parity). The **exactly-512 KiB** boundary is a documented false-positive (notice shown at
   `sizeBytes >= cap`) — on Jesse's decision list (T5 review Minor #2).
7. **Relative-arg open-beside acceptance (beyond parity)** — legacy `cwdRelative` withheld the
   affordance for already-relative args; the new one accepts them (`..`-gated, server `ResolveInRoot`
   403s escapes) — T3b M3.
8. **outputImages open-beside "unbuildable as specced" (precise)** — shell-path/written-file
   outputImages DO carry a `Path` + a `/doc/image?session=&path=` URL on the wire
   (`app_rpc_test.go:960`), but `reducer.ts:97` (`outputImagesToStrings`) flattens them to bare `src`
   strings, so post-reducer `ItemModel.outputImages` cannot feed `openDocBeside` without a reducer/model
   change; `ImageGallery` (with the M4 lightbox) is kept (T3b review M2's correction).
9. **Popout dormant** — dockview 7.0.2's `assertSameOriginPopoutUrl` rejects `about:blank`/`data:`/
   `blob:`; the only working mechanism is a served same-origin blank shell (a Go route) — Jesse's
   decision. `popOutPane` has zero call sites, locked by `popoutDormant.test.ts` (fix-round item 3).
10. **Palette `/status` dropped-with-reason** — the React status row is always-visible; there is no
    toggle-able "session details" panel, so a `[data-details-trigger]` would be a dead affordance
    (T4 concern 3).
11. **Subagent parent-breadcrumb dropped** (cross-cutting #3 / open-q #2) — the SPA renders no parent
    breadcrumb; the legacy `data-open-parent-beside` delegate had no reachable trigger and was
    suppressed in ThreadDocumentMode anyway. Decision: drop the dead delegate. The Escape-to-parent
    accelerator (§3.7) is likewise not ported.
12. **Image-open-beside-from-nested-pane moot** (cross-cutting #4 / open-q #3) — dockview panes are
    same-document, so `isPaneSafeHref`/the postMessage allowlist distinction is gone.
13. **Lightbox/ImageGallery independence** — T5's single-image `DocImageView` and T3's multi-image
    `ImageGallery` both build on the shared `Dialog`; a merged `ImageLightbox` would be a net line
    increase (fix-round item 5, YAGNI).
14. **Manifest double-served kept** (cross-cutting #8 / open-q #7) — `/assets/manifest.webmanifest` is
    also reachable (auth-gated, un-rewritten); server-side, harmless, out of frontend scope — accept.
15. **401-wall/self-heal carry no CSP** (cross-cutting #7) — server-side middleware order, unchanged
    and verified-preserved.
16. **showCost inert (triage #8) — consciously deferred** — no honest thread-level cost crosses the
    wire (`StatusRow.tsx:8-16`); its proposed home (the location cluster) carries no cost number.

Folded W6-close prior decisions (recorded, no W8 work): StatusRow epoch-clock → addressed by T4 (see
punch P1 for the residue), FOUC successor, the 10 minor W6 gaps, `SERF_HUB_WEB=new` operational note.

## Live proof (real hub built from this worktree, real browser, real model — no mocks)

Isolated fake `$HOME=/tmp/w8-live-home` (own `hub.lock`, real host hub untouched), `SERF_HUB_WEB=new`,
port 19288, repo `.env` sourced, model `oai-work/gpt-4o-mini`. Screenshots (11) in
`.superpowers/sdd/w8-close-shots/` (`git add -f`); credential material never echoed.

| # | Journey | Result | Evidence |
|---|---|---|---|
| 1 | Rich catalog spawn → real turn | **PASS** | `j1-model-catalog-search-badges.png` (grouping/badges/cost/ctx); model read notes.md |
| 2 | File card → doc pane (markdown + image) | **PASS** | `j2-doc-pane-markdown-beside.png`, `j2-doc-pane-image-and-j10-failure.png`; MW-B `format=raw`→`text/plain`+`nosniff` |
| 3 | Subagent → transcript pane | **NOT LIVE-DRIVEN** | gpt-4o-mini never invoked delegate; code+test verified (`subagentModule.tsx:141`, T3b P5) |
| 4 | Layout persistence across reload | **PASS** | `j4-layout-persistence-after-reload.png` (both doc panes survived; D3) |
| 5 | /thread single-pane + mobile | **PASS** | `j5-thread-single-pane-{desktop,mobile}.png` (chrome-strip, location cluster, 390px no h-overflow; closes G3) |
| 6 | StatusRow clock / reasoning / model-switch | **MIXED** | reasoning `(default)` + model-switch idle/**busy-gate** PASS; **work-clock BUG** `j6-worktime-epoch-BUG.png` |
| 7 | PendingChips / queue under active turn | **PASS** | `j7-queue-chip-under-active-turn.png`; queued → chip → **drained** to a real turn on completion |
| 8 | Settings polish | **PASS (partial)** | `j8-settings-marketplaces-count-refresh.png`; providers→credentials redirect; count + per-row Refresh; status dots not-provable (no plugins in env, T7-tested) |
| 9 | PWA brand | **PASS** | authed curl: manifest `#0e1116`, index.html theme-color `#0e1116`, `crossorigin`, token-injection, headers |
| 10 | Turn-failure diagnostics | **PASS** | `j10-turn-failure-provider400-retry.png`; badge "provider 400" + **live Retry** (D4 not-dark) |

Also live-verified: the 401 auth wall (`j5-auth-wall-401.png`, server-rendered plain text; SPA never
loads pre-auth); sticky-default model across spawns; a graceful "Couldn't load models" message on a
bad cwd (no crash).

## Punch list

- **P1 — MED/HIGH — work-time clock still shows epoch absurdity (`~495274h`), NEW live finding.**
  Root-caused from the live React fiber: `model.activeTurnStartedAt` parses to `1784774627`, which is
  **exactly `Date.now()/1000`** — the wire's `SerfThread.ActiveTurnStartedAt` is epoch-**seconds** but
  `reducer.ts`'s `epochMsToISO` reads it as epoch-**ms** → `1970` → `now − ~epoch ≈ 495274h`; and it is
  not cleared when `status.type==="awaiting"`, so the idle clock keeps ticking. The W8-T4 guard
  (`statusFormat.ts:61`) only catches `startedMs <= 0`, so a positive seconds-scaled anchor slips
  through. Manifests when a session is hydrated mid/just-post activity (a fully-settled cold reload
  showed a sane `30s`). **Not fixed** here — `reducer.ts` is a standing off-limits chokepoint and the
  true locus may be the Go daemon's `ActiveTurnStartedAt` unit — see Decisions.
- **Closed by this task:** G1 (observer prose → Item 0c), G2 (dead `modelLabel` → Item 0a), G3 (mobile
  single-pane hide → live Journey 5 mobile). Fix-round D6 (zoom a11y) live-confirmed ("Zoom image");
  D8 (task_list fixture) fixed.
- **Accepted/dormant (no action):** D5 popout dormant (Jesse decision), D7 lightbox independence,
  the manifest double-serve, the 401-no-CSP, the exactly-512 KiB truncation boundary.
- **Cosmetic (non-bug):** at a very narrow settings pane width (a 3-pane split), the marketplace
  Refresh/Remove buttons visually crowd — an artifact of the split geometry, not a defect.

## Decisions for Jesse

1. **The work-time epoch clock (P1).** Where to fix the seconds-vs-ms anchor: the Go daemon
   (`SerfThread.ActiveTurnStartedAt` should be epoch-ms), the reducer's `epochMsToISO` coercion, or a
   frontend `status.type==="active"` guard on `totalWorkMillis` (also fixes the tick-while-awaiting
   half). Recommend the Go unit fix as the root cause + the frontend status guard as defense-in-depth.
2. **Popout Go shell** — add a served same-origin blank shell route so dockview popout can work, or
   leave popout permanently dormant.
3. **X-Doc-Truncated header / the truncation-notice approach** — the native pane shows a >512 KiB
   notice client-side (from Content-Length); confirm this is the desired contract vs a server header.
4. **Raw-only `/doc/file` reshape** — MW-B added `?format=raw`; the legacy HTML page path still exists.
   Confirm the HTML page can be retired (it is outside §10's deletion glob).
5. **Thread-level cost aggregation** — `showCost` stays inert until an honest thread-total cost crosses
   the wire (only per-turn `Turn.cost` exists today).
6. **Exactly-512 KiB truncation false positive** — accept the `>=`-cap notice, or move to a strict `>`.
7. **M9 ratifications-by-default** (proceed as built, veto on return): single-pane `/thread/{ref}` LIVE
   composer (§Ambiguities #1, live-proven); the ask-transcript re-architecture; the location cluster
   rendering on `/thread` (thread-document-mode hide not replicated).

**Plan-doc framing correction** (the plan doc is intentionally not edited): the wave plan's §"Genuinely
open" still frames **MW-B as an open go/no-go** and single-pane-composer as pending — both are settled
and landed (MW-B `770800fe8`, MW-A `4e6936fcf`; the plan's own MW-B adjudication is in
`.superpowers/sdd/progress.md`), and the single-pane live composer is live-proven.

## Next steps (NOT this task)

- **Controller:** the wave → integration serial merge, then integration re-absorb of any newer main;
  a focused re-review of the three controller wiring commits (`9b14e3aaf`, `6c2e51b1e`, `2e2878e3c`).
- **M10:** deletion of the legacy machinery (`panes.js`, `thread.html`, `doc_serve.go` HTML pages, the
  legacy JS bundle) + the `SERF_HUB_WEB` flag flip (per the adopted order).
- **M9:** full e2e on the final artifact + the ratifications above; the M9 suites.
- **Final** whole-branch review.

## Verification (all gates green)

Frontend (from `cmd/serf-hub/frontend`): `npx tsc --noEmit` EXIT 0 → `npx vitest run` (bare) **243
files / 3474 tests, 0 failed** (baseline 3475; net −1 from the micro-items: −2 `modelLabel`, +1
observer) → `npm run lint` (biome ci) EXIT 0 → `npm run build` EXIT 0, `dist/PLACEHOLDER` restored,
tree clean. Go (worktree root): `go build ./...` EXIT 0; `go test ./cmd/serf-hub/...` — all 11 packages
`ok`.
