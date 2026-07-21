# Web Rewrite Wave 7 — Settings (M7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Waves 1-4
> conventions apply verbatim (wave worktree + sub-streams with exclusive manifests, wave-local SDD
> artifacts, controller-owned chokepoints, honest exit-code gates — tsc BEFORE vitest with
> test-file-count verification — commits as separate invocations, Biome is the lint gate).

**Goal:** every settings surface — 16 nav sections + the per-project override + `/credentials` —
as native panes on the appwire/store architecture, replacing the server-rendered `/_partials/settings/*`
family the deletion wave removes.

**Parity floors:** `docs/web-ui/parity/parity-m7-settings.md` + the settings half of
`docs/web-ui/parity/contracts-sidebar-search-settings.md`. Section inventory verified against
`templates/partials/settings.html:13-31` (16 exact) + per-project + the credentials redirect stub.

**Prereqs:** integration with waves 1-4. Wave worktree `webui-w7-settings` off integration. Runs
CONCURRENTLY with Wave 5 (interaction): W7 must NOT touch `panes/session/**`, `protocol/reducer.ts`,
`stores/threads.ts`, or the `Textarea` widget (W5-owned this cycle). Shared collision surfaces and
their discipline: `widgets/index.ts` barrel = controller-applied export lines only; `stores/prefs.ts`
= W7-T4 CREATES it (W5 uses an interim local hook on the SAME localStorage keys — key contract:
`serf.prefs.<name>`, e.g. `serf.prefs.enterToSend`, `serf.prefs.showCost`; W5's hook swaps to the
store at W7's merge, a W7-close line item); pane registry + route table = single-line appends,
controller-merged. **Merges to integration are SERIAL: W5 first, W7 absorbs then merges.**

## Binding constraints (every task)

- **Credential never-echo is a hard invariant**: stored secrets are never displayed, bearer token
  only as a fixed mask; JSX auto-escaping everywhere — no raw-HTML "faithful" ports.
- **Destructive-action consistency (decided this wave):** every destructive action (remove
  instance/marketplace/plugin/dir-row/config-row, clear) confirms via the new ConfirmDialog —
  including the currently-unconfirmed row removals AND plugin Install (a code-execution surface
  that today confirms Remove but not Install — fixed per beyond-parity; reviewers scrutinize).
- **Single-mutable-editor invariant kept**: one open editor per section (section-level state, not
  per-row), matching the legacy contract; a second open replaces after the same discard semantics.
- OAuth/device-code flows: effect-scoped timers with cleanup + flowId staleness re-check per tick
  (the legacy module-level-timer shape does not port).
- Wire truth through stores; toasts (W5-T1's convention) for user-action failures; widgets only;
  tokens-only CSS; sentence case; Biome; TDD with wire-true shapes — confirm real RPC response
  shapes against the Go handlers before pinning fixtures (the floor doc's field lists are a
  reverse-engineered lower bound).

## Tasks

### T1a ∥ T1b (parallel prerequisites; zero file overlap)

- **T1a — `serf/settings/overview` (Go + wire).** One new ScopeHub catalog method returning the
  field bag six template-only sections need: hub/runtime (version, commit, listen addr, rundir,
  bearer-token age, past-index path/size/per-page/count), storage (state dir, index size, live
  session count), agent roster, codex launches, MCP discovered/probed rows (name/transport/live).
  One response struct, omitempty sub-objects, catalog entry + typed payload (generated TS types
  come free), appwiredoc/drift gates green, handler tests against real hub state. Additive-only;
  goes on the wave branch (precedent: wave 3's tree-push Go work).
- **T1b — settings pane shell + shared form widgets (frontend).** `panes/settings/` shell: left
  nav reproducing the exact 4-cluster structure, section routing (`/settings`, `/settings/:section`,
  `/credentials` as a top-level alias resolving to the singleton settings pane pre-focused on
  credentials — NOT a second pane type), filter + mobile-back behaviors per `test-settings-shell.js`
  contracts; `registerPane({id:"settings", singleton:true})`. Plus the five shared widgets every
  stream needs, as NEW widget dirs (zero W5 overlap): `FormRow`/`Field` layout primitive,
  `CollectionEditor` (add-row/remove/validate-on-add — the most duplicated legacy pattern),
  `PathPicker` (browse-without-firing-until-Accept per `test-settings-dir-picker.js`; Wave 6's
  spawn dialog reuses it), `ConfirmDialog`, `RadioGroup` (visible options — Select doesn't cover).
  Exports reported to the controller for the barrel.

### T2 ∥ T3 ∥ T4 (streams off the wave branch after T1a+T1b)

- **T2 — Agents & models cluster** (`panes/settings/sections/{credentials/**,agents,launchShared/**,launchServer,launchCodex,inrepo,project}`):
  Credentials (#7 — the dominant piece: instance CRUD, API-key set, OAuth browser + device-code
  dual flow, default-instance; the shipped PRI-1880 RPCs are battle-tested — UI-only work),
  Agents roster (#8, overview-fed), Serf launch (#9 — ports the schema-driven LaunchConfigControls
  engine, ~90 behaviors, Appendix B), Codex launch (#10, overview-fed), In-repo trust (#11),
  Per-project override (#18 — shares #9's engine, 3-state loaded contract).
- **T3 — Extensions cluster** (`panes/settings/sections/{marketplacesPlugins/**,dirListSetting,pluginsDirs,skillsDirs,mcp}`):
  Marketplaces & Plugins (#12 — second-dominant; browse/filter, install/upgrade/remove/enable/
  disable/auto-upgrade; server-side RCE-hardening exists — do not weaken pre-install visibility,
  ADD the Install confirm), the parameterized `DirListSetting` used twice (#13 plugins dirs,
  #14 skills dirs — byte-identical twin per the floor doc's own recommendation), MCP (#15 —
  editable half on the DirListSetting family, probed half on T1a's overview).
- **T4 — Ungrouped + Daemon** (`panes/settings/sections/{general,theme,transcript,display,notifications,hub,storage}` + `src/stores/prefs.ts`):
  the seven simple sections — General/Hub/Storage from T1a's overview; Theme/Transcript/Display/
  Notifications on the NEW `prefs.ts` store (localStorage-backed, the `serf.prefs.*` key contract
  above; resolve the notifications default-value discrepancy the floor doc flags — copy says
  title/favicon ON, code says all OFF — pick the code's behavior and note it); browser
  Notification-API permission flow.

### T5: wave close
Parity sweep (both floors, all 18 surfaces), live proof (real hub: credential add→OAuth device
flow→default switch; marketplace add→browse→install-with-confirm→disable; dir add with validate;
launch-config edit→resolve; theme/density flips persisting; overview sections showing real daemon
data), the W5-merge absorption (swap W5's interim prefs hook to the store; barrel/registry/route
union-resolves), full gates + `go build`/`go test` (T1a), wave7-report, merge to integration
(AFTER W5 — serial).
