# Web Rewrite Wave 8 — Periphery (M8, the final build wave) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Waves 1-7
> conventions apply verbatim (wave worktree + sub-streams with EXCLUSIVE per-stream manifests in
> per-stream worktrees, wave-local SDD artifacts, controller-owned chokepoints, honest exit-code
> gates — tsc BEFORE vitest with test-file-count verification, vitest BARE in AND-chains — commits
> as separate invocations, Biome is the lint gate, TDD with wire-true fixtures and mutation-verified
> regression nets). This is the LAST build wave: it closes out M8 "periphery" AND every open parity
> item scheduled to W8, so it is broader than any prior wave — the stream count (6 parallel) reflects
> that, not scope creep. The merge to integration is controller-owned and serial, and is EXCLUDED
> from the close task (T8).

**Goal:** finish the workspace shell. Four things happen this wave: (1) the **M8 periphery surfaces**
the design doc §13 names — a native **doc/image viewer pane**, **`/thread/{ref}` single-pane mode**,
**PWA manifest** re-brand, and **dockview popout/open-beside** — become real; (2) the **rich model
catalog** Jesse deferred to W8 replaces the interim `model/list` Combobox at both swap sites; (3) the
**deferred transcript-parity clusters** wave 4 never scoped (task cards, steering classification,
turn-failure diagnostics, `ItemModel.error` rendering) get built; (4) the **session-chrome and
settings polish** items the final review tagged schedule-W8 land. Two wire-side follow-ups (the
projector's `Status:"completed"`-on-error hardcode, and a raw-file data path for the native doc pane)
are **controller-scheduled main-writer Go tasks**, NOT wave streams (see §Controller-scheduled Go).

**Parity floors (cite the real line; never round or reinvent a count — a fabricated floor number
wasted a fix cycle once, ledgered):**
- **`docs/web-ui/parity/parity-m8-periphery.md`** — the primary floor: **159 checkbox items across 5
  sections** (its own count, line 918): **§1 Doc/image viewer = 40**, **§2 Standalone thread document
  = 36**, **§3 panes.js UX = 48**, **§4 PWA manifest = 17**, **§5 Auth cookie flow = 18**, plus **10
  numbered cross-cutting findings** (floor lines 104-149) and **7 open-question items** (floor lines
  882-914) = **166 checkbox lines total** (floor line 921). Every floor requirement a task cites must
  quote the floor's own `path:line` anchor.
- **`docs/web-ui/parity/parity-m4-transcript.md`** — for the deferred transcript clusters: **§8
  Steering messages** (line 207: classification lines 209-217), **§9 System messages / banners**
  (line 224: `task_list`→`appendTaskUpdateCard` at line 239 / `renderer.js:4769-4786,4966-5061`; the
  diagnostic-card + `buildDiagnosticActions` recovery at line 237 / `renderer.js:4259-4278,4428-4521`),
  **§11 Tool-call row lifecycle** (line 261: "success glyph blank, only failure earns the eye").
- **`docs/web-ui/parity/contracts-transcript-scroll-liveness.md`** — **§11 Task / plan cards** (line
  236) and **§17 In-transcript job notifications** (line 314), plus its §9/§10 diagnostic-action
  bullets (T3 re-quotes exact lines before building — the wave4-report's "~25"/"~8" are approximate
  reverse-engineered lower bounds, NOT hard counts).
- Recorded-follow-up source of record for the deferred clusters: **`docs/superpowers/plans/wave4-report.md`**
  §"Deferred / punch items" #4 (task/plan cards), #5 (steering/notification classification), #6
  (turn-failure diagnostics), #7 (the two legacy regressions), and the wave5 close sweep
  **`.superpowers/sdd/w5-close-t6-parity-sweep.md`** MEDIUM #3 (send/steer/drain pending chips), #4-#6
  (chrome polish), and the `ItemModel.error` HIGH #1.
- Triage source of record: **`.superpowers/sdd/final-two-wave-review.md`** Duty 3 "schedule-W8 — 12"
  (extracted verbatim in §Schedule-W8 triage below).

**Prereqs:** the integration branch **after Wave 6's serial merge lands** — the base of this plan is
the **post-W6-merge integration tip** (not a pinned SHA: at authoring time W6 is still closing on
its own branch `w6-surfaces`). Wherever a W8 item builds on W6 content (the spawn `ModelField`
Combobox, the `⌘K` palette, the notifications engine, the `RailHost`/`useSidebarMode` rail), its
current form was read READ-ONLY from the wave-6 worktree
`/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/webui-w6-surfaces` (branch
`w6-surfaces`) and is cited as such; the streams import the MERGED form on integration. Wave worktree
**`webui-w8-periphery`** off the post-W6-merge integration tip; sub-streams branch off the wave branch
after T1. **Per-stream worktrees** (the git index is worktree-global — a stream must never share a
checkout with another write-capable stream even when their file manifests are disjoint;
`.superpowers/feedback_parallel_review_worktree_collision`). **Merge to integration is
controller-owned and serial, and is EXCLUDED from the close task (T8).**

## Binding constraints (every task)

- **Chokepoints are controller-owned — never stream-owned edits.** `shell/routing.ts`,
  `shell/paneRegistry.ts` (no edit — panes self-register; listed so streams know NOT to touch it),
  `shell/AppShell.tsx`, `panes/session/Session.tsx`, `widgets/index.ts` (barrel), `index.html` get
  single-line-append or controller-wiring treatment. T1 lands every chokepoint touch ONCE against the
  stable seam interfaces below; T2-T7 fill the seams and never touch a chokepoint again.
- **`src/protocol/reducer.ts` is controller-owned and is NOT edited this wave.** Every W8 render item
  consumes a field the reducer already maps (`ItemModel.error` at `reducer.ts:114`; `turn.error` at
  `reducer.ts:216`; settled tool items already carry `argumentsJSON` post-wire-honesty). If T3's
  steering/task-card trace (its first task) uncovers a *dropped* wire field the classification needs,
  T3 STOPS and reports it — the controller lands the reducer/Go change as a scheduled main-writer task
  (the W6 search-ref precedent), no stream edits `reducer.ts`.
- **`src/styles/tokens.css` is NOT touched this wave.** No W8 item needs a new token; the quiet-window
  rule is trivially satisfied. The PWA re-brand (T7) READS the brand background/foreground values from
  `tokens.css` and hardcodes their resolved hex into `manifest.webmanifest` + the `index.html`
  `theme-color` meta (a manifest cannot reference a CSS var) — a read, never an edit. Any stream that
  discovers a genuine need for a new token STOPS and escalates to the controller (a quiet window),
  never edits `tokens.css` in-stream. (Ledger 2026-07-21: "theme touches tokens.css = wants a quiet
  window.")
- **Design system binds:** widgets only; **tokens-only CSS** (no literal colors/spacing — the
  `requireclass-contract.test.ts` guard scans textually and is comment-blind, so never write
  `styles.<name>` inside a comment; `requireClass(...)` for every new class); **color-is-attention**
  (a neutral surface stays neutral; color means needs-your-eye — a task card's untouched rows stay
  neutral, only `error`/`failed` earns color, mirroring the "success glyph blank" floor rule §11:261);
  accessible names on every interactive element; sentence case; honest liveness (no fake "live" state).
- **Wire truth, Conflict-aware.** Confirm every real RPC/REST response shape against the Go handler
  (or a captured frame) BEFORE pinning a fixture — the floor field lists are reverse-engineered lower
  bounds (the Wave-5 lesson: synthetic fixtures the wire never produces are how bugs hide). All session
  mutations go through Wave-5's `threadsStore` actions (each already Conflict-typed); user-action
  failures surface via the existing `useToasts()` singleton — no new banner systems, no silent
  `.catch(()=>{})`.
- **Gates, per task and at close (honest exit codes, `.superpowers/feedback_await_behavior_not_timeouts`):**
  `npx tsc --noEmit` BEFORE `npx vitest run` (vitest exits 0 with parse-broken files silently
  excluded — tsc is the gate that catches them); vitest runs **bare in AND-chains** (never behind a
  grep'd pipe); assert the vitest **file count went UP** by the tests you added; `npm run build` then
  `git restore dist/PLACEHOLDER` (vite deletes the tracked placeholder each build — tree must be clean
  after); `npm run lint` (Biome ci) EXIT 0. Gates check exit codes, never grep'd pipes
  (`.superpowers/memory` — "gates check exit codes").

## Locked seam interfaces (T1 ships; streams import and fill)

T1 creates each seam as a compiling stub with its real signature, wires every chokepoint against it,
and hands the seam's own module tree to the owning stream. Concurrent streams import only the
signatures below.

```ts
// shell/paneActions.ts — imperative open-beside + popout (T1 ships stubs; T6 fills; T3/T5 consume)
//   openBeside opens `pane` in a split group beside the focused group (dockview addPanel with
//   position:{direction:"right"}); dedups an identical already-open pane by a (type + params-key)
//   signature and just focuses it instead (floor §3.2 panes.js:178-179). popOutPane promotes a panel
//   to a dockview-native popout window (floor cross-cutting #10 — NEW capability, no legacy port).
//   On the mobile StackHost (useIsMobile) openBeside degrades to a full navigate (no splits).
import type { PaneTypeId } from "./paneRegistry";
export interface PaneRef { type: PaneTypeId; params: unknown }
export function openBeside(pane: PaneRef): void;
export function popOutPane(paneId: string): void;

// panes/doc/openDoc.ts — the doc-pane opener (T1 stub; T5 fills + self-registers "doc")
//   Builds a doc PaneRef from a session ref + cwd-relative path and routes it through openBeside.
//   kind picks the file vs image data path. This is the ONE call every "open beside a file/image"
//   producer uses (floor §3.7 read_file/edit_file/write_file cards, image cards).
export interface DocParams { session: string; path: string; kind: "file" | "image" }
export function openDocBeside(params: DocParams): void;

// protocol/docContent.ts — doc data layer (T1 signature; T5 fills against the MW-B endpoint)
//   readDocFile fetches RAW file content for the native React doc pane (NOT the legacy /doc/file HTML
//   page). Depends on the controller-scheduled MW-B raw-content endpoint (see §Controller-scheduled
//   Go). docImageURL builds the /doc/image href, which already serves raw bytes (floor §1.5).
export interface DocFileContent {
  text: string; binary: boolean; mediaType: string; truncated: boolean; sizeBytes: number;
}
export function readDocFile(session: string, path: string): Promise<DocFileContent>;
export function docImageURL(session: string, path: string): string; // /doc/image?session=&path=, both query-escaped (floor §1.5, output_images.go:202)

// shell/singlePane.ts — single-pane layout mode for /thread/{ref} (T1 stub; T6 fills)
//   isSinglePaneRoute is true for a /thread/{ref} pathname; the shell then hides the rail + tab strip
//   and renders one pane full-viewport (floor §2.3 forbidden chrome: #sidebar / #search-dialog /
//   .settings-link absent). Pure string predicate so any host can call it without a React dep.
export function isSinglePaneRoute(pathname: string): boolean;

// widgets/modelCatalog/index.tsx — the rich model picker (T1 signature stub; T2 fills)
//   value/onChange MIRROR the interim ModelField contract (ModelField.tsx:40-44) so both swap sites
//   drop it in with a one-import change. loadCatalog is injected (harness-scoped) for testability;
//   it returns the /api/models envelope's models[] + recent[] (shape pinned below).
export interface ModelCatalogEntry {
  provider: string; model: string; displayName: string;   // display_name
  contextWindow?: number; supportsTools?: boolean; supportsVision?: boolean;
  maxOutputTokens?: number; supportsWebSearch?: boolean; supportsReasoning?: boolean;
  inputCostPerMillion?: number; outputCostPerMillion?: number; reasoningEffortLevels?: string[];
}
export interface ModelCatalog { models: ModelCatalogEntry[]; recent: ModelCatalogEntry[] }
export interface ModelCatalogProps {
  value: string;                              // qualified "provider/model" or "" for harness default
  onChange: (qualified: string) => void;
  loadCatalog: () => Promise<ModelCatalog>;
}
export function ModelCatalog(props: ModelCatalogProps): JSX.Element;

// panes/session/pending/PendingChips.tsx — send/steer/drain optimistic chips (T1 mounts; T4 fills)
//   Reads usePendingTurnEntries(sessionRef) for methods send|steer|drain (queue is already chipped
//   by QueueStrip) and renders one compact chip per pending entry beside the composer, reconciled and
//   reaped entirely by the EXISTING 10s pendingTurnsStore logic (T4 adds NO new store state).
export function PendingChips(props: { sessionRef: string }): JSX.Element;
```

## Locked cross-stream value pins (EXACT — no stream re-derives these)

- **`/api/models` catalog wire shape (T2 pins against the Go handler FIRST).** Envelope is
  `{"models":[…], "diagnostics":[…], "recent":[…]}` — `diagnostics` populated only when the request
  carries `?diagnostics=1` (`web_spawn.go:216,235-237`; `handleApiModels` at `:176`). Each `models`/
  `recent` entry (built by `modelDescriptorsToAPIModels`, `web_spawn.go:335-374`) carries EXACTLY:
  `provider` (string), `model` (string), `display_name` (string, from `prettifyModelDisplayName`,
  `:348`), and — when the embedded catalog knows the model — `context_window`, `supports_tools`,
  `supports_vision`, `max_output_tokens` (omitempty), `supports_web_search` (omitempty),
  `supports_reasoning`, `input_cost_per_million`, `output_cost_per_million`, `reasoning_effort_levels`
  (omitted when empty) — `web_spawn.go:352-366`. A providers.toml instance override may reset
  `reasoning_effort_levels`/`supports_reasoning`/`context_window` (`applyInstanceModelOverride`,
  `:381-416`). `recent` entries are the same maps (so they carry the same badges) resolved most-recent-
  first, mismatches dropped (`recentModelEntriesFromDescriptors`, `:311-327`). T2 confirms this against
  a live `/api/models` frame before pinning a fixture and does NOT invent an envelope.
- **The interim swap contract (T2 preserves, both sites).** The W6 spawn `ModelField`
  (`panes/spawn/ModelField.tsx:40-44`, read from `webui-w6-surfaces`) exposes
  `{value: string; onChange: (qualified:string)=>void; loadModels: () => Promise<ModelDescriptor[]>}`
  — value is a qualified `"provider/model"` or `""` for the harness default. The `ModelCatalog` widget
  keeps `value`/`onChange` identical (`loadModels` → `loadCatalog`, a `/api/models` call). The W7
  settings site is `panes/settings/sections/launchShared/fields.tsx` (the `modelPicker` kind — its
  own header comment `fields.tsx:1-11` records the "plain free-text provider/model input" cut as the
  thing W8 restores). Both swaps are consumption-side one-import changes; the rich UI lives entirely
  inside `widgets/modelCatalog/**`.
- **`ItemModel.error` is already mapped; render, do not re-map (T3).** `reducer.ts:114` sets
  `error: item.error`; `reducer.ts:216` sets `turn.error`. Every tool descriptor today drops the field
  (final review Duty 2 seam 5: "`ItemModel.error` UNIFORMLY unrendered"; the only reads are the
  ask-gate logic and turn-level scroll). T3 surfaces the error text in the tool-call renderers,
  keyed off `item.error !== undefined`, force-expanding the row on failure (mirroring the legacy
  shell-only behavior floor §2:100 `renderer-tools.js:589-594` and the "only failure earns a glyph"
  rule floor §11:261). The **root cause** is a Go bug — the projector hardcodes `Status:"completed"`
  even on error (`internal/appprojector/appwire_projection.go:437`, carrying `Error: data.Error` at
  `:435`) — fixed independently by **MW-A**; T3's frontend render is the parity mitigation that stands
  regardless and must not depend on the Go fix landing first.
- **DEFAULT_EFFORT_LEVELS fallback = `["minimal","low","medium","high"]` (T4).** The legacy picker fell
  back to this ladder for a reasoning-capable model whose ladder the hub does not enumerate. The
  current `StatusRow.ReasoningEffortControl` (`chrome/StatusRow.tsx:57-103`) deliberately does NOT — it
  renders no selector when `model.reasoningEffortLevels` is empty. **T4's FIRST step is wire truth:**
  determine whether serf's live `thread.serf` ever emits `supports_reasoning:true` with an empty ladder
  (`web_spawn.go:487-493` can; the reducer coerces it to `[]`, `reducer.ts:262-263,584-585`). If it can,
  T4 restores the 4-level fallback (parity, w5-close MEDIUM #6); if it provably cannot, T4 records the
  StatusRow comment's "no ambiguous third state" reasoning as the CONSCIOUS-DIVERGE citation and builds
  nothing. Do not guess — trace it.
- **Optimistic-pending method filter (T4).** `usePendingTurnEntries(ref, method?)`
  (`panes/session/composer/queue/pendingTurnsStore.ts:208`) is the existing selector; `PendingMethod`
  is the union `send|steer|drain|queue`. `QueueStrip.tsx:124` already renders the `"queue"` entries;
  T4's `PendingChips` renders the other three (`method !== "queue"`), reusing the store's 10s
  timeout/failure/reconcile machinery verbatim (w5-close MEDIUM #3; `Composer.tsx:363-391` registers
  all four). T4 adds NO store state and imports the hook read-only.
- **Doc/image data pins (T5).** `/doc/image` already returns RAW bytes — use it directly in an `<img>`:
  media types EXACTLY `image/png|jpeg|gif|webp` (SVG excluded → 404, an XSS guard not an oversight,
  floor §1.5 `output_images.go:158-173`), 8 MiB cap (`outputImageMaxBytes`), success headers
  `Cache-Control: private, max-age=60` + double-quoted lowercase-hex SHA-256 `ETag`
  (floor §1.5:280-282). `/doc/file` today always returns `text/html` (floor §1.3:220) — the native pane
  instead reads raw content via **MW-B** and renders it itself: binary = a NUL byte in the first 8 KiB
  (floor §1.3:222, checked BEFORE the markdown-extension check :224-226), text cap 512 KiB
  (`docFileMaxBytes`, floor §1.3:234) with a **truncation notice T5 SHOWS** (the legacy silently
  truncates — floor §1.3:236-238 / cross-cutting #9 — a conscious beyond-parity fix), markdown by
  case-insensitive `.md`/`.markdown` extension only (floor §1.3:239). **Markdown MUST be sanitized**
  through M4's DOMPurify posture before `innerHTML` — the legacy has NO client sanitizer at all (floor
  §1.4:258-261), so the native pane is a conscious security IMPROVEMENT, recorded as such.
- **Auth exempt-path set (T7 — exact, do not extend without a Go change).** The 6 exact-match
  auth-exempt paths are `/auth`, `/api/health`, `/assets/icon.svg`, `/assets/icon-192.png`,
  `/assets/icon-512.png`, `/assets/icon-maskable-512.png` (floor §5.3:804-812, `hubedge/auth_token.go:109-117`);
  match is exact-string, never prefix. The PWA icon paths MUST stay verbatim (renaming an icon means
  editing the Go exempt list — out of a frontend stream's scope). The `/rpc` WS handshake is guarded
  identically and a 401 upgrade surfaces to JS only as a generic close with no status
  (floor §5.3:813-819) — T7's single real frontend item is a connection-error hint for that case; the
  pre-auth wall is server-rendered text the SPA never gets to smooth over (floor §5.7:868-876).
- **PWA manifest pins (T7).** The `<link rel="manifest">` in `index.html:7` is MISSING the
  `crossorigin="use-credentials"` attribute the floor §4.5:763-766 marks REQUIRED for the manifest
  fetch to carry the auth cookie — T1 adds it (chokepoint `index.html` edit). `start_url` token
  injection (`/auth?token={t}&next=%2F`), `Cache-Control: no-store`, and
  `Content-Type: application/manifest+json; charset=utf-8` are all SERVER-side and already correct
  (floor §4.2-4.3, `web.go:277-287`) — T7 does not touch them. `background_color`/`theme_color`
  (`#0a0a0e` today, floor §4.4:748-753) and the `index.html` `theme-color` meta are re-synced TOGETHER
  to the new brand background resolved from `tokens.css` (§6.8 "re-generated to the new brand tokens").
  Icons stay the 4 pinned files (floor §4.4:741).
- **`/thread/{ref}` single-pane semantics (T6 — RESOLVED, see §Ambiguities #1).** `/thread/{ref}`
  renders the SESSION pane (composer live, per the tested legacy §2.5:440-448) inside a chrome-stripped
  single-pane shell layout (rail + tab strip + search + settings-link hidden, floor §2.3:398-401). The
  read-only **`transcript`** pane type (design §6.4) is a DISTINCT surface for open-beside viewing of
  another thread (no composer), reachable contextually via `openBeside`, not via a URL. T1 repoints
  `routing.ts`'s `/thread/{ref}` from the never-implemented `transcript` pane to the session pane +
  the single-pane flag.

## Schedule-W8 triage (the 12, verbatim from `.superpowers/sdd/final-two-wave-review.md` Duty 3) — every one homed

| # | Item (verbatim) | Home |
|---|---|---|
| 1 | Model-picker catalog restore (Jesse: RESTORE IN W8; interim plain `provider/model` input blessed) | **T2** |
| 2 | `ItemModel.error` text unsurfaced by any tool descriptor (a denied shell's error message is invisible) — A1-disclosed follow-up | **T3** (+ **MW-A** Go) |
| 3 | Optimistic-pending chips absent for send/steer/drain (only queue renders a chip) | **T4** |
| 4 | Session-chrome polish: model-switch trigger not busy-gated | **T4** |
| 5 | …model picker not Escape/outside-click dismissable | **T4** |
| 6 | …`DEFAULT_EFFORT_LEVELS` fallback dropped | **T4** |
| 7 | …location cluster (branch/worktree/cwd) absent | **T4** |
| 8 | `showCost` pref persists but has no consumer (no cost display reads it) — wire when a cost surface exists | **T4** |
| 9 | W7 settings polish: dir-list "N entries" count header (§13/§14) | **T7** |
| 10 | …`withBusy` on non-destructive per-row buttons (Marketplaces Refresh, Installed Enable/Disable/Auto-upgrade/Upgrade — double-click double-fire) | **T7** |
| 11 | …Installed plugin status dot | **T7** |
| 12 | …`/settings/providers`→`/credentials` redirect (stale bookmarks only) | **T1** (chokepoint routing) |

Sub-item folded with #12 (the review's own 13th line, "per-project `?cwd=` page renders inside the
settings-nav shell (cosmetic)") → **T7**. `showCost` (#8) has no cost surface today (StatusRow drops
dollar cost deliberately, `StatusRow.tsx:8-16`); T4 wires it to the location/telemetry cluster it
builds for #7 (the one place a cost readout could live) or records it consciously-deferred if no
honest cost number crosses the wire — same "trace first" discipline as #6.

## Deferred parity clusters (wave 4/5, every one homed)

| Cluster | Floor anchors | Home |
|---|---|---|
| Task / plan update cards (`task_list` renders as a bare JSON row) + the 2 legacy regressions (`action:"view"` task_list & `task-nudge` steer now render visible chrome legacy suppressed) | parity-m4 §9:239 (`renderer.js:4769-4786,4966-5061`), contracts-transcript §11:236; wave4-report Deferred #4/#7 | **T3** |
| Steering / notification classification (every kind renders one generic "Steering injected" divider) | parity-m4 §8:207-217, contracts-transcript §17:314; wave4-report Deferred #5 | **T3** |
| Turn-failure diagnostics (red end-cap, taxonomy badge, Retry/Reconnect recovery) | parity-m4 §9:237 (`renderer.js:4259-4278,4428-4521`), contracts-transcript §9/§10; wave4-report Deferred #6 | **T3** |
| `ItemModel.error` tool-error rendering | parity-m4 §11:261, §2:100; w5-close HIGH #1 | **T3** (= triage #2; + **MW-A**) |

## Tasks

### T1 (sequential): periphery chokepoint — controller-owned
Lands every chokepoint touch once, against the seam interfaces above, so the six streams never edit a
chokepoint. Steps:
1. **Routing (chokepoint `shell/routing.ts`):** repoint `/thread/{ref}` from `transcript` to the
   session pane rendered in single-pane mode (per pin/§Ambiguities #1 — `urlToPane`'s
   `/^\/thread\/([^/]+)$/` branch now yields `{type:"session", params:{ref, singlePane:true}}` or an
   equivalent flag the shell reads via `isSinglePaneRoute`); add the `/settings/providers` →
   `{type:"settings", params:{section:"credentials"}}` redirect (triage #12; today it falls through to
   `PlaceholderSection`, `routing.ts` has no such case). Leave the `doc` `paneToURL` case returning
   `null` (doc panes open via `openDocBeside`, not a URL — its existing comment already says so).
2. **Seam stubs handed off:** create every module in the seam block as a compiling stub with its real
   signature — `shell/paneActions.ts` (open-beside/popout no-ops), `panes/doc/openDoc.ts`,
   `protocol/docContent.ts` (rejecting stubs), `shell/singlePane.ts` (`isSinglePaneRoute` real; layout
   application stub), `widgets/modelCatalog/index.tsx` (renders the interim Combobox until T2 fills it,
   so nothing regresses mid-wave), `panes/session/pending/PendingChips.tsx` (renders nothing).
3. **Shell + session mounts (chokepoints `AppShell.tsx`, `Session.tsx`):** call the single-pane layout
   application from `AppShell` when `isSinglePaneRoute(location.pathname)` (hides rail + tab strip);
   mount `<PendingChips sessionRef=…/>` beside the composer in `Session.tsx`.
4. **index.html (chokepoint):** add `crossorigin="use-credentials"` to the `<link rel="manifest">`
   (`index.html:7`, floor §4.5 REQUIRED) and confirm the `theme-color` meta exists for T7 to re-sync.
5. **Barrel (chokepoint `widgets/index.ts`):** add the export line for `ModelCatalog` (and any new
   shared widget T3/T4/T5 report needing a barrel entry — collected at dispatch, appended once).
6. **Gate:** full suite (tsc→vitest, count up); `npm run build` + restore placeholder; Biome; **live
   smoke** against a fake-`$HOME` hub (`serf-hub` holds a host-global flock at `$HOME/.serf/hub.lock`
   — `.superpowers/project_web_rearchitecture_study`; W5 close lesson): `/thread/{ref}` opens one
   chrome-stripped pane with a live composer, `/settings/providers` lands on credentials, the interim
   model picker still works (widget stub), an empty doc-open call no-ops cleanly. Suggested tier:
   **opus** (contract-establishing; the seam signatures every stream imports).

### T2 ∥ T3 ∥ T4 ∥ T5 ∥ T6 ∥ T7 (streams off the wave branch after T1)

- **T2 — model catalog** (manifest: `widgets/modelCatalog/**` [NEW] + `panes/spawn/ModelField.tsx` +
  `panes/spawn/modelField.module.css` + `panes/settings/sections/launchShared/fields.tsx`). Fills the
  `ModelCatalog` seam and swaps it in at BOTH named sites. Covers triage #1. **First step:** trace the
  live `/api/models` envelope against `handleApiModels` (`web_spawn.go:176`) and pin a wire-true
  fixture (the pinned shape above is a lower bound; confirm field presence/omitempty live). Build the
  rich picker: options grouped by provider, each row showing `display_name` + capability badges
  (tools/vision/web-search/reasoning) + input/output cost + a "Recent" section from the envelope's
  `recent[]`; the `none`-vs-`(default)` reasoning-effort split the floor describes (floor §1.5, spawn
  parity §1.4/§1.5 rows the W6 sweep annotated W8-deferred); a diagnostics affordance behind
  `?diagnostics=1` (surfaced only on demand — do NOT fetch diagnostics on every open). Both swap sites
  become a one-import change preserving `value`/`onChange`. `fields.tsx` is T2's ONLY file inside
  `panes/settings/` — T7's manifest excludes it; neither stream edits the other's settings file (the
  exclusive-per-file discipline, `.superpowers/feedback_parallel_impl_file_ownership`). Suggested
  tier: **opus** (rich widget + wire-shape fidelity + provider grouping/badges/cost/Recent).

- **T3 — transcript parity** (manifest: `panes/session/transcript/**`). The single largest stream —
  it owns all of `transcript/**` so the four intra-transcript clusters cannot collide (a deliberate
  no-split, per `.superpowers/feedback_parallel_review_worktree_collision`). Covers the deferred
  clusters + triage #2. **First step (trace, may branch to the controller):** confirm the wire shapes
  the reducer preserves for (a) a `task_list` tool call's `argumentsJSON` on a settled item, (b) a
  `serf/steering/injected` item's classifiable content/summary, (c) `turn.error`. If any needed field
  is DROPPED by `reducer.ts`, STOP and report — the controller lands the reducer/Go change (no stream
  edits `reducer.ts`). Then build:
  - **`ItemModel.error` rendering** (triage #2): every tool descriptor surfaces `item.error` text when
    present and force-expands the failed row; a failure glyph appears where legacy showed one, success
    stays glyph-less (floor §11:261, §2:100). Independent of MW-A.
  - **Task / plan update cards**: a `task_list` descriptor in the tool-renderer registry
    (`toolRenderers.ts`) parsing `argumentsJSON` into a `.task-card` showing the touched rows +
    progress head (`"<done> / <total>"` + meter), per `renderer.js:4769-4786,4966-5061`; `action:"view"`
    renders nothing (fixing the legacy-regression, wave4-report Deferred #7).
  - **Steering / notification classification**: widen `messages/SteeringItem.tsx` past its 2-way split
    to classify current-task / task-nudge / full-list / notification / loop (parity-m4 §8:209-217,
    contracts §17); a `task-nudge` renders nothing (the second legacy-regression fix). Classification
    source is the legacy `renderer.js:4713-4736` logic — trace whether it is content-pattern-based
    (pure frontend) or wire-kind-based (→ the controller trace above).
  - **Turn-failure diagnostics**: a red end-cap in `TurnBlock.tsx` off `turn.error` with a taxonomy
    badge + Retry/Reconnect recovery actions wired to existing `threadsStore` actions
    (parity-m4 §9:237, `renderer.js:4259-4278,4428-4521`).
  Suggested tier: **opus** (four dense clusters + classification/diagnostic taxonomies + wire tracing).

- **T4 — session chrome + optimistic-pending chips** (manifest: `panes/session/chrome/**` +
  `panes/session/pending/**` [NEW]). Covers triage #3-#8. Fills T1's `PendingChips` seam (send/steer/
  drain chips off `usePendingTurnEntries`, method-filtered, reusing the store's reconcile/reap — chips
  render beside the composer, NOT injected into the virtualized transcript: rendering optimistic items
  into the virtual list is beyond the MEDIUM parity bar and the legacy chip was a lightweight
  indicator — recorded as a conscious presentation choice in the close sweep). In `chrome/**`:
  busy-gate the `ModelSwitch` trigger + `openPicker` on the live `isTurnActive`/busy predicate (not
  only the static `capabilities.changeModel`), matching Composer's Stop/Steer gate
  (`ModelSwitch.tsx:103-112,71-84`; `Composer.tsx:574,582`); make the model picker dismissable by
  Escape AND outside-click (not only Cancel, `ModelSwitch.tsx:86-88`); the `DEFAULT_EFFORT_LEVELS`
  fallback per the pin (trace-first); the location cluster (branch/worktree/cwd) in the status row
  (today only `branch`, only in the sidebar `RailRow.tsx:205` — trace the `ThreadModel` source fields
  first); a `showCost` consumer wired to that cluster or consciously deferred. **Coordination
  (PIN-B):** T4 MAY adopt the T2 `ModelCatalog` widget for the mid-session `ModelSwitch` once it
  lands (consistency across all model pickers); the busy-gate/dismiss fixes stand alone on the
  existing `model/list` Combobox if T2 has not merged — not a hard dependency. Suggested tier: **opus**
  (pending reconcile correctness + busy-gate invariant + three trace-first items).

- **T5 — doc / image viewer pane** (manifest: `panes/doc/**` [NEW]). Fills `openDoc.ts` +
  `docContent.ts` and self-registers the `"doc"` pane type (`registerPane<DocParams>({id:"doc", …})`
  — no `paneRegistry.ts`/`AppShell.tsx` edit; panes self-register). Covers floor §1 (**40 items**) +
  the open-question §"doc-viewer data path" (RESOLVED, §Ambiguities #2). A native React pane: image
  mode → `<img src={docImageURL(...)}>` (raw bytes, floor §1.5) inside the M4 lightbox; file mode →
  `readDocFile` (MW-B) then render binary-notice / DOMPurify-sanitized-markdown (via M4's Markdown
  widget) / HTML-escaped `<pre>` by the pinned mode-selection rules (floor §1.3-1.4), SHOWING a
  truncation notice past 512 KiB (beyond-parity, floor cross-cutting #9). Preserve the guard/status
  contract server-side (local-only, unknown-session 404, escape 403 vs missing 404 — floor §1.1-1.2)
  by calling the existing routes' data layer; do NOT reintroduce an iframe. Suggested tier: **opus**
  (new pane, content-mode selection, sanitization posture, security-relevant path handling).

- **T6 — single-pane mode + read-only transcript pane + open-beside/popout** (manifest:
  `shell/singlePane/**` [NEW] + `panes/transcript/**` [NEW] + `shell/paneActions.ts`). Fills the
  single-pane layout application (T1's `isSinglePaneRoute` drives it), the `openBeside`/`popOutPane`
  seam bodies, and self-registers the read-only `"transcript"` pane type (M4 transcript engine in
  read-only mode — no composer; `thread/read` already exists, no new data path). Covers floor §2
  (**36 items** — chrome suppression, the fallback-title quirk cross-cutting #2 [keep the fallback
  title, do NOT blank it — §Ambiguities #3], composer-stays-live §2.5) and the portable slice of §3
  (**48 items**): open-beside producers route through `openBeside`; popout is dockview-native (floor
  cross-cutting #10 — enable + verify, no build); the max-3-pane cap (floor §3.2) and the auto-open-
  observer behavior (floor §3.7) are recorded as dockview-model divergences in the close sweep (dockview
  manages space; no hard cap ported) unless the controller rules otherwise. Suggested tier: **opus**
  (shell layout mode + dockview split/popout integration + a second pane type).

- **T7 — settings polish + PWA + auth periphery** (manifest: `panes/settings/**` EXCEPT
  `sections/launchShared/fields.tsx` + `assets/manifest.webmanifest` + the 4 PWA icon assets +
  `stores/connection.ts` [connection-error hint only]). Covers triage #9-#11 + the cosmetic `?cwd=`
  sub-item, floor §4 (**17 items**), floor §5 (**18 items**). Settings: dir-list "N entries" count
  header on `DirListSetting`/`CollectionEditor` (`sections/dirListSetting.tsx`, floor-cited §12 already
  shows a count so it is an asymmetry, wave7-report §13/§14); `withBusy` on the non-destructive per-row
  buttons (`marketplacesPlugins/MarketplacesSection.tsx` Refresh, `marketplacesPlugins/InstalledSection.tsx`
  Enable/Disable/Auto-upgrade/Upgrade — double-click double-fire, wave7-report §12b/§12e); the Installed
  plugin status dot (`InstalledSection.tsx`, wave7-report §12e); the per-project `?cwd=` page rendering
  inside the settings-nav shell (cosmetic). PWA: re-brand `background_color`/`theme_color` + the
  `index.html` `theme-color` meta to the `tokens.css` brand background (READ, together — pin above);
  keep the 4 icon files at their exact auth-exempt paths; verify the double-served-manifest and
  token-injection behaviors in the close (floor §4.1/§4.2, server-side, unchanged). Auth: the ONE real
  frontend item — a connection-error hint when the `/rpc` WS handshake 401s indistinguishably (floor
  §5.3:813-819); everything else in §5 is server-side infra verified, not built (floor §5.7). T7's
  settings files are DISJOINT from T2's single `fields.tsx`. Suggested tier: **sonnet** (mechanical
  polish + re-brand + one connection hint; decompose-for-tier — many small changes, none dense).

### T8: wave close
Parity sweep of **all 159 M8 floor items** across the 5 sections + the **10 cross-cutting findings**
+ the **7 open-question decisions** (each recorded resolved or consciously-deferred), PLUS the deferred
transcript clusters against parity-m4 §8/§9/§11 + contracts §9/§10/§11/§17, PLUS the 12 schedule-W8
triage items — annotating every conscious divergence (the read-only-transcript-vs-single-pane split,
the dockview-native §3 items, the pending-chips-beside-composer placement, the beyond-parity doc
sanitization + truncation notice) and folding in W6's close divergence ledger + punch items per
§W6-close fold-in. **Live proof** on a real hub under an **isolated fake `$HOME`** (the host-global
`$HOME/.serf/hub.lock` flock forbids a shared hub; `.superpowers/project_web_rearchitecture_study`):
open a doc pane from a session's file tool card (text + markdown-sanitized + image + a >512 KiB
truncation) beside the session; open a `/thread/{ref}` share link and confirm one chrome-stripped
pane with a LIVE composer + the fallback title persisting for an unknown ref; pop out a pane; pick a
model from the rich catalog at spawn AND settings (badges/cost/Recent visible); drive a task_list
mutation and see a task card, a steering message classified, a forced turn failure showing the red
end-cap + Retry; a denied shell tool showing its error text; the pending chips on send/steer/drain;
the settings polish (dir count, no double-fire, status dot); install the PWA and relaunch through the
token `start_url`. Full gates + wave8-report. **The merge to integration is controller-owned and
serial — NOT part of T8.** Suggested tier: **opus**.

## Controller-scheduled Go tasks (main-writer, NOT wave streams)

Sequenced by the controller on the main checkout (the W6 main-writer3/4 precedent: a `sonnet` writer,
its own review, then absorbed into the wave branch). None is a W8 stream; each is additive Go with its
own drift/handler tests.

- **MW-A — projector terminal-error status.** `internal/appprojector/appwire_projection.go:437`
  hardcodes a completed tool item's `Status:"completed"` regardless of error (while `:435` carries
  `Error: data.Error`). Emit a terminal-error status so the wire is honest; this is the root cause
  behind the whole `ItemModel.error` class (final review Go follow-up; ledger). T3's frontend render is
  independent and must not wait on it, but MW-A closes the class at the source. **Schedule BEFORE T8's
  live proof** so the sweep can verify the honest status.
- **MW-B — raw-file data path for the native doc pane.** `/doc/file` returns HTML today (floor §1.3),
  which the native React pane cannot consume. Add a raw-content path — recommended shape:
  `GET /doc/file?format=raw` (or an appwire `doc/read` method) returning
  `{content, binary, mediaType, truncated, sizeBytes}` JSON, reusing the EXISTING `handleDocFile`
  guard/containment layer (`ResolveInRoot`, local-only, the 403-escape-vs-404-missing contract, floor
  §1.1-1.2) so only the presentation changes. `/doc/image` is unchanged (already raw bytes). **This is
  a controller/Jesse decision** — it is the one Go dependency of T5; if the doc pane is deferred
  instead, T5 drops and floor §1 moves to M9. Recommendation: add it (the design doc §6.4 lists doc
  viewer as a first-class native pane; an iframe reintroduces the boundary the rewrite removes).
- **MW-C (contingent) — steering classification wire field.** ONLY if T3's steering trace shows the
  classifiable kind is dropped by the reducer/projector rather than derivable from content. Likely
  unnecessary (legacy classified client-side by parsing `renderer.js:4713-4736`); listed so T3 has a
  defined escalation path rather than editing `reducer.ts` itself.

## W6-close fold-in points (controller applies at dispatch — W6's ledger + punch list are not yet written)

Wave 6 is still closing on `w6-surfaces`; its close will produce a divergence ledger and a punch list.
At W8 dispatch the controller reads them and routes each OPEN item to the stream whose manifest owns
the relevant files, per this table. Items W6 records as accepted-divergence need no W8 work — they
carry into T8's sweep as prior decisions.

| W6 close finding (category) | W8 fold-in home | Rationale |
|---|---|---|
| Model catalog swap (W6 shipped the interim Combobox; catalog is W8) | **T2** | T2 IS the catalog restore; the swap sites are T2's manifest. |
| Palette `/tasks` + `/status` inert (chrome lacks the trigger attributes the palette synthesizes clicks on — W6-T3 punch) | **T4** | T4 owns `chrome/**`; it adds the `[data-tasks-trigger]`/status trigger affordances the palette clicks. |
| Palette in-transcript search activation / scroll-to-hit (W6-T3 deferred beyond-parity) | **T3** (transcript owns scroll) or accept-permanently | Precise virtualized scroll-to-hit is a transcript-engine concern; T3 owns it if built, else recorded. |
| Spawn residuals (advanced-panel scalar-only validation; DirField mobile bottom-sheet — W6-T2 minors) | **T2** (closest — owns `panes/spawn/ModelField.tsx`) or accept | T2 already holds a spawn file; a residual spawn polish rides with it, else accepted-divergence. |
| Rail / sidebar / display residuals (Sheet left-anchor, ⌘B guard, display-gate regex, stale prefs comments, 900-vs-767 copy — W6-T5 fix-round) | **T6** (owns `shell/**`) only if a genuine bug survives the W6 fix round | W6 owns `shell/rail/**`; W8 touches shell only via T6, so a surviving shell/rail bug lands there. |
| Notifications residuals (post main-writer4 needs_you promotion) | controller-scheduled Go, or accept | No W8 stream owns `notifications/**`; a residual is a main-writer task or an accepted boundary. |
| Any W6 divergence-ledger entry marked accepted | none — recorded in T8's sweep | Prior decisions are cited, not re-litigated. |

## Cross-stream pins (concurrent T2-T7 code against these signatures; second-side reviewers read the first side's landed code)

- **PIN-A — `openBeside(pane: PaneRef): void` / `openDocBeside(params: DocParams): void`.** Producers
  **T3** (session file/image tool cards → `openDocBeside`) and the subagent "open transcript" rows
  (→ `openBeside({type:"transcript", …})`); implementer **T6** (`shell/paneActions.ts`) + **T5**
  (`panes/doc/openDoc.ts`). Until T6/T5 land, T3 codes against the T1 stubs; T3's reviewer reads T6's/
  T5's landed bodies. Dedup + mobile-degrade semantics per the seam comment.
- **PIN-B — `ModelCatalog` widget is optional for `ModelSwitch`.** Producer **T2**
  (`widgets/modelCatalog/**`); optional consumer **T4** (mid-session `ModelSwitch`). T4's busy-gate/
  dismiss fixes do NOT depend on T2; if T2 has merged, T4 adopts the widget for consistency and its
  reviewer diffs against T2's landed `ModelCatalog`; if not, T4 ships on the existing Combobox and the
  adoption is a later one-liner. No hard ordering.
- **PIN-C — `readDocFile` depends on MW-B, not on T5-internal state.** Producer of the data contract is
  the controller-scheduled **MW-B** endpoint; **T5** fills `protocol/docContent.ts` against it. Until
  MW-B lands, T5 codes the pane against the T1 `docContent.ts` stub shape (`DocFileContent`) with a
  fixture; T5's reviewer confirms the fixture matches MW-B's landed response before merge (the W6
  search-ref pre-absorb pattern).
- **PIN-D — settings file ownership is exclusive-per-file.** T2 owns EXACTLY
  `panes/settings/sections/launchShared/fields.tsx` inside `panes/settings/`; T7 owns
  `panes/settings/**` MINUS that one file. Different files, separate worktrees — no collision. Neither
  edits the settings barrel/`sections.ts` (if a new nav entry were needed it would be a T1 chokepoint —
  none is this wave).
- **PIN-E — global keydown owners stay disjoint.** ⌘K → palette (W6), ⌘B → sidebar cycle (W6). W8 adds
  NO new global chord (open-beside/popout are pane-scoped actions, not global chords). Neither T4 nor
  T6 registers a third global listener.

## Controller watch items (no W8 stream code)

- **`reducer.ts` / `tokens.css` are off-limits to streams this wave** (constraints above). A stream that
  believes it needs either STOPS and escalates; the controller lands the change (a quiet window for
  tokens; a scheduled main-writer for a reducer/wire field).
- **Auth exempt-path list + icon paths are Go-owned** (`hubedge/auth_token.go:109-117`). T7 must not
  rename an icon (it would silently un-exempt it); if a re-brand needs a new icon filename that is a
  Go touch, escalated to the controller — not a T7 edit.
- **must-ratify @ M9** (unchanged): the ask_user transcript re-architecture ratification gate
  (`no [data-ask-anchor]`/`.ask-settled-line`, dock not `form`-owned) — a documented Wave-4 choice,
  decided at M9/M10, untouched here. §Ambiguities #1's single-pane read-only-vs-composer resolution is
  a NEW ratification item flagged for the same gate.

## Self-review

- **Spec coverage (design §13 M8 = "doc viewer, `/thread/{ref}` single-pane mode, PWA, popout
  windows"):** doc viewer → T5; `/thread/{ref}` single-pane → T6; PWA → T7; popout → T6 (dockview-
  native). Design §5 "Global" M8 slice (doc/image viewer scoped to session cwd → T5; standalone
  `/thread/{ref}` → T6; PWA manifest/install → T7; `/auth?token=` cookie flow → T7) all covered. The
  final wave's ADDED scope: model catalog → T2 (Jesse W8 ruling, ledger 206); deferred transcript
  clusters → T3; chrome polish → T4; settings polish → T7 — every schedule-W8 triage item (12) and
  every named deferred cluster (4) homed in the tables above; the projector Go follow-up → MW-A;
  recent-prompts stays permanently dropped (W6 ruling, no W8 work).
- **Placeholder scan:** no "TBD"/"similar to task N"/"etc. (unspecified)". Every seam carries a full TS
  signature; every pinned value is literal (the `/api/models` field set, the 6 auth-exempt paths, the
  doc media types + 8 MiB/512 KiB caps + `Cache-Control` values, `DEFAULT_EFFORT_LEVELS`, the manifest
  color/attribute pins, floor counts 40/36/48/17/18/159/166). Approximate reverse-engineered counts
  (the deferred clusters' "~25"/"~8") are flagged as lower bounds T3 re-quotes, never presented as
  hard floor numbers.
- **Name/type consistency across tasks:** `openBeside`/`popOutPane`/`PaneRef`, `openDocBeside`/
  `DocParams`, `readDocFile`/`docImageURL`/`DocFileContent`, `isSinglePaneRoute`, `ModelCatalog`/
  `ModelCatalogEntry`/`ModelCatalogProps`, `PendingChips` are used identically in the seam block, the
  task bodies, and the pins. `ItemModel.error`/`turn.error`/`argumentsJSON`/`usePendingTurnEntries`/
  `PendingMethod`/`reasoning_effort_levels`/`display_name` match the real symbols verified in
  `reducer.ts`/`pendingTurnsStore.ts`/`web_spawn.go`/`ModelField.tsx`. `PaneTypeId` already carries
  `transcript`/`doc` (`paneRegistry.ts:8`) — no union edit needed; both self-register.
- **Manifests are disjoint:** T2 `widgets/modelCatalog/**` + spawn `ModelField.{tsx,module.css}` +
  the ONE settings `fields.tsx`; T3 `panes/session/transcript/**`; T4 `panes/session/chrome/**` +
  `panes/session/pending/**`; T5 `panes/doc/**`; T6 `shell/singlePane/**` + `panes/transcript/**` +
  `shell/paneActions.ts`; T7 `panes/settings/**` (minus `fields.tsx`) + manifest/icons +
  `stores/connection.ts`. `tokens.css` untouched. No two concurrent streams share a file (T2/T7 both
  sit in `panes/settings/` but on disjoint files in separate worktrees — PIN-D). T1's seam files are
  handed to their owning stream sequentially (T1 completes before streams branch).

## Spec ambiguities resolved (controller may override)

1. **`/thread/{ref}` = read-only transcript pane, or the app in single-pane mode with a live
   composer?** Design §6.7 says "renders the app in single-pane mode (also the share-link target)";
   §6.4 lists `transcript` as a distinct "read-only thread" pane type; the tested legacy §2.5:440-448
   keeps the composer LIVE in thread-document mode; the wave-3 `routing.ts` maps `/thread/{ref}` →
   `transcript` (a never-implemented placeholder — no component is registered). Resolved: **`/thread/
   {ref}` renders the SESSION pane (composer live, honoring the tested §2.5) inside a chrome-stripped
   single-pane shell layout**; the read-only `transcript` pane type is a DISTINCT open-beside surface
   (no composer) for viewing another thread. This honors §6.7 ("renders the app"), §2.5 (composer
   live), and §6.4 (transcript = separate read-only type), and reinterprets the wave-3 routing
   placeholder (T1 repoints it). **Flagged for M9 ratification** (it is a reinterpretation, and it
   changes which floor §2 rows are "met" vs "consciously diverged").
2. **Doc-viewer pane data path** (floor open-question §"doc_serve.go presentation vs data layer",
   lines 882-890). Resolved: **native React pane, existing routes' guard/status contract preserved,
   raw content via the controller-scheduled MW-B endpoint** (`/doc/image` already raw; `/doc/file`
   gains a raw mode). An iframe to the legacy HTML page is rejected — it reintroduces the boundary the
   rewrite removes and `/doc/file` is not SPA-flag-gated (floor cross-cutting #1). The native pane ADDS
   DOMPurify markdown sanitization the legacy lacks (floor §1.4:258-261) — a conscious security
   improvement. If the controller defers the doc pane, T5 + MW-B drop and floor §1 moves to M9.
3. **`/thread/{ref}` unknown-ref title (floor cross-cutting #2 / open-question line 904).** The legacy
   fallback title blanks moments after load (the `/state` poll re-swaps an empty title). Resolved:
   **the new single-pane mode keeps the fallback title (the raw ref) indefinitely** — the SPA has no
   equivalent title-blanking poll, so this is a free fix, recorded as a beyond-parity correction, not
   a behavior to replicate.
4. **panes.js §3 (48 items) — port or dockview-native?** Resolved: **the iframe + postMessage +
   localStorage mechanism is NOT ported** (§10 deletes `panes.js`); dockview provides tabs/splits/
   drag/resize/serialization/popout natively (design §6.4). T6 ports only the user-observable
   guarantees that are not automatic: open-beside from producers (→ `openBeside`) and the same-origin
   safety of the targets. The max-3-pane cap (floor §3.2) and auto-open-observer behavior (floor §3.7)
   are recorded as dockview-model divergences in T8's sweep (dockview manages space) unless the
   controller rules to preserve them.

## Genuinely open — controller decision needed

- **MW-B go / no-go (the doc-pane Go endpoint).** T5 has a hard dependency on a raw-file data path
  (§Ambiguities #2). The recommendation is to add it; the alternative is to defer the entire doc-viewer
  pane (floor §1, 40 items) to M9. This is a Jesse/controller scope+Go call before T5 dispatches — I
  did not invent the endpoint as settled, only recommended its shape.
- **Single-pane composer ratification (§Ambiguities #1).** Whether a `/thread/{ref}` share link should
  carry a LIVE composer (my resolution, matching tested §2.5) or be a READ-ONLY snapshot (the simpler
  share-link reading of §6.4/§6.7). Both are defensible; the resolution above picks composer-live and
  flags it for M9. If the controller prefers read-only, T6 mounts the read-only `transcript` pane at
  `/thread/{ref}` instead and T1's routing change is trivially different — a dispatch-time swap, not a
  re-plan.
