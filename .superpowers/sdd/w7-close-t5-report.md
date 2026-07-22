# W7 close (T5, pre-merge portion) — task status report

**Status: DONE_WITH_CONCERNS** (concerns are reportable findings for Jesse/M9-M10, not blockers or regressions.)

**Worktree:** `.../webui-w7-settings`, branch `w7-settings`. **No push, no merge.** Tree clean.
**Commit range (this close):** `11944e7a3..<HEAD>` — `7381dd78f` (item 0, comment fix) + the report/artifact commits below.

## What was done
- **Item 0** — corrected the two subscription-payload comments (`credentials.ts`, `extensions.ts`) that wrongly claimed empty wire payloads. Verified citations: `serf/auth/updated`→`{provider,activeSource}` (`app_rpc.go:764-767`), `serf/launch/updated`→`{cwd,layer}` (`:772-775`), marketplace/plugin genuinely empty (`:657,663`); generated types are `{}` for a codegen reason. Gated (tsc) + committed `7381dd78f`.
- **Item 1 — parity sweep**: both floor docs (`parity-m7-settings.md` 18 sections + Appendices A/B; settings-scoped `contracts-sidebar-search-settings.md`). Result: **no Critical/Major gaps; all 17 nav sections + per-project implemented; 5 Minor gaps; 18-entry divergence ledger.** High-stakes claims re-verified against source.
- **Item 2 — live proof**: real isolated hub (`:19280`, `SERF_HUB_WEB=new`, real `.env` keys) + Chrome. All 7 journeys pass (credential add/OAuth-device/default-switch, marketplace add/browse/install-confirm/disable, dir add+validate, launch-config edit→resolve, theme/density persist, overview real data, **cross-client staleness proven live**).
- **Item 3 — gates**: all green (below).
- **Item 4 — wave7-report**: `docs/superpowers/plans/wave7-report.md`.

## Gates (one line)
Frontend tsc clean · vitest **154 files / 2332 tests** pass · biome lint clean (467) · build ok + PLACEHOLDER restored; Go `go build ./...` ok · `go test ./cmd/serf-hub/...` ok. (Brief's `./internal/hub/...` path doesn't exist — hub is `./cmd/serf-hub/...`; corrected.)

## Concerns / findings for Jesse & M9/M10
- **Two in-code-only divergences the gate must consciously bless** (verified against source; would be Major reductions if unintended, both documented in code but NOT in SDD reports): (a) **model picker cut** — `modelPicker` is a plain `provider/model` text input, not the searchable catalog (`fields.tsx:1-11`); (b) **Sidebar-mode radio is inert** — persisted/rendered but no consumer applies it; real collapse is a separate `serf.rail.collapsed.v1` (`prefs.ts:45-50`, `Rail.tsx:49`).
- **5 Minor parity gaps**: §7a `/settings/providers` no redirect (→ placeholder); §13/§14 no dir count header; §12 Refresh/Enable/Disable/Auto-upgrade/Upgrade lack `withBusy` (double-fire); §12e no Installed status dot; §18 renders inside the nav shell.
- **Theme prefers-color-scheme listener — Jesse's veto is open** (implemented, not softened; `249943f94`).
- **`credentialsStore` staleness wiring added beyond the named roster** (auth/updated) — the store journey 7 proved live; revertable.
- **`launchConfigStore` scope boundary** — §9/§11/§18 have no cross-client live-update (no cached state).
- **Live-proof nuance**: instance CRUD (create/edit/remove/**setDefault**) broadcasts nothing (`app_rpc.go:512-545`) — only auth-source changes live-update other tabs. Same shape as the launchConfig boundary; a real backend gap if full instance-list liveness is wanted.
- **Isolation caveat**: plugin/marketplace storage is global (`~/.config/serf/plugins`, `cfg.PluginRoot=""`), not isolated by `hub_state_root`. Journey 2 wrote there; **cleaned up** via CLI (verified back to baseline: 0 installed, 2 default marketplaces).
- **Parity-doc defect**: `parity-m7-settings.md` §7h#2 is wrong (says device-poll doesn't stop on error; the real legacy `credentials.html:79-108` does stop, and the port matches — pinned by `a6339ceae`). Not a gap.
- **Gitleaks**: clean, no change (fix-round item 7; `make secret-scan` + `make fuzz-corpus-scan`, gitleaks v8.30.1, exit 0).
- **Test-env artifact (not a defect)**: a second hub (Jesse's, `:19281`, live session) + the shared Chrome dockview layout caused cross-origin wander on reload; pinned to `:19280` by clearing `serf.workspace.layout.v1` + client-side nav.

## Merge-time remainder — NOT done here (serial, after W5)
W5-merge absorption (swap W5's interim prefs hook to `stores/prefs.ts`; union-resolve `widgets/index.ts` barrel + pane registry + route table), then merge to integration (W5 first, then W7).
