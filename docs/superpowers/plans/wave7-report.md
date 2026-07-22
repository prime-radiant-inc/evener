# Wave 7 (Settings) — wave report

Worktree: `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/webui-w7-settings`, branch `w7-settings`.
Base (merge-base with `origin/main`): `4128e762d`. Wave HEAD: `7381dd78f`.
The close (T5, pre-merge portion) commit range is **`11944e7a3..7381dd78f`** — one code commit (`7381dd78f`, comment correction) plus the report/artifact commits recorded at the end of this file. Everything below `11944e7a3` (the three streams + controller wiring + the 7-item fix round) was built and reviewed before this close ran; it is summarized here, not re-done.

Wave 7 rebuilt the entire Settings surface (18 parity sections) as the React/dockview SPA, replacing the legacy server-rendered `templates/partials/settings/*` + `assets/*.js` UI. It ran in parallel with wave 5 off a shared base; **merges to integration are serial — W5 first, then W7 absorbs and merges** (see "Merge-time remainder", which is NOT part of this close).

---

## Wave trail

### T1a — hub-side `serf/settings/overview` wire method (`87b7367ec`, `c1713edff`)
Added the `serf/settings/overview` appwire method and the hub-side `hubSettingsOverview` implementation (`cmd/serf-hub/app_rpc_settings_overview.go`) that answers Settings → General/Hub/Storage/Agents/Codex-launch/MCP-servers (the read-only, overview-fed half). This is the one Go-side deliverable this branch carries; it is gated below (`go build ./...`, `go test ./cmd/serf-hub/...`). Review fixes: doc cross-reference + test hermeticity.

### T1b — settings shell, routing, and the shared widgets (`3d58927ab`, `9631b3cbf`, `a72339328`, `d88a163f9`, `d42f26b4e`, `1c5e72cf0`, `f50b5606e`, `42781d7b6`)
The frozen Settings pane shell + nav + routing, plus the new widget primitives every stream builds on: `FormRow`, `RadioGroup`, `ConfirmDialog`, `CollectionEditor`, `PathPicker`. A CI guard for `requireClass`/styles class references was added (`72e3031ab`) and its silent-skip gap closed per review (`cd942f2f6`).

### Stores (`9684ad49c`, `f78d9158c`, `1d5a50169`, `b96729249`)
`prefs` (theme/density/sidebar/font-size/transcript/display/notifications), `credentials` + `launchConfig` wire-truth stores, `settingsOverview` (fetch-once cache), `extensions` (marketplace/plugin/launch-layer wire truth).

### Three section streams (merged `43c7b36c2`, `75ce0a3e7`, `f32cf4c94`)
- **agents-models**: Credentials (instance CRUD, API-key, OAuth dual flow — `e07f81bbe`), Agents + Codex launch (`8295f7a41`), schema-driven launch-config engine + Serf launch + per-project (`c938bc268`), In-repo trust (`b994a567d`).
- **simple**: General/Hub/Storage (overview-fed, read-only — `beaa5f6bc`), Theme/Transcript/Display/Notifications (`58d0fca6d`).
- **extensions**: Marketplaces & Plugins (`2c2a831c4`), Plugins/Skills directory sections + shared `DirListSetting`/`PathListEditor` (`fd1a1a9af`, `c5c25c2c4`), MCP servers (`ec3460de1`).
Cross-cutting stream fixes: client-not-ready race across all six sections (`027b24cf3`), boolean encoding to `"1"/"0"` cross-wave contract (`932eeddca`), `initPrefs()` hydration entry point (`590e797fe`), inrepo stale-cwd resolve (`56350e822`), device-poll no-reschedule pin (`a6339ceae`), credentials mounts-before-connect integration test (`622ad9e37`).

### Controller integration wiring (`2a88e83a1`)
Wired the Agents & models sections into the frozen shell (`e2a38d33f`) and finalized the section registry/route table.

### Close fix round — 7 items (`d186c3cf6..249943f94`, + report `11944e7a3`)
All approved (fix-round review: 8/8 probes clean, 154 files / 2332 tests green). Per `.superpowers/sdd/w7-close-f1-report.md`:
1. **Cross-client staleness subscriptions** (`d186c3cf6`) — the wave's headline fix (below).
2. `dirListSetting` → `useConnectedEffect` migration (`9e12bdf50`).
3. CollectionEditor `renderAddField` slot + PathListEditor fold + **envMap structured input restored** (`d4f8285ba`).
4. ConfirmDialog `busy` prop across 5 sites (`eee47ed7b`).
5. Install-confirm source line (`3bded4d2e`).
6. **Theme honors `prefers-color-scheme`** (`249943f94`) — Jesse's veto open (below).
7. Gitleaks pass — clean, no change needed.

### This close (item 0 — `7381dd78f`)
Two subscription-payload comments (`stores/credentials.ts`, `stores/extensions.ts`) claimed the notification payloads are "empty/no fields on the wire". That is wrong about the wire. Corrected against verified source:
- `serf/auth/updated` carries `{provider, activeSource}` — `cmd/serf-hub/app_rpc.go:764-767` (`notifyAuthUpdated`).
- `serf/launch/updated` carries `{cwd, layer}` — `app_rpc.go:772-775` (`notifyLaunchUpdated`).
- `serf/marketplace/updated` and `serf/plugin/updated` genuinely send empty maps — `app_rpc.go:657,663`.
- All four generated payload types are `{}` in `protocol/types.gen.ts` (554/594/604/616) because codegen cannot see into Go's untyped `map[string]string`; the refetch is payload-agnostic so nothing reads them. Comments now state exactly this.

---

## Parity sweep (both floor docs, all 18 surfaces)

Swept `docs/web-ui/parity/parity-m7-settings.md` (18 sections + Appendices A/B) and the settings-scoped portions of `docs/web-ui/parity/contracts-sidebar-search-settings.md`, classifying every `- [ ]` requirement MET / DIVERGED / GAP against the actual code. The GAP and both high-stakes divergence claims were re-verified against source before landing here.

**Headline: no Critical or Major gaps. All 17 nav sections + per-project are implemented (no placeholders). 5 Minor gaps. An 18-entry divergence ledger.**

### GAP punch list (Minor only — nothing above Minor)
1. **§7a** `/settings/providers` does not redirect to `/credentials`; the deprecated URL falls through to `PlaceholderSection` ("hasn't been built yet"). `routing.ts` has no `providers`→`credentials` redirect; the nav points at `/credentials`, so only stale bookmarks to the old URL are affected. (`protocol/routing.ts`, `Settings.tsx:109`, `PlaceholderSection.tsx:19`.)
2. **§13/§14** dir-list "N entries" count header not rendered (`DirListSetting`/`CollectionEditor` have none) — §12 *does* show a count, so it's an asymmetry.
3. **§12b/§12e** `withBusy` not applied to the non-destructive per-row buttons: Marketplaces **Refresh** (`MarketplacesSection.tsx`), Installed **Enable/Disable · Auto-upgrade · Upgrade** (`InstalledSection.tsx`) — not disabled during their RPC, so a double-click can double-fire. (Destructive actions all have busy via ConfirmDialog.)
4. **§12e** Installed plugin status dot (warning/ended/idle) not rendered — state conveyed only via Chips, so a healthy/idle plugin has no positive indicator.
5. **§18** Per-project `?cwd=` page renders inside the settings-nav shell rather than the legacy standalone no-nav page — navigation-context cosmetic only.

### Divergence ledger (recorded, conscious)
The full 18-entry ledger is in `.superpowers/sdd/` sweep output; the load-bearing entries:
- **Read-only sections** (General/Agents/Codex/Hub/Storage + MCP discovered half) fetch `serf/settings/overview` instead of server-rendered HTML (w7-task-1a).
- **Theme "system" follows OS** via a real `prefers-color-scheme` matchMedia listener — implemented, not softened (`249943f94`; `prefs.ts:207-253`). **Jesse's veto open** (below).
- **envMap** structured NAME/value add-inputs restored (`d4f8285ba`; `collectionFields.tsx`).
- **§9/§11/§18 have no cross-client live-update** — `launchConfigStore` holds no cached state (w7-close-f1 item 1 table; below).
- **Notifications defaults resolved to code** (all four toggles OFF) and the intro copy rewritten to "all opt-in" (`notifications.tsx`; `prefs.ts:187-198`). ⚠ cross-wave: the runtime notification engine's own v3 migration defaults title/favicon *true* — if it reads the same `serf.prefs.*` keys the two defaults disagree; out of W7 scope, flagged.
- **Mobile nav-as-page** = React conditional render (`useIsMobile()` < 900px) + Back, not `body[data-settings-pane]` (w7-task-1b Scope B).
- **Added** the cross-client staleness subscriptions (the headline fix — `d186c3cf6`).
- **ConfirmDialog on every destructive/consequential action** (legacy used native `confirm()` or nothing); install-confirm gained a Source line.
- **Model picker not ported** — `modelPicker` renders as a plain `provider/model` free-text input, not the searchable catalog/badges/costs/Recent popup. Documented **in code only** (`fields.tsx:1-11`), NOT in the SDD reports; `test-settings-model-picker.js` behaviors are unmet. **Verified against source. The gate should consciously bless this cut — if unintended it is a Major reduction, not Minor.**
- **Sidebar-mode radio persists but is inert this wave** — `serf.prefs.sidebarMode` is written and rendered but no consumer applies it (the real collapse is a separate `serf.rail.collapsed.v1` in `shell/rail/Rail.tsx:49`). Documented **in code only** (`prefs.ts:45-50`), NOT in the SDD reports. **Verified against source (zero consumers). Same gate caveat as the model picker.**
- Launch-engine path/pathList = validated free-text (no PathPicker); native Constraint Validation bubble not ported (`fields.tsx:12-30`).
- CustomEvents (`serf-hub:transcript-system-status-changed`, `serf-hub:notifications-changed`) not dispatched — replaced by prefs-store reactivity; downstream live-update depends on other-wave consumers subscribing (none exist in this worktree — verified).
- `showCost`/`enterToSend` not mirrored to `body.dataset` (only density/fontSize are); the `body[data-show-cost]` CSS gate has no W7 writer — the W5 cost-display consumer must read the pref directly.

### Parity-doc defect found and corrected
**`parity-m7-settings.md` §7h item #2 is wrong.** It says device-code polling is *not* stopped by a transient poll error. The actual legacy (`templates/partials/credentials.html:79-108`) sets the error, refreshes, then `return`s with **no reschedule** — it *does* stop. The React port matches the real legacy (`oauthDialogs.tsx`), and this wave already pinned that behavior in a test (`a6339ceae`). The gate should treat that doc line as erroneous, not as an unmet requirement — no gap.

---

## Live proof (real hub, real browser, no mocks)

Built the real thing (`npm run build` → `git restore dist/PLACEHOLDER`; `go build -o serf-hub ./cmd/serf-hub` embedding the fresh dist), ran an **isolated** hub — `hub_state_root`/`run_dir`/`past_index_db` under a scratch dir, `addr 127.0.0.1:19280`, `SERF_HUB_WEB=new`, real provider keys sourced from the repo `.env` — and drove the UI with Chrome. Every navigate used an explicit `127.0.0.1:19280` URL; all credential/launch mutations landed in the isolated state root (verified: isolated `launch.toml` holds the edits, Jesse's `~/.serf/launch.toml` untouched). Screenshots in `.superpowers/sdd/w7-live-evidence/` (copied from the scratch run).

| # | Journey | Verdict | Evidence |
|---|---|---|---|
| 1 | Credential add → OAuth device flow → default switch | **Pass** | Added instance `worktest` (openai); real OpenAI **device-code flow** rendered honestly ("Sign in to openai", live device code, "Waiting for you to authorize…", Copy/Send-me-to-OpenAI/Cancel) — driven to the human-authorization boundary, then **cancelled** (never completed); default switched to `google` via `setDefault` (local UI updated). `oauth-dialog.png` |
| 2 | Marketplace add → browse → install-with-confirm → disable | **Pass** | Added `sp-test` (owner/repo `obra/superpowers-marketplace`) → 3 entries; browsed superpowers-marketplace → 10 real plugins fetched live; Install "superpowers-chrome" showed the confirm dialog **with the item-5 Source line** (`Source: github: obra/superpowers-marketplace`); installed v3.0.2 → Installed(1); **Disabled** → button flipped to Enable + "disabled" chip. `marketplace-*.png`, `install-confirm.png`, `installed-plugin.png` |
| 3 | Dir add with validate | **Pass** | Invalid `/nonexistent/...` → validation error ("no such …") and **input kept** (no add); valid dir → added, input cleared, empty-state gone; PathPicker "Use this folder" typeahead shown. `dir-added.png` |
| 4 | Launch-config edit → resolve | **Pass** | Serf-launch form rendered the full option schema; edited Model `""` → `anthropic/claude-sonnet-4-5`, Save → **"Launch defaults saved"** (setLayer + resolve); persisted to the isolated `launch.toml`. `launch-config.png`, `launch-saved.png` |
| 5 | Theme/density flips persisting | **Pass** | System→Dark (`data-theme=dark` live), Phone density→Comfortable, Font size→L — all persisted to `serf.prefs.*`; survived reload (radios show Dark/Comfortable/Auto/L). **prefers-color-scheme listener verified**: with theme=system and OS=light, `data-theme=light` was applied (`matchMedia` present) — the new OS-follow behavior. `theme-dark-persisted.png` |
| 6 | Overview sections showing real daemon data | **Pass** | General showed real isolated-hub data: hub address `127.0.0.1:19280`, run/state/past-index dirs, spawn timeout 30s, and the **bearer token masked as "•••••••••• just now"** (never-echo honored; the header also states "The UI never displays stored values"). |
| 7 | Cross-client staleness (the headline fix) | **Pass** | Two tabs on Credentials. In tab A, set an API key on `worktest` (real `serf/auth/updated` broadcast). Tab B (**never reloaded**) live-updated `worktest` from "signed-out / Set key" to "Configured via stored API key / effective" via the `credentialsStore` subscription + debounced refetch. `crossclient-observer-updated.png` |

### Live-proof findings (honest, including nuances)
- **The headline cross-client fix works live** — proven on `credentialsStore` via `serf/auth/updated` (journey 7). The `extensions`/marketplace path shares the identical wiring.
- **Nuance — instance CRUD does not broadcast.** A first cross-client attempt used "make default"; the observer did **not** update. Root cause (verified in `app_rpc.go:512-545`): the `serf/instance/*` handlers (create/edit/remove/**setDefault**) broadcast nothing — only the `serf/auth/*` handlers (login/logout/apiKeySet/device-authorized) call `notifyAuthUpdated`. So the "no update" was *correct behavior for a non-broadcasting mutation*, not a bug; re-running with `apiKeySet` (which does broadcast) propagated correctly. **Reportable boundary:** adding/removing/editing an instance or changing the default in one tab does NOT live-update other tabs — only credential-source changes do. Same shape as the `launchConfigStore` boundary below; worth a follow-up if full instance-list liveness is wanted (the broadcast doesn't exist on the backend to subscribe to).
- **Isolation caveat — plugin/marketplace storage is global.** `cfg.PluginRoot` was unset, so `plugins.Manager` used the default `~/.config/serf/plugins` (honoring `XDG_CONFIG_HOME`), **not** the isolated `hub_state_root`. My journey-2 marketplace add + plugin install therefore wrote to the shared global store. **I cleaned it up** afterward via the CLI (`serf plugin remove superpowers-chrome@superpowers-marketplace`, `serf plugin marketplace remove sp-test`) and verified the global store is back to baseline (0 installed, 2 seeded default marketplaces). Not a W7 defect, but note the hub does not isolate the plugin root by `hub_state_root`.
- **Test-environment artifact — the browser wandered.** A second serf-hub (Jesse's own, on `:19281`, with a live session) was running, and the shared Chrome profile's persisted dockview layout referenced its sessions; on a full reload the SPA followed those stale session panes cross-origin to `:19281`. Clearing `serf.workspace.layout.v1` and driving via client-side nav kept the session pinned to `:19280`. This is browser-profile contamination, not a W7 defect — every W7 page rendered and functioned correctly on the isolated hub.
- No content-loss, crash, or security issue surfaced. The never-echo credential invariant held throughout (masked token in Overview; password-typed key field; "UI never displays stored values" copy).

---

## Decisions for Jesse

1. **Theme `prefers-color-scheme` listener — implemented; your veto is open.** Close-fix item 6 (`249943f94`) resolved the copy/code gap by *implementing* the OS-follow behavior (a lazily-installed `matchMedia("(prefers-color-scheme: dark)")` listener that live-updates the open tab while theme=system), rather than softening the copy. Isolated to `prefs.ts` + `theme.tsx`, no new CSS, TDD'd. Verified live (journey 5: system resolved to light on a light-OS). If you'd rather the copy had been softened instead, this is the one to veto.

2. **`credentialsStore` staleness wiring was added beyond the named roster.** The fix-round brief named "the marketplaces/plugins stores at minimum". The implementer also wired `credentialsStore` to `serf/auth/updated` (the same staleness bug applies to the instance list). This is the store journey 7 proved live. Flagged for your awareness — trivially revertable if the roster intentionally excluded it, but it's a real fix.

3. **`launchConfigStore` scope boundary — no cross-client live-update for §9/§11/§18.** The launch/project/inrepo sections hold only local component state (no cached store state), so a `serf/launch/updated` broadcast has nothing to invalidate; they refresh only on mount. Making them live-refresh on a same-cwd broadcast is a separate feature (the broadcast payload does carry `{cwd, layer}` now, so it's feasible) — deliberately out of this wave's scope. (Related, discovered live: instance CRUD also has no broadcast — see live-proof findings.)

4. **Gitleaks — clean, no change needed.** Exact CI-matching command run this cycle (fix-round item 7):
   ```
   go install github.com/zricethezav/gitleaks/v8@v8.30.1
   export PATH="$(go env GOPATH)/bin:$PATH"
   make secret-scan        # gitleaks detect --no-git --redact --config .gitleaks.toml --source <repo>
   make fuzz-corpus-scan   # same config over every testdata/fuzz + fuzz/corpus dir
   ```
   Both exit 0, "no leaks found" across the whole working tree. No allowlist changes, no fixture reshaping.

---

## Gates (this close)

Frontend (`cmd/serf-hub/frontend`), AND-chained, vitest run bare:
```
npx tsc --noEmit        → clean
npx vitest run          → 154 files / 2332 tests, all passed
npm run lint (biome ci) → clean (467 files)
npm run build           → ok (270 modules) → git restore dist/PLACEHOLDER → tree clean
```
Go (repo root; this branch carries T1a's overview method):
```
go build ./...              → ok
go test ./cmd/serf-hub/...  → ok (all packages; hub package 29.6s)
```
Note: the brief cited `go -C . test ./internal/hub/...`, but there is no `internal/hub` at the repo root — the hub is `cmd/serf-hub` (package main) + `cmd/serf-hub/internal/*`, and T1a's overview tests live in `cmd/serf-hub/app_rpc_settings_overview_test.go`. The correct target is `./cmd/serf-hub/...` (used above). Working tree clean at `7381dd78f`.

---

## Merge-time remainder — NOT done in this close (per plan T5)

These happen later, **serially after wave 5 lands**, and are explicitly out of this close's scope:
1. **W5-merge absorption.** Swap W5's interim local prefs hook (which reads/writes the same `serf.prefs.<name>` localStorage keys) to this wave's `stores/prefs.ts` store. Union-resolve the three controller-owned collision surfaces: `widgets/index.ts` barrel (export lines), the pane registry, and the route table (single-line appends).
2. **Merge to integration — serial: W5 first, then W7 absorbs, then W7 merges.**

No push and no merge were performed in this close.
