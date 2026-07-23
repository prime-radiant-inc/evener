# W8 STABLE-PARTITION parity pre-sweep (for T8 close)

**Worktree:** `webui-w8-presweep` @ `0713970d8` (wave tip: T5+T2+T6+T3 merged; T4 chrome + T7 polish
still in flight). **Base of the merged streams:** `e3b9c188c` (integration absorb, perf levers).
**Method:** verified against CODE at the tip, not by transcribing reports. `npx tsc --noEmit` → **exit 0**
at the tip; the 22 stable-partition locking test files (`panes/doc/**`, `protocol/docContent`,
`widgets/modelCatalog/**`, transcript `taskCard`/`steeringClassify`/`SteeringItem`/`NotificationCard`/
`ToolCallItem`/`turnFailure`/`TurnFailureEndCap`/`TurnBlock`, `panes/transcript/**`, `shell/singlePane*`,
`shell/paneActions`) → **225 tests, all pass** (spot-run by this sweep). Reviews cross-read:
`w8-t2/t3/t5/t6-review.md`; ledger `progress.md`.

## Partition rule applied

A roster row is **DEFERRED** (its subject matter can still change under in-flight work) if it touches:
- **chrome/** or **StatusRow** (T4, in flight);
- the plan's **T7** polish scope (settings polish + **PWA §4** + **auth §5**);
- the **fix-round roster**: AppShell pane registration / layout restore · Session.tsx turn-failure
  wiring · popout · doc-pane zoom a11y · lightbox/ImageGallery convergence · askDock answerable-denied-ask
  · task_list note-only fixture · spawn-catalog harness-scoped enrichment.

Everything else is **STABLE** and swept below. The fix-round roster maps 1:1 onto the merged streams'
recorded Important/Handoff findings (T5 P3 layout-discard; T6 Important #1 transcript-discard + P1 popout;
T3 H1 recovery-button + M2 fixture; T5 Minor #3/#5 zoom-a11y/lightbox; T2 Minor #4 gallery enrichment).

---

## 1. Enumerated roster + partition tag

Roster = the exact T8 close scope: **159 M8 floor items** (§1=40, §2=36, §3=48, §4=17, §5=18) + **10
cross-cutting findings** + **7 open-question items** + **4 deferred transcript clusters** + **12
schedule-W8 triage items** = **192 units**.

| Group | Units | Stable | Deferred | Owning stream (status) |
|---|---|---|---|---|
| §1 Doc/image viewer | 40 | 40 | 0 | T5 (merged) |
| §2 Standalone thread doc | 36 | 33 | 3 | T6/T1 (merged); 3 → T4 chrome |
| §3 panes.js UX | 48 | 41 | 7 | T6 (merged); 7 → producers/popout |
| §4 PWA manifest | 17 | 0 | 17 | **T7 (in flight)** |
| §5 Auth cookie flow | 18 | 0 | 18 | **T7 (in flight)** |
| Cross-cutting findings | 10 | 5 | 5 | mixed |
| Open-question items | 7 | 3 | 4 | mixed |
| Deferred transcript clusters | 4 | 4 | 0 | T3 (merged) |
| Schedule-W8 triage | 12 | 3 | 9 | #1/#2/#12 merged; #3-#11 → T4/T7 |
| **TOTAL** | **192** | **129** | **63** | |

### Per-subsection partition detail

**§1 Doc/image viewer (40, all STABLE — T5).** §1.1 route/request (8) + §1.2 containment (5): server
data-layer preserved by **MW-B** (`/doc/file?format=raw`, guard reused; MW5 review "guard parity
mutation-bitten on 3 security tests incl. symlink escape"), client status-map by T5 → **met**. §1.3
content-mode (9): **met** (binary/cap/markdown-ext/pre-escape) except the legacy-HTML rows (always
`text/html`; exact binary-notice string) → **diverged**; silent-truncation → **diverged-better** (T5 shows
a notice). §1.4 markdown (6): **diverged** (legacy `marked.min.js` mechanics replaced by DOMPurify
`Markdown` widget) — 1 met (renders markdown). §1.5 image (6): **met** (endpoint contract preserved;
`docImageURL` → raw `/doc/image`). §1.6 page-chrome/SPA-cutover (6): **diverged** (native React pane, no
separate HTML document). *Image lightbox/zoom presentation is a distinct finding-set → deferred, not a
floor row.*

**§2 Standalone thread doc (36 → 33 STABLE, 3 DEFER — T6/T1).** §2.1 route/handler (7): **met/diverged**
(T1 repoints `/thread/{ref}` → session single-pane, `routing.ts:63-64`; legacy-handler specifics N/A under
SPA). §2.2 fallback (5): **diverged-resolved** (single-pane keeps the fallback title indefinitely per
plan §Ambiguities #3; `ShowSidebarToggle` dead-not-ported). §2.3 chrome (9): forbidden-markup-absent
**met** (chrome-strip); thread.html `<head>`/favicon/fonts/body-class → **diverged**. §2.4 script subset
(5): **diverged** (no separate document). §2.5 (5): send/steer-live + required composer markup **met** (2);
**location telemetry suppression + subagent parent-breadcrumb suppression + `.status-item.turns` absent
→ DEFER (3, chrome/T4)**. §2.6 compact-poll (3) + §2.7 composer-DOM (2): **diverged** (no htmx poll; React
composer).

**§3 panes.js UX (48 → 41 STABLE, 7 DEFER — T6).** §3.1-3.6 host-contract/open-close/persistence/
suppression/postMessage/splitters (37): **consciously-diverged** — iframe+postMessage+localStorage NOT
ported, dockview-native (plan §Ambiguities #4); §3.2 dedup (1) **met** via `openBeside` store same-params
check. §3.7 producers (7): sidebar-context-menu + auto-open-observer **diverged** (dockview manages space)
(2); **file-tool-card / image-card / subagent-row / converge-on-button / Escape-to-parent → DEFER (5,
producers unwired)**. §3.8 decisions (4): theme-sync + CSP-iframe **diverged-moot** (2); **parent-breadcrumb
+ image-from-nested-pane → DEFER (2)**. *(popout: the trailing "no legacy equivalent" note → deferred
finding, not a checkbox.)*

**§4 PWA (17) + §5 Auth (18): DEFER all 35** — the plan assigns floor §4 and §5 to **T7** (in flight),
including the one real frontend item (WS-handshake-401 connection hint). Server-side infra is unchanged but
T7 owns the sweep.

**Cross-cutting (10):** STABLE = #1 doc-not-flag-gated (resolved: native pane opened imperatively, inside
gated SPA), #2 title-blanking (resolved §Ambig #3), #5 iframe-theme-sync (moot), #6 `ShowSidebarToggle`
dead (resolved), #9 >512KiB silent-truncation (T5 shows notice, mutation-verified). DEFER = #3
parent-breadcrumb, #4 image-open-beside-nested, #7 401-no-CSP (§5/T7), #8 manifest-double-served (§4/T7),
#10 popout.

**Open-questions (7):** STABLE = #1 doc data-path (resolved → MW-B), #5 title indefinitely (resolved), #6
`ShowSidebarToggle` (resolved). DEFER = #2 breadcrumb wire-or-drop, #3 image-nested-pane, #4
`.status-item.turns`, #7 manifest exclusion (§4/T7).

**Deferred transcript clusters (4, all STABLE — T3):** ItemModel.error rendering · task/plan cards ·
steering/notification classification · turn-failure diagnostics. All built + merged (sub-divergences and
the deferred recovery-button noted in §2/§4 below).

**Schedule-W8 triage (12 → 3 STABLE):** #1 model-catalog (T2, merged), #2 ItemModel.error (T3, merged;
+MW-A `4e6936fcf` landed), #12 `/settings/providers`→`/credentials` (T1, merged). DEFER: #3-#8 → **T4**;
#9-#11 → **T7**.

---

## 2. Stable-partition sweep — VERIFIED-MET

Citations are `file:line` at `0713970d8`; the 22-file test run above is the locking evidence (all green).

### Doc/image viewer (§1 data layer + rendering — T5)
- **§1.1/§1.2 route + containment contract preserved** — client keys off status only: `errorKindForStatus`
  403→forbidden / 404→not-found / else→error (`protocol/docContent.ts`), locked by `docContent.test.ts`
  (`toMatchObject{kind:"forbidden"}`, mutation-verified in T5 review P2). Server side reused by MW-B
  (`cmd/serf-hub/doc_serve.go` `writeDocFileRaw`; MW5 review APPROVED, guard-parity mutation-bitten).
- **§1.3 content-mode selection** — binary = NUL in first 8 KiB, 512 KiB cap, markdown by `.md`/`.markdown`,
  else escaped `<pre>` — `panes/doc/DocPane.tsx` + `docContent.ts`; `DocPane.test.tsx` + `docFile.test.ts`
  (15 tests). **§1.3 truncation-notice (beyond-parity, cross-cutting #9)**: `truncated = sizeBytes >=
  DOC_FILE_MAX_BYTES` (`docContent.ts:80-81`); mutation `>=`→`>` bit the cap test (T5 review P1, re-run).
- **§1.5 image endpoint** — `<img src={docImageURL(session,path)}>` against raw `/doc/image` (media types /
  8 MiB cap / `Cache-Control`+`ETag` all server-preserved); `DocPane.test.tsx` image path.
- **Pane self-registers** — `panes/doc/openDoc.ts` → `import "./index"` → `registerPane("doc")`;
  `register.test.ts` ("no pane registered for doc" bit by removing the import).

### Transcript clusters (T3) + triage #2
- **ItemModel.error tool-error rendering** (triage #2; parity-m4 §11:261, §2:100; w5-close HIGH #1) —
  `toolFailed = (error present) || status==="failed"`, force-open on failure, success stays glyph-less;
  `ToolCallItem.test.tsx` (28 tests), mutation `toolFailed→false` fails 7 (T3 review P4). Independent of
  MW-A, which also landed (`4e6936fcf`, `SettledToolStatus(isError)`).
- **Task/plan update cards** (parity-m4 §9:239; contracts §11:236) — `taskCard` descriptor parses
  `argumentsJSON` → touched rows + `<done>/<total>` meter; `action:"view"` suppressed;
  `taskCard.test.tsx`, `suppress→true` fails 7 / `→false` fails 2 (T3 review P3).
- **Steering / notification classification** (parity-m4 §8:209-217; contracts §17:314) —
  `steeringClassify.ts` patterns (current-task / `^Task list:` full-list / tasks-done / task-nudge / loop /
  read-only / transcript) match `renderer-format.js:414-494`; `task-nudge` renders nothing (legacy-regression
  fix); per-block notification cards, excerpt entity-decoded then React-escaped (XSS-safe under CSP
  `script-src 'unsafe-inline'`). `steeringClassify.test.ts` + `SteeringItem.test.tsx` + `NotificationCard.test.tsx`.
- **Turn-failure end-cap** (parity-m4 §9:237; contracts §9/§10) — `TurnFailureEndCap` taxonomy maps REAL
  `TurnError`/`DiagnosticCause` values (provider / connection / source / defensive); badge + message + hint
  render. `turnFailure.test.ts` + `TurnFailureEndCap.test.tsx`. **(Recovery button = deferred, D4.)**

### Model catalog (T2) + triage #1
- **Rich catalog widget** — provider grouping + capability badges (tools/vision/web-search/reasoning) +
  input/output cost + Recent, all from real `/api/models` fields; `modelCatalog.test.tsx` +
  `catalogView.test.ts`. Wire loader emits `?diagnostics=1` envelope, snake→camel (`catalogClient.test.ts`,
  mutation drop-`diagnostics=1` fails 2; T2 review P2 vs `web_spawn.go:176,219-238`).
- **Both swap sites, scoped set preserved** — spawn `ModelField.tsx` builds the model SET from the scoped
  `loadModels` list (enrichment metadata-only, never adds/drops); settings `fields.tsx` `modelPicker` kind.
  `scopedCatalog.test.ts` + `ModelField.test.tsx`, mutation (SET from enrichment) fails 8 (T2 review P1).

### Single-pane + routing (T1/T6) + triage #12
- **`/thread/{ref}` → session single-pane** (§2.1; §Ambiguities #1) — `routing.ts:63-64` yields
  `{type:"session",params:{ref}}`; `AppShell.tsx:194,234` sets `[data-single-pane]`, `:245` suppresses the
  rail. Chrome-strip hides the dockview tab strip + `button[aria-label="Sessions"]` (`shell/singlePane/global.css`,
  confirmed in the built bundle by T6 review P4; `chromeStrip.test.ts`, unscope+delete mutations both bite).
- **`/settings/providers` → `/credentials`** (triage #12) — `routing.ts:52`; live-smoked (T1 report,
  aria-current on credentials).
- **Read-only transcript pane is genuinely read-only** (§Ambiguities #1 distinct surface) — `useTranscript`
  exposes only `model`+`loadOlder`; injected-composer mutation fails (T6 review P3). `panes/transcript/**`
  self-registers via `paneActions.ts:20`. **(No producer opens it → deferred, D2.)**
- **`openBeside` dedup + `popOutPane` body** — `paneActions.ts:40-71` (dedup via store same-params; mobile
  degrade when `getDockviewApi()===null`); `paneActions.test.ts` (7 tests).

---

## 3. Stable-partition sweep — CONSCIOUSLY-DIVERGED (with ruling cited)

- **§3.1-3.6 panes.js iframe/postMessage/localStorage NOT ported (37 rows)** — plan §Ambiguities #4:
  "the iframe + postMessage + localStorage mechanism is NOT ported (§10 deletes panes.js); dockview provides
  tabs/splits/drag/resize/serialization/popout natively." T6 review "Conscious divergences reviewed and
  accepted." Includes the max-3-pane cap (§3.2) + auto-open-observer (§3.7): "recorded as dockview-model
  divergences … dockview manages space."
- **§1.4 markdown = DOMPurify security IMPROVEMENT** — plan Doc/image pin: "the legacy has NO client
  sanitizer at all (floor §1.4:258-261), so the native pane is a conscious security IMPROVEMENT." T5 review
  P4 (hostile markdown cannot execute; `DocPane.test.tsx` raw-`<img onerror>` → no live img).
- **§1.3 >512 KiB now shows a truncation notice** (also cross-cutting #9) — plan pin: "a truncation notice
  T5 SHOWS (the legacy silently truncates … a conscious beyond-parity fix)." §1.1 exactly-512KiB
  false-positive consciously documented + escalated (T5 review Minor #2).
- **§1.6 / §2.3-2.4 / §2.6-2.7 legacy-HTML-document quirks** — native React pane / single-pane layout has no
  separate document, `<head>`, favicon, font links, htmx poll, or legacy composer DOM; these are replaced,
  not reproduced (plan §13 "the same React app rendering in a restricted layout mode, not by a second
  document"; floor §2 preamble).
- **§2.2 title-blanking fixed / `ShowSidebarToggle` dropped** — plan §Ambiguities #3: "the new single-pane
  mode keeps the fallback title (the raw ref) indefinitely … a beyond-parity correction." `ShowSidebarToggle`
  = dead legacy field, superseded by W6 `useSidebarMode` (open-q #6 / cross-cutting #6).
- **Cross-cutting #5 (iframe theme sync) / §3.8 CSP-iframe** — moot: dockview panes are same-document (T6
  review "no cross-frame boundary reintroduced").
- **T3 cluster sub-divergences** (documented for T8's sweep, T3 review "Additional verification"): update
  rows carry no description, no daemon auto-advance "now on X" row (parity-m4 §9:241 / contracts §11:248),
  no full-list fold, no `communicate` facts `<dl>` (§8:218). Touched-row gate `["done","cancelled",
  "in_progress"]` matches `renderer.js:5010` (omitting legacy `open→reopened` is faithful, not a regression).
- **Single-pane `.content` gutter residual** — `shell/singlePane/global.css` header + T6 review "Conscious
  divergences": the outer AppShell `.content` padding is CSS-Module-hashed in a chokepoint T6 can't edit;
  the visible single-pane effect (no tab strip/rail/drawer) is delivered, the gutter is cosmetic.

---

## 4. Explicit DEFERRED list (for the close's delta sweep)

Severity-annotated. **The HIGH items are the pre-sweep's headline: several merged, tested surfaces are
built-but-unreachable pending an in-flight fix-round touch. Do not read "deferred" as "fine."**

| ID | Sev | Item | Owner / fix-round | Evidence |
|---|---|---|---|---|
| **D1** | **HIGH** | **Doc pane has NO open-beside producer** — no `openDocBeside` caller anywhere; the file/image tool cards (floor §3.7) never build the affordance. Floor §1's entire user-reachability is dark. | AppShell registration + producer wiring | grep: 0 callers; T3 review P7 "T3 imports no openDoc/openBeside"; T5 review P3 "nothing imports openDoc yet" |
| **D2** | **HIGH** | **Read-only transcript pane has NO producer** — subagent "Open transcript" opens a **session** pane, not the transcript pane (`subagentModule.tsx:133-139`, stale W4-era comment); no `openBeside({type:"transcript"})` caller. The §Ambiguities #1 distinct surface + floor §2 read-only view is built-but-dead. | producer wiring | `subagentModule.tsx:139` `openPane("session",…)`; T6 review Important #1 |
| **D3** | **HIGH** | **Restoring a persisted layout containing a doc/transcript pane discards the WHOLE layout** — both types are unregistered at boot (`AppShell.tsx:23-26` eager-registers only welcome/session/settings/spawn); `restoreLayout` validates → throws → `clear()`. Latent only until D1/D2 wire a producer, then live. | AppShell pane registration / layout restore | T5 review Important #1 + T6 review Important #1 (`DockHost.tsx:128-133,282-285`; `workspace.ts:110-116,206-230`; `workspace.test.ts:285-300`) |
| **D4** | **HIGH** | **Turn-failure Retry/Reconnect button is dark in production** — `TurnBlock` rendered without `sessionRef` → `canRetry` false. Badge+message+hint DO render (met, §2); only recovery is dark. Verified one-liner: `sessionRef={ref}` at the `renderRow` site. | Session.tsx turn-failure wiring | T3 review H1 (`Session.tsx:182`) |
| **D5** | MED | **Popout inert** — `popOutPane` body exists, zero callers; dockview's `addPopoutGroup` defaults to same-origin `/popout.html` which serf-hub does not serve (SPA fallback would boot a 2nd app). Needs a `popoutUrl` frontend override OR a Go shell route (controller call at T8 live proof). | popout | `paneActions.ts:55-71`; T6 review P1 + Minor #2 |
| **D6** | LOW | Doc-pane zoom `<button>` accessible name is the filename, not the action ("View full size"). | doc-pane zoom a11y | T5 review Minor #3 (`DocPane.tsx:268`) |
| **D7** | LOW | T3/T5 image lightbox diverge — T5 reuses shared `Dialog`; T3 has `flow/ImageGallery.tsx`. Both on `Dialog`/`OverlayPanel`; verify parity or extract a shared `ImageLightbox`. | lightbox/ImageGallery convergence | T5 review Minor #5 |
| **D8** | LOW | `taskCard.test.tsx` "note-only update" fixture (`{action:"update",updates:[{id,notes}]}` / `"Updated 1."`) is not wire-reproducible (Go rejects a status-less update). Non-behavioral; correct to a reopen. | task_list note-only fixture | T3 review M2 |
| **D9** | LOW | Dev gallery `gallery-sections/modelCatalog.tsx` stale ("Interim-Combobox stub (T1)") + minimal fixture (no badges/cost/recent); orphaned `harnessModels.ts` `modelLabel` export. | spawn-catalog harness-scoped enrichment | T2 review Minor #2, #4 |
| **D10** | — | **§2.5 chrome (3 rows)**: location-telemetry suppression, subagent-parent-breadcrumb suppression, `.status-item.turns` absent — the new equivalents live in chrome/StatusRow. | T4 (chrome, in flight) | floor §2.5:432-439 |
| **D11** | — | **§4 PWA (17) + §5 Auth (18) + cross-cutting #7/#8 + open-q #7** — re-brand `background_color`/`theme_color`, manifest double-serve, `crossorigin`, exempt-path set, 401 wall, the WS-401 connection hint. | T7 (in flight) | floor §4/§5; plan T7 |
| **D12** | — | **Triage #3-#8** (pending chips send/steer/drain; model-switch busy-gate; picker Escape/outside-click; `DEFAULT_EFFORT_LEVELS`; location cluster; `showCost`) → **T4**; **#9-#11** (dir "N entries", withBusy, status dot) → **T7**. | T4 / T7 | plan triage table |
| **D13** | — | **§3.7/§3.8 open-beside decisions (4)**: parent-breadcrumb wire-or-drop, image-open-beside-from-nested-pane — moot-or-live depending on the producer/dockview resolution (D1/D2). | producer wiring | floor §3.8; open-q #2/#3/#4 |

**W6-close fold-ins** carried into T8 (recorded prior decisions, no W8 stream owns them here): StatusRow
epoch-clock → T4; FOUC successor; `showCost`-inert → T4 (#8); the 10 minor W6 gaps; `SERF_HUB_WEB=new`
operational note for T8's live proof. (Ledger: W6 close = 159 met / 78 diverged / 13 minor gaps.)

---

## 5. GAP punch list (stable-partition — merged code, not deferred, not diverged)

The big-ticket reachability/wiring problems are **DEFERRED** (D1-D5, in-flight fix-round) and severity-flagged
above — they are the delta sweep's required checks. Against the *stable, merged* code itself the sweep found
only these, all low-severity:

- **G1 — LOW — T3 notification prose drop.** An observer-callback notification with **no** `output:` section
  (`Observer callback:\nmessage: X`) drops the `message:` prose from display (kept only in the raw
  disclosure), because the parser extracts the message from the `output:` JSON envelope. Essentially
  unreachable (the callback's `structuredText` is normally non-empty) but real. (T3 review M3;
  `messages/NotificationCard.tsx` parse path.) *Punch: parse `message:` independently of `output:`.*
- **G2 — LOW — T2 dead export.** `panes/spawn/harnessModels.ts` `modelLabel` is now referenced only by its
  own test after `ModelField` became a thin adapter — orphaned; T2 couldn't clean it (out of manifest).
  (T2 review Minor #2.) *Punch: delete `modelLabel` (keep `harnessUsesSerfModels`, still live via `Spawn.tsx`).*
- **G3 — LOW (verification hole) — mobile single-pane hide unproven.** The chrome-strip mobile hide of
  `button[aria-label="Sessions"]` is verified only at the CSS-rule level (`chromeStrip.test.ts` reads
  `global.css` off disk; jsdom leaves CSS unprocessed). No test mounts StackHost on a `/thread` route to
  assert the end-to-end hide. (T6 review Minor #6.) *Punch: cover in T8's live mobile proof.*

**Coordination flag (not a defect) — C1 — MED — PIN-D boundary.** T2 added `.modelBlock/.modelLabel/.modelHelp`
to `panes/settings/sections/launchShared/fields.module.css`, which sits in **T7's** nominal manifest
(`panes/settings/**` minus `fields.tsx`). Necessary consequence of the blessed `fields.tsx` rich-kind branch,
but **T7 is still in flight** — the controller must confirm T7 did not touch `fields.module.css` /
`fields.test.tsx` before the T7 serial merge. (T2 review Minor #1.)
