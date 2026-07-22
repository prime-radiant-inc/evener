# Web Rewrite Wave 6 — Surfaces (M6) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Waves 1-5
> conventions apply verbatim (wave worktree + sub-streams with exclusive manifests, wave-local SDD
> artifacts, controller-owned chokepoints, honest exit-code gates — tsc BEFORE vitest with
> test-file-count verification — commits as separate invocations, Biome is the lint gate, TDD with
> wire-true fixtures and mutation-verified regression nets).

**Goal:** the four remaining first-class *surfaces* become native to the workspace shell — (1) a
**spawn pane** that starts real sessions (replacing AppShell's `SPAWN_NOT_READY_NOTE` welcome
fallback), (2) the **⌘K command palette** (session search + 22 slash commands over Wave 5's action
surface), (3) the **notifications engine** (title count, favicon badge, OS notification, sound,
single-tab election), and (4) **display-preference application** (font-size / phone-density CSS
gates + the `sidebarMode` consumer the Wave-7 radio names but nothing yet obeys). The protocol,
stores, widgets, transcript, composer, and settings sections are all already built; W6 consumes
them and wires the surfaces on top.

**Parity floors:** `docs/web-ui/parity/parity-m6-surfaces.md` — **250 checkbox items across 4
sections** (its own count, line 429): **§1 Spawn = 109** (incl. `dir-picker.js`), **§2
Palette/Search = 71**, **§3 Notifications = 41**, **§4 Theme/Density/Font/Display = 29**, plus **5
cross-cutting hazard notes** at the top (floor lines 27-36). These are the real counts; cite them,
do not round or reinvent them. Every floor requirement a task cites must quote the floor's own
`path:line` anchor. Much of §4 (theme core §4.1-4.3, composer prefs §4.8) is *already satisfied* by
Wave-7's `prefs.ts` + `theme.tsx` + `display.tsx`; W6-T5's new work is the **application gaps** the
floor's own hazard list and the final review flag, not a re-port of §4.

**Prereqs:** integration branch `worktree-webui-workspace-shell` at HEAD `9a25cb14b` (final
two-wave review verdict READY FOR WAVE 6, all six gates green). Wave worktree `webui-w6-surfaces`
off integration; sub-streams branch off the wave branch after T1. Per-stream worktrees (the git
index is worktree-global — a stream must never share a checkout with another write-capable stream;
`.superpowers/feedback_parallel_review_worktree_collision`). **Merge to integration is
controller-owned and serial, and is EXCLUDED from the close task (T6).**

## Binding constraints (every task)

- **Chokepoints are controller-owned — never stream-owned edits.** `AppShell.tsx`, `Session.tsx`,
  `Settings.tsx`, `panes/session/composer/Composer.tsx` (a frozen Wave-5 file touched here only for
  the one leading-`/` palette hook), `widgets/index.ts` (barrel), `shell/paneRegistry.ts`,
  `shell/routing.ts` get single-line-append or controller-wiring treatment. T1 lands every
  chokepoint touch ONCE against the stable seam interfaces below; T2-T5 fill the seams and never
  touch a chokepoint again.
- **`src/styles/tokens.css` is a quiet-window exclusive.** Exactly **T5** may edit it, named in T5's
  manifest. T2/T3/T4 are forbidden from touching it; all their CSS lives in their own
  `*.module.css` files. (Ledger 2026-07-21: "theme touches tokens.css = wants a quiet window.")
- **Design system binds:** widgets only; **tokens-only CSS** (no literal colors/spacing — the
  `requireclass-contract.test.ts` guard scans textually and is comment-blind, so never write
  `styles.<name>` inside a comment; `requireClass(...)` for every new class); **color-is-attention**
  (a neutral surface stays neutral; color means needs-your-eye); accessible names on every
  interactive element; sentence case; honest liveness (no fake "live" state).
- **Wire truth, Conflict-aware.** All session mutations go through Wave-5's `threadsStore` actions
  (each already Conflict-typed — `ConflictError`, `threads.ts` `mapConflict`); a palette
  `/steer`,`/queue`,`/promote`,`/model` etc. surfaces a Conflict as its own blocked state, never a
  blind retry. User-action failures surface via the existing `useToasts()` singleton (Wave-5's
  decided convention) or the palette's own inline `.palette-error` strip — no new banner systems, no
  silent `.catch(()=>{})`.
- **TDD with wire-true shapes.** Confirm every real RPC/REST response shape against the Go handler
  (or a captured frame) before pinning a fixture — the floor's field lists are a reverse-engineered
  lower bound. The Wave-5 lesson: synthetic fixtures the wire never produces are how bugs hide.
- **Gates, per task and at close (honest exit codes, `.superpowers/feedback_await_behavior_not_timeouts`):**
  `npx tsc --noEmit` BEFORE `npx vitest run` (vitest exits 0 with parse-broken files silently
  excluded — tsc is the gate that catches them); vitest runs **bare in AND-chains** (never behind a
  grep'd pipe); assert the vitest **file count went UP** by the tests you added; `npm run build`
  then `git restore dist/PLACEHOLDER` (vite deletes the tracked placeholder each build — tree must
  be clean after); `npm run lint` (Biome ci) EXIT 0.

## Locked seam interfaces (T1 ships; streams import and fill)

T1 creates each seam as a compiling stub with its real signature, wires every chokepoint against
it, and hands the seam's own module tree to the owning stream. Concurrent streams import only the
signatures below.

```ts
// panes/spawn/startThread.ts  — the thread/start seam (T1 defines; T2 fills the body)
//   Uses appwire "thread/start" (types.gen.ts ThreadStartParams/Response), NOT REST /api/spawn.
//   input is assembled from prompt+attachments via the buildInput shape (threads.ts:304-312:
//   {type:"text",text} then one {type:"image",mediaType,data,name} per InputAttachment).
import type { InputAttachment } from "../../stores/threads";
import type { LaunchConfigLayer } from "../../protocol/types.gen";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
export interface SpawnRequest {
  cwd: string;                 // required (ThreadStartParams.cwd)
  prompt: string;              // RAW, untrimmed — floor §1.12 (spawn.js:1251,1273-1275)
  attachments?: InputAttachment[];
  harness?: string;
  modelProvider?: string;      // serf-model harness: "<provider>/<model>" split → provider half
  model?: string;              // model id (bare id for a non-serf harness — floor §1.4)
  reasoningEffort?: string;    // wire camelCase — floor §1.11 (spawn.js:1122-1125,1277,1281-1282)
  branch?: string;
  accessMode?: string;         // merged into launchOverrides.sandbox iff schema didn't set it — floor §1.8
  launchOverrides?: LaunchConfigLayer;
}
export interface SpawnResult { ref: string; } // from ThreadStartResponse.thread, leading "local:" stripped — floor §1.14 (spawn.js:404-417)
export function startThread(client: AppwireClientLike, req: SpawnRequest): Promise<SpawnResult>;

// panes/spawn/preflight.ts — working-dir preflight (T1 defines; T2 fills) — floor §1.13
//   validate via appwire "serf/path/validate" (PathValidateParams{path,kind:"dir"} →
//   {valid,error?}); fail-OPEN if the CHECK itself throws (spawn.js:573-580); create via REST
//   POST /api/dirs/create (no appwire method exists — verified). Returns the caller's next step.
export type PreflightOutcome =
  | { kind: "ok" }
  | { kind: "abort"; message: string }           // deterministic "not fixable" (spawn.js:582-588)
  | { kind: "offer-create"; path: string };      // in-form Create&start dialog (spawn.js:527-566)
export function preflightDir(client: AppwireClientLike, path: string): Promise<PreflightOutcome>;
export function createDir(path: string): Promise<void>; // POST /api/dirs/create; .error||"HTTP <status>"

// shell/palette/paletteController.ts — global opener (T1 ships singleton+empty overlay; T3 fills)
//   A vanilla store (createStore) the mounted <CommandPalette/> subscribes to. openPalette seeds
//   the input and synthesizes the same "render immediately" path as the legacy openWith
//   (search.js:164-169). Controller wires ⌘K/Ctrl-K, [data-search-trigger] clicks, and Composer's
//   leading-"/"-on-empty hook to this ONE function.
export function openPalette(initialQuery?: string): void;
export function closePalette(): void;

// notifications/index.ts — the engine (T1 ships no-op stub; T4 fills)
//   Idempotent; subscribes treeStore + connectionStore + prefsStore; owns document.title, the
//   favicon <link>, OS Notification, sound, and Web-Locks leader election. Controller adds one call
//   at AppShell module-eval, beside initPrefs().
export function initNotifications(): void;

// shell/rail/railController.ts — imperative rail reveal (T1 stub; T5 fills; T3 consumes)
//   Expands the session's project section and scrolls it into view — floor §2.5 /project
//   (search.js:515-516,556-566). No-op-safe if the rail is collapsed (reveals first).
export function revealSessionInRail(ref: string): void;

// shell/rail/RailHost.tsx — sidebarMode-aware rail host (T1: pass-through to <Rail/>; T5 fills)
//   Same mount contract as the current <Rail/>. Controller swaps <Rail/>→<RailHost/> at BOTH
//   AppShell mount sites (desktop sibling AppShell.tsx:202; StackHost railSlot AppShell.tsx:206).
export function RailHost(props: { railSlot?: never }): JSX.Element; // desktop: mode logic; mobile: plain Rail
```

## Locked cross-stream value pins (EXACT — no stream re-derives these)

- **Notification prefs (T4 reads; NEVER re-defaults).** The engine reads notification opt-ins
  EXCLUSIVELY from `prefsStore.getState().notifications` + `.notificationsLoudScope` and introduces
  **no default layer of its own**. Wave-7 already baked the decided **all-OFF** defaults into
  `prefs.ts` (the "code-wins" resolution): `loadNotifications()` returns `title:false, favicon:false,
  os:false, sound:false` (`prefs.ts:197-204`); `notificationsLoudScope` defaults `"asks"`
  (`prefs.ts:300`). Keys (`prefs.ts:177-182,356-359`): `serf.prefs.notificationsTitle` /
  `…Favicon` / `…Os` / `…Sound` (booleans, `"1"`/`"0"` via `readBool`/`writeBool`, `prefs.ts:143-152`);
  `serf.prefs.notificationsLoudScope` (`"asks"`|`"all"`, `readEnum`, `prefs.ts:154-157,163`).
  **THE top cross-wave trap** (final review Duty 3, schedule-W6 #4): the legacy runtime engine's v3
  migration defaults `title`/`favicon` **TRUE** (`notifications.js:31`, floor §3.1) — that default
  is **explicitly NOT ported**. T4 wires prefs → behavior; it must not resurrect the TRUE defaults.
- **Favicon dot colors (T4) — pinned dark-theme constants regardless of page theme** (floor §3.3,
  `notifications.js:35-44`): `error=#f7768e`, `needs_you=#e0af68`, `working=#7aa2f7`; priority
  `error > needs_you > working`; no dot when none apply. (These literals live on the generated
  `data:image/svg+xml` favicon, not in tokens.css — the one sanctioned non-token color site,
  because the favicon renders against dark browser chrome, not the app surface.)
- **Sound (T4)** (floor §3.5, `notifications.js:199-213`): 800 Hz oscillator, gain `0.1`, stopped +
  context closed after **120 ms**; every failure silently swallowed.
- **Display prefs (T5 applies).** `serf.prefs.fontSize` (`"s"|"m"|"l"|"xl"`, default `"m"`) mirrors
  to `document.body.dataset.fontSize` → attribute `data-font-size` (`prefs.ts:265-267,287`).
  `serf.prefs.phoneDensity` (`"compact"|"comfortable"`, default `"compact"`) → `data-phone-density`
  (`prefs.ts:261-263,286`). `serf.prefs.sidebarMode` (`"auto"|"pane"|"rail"`, default `"auto"`,
  `prefs.ts:294`) has **no document mirror** — its consumer is the rail (T5).
- **sidebarMode semantics** are already fixed by the shipped Wave-7 help copy (`theme.tsx:103-104`,
  verbatim): "Collapsed hides the sidebar entirely — reopen it with the ☰ chip (top-left) as an
  overlay drawer; Auto collapses below 1200px and expands above it. ⌘B cycles collapsed → pane →
  auto." So: `auto` = responsive (collapse < 1200 px, expand ≥ 1200 px); `pane` = always expanded;
  `rail` (label "Collapsed") = hidden, reopened via a top-left ☰ chip as an overlay drawer; **⌘B**
  cycles `rail → pane → auto`. T5 makes exactly this true.
- **`/theme` palette command (T3)** calls `prefsStore.getState().setTheme(value)` for
  `value∈{"dark","light"}` (`prefs.ts:307-317`) — which sets `data-theme` immediately. This FIXES
  floor hazard #1 / §4.4 (the legacy `/theme` toggled dead `body.light-theme` classes and changed
  nothing until reload). Beyond-parity, decided: reviewers confirm the palette theme change is
  visible immediately and persists.
- **Palette session commands → threadsStore actions (T3).** Exact 1:1 map, all already shipped by
  Wave 5 (`threads.ts`): `/compact`→`compact(ref)`; `/interrupt`→`interrupt(ref)`;
  `/clear`→`clearThread(ref)`; `/shutdown`→`shutdown(ref)`; `/steer <t>`→`steer(ref,t)`;
  `/queue <t>`→`queue(ref,t)`; `/drain-as-steer`→`drainAsSteer(ref,"")`;
  `/model <p/m>`→`setModel(ref,provider,model)`; `/reasoning-effort <lvl>`→`setReasoningEffort(ref,lvl)`;
  `/goal <t>`→`setGoal(ref,t.trim())`; `/aside`→`forkFromTurn(ref,{aside:true})`. `/fork` is
  intentionally OMITTED (floor §2.5, search.js:497-499 — needs an edited message the palette can't
  collect). Idle-guard blocked states preserved (floor §2.5): each turn-scoped command that needs an
  active turn returns the floor's inline "<verb> failed: no active turn" via the palette's blocked
  sentinel, derived from `model.status`/`capabilities`, never a thrown error.

## Tasks

### T1 (sequential): surfaces chokepoint — controller-owned
Lands every chokepoint touch once, against the seam interfaces above, so the four streams never
edit a chokepoint. Steps:
1. **Spawn pane exists.** Create `panes/spawn/index.tsx` (`registerPane<SpawnPaneParams>({id:"spawn",
   singleton:true, title:()=>"New session", component:lazy(()=>import("./Spawn"))})`), a **minimal**
   `panes/spawn/Spawn.tsx` (prompt `Textarea` + a working-dir `PathPicker` [the Wave-7 widget, its
   documented spawn reuse] + a "Spawn" `Button` calling `startThread`), and the `startThread.ts` /
   `preflight.ts` seams (signatures above; minimal working bodies — bare prompt + cwd → real
   session). `PaneTypeId` already includes `"spawn"` (`paneRegistry.ts:8`) and `routing.ts` already
   maps `/new`→spawn and `/s/{ref}` back — no routing edit needed.
2. **AppShell (chokepoint):** in `openRouteAsPane` (`AppShell.tsx:102-114`) route `spawn`→
   `openPane("spawn", route.params)`; delete `SPAWN_NOT_READY_NOTE` (`AppShell.tsx:76`) and its
   welcome fallback branch; add `initNotifications()` beside `initPrefs()` (module-eval,
   `AppShell.tsx:28`); mount `<CommandPalette/>` as a sibling of `<ToastRegion/>`; add the global
   ⌘K/Ctrl-K + `[data-search-trigger]` listeners calling `openPalette()`; swap the two `<Rail/>`
   mounts (`AppShell.tsx:202,206`) to `<RailHost/>`. `panes/welcome/Welcome.tsx`'s `note` prop stays
   (still used for other empty states) but no longer carries the spawn note.
3. **Composer (chokepoint, one branch):** in `Composer.tsx`'s keydown, when the message is empty and
   the key is `/`, `preventDefault()` + `openPalette("/")` (floor §2.1, the legacy renderer.js:6914
   entry). Nothing else in Composer changes.
4. **Seam stubs handed off:** `shell/palette/paletteController.ts` + `shell/palette/CommandPalette.tsx`
   (renders nothing until T3), `notifications/index.ts` (no-op), `shell/rail/railController.ts`
   (no-op) + `shell/rail/RailHost.tsx` (pass-through to `<Rail/>`).
5. **Gate:** full suite (tsc→vitest, count up); `npm run build`+restore placeholder; Biome; **live
   smoke** against a fake-`$HOME` hub (`serf-hub` holds a host-global flock at `$HOME/.serf/hub.lock`
   — `.superpowers/project_web_rearchitecture_study`; W5 close lesson): `/new` opens the spawn shell,
   a bare-prompt spawn creates a session and routes to `/s/{ref}`, ⌘K opens an (empty) overlay.
   Suggested tier: **opus** (contract-establishing; the seam signatures every stream imports).

### T2 ∥ T3 ∥ T4 ∥ T5 (streams off the wave branch after T1)

- **T2 — spawn body** (manifest: `panes/spawn/**`). Fills the pane on T1's seams. Covers floor §1
  (**109 items**): the 6-chip bar in order harness/model/reasoning_effort/working_dir/branch/
  access_mode (§1.1, spawn.html:11-39) with desktop+mobile mirrored rows; the dir-picker
  (`serf/projects/recent` recent-projects **only on first listing** §1.6/dir-picker.js:47-55,
  `serf/dirs/complete` completions debounced 150 ms §1.6/dir-picker.js:303-307, accept-recent vs
  browse-into-dir §1.6/dir-picker.js:249-vs-270, `..` parent row, stale-response `requestID`
  drop); sticky-defaults/prefill layering (§1.9, spawn.js:1132-1149 — harness/branch/access before
  working_dir so branch auto-resolution sees the winner) keyed `serf-hub.spawn-defaults.<cwd|global>`;
  stale-model detection/cleanup (§1.10, spawn.js:154-175,249-256 — malformed/stale clear, unknown
  left, inline dismissible notice); advanced schema options (§1.11, `serf/launch/schema` →
  select/radio/boolean/modelPicker/text/integer/pathList/modelList/envMap/mcpServerList, path
  validation, `serf/launch/resolve` "show resolved config", camelCase `reasoningEffort` precedence);
  attachments (§1.12 — the shared `Dropzone` widget + paste/picker → base64 `InputAttachment`,
  submit blocked while any is `.pending`, attachment-only submit allowed); preflight (§1.13, T1's
  `preflight.ts`); submission (§1.14, `startThread`); `?dir=`/`?prompt=` URL prefill (spec §5) read
  from `window.location.search`. **Interim model + reasoning-effort pickers** = the Wave-5
  `ModelSwitch` pattern (appwire `model/list` → `provider/model` `Combobox`; `ModelDescriptor` is
  `{provider,model}` only, `types.gen.ts:422-425`). The **rich catalog** the floor §1.4/§1.5
  describe (REST `/api/models` `display_name` + capability badges + provider grouping + Recent +
  diagnostics + pricing; the distinct `none` vs `(default)` effort split) is **Jesse-decided Wave 8,
  NOT W6** (ledger 206: "RESTORE IN W8; interim plain input blessed") — those specific floor rows
  are W8-deferred, annotated in the parity sweep. Branch auto-resolution (§1.7) is REST-only
  (`GET /api/git/head?cwd=`; floor confirms no appwire `git/head`). Suggested tier: **opus**
  (sticky-default layering, stale-model cleanup, and the schema engine are the wave's densest logic).

- **T3 — command palette** (manifest: `shell/palette/**`; fills T1's `paletteController.ts` +
  `CommandPalette.tsx`). Covers floor §2 (**71 items**) + triage W6 #1 (palette not implemented).
  The ⌘K overlay (Dialog/Sheet-based widget composition; its own `.module.css`): open/close &
  global wiring (§2.1); the three-mode model recomputed per keystroke — `command-args` if a command
  is selected, else `command-filter` if input starts with `/`, else `search` (§2.2); **search mode**
  (§2.3, debounced 150 ms, REST `GET /api/search?q=` — **no appwire `search` method exists**,
  verified; Live / "Past · N" / "In session · N" sections, `<mark>` highlighting, ↑/↓ wraparound,
  Enter/⌘Enter/⇧Enter); **in-session search reads the focused session's `ThreadModel` from
  `threadsStore` (turns→items→text), NOT the DOM** — the legacy scanned `#conversation`
  (§2.3/search.js:961-982), impossible against the virtualized transcript; snippet building
  (~40 chars/side, §2.3/search.js:984-992) ports unchanged over the model; activate = focus the
  session pane + best-effort scroll (precise virtualized scroll-to-hit is beyond-parity, flagged in
  the sweep); **command-filter mode** (§2.4 — the 22-command registry search.js:326-518, scope
  gating global/ended-ok/session, `commandScore` fuzzy ranking, Recent from
  `localStorage["serf.search.recentCommands"]`); **every command** (§2.5) via the pinned
  threadsStore map + navigation (`/new`,`/spawn`,`/settings`,`/dashboard`→`routing.navigate`),
  `/theme`→`prefsStore.setTheme` (the hazard-#1 FIX), `/copy-id`, `/tasks`/`/status` (synthesize the
  chrome trigger clicks), `/project`→`railController.revealSessionInRail(ref)` (PIN below),
  `/upgrade`→`serf/upgrade`; command-args mode (§2.6); execution/error surfacing via the
  `{paletteBlocked,message}` sentinel + inline `.palette-error` strip (§2.7); the 7-row help panel
  (§2.8). Suggested tier: **opus** (mode state machine + 22-command registry + Conflict/idle-guarded
  session commands).

- **T4 — notifications engine** (manifest: `notifications/**`; fills T1's `initNotifications`).
  Covers floor §3 (**41 items**) + triage W6 #3 (loudScope) + #4 (all-OFF defaults — the top trap).
  Reads the pinned all-OFF prefs; derives **title count** (§3.2 — `"(needsYou+error) "` prefix, only
  when >0, gated on the `title` pref; base title from the focused pane via `workspaceStore` +
  `paneRegistry.title()` since the SPA has panes, not the legacy's server sections — the one honest
  divergence from §3.2's `"<section> · serf hub"`); **favicon dot** (§3.3, the pinned colors, inline
  `data:image/svg+xml` rebuilt per apply on a single `link[rel="icon"]`); **OS notification** (§3.4
  — requires `Notification.permission==="granted"` AND document not focused; click focuses window +
  navigates `/s/<threadId>`); **sound** (§3.5, pinned 800 Hz/0.1/120 ms); **baseline + edge-fire
  gating** (§3.6 — the engine's first `treeStore` snapshot IS the baseline; counts apply
  unconditionally, but OS/sound fire only on a transition INTO `needs_you`/`error`, only when
  unfocused, only on the elected leader; **loudScope** narrows: `"asks"` fires only for an
  `ask_pending` transition or an `error`, `"all"` for every qualifying transition); **single-tab
  election via Web Locks only** (§3.7 — `navigator.locks.request("serf-hub-os-leader",{ifAvailable:
  true})`; no-locks env → every tab is leader; **no BroadcastChannel**, floor hazard #2). The engine
  gets its data from `treeStore` (already subscribed to `serf/attention/changed` + `serf/tree/changed`
  and re-fetched on reconnect, `tree.ts:246-289`): title/favicon from `treeStore.attentionSummary`
  (`{needsYou,error,working}`, `tree.ts:81-85`); per-thread transitions by diffing the `needs_you`
  tier + `ask_pending` node flag (`tree.ts` TreeNode) across snapshots. **First step:** trace the
  real per-thread attention fields the tree carries and pin a wire-true fixture before building the
  transition detector (the W5 "trace the daemon shape first" discipline). Verify whether the
  Wave-7 `notifications.tsx` settings section already performs the browser
  `Notification.requestPermission()` on enabling the `os` toggle (floor §3.8, settings-notifications.js:42-58);
  if it does not, add it here (it is the notifications surface). Suggested tier: **opus** (edge-fire
  gating correctness, transition detection, election, and the all-OFF reconciliation).

- **T5 — display-preference application** (manifest: `src/styles/tokens.css` **[quiet-window
  exclusive]** + `shell/rail/**`). Covers the floor §4 **application gaps** (not the already-built
  theme core) + triage W6 #2 (sidebarMode consumer). (a) **tokens.css font/density gates:** add
  rules keyed off the pinned attributes so the persisted prefs finally change rendering —
  `body[data-font-size="s|m|l|xl"]` sets a `--font-scale` multiplier on the root type ramp (ship
  `0.9 / 1.0 / 1.1 / 1.25`, tunable in design review), `body[data-phone-density="compact|comfortable"]`
  sets a row-spacing multiplier under `@media (max-width:900px)` (ship `1.0 / 1.25`) — floor §4.5/§4.7
  note the legacy set the attributes but "no CSS in the new design system keys off them yet"
  (`prefs.ts:44-49`); T5 supplies the CSS. (b) **sidebarMode consumer** (`RailHost.tsx` + `railController.ts`
  + a `useSidebarMode` hook): implement the pinned semantics — `auto` responsive at the **1200 px**
  desktop threshold (distinct from the 900 px mobile/stack breakpoint in `useIsMobile`), `pane`
  always expanded, `rail`/"Collapsed" hidden behind a top-left **☰ overlay drawer**, and a global
  **⌘B** listener cycling `rail → pane → auto` (`prefsStore.setSidebarMode`); reconcile with the
  rail's existing boolean collapse (`serf.rail.collapsed.v1`, `Rail.tsx:49-66`) — the `rail` mode
  subsumes it. Mobile is unaffected ("Desktop only" per the copy): `RailHost` renders the plain
  `<Rail/>` inside `StackHost`'s drawer via `useIsMobile`. Fill `revealSessionInRail(ref)` for T3
  (lift the rail's `expandedOverrides`/scroll to an imperative handle). Coordinate the `/theme`
  visible-apply fix with T3 (T3 calls `prefsStore.setTheme`; T5 owns no theme CSS — theme already
  keys off `[data-theme]`, so no tokens.css theme rule changes). Suggested tiers: **sonnet** for the
  mechanical tokens.css font/density gates; **opus** for the sidebarMode consumer (shell integration
  + ⌘B + overlay drawer + the reveal handle).

### T6: wave close
Parity sweep of **all 250 floor items** across the 4 sections (annotating the W8-deferred rich-model
rows §1.4/§1.5, the accept-permanently divergences, and each hazard-note resolution — hazard #1
`/theme` FIXED, hazard #2 Web-Locks-only KEPT); **live proof** on a real hub under an **isolated
fake `$HOME`** (the host-global `$HOME/.serf/hub.lock` flock forbids a shared hub;
`.superpowers/project_web_rearchitecture_study`): spawn a real session from the pane (bare + with an
image attachment + `?dir=`/`?prompt=` prefill), drive the palette (search, `/steer`,`/model`,
`/theme`-applies-immediately,`/project`-reveals), notifications (grant OS permission, background the
tab, force a `needs_you`/`ask_pending` transition, observe title+favicon+OS+sound under each
loudScope, confirm a fresh load does NOT re-alert on pre-existing attention), and display (font-size
+ density visibly change, ⌘B cycles the sidebar through all three modes). **Critically: the
queue/steer/edit/promote-under-load journey Wave 5 could not run** — a bare `serf serve` advertises
those capabilities false, so it must run against a **HUB-SPAWNED** session (now possible via the W6
spawn pane; W5 close lesson, ledger 202). Full gates + wave6-report. **The merge to integration is
controller-owned and serial — NOT part of T6.** Suggested tier: **opus**.

## Cross-stream pins (concurrent T2-T5 code against these signatures; second-side reviewers read the first side's landed code)

- **PIN-A — `railController.revealSessionInRail(ref: string): void`.** Producer **T5**
  (`shell/rail/railController.ts`), consumer **T3** (`/project` command). Shape: expand the session's
  project section, scroll it into view (`block:"center"`), no-op-safe when the rail is collapsed
  (reveal first). Precedent: `openPalette` singleton pattern (T1) + Rail's `expandedOverrides`
  (`Rail.tsx:129`). Until T5 lands, T3 codes against the T1 stub; T3's reviewer reads T5's landed
  `railController.ts`.
- **PIN-B — notification prefs are read-only, all-OFF.** Producer is the shipped `prefs.ts`
  (`loadNotifications` false×4 `prefs.ts:197-204`; loudScope `"asks"` `prefs.ts:300`; keys
  `prefs.ts:177-182`; `readBool` `"1"/"0"` `prefs.ts:143-152`). Consumer **T4**. T4 adds no default
  layer; its reviewer diffs T4 against the legacy `notifications.js:31` TRUE defaults to confirm they
  are not resurrected. Precedent: `display.tsx:24-25` reads the same store for enterToSend/showCost.
- **PIN-C — `InputAttachment` → `InputItem`** (spawn). Producer is the shipped `threads.ts`
  (`InputAttachment` `threads.ts:45`; `buildInput` shape `threads.ts:304-312`). Consumer **T2**:
  assemble `ThreadStartParams.input` as `[{type:"text",text}?]` + one `{type:"image",mediaType,data,
  name}` per attachment — the exact buildInput shape (mirror it locally; `buildInput` is unexported).
  Precedent: `Composer.tsx:350` (`toInputAttachments()` from the shared `useAttachments`); the
  `Dropzone` widget and pure `encodePng` leaf are reusable read-only.
- **PIN-D — global keydown owners are disjoint.** ⌘K/Ctrl-K → palette (T1 wires; T3 behavior); ⌘B →
  sidebar cycle (T5, in `RailHost`). Both mount under AppShell; separate listeners, no capture
  conflict. Neither stream adds a third global chord.

## Controller watch items (no W6 stream code)

- **Triage W6 #5 — instance-CRUD cross-client live-update** is **already fixed on `main`**
  (`28e2b2141`, reuses `serf/auth/updated` BroadcastAll which `credentialsStore` already subscribes
  to) but is **main-only, not yet on this branch**. No W6 code: it arrives with the next
  main→integration re-absorb. The controller verifies it lands at that absorb (and that the absorb's
  likely `app_rpc.go` conflict — main's CRUD-broadcast vs the branch's W7-T1a overview method,
  ledger 214 — resolves as a union). T6's live proof need not cover it.
- **must-ratify @ M9** (unchanged, not W6): the ask_user transcript re-architecture ratification gate
  (no `[data-ask-anchor]`/`.ask-settled-line`, dock not `form`-owned) — a documented Wave-4 choice,
  decided at M9/M10, untouched here.

## Self-review

- **Spec coverage (design §13 M6 = "spawn, palette/search, notifications/badges, theme/prefs"):**
  spawn → T2; palette/search → T3; notifications/badges → T4; theme/prefs application → T5 (theme
  core already shipped Wave-7; §5 "theme light/dark + density + font size" application gaps covered).
  §5 spawn sub-items (dir picker + `serf/projects/recent` #35, path validation, model/harness
  pickers, launch overrides, `?dir=`/`?prompt=`) all in T2. §5 "single-tab election (Web Locks +
  BroadcastChannel)" → **resolved to Web-Locks-only** (see ambiguities). All 5 final-review
  schedule-W6 items folded (#1→T3, #2→T5, #3→T4, #4→T4, #5→controller watch).
- **Placeholder scan:** no "TBD"/"similar to task N"/"etc. (unspecified)". Every seam carries a full
  TS signature; every pinned value is literal (prefs keys, encodings, colors, Hz/ms, scale numbers,
  breakpoints, floor counts 109/71/41/29/250).
- **Name/type consistency across tasks:** `openPalette`/`closePalette`, `initNotifications`,
  `revealSessionInRail`, `RailHost`, `startThread`/`SpawnRequest`/`SpawnResult`,
  `preflightDir`/`PreflightOutcome`/`createDir` are used identically in the seam block, the task
  bodies, and the pins. `InputAttachment`/`buildInput`/`ModelDescriptor`/`ThreadStartParams`/
  `attentionSummary`/`ask_pending` match the real symbols verified in `threads.ts`/`types.gen.ts`/
  `tree.ts`. Chokepoint list matches the brief exactly (+ Composer.tsx, explicitly justified as a
  frozen-Wave-5 one-branch hook given controller-wiring treatment).
- **Manifests are disjoint:** T2 `panes/spawn/**`; T3 `shell/palette/**`; T4 `notifications/**`;
  T5 `src/styles/tokens.css` + `shell/rail/**`. Only T5 touches tokens.css (quiet-window exclusive).
  No two concurrent streams share a file; T1's seam files are handed to their owning stream
  sequentially (T1 completes before streams branch), so T2 owning `panes/spawn/**` (incl. T1's
  skeleton) and T3 owning `shell/palette/**` (incl. T1's stubs) is not a concurrent collision.

## Spec ambiguities resolved (controller may override)

1. **Single-tab election — spec says "Web Locks + BroadcastChannel" (§5, §6.8); floor says
   Web-Locks-only (§3.7, hazard #2).** Resolved to **Web-Locks-only.** Web Locks fully satisfies
   single-tab election on its own (floor-confirmed); BroadcastChannel would add cross-tab state sync,
   which the shipped `prefs.ts` *deliberately* omits ("No cross-tab `storage` event sync: deliberately
   omitted, matching the legacy", `prefs.ts:57-62`). Adding it is YAGNI and contradicts a landed
   decision. Flagged for ratification.
2. **`sidebarMode` `pane` semantics.** The legacy `SerfSidebar.applySidebarMode` internals were not
   in the floor's read-set, and the spec (§6.4) states "the tree is not a pane." Resolved using the
   **shipped Wave-7 help copy** (`theme.tsx:103-104`) as the authority: `auto`=responsive@1200px,
   `pane`=always-expanded, `rail`("Collapsed")=hidden→☰ drawer, ⌘B cycles rail→pane→auto. This
   matches the copy the user already sees, needs no dockview "sidebar-as-pane" (which the spec
   forbids), and gives each of the three radio values a real behavior.
3. **In-session palette search.** The legacy scans the `#conversation` DOM (§2.3); the new transcript
   is virtualized, so a DOM scan is impossible. Resolved to **scan the focused `ThreadModel`** in
   `threadsStore` (turns→items→text); activate focuses the session + best-effort scroll. Precise
   scroll-to-hit in the virtualized list is beyond-parity, flagged in the sweep rather than blocking.
4. **Spawn model + reasoning-effort pickers.** Resolved to the **interim `model/list` Combobox**
   (Wave-5 `ModelSwitch` pattern); the rich REST-`/api/models` catalog (badges/grouping/Recent/
   diagnostics/pricing; the `none`-vs-`(default)` effort split) is **Jesse-decided Wave 8** (ledger
   206), not a W6 gap. The affected floor §1.4/§1.5 rows are annotated W8-deferred in the sweep.

## Genuinely open — controller decision needed

- **Recent *prompts* (floor §1.1, `spawn.html:117-124` `.RecentPrompts`) has no appwire method**
  (only `serf/projects/recent` for *directories* exists — verified). The legacy sourced them from a
  server-rendered template var that the deletion wave removes. Options: (a) localStorage-back
  per-project recent prompts client-side (simplest, matches the "per-device" ethos; my
  recommendation), or (b) defer with sign-off as an accepted parity drop. T2 needs the answer before
  building the recent-prompts row; I did not invent a wire method for it.
