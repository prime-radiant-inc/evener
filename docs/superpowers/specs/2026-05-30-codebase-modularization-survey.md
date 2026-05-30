# Codebase modularization survey — cmd/, llm/, server/, internal/, cmdutil/

Date: 2026-05-30 · Companion to PRI-1938 (agent split) / PRI-1940 (agent modularization).
Read-only evaluation by 6 parallel subagents applying the same lens used on `agent`:
god-files, main-vs-library, leaf/cohesion, dependency direction, external-API blast radius.

## The headline

**~60,000 lines of hub + tui + cli logic live in `package main`** — where Go convention says
only a thin entrypoint belongs. Jesse's hypothesis is confirmed with numbers:

| Area | Lines (prod) | In `package main`? | Est. % that should be a library |
| --- | --- | --- | --- |
| `cmd/serf-tui` | ~15,800 | yes | **~80%** (→ `internal/tui` + leaf pkgs) |
| `cmd/serf-hub` | ~10,050 | yes | **~78%** (→ several `internal/hub*` pkgs) |
| `cmd/serf` (CLI) | ~1,850 | yes | **~65–70%** (→ `cmdutil` + `internal/launchcheck`) |

Nothing in the repo imports these `package main` dirs, so extraction has **zero external-caller
breakage** — the only cost is moving the (substantial) co-located tests. `llm/`, `server/`, and
`internal/` are already real library packages and in much better shape; their wins are god-file
splits, dedup, and a few clean leaf extractions.

## Per-area findings

### cmd/serf-hub (~10k prod / ~16k test) — biggest structural debt #2
- **God-files:** `web.go` (3,385 — HTTP layer), `app_rpc.go` (2,002 — RPC wiring + ~1,440 lines of domain logic).
- **Extract to libraries:** `internal/hubspawn` (spawn.go, zero web coupling), `internal/hubindex` (past/roster/prober/session_order), `internal/hubtree` (tree.go — a **pure function**), `internal/hubauth` + `internal/hubinstances`, `internal/hublaunch`, `internal/codexlaunch`, and the domain half of `app_rpc.go` → `internal/hubthread`. ~700–900 lines genuinely stay `main`.
- **Top moves:** P1 `spawn.go`→`internal/hubspawn` (M/low) · P2 `tree.go`→`internal/hubtree` (S/low, pure fn) · P3 `app_rpc.go` domain fns→`internal/hubthread` (L/med — split the `WebConfig` god-struct as part of it) · P7 split `web.go` internally by concern (M/low, same-package).

### cmd/serf-tui (~15.8k prod / ~15k test) — biggest structural debt #1
- **God-file:** `hub_model.go` (**4,557 — largest file in the repo**), the central bubbletea model.
- **The move:** extract essentially everything to `internal/tui` (package rename; `main.go` shrinks to ~150 lines), then decompose `hub_model.go` into ~5 concern files (dashboard / session / spawn / views / core update-loop). Clean leaf packages fall out: `internal/tui/widgets` (~1,800, lipgloss-only), `internal/tui/toolrender` (~1,075), `internal/tui/clipboard` (~680), `internal/hubstart` (564). `tui_samples.go` (551, ships in the binary, test-only callers) → a `_test.go`.
- **Top moves:** P1 `internal/tui` extraction (L/low) · P2 split `hub_model.go` (M/low) · P4–6 leaf widget/toolrender/clipboard packages (S–M/low).

### cmd/serf (CLI, ~1.85k) + cmdutil (558)
- No god-files. ~65–70% of the CLI is logic that should leave `main`.
- **Moves:** `buildInitialProfile`/`applyFastCheapModel`/`agentToServerDetailedStatus`/`drainEvents*` → `cmdutil`; launch-check model logic → `internal/launchcheck` (150 testable lines stuck in `main`); a shared `cmdutil.BuildSession(...)` to kill the ~40-line session-construction pattern duplicated across run.go/serve.go/serfeval.
- **cmdutil is a moderate grab-bag:** `SelectProfile` + `queryModelContextWindow` are pre-`providerconfig` tech debt (migrate `serfeval` off them, then delete); `ResolveSnapshot` is dead code (delete); the hub-only `MaterializeProvidersConfig`/`seedConfigFromEnv` don't belong with CLI-flag helpers.

### llm/ (~11.3k, already structured) — mostly healthy
- **God-files (largely intrinsic):** provider adapters — `openai/adapter.go` (2,357, carries TWO protocol paths: Responses + Chat-Completions), `openaicompat` (1,365), `anthropic` (1,354), `google` (1,043). Internal file-splits (responses.go / chat_completions.go / models.go etc.) improve navigation with zero API change.
- **Real dedup wins:** `stampEndpointURL` is copied verbatim in all 4 adapters → one `StampEndpointURL` (S/low); the 29-field `Request{...}` literal is duplicated in generate.go + stream_generate.go → a `buildRequest` helper (S/low). `media_utils.go` (path/MIME utils, no `llm` type dep) → `internal/mediautil` (S). Model catalog → `llm/catalog` subpackage is clean but high-blast (later).
- **Blast radius:** `llm.Request`/`Response` have ~1,000+ external refs each — touch field shapes with care.

### server/ (~2.4k, clean one-way deps)
- **God-files:** `server.go` (788 — struct + 12 handlers + 24 `Set*` injection methods), `appwire_runtime.go` (816 — RPC handlers + notification-replay reconstruction + snapshot builder).
- **Cleanest win:** `AppEventProjector` (appwire_projection.go, 689) has **no `*Server` dependency** — a pure, well-tested leaf → `internal/appprojector` (S/low). Split `appwire_runtime.go` (S). `security.go` → `internal` (S). Dead code: `reserveAppTurnID`.
- **DTO debt:** a 3-layer `DetailedStatus` stack (`agent.` → `server.` → `appwire.Serf`) forces hand-written fan-out translators; `server.` types could become aliases of `agent.` ones (server already imports agent). The 24 `Set*` methods → a config interface is the long-term direction (L/high, defer).

### internal/ (~9k, 13 subpackages — best-organized area)
- **God-files:** `appsource/codex_source.go` (1,258 — 5 concerns in one file → split codexLiveThread + mapping out, M/low), `appwire/types.go` (884 — cohesive protocol monolith, split only if it becomes a merge-conflict site).
- **Type duplication:** `launchconfig.LaunchOption` ≈ `appwire.LaunchOption` with a 131-line hand-written conversion (`wire.go`) + a 30-line copy loop in the hub — collapse to one type (M/med). `local_daemon.go` mapping helpers → own file (S).
- `appwire` is the **de-facto public API** (32 import sites) — treat its types as a stable contract.

## Cross-cutting roadmap (value × safety)

**Tier A — quick, safe, do-anytime (same-package splits + dedup + dead-code; the proven `agent` Spec-0 pattern):**
- Split the god-files in place: `web.go`, `hub_model.go`, `codex_source.go`, `openai/adapter.go`. Behavior-preserving, no import changes.
- `stampEndpointURL` dedup, `Request` buildRequest dedup, delete `ResolveSnapshot` + `reserveAppTurnID`, `media_utils`→internal.

**Tier B — clean library extractions (leaf-first, like the agent leaf-harvest):**
- `tree.go`→`internal/hubtree` (pure fn), `AppEventProjector`→`internal/appprojector`, `internal/tui/{widgets,toolrender,clipboard}`, `internal/launchcheck`.

**Tier C — the big main→library moves (the hypothesis payoff, per-area, spec'd + gated):**
- `cmd/serf-tui` → `internal/tui` (largest single win), `cmd/serf-hub` → `internal/hub*` (spawn/index/auth/…), `cmd/serf` CLI → cmdutil/`internal/launchcheck`.

**Tier D — deferred (high blast radius, needs design):**
- server `Set*`→interface, `llm/catalog` subpackage, the DTO-layer collapses, the LaunchOption type unification.

**Recommended execution order:** Tier A first (safe, immediate cognitive-load relief, proven pattern),
then Tier B leaf extractions, then Tier C per-area as deliberate spec'd efforts. Each area should get
its own ticket; the `cmd/serf-tui`→`internal/tui` and `cmd/serf-hub` library extractions are large
enough to warrant their own specs (like `agent` got).
