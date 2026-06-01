# Serf code architecture & module layout

How the Serf codebase is organized: which modules exist, what each is for, what
may depend on what, and how to build it. This is the **canonical, living** layout
reference — kept current as the structure evolves. (The dated migration spec under
`docs/superpowers/specs/2026-06-01-go-monorepo-module-architecture.md` records the
plan and the decisions behind this structure; this doc describes the result.)

## Shape

Serf is a **multi-module Go monorepo** containing two published libraries, a small
shared auth module, and one application made of three binaries that talk over two
wire contracts.

| Module | Path | Role | May depend on |
| --- | --- | --- | --- |
| `auth` | `auth/` | OpenAI OAuth: token storage + interactive login flow | (stdlib only) |
| `llm` | `llm/` | LLM client library — providers, requests, responses, streaming | `auth` |
| `agent` | `agent/` | Agent engine + the public persistence schema (`SessionMeta`, `Turn`, …) | `llm`, `auth` |
| **app** (root) | `.` | the **serf application** — the three binaries + the wire contracts | `agent`, `llm`, `auth` |

`llm` and `agent` are **published libraries** (consumed inside and outside Prime
Radiant); their `go.mod` files are kept clean and publishable. `auth` is its own
module because it's OAuth machinery (a login flow + a localhost callback server)
that the hub drives far more than `llm` consumes — keeping it separate preserves
`llm`'s minimal surface.

## The three application binaries (one product, three processes)

The app is **one product**: the binaries are coupled only by **protocols**, never by
importing each other's Go code.

| Binary | `cmd/` | Role | Talks to | via |
| --- | --- | --- | --- | --- |
| `serf` | `cmd/serf` | **engine** — runs an `agent.Session` | agent, llm | direct |
| `serf-hub` | `cmd/serf-hub` | **supervisor** — spawns `serf` subprocesses, serves clients | serf (spawn), agent (schema) | **AppWire** + schema |
| `serf-tui` | `cmd/serf-tui` | **client** — terminal dashboard | serf-hub | **hubapi** (HTTP) |

The two shared **contracts** are ordinary top-level packages in the app module:
`appwire/` (the engine↔hub↔tui wire protocol) and `hubapi/` (the hub's HTTP API).
Each binary owns its private code under `cmd/<bin>/internal/`.

## The placement rule — "what goes where"

1. **Reusable outside this repo?** → a library module (`llm/`, `agent/`, `auth/`).
2. **A contract *between* binaries (wire / HTTP)?** → a top-level package in the app
   module, named for what it is (`appwire/`, `hubapi/`).
3. **Private to exactly one binary?** → that binary's `cmd/<bin>/internal/…`.
4. **A binary entry point?** → `cmd/<bin>/main.go` (thin — wiring only).
5. **Shared by a library *and* the app?** → the lowest module that needs it, exported
   there; or its own small module; or (for tiny utils) **duplicated** into each
   product's `internal/`. Libraries never reach into app code.

A library must never import app code or the app's build metadata. Two consequences
already enforced: the build version is **injected**, not imported (`openai.ClientVersion`,
`agent.BuildVersion` — package-level settings the binaries set at startup, default
`"dev"`); and shared parsers (`frontmatter`, `diagnostic`) are **duplicated** into
`agent/internal/` so `agent` is self-contained.

## Building — the go.work workspace

The repo is a Go workspace (`go.work`, committed). `make build` / `make build-hub` /
`make build-tui` / `make build-llmcall` work as before; import paths are unchanged
(`primeradiant.com/serf/…`).

The unpublished sibling modules are wired with **versioned `replace … v0.0.0 => ./dir`
directives in `go.work`** (repo-local — invisible to external `go get`). This keeps
the committed `go.mod` files replace-free and publishable while a fresh clone builds
out-of-the-box. (`go.work use` alone does *not* resolve an unpublished sibling once a
module has external deps — it 404s trying to fetch it; the in-`go.work` replace is the
fix.) In workspace mode `go build`/`go test`/`go vet ./...` operate across all modules.

## Boundaries Go enforces

- `agent` / `llm` (modules) **cannot** import the app — the engine libraries can never
  depend on serf-app glue.
- `cmd/<bin>/internal/…` is importable only by that binary.
- `appwire` / `hubapi` are ordinary packages → all three binaries share them as the
  one legitimately-shared contract tier.
- External: `go get primeradiant.com/serf/agent` pulls `agent` + `llm` + `auth` and
  nothing else.

## Current status

- ✅ `auth`, `llm` carved into their own modules; `go.work` workspace established.
- ⏳ `agent` being carved into its module (decoupled from the app; module wiring in
  progress).
- Pending: the remaining top-level `internal/` packages (`appprojector`, `appserver`,
  `apptranscript`, `httpguard` → with the engine's `server/`; `binresolve` → duplicated
  hub+tui; `credentials` → stays until `cmdutil` dissolves) relocate to their owners as
  the root settles into the app module. See the dated spec for the phase-by-phase plan.
