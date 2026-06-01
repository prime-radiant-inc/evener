# M1 — App-side `internal/` de-mix (execution-ready plan)

Date: 2026-06-01
Spec: `docs/superpowers/specs/2026-06-01-go-monorepo-module-architecture.md` (§7 M1)
Module path: `primeradiant.com/serf` (single root module today; M1 is pre-`go.work`)
Scope: **pure relocation + import rewrites. No logic changes.** Gate after every step: `go build ./... && go vet ./...` (and a final `go test ./...`).

> Evidence rule: every classification below cites the authoritative importer grep
> `grep -rln --include='*.go' "\"primeradiant.com/serf/internal/<pkg>" . | grep -v worktrees`
> (full quoted import path; this excludes the string-literal/comment matches in
> `cmd/serf-namingcheck/main.go`, which are **not** Go imports — see §0 caveat).
> All greps exclude `.claude/worktrees/`.

---

## 0. Findings up front (read before executing)

1. **The spec §4 annotation is contradicted by the import graph.** §4's layout comment says
   "serf-hub ← appprojector, apptranscript, appserver … sink here." The actual importers show:
   - `appprojector` is imported **only by `server/`** (the engine's per-session HTTP/AppWire
     server) — **never by serf-hub**.
   - `appserver` is imported by `server/` **and** serf-hub **and** serf-tui (3 products).
   - `apptranscript` is imported by `server/` **and** serf-hub (2 products).
   `server/` is a **top-level engine package** (`package server`, imported by `cmd/serf/serve.go`
   and `cmdutil/cmdutil.go`). It is part of the **engine product** but it does **not** live under
   `cmd/serf/`. Therefore these three packages **cannot** sink into `cmd/serf-hub/internal/` — they
   are genuine **MULTI-IMPORTER** judgment calls (see §5). M1 must **leave them as shared app-side
   `internal/`** for now; relocating `server/` itself into the engine is M4 work, not M1.

2. **`auth` is imported by the `llm` library in NON-TEST code.**
   `llm/providers/openai/adapter.go:19` → `authopenai "primeradiant.com/serf/internal/auth/openai"`.
   `llm` is the lowest-layer published library. `auth` is therefore **not app-side at all** and
   **must not** sink into any `cmd/<bin>/internal/`. This is a layering fact stronger than §6's
   "duplicate" note (which only covered frontmatter/diagnostic). **Leave `auth` in place for M1**;
   its real home (own `internal/` under `llm`, or a shared util module) is decided in M2/M3 when
   `llm`/`agent` carve. Flagged in §5.

3. **`cmd/serf-namingcheck/main.go` is a false-positive importer.** It contains a hardcoded
   string-path list (`"internal/appwire/"`, `"internal/appsource/"`, `"internal/appserver/"`,
   `"internal/appprojector/"`, `"internal/launchconfig/"`) and prose comments — **not Go imports**.
   It does not appear in any authoritative full-path grep. But those string paths **will break the
   linter** if the dirs move. For every package this plan **moves**, the corresponding entry in
   `cmd/serf-namingcheck/main.go` (lines 82–95) must be updated to the new path. Of the moved
   packages only `appsource` and `launchconfig` are in that list (appwire/appserver/appprojector do
   **not** move in M1). `cmd/serf-internalcheck/` references no internal/ paths.

4. **Only 4 of the 12 packages actually relocate in M1:** `appsource` → serf-hub, `binresolve`
   (judgment call → serf-hub, see §5), `credentials` (judgment call → serf-hub, see §5),
   `launchconfig` (→ serf-hub, after cutting the agent-test smell). Two are **promoted** (`appwire`,
   `hubapi` → top-level contracts). The remaining six **stay put in M1** because they're
   multi-importer / cross-layer (`appprojector`, `appserver`, `apptranscript`, `auth`, `diagnostic`,
   `httpguard`) — each with a documented reason and a deferred target.

5. **Agent-test layering smell (spec §6, must cut before modules compile — but M1-optional):**
   `agent/session_fallback_test.go` imports `internal/appwire` **and** `internal/launchconfig`
   (test-only; confirmed the only agent file touching either). This blocks M3 (agent can't import
   the app module). It does **not** block M1's relocations, but `launchconfig` can't fully "belong
   to serf-hub" while an agent test imports it. Recommendation: cut this test's app-package
   dependency as **step 0 of M1** (rewrite against agent primitives) so `launchconfig` becomes
   cleanly serf-hub-owned. See §4 step 0 and §6.

Baseline `go build ./...` is **green** on `main` at plan time.

---

## 1. Ownership table

Classifications: **CONTRACT** (promote to top-level) · **OWNED-BY-ONE-BINARY** (sink to
`cmd/<bin>/internal/`) · **MULTI-IMPORTER** (2+ products or a shared top-level pkg — judgment call,
see §5) · **CROSS-LAYER** (imported by a library module — `llm`/`agent` — non-test).

Importer products are derived by mapping each importer file to its owner:
`cmd/serf-hub/*`→serf-hub · `cmd/serf-tui/*`→serf-tui · `cmd/serf/*`→serf(engine) ·
`server/*`→engine (server pkg) · `cmdutil/*`→**shared** (all 3 bins import cmdutil) ·
`agent/*`→agent lib · `llm/*`→llm lib · `internal/<x>/*`→another app-side internal pkg.
(Self-imports within the same package are omitted from "products".)

| # | Package | Importer set (file → product) | Classification | Target path (M1) |
|---|---------|-------------------------------|----------------|------------------|
| 1 | **appwire** (+ `appwire/appwiretest`) | 122 files across **agent**(test: `session_fallback_test.go`), **serf-hub**(×39), **serf-tui**(×43), **serf(engine)**(×4: `internal/launchcheck/*`, `launch_check_dispatch_test.go`, `serve.go`), **engine `server/`**(×7), and app-internal selves (appprojector, appserver, appsource, apptranscript, launchconfig). All 3 binaries + engine + a lib test. | **CONTRACT** | `appwire/` (+ `appwire/appwiretest/`) — top-level |
| 2 | **hubapi** | **serf-hub**(×7: `web_api*.go`, `web_session.go`, `web_spawn.go`, `web_types.go`, `web_workspace.go`, `web_test.go`), **serf-tui**(×1: `internal/hubstart/hub_start.go`), self(×2 test). Hub serves it, tui consumes it. | **CONTRACT** | `hubapi/` — top-level |
| 3 | **appsource** | **serf-hub ONLY** (×14: `app_compact.go`, `app_models.go`, `app_rpc{,_test}.go`, `app_sources.go`, `app_threadlifecycle.go`, `app_threadlist.go`, `app_transcripts.go`, `codex_launch_real_test.go`, `config.go`, `image_attachments_test.go`, `internal/codexlaunch/codex_launch.go`, `web{,_test}.go`). No engine/tui/lib importer. | **OWNED-BY-ONE-BINARY** | `cmd/serf-hub/internal/appsource/` |
| 4 | **launchconfig** | **serf-hub**(×7: `app_launch{,_test}.go`, `app_threadlifecycle.go`, `e2e_test.go`, `internal/claudeplugins/claude_plugins.go`, `spawn{,_test}.go`), **agent**(test-only: `session_fallback_test.go`). Self-imported by appwire? No. | **OWNED-BY-ONE-BINARY** (after cutting agent-test smell) | `cmd/serf-hub/internal/launchconfig/` |
| 5 | **binresolve** | **serf-hub**(×1: `main.go`), **serf-tui**(×1: `internal/hubstart/hub_start.go`). 2 binaries. | **MULTI-IMPORTER** → resolved to serf-hub (§5) | `cmd/serf-hub/internal/binresolve/` |
| 6 | **credentials** | **serf-hub**(×8: `app_auth{,_test}.go`, `app_auth_instance_test.go`, `app_instances_test.go`, `main.go`, `spawn{,_test}.go`, `web.go`), **shared cmdutil**(×1: `load_client.go`). cmdutil is imported by all 3 bins. | **MULTI-IMPORTER** (shared via cmdutil) → resolved to serf-hub (§5) | `cmd/serf-hub/internal/credentials/` |
| 7 | **appprojector** | **engine `server/` ONLY** (×2: `appwire_runtime.go`, `server.go`). Self-imports apptranscript+diagnostic+appwire. **NOT** imported by serf-hub (contra §4 annotation). | **MULTI-IMPORTER** (sole importer is top-level engine `server/`, not under `cmd/serf/`) | **STAY** `internal/appprojector/` in M1; → engine in M4 (§5) |
| 8 | **appserver** | **engine `server/`**(×3: `appwire_runtime.go`, `appwire_turns.go`, `server.go`), **serf-hub**(×6: `app_rpc{,_test}.go`, `codex_launch_test.go`, `image_attachments_test.go`, `web{,_test}.go`), **serf-tui**(×7, all `*_test.go`: `hub_agents_test.go`, `hub_appwire_test.go`, `hub_auth_test.go`, `hub_composer_test.go`, `hub_model_test.go`, `hub_send_attachments_test.go`, `tmux_e2e_test.go`), self(appsource tests). **3 products.** | **MULTI-IMPORTER** | **STAY** `internal/appserver/` in M1 (§5) |
| 9 | **apptranscript** | **engine `server/`**(×1: `appwire_turns.go`), **serf-hub**(×1: `app_threadread.go`), self(appprojector). 2 products. | **MULTI-IMPORTER** | **STAY** `internal/apptranscript/` in M1 (§5) |
| 10 | **auth** (only subpkgs `auth/openai`, `auth/openai/oaitest`; no root `.go`) | **llm**(NON-TEST: `providers/openai/adapter.go`; +test `adapter_test.go`), **agent**(test: `provider_instance_1b_integration_test.go`), **serf-hub**(×8), **serf-tui**(×1: `auth.go`), **serf(engine)**(×6: `internal/launchcheck/launchcheck_test.go`, `main_test.go`, `openai_login.go`, `openai_logout.go`, `openai_status.go`, `run_test.go`), **shared cmdutil**(test: `materialize_test.go`), self. | **CROSS-LAYER** (imported by `llm` lib, non-test) + multi-importer | **STAY** `internal/auth/` in M1; home decided in M2/M3 (§5) |
| 11 | **diagnostic** | **agent**(NON-TEST: `diagnostics.go`), **serf-hub**(×3: `app_rpc_test.go`, `web_api.go`, `web_spawn.go`), **serf(engine)**(×1: `internal/launchcheck/launchcheck.go`), **engine `server/`**(test: `appwire_server_test.go`), self(appprojector, apptranscript). | **CROSS-LAYER** — spec §6 **DECIDED: duplicate** | **STAY** `internal/diagnostic/` in M1; duplicate per product in M3 (§5) |
| 12 | **httpguard** | **engine `server/` ONLY** (×1: `server.go`). | OWNED-BY-ENGINE, but sole importer is top-level `server/` (not under `cmd/serf/`) | **STAY** `internal/httpguard/` in M1; → engine in M4 (§5) |

**Authoritative real-import file counts** (sanity): appprojector 2, appserver 18, appsource 14,
apptranscript 3, appwire 122, auth 21, binresolve 2, credentials 9, diagnostic 8, httpguard 1,
hubapi 10, launchconfig 8. Total distinct importer files across all 12: **144**.

---

## 2. `git mv` move-list (only the 6 packages that move in M1)

> Use `git mv` on the **directory** so history is preserved. After each move, run the
> corresponding §3 import rewrites, then the §4 gate. Do **not** create the target `internal/`
> dir manually — `git mv <dir> <newparent>/<dir>` moves the whole tree.

**Promotions (CONTRACT → top-level):**

```
git mv internal/appwire appwire        # moves appwire/ AND appwire/appwiretest/ subtree
git mv internal/hubapi  hubapi
```

**Sink to owning binary (serf-hub):**

```
git mv internal/appsource     cmd/serf-hub/internal/appsource
git mv internal/launchconfig  cmd/serf-hub/internal/launchconfig
git mv internal/binresolve    cmd/serf-hub/internal/binresolve
git mv internal/credentials   cmd/serf-hub/internal/credentials
```

`cmd/serf-hub/internal/` already exists (it holds `codexlaunch`, `claudeplugins`, etc.), so these
land beside existing hub-private packages.

**NOT moved in M1** (stay at `internal/<pkg>`, with deferred targets — see §5):
`appprojector`, `appserver`, `apptranscript`, `auth`, `diagnostic`, `httpguard`.

---

## 3. Import-rewrite map

Each rewrite is `OLD → NEW` applied to the **exact file list** below (from the authoritative greps).
Mechanical command per package (run from repo root; `gofmt` keeps imports sorted):

```
# template — substitute OLD/NEW; -l lists, then apply:
gofmt -w -r '"OLD" -> "NEW"' <files…>
# OR, scoped sed (verify with grep first):
grep -rl --include='*.go' '"OLD"' . | grep -v worktrees | xargs sed -i '' 's#"OLD"#"NEW"#g'
```

> `gofmt -r` rewrites only matching string-literal import paths; it will **not** touch the
> string-literal path list in `cmd/serf-namingcheck/main.go` (those are bare `"internal/appsource/"`
> without the module prefix) — update those by hand (see §0.3 / §4 step 6).

### 3.1 appwire → appwire (CONTRACT promotion)

Two distinct import paths move:

- `primeradiant.com/serf/internal/appwire` → `primeradiant.com/serf/appwire`
- `primeradiant.com/serf/internal/appwire/appwiretest` → `primeradiant.com/serf/appwire/appwiretest`

Files needing rewrite (**122** real-import files — full set):

*agent (lib test):* `agent/session_fallback_test.go`

*serf-hub (39):* `cmd/serf-hub/app_auth_instance_test.go`, `app_auth_test.go`, `app_auth.go`,
`app_compact.go`, `app_instances_test.go`, `app_instances.go`, `app_launch_test.go`,
`app_launch.go`, `app_models.go`, `app_paths.go`, `app_rpc_test.go`, `app_rpc.go`,
`app_sources.go`, `app_threadlifecycle.go`, `app_threadlist.go`, `app_threadread.go`,
`app_transcripts_test.go`, `app_transcripts.go`, `appwire_validation_test.go`,
`appwire_validation.go`, `codex_launch_real_test.go`, `codex_launch_test.go`,
`image_attachments_test.go`, `internal/codexlaunch/codex_launch.go`, `roster_test.go`, `roster.go`,
`session_order.go`, `spawn.go`, `tree_test.go`, `tree.go`, `web_api_tree.go`, `web_api.go`,
`web_session.go`, `web_settings.go`, `web_spawn.go`, `web_test.go`, `web_types.go`,
`web_workspace.go`, `web.go` (all under `cmd/serf-hub/`)

*serf-tui (43):* `cmd/serf-tui/command_palette.go`, `credentials_panel_test.go`,
`credentials_panel.go`, `details_drawer.go`, `hub_agents_test.go`, `hub_appwire_test.go`,
`hub_auth_test.go`, `hub_auth.go`, `hub_browse.go`, `hub_commands.go`, `hub_composer_test.go`,
`hub_dashboard.go`, `hub_model_test.go`, `hub_model.go`, `hub_notice_test.go`,
`hub_notifications.go`, `hub_send_attachments_test.go`, `hub_session_keys.go`, `hub_spawn.go`,
`hub_status_test.go`, `hub_status.go`, `hub_transcript_reducer_test.go`,
`hub_transcript_reducer.go`, `hub_transcript_widgets_test.go`, `hub_transcripts.go`,
`hub_types.go`, `hub_update.go`, `internal/hubdiagnostics/hubdiagnostics_test.go`,
`internal/hubdiagnostics/hubdiagnostics.go`, `internal/hubstart/hub_start_test.go`,
`internal/hubstart/hub_start.go`, `internal/pending/pending.go`,
`launch_overrides_modal_test.go`, `launch_overrides_modal.go`, `launch_schema_test.go`,
`launch_schema.go`, `launch_settings_panel_test.go`, `launch_settings_panel.go`,
`launchconfig_client.go`, `notice_panel.go`, `pending_test.go`, `queue_send.go`,
`tmux_e2e_test.go` (all under `cmd/serf-tui/`)

*serf engine (4):* `cmd/serf/internal/launchcheck/launchcheck_test.go`,
`cmd/serf/internal/launchcheck/launchcheck.go`, `cmd/serf/launch_check_dispatch_test.go`,
`cmd/serf/serve.go`

*engine server/ (7):* `server/appwire_runtime.go`, `server/appwire_server_test.go`,
`server/appwire_turns.go`, `server/bridge_test.go`, `server/image_attachments_test.go`,
`server/server_handlers.go`, `server/server.go`

*app-internal selves (within not-yet-moved packages — rewrite to the new top-level path):*
`internal/appprojector/appwire_projection_test.go`, `internal/appprojector/appwire_projection.go`,
`internal/appserver/notifier_test.go`, `internal/appserver/notifier.go`,
`internal/appserver/router_test.go`, `internal/appserver/router.go`,
`internal/appserver/server_test.go`, `internal/appserver/server.go`,
`internal/appserver/websocket_send_test.go`, `internal/appserver/websocket_test.go`,
`internal/appserver/websocket.go`, `internal/appsource/codex_input.go`,
`internal/appsource/codex_live_thread.go`, `internal/appsource/codex_mapping.go`,
`internal/appsource/codex_source_test.go`, `internal/appsource/codex_source.go`,
`internal/appsource/local_daemon_test.go`, `internal/appsource/local_daemon.go`,
`internal/appsource/registry_test.go`, `internal/appsource/registry.go`,
`internal/appsource/source.go`, `internal/apptranscript/apptranscript_test.go`,
`internal/apptranscript/apptranscript.go`, `internal/launchconfig/wire_test.go`,
`internal/launchconfig/wire.go`

*appwire's own subtree (self-references — the `appwiretest` import + parent import):*
`internal/appwire/optimistic_test.go` (imports both `…/appwire` and `…/appwire/appwiretest`),
`internal/appwire/appwiretest/scripted_transport.go` (imports `…/appwire`),
`internal/appwire/appwiretest/scripted_transport_test.go`
— **Note:** after `git mv internal/appwire appwire`, these files live at `appwire/…`; rewrite their
own import strings there.

> NOTE on `appsource`/`launchconfig` self-files: those packages also **move to serf-hub** in this
> phase. Their internal appwire-import rewrite (to top-level `appwire`) is the same regardless of
> where they land; sequence appwire's promotion (step 2) so it is already top-level when you rewrite
> them, or rewrite appwire-refs in the same pass.

### 3.2 hubapi → hubapi (CONTRACT promotion)

- `primeradiant.com/serf/internal/hubapi` → `primeradiant.com/serf/hubapi`

Files (**10**): `cmd/serf-hub/web_api_tree.go`, `cmd/serf-hub/web_api.go`,
`cmd/serf-hub/web_session.go`, `cmd/serf-hub/web_spawn.go`, `cmd/serf-hub/web_test.go`,
`cmd/serf-hub/web_types.go`, `cmd/serf-hub/web_workspace.go`,
`cmd/serf-tui/internal/hubstart/hub_start.go`, plus self: `internal/hubapi/client_test.go`,
`internal/hubapi/refs_test.go` (→ live at `hubapi/…` after the move).

### 3.3 appsource → cmd/serf-hub/internal/appsource (sink)

- `primeradiant.com/serf/internal/appsource` → `primeradiant.com/serf/cmd/serf-hub/internal/appsource`

Files (**14**): `cmd/serf-hub/app_compact.go`, `app_models.go`, `app_rpc_test.go`, `app_rpc.go`,
`app_sources.go`, `app_threadlifecycle.go`, `app_threadlist.go`, `app_transcripts.go`,
`codex_launch_real_test.go`, `config.go`, `image_attachments_test.go`,
`internal/codexlaunch/codex_launch.go`, `web_test.go`, `web.go` (all under `cmd/serf-hub/`).
All importers are already inside `cmd/serf-hub/` → the `cmd/serf-hub/internal/` sink is import-legal.

### 3.4 launchconfig → cmd/serf-hub/internal/launchconfig (sink)

- `primeradiant.com/serf/internal/launchconfig` → `primeradiant.com/serf/cmd/serf-hub/internal/launchconfig`

Files (**7**, after step 0 removes the agent-test importer): `cmd/serf-hub/app_launch_test.go`,
`app_launch.go`, `app_threadlifecycle.go`, `e2e_test.go`, `internal/claudeplugins/claude_plugins.go`,
`spawn_test.go`, `spawn.go` (all under `cmd/serf-hub/`).
**Blocker:** `agent/session_fallback_test.go` also imports launchconfig today. After the §4 step-0
cut, no non-hub importer remains and the `cmd/serf-hub/internal/` sink is legal. **If step 0 is
deferred, do NOT move launchconfig** (a `cmd/serf-hub/internal/` package can't be imported by an
agent test) — leave it at `internal/launchconfig/` until M3.

### 3.5 binresolve → cmd/serf-hub/internal/binresolve (sink; see §5 resolution)

- `primeradiant.com/serf/internal/binresolve` → `primeradiant.com/serf/cmd/serf-hub/internal/binresolve`

Files (**2**): `cmd/serf-hub/main.go`, `cmd/serf-tui/internal/hubstart/hub_start.go`.
**Cross-binary importer:** `cmd/serf-tui/internal/hubstart/hub_start.go` would lose access (a
serf-hub-private package can't be imported by serf-tui). **See §5 for the required duplication** —
this rewrite line applies to the **hub** copy; the **tui** importer points at a duplicated tui copy.

### 3.6 credentials → cmd/serf-hub/internal/credentials (sink; see §5 resolution)

- `primeradiant.com/serf/internal/credentials` → `primeradiant.com/serf/cmd/serf-hub/internal/credentials`

Files (**9**): `cmd/serf-hub/app_auth_instance_test.go`, `app_auth_test.go`, `app_auth.go`,
`app_instances_test.go`, `main.go`, `spawn_test.go`, `spawn.go`, `web.go` (8 under `cmd/serf-hub/`),
plus **`cmdutil/load_client.go`** (shared). **Cross-layer importer:** `cmdutil` is imported by all
three binaries, so a serf-hub-private credentials breaks `cmdutil`. **See §5 for resolution.**

### 3.7 namingcheck hardcoded path-list fixups (NOT Go imports — manual)

In `cmd/serf-namingcheck/main.go` (lines 82–95), update the string-literal scan paths for the
**moved** dirs only:

- `"internal/appsource/"` → `"cmd/serf-hub/internal/appsource/"`
- `"internal/launchconfig/"` → `"cmd/serf-hub/internal/launchconfig/"`
- `"internal/appwire/"` → `"appwire/"`

Leave `"internal/appserver/"` and `"internal/appprojector/"` unchanged (those packages do not move
in M1). Also update the prose comments referencing these dirs (lines ~82–86) for accuracy.

---

## 4. Safe execution order (leaf-first; gate `go build ./... && go vet ./...` after EACH step)

Order is chosen so each step compiles independently and touches the fewest cross-cutting files
first. Promotions go first (they only widen visibility — strictly safe), then sinks. Run the gate
**after every numbered step**; run `go test ./...` after steps 0, 1, 2, and at the end.

**Step 0 — Cut the agent-test layering smell (prerequisite for moving launchconfig).**
Rewrite `agent/session_fallback_test.go` to stop importing `internal/appwire` and
`internal/launchconfig` (reconstruct the fixture from agent primitives). No production code changes.
Gate: `go build ./... && go vet ./... && go test ./agent/...`.
*(If Jesse prefers to defer this to M3, skip launchconfig in M1 — see §3.4 blocker.)*

**Step 1 — Promote `hubapi` → top-level `hubapi/`.** (Smallest contract: 10 files, hub+tui only.)
`git mv internal/hubapi hubapi`; apply §3.2 rewrites; gate.

**Step 2 — Promote `appwire` → top-level `appwire/`.** (Largest blast radius: 122 files, but a pure
visibility-widening move — every old importer can still see the new top-level package.)
`git mv internal/appwire appwire`; apply §3.1 rewrites (incl. the `appwiretest` sub-path and all
self/internal-pkg references); gate + `go test ./...`.

**Step 3 — Sink `appsource` → `cmd/serf-hub/internal/appsource`.** (14 files, all already in
`cmd/serf-hub/`; clean single-owner.) `git mv internal/appsource cmd/serf-hub/internal/appsource`;
apply §3.3 rewrites; gate.

**Step 4 — Sink `launchconfig` → `cmd/serf-hub/internal/launchconfig`.** (Requires step 0 done.)
`git mv internal/launchconfig cmd/serf-hub/internal/launchconfig`; apply §3.4 rewrites; gate.

**Step 5 — Resolve + sink `binresolve`** per §5 (duplicate the tiny pkg into both
`cmd/serf-hub/internal/binresolve` and `cmd/serf-tui/internal/binresolve`, OR — preferred if Jesse
approves — leave it as a shared app-side `internal/binresolve` until M4). If duplicating:
`git mv internal/binresolve cmd/serf-hub/internal/binresolve`, then `git mv`/copy a second tree into
`cmd/serf-tui/internal/binresolve`, apply §3.5 rewrites (hub importer → hub copy, tui importer → tui
copy); gate.

**Step 6 — Resolve + sink `credentials`** per §5. Because `cmdutil/load_client.go` (shared) imports
it, the clean M1 option is to **leave `credentials` at top-level `internal/credentials`** until
`cmdutil` is dissolved (M4), OR duplicate (hub copy under `cmd/serf-hub/internal/credentials` +
move the `cmdutil` usage's needs). **Recommended for M1: leave in place** (see §5). If leaving,
skip the move; no rewrites.

**Step 7 — namingcheck fixups** (§3.7) for whatever moved. Gate: build the linter
(`go build ./cmd/serf-namingcheck/...`) and, if it has a self-check mode, run it.

**Step 8 — Final full gate:** `go build ./... && go vet ./... && go test ./...`. Confirm
`internal/` now contains only the packages §2 left in place (`appprojector`, `appserver`,
`apptranscript`, `auth`, `diagnostic`, `httpguard`, and — per step 6 decision — possibly
`credentials`, and per step 5, possibly `binresolve`).

> Promotions before sinks is deliberate: a top-level promotion can never break an importer (it only
> moves a package "up"), so steps 1–2 are zero-risk ordering-wise even though appwire is huge.
> Sinks (steps 3–6) are the ones that can fail to compile if an out-of-binary importer remains —
> which is exactly why the §5 multi-importer packages are excluded from sinking.

---

## 5. MULTI-IMPORTER flags (the judgment calls) + recommended resolution

Per the spec placement rule (§3): a package imported by 2+ binaries, or by a top-level shared
package (`server/`, `cmdutil/`), or by a library module (`llm`/`agent`), **cannot** live in a single
`cmd/<bin>/internal/`. Each below lists its importer set and the recommended M1 disposition.

### 5.1 `appserver` — imported by **3 products** (engine `server/`, serf-hub, serf-tui)
Importers: `server/{appwire_runtime,appwire_turns,server}.go` (engine) · `cmd/serf-hub/{app_rpc,
app_rpc_test,codex_launch_test,image_attachments_test,web,web_test}.go` · `cmd/serf-tui/*_test.go`
(×7, test-only) · self `internal/appsource/*_test.go`.
**Resolution: LEAVE as top-level `internal/appserver/` in M1.** It is genuinely shared between the
engine and the hub (server-side AppWire impl the engine serves and the hub re-uses). Its true home
is the **engine product** once `server/` itself relocates under the engine (M4); the hub/tui usages
are then either (a) re-pointed at the engine's exported surface or (b) recognized as a second
contract. **Do not sink into `cmd/serf-hub/internal/` — that would break `server/` (engine).**
Flag for M4 decision: "is appserver part of the AppWire contract, or engine-private re-used by hub?"

### 5.2 `apptranscript` — imported by **2 products** (engine `server/`, serf-hub)
Importers: `server/appwire_turns.go` (engine) · `cmd/serf-hub/app_threadread.go` · self
`internal/appprojector/appwire_projection.go`.
**Resolution: LEAVE as top-level `internal/apptranscript/` in M1.** Same shape as appserver: shared
engine↔hub. Sinking to serf-hub breaks the engine `server/` importer. Defer to M4 with `server/`.

### 5.3 `appprojector` — imported by **engine `server/` only** (but `server/` is top-level)
Importers: `server/{appwire_runtime,server}.go` only. **Not** imported by serf-hub (the §4
annotation is wrong). Self-imports apptranscript, diagnostic, appwire.
**Resolution: LEAVE as top-level `internal/appprojector/` in M1.** It is engine-owned, but its only
importer is the top-level `server/` package, which M1 does not move. It will travel **with `server/`
into the engine** in M4. Sinking it to `cmd/serf/internal/` now would break `server/` (which is not
under `cmd/serf/`). Pure single-owner, just not yet relocatable.

### 5.4 `httpguard` — imported by **engine `server/` only**
Importer: `server/server.go` only.
**Resolution: LEAVE as top-level `internal/httpguard/` in M1.** Identical situation to appprojector
(sole importer is top-level `server/`). Travels with `server/` into the engine in M4.

### 5.5 `auth` — **CROSS-LAYER: imported by the `llm` library (non-test)** + 3 binaries + shared cmdutil
Importers: **`llm/providers/openai/adapter.go` (NON-TEST)** + `llm/providers/openai/adapter_test.go`
· `agent/provider_instance_1b_integration_test.go` (test) · `cmd/serf-hub/*` (×8) ·
`cmd/serf-tui/auth.go` · `cmd/serf/{internal/launchcheck/launchcheck_test,main_test,openai_login,
openai_logout,openai_status,run_test}.go` · `cmdutil/materialize_test.go` (shared). Package is
only `internal/auth/openai` (+ `oaitest`); no root `.go`.
**Resolution: LEAVE as top-level `internal/auth/` in M1.** This is the **hardest** placement: `llm`
is the lowest-layer published library and depends on `auth/openai` in production code, so `auth`
**cannot** be app-side. Its real home is decided when `llm` carves (**M2**): either `auth/openai`
moves **into `llm`** (e.g. `llm/internal/authopenai` or a public `llm/auth` surface) so the engine/
hub/tui reach it through `llm`, or it becomes its own tiny shared module. **Out of M1 scope** beyond
flagging. Do not move in M1.

### 5.6 `diagnostic` — **CROSS-LAYER (spec §6 DECIDED: duplicate)**
Importers: **`agent/diagnostics.go` (NON-TEST)** · `cmd/serf-hub/{app_rpc_test,web_api,web_spawn}.go`
· `cmd/serf/internal/launchcheck/launchcheck.go` · `server/appwire_server_test.go` (engine, test) ·
self appprojector+apptranscript.
**Resolution: LEAVE as top-level `internal/diagnostic/` in M1; duplicate per §6 in M3.** §6 already
decided: agent keeps `agent/internal/diagnostic`; each app product gets its own copy. That split
happens when agent carves (**M3**), not in M1 (M1 has no agent module yet to duplicate into). Do not
move in M1.

### 5.7 `binresolve` — **2 binaries** (serf-hub, serf-tui)
Importers: `cmd/serf-hub/main.go` · `cmd/serf-tui/internal/hubstart/hub_start.go`. Tiny utility
(resolves a sibling binary path).
**Resolution (recommend): DUPLICATE** per the spec's standing "duplicate small cross-cutting utils"
decision (§6) — `cmd/serf-hub/internal/binresolve` + `cmd/serf-tui/internal/binresolve`. It's a
trivial pure-function package; duplication keeps both binaries self-contained with no app-side
shared `internal/`. **Alternative (if Jesse prefers no duplication):** leave at top-level
`internal/binresolve/` until both binaries' homes settle (M4). **Either is M1-safe**; my
recommendation is duplicate (matches the §6 precedent and removes a shared-internal). **Needs
Jesse's nod** since duplication is a (small) logic-shape decision.

### 5.8 `credentials` — **shared via `cmdutil`** (serf-hub + shared cmdutil)
Importers: `cmd/serf-hub/*` (×8) · **`cmdutil/load_client.go`** (shared — `cmdutil` is imported by
serf, serf-hub, **and** llmcall). The `cmdutil` usage (`credentials.LoadStore`) means credentials is
effectively reachable by all three binaries.
**Resolution (recommend): LEAVE as top-level `internal/credentials/` in M1.** Because `cmdutil` is
the cross-cutting glue (dissolved in M4), sinking credentials to `cmd/serf-hub/internal/` now would
break `cmdutil/load_client.go`. Cleanest M1 move is to **not move it** and let it settle when
`cmdutil` is decomposed (M4) — at which point the `LoadStore` caller goes wherever cmdutil's
client-loading lands. **Alternative:** duplicate (hub copy + a copy for cmdutil's consumer), but
that fragments the credential store format across copies — **not recommended** for a
serialization-format-bearing package. **Needs Jesse's decision**; default = leave in place.

#### Summary of judgment-call packages (the only non-mechanical decisions)

| Package | Why it's a judgment call | M1 disposition | Deferred to |
|---------|--------------------------|----------------|-------------|
| `appserver` | imported by engine `server/` + serf-hub + serf-tui (3) | **stay** top-level | M4 (with `server/`) |
| `apptranscript` | engine `server/` + serf-hub (2) | **stay** top-level | M4 |
| `appprojector` | sole importer is top-level engine `server/` | **stay** top-level | M4 |
| `httpguard` | sole importer is top-level engine `server/` | **stay** top-level | M4 |
| `auth` | imported by **`llm` lib (non-test)** + 3 bins + cmdutil | **stay** top-level | M2 (with `llm`) |
| `diagnostic` | imported by **`agent` lib (non-test)**; §6 duplicate | **stay** top-level | M3 (duplicate) |
| `binresolve` | 2 binaries (hub + tui) | **duplicate** (recommend) or stay | M1 (decision needed) |
| `credentials` | shared via `cmdutil` (all 3 bins) | **stay** top-level (recommend) | M4 (with cmdutil) |

Only **4** packages cleanly move in M1: `appsource` (sink), `launchconfig` (sink, after step 0),
plus the two **contract promotions** `appwire` and `hubapi`. `binresolve`/`credentials` are the two
genuine M1 judgment calls requiring Jesse's choice.

---

## 6. Risk / sequencing vs the in-flight tui-hub refactor

The in-flight tui/hub modularize workflow edits files under `cmd/serf-tui/**` and `cmd/serf-hub/**`
(carving `cmd/serf-{hub,tui}/internal/<pkg>`). M1's import rewrites also rain down on those exact
trees, creating rebase pressure. **Per spec §7, M1 runs AFTER the tui/hub modularize + surface-min
batches land.** Concretely:

**Rewrite footprint that lands in tui/hub files (rebase-collision surface):**

| Package (M1 action) | files rewritten in `cmd/serf-hub/**` | files rewritten in `cmd/serf-tui/**` |
|---------------------|--------------------------------------|--------------------------------------|
| **appwire** (promote) | **39** | **43** |
| **hubapi** (promote) | 7 | 1 (`internal/hubstart/hub_start.go`) |
| **appsource** (sink) | 14 | 0 |
| **launchconfig** (sink) | 7 | 0 |
| **credentials** (if moved) | 8 | 0 |
| **binresolve** (if duplicated) | 1 (`main.go`) | 1 (`internal/hubstart/hub_start.go`) |
| **auth** (stays, but rewrite if moved later) | 8 | 1 (`auth.go`) |
| **appserver** (stays) | 6 | 7 (all `*_test.go`) |
| **diagnostic** (stays) | 3 | 0 |

- **Highest collision risk: `appwire` (82 tui+hub files) and `hubapi` (8).** Both are **contract
  promotions** touching nearly every hub/tui file that speaks the wire/HTTP protocol — exactly the
  files the tui-hub refactor is restructuring. **These two MUST land after tui-hub merges.** A
  promotion is a single global path rewrite (`internal/appwire` → `appwire`), so once tui-hub is in,
  re-deriving the file list with the §3.1/§3.2 greps and re-running `gofmt -r` is mechanical even if
  filenames shifted.
- **`appsource`/`launchconfig` sinks** touch hub-only files (14 + 7). Lower cross-product risk (no
  tui), but still collide with any hub `internal/` carving — sequence after the hub carve settles.
- **`binresolve` duplication** is the one move that edits a **tui** file
  (`cmd/serf-tui/internal/hubstart/hub_start.go`) — and `hubstart` is itself a freshly-carved
  tui-internal package from the in-flight refactor. **Highest single-file rebase hazard**; do
  binresolve **last** and re-confirm `hub_start.go`'s location/imports against post-merge `main`.
- **`appserver` (stays, not moved) still gets its appwire-import rewritten** in 6 hub + 7 tui test
  files via §3.1 — i.e. even "stay" packages incur tui/hub edits because they import `appwire`.
  Factor this into the rebase: the appwire promotion's reach is wider than the packages it moves.

**Sequencing directive:** run M1 in a fresh worktree branched from `origin/main` **only after** the
tui-hub modularize branch and surface-min batch have ff-merged to `main`. Re-run every §3 grep
against that post-merge tree to regenerate the exact file lists before applying rewrites (filenames
under `cmd/serf-{hub,tui}/internal/**` will have moved). Gate each step; ff-merge M1 as one unit.

**Non-tui/hub risk:** the `server/` (engine) files (`appwire_runtime.go`, `appwire_turns.go`,
`server.go`, `server_handlers.go`, + 3 tests) all get the appwire/hubapi rewrites and are **not**
touched by the tui-hub refactor — low collision, but they're the reason `appserver`/`apptranscript`/
`appprojector`/`httpguard` can't sink (see §5). The agent-test cut (step 0) touches only
`agent/session_fallback_test.go` — isolated, no collision.

---

## 7. Post-M1 end state of `internal/`

After M1 (with recommended dispositions: binresolve duplicated, credentials left in place),
top-level `internal/` contains **only**:
`appprojector`, `appserver`, `apptranscript`, `auth`, `credentials`, `diagnostic`, `httpguard`
— every one a documented multi-importer/cross-layer case with a deferred home in M2–M4. The two
contracts (`appwire`, `hubapi`) are top-level; `appsource`, `launchconfig` (and the hub copy of
`binresolve`) are hub-private. No behavior changed; `go build ./... && go vet ./... && go test ./...`
green.
