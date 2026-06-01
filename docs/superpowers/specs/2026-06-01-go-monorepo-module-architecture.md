# Serf Go monorepo — module architecture

Date: 2026-06-01 · Ratified by Jesse.
Status: **executing — M1 ✅ M2 ✅ M3 ✅; M4 next.** This doc is kept **current as each phase lands** — see §1a.

## 1a. Execution status & decisions (living)

**Phases** (see §7 for detail):
- **M1 — de-mix `internal/`** ✅ merged. Promoted `appwire`,`hubapi` → top-level contracts; sank `appsource`,`launchconfig` → `cmd/serf-hub/internal/`. `internal/` went 12 → 8 packages.
- **M2 — carve `llm`** ✅ merged. Carved **`auth`** and **`llm`** into their own modules; established the go.work workspace.
- **M3 — carve `agent`** ⏳ in progress. agent decoupled from the app (now self-contained); module carve next.
- **M4** (root = app module; relocate the remaining `internal/` travelers) and **M5** (per-module hygiene) — pending.

**Decisions made during execution (these AMEND the original plan below):**
- **`auth/openai` = its own module** (`primeradiant.com/serf/auth`), NOT folded into llm. Review-panel call: it's OpenAI-OAuth machinery (login flow + a localhost callback server, ~31 symbols) that the hub drives far more than llm consumes; keeping it separate preserves llm's Pike-grade surface. It sits below llm; llm/agent/apps all depend on it.
- **Build version = injected, never imported.** A library must not import the app's `buildinfo`. `llm` exposes `openai.ClientVersion`; `agent` exposes `agent.BuildVersion` (both default `"dev"`, per-process package-level settings); the serf binaries set them from build info at startup.
- **`frontmatter`, `diagnostic` = duplicated** into `agent/internal/` (the §6 "duplicate" decision) so agent is self-contained; the app keeps the top-level copies.
- **The three binaries stay under `cmd/`** of one app module (Jesse-confirmed): they are one application coupled only by protocols (AppWire subprocess, hubapi HTTP), so module boundaries are reserved for the *libraries* (which have importers), not the run-only binaries.

**go.work mechanics (load-bearing).** `go.work use ./auth ./llm …` ALONE fails to resolve an *unpublished* sibling once a module has external deps — `go build` 404s trying to fetch it. **Fix: versioned `replace … v0.0.0 => ./dir` directives IN `go.work`** (committed, repo-local — invisible to external `go get`), with the go.mod requires at `@v0.0.0`. This keeps the published go.mods replace-free while a fresh clone builds out-of-the-box. `go.work` is committed.

**Correction to §4/§7 below:** the original annotation sinking `appprojector`/`apptranscript`/`appserver` into **serf-hub** is **wrong per the import graph** — all three are imported by the *engine's* top-level `server/` package (and `appprojector` isn't imported by serf-hub at all). They travel with the engine in M4. `binresolve` → duplicate (hub+tui); `credentials` → stays top-level until `cmdutil` dissolves (M4).

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

**DECIDED (Jesse): duplicate.** Each product that needs one of these small utils gets its own
copy in its own `internal/` (agent keeps `agent/internal/{frontmatter,diagnostic}`; the hub/app
gets its own copy under `cmd/<bin>/internal/`). This keeps every product self-contained with no
inter-product util dependency.

- **`frontmatter`** (generic YAML frontmatter parser) — used by `agent` (skills.go) and
  `cmd/serf-hub` → duplicate. **Caveat:** it's a parser the engine and the hub must *agree* on;
  if the copies ever drift (the hub displaying skill metadata that differs from what the engine
  loads), fold it into `agent`'s public surface instead (the apps already depend on `agent`).
- **`diagnostic`** — used by `agent` (diagnostics.go) and apps → duplicate.
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
