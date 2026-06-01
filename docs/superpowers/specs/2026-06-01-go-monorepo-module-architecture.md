# Serf Go monorepo — module architecture

Date: 2026-06-01 · Ratified by Jesse.
Status: **ratified, not yet executed.** Migration is sequenced after the in-flight refactors land.

## 1. Decision

Serf is a **monorepo of multiple products**, not one application. Restructure it as a
**multi-module Go monorepo**:

- `llm` and `agent` are **published libraries** — consumed by other code **inside and outside
  Prime Radiant**. Each becomes its own Go module so external consumers inherit only the
  library's own dependencies (no Bubble Tea, no hub/tui transitive deps).
- The **serf application** (engine + supervisor + client binaries, plus the wire/HTTP
  contracts between them) is the root module.
- `go.work` ties the modules together for local development; import paths are unchanged
  (the modules keep the `primeradiant.com/serf/...` prefix), so this is **additive**, not a
  repo-wide rename.

This makes "what goes where" **compiler-enforced**: the app module physically cannot reach
`agent/internal/`, and an external consumer physically cannot reach the serf app.

## 2. The products and their boundaries (verified)

| Product | Role | Talks to | via |
| --- | --- | --- | --- |
| `llm` | LLM client library (providers, request/response, streaming) | — | (lowest layer) |
| `agent` | Agent engine + **public persistence schema** (`SessionMeta`, `Turn`, `TranscriptHeader`, `Task`, …) | `llm` | direct |
| `cmd/serf` | **Engine** binary — runs `agent.Session`, serves per-session AppWire/HTTP | agent, llm | direct |
| `cmd/serf-hub` | **Supervisor** — **spawns `serf` subprocesses**, persists metadata, serves clients | serf (spawn), agent (schema only) | **AppWire protocol** + agent schema |
| `cmd/serf-tui` | **Client** — terminal UI | serf-hub | **hubapi** (HTTP) + agent schema |

Verified: serf-hub does `exec.Command(serfBinary, …, "--protocol", appwire.ProtocolVersion)`
(spawn.go) — it does **not** embed the engine; its `agent` usage is schema types only
(`SessionMeta` ×89, `TranscriptHeader`, `Turn`, …), never `NewSession`/`ProcessInput`.
serf-tui uses `hubapi.NewClient(baseURL, httpClient)` and only `agent` schema types for
rendering. The genuinely shared things are the **contracts** (`appwire`, `hubapi`) and
**agent's public schema**.

## 3. The placement rule (the whole point — "what goes where")

Every file answers exactly one question, top to bottom:

1. **Reusable outside this repo?** → a **library module**: `llm/`, `agent/`.
2. **The contract *between* binaries (wire / HTTP protocol)?** → a **contract package** in the
   app module, named for what it is: `appwire/`, `hubapi/`.
3. **Private to exactly one binary?** → that binary's `cmd/<bin>/internal/…`.
4. **A binary entry point?** → `cmd/<bin>/main.go` (thin — wiring only).
5. **A util used by a library *and* an app?** → it belongs to the **lowest module that needs
   it** and is exported there (or is its own tiny module). The app reaches it *through* that
   module's public API — never the other way. (Rare; see §6.)

There is **no top-level `internal/`** in the end state — once each product owns its
`internal/`, there is no "module-wide glue drawer" to mix things in.

## 4. Target layout

```
primeradiant.com/serf/                      (repo root)
├── go.work                                 → ./llm ./agent .   (local dev)
│
├── llm/            module primeradiant.com/serf/llm            PUBLIC LIBRARY
│   ├── go.mod                               (deps: LLM/HTTP only)
│   ├── *.go                                 public API
│   ├── providers/  + providers/internal/{openaichat,transport}
│   └── internal/                            llm-private
│
├── agent/          module primeradiant.com/serf/agent         PUBLIC LIBRARY (requires llm)
│   ├── go.mod                               (deps: llm + minimal)
│   ├── *.go                                 public API incl. the persistence schema
│   ├── events/                              public event-stream subpackage (already carved)
│   └── internal/                            agent-private (agenttest, workspace, promptpath, installid, …)
│
└── (root module: primeradiant.com/serf — the SERF APPLICATION; requires agent, llm)
    ├── go.mod
    ├── appwire/                             CONTRACT: versioned engine↔hub↔tui wire protocol
    ├── hubapi/                              CONTRACT: hub HTTP API (client + server types)
    └── cmd/
        ├── serf/       + internal/          ENGINE
        ├── serf-hub/   + internal/          SUPERVISOR  ← appprojector, apptranscript, appserver,
        │                                                   auth, credentials, binresolve sink here
        ├── serf-tui/   + internal/          CLIENT
        └── llmcall/                         (small llm CLI — its own internal/ if it grows)
```

## 5. Boundaries Go will enforce

- `agent` (module) cannot import the app module → the engine library can never depend on
  serf-app glue. An attempt won't compile.
- The app module imports `agent` + `llm` as **versioned dependencies** (local via `go.work`).
- `cmd/serf-hub/internal/...` is importable only by `cmd/serf-hub` → hub-private code can't
  leak into the tui or the engine.
- `appwire` / `hubapi` are ordinary (non-internal) packages in the app module → all three
  binaries import them as the shared contract; they are the one thing legitimately shared.
- External: `go get primeradiant.com/serf/agent` pulls `agent` + `llm` and nothing else.

## 6. The cross-cutting utilities (the only judgment calls)

A few packages are used by a library **and** an app and so can't sit in app-side `internal/`.
Each needs a one-line placement call, decided by verified usage:

- **`frontmatter`** (generic YAML frontmatter parser) — used by `agent` (skills.go) and
  `cmd/serf-hub`. Options: (a) its own tiny module both require; (b) fold into `agent`'s public
  surface; (c) duplicate (it's small). Recommend (a) if it's reused elsewhere, else (b).
- **`diagnostic`** — used by `agent` (diagnostics.go) and apps. Same options; if apps stop
  needing it directly, it becomes `agent/internal/diagnostic`.
- **`appwire` / `launchconfig` imported by `agent`'s *test*** (`session_fallback_test.go`) — a
  **layering violation**. Must be cut (rewrite the test against agent primitives) before the
  modules can compile, since a library module cannot depend on the app module.
- `buildinfo`, `perf-bench`, `inspo`, `test`, `tools` — get the same once-over: into the owning
  module's `internal/`, or a tooling location; none should be top-level in the end state.

## 7. Migration plan (behavior-preserving, staged, each phase independently gated + mergeable)

**Prerequisite — land the in-flight refactors first** (so the migration moves a quiescent tree):
the tui/hub modularize workflow (carving `cmd/serf-{hub,tui}/internal/<pkg>`) and the surface-min
cleanup batch. The per-product `internal/` carving is itself part of this architecture.

- **M1 — App-side `internal/` de-mix.** Move each app-only top-level `internal/` package into its
  owning binary's `cmd/<bin>/internal/` (appprojector/apptranscript/appserver/auth/credentials/
  binresolve → serf-hub or the owning product per its importers). Keep `appwire`/`hubapi` as
  top-level contract packages. Cut the agent-test layering smell. Gate: build/vet/test green.
- **M2 — `go.work` + carve `llm`.** Add `llm/go.mod` (module `…/serf/llm`) + a root `go.work`.
  `llm` is the lowest layer (no serf-internal deps), so it carves cleanly first. Gate: whole-repo
  build/test under `go.work`.
- **M3 — Carve `agent`.** Add `agent/go.mod` (requires `llm`). Resolve the cross-cutting utils
  (§6) so `agent` has no app dependency. Gate.
- **M4 — Root module = the serf app.** Root `go.mod` requires `agent` + `llm`; holds `appwire`,
  `hubapi`, `cmd/*`. Gate.
- **M5 — Per-module hygiene.** Run `namingcheck`/`internalcheck` per library module; per-module
  CI; doc/READMEs note each module's public surface. Confirm `go get …/agent` from a scratch
  module pulls only agent+llm.

Each phase is a normal worktree → gated → ff-merge unit. No logic changes anywhere; this is
pure relocation + module wiring.

## 8. End state

Three modules, boundaries enforced by the compiler:

- **`llm`** — a clean, independently-consumable LLM client library.
- **`agent`** — a clean agent engine + public schema library (depends only on `llm`).
- **serf app** — three product binaries (engine / supervisor / client) over two real API
  boundaries (AppWire, hubapi), each owning its `internal/`, sharing only the contracts.

A newcomer (or an external consumer) can tell exactly what's public, what's a contract, and
what's private to one product — and the build enforces it.
